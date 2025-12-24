package llm

import (
	"context"
	"encoding/json"
	"strings"

	"chase-code/server/tools"
)

// ===== 对话与工具调用的通用上下文类型（供 server 与 llm 共同使用） =====

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

// Prompt 对应一次调用的完整输入。
// 当前实现主要用于 Chat Completions / Responses，但同时预留了 ResponseItem / Tool
// 级别的结构，方便在本地编排工具调用，而不强耦合到底层 HTTP 协议。
type Prompt struct {
	Messages []Message
	Tools    []tools.ToolSpec `json:"-"`
	Items    []ResponseItem   `json:"-"`
}

// 为方便其它包使用，直接公开 tools 包里的类型。
type ToolSpec = tools.ToolSpec
type ToolCall = tools.ToolCall

// 工具输出截断相关常量，可按需调整。
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

// 内部使用的别名，便于在本包内与旧代码兼容。
func truncateToolOutput(s string) string {
	return TruncateToolOutput(s)
}

// LLMEventKind / LLMEvent / LLMStream 参考 codex 的流式接口抽象，当前实现
// 只在 CompletionsClient.Stream 中做简单封装，保留扩展空间。
type LLMEventKind string

const (
	LLMEventCreated    LLMEventKind = "created"
	LLMEventTextDelta  LLMEventKind = "text_delta"
	LLMEventCompleted  LLMEventKind = "completed"
	LLMEventRateLimits LLMEventKind = "rate_limits"
	LLMEventError      LLMEventKind = "error"
)

type LLMEvent struct {
	Kind      LLMEventKind
	TextDelta string
	FullText  string
	Error     error
	Result    *LLMResult
}

type LLMStream struct {
	C   <-chan LLMEvent
	Err error
}

// LLMMessage 表示一次完整调用返回的一条 assistant 消息。
type LLMMessage struct {
	Role    Role
	Content string
}

// LLMResult 是 Complete 返回的结构化结果，
// 目前包含一条 assistant 消息以及可选的工具调用列表（来自 Completions tool_calls）。
type LLMResult struct {
	Message   LLMMessage
	ToolCalls []ToolCall
}

// LLMClient 抽象一个“模型客户端”，参考 codex 的 ModelClient：
//   - Complete 返回一个结构化的 LLMResult，而不是裸字符串，方便扩展；
//   - Stream 保持现有的事件流接口，用于以后支持真正的流式输出。
type LLMClient interface {
	Complete(ctx context.Context, p Prompt) (*LLMResult, error)
	Stream(ctx context.Context, p Prompt) *LLMStream
}
