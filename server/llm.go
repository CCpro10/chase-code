package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LLMEventKind / LLMEvent / LLMStream 参考 codex 的流式接口抽象，当前实现
// 只在 OpenAIClient.Stream 中做简单封装，保留扩展空间。
type LLMEventKind string

const (
	LLMEventCreated    LLMEventKind = "created"
	LLMEventTextDelta  LLMEventKind = "text_delta"
	LLMEventCompleted  LLMEventKind = "completed"
	LLMEventRateLimits LLMEventKind = "rate_limits"
	LLMEventError      LLMEventKind = "error"
)

type LLMEvent struct {
	Kind      LLMEventKind
	TextDelta string
	FullText  string
	Error     error
}

type LLMStream struct {
	C   <-chan LLMEvent
	Err error
}

// LLMMessage 表示一次完整调用返回的一条 assistant 消息。
type LLMMessage struct {
	Role    Role
	Content string
}

// LLMResult 是 Complete 返回的结构化结果，
// 目前包含一条 assistant 消息以及可选的工具调用列表（来自 OpenAI tool_calls）。
type LLMResult struct {
	Message   LLMMessage
	ToolCalls []ToolCall
}

// LLMClient 抽象一个“模型客户端”，参考 codex 的 ModelClient：
//   - Complete 返回一个结构化的 LLMResult，而不是裸字符串，方便扩展；
//   - Stream 保持现有的事件流接口，用于以后支持真正的流式输出。
type LLMClient interface {
	Complete(ctx context.Context, p Prompt) (*LLMResult, error)
	Stream(ctx context.Context, p Prompt) *LLMStream
}

type LLMProvider string

const (
	ProviderOpenAI LLMProvider = "openai"
	ProviderKimi   LLMProvider = "kimi" // Kimi (Moonshot) 兼容 OpenAI Chat API
)

type LLMConfig struct {
	Provider LLMProvider
	Model    string
	APIKey   string
	BaseURL  string
	Timeout  time.Duration
}

// NewLLMConfigFromEnv 从环境变量加载配置。
//
// 支持的 provider:
//   - openai (默认): 使用 CHASE_CODE_OPENAI_* 环境变量
//   - kimi:          使用 CHASE_CODE_KIMI_* 环境变量，Kimi API 兼容 OpenAI Chat Completions
func NewLLMConfigFromEnv() (*LLMConfig, error) {
	provider := os.Getenv("CHASE_CODE_LLM_PROVIDER")
	if provider == "" {
		provider = string(ProviderOpenAI)
	}

	p := LLMProvider(provider)
	switch p {
	case ProviderOpenAI:
		apiKey := os.Getenv("CHASE_CODE_OPENAI_API_KEY")
		if apiKey == "" {
			return nil, errors.New("缺少环境变量 CHASE_CODE_OPENAI_API_KEY")
		}
		model := os.Getenv("CHASE_CODE_OPENAI_MODEL")
		if model == "" {
			model = "gpt-4.1-mini"
		}
		baseURL := os.Getenv("CHASE_CODE_OPENAI_BASE_URL")
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		return &LLMConfig{
			Provider: ProviderOpenAI,
			Model:    model,
			APIKey:   apiKey,
			BaseURL:  baseURL,
			Timeout:  60 * time.Second,
		}, nil

	case ProviderKimi:
		// Kimi（Moonshot）API：兼容 OpenAI 的 /v1/chat/completions
		apiKey := os.Getenv("CHASE_CODE_KIMI_API_KEY")
		if apiKey == "" {
			// 兼容直接使用 MOONSHOT_API_KEY 的场景
			apiKey = os.Getenv("MOONSHOT_API_KEY")
		}
		if apiKey == "" {
			return nil, errors.New("缺少环境变量 CHASE_CODE_KIMI_API_KEY 或 MOONSHOT_API_KEY")
		}
		model := os.Getenv("CHASE_CODE_KIMI_MODEL")
		if model == "" {
			model = "kimi-k2-turbo-preview"
		}
		baseURL := os.Getenv("CHASE_CODE_KIMI_BASE_URL")
		if baseURL == "" {
			baseURL = "https://api.moonshot.cn/v1"
		}
		return &LLMConfig{
			Provider: ProviderKimi,
			Model:    model,
			APIKey:   apiKey,
			BaseURL:  baseURL,
			Timeout:  60 * time.Second,
		}, nil

	default:
		return nil, fmt.Errorf("不支持的 LLM Provider: %s", provider)
	}
}

// NewLLMClient 每次被调用时都会初始化一个新的 LLMClient 实例。
//
// 为了便于调试不同对话会话（Session）的行为，这里仿照 codex 的做法，
// 为每次会话生成一个简单的 SessionID，并将日志输出到按 SessionID 区分的
// 独立日志文件中：
//   - SessionID 由当前日期(YYYYMMDD)、时间(HHMMSS)和 4 位随机数组成；
//   - 日志文件路径形如：
//     $CWD/.chase-code/logs/chase-code-<SessionID>.log
//   - 如需覆盖默认路径，可通过环境变量 CHASE_CODE_LOG_FILE 指定完整文件名。
//
// 注意：这里仍然使用标准库 log 作为输出后端，log.SetOutput 是进程级别的，
// chase-code 默认在单会话模式下运行，因此该行为是可以接受的。
func NewLLMClient(cfg *LLMConfig) (LLMClient, error) {
	// 初始化日志输出：优先使用显式指定的 CHASE_CODE_LOG_FILE，
	// 否则在当前工作目录下按 SessionID 生成独立日志文件。
	path := os.Getenv("CHASE_CODE_LOG_FILE")
	if path == "" {
		if cwd, err := os.Getwd(); err == nil {
			// 生成形如 20251220-153005-4821 的 SessionID
			now := time.Now()
			datePart := now.Format("20060102-150405")
			// 使用纳秒时间作为随机源，避免全局 rand.Seed 带来的竞态
			rnd := rand.New(rand.NewSource(now.UnixNano()))
			randPart := rnd.Intn(10000)
			path = filepath.Join(cwd, ".chase-code", "logs", fmt.Sprintf("chase-code-%s-%04d.log", datePart, randPart))
		}
	}
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			log.Printf("[llm] 创建日志目录失败: %v", err)
		} else {
			f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err == nil {
				log.SetOutput(f)
				log.SetFlags(log.LstdFlags | log.Lmicroseconds)
				log.Printf("[llm] 使用日志文件: %s", path)
			} else {
				// 打不开日志文件时，退回标准错误输出
				log.Printf("[llm] 打开日志文件失败: %v", err)
			}
		}
	}

	switch cfg.Provider {
	case ProviderOpenAI, ProviderKimi:
		// Kimi API 兼容 OpenAI Chat Completions，因此可以复用同一个 HTTP 客户端实现，
		// 通过不同的 BaseURL/Model/APIKey 区分具体提供商。
		return &OpenAIClient{cfg: *cfg, httpClient: &http.Client{Timeout: cfg.Timeout}}, nil
	default:
		return nil, fmt.Errorf("不支持的 LLM Provider: %s", cfg.Provider)
	}
}

// --------------------- OpenAI Chat Completions 实现 ---------------------

type OpenAIClient struct {
	cfg        LLMConfig
	httpClient *http.Client
}

type openAIChatRequest struct {
	Model      string               `json:"model"`
	Messages   []openAIChatMessage  `json:"messages"`
	Stream     bool                 `json:"stream"`
	Tools      []openAIFunctionTool `json:"tools,omitempty"`
	ToolChoice json.RawMessage      `json:"tool_choice,omitempty"`
}

type openAIChatMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	Name       string           `json:"name,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function openAIToolCallFunction `json:"function"`
}

type openAIToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// openAIFunctionTool / openAIFunction 定义了 OpenAI tools/function calling 所需的最小结构。
// 目前只在 buildChatRequest 中使用，用于在 Prompt.Tools 非空时构造 tools 数组，
// 行为与现有实现保持兼容：如果没有提供 ToolSpec 或缺少参数模式，则不会附带 tools 字段。
type openAIFunctionTool struct {
	Type     string            `json:"type"` // 始终为 "function"
	Function openAIFunctionDef `json:"function"`
}

type openAIFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// buildMessagesFromItems 将高层的 ResponseItem 历史转换为底层 OpenAI Chat 消息。
// 这里会：
//   - 将普通对话消息映射为 user/assistant 等角色；
//   - 将工具输出映射为 role:"tool" 的消息，并在可能的情况下携带 tool_call_id。
func buildMessagesFromItems(items []ResponseItem) []openAIChatMessage {
	msgs := make([]openAIChatMessage, 0, len(items))
	for _, it := range items {
		switch it.Type {
		case ResponseItemMessage:
			msgs = append(msgs, openAIChatMessage{
				Role:    string(it.Role),
				Content: it.Text,
			})

		case ResponseItemToolResult:
			// 将工具输出作为 role:"tool" 的消息发送给 OpenAI，
			// 使其语义更接近 codex 对 FunctionCallOutput 的处理方式。
			if it.ToolName == "" && it.ToolOutput == "" {
				continue
			}
			msgs = append(msgs, openAIChatMessage{
				Role:       "tool",
				Name:       it.ToolName,
				ToolCallID: it.CallID,
				Content:    it.ToolOutput,
			})

		case ResponseItemToolCall:
			if it.ToolName == "" {
				continue
			}
			args := strings.TrimSpace(string(it.ToolArguments))
			if args == "" {
				args = "{}"
			}
			msgs = append(msgs, openAIChatMessage{
				Role: "assistant",
				ToolCalls: []openAIToolCall{
					{
						ID:   it.CallID,
						Type: "function",
						Function: openAIToolCallFunction{
							Name:      it.ToolName,
							Arguments: args,
						},
					},
				},
			})
		}
	}
	return msgs
}

// openAIChatResponse 对应 Chat Completions 的响应结构。
// 为了后续支持 OpenAI tools/function calling，这里预留了 tool_calls 字段，
// 当前实现仍然只使用 message.content 字段驱动现有的工具 JSON 协议。
type openAIChatResponse struct {
	Choices []struct {
		Message struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls,omitempty"`
		} `json:"message"`
	} `json:"choices"`
}

// buildChatRequest 仿照 codex 的做法，将 Prompt 映射为 Chat Completions 请求体：
//   - 优先基于 Prompt.Items 构造 messages（便于使用 tool role）；如 Items 为空则回退到 Prompt.Messages；
//   - 如有需要，后续可以在这里将 Prompt.Tools 转换为 OpenAI tools/function 调用。
//
// 当前实现只在 Prompt.Tools 中存在带参数模式的 ToolSpec 时才填充 tools 字段，
// 对现有行为完全兼容（默认不会启用 function calling）。
func (c *OpenAIClient) buildChatRequest(p Prompt) openAIChatRequest {
	var msgs []openAIChatMessage
	if len(p.Items) > 0 {
		msgs = buildMessagesFromItems(p.Items)
	} else {
		msgs = make([]openAIChatMessage, 0, len(p.Messages))
		for _, m := range p.Messages {
			msgs = append(msgs, openAIChatMessage{Role: string(m.Role), Content: m.Content})
		}
	}

	req := openAIChatRequest{Model: c.cfg.Model, Messages: msgs, Stream: false}

	// 预留：当 Prompt.Tools 提供了参数 schema 时，将其转成 OpenAI function tools。
	if len(p.Tools) > 0 {
		tools := make([]openAIFunctionTool, 0, len(p.Tools))
		for _, t := range p.Tools {
			// 只有在 parameters 非空时才生成 function tool，避免发送不完整的 schema。
			if len(t.Parameters) == 0 || string(t.Parameters) == "null" {
				continue
			}
			tools = append(tools, openAIFunctionTool{
				Type: "function",
				Function: openAIFunctionDef{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.Parameters,
				},
			})
		}
		if len(tools) > 0 {
			req.Tools = tools
			// 先固定为 auto，后续可以按需支持指定某个函数或 parallel 调用。
			req.ToolChoice = json.RawMessage(`"auto"`)
		}
	}

	return req
}

func (c *OpenAIClient) Complete(ctx context.Context, p Prompt) (*LLMResult, error) {
	url := fmt.Sprintf("%s/chat/completions", c.cfg.BaseURL)

	reqBody := c.buildChatRequest(p)

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	// 记录完整请求体（不包含 API Key），使用缩进后的 JSON 便于阅读
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err != nil {
		// 回退到原始 body，避免因为格式化失败丢日志
		pretty.Write(data)
	}
	log.Printf("[llm] request provider=%s model=%s url=%s body_bytes=%d body=\n%s", c.cfg.Provider, c.cfg.Model, url, len(data), pretty.String())

	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		log.Printf("[llm] new request error: %v", err)
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("[llm] http error: %v (elapsed=%s)", err, time.Since(start))
		return nil, err
	}
	defer resp.Body.Close()

	// 读取完整响应体并打印出来，方便调试所有 2xx/非 2xx 情况。
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		log.Printf("[llm] read response error: %v (elapsed=%s)", readErr, time.Since(start))
		return nil, readErr
	}
	log.Printf("[llm] raw response status=%d body_bytes=%d body=\n%s", resp.StatusCode, len(respBody), string(respBody))

	if resp.StatusCode/100 != 2 {
		msg := fmt.Sprintf("OpenAI API 返回非 2xx 状态码: %d, body: %s", resp.StatusCode, string(respBody))
		log.Printf("[llm] non-2xx status=%d (elapsed=%s)", resp.StatusCode, time.Since(start))
		return nil, errors.New(msg)
	}

	var out openAIChatResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		log.Printf("[llm] decode response error: %v (elapsed=%s)", err, time.Since(start))
		return nil, err
	}
	if len(out.Choices) == 0 {
		log.Printf("[llm] empty choices (elapsed=%s)", time.Since(start))
		return nil, errors.New("OpenAI 响应中没有 choices")
	}

	msg := out.Choices[0].Message
	log.Printf("[llm] success provider=%s model=%s elapsed=%s", c.cfg.Provider, c.cfg.Model, time.Since(start))

	var toolCalls []ToolCall
	// 默认启用 function calling：如果模型返回了 tool_calls，则优先将其解析为内部 ToolCall 列表，
	// 由上层 Session 使用；否则仍然只依赖 message.content 中的自定义 JSON 协议作为回退路径。
	if len(msg.ToolCalls) > 0 {
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
	}

	return &LLMResult{
		Message: LLMMessage{
			Role:    Role(msg.Role),
			Content: msg.Content,
		},
		ToolCalls: toolCalls,
	}, nil
}

func (c *OpenAIClient) Stream(ctx context.Context, p Prompt) *LLMStream {
	ch := make(chan LLMEvent, 8)
	stream := &LLMStream{C: ch}

	go func() {
		defer close(ch)
		res, err := c.Complete(ctx, p)
		if err != nil {
			stream.Err = err
			ch <- LLMEvent{Kind: LLMEventError, Error: err}
			return
		}
		ch <- LLMEvent{Kind: LLMEventCreated}
		ch <- LLMEvent{Kind: LLMEventTextDelta, TextDelta: res.Message.Content}
		ch <- LLMEvent{Kind: LLMEventCompleted, FullText: res.Message.Content}
	}()

	return stream
}
