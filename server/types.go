package server

import (
	"chase-code/server/llm"
)

// 本文件主要作为 llm 包中公共对话/工具类型的别名出口，避免上层调用方
// 同时依赖 server 和 llm 两个包的细节。

// Role 表示对话中一条消息的身份。
type Role = llm.Role

const (
	RoleSystem    = llm.RoleSystem
	RoleUser      = llm.RoleUser
	RoleAssistant = llm.RoleAssistant
	RoleTool      = llm.RoleTool
)

// Message 是对话的一条消息，类似 OpenAI 的 chat message。
type Message = llm.Message

// Prompt 对应一次调用的完整输入。
type Prompt = llm.Prompt

// ResponseItemType 表示一次“对话轨迹条目”的类型。
type ResponseItemType = llm.ResponseItemType

const (
	ResponseItemMessage    = llm.ResponseItemMessage
	ResponseItemToolCall   = llm.ResponseItemToolCall
	ResponseItemToolResult = llm.ResponseItemToolResult
)

// ResponseItem 是“对话+工具调用”的统一表示。
type ResponseItem = llm.ResponseItem

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

// TruncateToolOutput 对工具输出做长度和行数截断，防止上下文被撑爆。
func TruncateToolOutput(s string) string {
	return llm.TruncateToolOutput(s)
}
