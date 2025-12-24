package cli

import (
	"context"
	"sort"
)

// CommandContext 包含执行命令所需的上下文信息。
type CommandContext struct {
	Context           context.Context
	Args              []string
	PendingApprovalID string
}

// CommandResult 包含命令执行的结果，用于 REPL 渲染或 CLI 输出。
type CommandResult struct {
	Lines    []string
	Quit     bool
	Error    error
	ExitCode int // 供顶级子命令（如 shell）使用
}

// CLICommand 定义了所有 CLI 工具/命令必须实现的接口。
type CLICommand interface {
	Name() string
	Aliases() []string
	Description() string
	Help() string
	Execute(ctx *CommandContext) CommandResult
}

// Registry 负责管理所有的 CLI 命令。
type Registry struct {
	commands map[string]CLICommand
}

func NewRegistry() *Registry {
	return &Registry{
		commands: make(map[string]CLICommand),
	}
}

func (r *Registry) Register(cmd CLICommand) {
	r.commands[cmd.Name()] = cmd
	for _, alias := range cmd.Aliases() {
		r.commands[alias] = cmd
	}
}

func (r *Registry) Get(name string) CLICommand {
	return r.commands[name]
}

func (r *Registry) List() []CLICommand {
	var cmds []CLICommand
	seen := make(map[CLICommand]bool)
	for _, cmd := range r.commands {
		if !seen[cmd] {
			cmds = append(cmds, cmd)
			seen[cmd] = true
		}
	}

	sort.Slice(cmds, func(i, j int) bool {
		return cmds[i].Name() < cmds[j].Name()
	})

	return cmds
}

var globalRegistry = NewRegistry()

func Register(cmd CLICommand) {
	globalRegistry.Register(cmd)
}

func GetCommand(name string) CLICommand {
	return globalRegistry.Get(name)
}

func ListCommands() []CLICommand {
	return globalRegistry.List()
}
