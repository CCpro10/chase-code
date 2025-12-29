# chase-code

[English](README.md) | 简体中文

chase-code 是一个模仿 Codex 体验的本地命令行工具，通过「LLM + 工具」完成仓库内的开发任务。

## 功能亮点

- **Agent REPL（Bubble Tea TUI）**：交互式终端 UI，支持多步任务驱动。
- **本地工具**：`shell_command`、`apply_patch`。
- **可插拔 LLM 提供商**：OpenAI、Kimi（Moonshot，兼容 OpenAI 接口）。

## 环境要求

- Go 1.21+（建议与仓库内其他模块保持一致）。

## 构建

在仓库根目录执行：

```bash
cd chase-code
# 构建二进制（输出 ./chase-code）
go build -o chase-code .
```

构建完成后可加入 `PATH` 方便全局调用。

## 配置 LLM

LLM 配置集中在 `server/llm/config.go`（`LLMModel`、`NewLLMModelsFromEnv`）。

### 通用环境变量

- `CHASE_CODE_LLM_PROVIDER`：
  - `openai`（默认）
  - `kimi`

### OpenAI

必需：

- `CHASE_CODE_OPENAI_API_KEY`

可选：

- `CHASE_CODE_OPENAI_MODEL`（默认 `gpt-4.1-mini`）
- `CHASE_CODE_OPENAI_BASE_URL`（默认 `https://api.openai.com/v1`）

示例：

```bash
export CHASE_CODE_LLM_PROVIDER=openai
export CHASE_CODE_OPENAI_API_KEY="sk-..."
export CHASE_CODE_OPENAI_MODEL="gpt-4.1-mini"
# 如需代理：
# export CHASE_CODE_OPENAI_BASE_URL="https://your-proxy/v1"
```

### Kimi（Moonshot）

必需：

- `CHASE_CODE_KIMI_API_KEY` 或 `MOONSHOT_API_KEY`

可选：

- `CHASE_CODE_KIMI_MODEL`（默认 `kimi-k2-turbo-preview`）
- `CHASE_CODE_KIMI_BASE_URL`（默认 `https://api.moonshot.cn/v1`）

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

### 1）Agent REPL（推荐）

在目标工程目录中直接运行：

```bash
chase-code
```

### 2）子命令模式

```bash
chase-code                 # 进入 agent REPL
chase-code shell [选项] -- <shell 命令字符串>
chase-code read <文件路径>
chase-code edit -file <文件路径> -from <旧串> -to <新串> [-all]
chase-code repl            # 显式进入 REPL
```

常见示例：

```bash
# 在当前目录执行命令
chase-code shell -- "ls -la"

# 读取 main.go
chase-code read ./main.go

# 替换第一次出现的 foo 为 bar
chase-code edit -file main.go -from "foo" -to "bar"

# 全量替换 foo 为 bar
chase-code edit -file main.go -from "foo" -to "bar" -all
```

## 安全与沙箱

- `shell` 工具与子命令使用 `server/tools` 提供的沙箱策略。
- 支持策略：`full`、`readonly`、`workspace`。
- 默认采用 workspace 级别限制，避免误改仓库外路径。

建议：

- 在受控的项目目录内运行。
- 对删除/网络等高风险命令保持审慎。

## 目录结构

- `main.go`：入口，调用 `cli.Run()`。
- `cli/`：命令行解析与 REPL UI。
- `server/`：核心能力（LLM 客户端、工具路由、文件编辑、沙箱执行）。

扩展建议：

- 在 `server/` 实现能力。
- 在 `cli/` 衔接子命令与 REPL。
- 保持 `main.go` 简洁。

## License

Apache-2.0，详见 `LICENSE`。
