package agent

import (
	"context"
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
}

// RunTurn 执行一轮用户指令：
//   - 先将 userInput 追加到历史消息中；
//   - 在最多 MaxSteps 步内，反复调用 LLM 和工具；
//   - 当 LLM 不再返回工具调用 JSON 时，将其视为最终回答并结束。
//
// 返回更新后的对话历史。
func (s *Session) RunTurn(ctx context.Context, userInput string, history []server.Message) ([]server.Message, error) {
	if s == nil || s.Client == nil || s.Router == nil {
		return history, nil
	}

	maxSteps := s.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 10
	}

	msgs := append(history, server.Message{Role: server.RoleUser, Content: userInput})

	if s.Sink != nil {
		s.Sink.SendEvent(server.Event{Kind: server.EventTurnStarted, Time: time.Now()})
	}

	baseCtx := ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}

	for step := 0; step < maxSteps; step++ {
		// 1) 调用 LLM
		if s.Sink != nil {
			s.Sink.SendEvent(server.Event{Kind: server.EventAgentThinking, Time: time.Now(), Step: step})
		}

		prompt := server.Prompt{Messages: msgs}

		callCtx, cancel := context.WithTimeout(baseCtx, 60*time.Second)
		res, err := s.Client.Complete(callCtx, prompt)
		cancel()
		if err != nil {
			return msgs, err
		}
		reply := res.Message.Content

		// 2) 解析为工具调用 JSON；若失败则视为最终回答
		calls, err := servertools.ParseToolCallsJSON(reply)
		if err != nil || len(calls) == 0 {
			if s.Sink != nil {
				s.Sink.SendEvent(server.Event{
					Kind:    server.EventAgentTextDone,
					Time:    time.Now(),
					Step:    step,
					Message: reply,
				})
				s.Sink.SendEvent(server.Event{Kind: server.EventTurnFinished, Time: time.Now(), Step: step})
			}
			msgs = append(msgs, server.Message{Role: server.RoleAssistant, Content: reply})
			return msgs, nil
		}

		// 有工具调用时，先发送“规划”事件，方便 CLI 展示原始 JSON
		if s.Sink != nil {
			s.Sink.SendEvent(server.Event{
				Kind:    server.EventToolPlanned,
				Time:    time.Now(),
				Step:    step,
				Message: reply,
			})
		}

		// 3) 依次执行所有工具调用，将输出写回历史，供下一轮 LLM 使用
		for _, c := range calls {
			if s.Sink != nil {
				s.Sink.SendEvent(server.Event{
					Kind:     server.EventToolStarted,
					Time:     time.Now(),
					Step:     step,
					ToolName: c.ToolName,
				})
			}

			item, err := s.Router.Execute(callCtx, c)
			if err != nil {
				if s.Sink != nil {
					s.Sink.SendEvent(server.Event{
						Kind:     server.EventToolFinished,
						Time:     time.Now(),
						Step:     step,
						ToolName: c.ToolName,
						Message:  "工具执行失败: " + err.Error(),
					})
				}
				continue
			}

			if s.Sink != nil {
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
			}

			// 把工具结果写回对话历史，便于后续 LLM 使用
			msgs = append(msgs,
				server.Message{Role: server.RoleAssistant, Content: "工具 " + item.ToolName + " 的输出:\n" + item.ToolOutput},
			)
		}
	}

	// 达到最大步数仍未返回最终回答
	if s.Sink != nil {
		s.Sink.SendEvent(server.Event{
			Kind:    server.EventTurnFinished,
			Time:    time.Now(),
			Step:    s.MaxSteps,
			Message: "达到最大步数，终止",
		})
	}
	return msgs, nil
}
