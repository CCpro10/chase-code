package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseToolCallsJSON 解析 LLM 输出的 JSON 字符串，支持单个对象或对象数组。
func ParseToolCallsJSON(raw string) ([]ToolCall, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("工具调用 JSON 为空")
	}

	if strings.HasPrefix(raw, "[") {
		var calls []ToolCall
		if err := json.Unmarshal([]byte(raw), &calls); err != nil {
			return nil, err
		}
		return calls, nil
	}

	var call ToolCall
	if err := json.Unmarshal([]byte(raw), &call); err != nil {
		return nil, err
	}
	return []ToolCall{call}, nil
}

// DefaultToolSpecs 返回 chase-code 默认暴露给 LLM 的工具集合。
var (
	toolParamsShellCommand = json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {
      "type": "string",
      "description": "The shell script to execute in the user's default shell"
    },
    "justification": {
      "type": "string",
      "description": "Only set if sandbox_permissions is \"require_escalated\". 1-sentence explanation of why we want to run this command."
    },
    "login": {
      "type": "boolean",
      "description": "Whether to run the shell with login shell semantics. Defaults to true."
    },
    "sandbox_permissions": {
      "type": "string",
      "description": "Sandbox permissions for the command. Set to \"require_escalated\" to request running without sandbox restrictions; defaults to \"use_default\"."
    },
    "timeout_ms": {
      "type": "number",
      "description": "The timeout for the command in milliseconds"
    },
    "workdir": {
      "type": "string",
      "description": "The working directory to execute the command in"
    }
  },
  "required": ["command"],
  "additionalProperties": false
}`)

	toolFormatApplyPatch = json.RawMessage(`{
  "type": "grammar",
  "syntax": "lark",
  "definition": "start: begin_patch hunk+ end_patch\nbegin_patch: \"*** Begin Patch\" LF\nend_patch: \"*** End Patch\" LF?\n\nhunk: add_hunk | delete_hunk | update_hunk\nadd_hunk: \"*** Add File: \" filename LF add_line+\ndelete_hunk: \"*** Delete File: \" filename LF\nupdate_hunk: \"*** Update File: \" filename LF change_move? change?\n\nfilename: /(.+)/\nadd_line: \"+\" /(.*)/ LF -> line\n\nchange_move: \"*** Move to: \" filename LF\nchange: (change_context | change_line)+ eof_line?\nchange_context: (\"@@\" | \"@@ \" /(.+)/) LF\nchange_line: (\"+\" | \"-\" | \" \") /(.*)/ LF\neof_line: \"*** End of File\" LF\n\n%import common.LF\n"
}`)
)

func DefaultToolSpecs() []ToolSpec {
	shellStrict := false
	return []ToolSpec{
		{
			Kind:        ToolKindFunction,
			Name:        "shell_command",
			Description: "Runs a shell command and returns its output.\n- Always set the `workdir` param when using the shell_command function. Do not use `cd` unless absolutely necessary.",
			Parameters:  toolParamsShellCommand,
			Strict:      &shellStrict,
		},
		{
			Kind:        ToolKindCustom,
			Name:        "apply_patch",
			Description: "Use the `apply_patch` tool to edit files. This is a FREEFORM tool, so do not wrap the patch in JSON.",
			Format:      toolFormatApplyPatch,
		},
	}
}

// ToolCall 描述一条来自 LLM 的工具调用请求，对应约定的 JSON 协议。
type ToolCall struct {
	ToolName  string          `json:"tool_name"`
	Arguments json.RawMessage `json:"arguments"`
	CallID    string          `json:"call_id,omitempty"`
}

// ToolSpec 描述一个可被模型调用的工具定义。
// 对于当前实现，主要依赖 Name 和 Description。
type ToolSpec struct {
	Kind        ToolKind        `json:"kind"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Format      json.RawMessage `json:"format,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// ToolKind 对应工具的类别，参考 codex 的 ToolSpec。
type ToolKind string

const (
	ToolKindFunction   ToolKind = "function"
	ToolKindLocalShell ToolKind = "local_shell"
	ToolKindWebSearch  ToolKind = "web_search"
	ToolKindCustom     ToolKind = "custom"
)
