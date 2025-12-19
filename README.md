# chase-code

chase-code 是一个模仿 Codex 体验的本地命令行原型工具，用于在本地代码仓库中通过「LLM + 工具」的方式完成开发任务。

它提供了一个简单的 agent REPL，以及一组可被模型调用或直接从命令行使用的工具（shell / read / edit / apply_patch / list_dir / grep_files），方便在本地快速迭代代码。

## 功能特性

- **Agent REPL**：
  - 直接运行 `chase-code` 即进入交互式 REPL，由 LLM 驱动并通过工具完成任务。
- **多种本地工具**（在 `server` 包中实现）：
  - `shell`：执行本地 shell 命令，支持超时与沙箱策略。
  - `read_file` / `read` 子命令：读取文件内容，便于 LLM 理解代码。
  - `edit_file` / `edit` 子命令：基于字符串片段的安全编辑工具，默认要求 `from` 片段在文件中唯一，避免误改多处。
  - `apply_patch`：与 `edit_file` 语义一致，更偏向「补丁」概念的别名。
  - `list_dir`：列出目录内容，辅助浏览工程结构。
  - `grep_files`：在代码中查找包含指定子串或模式的文件（内部使用 ripgrep）。
- **可插拔 LLM 客户端**：
  - 通过环境变量配置不同的模型提供商与模型名称。
  - 当前支持：OpenAI、Kimi（Moonshot，兼容 OpenAI Chat API）。

## 构建与安装

### 依赖

- Go 1.21+（推荐使用与仓库其他模块一致的 Go 版本）。

### 本地构建

在仓库根目录：

```bash
cd chase-code
# 构建可执行文件（默认输出为当前目录下的 `chase-code`）
go build -o chase-code .
```

构建成功后，可以将生成的 `chase-code` 加入到 PATH 中，方便在任意工程目录调用。

## 配置 LLM

LLM 客户端由 `server/llm.go` 中的 `LLMConfig` 与 `NewLLMConfigFromEnv` 管理。

### 公共环境变量

- `CHASE_CODE_LLM_PROVIDER`：
  - 可选值：`openai`（默认）、`kimi`。

### 使用 OpenAI

需要设置：

- `CHASE_CODE_OPENAI_API_KEY`：OpenAI API key（必需）。
- `CHASE_CODE_OPENAI_MODEL`：模型名称（可选，默认 `gpt-4.1-mini`）。
- `CHASE_CODE_OPENAI_BASE_URL`：OpenAI API Base URL（可选，默认 `https://api.openai.com/v1`）。

示例：

```bash
export CHASE_CODE_LLM_PROVIDER=openai
export CHASE_CODE_OPENAI_API_KEY="sk-..."
export CHASE_CODE_OPENAI_MODEL="gpt-4.1-mini"
# 如需自定义反向代理：
# export CHASE_CODE_OPENAI_BASE_URL="https://your-proxy/v1"
```

### 使用 Kimi（Moonshot）

Kimi 兼容 OpenAI Chat Completions 接口，配置方式如下：

- `CHASE_CODE_LLM_PROVIDER=kimi`
- `CHASE_CODE_KIMI_API_KEY` 或 `MOONSHOT_API_KEY`：API key（二者其一即可）。
- `CHASE_CODE_KIMI_MODEL`：模型名称（可选，默认 `kimi-k2-turbo-preview`）。
- `CHASE_CODE_KIMI_BASE_URL`：Base URL（可选，默认 `https://api.moonshot.cn/v1`）。

示例：

```bash
export CHASE_CODE_LLM_PROVIDER=kimi
export CHASE_CODE_KIMI_API_KEY="your-kimi-key"
# 或者
# export MOONSHOT_API_KEY="your-kimi-key"

export CHASE_CODE_KIMI_MODEL="kimi-k2-turbo-preview"
# 可选：
# export CHASE_CODE_KIMI_BASE_URL="https://api.moonshot.cn/v1"
```

## 使用方式

### 1. Agent REPL（推荐）

在希望操作的代码仓库根目录直接运行：

```bash
chase-code
```

- 默认进入基于 LLM + 工具的交互式 REPL。
- 可以多轮对话，让 agent 自动调用 `read_file`、`edit_file`、`shell` 等工具修改代码。

### 2. 子命令模式

`cli/cli.go` 中定义了若干子命令，便于直接在命令行调用核心能力：

```bash
chase-code                 # 进入 agent REPL
chase-code shell [选项] -- <shell 命令字符串>
chase-code read <文件路径>
chase-code edit -file <文件路径> -from <旧串> -to <新串> [-all]
chase-code repl            # 显式进入 REPL
```

常见示例：

```bash
# 以当前目录为工作目录执行一条 shell 命令
chase-code shell -- "ls -la"

# 读取 main.go 内容
chase-code read ./main.go

# 将 main.go 中第一次出现的 foo 替换为 bar（要求 from 片段唯一）
chase-code edit -file main.go -from "foo" -to "bar"

# 将所有 foo 替换为 bar
chase-code edit -file main.go -from "foo" -to "bar" -all
```

## 安全与沙箱

`shell` 工具和 `shell` 子命令内部使用 `server/tools` 包中的沙箱能力：

- 支持通过 `-policy` 或工具参数选择不同策略：`full` / `readonly` / `workspace`。
- 默认策略（在工具模式下）为对当前工作区尽量安全的写入模式，避免对系统其他路径造成影响。

在实际使用中，建议：

- 将 `chase-code` 运行在受控的项目目录内；
- 对高危命令（如删除文件、网络相关操作）做好额外校验或只在受信环境启用。

## 目录结构概览

- `main.go`：程序入口，调用 `cli.Run()`。
- `cli/`：命令行解析与子命令实现（shell/read/edit/repl 等）。
- `server/`：核心能力（LLM 客户端、工具路由、文件编辑、沙箱执行等）。

如需新增子命令或扩展工具集，建议：

- 在 `server` 包中实现具体能力；
- 在 `cli` 包中增加对应子命令或 REPL 集成；
- 保持 `main.go` 简洁，仅负责将控制权交给 `cli.Run()`。
