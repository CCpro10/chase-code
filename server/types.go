package server

import (
	"encoding/json"
	"strings"

	"chase-code/server/tools"
)

// Role 表示对话中一条消息的身份。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message 是对话的一条消息，类似 OpenAI 的 chat message。
type Message struct {
	Role       Role   `json:"role"`
	Content    string `json:"content"`
	Name       string `json:"name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// Prompt 对应一次调用的完整输入。
// 当前实现主要用于 Chat Completions，但同时预留了 ResponseItem / Tool
// 级别的结构，方便后续在本地编排工具调用，而不强耦合到底层 HTTP 协议。
type Prompt struct {
	Messages []Message
	Tools    []tools.ToolSpec `json:"-"`
	Items    []ResponseItem   `json:"-"`
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

	// ToolCalls 仅用于 assistant 消息，表示与该回复绑定的工具调用。
	ToolCalls []tools.ToolCall `json:"tool_calls,omitempty"`

	ToolName      string          `json:"tool_name,omitempty"`
	ToolArguments json.RawMessage `json:"tool_arguments,omitempty"`
	ToolOutput    string          `json:"tool_output,omitempty"`
	CallID        string          `json:"call_id,omitempty"`
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
// 仅保留普通对话消息；工具结果通过 Prompt.Items 以 tool role 传递给模型。
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
			if it.ToolName == "" && it.ToolOutput == "" {
				continue
			}
			msgs = append(msgs, Message{
				Role:       RoleTool,
				Content:    TruncateToolOutput(it.ToolOutput),
				Name:       it.ToolName,
				ToolCallID: it.CallID,
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
	toolPreviewMaxRunes   = 40960
	toolPreviewMaxLines   = 800
	toolPreviewTruncation = "...(工具输出已截断)"
)

// TruncateToolOutput 对工具输出做长度和行数截断，防止上下文被撑爆。
func TruncateToolOutput(s string) string {
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
