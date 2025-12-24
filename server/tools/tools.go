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
	toolParamsShell = json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {
      "type": "string",
      "description": "要执行的 shell 命令（在用户工作目录下）"
    },
    "timeout_ms": {
      "type": "integer",
      "description": "超时时间（毫秒）。建议值 60000。",
      "minimum": 1
    },
    "policy": {
      "type": "string",
      "description": "权限策略：'workspace'=仅限工程目录；'readonly'=只读；'full'=无限制。建议默认 'workspace'。",
      "enum": ["full", "readonly", "workspace"]
    }
  },
  "required": ["command", "timeout_ms", "policy"],
  "additionalProperties": false
}`)

	toolParamsReadFile = json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "要读取的文件路径（相对或绝对路径）"
    },
    "max_bytes": {
      "type": "integer",
      "description": "最大读取字节数。建议值 524288 (512KB)。",
      "minimum": 1
    }
  },
  "required": ["path", "max_bytes"],
  "additionalProperties": false
}`)

	toolParamsListDir = json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "要列出的目录路径（相对或绝对路径）"
    }
  },
  "required": ["path"],
  "additionalProperties": false
}`)

	toolParamsGrepFiles = json.RawMessage(`{
  "type": "object",
  "properties": {
    "root": {
      "type": "string",
      "description": "搜索的起始目录（如 '.'）"
    },
    "pattern": {
      "type": "string",
      "description": "要匹配的正则或文本模式"
    },
    "max_matches": {
      "type": "integer",
      "description": "最大返回匹配行数。建议值 200。",
      "minimum": 1
    }
  },
  "required": ["root", "pattern", "max_matches"],
  "additionalProperties": false
}`)

	toolParamsApplyPatch = json.RawMessage(`{
  "type": "object",
  "properties": {
    "file": {
      "type": "string",
      "description": "要修改的文件路径（相对或绝对路径）"
    },
    "from": {
      "type": "string",
      "description": "待替换的唯一原始字符串片段"
    },
    "to": {
      "type": "string",
      "description": "替换后的新字符串片段"
    },
    "all": {
      "type": "boolean",
      "description": "是否替换所有匹配项。false 表示仅替换首个匹配（更安全），true 表示全部替换。"
    }
  },
  "required": ["file", "from", "to", "all"],
  "additionalProperties": false
}`)
)

func DefaultToolSpecs() []ToolSpec {
	return []ToolSpec{
		{
			Kind:        ToolKindFunction,
			Name:        "shell",
			Description: "执行 shell 命令。",
			Parameters:  toolParamsShell,
		},
		{
			Kind:        ToolKindFunction,
			Name:        "read_file",
			Description: "读取文件内容。",
			Parameters:  toolParamsReadFile,
		},
		{
			Kind:        ToolKindFunction,
			Name:        "list_dir",
			Description: "列出目录内容。",
			Parameters:  toolParamsListDir,
		},
		{
			Kind:        ToolKindFunction,
			Name:        "grep_files",
			Description: "使用 ripgrep 在代码中查找匹配行。用于搜索代码、查询理解项目结构。",
			Parameters:  toolParamsGrepFiles,
		},
		{
			Kind:        ToolKindFunction,
			Name:        "apply_patch",
			Description: "对单个文件应用修改（基于字符串替换）。",
			Parameters:  toolParamsApplyPatch,
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
}

// ToolKind 对应工具的类别，参考 codex 的 ToolSpec。
type ToolKind string

const (
	ToolKindFunction   ToolKind = "function"
	ToolKindLocalShell ToolKind = "local_shell"
	ToolKindWebSearch  ToolKind = "web_search"
	ToolKindCustom     ToolKind = "custom"
)
