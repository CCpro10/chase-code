//go:build !windows

package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
)

// applySttyLineMode 使用 stty 统一行模式与退格键行为。
// stty 是类 Unix 的终端配置工具，这里用于恢复行模式与回显设置，保证可编辑输入。
func applySttyLineMode(tty *os.File) error {
	if tty == nil {
		return fmt.Errorf("终端为空")
	}
	sttyPath, err := resolveSttyPath()
	if err != nil {
		return err
	}
	eraseKey := resolveEraseKeyString()
	cmd := exec.Command(sttyPath, "erase", eraseKey, "icanon", "echo", "echoe", "echok")
	cmd.Stdin = tty
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

// resolveSttyPath 优先从 PATH 查找 stty，否则尝试常见路径。
// 某些受限环境 PATH 不完整，因此需要兜底。
func resolveSttyPath() (string, error) {
	if path, err := exec.LookPath("stty"); err == nil {
		return path, nil
	}
	candidates := []string{"/bin/stty", "/usr/bin/stty"}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("未找到 stty")
}
