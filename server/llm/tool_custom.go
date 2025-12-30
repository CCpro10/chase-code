package llm

import (
	"encoding/json"

	servertools "chase-code/server/tools"
)

// buildCustomToolPayload 生成 custom 工具需要的原始 JSON。
func buildCustomToolPayload(t ToolSpec) (json.RawMessage, bool) {
	if t.Kind != servertools.ToolKindCustom || len(t.Format) == 0 {
		return nil, false
	}

	payload := map[string]any{
		"type":   "custom",
		"name":   t.Name,
		"format": json.RawMessage(t.Format),
	}
	if t.Description != "" {
		payload["description"] = t.Description
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, false
	}
	return json.RawMessage(data), true
}
