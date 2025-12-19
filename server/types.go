package server

import "encoding/json"

// Role 表示对话中一条消息的身份。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message 是对话的一条消息，类似 OpenAI 的 chat message。
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

// Prompt 对应一次调用的完整输入。
// 当前实现主要用于 Chat Completions，但同时预留了 ResponseItem / Tool
// 级别的结构，方便后续在本地编排工具调用，而不强耦合到底层 HTTP 协议。
type Prompt struct {
	Messages []Message
	Tools    []ToolSpec     `json:"-"`
	Items    []ResponseItem `json:"-"`
}

// ToolKind 对应工具的类别，参考 codex 的 ToolSpec。
type ToolKind string

const (
	ToolKindFunction   ToolKind = "function"
	ToolKindLocalShell ToolKind = "local_shell"
	ToolKindWebSearch  ToolKind = "web_search"
	ToolKindCustom     ToolKind = "custom"
)

// ToolSpec 描述一个可被模型调用的工具定义。
// 对于当前实现，主要依赖 Name 和 Description。
type ToolSpec struct {
	Kind        ToolKind        `json:"kind"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ResponseItemType 表示一次“对话轨迹条目”的类型。
type ResponseItemType string

const (
	ResponseItemMessage    ResponseItemType = "message"
	ResponseItemToolCall   ResponseItemType = "tool_call"
	ResponseItemToolResult ResponseItemType = "tool_result"
)

// ResponseItem 是“对话+工具调用”的统一表示。
type ResponseItem struct {
	Type ResponseItemType `json:"type"`

	Role Role   `json:"role,omitempty"`
	Text string `json:"text,omitempty"`

	ToolName      string          `json:"tool_name,omitempty"`
	ToolArguments json.RawMessage `json:"tool_arguments,omitempty"`
	ToolOutput    string          `json:"tool_output,omitempty"`
}

// ToolCall 描述一条来自 LLM 的工具调用请求，对应约定的 JSON 协议。
type ToolCall struct {
	ToolName  string          `json:"tool_name"`
	Arguments json.RawMessage `json:"arguments"`
}
