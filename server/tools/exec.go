package tools

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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
}

func RunExec(p ExecParams, _ SandboxPolicy) (*ExecResult, error) {
	if err := validateExecParams(p); err != nil {
		return nil, err
	}

	ctx, cancel := buildExecContext(p.Timeout)
	if cancel != nil {
		defer cancel()
	}

	cmd := buildExecCommand(ctx, p)
	return runExecCommand(ctx, cmd, p.Timeout)
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
func buildExecCommand(ctx context.Context, p ExecParams) *exec.Cmd {
	cmd := exec.CommandContext(ctx, p.Command[0], p.Command[1:]...)
	if p.Cwd != "" {
		cmd.Dir = p.Cwd
	}
	if len(p.Env) > 0 {
		cmd.Env = p.Env
	}
	cmd.Stdout = StdoutColored
	cmd.Stderr = StderrColored
	return cmd
}

// runExecCommand 执行命令并解析退出状态。
func runExecCommand(ctx context.Context, cmd *exec.Cmd, timeout time.Duration) (*ExecResult, error) {
	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	result := &ExecResult{Duration: dur}

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
