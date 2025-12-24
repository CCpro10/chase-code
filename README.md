# chase-code

English | [简体中文](README.zh-CN.md)

chase-code is a local CLI agent inspired by the Codex experience. It helps you solve coding tasks inside a repository by combining LLM reasoning with safe, built-in tools.

## Highlights

- **Agent REPL (Bubble Tea TUI)**: an interactive terminal UI that drives multi-step tasks with tools.
- **Local tools**: `shell`, `read_file`, `edit_file`, `apply_patch`, `list_dir`, `grep_files`.
- **Pluggable LLM providers**: OpenAI and Kimi (Moonshot, OpenAI-compatible).

## Requirements

- Go 1.21+ (align with the Go version used in this repo).

## Build

From the repo root:

```bash
cd chase-code
# Build the binary (outputs ./chase-code)
go build -o chase-code .
```

Optionally add the binary to your `PATH` for convenience.

## Configure LLM

LLM config lives in `server/llm/config.go` (`LLMModel`, `NewLLMModelsFromEnv`).

### Common environment variables

- `CHASE_CODE_LLM_PROVIDER`:
  - `openai` (default)
  - `kimi`

### OpenAI

Required:

- `CHASE_CODE_OPENAI_API_KEY`

Optional:

- `CHASE_CODE_OPENAI_MODEL` (default: `gpt-4.1-mini`)
- `CHASE_CODE_OPENAI_BASE_URL` (default: `https://api.openai.com/v1`)

Example:

```bash
export CHASE_CODE_LLM_PROVIDER=openai
export CHASE_CODE_OPENAI_API_KEY="sk-..."
export CHASE_CODE_OPENAI_MODEL="gpt-4.1-mini"
# Optional proxy/base URL:
# export CHASE_CODE_OPENAI_BASE_URL="https://your-proxy/v1"
```

### Kimi (Moonshot)

Required:

- `CHASE_CODE_KIMI_API_KEY` or `MOONSHOT_API_KEY`

Optional:

- `CHASE_CODE_KIMI_MODEL` (default: `kimi-k2-turbo-preview`)
- `CHASE_CODE_KIMI_BASE_URL` (default: `https://api.moonshot.cn/v1`)

Example:

```bash
export CHASE_CODE_LLM_PROVIDER=kimi
export CHASE_CODE_KIMI_API_KEY="your-kimi-key"
# or
# export MOONSHOT_API_KEY="your-kimi-key"

export CHASE_CODE_KIMI_MODEL="kimi-k2-turbo-preview"
# Optional:
# export CHASE_CODE_KIMI_BASE_URL="https://api.moonshot.cn/v1"
```

## Usage

### 1) Agent REPL (recommended)

Run from any repo you want to work on:

```bash
chase-code
```

### 2) Subcommands

```bash
chase-code                 # Enter the agent REPL
chase-code shell [options] -- <shell command>
chase-code read <file>
chase-code edit -file <file> -from <old> -to <new> [-all]
chase-code repl            # Explicitly enter the REPL
```

Examples:

```bash
# Run a shell command in the current directory
chase-code shell -- "ls -la"

# Read a file
chase-code read ./main.go

# Replace the first occurrence of "foo" with "bar"
chase-code edit -file main.go -from "foo" -to "bar"

# Replace all occurrences of "foo" with "bar"
chase-code edit -file main.go -from "foo" -to "bar" -all
```

## Safety and sandboxing

- The `shell` tool and subcommand use sandbox policies from `server/tools`.
- Supported policies: `full`, `readonly`, `workspace`.
- Default policy is workspace-safe to avoid modifying paths outside the repo.

Recommended usage:

- Run `chase-code` inside a controlled project directory.
- Double-check high-risk commands (delete, network operations) before approval.

## Project layout

- `main.go`: entry point, calls `cli.Run()`.
- `cli/`: command parsing and REPL UI.
- `server/`: core logic (LLM client, tool routing, file edits, sandbox exec).

To extend tools:

- Implement capability in `server/`.
- Add CLI/REPL wiring in `cli/`.
- Keep `main.go` minimal.

## License

Apache-2.0. See `LICENSE`.
