package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	servertools "chase-code/server/tools"
)

// Run 是 chase-code 的 CLI 入口，负责解析子命令并调用 server 包的核心逻辑。
// 模仿 codex 的体验：
//   - 直接运行 `chase-code` 时，默认进入基于 agent 的 REPL；
//   - 也可以通过子命令显式调用 shell/read/edit/repl。
func Run() {
	if len(os.Args) < 2 {
		// 无子命令时，默认进入 REPL（agent 优先）。
		if err := runRepl(); err != nil {
			fmt.Fprintf(os.Stderr, "repl 退出: %v\n", err)
			os.Exit(1)
		}
		return
	}

	cmd := os.Args[1]
	switch cmd {
	case "shell":
		if err := runShell(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "shell 命令失败: %v\n", err)
			os.Exit(1)
		}
	case "repl":
		if err := runRepl(); err != nil {
			fmt.Fprintf(os.Stderr, "repl 退出: %v\n", err)
			os.Exit(1)
		}
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	prog := filepath.Base(os.Args[0])
	fmt.Fprintf(os.Stderr, `chase-code: Codex 风格的本地 CLI 原型

用法:
  %[1]s                 # 直接进入 agent REPL（推荐）
  %[1]s shell [选项] -- <shell 命令字符串>
  %[1]s repl

子命令说明:
  (无子命令)          进入基于 LLM+工具的 agent repl，默认使用 /agent 处理输入。
  shell                使用当前用户默认 shell 执行命令，默认启用 login shell。
  repl                 进入交互式终端，在同一工作目录下多轮执行 agent/shell。

示例:
  %[1]s
  %[1]s shell -- "ls -la"
  %[1]s repl
`, prog)
}

// runShell 解析 shell 子命令参数并调用 tools.RunExec。
func runShell(args []string) error {
	result, err := executeShellCommand(args)
	if result == nil {
		return err
	}

	if strings.TrimSpace(result.Output) != "" {
		fmt.Fprint(os.Stdout, result.Output)
		if !strings.HasSuffix(result.Output, "\n") {
			fmt.Fprintln(os.Stdout)
		}
	}
	if result.TimedOut {
		fmt.Fprintf(os.Stderr, "命令超时 (耗时 %s)\n", result.Duration)
	}
	os.Exit(result.ExitCode)
	return nil
}

// shellExecResult 描述一次 shell 命令执行结果。
type shellExecResult struct {
	Output   string
	ExitCode int
	Duration time.Duration
	TimedOut bool
}

// executeShellCommand 执行 shell 子命令并返回结构化结果，供 REPL 复用。
func executeShellCommand(args []string) (*shellExecResult, error) {
	fs := flag.NewFlagSet("shell", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	timeout := fs.Duration("timeout", 10*time.Second, "命令超时时间，如 5s、2m；0 表示不设置超时")
	policyStr := fs.String("policy", string(servertools.SandboxWorkspaceWrite), "沙箱策略: full|readonly|workspace")
	loginShell := fs.Bool("login", true, "是否使用 login shell (-lc/-l) 来执行命令")

	// chase-code shell [flags] -- <command string>
	cmdIndex := -1
	for i, a := range args {
		if a == "--" {
			cmdIndex = i
			break
		}
	}

	var flagArgs, cmdParts []string
	if cmdIndex == -1 {
		flagArgs = args
	} else {
		flagArgs = args[:cmdIndex]
		cmdParts = args[cmdIndex+1:]
	}

	if err := fs.Parse(flagArgs); err != nil {
		return nil, err
	}

	if len(cmdParts) == 0 {
		cmdParts = fs.Args()
	}
	if len(cmdParts) == 0 {
		return nil, fmt.Errorf("缺少要执行的 shell 命令")
	}
	commandStr := strings.Join(cmdParts, " ")

	policy, err := servertools.ParseSandboxPolicy(*policyStr)
	if err != nil {
		return nil, err
	}

	shell := servertools.DetectUserShell()
	if shell.Kind == servertools.ShellUnknown {
		return nil, fmt.Errorf("无法自动检测用户 shell，请显式指定命令，如: bash -lc '...'")
	}

	shellArgs := shell.DeriveExecArgs(commandStr, *loginShell)

	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("获取当前工作目录失败: %w", err)
	}

	params := servertools.ExecParams{
		Command: shellArgs,
		Cwd:     cwd,
		Timeout: *timeout,
		Env:     os.Environ(),
	}

	result, err := servertools.RunExec(params, policy)
	if result == nil {
		return nil, err
	}
	return &shellExecResult{
		Output:   result.Output,
		ExitCode: result.ExitCode,
		Duration: result.Duration,
		TimedOut: result.TimedOut,
	}, err
}
