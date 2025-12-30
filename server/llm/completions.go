package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/shared"
	"github.com/openai/openai-go/shared/constant"

	servertools "chase-code/server/tools"
	"chase-code/server/utils"
)

// CompletionsClient 使用官方 openai-go SDK 的 Chat Completions API 与模型交互。
type CompletionsClient struct {
	cfg    clientConfig
	client *openai.Client
}

// NewCompletionsClient 创建一个新的 CompletionsClient。
func NewCompletionsClient(cfg clientConfig) *CompletionsClient {
	c := openai.NewClient(
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(cfg.BaseURL),
		option.WithHTTPClient(newHTTPClient(cfg.Timeout)),
	)
	return &CompletionsClient{cfg: cfg, client: &c}
}

// Complete 调用 Chat Completions API 获取完整回复。
func (c *CompletionsClient) Complete(ctx context.Context, p Prompt) (*LLMResult, error) {
	start := time.Now()
	params := c.buildParams(p)

	resp, err := c.client.Chat.Completions.New(ctx, params)
	if err != nil {
		log.Printf("[llm] Completions API error: %v (elapsed=%s)", err, time.Since(start))
		return nil, err
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("Completions API returned no choices")
	}

	choice := resp.Choices[0]
	log.Printf("[llm] success alias=%s model=%s elapsed=%s", c.cfg.Alias, c.cfg.Model, time.Since(start))

	return &LLMResult{
		Message: LLMMessage{
			Role:    Role(choice.Message.Role),
			Content: choice.Message.Content,
		},
		ToolCalls: c.extractToolCalls(choice.Message.ToolCalls),
	}, nil
}

// Stream 调用 Chat Completions API 的流式接口。
func (c *CompletionsClient) Stream(ctx context.Context, p Prompt) *LLMStream {
	ch := make(chan LLMEvent, 128)
	stream := &LLMStream{C: ch}

	go func() {
		defer close(ch)
		params := c.buildParams(p)

		s := c.client.Chat.Completions.NewStreaming(ctx, params)
		defer s.Close()

		ch <- LLMEvent{Kind: LLMEventCreated}
		var fullTextBuilder strings.Builder
		toolCallsMap := make(map[int64]*openai.ChatCompletionChunkChoiceDeltaToolCall)

		for s.Next() {
			chunk := s.Current()
			if len(chunk.Choices) == 0 {
				continue
			}
			delta := chunk.Choices[0].Delta
			if delta.Content != "" {
				fullTextBuilder.WriteString(delta.Content)
				ch <- LLMEvent{Kind: LLMEventTextDelta, TextDelta: delta.Content}
			}

			collectToolCallDelta(toolCallsMap, delta.ToolCalls)
		}

		if err := s.Err(); err != nil {
			log.Printf("[llm] stream error: %v", err)
			ch <- LLMEvent{Kind: LLMEventError, Error: err}
			return
		}

		fullText := fullTextBuilder.String()
		finalResult := &LLMResult{
			Message: LLMMessage{
				Role:    RoleAssistant,
				Content: fullText,
			},
		}

		finalResult.ToolCalls = finalizeStreamToolCalls(toolCallsMap)
		ch <- LLMEvent{Kind: LLMEventCompleted, FullText: fullText, Result: finalResult}
	}()

	return stream
}

// buildParams 构造 Chat Completions API 请求参数。
func (c *CompletionsClient) buildParams(p Prompt) openai.ChatCompletionNewParams {
	msgs := c.buildMessages(p)
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(c.cfg.Model),
		Messages: msgs,
	}

	if sdkTools := c.buildTools(p.Tools); len(sdkTools) > 0 {
		params.Tools = sdkTools
		params.ToolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{
			OfAuto: param.NewOpt("auto"),
		}
	}

	log.Printf("build params: %s\n", utils.ToIndentJSONString(params))
	return params
}

// buildMessages 将 Prompt 转换为 Chat Completions 所需的消息列表。
func (c *CompletionsClient) buildMessages(p Prompt) []openai.ChatCompletionMessageParamUnion {
	items := normalizePromptItems(p)
	if len(items) == 0 {
		return nil
	}

	var msgs []openai.ChatCompletionMessageParamUnion
	for _, it := range items {
		switch it.Type {
		case ResponseItemMessage:
			msg, ok := buildCompletionMessageFromItem(it)
			if ok {
				msgs = append(msgs, msg)
			}
		case ResponseItemToolCall:
			// 兼容逻辑：单独的 tool_call 映射为 assistant 消息
			msg, ok := buildCompletionToolCallMessage(it)
			if ok {
				msgs = append(msgs, msg)
			}
		case ResponseItemToolResult:
			msg, ok := buildCompletionToolResultMessage(it)
			if ok {
				msgs = append(msgs, msg)
			}
		}
	}
	return msgs
}

// buildCompletionMessageFromItem 将普通消息转换为 SDK message。
func buildCompletionMessageFromItem(it ResponseItem) (openai.ChatCompletionMessageParamUnion, bool) {
	switch it.Role {
	case RoleSystem:
		return openai.SystemMessage(it.Text), true
	case RoleUser:
		return openai.UserMessage(it.Text), true
	case RoleAssistant:
		return buildCompletionAssistantMessage(it.Text, it.ToolCalls), true
	case RoleTool:
		log.Printf("[llm] skip tool role message in completions input")
		return openai.ChatCompletionMessageParamUnion{}, false
	default:
		return openai.ChatCompletionMessageParamUnion{}, false
	}
}

// buildCompletionAssistantMessage 构建包含工具调用的 assistant 消息。
func buildCompletionAssistantMessage(text string, calls []ToolCall) openai.ChatCompletionMessageParamUnion {
	msg := openai.AssistantMessage(text)
	if len(calls) == 0 {
		return msg
	}
	if toolCalls := buildCompletionToolCallParams(calls); len(toolCalls) > 0 {
		msg.OfAssistant.ToolCalls = toolCalls
	}
	return msg
}

// buildCompletionToolCallParams 将工具调用列表转换为 SDK 需要的格式。
func buildCompletionToolCallParams(calls []ToolCall) []openai.ChatCompletionMessageToolCallParam {
	out := make([]openai.ChatCompletionMessageToolCallParam, 0, len(calls))
	for _, tc := range calls {
		if strings.TrimSpace(tc.ToolName) == "" {
			continue
		}
		out = append(out, openai.ChatCompletionMessageToolCallParam{
			ID:       tc.CallID,
			Type:     constant.Function("function"),
			Function: openai.ChatCompletionMessageToolCallFunctionParam{Name: tc.ToolName, Arguments: formatFunctionCallArguments(tc.Arguments)},
		})
	}
	return out
}

// buildCompletionToolCallMessage 将独立 tool_call 转为 assistant 消息。
func buildCompletionToolCallMessage(it ResponseItem) (openai.ChatCompletionMessageParamUnion, bool) {
	if strings.TrimSpace(it.ToolName) == "" {
		return openai.ChatCompletionMessageParamUnion{}, false
	}
	callID := strings.TrimSpace(it.CallID)
	if callID == "" {
		log.Printf("[llm] skip tool call: missing tool_call_id name=%s", it.ToolName)
		return openai.ChatCompletionMessageParamUnion{}, false
	}
	msg := openai.AssistantMessage("")
	msg.OfAssistant.ToolCalls = []openai.ChatCompletionMessageToolCallParam{{
		ID:       callID,
		Type:     constant.Function("function"),
		Function: openai.ChatCompletionMessageToolCallFunctionParam{Name: it.ToolName, Arguments: formatFunctionCallArguments(it.ToolArguments)},
	}}
	return msg, true
}

// buildCompletionToolResultMessage 构建 tool 角色的输出消息。
func buildCompletionToolResultMessage(it ResponseItem) (openai.ChatCompletionMessageParamUnion, bool) {
	if it.ToolName == "" && it.ToolOutput == "" {
		return openai.ChatCompletionMessageParamUnion{}, false
	}
	callID := strings.TrimSpace(it.CallID)
	if callID == "" {
		log.Printf("[llm] skip tool result: missing tool_call_id")
		return openai.ChatCompletionMessageParamUnion{}, false
	}
	return openai.ToolMessage(truncateToolOutput(it.ToolOutput), callID), true
}

// collectToolCallDelta 将增量 tool_call 合并到缓存中。
func collectToolCallDelta(toolCallsMap map[int64]*openai.ChatCompletionChunkChoiceDeltaToolCall, calls []openai.ChatCompletionChunkChoiceDeltaToolCall) {
	for _, tc := range calls {
		idx := tc.Index
		if _, ok := toolCallsMap[idx]; !ok {
			toolCallsMap[idx] = &openai.ChatCompletionChunkChoiceDeltaToolCall{}
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

// finalizeStreamToolCalls 将流式聚合的 tool_call 转换为最终结构。
func finalizeStreamToolCalls(toolCallsMap map[int64]*openai.ChatCompletionChunkChoiceDeltaToolCall) []ToolCall {
	if len(toolCallsMap) == 0 {
		return nil
	}
	var indices []int
	for k := range toolCallsMap {
		indices = append(indices, int(k))
	}
	sort.Ints(indices)

	finalToolCalls := make([]ToolCall, 0, len(indices))
	for _, idx := range indices {
		tc := toolCallsMap[int64(idx)]
		if strings.TrimSpace(tc.Function.Name) == "" {
			continue
		}
		finalToolCalls = append(finalToolCalls, ToolCall{
			Kind:      servertools.ToolKindFunction,
			ToolName:  tc.Function.Name,
			Arguments: normalizeSDKArguments(json.RawMessage(tc.Function.Arguments)),
			CallID:    strings.TrimSpace(tc.ID),
		})
	}
	return finalToolCalls
}

// buildTools 将内部工具定义转换为 Chat Completions API 的格式。
func (c *CompletionsClient) buildTools(tools []ToolSpec) []openai.ChatCompletionToolParam {
	var sdkTools []openai.ChatCompletionToolParam
	for _, t := range tools {
		t = normalizeCompletionToolSpec(t)
		if len(t.Parameters) == 0 || string(t.Parameters) == "null" {
			continue
		}
		var paramsMap map[string]any
		if err := json.Unmarshal(t.Parameters, &paramsMap); err != nil {
			log.Printf("[llm] skip tool %s: invalid parameters: %v", t.Name, err)
			continue
		}
		sdkTools = append(sdkTools, openai.ChatCompletionToolParam{
			Type: constant.Function("function"),
			Function: openai.FunctionDefinitionParam{
				Name:        t.Name,
				Description: param.NewOpt(t.Description),
				Parameters:  paramsMap,
			},
		})
	}
	return sdkTools
}

// normalizeCompletionToolSpec 将 custom 工具转换为 completions 可用的函数工具。
func normalizeCompletionToolSpec(t ToolSpec) ToolSpec {
	if t.Kind == servertools.ToolKindCustom && t.Name == "apply_patch" {
		return servertools.ApplyPatchToolSpecFunction()
	}
	return t
}

// extractToolCalls 从 Completions 返回中解析工具调用。
func (c *CompletionsClient) extractToolCalls(calls []openai.ChatCompletionMessageToolCall) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	var toolCalls []ToolCall
	for _, tc := range calls {
		if strings.TrimSpace(tc.Function.Name) == "" {
			continue
		}
		toolCalls = append(toolCalls, ToolCall{
			Kind:      servertools.ToolKindFunction,
			ToolName:  tc.Function.Name,
			Arguments: normalizeSDKArguments(json.RawMessage(tc.Function.Arguments)),
			CallID:    strings.TrimSpace(tc.ID),
		})
	}
	return toolCalls
}
