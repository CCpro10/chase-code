package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ToolRouter 是一个极简版的工具路由器，负责根据 ToolCall 调用本地工具。
// 如需接入远程工具（例如 MCP tools），可以在构造时传入实现了 ToolCaller
// 接口的客户端，当遇到本地未内置的工具名时自动尝试通过该客户端代理调用。

// ToolCaller 抽象了一个可以调用远程工具的客户端，例如 MCPClient。
// 这样 tools 包无需直接依赖具体的 mcp 包，实现解耦。
type ToolCaller interface {
	CallTool(ctx context.Context, name string, arguments json.RawMessage) (string, error)
}

type ToolRouter struct {
	specs map[string]ToolSpec
	// remote 用于代理执行本地未内置的工具（如 MCP server 提供的工具）。
	remote ToolCaller
}

// ToolResult 表示单次工具调用的原始结果，由上层自行封装为 ResponseItem。
type ToolResult struct {
	ToolName string
	Output   string
}

func NewToolRouter(tools []ToolSpec) *ToolRouter {
	m := make(map[string]ToolSpec, len(tools))
	for _, t := range tools {
		m[t.Name] = t
	}
	return &ToolRouter{specs: m}
}

// NewToolRouterWithMCP 在 NewToolRouter 的基础上额外注入一个 ToolCaller，
// 以便在本地未内置某个工具时，能够代理到远程工具服务（例如 MCP server）。
// 这里的参数类型是本包定义的接口，而不是具体的 mcp 包类型，避免包之间循环依赖。
func NewToolRouterWithMCP(tools []ToolSpec, remote ToolCaller) *ToolRouter {
	m := make(map[string]ToolSpec, len(tools))
	for _, t := range tools {
		m[t.Name] = t
	}
	return &ToolRouter{specs: m, remote: remote}
}

func (r *ToolRouter) Specs() []ToolSpec {
	out := make([]ToolSpec, 0, len(r.specs))
	for _, s := range r.specs {
		out = append(out, s)
	}
	return out
}

func (r *ToolRouter) Execute(ctx context.Context, call ToolCall) (ToolResult, error) {
	switch call.ToolName {
	case "shell", "shell_command":
		return r.execShell(ctx, call)
	case "apply_patch":
		return r.execApplyPatch(call)
	default:
		// 若注入了 remote client，则尝试将未知工具代理到远程服务（如 MCP server）。
		if r.remote != nil {
			out, err := r.remote.CallTool(ctx, call.ToolName, call.Arguments)
			if err != nil {
				return ToolResult{}, fmt.Errorf("远程工具 %s 执行失败: %w", call.ToolName, err)
			}
			return ToolResult{ToolName: call.ToolName, Output: out}, nil
		}
		return ToolResult{}, fmt.Errorf("未知工具: %s", call.ToolName)
	}
}

// ---------------- shell ----------------

type shellArgs struct {
	Command            string  `json:"command"`
	Justification      string  `json:"justification,omitempty"`
	Login              *bool   `json:"login,omitempty"`
	SandboxPermissions string  `json:"sandbox_permissions,omitempty"`
	TimeoutMs          float64 `json:"timeout_ms,omitempty"`
	Workdir            string  `json:"workdir,omitempty"`
	Policy             string  `json:"policy,omitempty"`
}

func (r *ToolRouter) execShell(_ context.Context, call ToolCall) (ToolResult, error) {
	var args shellArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return ToolResult{}, fmt.Errorf("解析 %s 参数失败: %w", call.ToolName, err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return ToolResult{}, fmt.Errorf("%s 工具需要非空 command 字段", call.ToolName)
	}

	policy := SandboxWorkspaceWrite
	if args.Policy != "" {
		p, err := ParseSandboxPolicy(args.Policy)
		if err != nil {
			return ToolResult{}, err
		}
		policy = p
	}
	if args.SandboxPermissions == "require_escalated" {
		// TODO: 对接审批/沙箱升级逻辑，目前保持默认策略。
	}

	shell := DetectUserShell()
	if shell.Kind == ShellUnknown {
		return ToolResult{}, fmt.Errorf("无法自动检测用户 shell")
	}

	timeout := 10 * time.Second
	if args.TimeoutMs > 0 {
		timeout = time.Duration(int64(args.TimeoutMs)) * time.Millisecond
	}

	login := true
	if args.Login != nil {
		login = *args.Login
	}
	shellArgs := shell.DeriveExecArgs(args.Command, login)
	cwd, err := os.Getwd()
	if err != nil {
		return ToolResult{}, fmt.Errorf("获取当前工作目录失败: %w", err)
	}
	if strings.TrimSpace(args.Workdir) != "" {
		if filepath.IsAbs(args.Workdir) {
			cwd = args.Workdir
		} else {
			cwd = filepath.Join(cwd, args.Workdir)
		}
	}
	params := ExecParams{Command: shellArgs, Cwd: cwd, Timeout: timeout, Env: os.Environ()}

	res, err := RunExec(params, policy)
	if err != nil {
		return ToolResult{}, err
	}

	// 将命令输出与元信息一起返回给模型，便于后续推理。
	// 约定：先给出原始输出，末尾附加一行 summary。
	output := strings.TrimRight(res.Output, "\n")
	if output == "" {
		output = "(no output)"
	}
	summary := fmt.Sprintf("command=%q exit_code=%d duration=%s timed_out=%v", args.Command, res.ExitCode, res.Duration, res.TimedOut)
	toolOutput := output + "\n---\n" + summary
	return ToolResult{ToolName: call.ToolName, Output: toolOutput}, nil
}

// ---------------- apply_patch ----------------

func (r *ToolRouter) execApplyPatch(call ToolCall) (ToolResult, error) {
	return r.execPatchCommon("apply_patch", call)
}

func (r *ToolRouter) execPatchCommon(toolName string, call ToolCall) (ToolResult, error) {
	req, err := ParseApplyPatchArguments(call.Arguments)
	if err != nil {
		return ToolResult{}, fmt.Errorf("解析 %s 参数失败: %w", toolName, err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return ToolResult{}, fmt.Errorf("获取工作目录失败: %w", err)
	}
	result, err := ApplyPatchText(cwd, req.Patch)
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{ToolName: toolName, Output: formatPatchResultOutput(result)}, nil
}
