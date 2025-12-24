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
	"strings"
	"time"
)

// --------------------- Responses(input) 实现 ---------------------

// ResponsesClient 使用 Responses API 与模型交互。
type ResponsesClient struct {
	cfg        clientConfig
	httpClient *http.Client
}

// responsesResponseRequest 对应 Responses API 的请求体。
type responsesResponseRequest struct {
	Model          string                    `json:"model"`
	Input          []responsesInputMessage   `json:"input"`
	Stream         bool                      `json:"stream,omitempty"`
	Tools          []responsesToolDefinition `json:"tools,omitempty"`
	ToolChoice     string                    `json:"tool_choice,omitempty"`
	PromptCacheKey string                    `json:"prompt_cache_key,omitempty"`
	User           string                    `json:"user,omitempty"`
}

// responsesInputMessage 表示 Responses API 的输入消息。
type responsesInputMessage struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	Name       string `json:"name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// responsesToolDefinition 表示 Responses API 的 tool 定义。
type responsesToolDefinition struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type responsesResponse struct {
	Output []responsesOutputItem `json:"output"`
}

type responsesOutputItem struct {
	ID        string                 `json:"id,omitempty"`
	Type      string                 `json:"type"`
	Role      string                 `json:"role,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Arguments json.RawMessage        `json:"arguments,omitempty"`
	CallID    string                 `json:"call_id,omitempty"`
	Function  *responsesToolCallFunc `json:"function,omitempty"`
	Content   []responsesContentPart `json:"content,omitempty"`
}

type responsesContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesToolCallFunc struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type responsesStreamEvent struct {
	Type  string          `json:"type"`
	Delta string          `json:"delta,omitempty"`
	Text  string          `json:"text,omitempty"`
	Item  json.RawMessage `json:"item,omitempty"`
}

// Complete 发送 Responses API 请求并返回完整结果。
func (c *ResponsesClient) Complete(ctx context.Context, p Prompt) (*LLMResult, error) {
	url := c.responsesURL()
	reqBody := c.buildResponseRequest(p)

	data, pretty, err := marshalRequestBody(reqBody)
	if err != nil {
		return nil, err
	}
	logRequest(c.cfg, url, data, pretty)

	start := time.Now()
	respBody, status, err := c.doResponseRequest(ctx, url, data, start)
	if err != nil {
		return nil, err
	}
	logRawResponse(status, respBody)

	if status/100 != 2 {
		log.Printf("[llm] non-2xx status=%d (elapsed=%s)", status, time.Since(start))
		return nil, fmt.Errorf("Responses API 返回非 2xx 状态码: %d, body: %s", status, string(respBody))
	}

	resp, err := decodeResponsesResponse(respBody, start)
	if err != nil {
		return nil, err
	}

	text, calls, err := extractResponsesOutput(resp, start)
	if err != nil {
		return nil, err
	}

	log.Printf("[llm] success alias=%s model=%s elapsed=%s", c.cfg.Alias, c.cfg.Model, time.Since(start))
	return &LLMResult{
		Message: LLMMessage{
			Role:    RoleAssistant,
			Content: text,
		},
		ToolCalls: calls,
	}, nil
}

// Stream 以流式接口返回 Responses API 的输出。
func (c *ResponsesClient) Stream(ctx context.Context, p Prompt) *LLMStream {
	ch := make(chan LLMEvent, 128)
	stream := &LLMStream{C: ch}

	go func() {
		defer close(ch)

		url := c.responsesURL()
		reqBody := c.buildResponseRequest(p)
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
			err := fmt.Errorf("Responses API error: %d %s", resp.StatusCode, string(body))
			stream.Err = err
			ch <- LLMEvent{Kind: LLMEventError, Error: err}
			return
		}

		ch <- LLMEvent{Kind: LLMEventCreated}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		var fullTextBuilder strings.Builder
		var toolCalls []ToolCall

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

			var ev responsesStreamEvent
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				log.Printf("[llm] decode stream error: %v payload=%s", err, payload)
				continue
			}

			switch ev.Type {
			case "response.output_text.delta":
				if ev.Delta != "" {
					fullTextBuilder.WriteString(ev.Delta)
					ch <- LLMEvent{Kind: LLMEventTextDelta, TextDelta: ev.Delta}
				}
			case "response.output_text.done":
				if fullTextBuilder.Len() == 0 && ev.Text != "" {
					fullTextBuilder.WriteString(ev.Text)
				}
			case "response.output_item.done":
				if len(ev.Item) == 0 {
					continue
				}
				item, err := decodeResponsesOutputItem(ev.Item)
				if err != nil {
					log.Printf("[llm] decode output item error: %v", err)
					continue
				}
				if call, ok := responsesToolCallFromItem(item); ok {
					toolCalls = append(toolCalls, call)
				}
				if fullTextBuilder.Len() == 0 && item.Type == "message" {
					text := extractResponsesTextFromItem(item)
					if text != "" {
						fullTextBuilder.WriteString(text)
					}
				}
			default:
				continue
			}
		}

		if err := scanner.Err(); err != nil {
			log.Printf("[llm] stream scan error: %v", err)
			stream.Err = err
			ch <- LLMEvent{Kind: LLMEventError, Error: err}
			return
		}

		fullText := fullTextBuilder.String()
		finalResult := &LLMResult{
			Message: LLMMessage{
				Role:    RoleAssistant,
				Content: fullText,
			},
			ToolCalls: toolCalls,
		}

		log.Printf("[llm] stream complete elapsed=%s len=%d tool_calls=%d", time.Since(start), len(fullText), len(toolCalls))
		ch <- LLMEvent{Kind: LLMEventCompleted, FullText: fullText, Result: finalResult}
	}()

	return stream
}

// responsesURL 返回完整的 /responses URL。
func (c *ResponsesClient) responsesURL() string {
	return fmt.Sprintf("%s/responses", c.cfg.BaseURL)
}

// buildResponseRequest 构建 Responses API 请求体。
func (c *ResponsesClient) buildResponseRequest(p Prompt) responsesResponseRequest {
	req := responsesResponseRequest{
		Model: c.cfg.Model,
		Input: buildResponsesInputMessages(p),
	}

	if cacheKey := strings.TrimSpace(c.cfg.CacheKey); cacheKey != "" {
		req.PromptCacheKey = cacheKey
		req.User = cacheKey
	}

	tools := buildResponsesToolDefinitions(p.Tools)
	if len(tools) > 0 {
		req.Tools = tools
		req.ToolChoice = "auto"
	}

	return req
}

// buildResponsesInputMessages 将 Prompt 转换为 Responses 的 input 消息列表。
func buildResponsesInputMessages(p Prompt) []responsesInputMessage {
	if len(p.Items) > 0 {
		return buildResponsesMessagesFromItems(p.Items)
	}

	msgs := make([]responsesInputMessage, 0, len(p.Messages))
	for _, m := range p.Messages {
		msgs = append(msgs, responsesInputMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
		})
	}
	return msgs
}

// buildResponsesMessagesFromItems 将 ResponseItem 历史转换为 Responses input 消息。
func buildResponsesMessagesFromItems(items []ResponseItem) []responsesInputMessage {
	msgs := make([]responsesInputMessage, 0, len(items))
	for _, it := range items {
		switch it.Type {
		case ResponseItemMessage:
			msgs = append(msgs, responsesInputMessage{
				Role:    string(it.Role),
				Content: it.Text,
			})
		case ResponseItemToolResult:
			if it.ToolName == "" && it.ToolOutput == "" {
				continue
			}
			msgs = append(msgs, responsesInputMessage{
				Role:       "tool",
				Content:    truncateToolOutput(it.ToolOutput),
				Name:       it.ToolName,
				ToolCallID: it.CallID,
			})
		case ResponseItemToolCall:
			// Responses 输入中暂无 tool_calls 字段，先忽略历史里的独立 tool_call。
			continue
		default:
			continue
		}
	}
	return msgs
}

// buildResponsesToolDefinitions 构建 Responses 的 function tool 定义。
func buildResponsesToolDefinitions(tools []ToolSpec) []responsesToolDefinition {
	if len(tools) == 0 {
		return nil
	}
	out := make([]responsesToolDefinition, 0, len(tools))
	for _, t := range tools {
		if len(t.Parameters) == 0 || string(t.Parameters) == "null" {
			continue
		}
		out = append(out, responsesToolDefinition{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		})
	}
	return out
}

// doResponseRequest 发送请求并返回响应体与状态码。
func (c *ResponsesClient) doResponseRequest(ctx context.Context, url string, data []byte, start time.Time) ([]byte, int, error) {
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

// decodeResponsesResponse 解析 Responses API 返回的 JSON。
func decodeResponsesResponse(respBody []byte, start time.Time) (responsesResponse, error) {
	var out responsesResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		log.Printf("[llm] decode response error: %v (elapsed=%s)", err, time.Since(start))
		return responsesResponse{}, err
	}
	return out, nil
}

// decodeResponsesOutputItem 解析单个 output item。
func decodeResponsesOutputItem(raw json.RawMessage) (responsesOutputItem, error) {
	var item responsesOutputItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return responsesOutputItem{}, err
	}
	return item, nil
}

// extractResponsesOutput 从响应中抽取文本与工具调用。
func extractResponsesOutput(resp responsesResponse, start time.Time) (string, []ToolCall, error) {
	if len(resp.Output) == 0 {
		log.Printf("[llm] empty output (elapsed=%s)", time.Since(start))
		return "", nil, errors.New("Responses 响应中没有 output")
	}
	text := extractResponsesTextFromItems(resp.Output)
	calls := extractResponsesToolCalls(resp.Output)
	if text == "" && len(calls) == 0 {
		log.Printf("[llm] empty text/tool calls (elapsed=%s)", time.Since(start))
		return "", nil, errors.New("Responses 响应中没有可用输出")
	}
	return text, calls, nil
}

// extractResponsesTextFromItems 提取所有 assistant 消息文本。
func extractResponsesTextFromItems(items []responsesOutputItem) string {
	var texts []string
	for _, item := range items {
		if item.Type != "message" || item.Role != "assistant" {
			continue
		}
		text := extractResponsesTextFromItem(item)
		if text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n")
}

// extractResponsesTextFromItem 从单个 message item 中抽取文本。
func extractResponsesTextFromItem(item responsesOutputItem) string {
	if item.Type != "message" {
		return ""
	}
	var parts []string
	for _, part := range item.Content {
		if part.Type == "output_text" || part.Type == "text" {
			if part.Text != "" {
				parts = append(parts, part.Text)
			}
		}
	}
	return strings.Join(parts, "")
}

// extractResponsesToolCalls 提取工具调用列表。
func extractResponsesToolCalls(items []responsesOutputItem) []ToolCall {
	if len(items) == 0 {
		return nil
	}
	out := make([]ToolCall, 0)
	for _, item := range items {
		if call, ok := responsesToolCallFromItem(item); ok {
			out = append(out, call)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// responsesToolCallFromItem 将 output item 转换为内部 ToolCall。
func responsesToolCallFromItem(item responsesOutputItem) (ToolCall, bool) {
	if item.Type != "tool_call" && item.Type != "function_call" {
		return ToolCall{}, false
	}
	name := strings.TrimSpace(item.Name)
	args := item.Arguments
	callID := strings.TrimSpace(item.CallID)

	if item.Function != nil {
		if name == "" {
			name = strings.TrimSpace(item.Function.Name)
		}
		if len(args) == 0 {
			args = item.Function.Arguments
		}
	}
	if callID == "" {
		callID = strings.TrimSpace(item.ID)
	}
	if name == "" {
		return ToolCall{}, false
	}
	return ToolCall{
		ToolName:  name,
		Arguments: normalizeResponsesArguments(args),
		CallID:    callID,
	}, true
}

// normalizeResponsesArguments 兼容字符串与对象两种 arguments 形态。
func normalizeResponsesArguments(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		asString = strings.TrimSpace(asString)
		if asString == "" {
			return json.RawMessage("{}")
		}
		if json.Valid([]byte(asString)) {
			return json.RawMessage(asString)
		}
		quoted, _ := json.Marshal(asString)
		return json.RawMessage(quoted)
	}
	if json.Valid(raw) {
		return raw
	}
	quoted, _ := json.Marshal(string(raw))
	return json.RawMessage(quoted)
}
