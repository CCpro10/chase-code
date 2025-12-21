//go:build darwin

package cli

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// setTerminalLineMode 使用 termios 直接修复终端行模式与退格键配置。
// macOS 下 termios 更可靠，可避免 stty 调用失败或未生效。
func setTerminalLineMode(tty *os.File) error {
	if tty == nil {
		return fmt.Errorf("终端为空")
	}
	if err := applyTermiosLineMode(tty); err == nil {
		return nil
	}
	return applySttyLineMode(tty)
}

// applyTermiosLineMode 调整终端行模式与退格键行为。
// ICANON+ECHO 恢复行编辑；ECHOCTL 关闭控制字符回显；VERASE 设置退格键值。
func applyTermiosLineMode(tty *os.File) error {
	term, err := getTermios(tty.Fd())
	if err != nil {
		return err
	}
	term.Lflag |= syscall.ICANON | syscall.ECHO | syscall.ECHOE | syscall.ECHOK
	term.Lflag &^= syscall.ECHOCTL
	term.Cc[syscall.VERASE] = resolveEraseKeyByte()
	return setTermios(tty.Fd(), term)
}

// getTermios 读取当前终端配置。
// 使用 ioctl 读取 TIOCGETA。
func getTermios(fd uintptr) (*syscall.Termios, error) {
	var term syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCGETA), uintptr(unsafe.Pointer(&term)))
	if errno != 0 {
		return nil, errno
	}
	return &term, nil
}

// setTermios 写回终端配置。
// 使用 ioctl 写回 TIOCSETA。
func setTermios(fd uintptr, term *syscall.Termios) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCSETA), uintptr(unsafe.Pointer(term)))
	if errno != 0 {
		return errno
	}
	return nil
}
