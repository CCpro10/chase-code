package llm

import (
	"encoding/json"
	"strings"
)

// normalizePromptItems 确保 Prompt.Items 有可用内容；若为空则从 Messages 生成。
// 其中 RoleTool 会被转换为 tool_result，避免工具输出丢失。
func normalizePromptItems(p Prompt) []ResponseItem {
	if len(p.Items) > 0 {
		return p.Items
	}
	if len(p.Messages) == 0 {
		return nil
	}

	items := make([]ResponseItem, 0, len(p.Messages))
	for _, m := range p.Messages {
		if m.Role == RoleTool {
			items = append(items, ResponseItem{
				Type:       ResponseItemToolResult,
				ToolName:   m.Name,
				ToolOutput: m.Content,
				CallID:     strings.TrimSpace(m.ToolCallID),
			})
			continue
		}
		items = append(items, ResponseItem{
			Type:   ResponseItemMessage,
			Role:   m.Role,
			Text:   m.Content,
			CallID: strings.TrimSpace(m.ToolCallID),
		})
	}
	return items
}

// normalizeSDKArguments 尝试将 SDK 返回/传入的 arguments 规范为有效 JSON。
func normalizeSDKArguments(args json.RawMessage) json.RawMessage {
	if len(args) == 0 {
		return json.RawMessage("{}")
	}
	var s string
	if err := json.Unmarshal(args, &s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			return json.RawMessage("{}")
		}
		if json.Valid([]byte(s)) {
			return json.RawMessage(s)
		}
		quoted, _ := json.Marshal(s)
		return json.RawMessage(quoted)
	}
	return args
}

// formatFunctionCallArguments 将工具参数格式化为 SDK 需要的 JSON 字符串。
func formatFunctionCallArguments(args json.RawMessage) string {
	return string(normalizeSDKArguments(args))
}
