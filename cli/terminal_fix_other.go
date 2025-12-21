//go:build !darwin && !windows

package cli

import (
	"fmt"
	"os"
)

// setTerminalLineMode 非 macOS 环境下退回使用 stty 修复终端配置。
func setTerminalLineMode(tty *os.File) error {
	if tty == nil {
		return fmt.Errorf("终端为空")
	}
	return applySttyLineMode(tty)
}
