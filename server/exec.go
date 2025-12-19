package server

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
	if len(p.Command) == 0 {
		return nil, errors.New("RunExec: command 为空")
	}

	ctx := context.Background()
	if p.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, p.Command[0], p.Command[1:]...)
	if p.Cwd != "" {
		cmd.Dir = p.Cwd
	}
	if len(p.Env) > 0 {
		cmd.Env = p.Env
	}

	cmd.Stdout = StdoutColored
	cmd.Stderr = StderrColored

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	result := &ExecResult{Duration: dur}

	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.ExitCode = 124
		return result, fmt.Errorf("命令执行超时 (%s)", p.Timeout)
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				result.ExitCode = status.ExitStatus()
				return result, err
			}
		}
		result.ExitCode = -1
		return result, err
	}

	result.ExitCode = 0
	return result, nil
}
