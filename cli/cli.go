package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"chase-code/server"
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
	case "read":
		if err := runRead(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "read 命令失败: %v\n", err)
			os.Exit(1)
		}
	case "edit":
		if err := runEdit(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "edit 命令失败: %v\n", err)
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
  %[1]s read <文件路径>
  %[1]s edit -file <文件路径> -from <旧串> -to <新串> [-all]
  %[1]s repl

子命令说明:
  (无子命令)          进入基于 LLM+工具的 agent repl，默认使用 :agent 处理输入。
  shell                使用当前用户默认 shell 执行命令，默认启用 login shell。
  read                 读取并打印代码文件内容，便于后续由 LLM 理解。
  edit                 通过简单字符串替换编辑文件，模拟 Edit 工具能力。
  repl                 进入交互式终端，在同一工作目录下多轮执行 agent/shell/read/edit。

示例:
  %[1]s
  %[1]s shell -- "ls -la"
  %[1]s read ./main.go
  %[1]s edit -file main.go -from "foo" -to "bar" -all
  %[1]s repl
`, prog)
}

// runShell 解析 shell 子命令参数并调用 server.RunExec。
func runShell(args []string) error {
	fs := flag.NewFlagSet("shell", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	timeout := fs.Duration("timeout", 10*time.Second, "命令超时时间，如 5s、2m；0 表示不设置超时")
	policyStr := fs.String("policy", string(server.SandboxWorkspaceWrite), "沙箱策略: full|readonly|workspace")
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
		return err
	}

	if len(cmdParts) == 0 {
		cmdParts = fs.Args()
	}
	if len(cmdParts) == 0 {
		return fmt.Errorf("缺少要执行的 shell 命令")
	}
	commandStr := strings.Join(cmdParts, " ")

	policy, err := server.ParseSandboxPolicy(*policyStr)
	if err != nil {
		return err
	}

	shell := server.DetectUserShell()
	if shell.Kind == server.ShellUnknown {
		return fmt.Errorf("无法自动检测用户 shell，请显式指定命令，如: bash -lc '...'")
	}

	shellArgs := shell.DeriveExecArgs(commandStr, *loginShell)

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("获取当前工作目录失败: %w", err)
	}

	params := server.ExecParams{
		Command: shellArgs,
		Cwd:     cwd,
		Timeout: *timeout,
		Env:     os.Environ(),
	}

	result, err := server.RunExec(params, policy)
	if err != nil {
		return err
	}

	if result != nil {
		if result.TimedOut {
			fmt.Fprintf(os.Stderr, "命令超时 (耗时 %s)\n", result.Duration)
		}
		os.Exit(result.ExitCode)
	}
	return nil
}

// runRead 使用 server.ReadFileLimited 读取文件内容。
func runRead(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("用法: chase-code read <文件路径>")
	}
	path := args[0]

	data, err := server.ReadFileLimited(path, 512*1024)
	if err != nil {
		return err
	}

	os.Stdout.Write(data)
	if !strings.HasSuffix(string(data), "\n") {
		fmt.Println()
	}
	return nil
}

// runEdit 使用 server.ApplyEdit 模拟 Edit 工具对代码的修改能力。
func runEdit(args []string) error {
	fs := flag.NewFlagSet("edit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	file := fs.String("file", "", "要编辑的文件路径")
	from := fs.String("from", "", "需要替换的旧字符串")
	to := fs.String("to", "", "替换后的新字符串")
	replaceAll := fs.Bool("all", false, "是否替换文件中出现的所有旧字符串（默认只替换第一个）")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *file == "" || *from == "" {
		return fmt.Errorf("edit 需要 -file 和 -from 参数，-to 为空时等价于删除旧字符串")
	}

	abs, err := filepath.Abs(*file)
	if err != nil {
		return fmt.Errorf("解析文件路径失败: %w", err)
	}

	if err := server.ApplyEdit(abs, *from, *to, *replaceAll); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "已更新文件: %s\n", abs)
	return nil
}
