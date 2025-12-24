package llm

import (
	"context"

	serverpkg "chase-code/server"
	"chase-code/server/tools"
)

// 复用 server 包定义的上下文类型，避免循环依赖与重复定义。
type Role = serverpkg.Role

const (
	RoleSystem    = serverpkg.RoleSystem
	RoleUser      = serverpkg.RoleUser
	RoleAssistant = serverpkg.RoleAssistant
	RoleTool      = serverpkg.RoleTool
)

type Prompt = serverpkg.Prompt
type ToolSpec = tools.ToolSpec
type ToolCall = tools.ToolCall
type ResponseItem = serverpkg.ResponseItem
type ResponseItemType = serverpkg.ResponseItemType

const (
	ResponseItemMessage    = serverpkg.ResponseItemMessage
	ResponseItemToolCall   = serverpkg.ResponseItemToolCall
	ResponseItemToolResult = serverpkg.ResponseItemToolResult
)

func truncateToolOutput(s string) string {
	return serverpkg.TruncateToolOutput(s)
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
