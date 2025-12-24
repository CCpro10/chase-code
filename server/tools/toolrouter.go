package tools

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
	Command   string `json:"command"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
	Policy    string `json:"policy,omitempty"`
}

func (r *ToolRouter) execShell(_ context.Context, call ToolCall) (ToolResult, error) {
	var args shellArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return ToolResult{}, fmt.Errorf("解析 shell 参数失败: %w", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return ToolResult{}, fmt.Errorf("shell 工具需要非空 command 字段")
	}

	policy := SandboxWorkspaceWrite
	if args.Policy != "" {
		p, err := ParseSandboxPolicy(args.Policy)
		if err != nil {
			return ToolResult{}, err
		}
		policy = p
	}

	shell := DetectUserShell()
	if shell.Kind == ShellUnknown {
		return ToolResult{}, fmt.Errorf("无法自动检测用户 shell")
	}

	timeout := 10 * time.Second
	if args.TimeoutMs > 0 {
		timeout = time.Duration(args.TimeoutMs) * time.Millisecond
	}

	shellArgs := shell.DeriveExecArgs(args.Command, true)
	cwd, err := os.Getwd()
	if err != nil {
		return ToolResult{}, fmt.Errorf("获取当前工作目录失败: %w", err)
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
	return ToolResult{ToolName: "shell", Output: toolOutput}, nil
}

// ---------------- read_file ----------------

type readFileArgs struct {
	Path     string `json:"path"`
	MaxBytes int    `json:"max_bytes,omitempty"`
}

func (r *ToolRouter) execReadFile(call ToolCall) (ToolResult, error) {
	var args readFileArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return ToolResult{}, fmt.Errorf("解析 read_file 参数失败: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return ToolResult{}, fmt.Errorf("read_file 需要 path 字段")
	}
	maxBytes := args.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 512 * 1024
	}
	data, err := ReadFileLimited(args.Path, maxBytes)
	if err != nil {
		return ToolResult{}, err
	}

	return ToolResult{ToolName: "read_file", Output: string(data)}, nil
}

// ---------------- edit/apply_patch ----------------

type patchFileArgs struct {
	File string `json:"file"`
	From string `json:"from"`
	To   string `json:"to"`
	All  bool   `json:"all,omitempty"`
}

func (r *ToolRouter) execEditFile(call ToolCall) (ToolResult, error) {
	// 保留 edit_file 作为 apply_patch 的别名，方便向后兼容。
	return r.execPatchCommon("edit_file", call)
}

func (r *ToolRouter) execApplyPatch(call ToolCall) (ToolResult, error) {
	return r.execPatchCommon("apply_patch", call)
}

func (r *ToolRouter) execPatchCommon(toolName string, call ToolCall) (ToolResult, error) {
	var args patchFileArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return ToolResult{}, fmt.Errorf("解析 %s 参数失败: %w", toolName, err)
	}
	if strings.TrimSpace(args.File) == "" || strings.TrimSpace(args.From) == "" {
		return ToolResult{}, fmt.Errorf("%s 需要 file 和 from 字段", toolName)
	}

	abs, err := filepath.Abs(args.File)
	if err != nil {
		return ToolResult{}, fmt.Errorf("解析文件路径失败: %w", err)
	}
	if err := ApplyEdit(abs, args.From, args.To, args.All); err != nil {
		return ToolResult{}, err
	}

	msg := fmt.Sprintf("已更新文件: %s", abs)
	return ToolResult{ToolName: toolName, Output: msg}, nil
}

// ---------------- list_dir ----------------

type listDirArgs struct {
	Path string `json:"path"`
}

func (r *ToolRouter) execListDir(call ToolCall) (ToolResult, error) {
	var args listDirArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return ToolResult{}, fmt.Errorf("解析 list_dir 参数失败: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return ToolResult{}, fmt.Errorf("list_dir 需要 path 字段")
	}

	entries, err := os.ReadDir(args.Path)
	if err != nil {
		return ToolResult{}, fmt.Errorf("读取目录失败: %w", err)
	}

	var b strings.Builder
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		fmt.Fprintln(&b, name)
	}

	return ToolResult{ToolName: "list_dir", Output: b.String()}, nil
}

// ---------------- grep_files ----------------

type grepFilesArgs struct {
	Root       string `json:"root"`
	Pattern    string `json:"pattern"`
	MaxMatches int    `json:"max_matches,omitempty"`
}

func (r *ToolRouter) execGrepFiles(call ToolCall) (ToolResult, error) {
	args, err := parseGrepFilesArgs(call.Arguments)
	if err != nil {
		return ToolResult{}, err
	}

	out, err := runRipgrep(args.Root, args.Pattern, args.MaxMatches)
	if err == nil {
		return buildGrepResult(out), nil
	}

	out, err = runGrepFallback(args)
	if err != nil {
		return ToolResult{}, err
	}
	return buildGrepResult(out), nil
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

// parseGrepFilesArgs 解析并标准化 grep_files 参数。
func parseGrepFilesArgs(raw json.RawMessage) (grepFilesArgs, error) {
	var args grepFilesArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return grepFilesArgs{}, fmt.Errorf("解析 grep_files 参数失败: %w", err)
	}
	args.Root = strings.TrimSpace(args.Root)
	if args.Root == "" {
		args.Root = "."
	}
	if strings.TrimSpace(args.Pattern) == "" {
		return grepFilesArgs{}, fmt.Errorf("grep_files 需要非空 pattern 字段")
	}
	if args.MaxMatches <= 0 {
		args.MaxMatches = 200
	}
	return args, nil
}

// runGrepFallback 使用 WalkDir 进行子串匹配，作为 rg 的回退方案。
func runGrepFallback(args grepFilesArgs) (string, error) {
	var (
		b       strings.Builder
		matches int
	)

	walkErr := filepath.WalkDir(args.Root, func(path string, d fs.DirEntry, err error) error {
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
		if matches >= args.MaxMatches {
			return io.EOF
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, io.EOF) {
		return "", fmt.Errorf("遍历目录失败: %w", walkErr)
	}

	if matches == 0 {
		b.WriteString("未找到匹配项")
	}

	return b.String(), nil
}

// buildGrepResult 标准化 grep_files 输出结果。
func buildGrepResult(output string) ToolResult {
	if strings.TrimSpace(output) == "" {
		output = "未找到匹配项"
	}
	return ToolResult{ToolName: "grep_files", Output: output}
}
