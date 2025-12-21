package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type SandboxPolicy string

const (
	SandboxFullAccess     SandboxPolicy = "full"
	SandboxReadOnly       SandboxPolicy = "readonly"
	SandboxWorkspaceWrite SandboxPolicy = "workspace"
)

func ParseSandboxPolicy(s string) (SandboxPolicy, error) {
	switch s {
	case string(SandboxFullAccess), "danger", "DangerFullAccess":
		return SandboxFullAccess, nil
	case string(SandboxReadOnly), "read-only", "ReadOnly":
		return SandboxReadOnly, nil
	case string(SandboxWorkspaceWrite), "workspace-write", "WorkspaceWrite":
		return SandboxWorkspaceWrite, nil
	default:
		return "", fmt.Errorf("未知沙箱策略: %q", s)
	}
}

type ExecParams struct {
	Command []string
	Cwd     string
	Timeout time.Duration
	Env     []string
}

type ExecResult struct {
	ExitCode int
	Duration time.Duration
	TimedOut bool
	// Output 为本次执行收集到的 stdout/stderr 文本，主要用于反馈给 LLM。
	// CLI 模式下依然通过 StdoutColored/StderrColored 实时输出到终端。
	Output string
}

func RunExec(p ExecParams, _ SandboxPolicy) (*ExecResult, error) {
	if err := validateExecParams(p); err != nil {
		return nil, err
	}

	ctx, cancel := buildExecContext(p.Timeout)
	if cancel != nil {
		defer cancel()
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd := buildExecCommand(ctx, p, &stdoutBuf, &stderrBuf)
	return runExecCommand(ctx, cmd, p.Timeout, &stdoutBuf, &stderrBuf)
}

// validateExecParams 校验执行参数的合法性。
func validateExecParams(p ExecParams) error {
	if len(p.Command) == 0 {
		return errors.New("RunExec: command 为空")
	}
	return nil
}

// buildExecContext 根据超时参数构造上下文。
func buildExecContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.Background(), nil
	}
	return context.WithTimeout(context.Background(), timeout)
}

// buildExecCommand 构造待执行的命令对象。
func buildExecCommand(ctx context.Context, p ExecParams, stdoutBuf, stderrBuf *bytes.Buffer) *exec.Cmd {
	cmd := exec.CommandContext(ctx, p.Command[0], p.Command[1:]...)
	if p.Cwd != "" {
		cmd.Dir = p.Cwd
	}
	if len(p.Env) > 0 {
		cmd.Env = p.Env
	}
	// 仅写入缓冲区，由调用方决定是否、如何将输出写给用户。
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf
	return cmd
}

// runExecCommand 执行命令并解析退出状态。
func runExecCommand(ctx context.Context, cmd *exec.Cmd, timeout time.Duration, stdoutBuf, stderrBuf *bytes.Buffer) (*ExecResult, error) {
	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	// 将 stdout/stderr 合并为一段文本返回给调用方，方便 LLM 使用。
	mergedOutput := mergeExecOutput(stdoutBuf.String(), stderrBuf.String())
	result := &ExecResult{Duration: dur, Output: mergedOutput}

	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.ExitCode = 124
		return result, fmt.Errorf("命令执行超时 (%s)", timeout)
	}

	if err != nil {
		result.ExitCode = exitCodeFromError(err)
		return result, err
	}

	result.ExitCode = 0
	return result, nil
}

// mergeExecOutput 将 stdout/stderr 文本合并为一段可读性较好的输出。
// 目前策略较为简单：优先展示 stdout，若 stderr 非空则附加一个分隔标记。
func mergeExecOutput(stdout, stderr string) string {
	stdout = strings.TrimRight(stdout, "\n")
	stderr = strings.TrimRight(stderr, "\n")

	if stdout == "" && stderr == "" {
		return ""
	}
	if stdout == "" {
		return stderr
	}
	if stderr == "" {
		return stdout
	}

	return stdout + "\n--- stderr ---\n" + stderr
}

// exitCodeFromError 从 exec 错误中提取退出码。
func exitCodeFromError(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return status.ExitStatus()
		}
	}
	return -1
}
