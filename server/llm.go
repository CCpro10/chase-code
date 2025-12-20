package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
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

// LLMResult 是 Complete 返回的结构化结果，目前只包含一条 assistant 消息，
// 以后可以扩展 usage、tool 调用信息等元数据。
type LLMResult struct {
	Message LLMMessage
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
		// 文档示例：
		//   api_key="MOONSHOT_API_KEY"
		//   base_url="https://api.moonshot.cn/v1"
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

func NewLLMClient(cfg *LLMConfig) (LLMClient, error) {
	// 初始化日志输出（只做一次，重复调用 log.SetOutput 影响有限）
	path := os.Getenv("CHASE_CODE_LOG_FILE")
	if path == "" {
		if cwd, err := os.Getwd(); err == nil {
			path = filepath.Join(cwd, ".chase-code", "logs", "chase-code.log")
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
	Model    string              `json:"model"`
	Messages []openAIChatMessage `json:"messages"`
	Stream   bool                `json:"stream"`
}

type openAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (c *OpenAIClient) Complete(ctx context.Context, p Prompt) (*LLMResult, error) {
	url := fmt.Sprintf("%s/chat/completions", c.cfg.BaseURL)

	msgs := make([]openAIChatMessage, 0, len(p.Messages))
	for _, m := range p.Messages {
		msgs = append(msgs, openAIChatMessage{Role: string(m.Role), Content: m.Content})
	}

	reqBody := openAIChatRequest{Model: c.cfg.Model, Messages: msgs, Stream: false}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	// 记录请求日志（不包含 API Key）
	log.Printf("[llm] request provider=%s model=%s url=%s body_bytes=%d", c.cfg.Provider, c.cfg.Model, url, len(data))

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

	if resp.StatusCode/100 != 2 {
		var bodyBytes [4096]byte
		n, _ := resp.Body.Read(bodyBytes[:])
		msg := fmt.Sprintf("OpenAI API 返回非 2xx 状态码: %d, body: %s", resp.StatusCode, string(bodyBytes[:n]))
		log.Printf("[llm] non-2xx status=%d body_snippet=%q (elapsed=%s)", resp.StatusCode, string(bodyBytes[:n]), time.Since(start))
		return nil, errors.New(msg)
	}

	var out openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		log.Printf("[llm] decode response error: %v (elapsed=%s)", err, time.Since(start))
		return nil, err
	}
	if len(out.Choices) == 0 {
		log.Printf("[llm] empty choices (elapsed=%s)", time.Since(start))
		return nil, errors.New("OpenAI 响应中没有 choices")
	}

	msg := out.Choices[0].Message
	log.Printf("[llm] success provider=%s model=%s elapsed=%s", c.cfg.Provider, c.cfg.Model, time.Since(start))

	return &LLMResult{
		Message: LLMMessage{
			Role:    Role(msg.Role),
			Content: msg.Content,
		},
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
