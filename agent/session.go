package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"chase-code/server"
	servertools "chase-code/server/tools"
)

// Session 封装了一次基于 LLM+工具的对话会话（类似 codex-rs 的 Session），
// 负责驱动多轮 LLM 调用和工具调用，并通过 EventSink 将关键步骤发送给上层（如 CLI）。
type Session struct {
	Client   server.LLMClient
	Router   *servertools.ToolRouter
	Sink     server.EventSink
	MaxSteps int // 单次 turn 允许的最多 LLM+工具循环步数，<=0 时使用默认 10

	Config SessionConfig

	// approvals 用于接收来自 CLI 的补丁审批结果。
	approvals chan ApprovalDecision
}

// ApprovalDecision 表示一次补丁审批请求的结果。
type ApprovalDecision struct {
	RequestID string
	Approved  bool
}

// NewSession 创建一个带事件和审批通道的 Session。
func NewSession(client server.LLMClient, router *servertools.ToolRouter, sink server.EventSink, maxSteps int) *Session {
	if maxSteps <= 0 {
		maxSteps = 10
	}
	cfg := DefaultSessionConfigFromEnv()
	return &Session{
		Client:    client,
		Router:    router,
		Sink:      sink,
		MaxSteps:  maxSteps,
		Config:    cfg,
		approvals: make(chan ApprovalDecision, 1),
	}
}

// ApprovalsChan 返回一个写端通道，供 CLI 将用户审批结果写回。
func (s *Session) ApprovalsChan() chan<- ApprovalDecision {
	return s.approvals
}

// RunTurn 执行一轮用户指令：
//   - 先将 userInput 追加到历史中（以 ResponseItem 形式管理）；
//   - 在最多 MaxSteps 步内，反复调用 LLM 和工具；
//   - 当 LLM 不再返回工具调用 JSON 时，将其视为最终回答并结束。
//
// 返回更新后的对话历史（仍然是 []server.Message，以兼容现有调用方）。
func (s *Session) RunTurn(ctx context.Context, userInput string, history []server.Message) ([]server.Message, error) {
	if s == nil || s.Client == nil || s.Router == nil {
		return history, nil
	}

	maxSteps := s.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 10
	}

	// 1) 将旧的 []Message 历史转换为 []ResponseItem
	historyItems := make([]server.ResponseItem, 0, len(history)+1)
	for _, m := range history {
		historyItems = append(historyItems, server.ResponseItem{
			Type: server.ResponseItemMessage,
			Role: m.Role,
			Text: m.Content,
		})
	}

	// 2) 追加本轮用户输入
	historyItems = append(historyItems, server.ResponseItem{
		Type: server.ResponseItemMessage,
		Role: server.RoleUser,
		Text: userInput,
	})

	cm := server.NewContextManager(historyItems)

	log.Printf("[agent] new turn input=%q history_len=%d", userInput, len(history))

	s.Sink.SendEvent(server.Event{Kind: server.EventTurnStarted, Time: time.Now()})

	baseCtx := ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}

	for step := 0; step < maxSteps; step++ {
		// 1) 调用 LLM
		s.Sink.SendEvent(server.Event{Kind: server.EventAgentThinking, Time: time.Now(), Step: step})

		// 每一轮根据当前历史构造 Prompt
		prompt := server.Prompt{
			Messages: cm.BuildPromptMessages(),
			Tools:    s.Router.Specs(),
			Items:    cm.History(),
		}

		log.Printf("[agent] step=%d calling LLM (history_items=%d, prompt_msgs=%d)", step, len(cm.History()), len(prompt.Messages))

		// 为本次 LLM 调用单独设置超时，避免影响后续工具执行/审批流程。
		llmCtx, cancelLLM := context.WithTimeout(baseCtx, 60*time.Second)
		res, err := s.Client.Complete(llmCtx, prompt)
		cancelLLM()
		if err != nil {
			log.Printf("[agent] step=%d LLM error: %v", step, err)
			// 失败时直接返回当前折叠后的历史
			return itemsToMessages(cm.History()), err
		}

		reply := res.Message.Content
		log.Printf("[agent] step=%d LLM reply_len=%d tool_calls=%d", step, len(reply), len(res.ToolCalls))
		log.Printf("[agent] step=%d LLM reply preview:\n%s", step, previewLLMReplyForLog(reply))

		if strings.TrimSpace(reply) != "" {
			cm.Record(server.ResponseItem{
				Type: server.ResponseItemMessage,
				Role: server.RoleAssistant,
				Text: reply,
			})
		}

		var calls []server.ToolCall
		if len(res.ToolCalls) > 0 {
			// 1) 优先使用 OpenAI tool_calls 结果
			calls = res.ToolCalls
		} else {
			// 2) 回退到原有的文本 JSON 工具协议
			var parseErr error
			calls, parseErr = servertools.ParseToolCallsJSON(reply)
			if parseErr != nil {
				log.Printf("[agent] step=%d parse tool_calls from text failed: %v", step, parseErr)
			}
		}

		// 如果既没有 function call，也没有有效的文本工具 JSON，则视为最终回答
		if len(calls) == 0 {
			s.Sink.SendEvent(server.Event{
				Kind:    server.EventAgentTextDone,
				Time:    time.Now(),
				Step:    step,
				Message: reply,
			})
			s.Sink.SendEvent(server.Event{Kind: server.EventTurnFinished, Time: time.Now(), Step: step})

			return itemsToMessages(cm.History()), nil
		}

		for i := range calls {
			if strings.TrimSpace(calls[i].CallID) == "" {
				calls[i].CallID = fmt.Sprintf("local-%d-%d", step, i)
			}
		}

		// 有工具调用时，先发送“规划”事件，方便 CLI 展示原始 JSON 或 function 调用情况
		s.Sink.SendEvent(server.Event{
			Kind:    server.EventToolPlanned,
			Time:    time.Now(),
			Step:    step,
			Message: reply,
		})
		log.Printf("[agent] step=%d resolved %d tool_calls", step, len(calls))

		// 3) 依次执行所有工具调用，将输出写回历史，供下一轮 LLM 使用
		//    工具执行和审批流程使用 baseCtx，以避免受 LLM 超时影响。
		for _, c := range calls {
			log.Printf("[agent] step=%d executing tool=%s", step, c.ToolName)

			// 3.1 先记录工具调用本身
			cm.Record(server.ResponseItem{
				Type:          server.ResponseItemToolCall,
				ToolName:      c.ToolName,
				ToolArguments: c.Arguments,
				CallID:        c.CallID,
			})

			var item server.ResponseItem
			var execErr error
			toolCtx := baseCtx
			if c.ToolName == "apply_patch" {
				item, execErr = s.executeApplyPatchWithSafety(toolCtx, c, step)
			} else {
				item, execErr = s.Router.Execute(toolCtx, c)
			}

			if execErr != nil {
				s.Sink.SendEvent(server.Event{
					Kind:     server.EventToolFinished,
					Time:     time.Now(),
					Step:     step,
					ToolName: c.ToolName,
					Message:  "工具执行失败: " + execErr.Error(),
				})
				log.Printf("[agent] step=%d tool=%s error=%v", step, c.ToolName, execErr)

				// 同时把失败信息写回历史，避免模型误以为工具执行成功。
				cm.Record(server.ResponseItem{
					Type:       server.ResponseItemToolResult,
					ToolName:   c.ToolName,
					ToolOutput: fmt.Sprintf("工具执行失败: %v", execErr),
					CallID:     c.CallID,
				})

				continue
			}

			s.Sink.SendEvent(server.Event{
				Kind:     server.EventToolOutputDelta,
				Time:     time.Now(),
				Step:     step,
				ToolName: c.ToolName,
				Message:  item.ToolOutput,
			})
			s.Sink.SendEvent(server.Event{
				Kind:     server.EventToolFinished,
				Time:     time.Now(),
				Step:     step,
				ToolName: c.ToolName,
			})
			log.Printf("[agent] step=%d tool=%s done output_len=%d", step, c.ToolName, len(item.ToolOutput))

			// 3.2 把工具结果以 ResponseItem 形式写回历史，
			//      实际暴露给模型时由 ContextManager/llm.go 统一包装。
			cm.Record(server.ResponseItem{
				Type:       server.ResponseItemToolResult,
				ToolName:   item.ToolName,
				ToolOutput: item.ToolOutput,
				CallID:     c.CallID,
			})
		}
	}

	// 达到最大步数仍未返回最终回答
	s.Sink.SendEvent(server.Event{
		Kind:    server.EventTurnFinished,
		Time:    time.Now(),
		Step:    s.MaxSteps,
		Message: "达到最大步数，终止",
	})

	return itemsToMessages(cm.History()), nil
}

// itemsToMessages 将 ResponseItem 历史折叠回旧的 []server.Message 形式，
// 只保留 Type=message 的条目，确保对外行为兼容。
func itemsToMessages(items []server.ResponseItem) []server.Message {
	msgs := make([]server.Message, 0, len(items))
	for _, it := range items {
		if it.Type != server.ResponseItemMessage {
			continue
		}
		msgs = append(msgs, server.Message{
			Role:    it.Role,
			Content: it.Text,
		})
	}
	return msgs
}

const (
	llmReplyPreviewMaxRunes = 1024
	llmReplyPreviewMaxLines = 20
)

// previewLLMReplyForLog 对 LLM 回复做简单截断，避免日志过长。
func previewLLMReplyForLog(s string) string {
	if s == "" {
		return s
	}

	runes := []rune(s)
	if len(runes) > llmReplyPreviewMaxRunes {
		runes = runes[:llmReplyPreviewMaxRunes]
	}
	truncated := string(runes)

	lines := strings.Split(truncated, "\n")
	if len(lines) > llmReplyPreviewMaxLines {
		lines = append(lines[:llmReplyPreviewMaxLines], "...(LLM reply 已截断)")
	}

	return strings.Join(lines, "\n")
}

// executeApplyPatchWithSafety 对 apply_patch 调用进行安全评估和必要的人工审批，
// 只有在补丁被认为安全或被用户明确批准的情况下才真正执行。
func (s *Session) executeApplyPatchWithSafety(ctx context.Context, call server.ToolCall, step int) (server.ResponseItem, error) {
	var args struct {
		File string `json:"file"`
		From string `json:"from"`
		To   string `json:"to"`
		All  bool   `json:"all"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return server.ResponseItem{}, fmt.Errorf("解析 apply_patch 参数失败: %w", err)
	}

	// 先做简单的安全评估
	decision := servertools.EvaluateSimplePatchSafety(args.File, args.From, args.To, args.All)

	// 根据 SessionConfig 调整审批策略
	switch s.Config.ToolApproval.ApplyPatch {
	case ApprovalModeAlwaysAsk:
		if decision.Level == servertools.PatchSafe {
			decision.Level = servertools.PatchAskUser
		}
	case ApprovalModeAlwaysApprove:
		if decision.Level == servertools.PatchAskUser {
			decision.Level = servertools.PatchSafe
		}
	case ApprovalModeAuto:
		// 保持原有决策
	}

	switch decision.Level {
	case servertools.PatchSafe:
		// 直接执行
		s.Sink.SendEvent(server.Event{
			Kind:     server.EventToolStarted,
			Time:     time.Now(),
			Step:     step,
			ToolName: call.ToolName,
		})
		return s.Router.Execute(ctx, call)

	case servertools.PatchReject:
		reason := decision.Reason
		if reason == "" {
			reason = "补丁被安全策略拒绝"
		}
		s.Sink.SendEvent(server.Event{
			Kind:     server.EventToolFinished,
			Time:     time.Now(),
			Step:     step,
			ToolName: call.ToolName,
			Message:  "patch rejected: " + reason,
		})
		return server.ResponseItem{}, fmt.Errorf("补丁被拒绝: %s", reason)

	case servertools.PatchAskUser:
		// 向 CLI 发送审批请求事件，并等待用户决策
		reqID := fmt.Sprintf("patch-%d-%d", time.Now().UnixNano(), step)
		s.Sink.SendEvent(server.Event{
			Kind:      server.EventPatchApprovalRequest,
			Time:      time.Now(),
			Step:      step,
			ToolName:  call.ToolName,
			RequestID: reqID,
			Paths:     decision.Paths,
			Message:   decision.Reason,
		})

		approved, err := s.waitForApproval(ctx, reqID)
		if err != nil {
			return server.ResponseItem{}, err
		}
		if !approved {
			s.Sink.SendEvent(server.Event{
				Kind:      server.EventPatchApprovalResult,
				Time:      time.Now(),
				Step:      step,
				ToolName:  call.ToolName,
				RequestID: reqID,
				Message:   "patch rejected by user",
			})
			return server.ResponseItem{}, fmt.Errorf("补丁被用户拒绝")
		}

		s.Sink.SendEvent(server.Event{
			Kind:      server.EventPatchApprovalResult,
			Time:      time.Now(),
			Step:      step,
			ToolName:  call.ToolName,
			RequestID: reqID,
			Message:   "patch approved",
		})
		s.Sink.SendEvent(server.Event{
			Kind:     server.EventToolStarted,
			Time:     time.Now(),
			Step:     step,
			ToolName: call.ToolName,
		})
		return s.Router.Execute(ctx, call)
	}

	// 理论上不会到这里
	return s.Router.Execute(ctx, call)
}

func (s *Session) waitForApproval(ctx context.Context, requestID string) (bool, error) {
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case d := <-s.approvals:
			if d.RequestID == requestID {
				return d.Approved, nil
			}
			// 非本请求的审批结果直接丢弃（当前实现只考虑串行审批）。
		}
	}
}
