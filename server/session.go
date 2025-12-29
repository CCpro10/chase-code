package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"time"

	"chase-code/server/config"
	"chase-code/server/llm"
	"chase-code/server/persistence"
	"chase-code/server/prompt"
	servertools "chase-code/server/tools"
)

// Session 封装了一次基于 LLM+工具的对话会话（类似 codex-rs 的 Session），
// 负责驱动多轮 LLM 调用和工具调用，并通过 EventSink 将关键步骤发送给上层（如 CLI）。
type Session struct {
	ID       string
	Client   llm.LLMClient
	Router   *servertools.ToolRouter
	Sink     EventSink
	MaxSteps int // 单次 turn 允许的最多 LLM+工具循环步数，<=0 时使用默认 10

	Config config.SessionConfig

	// approvals 用于接收来自 CLI 的补丁审批结果。
	approvals chan ApprovalDecision

	// history 记录会话内所有对话与工具轨迹，生命周期跟随 Session。
	history []ResponseItem
}

// ApprovalDecision 表示一次补丁审批请求的结果。
type ApprovalDecision struct {
	RequestID string
	Approved  bool
}

// NewSession 创建一个带事件和审批通道的 Session。
func NewSession(client llm.LLMClient, router *servertools.ToolRouter, sink EventSink, maxSteps int) *Session {
	if maxSteps <= 0 {
		maxSteps = 10
	}
	cfg := config.DefaultSessionConfigFromEnv()
	return &Session{
		ID:        newSessionID(),
		Client:    client,
		Router:    router,
		Sink:      sink,
		MaxSteps:  maxSteps,
		Config:    cfg,
		approvals: make(chan ApprovalDecision, 1),
	}
}

// LoadHistory 从持久化存储加载历史记录。
func (s *Session) LoadHistory(id string) error {
	history, err := persistence.Load(id)
	if err != nil {
		return err
	}
	s.history = history
	s.ID = id // 切换到该会话 ID
	log.Printf("[session] loaded history for session %s (items=%d)", id, len(history))
	return nil
}

func newSessionID() string {
	now := time.Now()
	datePart := now.Format("20060102-150405")
	rnd := rand.New(rand.NewSource(now.UnixNano()))
	randPart := rnd.Intn(10000)
	return fmt.Sprintf("%s-%04d", datePart, randPart)
}

// ApprovalsChan 返回一个写端通道，供 CLI 将用户审批结果写回。
func (s *Session) ApprovalsChan() chan<- ApprovalDecision {
	return s.approvals
}

// ResetHistoryWithSystemPrompt 重置会话历史，并注入系统提示词（如为空则仅清空历史）。
func (s *Session) ResetHistoryWithSystemPrompt(systemPrompt string) {
	if s == nil {
		return
	}

	s.history = nil
	if strings.TrimSpace(systemPrompt) == "" {
		return
	}
	s.history = append(s.history, ResponseItem{
		Type: ResponseItemMessage,
		Role: RoleSystem,
		Text: systemPrompt,
	})
}

// AppendEnvironmentContext appends a codex-style environment context message.
func (s *Session) AppendEnvironmentContext(contextText string) {
	if s == nil {
		return
	}
	if strings.TrimSpace(contextText) == "" {
		return
	}
	s.history = append(s.history, ResponseItem{
		Type: ResponseItemMessage,
		Role: RoleUser,
		Text: contextText,
	})
}

type turnContext struct {
	baseCtx  context.Context
	cm       *ContextManager
	maxSteps int
}

// RunTurn 执行一轮用户指令：
//   - 使用 Session 内部历史，并将 userInput 追加进去；
//   - 在最多 MaxSteps 步内，反复调用 LLM 和工具；
//   - 当 LLM 不再返回工具调用 JSON 时，将其视为最终回答并结束。
func (s *Session) RunTurn(ctx context.Context, userInput string) error {
	if s == nil || s.Client == nil || s.Router == nil {
		return nil
	}

	turn := s.newTurnContext(ctx, userInput)
	defer s.commitHistory(turn.cm)

	log.Printf("[agent] new turn input=%q history_len=%d", userInput, len(s.history))
	s.Sink.SendEvent(Event{Kind: EventTurnStarted, Time: time.Now()})

	for step := 0; step < turn.maxSteps; step++ {
		done, err := s.runTurnStep(turn, step)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}

	s.finishTurnDueToMaxSteps(turn.maxSteps)
	return nil
}

// newTurnContext 构建一次 turn 的上下文信息，并把用户输入写入历史。
func (s *Session) newTurnContext(ctx context.Context, userInput string) *turnContext {
	baseCtx := ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}

	cm := NewContextManager(s.history)
	cm.Record(ResponseItem{
		Type: ResponseItemMessage,
		Role: RoleUser,
		Text: userInput,
	})

	return &turnContext{
		baseCtx:  baseCtx,
		cm:       cm,
		maxSteps: resolveMaxSteps(s.MaxSteps),
	}
}

// commitHistory 将 ContextManager 中的历史落到 Session 内部状态。
func (s *Session) commitHistory(cm *ContextManager) {
	if s == nil || cm == nil {
		return
	}
	s.history = cm.History()
	if err := persistence.Save(s.ID, s.history); err != nil {
		log.Printf("[session] failed to save session %s: %v", s.ID, err)
	}
}

// resolveMaxSteps 确保最大步数始终是一个可用的正数。
func resolveMaxSteps(maxSteps int) int {
	if maxSteps <= 0 {
		return 10
	}
	return maxSteps
}

// runTurnStep 执行单步 LLM + 工具调用，返回是否已经结束本次 turn。
func (s *Session) runTurnStep(turn *turnContext, step int) (bool, error) {
	s.emitAgentThinking(step)

	prompt := s.buildPrompt(turn.cm)
	res, err := s.callLLM(turn.baseCtx, prompt, step)
	if err != nil {
		return true, err
	}

	reply := res.Message.Content
	s.logLLMReply(step, reply, res.ToolCalls)

	calls := s.resolveToolCalls(res, reply, step)
	s.ensureCallIDs(calls, step)
	s.recordAssistantReply(turn.cm, reply, calls)
	if len(calls) == 0 {
		s.emitFinalReply(step, reply)
		return true, nil
	}

	s.emitToolPlan(step, reply)
	s.executeToolCalls(turn.baseCtx, turn.cm, calls, step)

	return false, nil
}

// buildPrompt 根据当前历史生成 Prompt。
func (s *Session) buildPrompt(cm *ContextManager) Prompt {
	return Prompt{
		Messages: cm.BuildPromptMessages(),
		Tools:    s.Router.Specs(),
		Items:    cm.History(),
	}
}

// callLLM 执行一次 LLM 调用，并在调用失败时记录日志。
func (s *Session) callLLM(baseCtx context.Context, prompt Prompt, step int) (*llm.LLMResult, error) {
	log.Printf("[agent] step=%d calling LLM (history_items=%d, prompt_msgs=%d)", step, len(prompt.Items), len(prompt.Messages))

	// 为本次 LLM 调用单独设置超时，避免影响后续工具执行/审批流程。
	llmCtx, cancelLLM := context.WithTimeout(baseCtx, 120*time.Second) // 增加超时以适应流式传输
	defer cancelLLM()

	stream := s.Client.Stream(llmCtx, prompt)

	var lastError error
	var finalResult *llm.LLMResult

	for ev := range stream.C {
		switch ev.Kind {
		case llm.LLMEventTextDelta:
			// 仅当 delta 不为空时才发送，避免不必要的 UI 刷新
			if ev.TextDelta != "" {
				s.Sink.SendEvent(Event{
					Kind:    EventAgentTextDelta,
					Time:    time.Now(),
					Step:    step,
					Message: ev.TextDelta,
				})
			}
		case llm.LLMEventError:
			lastError = ev.Error
		case llm.LLMEventCompleted:
			finalResult = ev.Result
		}
	}

	if lastError != nil {
		log.Printf("[agent] step=%d LLM error: %v", step, lastError)
		return nil, lastError
	}

	if finalResult == nil {
		return nil, fmt.Errorf("LLM stream completed without result")
	}

	return finalResult, nil
}

// logLLMReply 输出 LLM 回复摘要日志。
func (s *Session) logLLMReply(step int, reply string, toolCalls []servertools.ToolCall) {
	log.Printf("[agent] step=%d LLM reply_len=%d tool_calls=%d", step, len(reply), len(toolCalls))
	log.Printf("[agent] step=%d LLM reply preview:\n%s", step, previewLLMReplyForLog(reply))
}

// recordAssistantReply 将 LLM 输出及其工具调用写入上下文历史。
func (s *Session) recordAssistantReply(cm *ContextManager, reply string, calls []servertools.ToolCall) {
	if strings.TrimSpace(reply) == "" && len(calls) == 0 {
		return
	}
	item := ResponseItem{
		Type: ResponseItemMessage,
		Role: RoleAssistant,
		Text: reply,
	}
	if len(calls) > 0 {
		item.ToolCalls = cloneToolCalls(calls)
	}
	cm.Record(item)
}

// cloneToolCalls 深拷贝工具调用，避免后续修改影响历史记录。
func cloneToolCalls(calls []servertools.ToolCall) []servertools.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]servertools.ToolCall, 0, len(calls))
	for _, call := range calls {
		copied := call
		if len(call.Arguments) > 0 {
			args := make(json.RawMessage, len(call.Arguments))
			copy(args, call.Arguments)
			copied.Arguments = args
		}
		out = append(out, copied)
	}
	return out
}

// resolveToolCalls 解析工具调用，优先使用 function calling，失败时退回文本协议。
func (s *Session) resolveToolCalls(res *llm.LLMResult, reply string, step int) []servertools.ToolCall {
	if len(res.ToolCalls) > 0 {
		return res.ToolCalls
	}

	calls, err := servertools.ParseToolCallsJSON(reply)
	if err != nil {
		log.Printf("[agent] step=%d parse tool_calls from text failed: %v", step, err)
		return nil
	}
	return calls
}

// ensureCallIDs 确保每个工具调用都有稳定的 CallID。
func (s *Session) ensureCallIDs(calls []servertools.ToolCall, step int) {
	for i := range calls {
		if strings.TrimSpace(calls[i].CallID) != "" {
			continue
		}
		calls[i].CallID = fmt.Sprintf("local-%d-%d", step, i)
	}
}

// emitAgentThinking 发送 agent 思考事件。
func (s *Session) emitAgentThinking(step int) {
	s.Sink.SendEvent(Event{Kind: EventAgentThinking, Time: time.Now(), Step: step})
}

// emitFinalReply 发送最终回答事件并结束当前 turn。
func (s *Session) emitFinalReply(step int, reply string) {
	s.Sink.SendEvent(Event{
		Kind:    EventAgentTextDone,
		Time:    time.Now(),
		Step:    step,
		Message: reply,
	})
	s.Sink.SendEvent(Event{Kind: EventTurnFinished, Time: time.Now(), Step: step})
}

// emitToolPlan 发送工具规划事件。
func (s *Session) emitToolPlan(step int, reply string) {
	s.Sink.SendEvent(Event{
		Kind:    EventToolPlanned,
		Time:    time.Now(),
		Step:    step,
		Message: reply,
	})
}

// finishTurnDueToMaxSteps 在达到最大步数时输出终止事件。
func (s *Session) finishTurnDueToMaxSteps(maxSteps int) {
	s.Sink.SendEvent(Event{
		Kind:    EventTurnFinished,
		Time:    time.Now(),
		Step:    maxSteps,
		Message: "达到最大步数，终止",
	})
}

// executeToolCalls 执行所有工具调用并将结果写回历史。
func (s *Session) executeToolCalls(ctx context.Context, cm *ContextManager, calls []servertools.ToolCall, step int) {
	log.Printf("[agent] step=%d resolved %d tool_calls", step, len(calls))
	for _, call := range calls {
		s.executeSingleToolCall(ctx, cm, call, step)
	}
}

// executeSingleToolCall 执行单个工具调用，并将结果写回 ContextManager。
func (s *Session) executeSingleToolCall(ctx context.Context, cm *ContextManager, call servertools.ToolCall, step int) {
	log.Printf("[agent] step=%d executing tool=%s", step, call.ToolName)

	item, execErr := s.executeToolCall(ctx, call, step)
	if execErr != nil {
		s.emitToolError(step, call.ToolName, execErr)
		log.Printf("[agent] step=%d tool=%s error=%v", step, call.ToolName, execErr)
		cm.Record(ResponseItem{
			Type:       ResponseItemToolResult,
			ToolName:   call.ToolName,
			ToolOutput: fmt.Sprintf("工具执行失败: %v", execErr),
			CallID:     call.CallID,
		})
		return
	}

	s.emitToolOutput(step, call.ToolName, item.ToolOutput)
	log.Printf("[agent] step=%d tool=%s done output_len=%d", step, call.ToolName, len(item.ToolOutput))

	cm.Record(ResponseItem{
		Type:       ResponseItemToolResult,
		ToolName:   item.ToolName,
		ToolOutput: item.ToolOutput,
		CallID:     call.CallID,
	})
}

// executeToolCall 处理工具调用分发和安全审批。
func (s *Session) executeToolCall(ctx context.Context, call servertools.ToolCall, step int) (ResponseItem, error) {
	if call.ToolName == "apply_patch" {
		return s.executeApplyPatchWithSafety(ctx, call, step)
	}
	res, err := s.Router.Execute(ctx, call)
	if err != nil {
		return ResponseItem{}, err
	}
	return ResponseItem{
		Type:       ResponseItemToolResult,
		ToolName:   res.ToolName,
		ToolOutput: res.Output,
	}, nil
}

// emitToolOutput 输出工具结果事件。
func (s *Session) emitToolOutput(step int, toolName, output string) {
	s.Sink.SendEvent(Event{
		Kind:     EventToolOutputDelta,
		Time:     time.Now(),
		Step:     step,
		ToolName: toolName,
		Message:  output,
	})
	s.Sink.SendEvent(Event{
		Kind:     EventToolFinished,
		Time:     time.Now(),
		Step:     step,
		ToolName: toolName,
	})
}

// emitToolError 输出工具失败事件。
func (s *Session) emitToolError(step int, toolName string, err error) {
	s.Sink.SendEvent(Event{
		Kind:     EventToolFinished,
		Time:     time.Now(),
		Step:     step,
		ToolName: toolName,
		Message:  "工具执行失败: " + err.Error(),
	})
}

const (
	llmReplyPreviewMaxRunes = 1024
	llmReplyPreviewMaxLines = 20000
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
func (s *Session) executeApplyPatchWithSafety(ctx context.Context, call servertools.ToolCall, step int) (ResponseItem, error) {
	req, err := servertools.ParseApplyPatchArguments(call.Arguments)
	if err != nil {
		return ResponseItem{}, err
	}

	decision := s.evaluatePatchDecision(req)
	return s.handlePatchDecision(ctx, call, step, decision)
}

// evaluatePatchDecision 执行安全评估，并应用 SessionConfig 的审批策略。
func (s *Session) evaluatePatchDecision(req servertools.ApplyPatchRequest) servertools.PatchSafetyDecision {
	decision := servertools.EvaluatePatchSafety(req.Summary)
	return s.applyPatchApprovalPolicy(decision)
}

// applyPatchApprovalPolicy 根据 SessionConfig 调整补丁审批等级。
func (s *Session) applyPatchApprovalPolicy(decision servertools.PatchSafetyDecision) servertools.PatchSafetyDecision {
	switch s.Config.ToolApproval.ApplyPatch {
	case config.ApprovalModeAlwaysAsk:
		if decision.Level == servertools.PatchSafe {
			decision.Level = servertools.PatchAskUser
		}
	case config.ApprovalModeAlwaysApprove:
		if decision.Level == servertools.PatchAskUser {
			decision.Level = servertools.PatchSafe
		}
	case config.ApprovalModeAuto:
		// 保持原有决策
	}
	return decision
}

// handlePatchDecision 根据安全评估结果执行或请求审批。
func (s *Session) handlePatchDecision(ctx context.Context, call servertools.ToolCall, step int, decision servertools.PatchSafetyDecision) (ResponseItem, error) {
	switch decision.Level {
	case servertools.PatchSafe:
		return s.executePatchTool(ctx, call, step)
	case servertools.PatchReject:
		return s.rejectPatch(call, step, decision.Reason)
	case servertools.PatchAskUser:
		return s.requestPatchApprovalAndExecute(ctx, call, step, decision)
	default:
		return s.executePatchTool(ctx, call, step)
	}
}

// executePatchTool 执行补丁工具调用，并发出开始事件。
func (s *Session) executePatchTool(ctx context.Context, call servertools.ToolCall, step int) (ResponseItem, error) {
	s.Sink.SendEvent(Event{
		Kind:     EventToolStarted,
		Time:     time.Now(),
		Step:     step,
		ToolName: call.ToolName,
	})
	res, err := s.Router.Execute(ctx, call)
	if err != nil {
		return ResponseItem{}, err
	}
	return ResponseItem{
		Type:       ResponseItemToolResult,
		ToolName:   res.ToolName,
		ToolOutput: res.Output,
	}, nil
}

// rejectPatch 处理被拒绝的补丁，返回错误原因。
func (s *Session) rejectPatch(call servertools.ToolCall, step int, reason string) (ResponseItem, error) {
	if reason == "" {
		reason = "补丁被安全策略拒绝"
	}
	s.Sink.SendEvent(Event{
		Kind:     EventToolFinished,
		Time:     time.Now(),
		Step:     step,
		ToolName: call.ToolName,
		Message:  "patch rejected: " + reason,
	})
	return ResponseItem{}, fmt.Errorf("补丁被拒绝: %s", reason)
}

// requestPatchApprovalAndExecute 发起审批请求，并在批准后执行补丁。
func (s *Session) requestPatchApprovalAndExecute(ctx context.Context, call servertools.ToolCall, step int, decision servertools.PatchSafetyDecision) (ResponseItem, error) {
	reqID := s.newPatchRequestID(step)
	s.emitPatchApprovalRequest(call, step, reqID, decision)

	approved, err := s.waitForApproval(ctx, reqID)
	if err != nil {
		return ResponseItem{}, err
	}
	if !approved {
		s.emitPatchApprovalResult(call, step, reqID, false)
		return ResponseItem{}, fmt.Errorf("补丁被用户拒绝")
	}

	s.emitPatchApprovalResult(call, step, reqID, true)
	return s.executePatchTool(ctx, call, step)
}

// newPatchRequestID 生成补丁审批请求的唯一 ID。
func (s *Session) newPatchRequestID(step int) string {
	return fmt.Sprintf("patch-%d-%d", time.Now().UnixNano(), step)
}

// emitPatchApprovalRequest 向 CLI 发送补丁审批请求事件。
func (s *Session) emitPatchApprovalRequest(call servertools.ToolCall, step int, reqID string, decision servertools.PatchSafetyDecision) {
	s.Sink.SendEvent(Event{
		Kind:      EventPatchApprovalRequest,
		Time:      time.Now(),
		Step:      step,
		ToolName:  call.ToolName,
		RequestID: reqID,
		Paths:     decision.Paths,
		Message:   decision.Reason,
	})
}

// emitPatchApprovalResult 向 CLI 发送补丁审批结果事件。
func (s *Session) emitPatchApprovalResult(call servertools.ToolCall, step int, reqID string, approved bool) {
	message := "patch rejected by user"
	if approved {
		message = "patch approved"
	}
	s.Sink.SendEvent(Event{
		Kind:      EventPatchApprovalResult,
		Time:      time.Now(),
		Step:      step,
		ToolName:  call.ToolName,
		RequestID: reqID,
		Message:   message,
	})
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

// ManualCompactHistory 手动触发上下文压缩。
// 会调用 LLM 生成摘要，并用摘要替换当前历史记录（保留 System Prompt）。
func (s *Session) ManualCompactHistory(ctx context.Context) (string, error) {
	if len(s.history) <= 2 {
		return "历史记录太短，无需压缩", nil
	}

	start := time.Now()
	log.Printf("[session] starting manual compaction items=%d", len(s.history))

	// 1. 构造摘要生成的 Prompt
	// 使用当前的完整历史，但追加一条 Summarization Prompt
	cm := NewContextManager(s.history)
	cm.Record(ResponseItem{
		Type: ResponseItemMessage,
		Role: RoleUser,
		Text: prompt.GetCompactPrompt(),
	})

	compactPrompt := Prompt{
		Messages: cm.BuildPromptMessages(),
		// 摘要任务通常不需要 Tools，减少 Token 消耗
		Items: cm.History(),
	}

	// 2. 调用 LLM 生成摘要 (非流式)
	res, err := s.Client.Complete(ctx, compactPrompt)
	if err != nil {
		return "", fmt.Errorf("生成摘要失败: %w", err)
	}
	summary := res.Message.Content
	log.Printf("[session] compaction summary generated len=%d elapsed=%s", len(summary), time.Since(start))

	// 3. 重建历史
	var newHistory []ResponseItem

	// 3.1 保留 System Prompt
	if len(s.history) > 0 && s.history[0].Role == RoleSystem {
		newHistory = append(newHistory, s.history[0])
	} else {
		// 理论上应该有，如果没有就补一个默认的或者跳过
	}

	// 3.2 插入摘要作为 User Message (参考 Codex 逻辑，或者 System Message)
	// 为了让 LLM 明确知道这是历史摘要，我们加个前缀
	summaryMsg := fmt.Sprintf("这是之前对话的历史摘要（Context Compacted）：\n\n%s", summary)
	newHistory = append(newHistory, ResponseItem{
		Type: ResponseItemMessage,
		Role: RoleUser, // 或者 RoleSystem，视模型偏好而定。RoleUser 通常比较稳妥，像用户提供了背景。
		Text: summaryMsg,
	})

	// 3.3 替换当前历史
	s.history = newHistory

	// 3.4 立即持久化
	if err := persistence.Save(s.ID, s.history); err != nil {
		log.Printf("[session] failed to save compacted session: %v", err)
	}

	return summary, nil
}

// BuildToolSystemPrompt 基于工具列表构造一段 system prompt。
func BuildToolSystemPrompt(tools []servertools.ToolSpec) string {
	s, err := prompt.BuildSystemPrompt(tools)
	if err != nil {
		log.Printf("加载 System Prompt 失败: %v，使用简易回退", err)
		return "System prompt load failed."
	}
	return s
}
