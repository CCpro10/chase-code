package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	servertools "chase-code/server/tools"
)

const (
	envCwdKey         = "CHASE_CODE_CWD"
	envApprovalPolicy = "CHASE_CODE_APPROVAL_POLICY"
	envSandboxMode    = "CHASE_CODE_SANDBOX_MODE"
	envNetworkAccess  = "CHASE_CODE_NETWORK_ACCESS"
	envShellKey       = "CHASE_CODE_SHELL"
)

// EnvironmentContext describes the runtime context shared with the model.
type EnvironmentContext struct {
	Cwd            string
	ApprovalPolicy string
	SandboxMode    string
	NetworkAccess  string
	Shell          string
}

// DefaultEnvironmentContext builds a context snapshot for the initial prompt.
func DefaultEnvironmentContext() EnvironmentContext {
	return EnvironmentContext{
		Cwd:            firstNonEmpty(readEnv(envCwdKey), getCwd()),
		ApprovalPolicy: firstNonEmpty(readEnv(envApprovalPolicy), "on-request"),
		SandboxMode:    normalizeSandboxMode(firstNonEmpty(readEnv(envSandboxMode), "workspace-write")),
		NetworkAccess:  firstNonEmpty(readEnv(envNetworkAccess), "restricted"),
		Shell:          firstNonEmpty(readEnv(envShellKey), detectShellName()),
	}
}

// FormatEnvironmentContext renders the context as a codex-style XML block.
func FormatEnvironmentContext(ctx EnvironmentContext) string {
	return fmt.Sprintf(
		"<environment_context>\n  <cwd>%s</cwd>\n  <approval_policy>%s</approval_policy>\n  <sandbox_mode>%s</sandbox_mode>\n  <network_access>%s</network_access>\n  <shell>%s</shell>\n</environment_context>",
		escapeEnvValue(ctx.Cwd),
		escapeEnvValue(ctx.ApprovalPolicy),
		escapeEnvValue(ctx.SandboxMode),
		escapeEnvValue(ctx.NetworkAccess),
		escapeEnvValue(ctx.Shell),
	)
}

func readEnv(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func getCwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return cwd
}

func detectShellName() string {
	shell := servertools.DetectUserShell()
	switch shell.Kind {
	case servertools.ShellZsh:
		return "zsh"
	case servertools.ShellBash:
		return "bash"
	case servertools.ShellPwsh:
		return "pwsh"
	}
	if shell.ShellPath != "" {
		return filepath.Base(shell.ShellPath)
	}
	return "unknown"
}

func normalizeSandboxMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "workspace":
		return "workspace-write"
	default:
		return value
	}
}

func escapeEnvValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(value)
}
