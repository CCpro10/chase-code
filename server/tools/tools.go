package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"chase-code/server"
)

// ParseToolCallsJSON 解析 LLM 输出的 JSON 字符串，支持单个对象或对象数组。
func ParseToolCallsJSON(raw string) ([]server.ToolCall, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("工具调用 JSON 为空")
	}

	if strings.HasPrefix(raw, "[") {
		var calls []server.ToolCall
		if err := json.Unmarshal([]byte(raw), &calls); err != nil {
			return nil, err
		}
		return calls, nil
	}

	var call server.ToolCall
	if err := json.Unmarshal([]byte(raw), &call); err != nil {
		return nil, err
	}
	return []server.ToolCall{call}, nil
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
      "description": "超时时间（毫秒，可选，默认 60000）",
      "minimum": 1
    },
    "policy": {
      "type": "string",
      "description": "命令权限策略：full=不限制；readonly=只读；workspace=仅允许当前工程目录",
      "enum": ["full", "readonly", "workspace"]
    }
  },
  "required": ["command"],
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
      "description": "最大读取字节数（可选，不填则读取整个文件）",
      "minimum": 1
    }
  },
  "required": ["path"],
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
      "description": "搜索的根目录路径"
    },
    "pattern": {
      "type": "string",
      "description": "要匹配的正则或文本模式"
    },
    "max_matches": {
      "type": "integer",
      "description": "最大匹配行数（可选）",
      "minimum": 1
    }
  },
  "required": ["root", "pattern"],
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
      "description": "待替换的原始字符串（必须能在文件中找到）"
    },
    "to": {
      "type": "string",
      "description": "替换后的新字符串"
    },
    "all": {
      "type": "boolean",
      "description": "是否替换文件中出现的所有 from；默认只允许唯一一次匹配"
    }
  },
  "required": ["file", "from", "to"],
  "additionalProperties": false
}`)
)

func DefaultToolSpecs() []server.ToolSpec {
	return []server.ToolSpec{
		{
			Kind:        server.ToolKindFunction,
			Name:        "shell",
			Description: "执行 shell 命令。",
			Parameters:  toolParamsShell,
		},
		{
			Kind:        server.ToolKindFunction,
			Name:        "read_file",
			Description: "读取文件内容。",
			Parameters:  toolParamsReadFile,
		},
		{
			Kind:        server.ToolKindFunction,
			Name:        "list_dir",
			Description: "列出目录内容。",
			Parameters:  toolParamsListDir,
		},
		{
			Kind:        server.ToolKindFunction,
			Name:        "grep_files",
			Description: "使用 ripgrep 在代码中查找匹配行。用于搜索代码、查询理解项目结构。",
			Parameters:  toolParamsGrepFiles,
		},
		{
			Kind:        server.ToolKindFunction,
			Name:        "apply_patch",
			Description: "对单个文件应用修改（基于字符串替换）。",
			Parameters:  toolParamsApplyPatch,
		},
	}
}

// BuildToolSystemPrompt 基于工具列表构造一段 system prompt，
// 主要用于提示模型如何安全、高效地使用这些 function tools。
// 注意：工具调用通过 OpenAI function calling 完成，模型不需要、也不应该在 message
// 内容中手写任何 JSON 工具调用；只需要在内部决定是否调用某个工具即可。
func BuildToolSystemPrompt(tools []server.ToolSpec) string {
	var b strings.Builder

	// 角色与目标
	b.WriteString("你是 chase-code 的本地代码助手，运行在用户的工作目录中，可以调用工具帮助用户完成开发任务。\n")
	b.WriteString("你的目标是：在保证安全和谨慎修改代码的前提下，尽量自动完成用户的开发任务，并用中文解释你的思路。\n\n")

	// 工具使用总原则
	b.WriteString("=== 工具使用总原则 ===\n\n")
	b.WriteString("- 你可以通过“函数调用（function calling）”的方式使用下列工具。\n")
	b.WriteString("- 工具调用由系统根据 tools 定义触发，你不需要在回复中手写 JSON。\n")
	b.WriteString("- 在给用户的回复中，禁止输出任何表示工具调用的 JSON 结构或工具名+参数的伪代码。\n")
	b.WriteString("- 你的 message 内容只面向用户，应该是自然语言解释、结论和后续计划。\n\n")

	b.WriteString("在决定是否调用工具时，请先思考：\n")
	b.WriteString("1. 当前还缺少什么信息？\n")
	b.WriteString("2. 哪个工具最适合获取这些信息或修改代码？\n")

	b.WriteString("=== 工具选择建议 ===\n\n")
	b.WriteString("- 想了解项目结构 → 优先使用 list_dir 或 grep_files。\n")
	b.WriteString("- 想阅读/理解某个文件 → 使用 read_file。\n")
	b.WriteString("- 想做小范围修改 → 使用 apply_patch，修改前尽量先 read_file 确认上下文。\n")
	b.WriteString("- 想执行命令（如 go test / go build）→ 使用 shell，但要避免危险命令（删除系统文件、格式化磁盘等）。\n")
	b.WriteString("- 执行工具后、继续根据用户需求，选择其他工具、直到完成用户的任务。\n\n")

	// 工具列表（给模型一个清晰的总览）
	b.WriteString("=== 可用工具列表（名称 / 描述 ） ===\n")
	for i, t := range tools {
		params := strings.TrimSpace(string(t.Parameters))
		if params == "" {
			params = "{}"
		}
		fmt.Fprintf(&b, "%d. %s — %s\n   \n", i+1, t.Name, t.Description)
	}
	b.WriteString("\n")

	b.WriteString("=== 回复风格要求 ===\n\n")
	b.WriteString("- 始终用中文回复。\n")
	b.WriteString("- 当你使用了工具时，在给用户的自然语言回答中，\n  可以用一两句话概括你刚才做了什么（例如：‘我刚才用 read_file 看了 main.go 的内容’）。\n")
	b.WriteString("- 不要在自然语言中暴露底层的函数名、JSON 结构或完整参数，只描述你做过的动作和结论。\n")
	b.WriteString("- 如果需要用户决策（例如选择某种改动方案），先列出备选方案及利弊，让用户选择。\n")
	b.WriteString("- 可以多次调用工具、直到完成用户任务。\n")

	return b.String()
}
