package cli

import (
	"fmt"
	"strings"
)

// formatShellResult 将 shell 执行结果格式化为可显示的多行文本。
func formatShellResult(result *shellExecResult) []string {
	if result == nil {
		return nil
	}
	lines := make([]string, 0, 4)
	output := strings.TrimRight(result.Output, "\n")
	if strings.TrimSpace(output) != "" {
		lines = append(lines, splitLines(output)...)
	}
	if result.TimedOut {
		lines = append(lines, fmt.Sprintf("命令超时 (耗时 %s)", result.Duration))
	}
	if result.ExitCode != 0 {
		lines = append(lines, fmt.Sprintf("退出码: %d", result.ExitCode))
	}
	return lines
}
