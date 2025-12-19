package server

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type ShellKind string

const (
	ShellZsh    ShellKind = "zsh"
	ShellBash   ShellKind = "bash"
	ShellPwsh   ShellKind = "pwsh"
	ShellUnknown ShellKind = "unknown"
)

type Shell struct {
	Kind       ShellKind
	ShellPath  string
	ProfilePath string
}

func DetectUserShell() Shell {
	if runtime.GOOS == "windows" {
		return Shell{Kind: ShellPwsh, ShellPath: "pwsh.exe"}
	}

	if shellPath := os.Getenv("SHELL"); shellPath != "" {
		return normalizeUnixShell(shellPath)
	}

	if fileExists("/bin/zsh") {
		return Shell{Kind: ShellZsh, ShellPath: "/bin/zsh", ProfilePath: filepath.Join(userHomeDir(), ".zshrc")}
	}
	if fileExists("/bin/bash") {
		return Shell{Kind: ShellBash, ShellPath: "/bin/bash", ProfilePath: filepath.Join(userHomeDir(), ".bashrc")}
	}

	return Shell{Kind: ShellUnknown}
}

func normalizeUnixShell(shellPath string) Shell {
	kind := ShellUnknown
	base := filepath.Base(shellPath)
	switch base {
	case "zsh":
		kind = ShellZsh
	case "bash":
		kind = ShellBash
	default:
		kind = ShellUnknown
	}

	home := userHomeDir()
	profile := ""
	if kind == ShellZsh {
		profile = filepath.Join(home, ".zshrc")
	} else if kind == ShellBash {
		profile = filepath.Join(home, ".bashrc")
	}

	return Shell{Kind: kind, ShellPath: shellPath, ProfilePath: profile}
}

func (s Shell) DeriveExecArgs(command string, useLoginShell bool) []string {
	if s.Kind == ShellPwsh {
		args := []string{s.ShellPath, "-NoLogo"}
		args = append(args, "-NoProfile", "-Command", command)
		return args
	}

	if s.Kind == ShellZsh || s.Kind == ShellBash {
		arg := "-c"
		if useLoginShell {
			arg = "-lc"
		}
		return []string{s.ShellPath, arg, command}
	}

	parts := fieldsOrSingle(command)
	return parts
}

func userHomeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func fieldsOrSingle(s string) []string {
	parts := strings.Fields(s)
	if len(parts) == 0 {
		return []string{s}
	}
	return parts
}
