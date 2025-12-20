package server

import (
	"encoding/json"
	"fmt"
	"strings"
)

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

// ContextManager 管理一次会话的完整历史（消息 + 工具调用 + 工具结果）。
type ContextManager struct {
	items []ResponseItem
}

// NewContextManager 从已有的历史条目构造一个 ContextManager。
// 会复制一份切片，避免外部直接修改内部状态。
func NewContextManager(history []ResponseItem) *ContextManager {
	cp := make([]ResponseItem, len(history))
	copy(cp, history)
	return &ContextManager{items: cp}
}

// Record 将新的 ResponseItem 追加到历史中。
func (c *ContextManager) Record(items ...ResponseItem) {
	if c == nil {
		return
	}
	for _, it := range items {
		// 如有需要，可以在这里模仿 codex 的 is_api_message 做过滤
		c.items = append(c.items, it)
	}
}

// BuildPromptMessages 将内部的 ResponseItem 历史转换为模型最终看到的 Message 数组。
// 在这里统一决定：
//   - 工具结果以什么样的自然语言形式暴露给模型
//   - 工具输出是否截断
func (c *ContextManager) BuildPromptMessages() []Message {
	if c == nil {
		return nil
	}

	msgs := make([]Message, 0, len(c.items))
	for _, it := range c.items {
		switch it.Type {
		case ResponseItemMessage:
			// 纯文本消息，直接透传
			msgs = append(msgs, Message{
				Role:    it.Role,
				Content: it.Text,
			})

		case ResponseItemToolResult:
			// 工具结果：以统一的自然语言包装 + 截断输出
			if it.ToolName == "" && it.ToolOutput == "" {
				continue
			}
			content := fmt.Sprintf("工具 %s 的输出:\n%s", it.ToolName, truncateToolOutput(it.ToolOutput))
			msgs = append(msgs, Message{
				Role:    RoleAssistant,
				Content: content,
			})

		case ResponseItemToolCall:
			// 是否把工具调用计划暴露给模型由你决定：
			// 这里先选择略过，行为更接近当前实现。
			continue
		}
	}

	return msgs
}

// History 返回当前全部 ResponseItem 历史的拷贝。
func (c *ContextManager) History() []ResponseItem {
	if c == nil {
		return nil
	}
	cp := make([]ResponseItem, len(c.items))
	copy(cp, c.items)
	return cp
}

// 工具输出截断相关的简单常量，可按需调整。
const (
	toolPreviewMaxRunes   = 4096
	toolPreviewMaxLines   = 80
	toolPreviewTruncation = "...(工具输出已截断)"
)

// truncateToolOutput 对工具输出做长度和行数截断，防止上下文被工具结果撑爆。
func truncateToolOutput(s string) string {
	if s == "" {
		return s
	}

	// 先按 rune 数截断，保证不会截到 UTF-8 半个字符。
	runes := []rune(s)
	if len(runes) > toolPreviewMaxRunes {
		runes = runes[:toolPreviewMaxRunes]
	}
	truncated := string(runes)

	// 再按行数截断
	lines := strings.Split(truncated, "\n")
	if len(lines) > toolPreviewMaxLines {
		lines = append(lines[:toolPreviewMaxLines], toolPreviewTruncation)
	}

	return strings.Join(lines, "\n")
}
