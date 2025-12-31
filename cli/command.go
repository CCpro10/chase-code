package cli

import "sort"

// CLICommand 定义了所有 CLI 工具/命令必须实现的接口。
type CLICommand interface {
	Name() string
	Aliases() []string
	Description() string
	Help() string
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
