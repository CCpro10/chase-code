package llm

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
	"github.com/openai/openai-go/shared/constant"
)

// ResponsesClient 使用官方 openai-go SDK 的 Responses API 与模型交互。
type ResponsesClient struct {
	cfg    clientConfig
	client *openai.Client
}

// NewResponsesClient 创建一个新的 ResponsesClient。
func NewResponsesClient(cfg clientConfig) *ResponsesClient {
	c := openai.NewClient(
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(cfg.BaseURL),
		option.WithHTTPClient(newHTTPClient(cfg.Timeout)),
	)
	return &ResponsesClient{cfg: cfg, client: &c}
}

// Complete 发送 Responses API 请求并返回完整结果。
func (c *ResponsesClient) Complete(ctx context.Context, p Prompt) (*LLMResult, error) {
	start := time.Now()
	resp, err := c.client.Responses.New(ctx, c.buildParams(p))
	if err != nil {
		log.Printf("[llm] Responses API error: %v (elapsed=%s)", err, time.Since(start))
		return nil, err
	}

	text, calls := c.extractOutput(resp.Output)
	log.Printf("[llm] success alias=%s model=%s elapsed=%s", c.cfg.Alias, c.cfg.Model, time.Since(start))

	return &LLMResult{
		Message:   LLMMessage{Role: RoleAssistant, Content: text},
		ToolCalls: calls,
	}, nil
}

// Stream 以流式接口返回 Responses API 的输出。
func (c *ResponsesClient) Stream(ctx context.Context, p Prompt) *LLMStream {
	ch := make(chan LLMEvent, 128)
	stream := &LLMStream{C: ch}

	go func() {
		defer close(ch)
		start := time.Now()
		s := c.client.Responses.NewStreaming(ctx, c.buildParams(p))
		defer s.Close()

		ch <- LLMEvent{Kind: LLMEventCreated}
		var textBuilder strings.Builder
		var toolCalls []ToolCall

		for s.Next() {
			ev := s.Current()
			switch ev.Type {
			case "response.output_text.delta":
				delta := ev.AsResponseOutputTextDelta().Delta
				textBuilder.WriteString(delta)
				ch <- LLMEvent{Kind: LLMEventTextDelta, TextDelta: delta}
			case "response.output_item.done":
				item := ev.AsResponseOutputItemDone().Item

				jsonStr, _ := json.Marshal(item)
				log.Printf("[llm] stream item: %v", string(jsonStr))

				if call, ok := c.parseToolCall(item); ok {
					toolCalls = append(toolCalls, call)
				}
				if textBuilder.Len() == 0 && item.Type == "message" {
					textBuilder.WriteString(c.extractText(item.Content, string(item.Role)))
				}
			}
		}

		if err := s.Err(); err != nil {
			log.Printf("[llm] stream error: %v", err)
			ch <- LLMEvent{Kind: LLMEventError, Error: err}
			return
		}

		fullText := textBuilder.String()
		result := &LLMResult{
			Message:   LLMMessage{Role: RoleAssistant, Content: fullText},
			ToolCalls: toolCalls,
		}
		log.Printf("[llm] stream complete elapsed=%s len=%d tool_calls=%d  result=%v", time.Since(start), len(fullText), len(toolCalls), result)
		ch <- LLMEvent{Kind: LLMEventCompleted, FullText: fullText, Result: result}
	}()

	return stream
}

func (c *ResponsesClient) buildParams(p Prompt) responses.ResponseNewParams {
	inputItems := make([]responses.ResponseInputItemUnionParam, 0)

	items := p.Items
	if len(items) == 0 {
		for _, m := range p.Messages {
			items = append(items, ResponseItem{Type: ResponseItemMessage, Role: m.Role, Text: m.Content})
		}
	}

	for _, it := range items {
		switch it.Type {
		case ResponseItemMessage:
			if it.Text != "" || it.Role == RoleUser {
				inputItems = append(inputItems, responses.ResponseInputItemParamOfMessage(it.Text, responses.EasyInputMessageRole(it.Role)))
			}
			if it.Role == RoleAssistant && len(it.ToolCalls) > 0 {
				for _, call := range it.ToolCalls {
					inputItems = append(inputItems, responses.ResponseInputItemParamOfFunctionCall(
						string(call.Arguments),
						call.CallID,
						call.ToolName,
					))
				}
			}
		case ResponseItemToolCall:
			inputItems = append(inputItems, responses.ResponseInputItemParamOfFunctionCall(
				string(it.ToolArguments),
				it.CallID,
				it.ToolName,
			))
		case ResponseItemToolResult:
			if it.ToolName != "" || it.ToolOutput != "" {
				inputItems = append(inputItems, responses.ResponseInputItemParamOfFunctionCallOutput(
					it.CallID,
					truncateToolOutput(it.ToolOutput),
				))
			}
		}
	}

	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(c.cfg.Model),
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: responses.ResponseInputParam(inputItems)},
	}

	if key := strings.TrimSpace(c.cfg.CacheKey); key != "" {
		params.PromptCacheKey = param.NewOpt(key)
		params.User = param.NewOpt(key)
	}

	if sdkTools := c.buildTools(p.Tools); len(sdkTools) > 0 {
		params.Tools = sdkTools
		params.ToolChoice = responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: param.NewOpt(responses.ToolChoiceOptionsAuto)}
	}

	return params
}

func (c *ResponsesClient) buildTools(tools []ToolSpec) []responses.ToolUnionParam {
	var sdkTools []responses.ToolUnionParam
	for _, t := range tools {
		if raw, ok := buildCustomToolPayload(t); ok {
			sdkTools = append(sdkTools, param.Override[responses.ToolUnionParam](raw))
			continue
		}
		if len(t.Parameters) == 0 || string(t.Parameters) == "null" {
			continue
		}
		var paramsMap map[string]any
		if err := json.Unmarshal(t.Parameters, &paramsMap); err == nil {
			sdkTools = append(sdkTools, responses.ToolUnionParam{
				OfFunction: &responses.FunctionToolParam{
					Name:        t.Name,
					Description: param.NewOpt(t.Description),
					Parameters:  paramsMap,
					Strict:      param.NewOpt(true),
				},
			})
		}
	}
	return sdkTools
}

func (c *ResponsesClient) extractOutput(output []responses.ResponseOutputItemUnion) (string, []ToolCall) {
	var texts []string
	var calls []ToolCall
	for _, item := range output {
		if text := c.extractText(item.Content, string(item.Role)); text != "" {
			texts = append(texts, text)
		}
		if call, ok := c.parseToolCall(item); ok {
			calls = append(calls, call)
		}
	}
	return strings.Join(texts, "\n"), calls
}

func (c *ResponsesClient) extractText(content []responses.ResponseOutputMessageContentUnion, role string) string {
	if role != string(constant.Assistant("assistant")) {
		return ""
	}
	var parts []string
	for _, part := range content {
		if t := part.AsOutputText().Text; t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "")
}

// parseToolCall converts a response output item into a tool call when supported.
func (c *ResponsesClient) parseToolCall(item responses.ResponseOutputItemUnion) (ToolCall, bool) {
	switch item.Type {
	case "function_call", "tool_call":
		return ToolCall{
			ToolName:  item.Name,
			Arguments: normalizeSDKArguments(json.RawMessage(item.Arguments)),
			CallID:    item.CallID,
		}, true
	case "custom_tool_call":
		return c.parseCustomToolCall(item)
	default:
		return ToolCall{}, false
	}
}

// parseCustomToolCall extracts input from custom_tool_call output items.
func (c *ResponsesClient) parseCustomToolCall(item responses.ResponseOutputItemUnion) (ToolCall, bool) {
	raw := item.RawJSON()
	if raw == "" {
		return ToolCall{}, false
	}

	var payload struct {
		Name      string          `json:"name"`
		CallID    string          `json:"call_id"`
		Input     json.RawMessage `json:"input"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ToolCall{}, false
	}

	name := payload.Name
	if name == "" {
		name = item.Name
	}
	callID := payload.CallID
	if callID == "" {
		callID = item.CallID
	}

	args := payload.Input
	if len(args) == 0 {
		args = payload.Arguments
	}
	if len(args) == 0 && item.Arguments != "" {
		args = json.RawMessage(item.Arguments)
	}
	if name == "" || len(args) == 0 {
		return ToolCall{}, false
	}

	return ToolCall{
		ToolName:  name,
		Arguments: normalizeSDKArguments(args),
		CallID:    callID,
	}, true
}

func normalizeSDKArguments(args json.RawMessage) json.RawMessage {
	if len(args) == 0 {
		return json.RawMessage("{}")
	}
	var s string
	if err := json.Unmarshal(args, &s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			return json.RawMessage("{}")
		}
		if json.Valid([]byte(s)) {
			return json.RawMessage(s)
		}
		quoted, _ := json.Marshal(s)
		return json.RawMessage(quoted)
	}
	return args
}
