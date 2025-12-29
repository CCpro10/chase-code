package tools

import (
	"fmt"
	"os"
	"strings"
)

// ApplyEdit 是旧版基于字符串替换的编辑实现，保留用于兼容历史协议：
//   - 保留原有文件权限位
//   - 默认要求 from 片段在文件中唯一（All=false），否则报错，避免误改多处
//   - 提示性错误信息，方便调整补丁
func ApplyEdit(path, old, new string, replaceAll bool) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("读取文件信息失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("目标不是普通文件: %s", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取文件失败: %w", err)
	}

	content := string(data)
	if !strings.Contains(content, old) {
		return fmt.Errorf("目标字符串在文件中不存在: %q", old)
	}

	// 计算出现次数，用于非 all 模式保护性检查
	count := strings.Count(content, old)
	if !replaceAll && count > 1 {
		return fmt.Errorf("from 字符串在文件中出现了 %d 次；为避免误改，需保证 from 唯一，或将 all=true", count)
	}

	var replaced string
	if replaceAll {
		replaced = strings.ReplaceAll(content, old, new)
	} else {
		replaced = strings.Replace(content, old, new, 1)
	}

	if replaced == content {
		return fmt.Errorf("补丁未生效：应用后文件内容未发生变化")
	}

	if err := os.WriteFile(path, []byte(replaced), info.Mode().Perm()); err != nil {
		return fmt.Errorf("写回文件失败: %w", err)
	}
	return nil
}
