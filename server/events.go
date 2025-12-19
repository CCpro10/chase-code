package server

import "time"

// EventKind 表示事件的类型，仿照 codex-rs 中的 EventMsg 做一个精简版本。
// 后续如果需要，可以逐步扩展更多的事件种类。
type EventKind string

const (
	// 一次用户 turn 的生命周期
	EventTurnStarted  EventKind = "turn_started"
	EventTurnFinished EventKind = "turn_finished"

	// LLM / Agent 相关
	EventAgentThinking  EventKind = "agent_thinking"   // 准备调用 LLM
	EventAgentTextDelta EventKind = "agent_text_delta" // 流式增量文本（当前未启用，仅预留）
	EventAgentTextDone  EventKind = "agent_text_done"  // 一轮回答完成

	// 工具调用相关
	EventToolPlanned     EventKind = "tool_planned"      // 解析出了 tool_calls JSON 或自然语言规划
	EventToolStarted     EventKind = "tool_started"      // 某个工具开始执行
	EventToolOutputDelta EventKind = "tool_output_delta" // 工具输出的增量内容（当前一次性发送）
	EventToolFinished    EventKind = "tool_finished"     // 某个工具执行结束

	// 补丁审批相关
	EventPatchApprovalRequest EventKind = "patch_approval_request" // 需要用户确认的补丁
	EventPatchApprovalResult  EventKind = "patch_approval_result"  // 审批结果（日志用）
)

// Event 是从 server 发送给上层（例如 CLI）的统一事件结构。
// 它故意保持精简，只包含当前 CLI 渲染所需的字段，避免过早设计复杂结构。
type Event struct {
	Kind EventKind `json:"kind"`
	Time time.Time `json:"time"`

	// Step 表示这是本次 turn 中的第几步（第几轮 LLM+工具循环）。
	Step int `json:"step,omitempty"`

	// ToolName 对于工具相关事件，标记是哪一个工具。
	ToolName string `json:"tool_name,omitempty"`

	// Message 是通用文本载荷，例如 agent 的最终回答、
	// 工具的输出内容、或“工具规划”的原始 JSON 等。
	Message string `json:"message,omitempty"`

	// RequestID 用于将一次补丁审批请求与用户的审批指令关联起来。
	RequestID string `json:"request_id,omitempty"`
	// Paths 是本次补丁涉及到的文件路径列表，用于给用户展示摘要。
	Paths []string `json:"paths,omitempty"`
}

// EventSink 抽象一个事件下游。
// 在 CLI 中通常会用一个带缓冲的 chan Event 包装成 ChanEventSink，
// 然后在单独的 goroutine 中消费事件并渲染到终端。
type EventSink interface {
	SendEvent(ev Event)
}

// ChanEventSink 是最简单的 EventSink 实现：
// 将事件非阻塞地写入一个 chan<- Event 中，
// 如果通道已满则直接丢弃该事件，避免阻塞 agent 主流程。
type ChanEventSink struct {
	Ch chan<- Event
}

func (s ChanEventSink) SendEvent(ev Event) {
	if s.Ch == nil {
		return
	}
	select {
	case s.Ch <- ev:
	default:
		// 下游消费速度慢时，丢弃事件以避免阻塞。
	}
}
