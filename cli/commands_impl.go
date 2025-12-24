package cli

import (
	"fmt"
	"strings"

	"chase-code/server/llm"
)

// ShellCommand 实现 /shell 命令。
type ShellCommand struct{}

func (c *ShellCommand) Name() string        { return "shell" }
func (c *ShellCommand) Aliases() []string   { return nil }
func (c *ShellCommand) Description() string { return "通过用户默认 shell 执行命令" }
func (c *ShellCommand) Help() string {
	return "用法: /shell <命令>\n示例: /shell ls -la"
}

func (c *ShellCommand) Execute(ctx *CommandContext) CommandResult {
	if len(ctx.Args) == 0 {
		return CommandResult{Error: fmt.Errorf("缺少要执行的 shell 命令")}
	}
	result, err := executeShellCommand(ctx.Args)
	if result == nil {
		return CommandResult{Error: err}
	}
	return CommandResult{
		Lines:    formatShellResult(result),
		Error:    err,
		ExitCode: result.ExitCode,
	}
}

// AgentCommand 实现 /agent 命令。
type AgentCommand struct{}

func (c *AgentCommand) Name() string        { return "agent" }
func (c *AgentCommand) Aliases() []string   { return nil }
func (c *AgentCommand) Description() string { return "通过 LLM+工具自动完成任务" }
func (c *AgentCommand) Help() string {
	return "用法: /agent <指令>\n示例: /agent 帮我把 main.go 里的错误修复了"
}

func (c *AgentCommand) Execute(ctx *CommandContext) CommandResult {
	userInput := strings.TrimSpace(strings.Join(ctx.Args, " "))
	if userInput == "" {
		return CommandResult{Error: fmt.Errorf("缺少任务描述")}
	}
	err := startAgentTurn(userInput)
	return CommandResult{Error: err}
}

// ApprovalsCommand 实现 /approvals 命令。
type ApprovalsCommand struct{}

func (c *ApprovalsCommand) Name() string        { return "approvals" }
func (c *ApprovalsCommand) Aliases() []string   { return nil }
func (c *ApprovalsCommand) Description() string { return "查看或设置 apply_patch 审批模式" }
func (c *ApprovalsCommand) Help() string {
	return "用法:\n  /approvals           显示当前模式\n  /approvals auto|ask|approve  设置模式"
}

func (c *ApprovalsCommand) Execute(ctx *CommandContext) CommandResult {
	lines, err := handleApprovalsCommand(ctx.Args)
	return CommandResult{Lines: lines, Error: err}
}

// ApproveCommand 实现 /approve 命令。
type ApproveCommand struct{}

func (c *ApproveCommand) Name() string        { return "approve" }
func (c *ApproveCommand) Aliases() []string   { return nil }
func (c *ApproveCommand) Description() string { return "批准补丁请求" }
func (c *ApproveCommand) Help() string        { return "用法: /approve <请求ID>" }

func (c *ApproveCommand) Execute(ctx *CommandContext) CommandResult {
	lines, err := handleApprovalCommand(ctx.Args, true)
	return CommandResult{Lines: lines, Error: err}
}

// RejectCommand 实现 /reject 命令。
type RejectCommand struct{}

func (c *RejectCommand) Name() string        { return "reject" }
func (c *RejectCommand) Aliases() []string   { return nil }
func (c *RejectCommand) Description() string { return "拒绝补丁请求" }
func (c *RejectCommand) Help() string        { return "用法: /reject <请求ID>" }

func (c *RejectCommand) Execute(ctx *CommandContext) CommandResult {
	lines, err := handleApprovalCommand(ctx.Args, false)
	return CommandResult{Lines: lines, Error: err}
}

// QuitCommand 实现 /quit 命令。
type QuitCommand struct{}

func (c *QuitCommand) Name() string        { return "quit" }
func (c *QuitCommand) Aliases() []string   { return []string{"q", "exit"} }
func (c *QuitCommand) Description() string { return "退出 repl" }
func (c *QuitCommand) Help() string        { return "用法: /quit" }

func (c *QuitCommand) Execute(ctx *CommandContext) CommandResult {
	return CommandResult{Quit: true}
}

// HelpCommand 实现 /help 命令。
type HelpCommand struct{}

func (c *HelpCommand) Name() string        { return "help" }
func (c *HelpCommand) Aliases() []string   { return nil }
func (c *HelpCommand) Description() string { return "显示帮助信息" }
func (c *HelpCommand) Help() string        { return "用法: /help" }

func (c *HelpCommand) Execute(ctx *CommandContext) CommandResult {
	return CommandResult{Lines: replHelpLines()}
}

// ReplCommand 实现 repl 子命令。
type ReplCommand struct{}

func (c *ReplCommand) Name() string        { return "repl" }
func (c *ReplCommand) Aliases() []string   { return nil }
func (c *ReplCommand) Description() string { return "进入交互式终端" }
func (c *ReplCommand) Help() string        { return "用法: chase-code repl" }

func (c *ReplCommand) Execute(ctx *CommandContext) CommandResult {
	err := runRepl()
	return CommandResult{Error: err}
}

// ModelCommand 实现 /model 命令。
type ModelCommand struct{}

func (c *ModelCommand) Name() string        { return "model" }
func (c *ModelCommand) Aliases() []string   { return nil }
func (c *ModelCommand) Description() string { return "查看或切换当前使用的模型" }
func (c *ModelCommand) Help() string {
	return "用法:\n  /model               显示当前模型\n  /model <alias>       切换到指定模型"
}

func (c *ModelCommand) Execute(ctx *CommandContext) CommandResult {
	if len(ctx.Args) == 0 {
		m := llm.GetCurrentModel()
		if m == nil {
			return CommandResult{Lines: []string{"当前未加载任何模型"}}
		}
		return CommandResult{Lines: []string{
			fmt.Sprintf("当前模型: %s (alias: %s)", m.Model, m.Alias),
			fmt.Sprintf("BaseURL: %s", m.BaseURL),
		}}
	}

	alias := ctx.Args[0]
	m, err := llm.FindModel(alias)
	if err != nil {
		return CommandResult{Error: err}
	}

	sess, err := getOrInitReplAgent()
	if err != nil {
		return CommandResult{Error: err}
	}

	// 切换 Session 中的 Client
	sess.session.Client = m.Client
	return CommandResult{Lines: []string{
		fmt.Sprintf("已切换到模型: %s (alias: %s)", m.Model, m.Alias),
	}}
}

func init() {
	Register(&ShellCommand{})
	Register(&AgentCommand{})
	Register(&ApprovalsCommand{})
	Register(&ApproveCommand{})
	Register(&RejectCommand{})
	Register(&QuitCommand{})
	Register(&HelpCommand{})
	Register(&ReplCommand{})
	Register(&ModelCommand{})
}
