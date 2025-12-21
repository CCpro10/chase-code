//go:build windows

package cli

import (
	"fmt"
	"os"
)

// setTerminalLineMode Windows 下无需处理，直接返回。
func setTerminalLineMode(_ *os.File) error {
	return fmt.Errorf("windows 下不支持 stty/termios")
}
