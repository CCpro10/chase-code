package tools

import (
	"fmt"
	"strings"
)

// formatPatchResultOutput formats patch results for display.
func formatPatchResultOutput(result ApplyPatchResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "已应用补丁（新增 %d / 修改 %d / 删除 %d）：", len(result.Summary.Added), len(result.Summary.Modified), len(result.Summary.Deleted))
	if len(result.Summary.Paths) > 0 {
		b.WriteString("\n")
		for _, path := range result.Summary.Paths {
			fmt.Fprintf(&b, "- %s\n", path)
		}
	}

	patch := strings.TrimRight(result.Patch, "\n")
	if strings.TrimSpace(patch) != "" {
		b.WriteString("\n补丁内容:\n")
		b.WriteString(patch)
	}
	return b.String()
}
