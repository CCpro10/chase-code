package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ToolRouter 是一个极简版的工具路由器，负责根据 ToolCall 调用本地工具。
// 如需接入 MCP tools，可以在构造时传入 MCPClient，当遇到本地未内置的工具名时
// 自动尝试通过 MCPClient 代理调用。
type ToolRouter struct {
	specs map[string]ToolSpec
	mcp   MCPClient
}

func NewToolRouter(tools []ToolSpec) *ToolRouter {
	m := make(map[string]ToolSpec, len(tools))
	for _, t := range tools {
		m[t.Name] = t
	}
	return &ToolRouter{specs: m}
}

// NewToolRouterWithMCP 在 NewToolRouter 的基础上额外注入一个 MCPClient，
// 以便在本地未内置某个工具时，能够代理到 MCP server。
func NewToolRouterWithMCP(tools []ToolSpec, mcp MCPClient) *ToolRouter {
	m := make(map[string]ToolSpec, len(tools))
	for _, t := range tools {
		m[t.Name] = t
	}
	return &ToolRouter{specs: m, mcp: mcp}
}

func (r *ToolRouter) Specs() []ToolSpec {
	out := make([]ToolSpec, 0, len(r.specs))
	for _, s := range r.specs {
		out = append(out, s)
	}
	return out
}

func (r *ToolRouter) Execute(ctx context.Context, call ToolCall) (ResponseItem, error) {
	switch call.ToolName {
	case "shell":
		return r.execShell(ctx, call)
	case "read_file":
		return r.execReadFile(call)
	case "edit_file":
		return r.execEditFile(call)
	case "list_dir":
		return r.execListDir(call)
	case "grep_files":
		return r.execGrepFiles(call)
	case "apply_patch":
		return r.execApplyPatch(call)
	default:
		// 若注入了 MCPClient，则尝试将未知工具代理到 MCP server。
		if r.mcp != nil {
			out, err := r.mcp.CallTool(ctx, call.ToolName, call.Arguments)
			if err != nil {
				return ResponseItem{}, fmt.Errorf("MCP 工具 %s 执行失败: %w", call.ToolName, err)
			}
			return ResponseItem{
				Type:       ResponseItemToolResult,
				ToolName:   call.ToolName,
				ToolOutput: out,
			}, nil
		}
		return ResponseItem{}, fmt.Errorf("未知工具: %s", call.ToolName)
	}
}

// ---------------- shell ----------------

type shellArgs struct {
	Command   string `json:"command"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
	Policy    string `json:"policy,omitempty"`
}

func (r *ToolRouter) execShell(_ context.Context, call ToolCall) (ResponseItem, error) {
	var args shellArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return ResponseItem{}, fmt.Errorf("解析 shell 参数失败: %w", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return ResponseItem{}, fmt.Errorf("shell 工具需要非空 command 字段")
	}

	policy := SandboxWorkspaceWrite
	if args.Policy != "" {
		p, err := ParseSandboxPolicy(args.Policy)
		if err != nil {
			return ResponseItem{}, err
		}
		policy = p
	}

	shell := DetectUserShell()
	if shell.Kind == ShellUnknown {
		return ResponseItem{}, fmt.Errorf("无法自动检测用户 shell")
	}

	timeout := 10 * time.Second
	if args.TimeoutMs > 0 {
		timeout = time.Duration(args.TimeoutMs) * time.Millisecond
	}

	shellArgs := shell.DeriveExecArgs(args.Command, true)
	cwd, err := os.Getwd()
	if err != nil {
		return ResponseItem{}, fmt.Errorf("获取当前工作目录失败: %w", err)
	}
	params := ExecParams{Command: shellArgs, Cwd: cwd, Timeout: timeout, Env: os.Environ()}

	res, err := RunExec(params, policy)
	if err != nil {
		return ResponseItem{}, err
	}

	summary := fmt.Sprintf("command=%q exit_code=%d duration=%s timed_out=%v", args.Command, res.ExitCode, res.Duration, res.TimedOut)
	return ResponseItem{Type: ResponseItemToolResult, ToolName: "shell", ToolOutput: summary}, nil
}

// ---------------- read_file ----------------

type readFileArgs struct {
	Path     string `json:"path"`
	MaxBytes int    `json:"max_bytes,omitempty"`
}

func (r *ToolRouter) execReadFile(call ToolCall) (ResponseItem, error) {
	var args readFileArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return ResponseItem{}, fmt.Errorf("解析 read_file 参数失败: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return ResponseItem{}, fmt.Errorf("read_file 需要 path 字段")
	}
	maxBytes := args.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 512 * 1024
	}
	data, err := ReadFileLimited(args.Path, maxBytes)
	if err != nil {
		return ResponseItem{}, err
	}

	return ResponseItem{Type: ResponseItemToolResult, ToolName: "read_file", ToolOutput: string(data)}, nil
}

// ---------------- edit/apply_patch ----------------

type patchFileArgs struct {
	File string `json:"file"`
	From string `json:"from"`
	To   string `json:"to"`
	All  bool   `json:"all,omitempty"`
}

func (r *ToolRouter) execEditFile(call ToolCall) (ResponseItem, error) {
	// 保留 edit_file 作为 apply_patch 的别名，方便向后兼容。
	return r.execPatchCommon("edit_file", call)
}

func (r *ToolRouter) execApplyPatch(call ToolCall) (ResponseItem, error) {
	return r.execPatchCommon("apply_patch", call)
}

func (r *ToolRouter) execPatchCommon(toolName string, call ToolCall) (ResponseItem, error) {
	var args patchFileArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return ResponseItem{}, fmt.Errorf("解析 %s 参数失败: %w", toolName, err)
	}
	if strings.TrimSpace(args.File) == "" || strings.TrimSpace(args.From) == "" {
		return ResponseItem{}, fmt.Errorf("%s 需要 file 和 from 字段", toolName)
	}

	abs, err := filepath.Abs(args.File)
	if err != nil {
		return ResponseItem{}, fmt.Errorf("解析文件路径失败: %w", err)
	}
	if err := ApplyEdit(abs, args.From, args.To, args.All); err != nil {
		return ResponseItem{}, err
	}

	msg := fmt.Sprintf("已更新文件: %s", abs)
	return ResponseItem{Type: ResponseItemToolResult, ToolName: toolName, ToolOutput: msg}, nil
}

// ---------------- list_dir ----------------

type listDirArgs struct {
	Path string `json:"path"`
}

func (r *ToolRouter) execGrepFiles(call ToolCall) (ResponseItem, error) {
	var args grepFilesArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return ResponseItem{}, fmt.Errorf("解析 grep_files 参数失败: %w", err)
	}
	root := strings.TrimSpace(args.Root)
	if root == "" {
		root = "."
	}
	if strings.TrimSpace(args.Pattern) == "" {
		return ResponseItem{}, fmt.Errorf("grep_files 需要非空 pattern 字段")
	}
	maxMatches := args.MaxMatches
	if maxMatches <= 0 {
		maxMatches = 200
	}

	// 优先尝试使用 ripgrep，如果系统中安装了 rg，则直接利用 rg 的搜索能力，
	// 以便在大仓库中快速模糊/正则查找代码（模仿 codex 的用法）。
	out, err := runRipgrep(root, args.Pattern, maxMatches)
	if err == nil {
		if strings.TrimSpace(out) == "" {
			out = "未找到匹配项"
		}
		return ResponseItem{Type: ResponseItemToolResult, ToolName: "grep_files", ToolOutput: out}, nil
	}

	// 如果 rg 不存在或调用失败，则回退到原来的 Go WalkDir + 子串搜索实现，
	// 以保证工具在没有 ripgrep 的环境中仍然可用。

	var (
		b       strings.Builder
		matches int
	)

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if !strings.Contains(string(data), args.Pattern) {
			return nil
		}
		fmt.Fprintf(&b, "%s: 包含子串 %q\n", path, args.Pattern)
		matches++
		if matches >= maxMatches {
			return io.EOF
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, io.EOF) {
		return ResponseItem{}, fmt.Errorf("遍历目录失败: %w", walkErr)
	}

	if matches == 0 {
		b.WriteString("未找到匹配项")
	}

	return ResponseItem{Type: ResponseItemToolResult, ToolName: "grep_files", ToolOutput: b.String()}, nil
}

// runRipgrep 使用 rg(1) 在 root 下搜索 pattern，并限制返回的匹配行数。
// 如果 rg 不存在或调用失败，返回错误，由调用方决定是否回退。
func runRipgrep(root, pattern string, maxMatches int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	args := []string{"--hidden", "--line-number", "--no-heading", "--color", "never", "-S"}
	if maxMatches > 0 {
		args = append(args, "-m", strconv.Itoa(maxMatches))
	}
	args = append(args, pattern, root)

	cmd := exec.CommandContext(ctx, "rg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// 对于 "未找到匹配" 的情况，rg 会返回退出码 1；这不视为错误，
		// 而是返回空字符串，由上层统一处理为 "未找到匹配项"。
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 1 {
				return string(out), nil
			}
		}
		return "", err
	}
	return string(out), nil
}

// ---------------- apply_patch ----------------

type applyPatchArgs struct {
	File string `json:"file"`
	From string `json:"from"`
	To   string `json:"to"`
	All  bool   `json:"all,omitempty"`
}

func (r *ToolRouter) execApplyPatch(call ToolCall) (ResponseItem, error) {
	var args applyPatchArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return ResponseItem{}, fmt.Errorf("解析 apply_patch 参数失败: %w", err)
	}
	if strings.TrimSpace(args.File) == "" || strings.TrimSpace(args.From) == "" {
		return ResponseItem{}, fmt.Errorf("apply_patch 需要 file 和 from 字段")
	}

	abs, err := filepath.Abs(args.File)
	if err != nil {
		return ResponseItem{}, fmt.Errorf("解析文件路径失败: %w", err)
	}
	if err := ApplyEdit(abs, args.From, args.To, args.All); err != nil {
		return ResponseItem{}, err
	}

	msg := fmt.Sprintf("apply_patch 已更新文件: %s", abs)
	return ResponseItem{Type: ResponseItemToolResult, ToolName: "apply_patch", ToolOutput: msg}, nil
}
