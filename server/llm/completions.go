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

			for _, tc := range delta.ToolCalls {
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

		if len(toolCallsMap) > 0 {
			var indices []int
			for k := range toolCallsMap {
				indices = append(indices, int(k))
			}
			sort.Ints(indices)

			var finalToolCalls []ToolCall
			for _, idx := range indices {
				tc := toolCallsMap[int64(idx)]
				finalToolCalls = append(finalToolCalls, ToolCall{
					ToolName:  tc.Function.Name,
					Arguments: json.RawMessage(tc.Function.Arguments),
					CallID:    tc.ID,
				})
			}
			finalResult.ToolCalls = finalToolCalls
		}
		ch <- LLMEvent{Kind: LLMEventCompleted, FullText: fullText, Result: finalResult}
	}()

	return stream
}

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

func (c *CompletionsClient) buildMessages(p Prompt) []openai.ChatCompletionMessageParamUnion {
	items := p.Items
	if len(items) == 0 {
		for _, m := range p.Messages {
			items = append(items, ResponseItem{Type: ResponseItemMessage, Role: m.Role, Text: m.Content, CallID: m.ToolCallID})
		}
	}

	var msgs []openai.ChatCompletionMessageParamUnion
	for _, it := range items {
		switch it.Type {
		case ResponseItemMessage:
			switch it.Role {
			case RoleSystem:
				msgs = append(msgs, openai.SystemMessage(it.Text))
			case RoleUser:
				msgs = append(msgs, openai.UserMessage(it.Text))
			case RoleAssistant:
				msg := openai.AssistantMessage(it.Text)
				if len(it.ToolCalls) > 0 {
					var toolCalls []openai.ChatCompletionMessageToolCallParam
					for _, tc := range it.ToolCalls {
						toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallParam{
							ID:       tc.CallID,
							Type:     constant.Function("function"),
							Function: openai.ChatCompletionMessageToolCallFunctionParam{Name: tc.ToolName, Arguments: string(tc.Arguments)},
						})
					}
					msg.OfAssistant.ToolCalls = toolCalls
				}
				msgs = append(msgs, msg)
			}
		case ResponseItemToolCall:
			// 兼容逻辑：单独的 tool_call 映射为 assistant 消息
			msg := openai.AssistantMessage("")
			msg.OfAssistant.ToolCalls = []openai.ChatCompletionMessageToolCallParam{{
				ID:       it.CallID,
				Type:     constant.Function("function"),
				Function: openai.ChatCompletionMessageToolCallFunctionParam{Name: it.ToolName, Arguments: string(it.ToolArguments)},
			}}
			msgs = append(msgs, msg)
		case ResponseItemToolResult:
			if it.ToolName == "" && it.ToolOutput == "" {
				continue
			}
			callID := strings.TrimSpace(it.CallID)
			if callID == "" {
				log.Printf("[llm] skip tool result: missing tool_call_id")
				continue
			}
			msgs = append(msgs, openai.ToolMessage(truncateToolOutput(it.ToolOutput), callID))
		}
	}
	return msgs
}

func (c *CompletionsClient) buildTools(tools []ToolSpec) []openai.ChatCompletionToolParam {
	var sdkTools []openai.ChatCompletionToolParam
	for _, t := range tools {
		if len(t.Parameters) == 0 || string(t.Parameters) == "null" {
			continue
		}
		var paramsMap map[string]any
		if err := json.Unmarshal(t.Parameters, &paramsMap); err == nil {
			sdkTools = append(sdkTools, openai.ChatCompletionToolParam{
				Type: constant.Function("function"),
				Function: openai.FunctionDefinitionParam{
					Name:        t.Name,
					Description: param.NewOpt(t.Description),
					Parameters:  paramsMap,
				},
			})
		}
	}
	return sdkTools
}

func (c *CompletionsClient) extractToolCalls(calls []openai.ChatCompletionMessageToolCall) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	var toolCalls []ToolCall
	for _, tc := range calls {
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
