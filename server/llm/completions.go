package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

// --------------------- Completions(messages) 实现 ---------------------

type CompletionsClient struct {
	cfg        clientConfig
	httpClient *http.Client
}

type completionsChatRequest struct {
	Model      string                    `json:"model"`
	Messages   []completionsChatMessage  `json:"messages"`
	Stream     bool                      `json:"stream"`
	Tools      []completionsFunctionTool `json:"tools,omitempty"`
	ToolChoice json.RawMessage           `json:"tool_choice,omitempty"`
}

type completionsChatMessage struct {
	Role       string                `json:"role"`
	Content    string                `json:"content,omitempty"`
	Name       string                `json:"name,omitempty"`
	ToolCallID string                `json:"tool_call_id,omitempty"`
	ToolCalls  []completionsToolCall `json:"tool_calls,omitempty"`
}

type completionsToolCall struct {
	ID       string                      `json:"id"`
	Type     string                      `json:"type"`
	Function completionsToolCallFunction `json:"function"`
}

type completionsToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// completionsFunctionTool / completionsFunction 定义了 Completions tools/function calling 所需的最小结构。
// 目前只在 buildChatRequest 中使用，用于在 Prompt.Tools 非空时构造 tools 数组，
// 行为与现有实现保持兼容：如果没有提供 ToolSpec 或缺少参数模式，则不会附带 tools 字段。
type completionsFunctionTool struct {
	Type     string                 `json:"type"` // 始终为 "function"
	Function completionsFunctionDef `json:"function"`
}

type completionsFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// buildMessagesFromItems 将高层的 ResponseItem 历史转换为 Completions Chat 消息。
// 这里会：
//   - 将普通对话消息映射为 user/assistant 等角色；
//   - 将工具输出映射为 role:"tool" 的消息，并在可能的情况下携带 tool_call_id；
//   - 对 assistant 消息携带的 tool_calls 直接写入同一条消息。
func buildMessagesFromItems(items []ResponseItem) []completionsChatMessage {
	msgs := make([]completionsChatMessage, 0, len(items))
	for _, it := range items {
		if msg, ok := messageFromItem(it); ok {
			msgs = append(msgs, msg)
		}
	}
	return msgs
}

// messageFromItem 将 ResponseItem 映射为 Completions Chat 消息。
func messageFromItem(it ResponseItem) (completionsChatMessage, bool) {
	switch it.Type {
	case ResponseItemMessage:
		msg := completionsChatMessage{
			Role:    string(it.Role),
			Content: it.Text,
		}
		if it.Role == RoleAssistant && len(it.ToolCalls) > 0 {
			msg.ToolCalls = completionsToolCallsFromToolCalls(it.ToolCalls)
		}
		return msg, true

	case ResponseItemToolResult:
		if it.ToolName == "" && it.ToolOutput == "" {
			return completionsChatMessage{}, false
		}
		return completionsChatMessage{
			Role:       "tool",
			Name:       it.ToolName,
			ToolCallID: it.CallID,
			Content:    it.ToolOutput,
		}, true

	case ResponseItemToolCall:
		// 兼容旧历史结构：独立的 tool_call 仍然映射为一条 assistant 消息。
		calls := completionsToolCallsFromToolCalls([]ToolCall{{
			ToolName:  it.ToolName,
			Arguments: it.ToolArguments,
			CallID:    it.CallID,
		}})
		if len(calls) == 0 {
			return completionsChatMessage{}, false
		}
		return completionsChatMessage{
			Role:      "assistant",
			ToolCalls: calls,
		}, true
	default:
		return completionsChatMessage{}, false
	}
}

// completionsToolCallsFromToolCalls 转换内部 ToolCall 为 Completions tool_calls 结构。
func completionsToolCallsFromToolCalls(calls []ToolCall) []completionsToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]completionsToolCall, 0, len(calls))
	for _, call := range calls {
		if strings.TrimSpace(call.ToolName) == "" {
			continue
		}
		args := strings.TrimSpace(string(call.Arguments))
		if args == "" {
			args = "{}"
		}
		out = append(out, completionsToolCall{
			ID:   call.CallID,
			Type: "function",
			Function: completionsToolCallFunction{
				Name:      call.ToolName,
				Arguments: args,
			},
		})
	}
	return out
}

// completionsChatResponse 对应 Completions 的响应结构。
// 为了后续支持 tools/function calling，这里预留了 tool_calls 字段，
// 当前实现仍然只使用 message.content 字段驱动现有的工具 JSON 协议。
type completionsChatResponse struct {
	Choices []completionsChatChoice `json:"choices"`
}

type completionsChatChoice struct {
	Message completionsChatResponseMessage `json:"message"`
}

type completionsChatResponseMessage struct {
	Role      string                `json:"role"`
	Content   string                `json:"content"`
	ToolCalls []completionsToolCall `json:"tool_calls,omitempty"`
}

// buildChatRequest 仿照 codex 的做法，将 Prompt 映射为 Completions 请求体：
//   - 优先基于 Prompt.Items 构造 messages（便于使用 tool role）；如 Items 为空则回退到 Prompt.Messages；
//   - 如有需要，后续可以在这里将 Prompt.Tools 转换为 tools/function 调用。
//
// 当前实现只在 Prompt.Tools 中存在带参数模式的 ToolSpec 时才填充 tools 字段，
// 对现有行为完全兼容（默认不会启用 function calling）。
func (c *CompletionsClient) buildChatRequest(p Prompt) completionsChatRequest {
	msgs := buildChatMessages(p)
	req := completionsChatRequest{Model: c.cfg.Model, Messages: msgs, Stream: false}

	tools := buildToolDefinitions(p.Tools)
	if len(tools) > 0 {
		req.Tools = tools
		req.ToolChoice = json.RawMessage(`"auto"`)
	}

	return req
}

// buildChatMessages 将 Prompt 转换为 Completions Chat 消息列表。
func buildChatMessages(p Prompt) []completionsChatMessage {
	if len(p.Items) > 0 {
		return buildMessagesFromItems(p.Items)
	}
	msgs := make([]completionsChatMessage, 0, len(p.Messages))
	for _, m := range p.Messages {
		msgs = append(msgs, completionsChatMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
		})
	}
	return msgs
}

// buildToolDefinitions 构建 Completions function tools 定义。
func buildToolDefinitions(tools []ToolSpec) []completionsFunctionTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]completionsFunctionTool, 0, len(tools))
	for _, t := range tools {
		if len(t.Parameters) == 0 || string(t.Parameters) == "null" {
			continue
		}
		out = append(out, completionsFunctionTool{
			Type: "function",
			Function: completionsFunctionDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}
	return out
}

func (c *CompletionsClient) Complete(ctx context.Context, p Prompt) (*LLMResult, error) {
	url := c.chatCompletionsURL()
	reqBody := c.buildChatRequest(p)

	data, pretty, err := marshalRequestBody(reqBody)
	if err != nil {
		return nil, err
	}
	logRequest(c.cfg, url, data, pretty)

	start := time.Now()
	respBody, status, err := c.doChatRequest(ctx, url, data, start)
	if err != nil {
		return nil, err
	}
	logRawResponse(status, respBody)

	if status/100 != 2 {
		log.Printf("[llm] non-2xx status=%d (elapsed=%s)", status, time.Since(start))
		return nil, fmt.Errorf("Completions API 返回非 2xx 状态码: %d, body: %s", status, string(respBody))
	}

	resp, err := decodeChatResponse(respBody, start)
	if err != nil {
		return nil, err
	}

	msg, err := firstChoiceMessage(resp, start)
	if err != nil {
		return nil, err
	}

	log.Printf("[llm] success alias=%s model=%s elapsed=%s", c.cfg.Alias, c.cfg.Model, time.Since(start))
	return buildLLMResult(msg), nil
}

// chatCompletionsURL 返回完整的 chat/completions URL。
func (c *CompletionsClient) chatCompletionsURL() string {
	return fmt.Sprintf("%s/chat/completions", c.cfg.BaseURL)
}

// doChatRequest 发送请求并返回响应体与状态码。
func (c *CompletionsClient) doChatRequest(ctx context.Context, url string, data []byte, start time.Time) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		log.Printf("[llm] new request error: %v", err)
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("[llm] http error: %v (elapsed=%s)", err, time.Since(start))
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		log.Printf("[llm] read response error: %v (elapsed=%s)", readErr, time.Since(start))
		return nil, resp.StatusCode, readErr
	}
	return respBody, resp.StatusCode, nil
}

// decodeChatResponse 解析响应 JSON。
func decodeChatResponse(respBody []byte, start time.Time) (completionsChatResponse, error) {
	var out completionsChatResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		log.Printf("[llm] decode response error: %v (elapsed=%s)", err, time.Since(start))
		return completionsChatResponse{}, err
	}
	return out, nil
}

// firstChoiceMessage 获取首条消息，确保 choices 非空。
func firstChoiceMessage(resp completionsChatResponse, start time.Time) (completionsChatResponseMessage, error) {
	if len(resp.Choices) == 0 {
		log.Printf("[llm] empty choices (elapsed=%s)", time.Since(start))
		return completionsChatResponseMessage{}, errors.New("Completions 响应中没有 choices")
	}
	return resp.Choices[0].Message, nil
}

// buildLLMResult 将响应消息转换为内部结构。
func buildLLMResult(msg completionsChatResponseMessage) *LLMResult {
	return &LLMResult{
		Message: LLMMessage{
			Role:    Role(msg.Role),
			Content: msg.Content,
		},
		ToolCalls: extractToolCalls(msg),
	}
}

// extractToolCalls 提取 Completions 返回的 tool_calls。
func extractToolCalls(msg completionsChatResponseMessage) []ToolCall {
	if len(msg.ToolCalls) == 0 {
		return nil
	}
	toolCalls := make([]ToolCall, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		if tc.Function.Name == "" {
			continue
		}
		toolCalls = append(toolCalls, ToolCall{
			ToolName:  tc.Function.Name,
			Arguments: json.RawMessage(tc.Function.Arguments),
			CallID:    tc.ID,
		})
	}
	return toolCalls
}

// Stream 以流式接口返回响应。
func (c *CompletionsClient) Stream(ctx context.Context, p Prompt) *LLMStream {
	ch := make(chan LLMEvent, 128)
	stream := &LLMStream{C: ch}

	go func() {
		defer close(ch)

		url := c.chatCompletionsURL()
		reqBody := c.buildChatRequest(p)
		reqBody.Stream = true

		data, pretty, err := marshalRequestBody(reqBody)
		if err != nil {
			stream.Err = err
			ch <- LLMEvent{Kind: LLMEventError, Error: err}
			return
		}
		logRequest(c.cfg, url, data, pretty)

		start := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			stream.Err = err
			ch <- LLMEvent{Kind: LLMEventError, Error: err}
			return
		}
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			log.Printf("[llm] http error: %v (elapsed=%s)", err, time.Since(start))
			stream.Err = err
			ch <- LLMEvent{Kind: LLMEventError, Error: err}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode/100 != 2 {
			body, _ := io.ReadAll(resp.Body)
			log.Printf("[llm] non-2xx status=%d body=%s", resp.StatusCode, string(body))
			err := fmt.Errorf("Completions API error: %d %s", resp.StatusCode, string(body))
			stream.Err = err
			ch <- LLMEvent{Kind: LLMEventError, Error: err}
			return
		}

		ch <- LLMEvent{Kind: LLMEventCreated}

		scanner := bufio.NewScanner(resp.Body)
		var fullTextBuilder strings.Builder

		// toolCallsMap 用于收集流式的 tool calls: index -> *completionsToolCall
		toolCallsMap := make(map[int]*completionsToolCall)

		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := strings.TrimPrefix(line, "data: ")
			if payload == "[DONE]" {
				break
			}

			var streamResp completionsChatStreamResponse
			if err := json.Unmarshal([]byte(payload), &streamResp); err != nil {
				log.Printf("[llm] decode stream error: %v payload=%s", err, payload)
				continue
			}

			if len(streamResp.Choices) > 0 {
				delta := streamResp.Choices[0].Delta

				// 1. 处理文本内容
				if delta.Content != "" {
					fullTextBuilder.WriteString(delta.Content)
					ch <- LLMEvent{Kind: LLMEventTextDelta, TextDelta: delta.Content}
				}

				// 2. 处理 Tool Calls
				for _, tc := range delta.ToolCalls {
					idx := tc.Index
					if _, ok := toolCallsMap[idx]; !ok {
						toolCallsMap[idx] = &completionsToolCall{}
					}
					target := toolCallsMap[idx]

					if tc.ID != "" {
						target.ID = tc.ID
					}
					if tc.Type != "" {
						target.Type = tc.Type
					}
					if tc.Function.Name != "" {
						target.Function.Name += tc.Function.Name
					}
					if tc.Function.Arguments != "" {
						target.Function.Arguments += tc.Function.Arguments
					}
				}
			}
		}

		if err := scanner.Err(); err != nil {
			log.Printf("[llm] stream scan error: %v", err)
			stream.Err = err
			ch <- LLMEvent{Kind: LLMEventError, Error: err}
			return
		}

		fullText := fullTextBuilder.String()

		// 构造最终结果
		finalResult := &LLMResult{
			Message: LLMMessage{
				Role:    RoleAssistant,
				Content: fullText,
			},
		}

		// 转换 tool calls
		if len(toolCallsMap) > 0 {
			// 按索引排序
			var indices []int
			for k := range toolCallsMap {
				indices = append(indices, k)
			}
			sort.Ints(indices)

			var finalToolCalls []ToolCall
			for _, idx := range indices {
				tc := toolCallsMap[idx]
				finalToolCalls = append(finalToolCalls, ToolCall{
					ToolName:  tc.Function.Name,
					Arguments: json.RawMessage(tc.Function.Arguments),
					CallID:    tc.ID,
				})
			}
			finalResult.ToolCalls = finalToolCalls
		}

		log.Printf("[llm] stream complete elapsed=%s len=%d tool_calls=%d", time.Since(start), len(fullText), len(finalResult.ToolCalls))
		ch <- LLMEvent{Kind: LLMEventCompleted, FullText: fullText, Result: finalResult}
	}()

	return stream
}

type completionsChatStreamResponse struct {
	Choices []completionsChatStreamChoice `json:"choices"`
}

type completionsChatStreamChoice struct {
	Delta completionsChatStreamDelta `json:"delta"`
}

type completionsChatStreamDelta struct {
	Role      string                          `json:"role"`
	Content   string                          `json:"content"`
	ToolCalls []completionsChatStreamToolCall `json:"tool_calls,omitempty"`
}

type completionsChatStreamToolCall struct {
	Index    int                           `json:"index"`
	ID       string                        `json:"id"`
	Type     string                        `json:"type"`
	Function completionsChatStreamFunction `json:"function"`
}

type completionsChatStreamFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
