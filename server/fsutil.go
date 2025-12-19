package server

import (
	"fmt"
	"os"
)

// ReadFileLimited 读取文件内容，并在超过 maxBytes 时做截断和告警。
func ReadFileLimited(path string, maxBytes int) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取文件失败: %w", err)
	}
	if maxBytes > 0 && len(data) > maxBytes {
		fmt.Fprintf(os.Stderr, "警告: 文件过大，仅输出前 %d 字节\n", maxBytes)
		data = data[:maxBytes]
	}
	return data, nil
}
