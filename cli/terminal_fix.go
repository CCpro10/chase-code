package cli

import (
	"log"
	"os"
	"runtime"
	"strings"
)

// ensureTerminalEraseKey 确保交互式终端的退格键行为正常。
// 背景：部分进程会把终端切到非行模式/关闭回显，导致退格显示 ^? 且无法删除。
func ensureTerminalEraseKey() {
	if runtime.GOOS == "windows" {
		return
	}
	tty, cleanup, err := openReplTTY()
	if err != nil {
		log.Printf("[repl] 获取终端失败: %v", err)
		return
	}
	if tty == nil {
		return
	}
	if cleanup != nil {
		defer cleanup()
	}
	if err := setTerminalLineMode(tty); err != nil {
		log.Printf("[repl] 设置终端退格键失败: %v", err)
	}
}

// openReplTTY 返回可用于终端配置的文件句柄。
// 优先使用 /dev/tty，避免 stdin 被重定向导致配置落空。
func openReplTTY() (*os.File, func(), error) {
	if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		if isTerminal(tty) {
			return tty, func() { _ = tty.Close() }, nil
		}
		_ = tty.Close()
	}
	if isTerminal(os.Stdin) {
		return os.Stdin, nil, nil
	}
	return nil, nil, nil
}

// isTerminal 判断文件描述符是否为交互式终端。
func isTerminal(file *os.File) bool {
	if file == nil {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// resolveEraseKeyString 返回 stty 期望的退格键表示，支持通过环境变量覆盖。
// 典型取值：^?（DEL）或 ^H（BS）。
func resolveEraseKeyString() string {
	if value := strings.TrimSpace(os.Getenv("CHASE_CODE_ERASE_KEY")); value != "" {
		return value
	}
	return "^?"
}

// resolveEraseKeyByte 将退格键字符串转换为控制字符，供 termios 使用。
func resolveEraseKeyByte() byte {
	raw := strings.TrimSpace(resolveEraseKeyString())
	switch strings.ToUpper(raw) {
	case "^?", "DEL":
		return 0x7f
	case "^H", "BS":
		return 0x08
	}
	if len(raw) == 1 {
		return raw[0]
	}
	return 0x7f
}
