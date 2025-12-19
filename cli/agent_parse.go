package cli

import (
	"encoding/json"
	"strings"
	"unicode"

	"chase-code/server"
)

// parseToolCallsFromText 尝试从自然语言文本中提取类似
// "调用工具 read_file，参数: { ... }" 这样的工具调用描述，
// 将其转换为 ToolCall 列表，作为对严格 JSON 解析的补充。
func parseToolCallsFromText(s string) ([]server.ToolCall, bool) {
	idx := strings.Index(s, "调用工具")
	if idx == -1 {
		return nil, false
	}

	rest := s[idx+len("调用工具"):]
	// 期望格式：<tool_name>，参数: { ... }

	// 先找到“参数”二字出现的位置
	paramIdx := strings.Index(rest, "参数")
	if paramIdx == -1 {
		return nil, false
	}

	namePart := strings.TrimSpace(rest[:paramIdx])
	// namePart 通常形如 "read_file，" 或 "read_file "，我们要提取前面的标识符部分
	var nameRunes []rune
	for _, r := range namePart {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			nameRunes = append(nameRunes, r)
		} else {
			break
		}
	}
	toolName := strings.TrimSpace(string(nameRunes))
	if toolName == "" {
		return nil, false
	}

	// 从原始字符串中提取第一个 JSON 对象作为 arguments
	jsonStart := strings.Index(s, "{")
	jsonEnd := strings.LastIndex(s, "}")
	if jsonStart == -1 || jsonEnd <= jsonStart {
		return nil, false
	}
	jsonStr := s[jsonStart : jsonEnd+1]

	// 仅做基本的 JSON 校验，避免把怪异内容当成 arguments
	var tmp any
	if err := json.Unmarshal([]byte(jsonStr), &tmp); err != nil {
		return nil, false
	}

	call := server.ToolCall{
		ToolName:  toolName,
		Arguments: json.RawMessage(jsonStr),
	}
	return []server.ToolCall{call}, true
}
