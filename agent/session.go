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

	// history 记录会话内所有对话与工具轨迹，生命周期跟随 Session。
	history []server.ResponseItem
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

// ResetHistoryWithSystemPrompt 重置会话历史，并注入系统提示词（如为空则仅清空历史）。
func (s *Session) ResetHistoryWithSystemPrompt(systemPrompt string) {
	if s == nil {
		return
	}

	s.history = nil
	if strings.TrimSpace(systemPrompt) == "" {
		return
	}
	s.history = append(s.history, server.ResponseItem{
		Type: server.ResponseItemMessage,
		Role: server.RoleSystem,
		Text: systemPrompt,
	})
}

type turnContext struct {
	baseCtx  context.Context
	cm       *server.ContextManager
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
	s.Sink.SendEvent(server.Event{Kind: server.EventTurnStarted, Time: time.Now()})

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

	cm := server.NewContextManager(s.history)
	cm.Record(server.ResponseItem{
		Type: server.ResponseItemMessage,
		Role: server.RoleUser,
		Text: userInput,
	})

	return &turnContext{
		baseCtx:  baseCtx,
		cm:       cm,
		maxSteps: resolveMaxSteps(s.MaxSteps),
	}
}

// commitHistory 将 ContextManager 中的历史落到 Session 内部状态。
func (s *Session) commitHistory(cm *server.ContextManager) {
	if s == nil || cm == nil {
		return
	}
	s.history = cm.History()
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
	s.recordAssistantReply(turn.cm, reply)

	calls := s.resolveToolCalls(res, reply, step)
	if len(calls) == 0 {
		s.emitFinalReply(step, reply)
		return true, nil
	}

	s.ensureCallIDs(calls, step)
	s.emitToolPlan(step, reply)
	s.executeToolCalls(turn.baseCtx, turn.cm, calls, step)

	return false, nil
}

// buildPrompt 根据当前历史生成 Prompt。
func (s *Session) buildPrompt(cm *server.ContextManager) server.Prompt {
	return server.Prompt{
		Messages: cm.BuildPromptMessages(),
		Tools:    s.Router.Specs(),
		Items:    cm.History(),
	}
}

// callLLM 执行一次 LLM 调用，并在调用失败时记录日志。
func (s *Session) callLLM(baseCtx context.Context, prompt server.Prompt, step int) (*server.LLMResult, error) {
	log.Printf("[agent] step=%d calling LLM (history_items=%d, prompt_msgs=%d)", step, len(prompt.Items), len(prompt.Messages))

	// 为本次 LLM 调用单独设置超时，避免影响后续工具执行/审批流程。
	llmCtx, cancelLLM := context.WithTimeout(baseCtx, 60*time.Second)
	res, err := s.Client.Complete(llmCtx, prompt)
	cancelLLM()
	if err != nil {
		log.Printf("[agent] step=%d LLM error: %v", step, err)
		return nil, err
	}
	return res, nil
}

// logLLMReply 输出 LLM 回复摘要日志。
func (s *Session) logLLMReply(step int, reply string, toolCalls []server.ToolCall) {
	log.Printf("[agent] step=%d LLM reply_len=%d tool_calls=%d", step, len(reply), len(toolCalls))
	log.Printf("[agent] step=%d LLM reply preview:\n%s", step, previewLLMReplyForLog(reply))
}

// recordAssistantReply 将 LLM 输出写入上下文历史。
func (s *Session) recordAssistantReply(cm *server.ContextManager, reply string) {
	if strings.TrimSpace(reply) == "" {
		return
	}
	cm.Record(server.ResponseItem{
		Type: server.ResponseItemMessage,
		Role: server.RoleAssistant,
		Text: reply,
	})
}

// resolveToolCalls 解析工具调用，优先使用 function calling，失败时退回文本协议。
func (s *Session) resolveToolCalls(res *server.LLMResult, reply string, step int) []server.ToolCall {
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
func (s *Session) ensureCallIDs(calls []server.ToolCall, step int) {
	for i := range calls {
		if strings.TrimSpace(calls[i].CallID) != "" {
			continue
		}
		calls[i].CallID = fmt.Sprintf("local-%d-%d", step, i)
	}
}

// emitAgentThinking 发送 agent 思考事件。
func (s *Session) emitAgentThinking(step int) {
	s.Sink.SendEvent(server.Event{Kind: server.EventAgentThinking, Time: time.Now(), Step: step})
}

// emitFinalReply 发送最终回答事件并结束当前 turn。
func (s *Session) emitFinalReply(step int, reply string) {
	s.Sink.SendEvent(server.Event{
		Kind:    server.EventAgentTextDone,
		Time:    time.Now(),
		Step:    step,
		Message: reply,
	})
	s.Sink.SendEvent(server.Event{Kind: server.EventTurnFinished, Time: time.Now(), Step: step})
}

// emitToolPlan 发送工具规划事件。
func (s *Session) emitToolPlan(step int, reply string) {
	s.Sink.SendEvent(server.Event{
		Kind:    server.EventToolPlanned,
		Time:    time.Now(),
		Step:    step,
		Message: reply,
	})
}

// finishTurnDueToMaxSteps 在达到最大步数时输出终止事件。
func (s *Session) finishTurnDueToMaxSteps(maxSteps int) {
	s.Sink.SendEvent(server.Event{
		Kind:    server.EventTurnFinished,
		Time:    time.Now(),
		Step:    maxSteps,
		Message: "达到最大步数，终止",
	})
}

// executeToolCalls 执行所有工具调用并将结果写回历史。
func (s *Session) executeToolCalls(ctx context.Context, cm *server.ContextManager, calls []server.ToolCall, step int) {
	log.Printf("[agent] step=%d resolved %d tool_calls", step, len(calls))
	for _, call := range calls {
		s.executeSingleToolCall(ctx, cm, call, step)
	}
}

// executeSingleToolCall 执行单个工具调用，并将结果写回 ContextManager。
func (s *Session) executeSingleToolCall(ctx context.Context, cm *server.ContextManager, call server.ToolCall, step int) {
	log.Printf("[agent] step=%d executing tool=%s", step, call.ToolName)
	cm.Record(server.ResponseItem{
		Type:          server.ResponseItemToolCall,
		ToolName:      call.ToolName,
		ToolArguments: call.Arguments,
		CallID:        call.CallID,
	})

	item, execErr := s.executeToolCall(ctx, call, step)
	if execErr != nil {
		s.emitToolError(step, call.ToolName, execErr)
		log.Printf("[agent] step=%d tool=%s error=%v", step, call.ToolName, execErr)
		cm.Record(server.ResponseItem{
			Type:       server.ResponseItemToolResult,
			ToolName:   call.ToolName,
			ToolOutput: fmt.Sprintf("工具执行失败: %v", execErr),
			CallID:     call.CallID,
		})
		return
	}

	s.emitToolOutput(step, call.ToolName, item.ToolOutput)
	log.Printf("[agent] step=%d tool=%s done output_len=%d", step, call.ToolName, len(item.ToolOutput))

	cm.Record(server.ResponseItem{
		Type:       server.ResponseItemToolResult,
		ToolName:   item.ToolName,
		ToolOutput: item.ToolOutput,
		CallID:     call.CallID,
	})
}

// executeToolCall 处理工具调用分发和安全审批。
func (s *Session) executeToolCall(ctx context.Context, call server.ToolCall, step int) (server.ResponseItem, error) {
	if call.ToolName == "apply_patch" {
		return s.executeApplyPatchWithSafety(ctx, call, step)
	}
	return s.Router.Execute(ctx, call)
}

// emitToolOutput 输出工具结果事件。
func (s *Session) emitToolOutput(step int, toolName, output string) {
	s.Sink.SendEvent(server.Event{
		Kind:     server.EventToolOutputDelta,
		Time:     time.Now(),
		Step:     step,
		ToolName: toolName,
		Message:  output,
	})
	s.Sink.SendEvent(server.Event{
		Kind:     server.EventToolFinished,
		Time:     time.Now(),
		Step:     step,
		ToolName: toolName,
	})
}

// emitToolError 输出工具失败事件。
func (s *Session) emitToolError(step int, toolName string, err error) {
	s.Sink.SendEvent(server.Event{
		Kind:     server.EventToolFinished,
		Time:     time.Now(),
		Step:     step,
		ToolName: toolName,
		Message:  "工具执行失败: " + err.Error(),
	})
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
	args, err := s.parseApplyPatchArgs(call)
	if err != nil {
		return server.ResponseItem{}, err
	}

	decision := s.evaluatePatchDecision(args)
	return s.handlePatchDecision(ctx, call, step, decision)
}

type applyPatchArgs struct {
	File string `json:"file"`
	From string `json:"from"`
	To   string `json:"to"`
	All  bool   `json:"all"`
}

// parseApplyPatchArgs 解析 apply_patch 的参数。
func (s *Session) parseApplyPatchArgs(call server.ToolCall) (applyPatchArgs, error) {
	var args applyPatchArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return applyPatchArgs{}, fmt.Errorf("解析 apply_patch 参数失败: %w", err)
	}
	return args, nil
}

// evaluatePatchDecision 执行安全评估，并应用 SessionConfig 的审批策略。
func (s *Session) evaluatePatchDecision(args applyPatchArgs) servertools.PatchSafetyDecision {
	decision := servertools.EvaluateSimplePatchSafety(args.File, args.From, args.To, args.All)
	return s.applyPatchApprovalPolicy(decision)
}

// applyPatchApprovalPolicy 根据 SessionConfig 调整补丁审批等级。
func (s *Session) applyPatchApprovalPolicy(decision servertools.PatchSafetyDecision) servertools.PatchSafetyDecision {
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
	return decision
}

// handlePatchDecision 根据安全评估结果执行或请求审批。
func (s *Session) handlePatchDecision(ctx context.Context, call server.ToolCall, step int, decision servertools.PatchSafetyDecision) (server.ResponseItem, error) {
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
func (s *Session) executePatchTool(ctx context.Context, call server.ToolCall, step int) (server.ResponseItem, error) {
	s.Sink.SendEvent(server.Event{
		Kind:     server.EventToolStarted,
		Time:     time.Now(),
		Step:     step,
		ToolName: call.ToolName,
	})
	return s.Router.Execute(ctx, call)
}

// rejectPatch 处理被拒绝的补丁，返回错误原因。
func (s *Session) rejectPatch(call server.ToolCall, step int, reason string) (server.ResponseItem, error) {
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
}

// requestPatchApprovalAndExecute 发起审批请求，并在批准后执行补丁。
func (s *Session) requestPatchApprovalAndExecute(ctx context.Context, call server.ToolCall, step int, decision servertools.PatchSafetyDecision) (server.ResponseItem, error) {
	reqID := s.newPatchRequestID(step)
	s.emitPatchApprovalRequest(call, step, reqID, decision)

	approved, err := s.waitForApproval(ctx, reqID)
	if err != nil {
		return server.ResponseItem{}, err
	}
	if !approved {
		s.emitPatchApprovalResult(call, step, reqID, false)
		return server.ResponseItem{}, fmt.Errorf("补丁被用户拒绝")
	}

	s.emitPatchApprovalResult(call, step, reqID, true)
	return s.executePatchTool(ctx, call, step)
}

// newPatchRequestID 生成补丁审批请求的唯一 ID。
func (s *Session) newPatchRequestID(step int) string {
	return fmt.Sprintf("patch-%d-%d", time.Now().UnixNano(), step)
}

// emitPatchApprovalRequest 向 CLI 发送补丁审批请求事件。
func (s *Session) emitPatchApprovalRequest(call server.ToolCall, step int, reqID string, decision servertools.PatchSafetyDecision) {
	s.Sink.SendEvent(server.Event{
		Kind:      server.EventPatchApprovalRequest,
		Time:      time.Now(),
		Step:      step,
		ToolName:  call.ToolName,
		RequestID: reqID,
		Paths:     decision.Paths,
		Message:   decision.Reason,
	})
}

// emitPatchApprovalResult 向 CLI 发送补丁审批结果事件。
func (s *Session) emitPatchApprovalResult(call server.ToolCall, step int, reqID string, approved bool) {
	message := "patch rejected by user"
	if approved {
		message = "patch approved"
	}
	s.Sink.SendEvent(server.Event{
		Kind:      server.EventPatchApprovalResult,
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
