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

	"chase-code/config"
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
	env := config.Get()
	provider := normalizeProvider(env.LLMProvider)
	switch provider {
	case ProviderOpenAI:
		return buildOpenAIConfig(env)
	case ProviderKimi:
		return buildKimiConfig(env)
	default:
		return nil, fmt.Errorf("不支持的 LLM Provider: %s", provider)
	}
}

// normalizeProvider 将空值转换为默认 provider。
func normalizeProvider(raw string) LLMProvider {
	if strings.TrimSpace(raw) == "" {
		return ProviderOpenAI
	}
	return LLMProvider(raw)
}

// buildOpenAIConfig 构建 OpenAI 配置。
func buildOpenAIConfig(env *config.Config) (*LLMConfig, error) {
	apiKey := env.OpenAIAPIKey
	if apiKey == "" {
		return nil, errors.New("缺少环境变量 CHASE_CODE_OPENAI_API_KEY")
	}
	model := env.OpenAIModel
	if model == "" {
		model = "gpt-4.1-mini"
	}
	baseURL := env.OpenAIBaseURL
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
}

// buildKimiConfig 构建 Kimi(Moonshot) 配置。
func buildKimiConfig(env *config.Config) (*LLMConfig, error) {
	apiKey := env.KimiAPIKey
	if apiKey == "" {
		apiKey = env.MoonshotAPIKey
	}
	if apiKey == "" {
		return nil, errors.New("缺少环境变量 CHASE_CODE_KIMI_API_KEY 或 MOONSHOT_API_KEY")
	}
	model := env.KimiModel
	if model == "" {
		model = "kimi-k2-0905-preview"
	}
	baseURL := env.KimiBaseURL
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
	initLLMLogger()
	return newClientByProvider(cfg)
}

// initLLMLogger 初始化日志输出位置。
func initLLMLogger() {
	path := resolveLogFilePath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("[llm] 创建日志目录失败: %v", err)
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("[llm] 打开日志文件失败: %v", err)
		return
	}
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("[llm] 使用日志文件: %s", path)
}

// resolveLogFilePath 计算日志输出路径。
func resolveLogFilePath() string {
	path := config.Get().LogFile
	if path != "" {
		return path
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Join(cwd, ".chase-code", "logs", fmt.Sprintf("chase-code-%s.log", newSessionID()))
}

// newSessionID 生成用于日志文件的会话标识。
func newSessionID() string {
	now := time.Now()
	datePart := now.Format("20060102-150405")
	rnd := rand.New(rand.NewSource(now.UnixNano()))
	randPart := rnd.Intn(10000)
	return fmt.Sprintf("%s-%04d", datePart, randPart)
}

// newClientByProvider 根据 Provider 创建客户端。
func newClientByProvider(cfg *LLMConfig) (LLMClient, error) {
	switch cfg.Provider {
	case ProviderOpenAI, ProviderKimi:
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
		if msg, ok := messageFromItem(it); ok {
			msgs = append(msgs, msg)
		}
	}
	return msgs
}

// messageFromItem 将 ResponseItem 映射为 OpenAI Chat 消息。
func messageFromItem(it ResponseItem) (openAIChatMessage, bool) {
	switch it.Type {
	case ResponseItemMessage:
		return openAIChatMessage{
			Role:    string(it.Role),
			Content: it.Text,
		}, true

	case ResponseItemToolResult:
		if it.ToolName == "" && it.ToolOutput == "" {
			return openAIChatMessage{}, false
		}
		return openAIChatMessage{
			Role:       "tool",
			Name:       it.ToolName,
			ToolCallID: it.CallID,
			Content:    it.ToolOutput,
		}, true

	case ResponseItemToolCall:
		if it.ToolName == "" {
			return openAIChatMessage{}, false
		}
		args := strings.TrimSpace(string(it.ToolArguments))
		if args == "" {
			args = "{}"
		}
		return openAIChatMessage{
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
		}, true
	default:
		return openAIChatMessage{}, false
	}
}

// openAIChatResponse 对应 Chat Completions 的响应结构。
// 为了后续支持 OpenAI tools/function calling，这里预留了 tool_calls 字段，
// 当前实现仍然只使用 message.content 字段驱动现有的工具 JSON 协议。
type openAIChatResponse struct {
	Choices []openAIChatChoice `json:"choices"`
}

type openAIChatChoice struct {
	Message openAIChatResponseMessage `json:"message"`
}

type openAIChatResponseMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []openAIToolCall `json:"tool_calls,omitempty"`
}

// buildChatRequest 仿照 codex 的做法，将 Prompt 映射为 Chat Completions 请求体：
//   - 优先基于 Prompt.Items 构造 messages（便于使用 tool role）；如 Items 为空则回退到 Prompt.Messages；
//   - 如有需要，后续可以在这里将 Prompt.Tools 转换为 OpenAI tools/function 调用。
//
// 当前实现只在 Prompt.Tools 中存在带参数模式的 ToolSpec 时才填充 tools 字段，
// 对现有行为完全兼容（默认不会启用 function calling）。
func (c *OpenAIClient) buildChatRequest(p Prompt) openAIChatRequest {
	msgs := buildChatMessages(p)
	req := openAIChatRequest{Model: c.cfg.Model, Messages: msgs, Stream: false}

	tools := buildToolDefinitions(p.Tools)
	if len(tools) > 0 {
		req.Tools = tools
		req.ToolChoice = json.RawMessage(`"auto"`)
	}

	return req
}

// buildChatMessages 将 Prompt 转换为 OpenAI Chat 消息列表。
func buildChatMessages(p Prompt) []openAIChatMessage {
	if len(p.Items) > 0 {
		return buildMessagesFromItems(p.Items)
	}
	msgs := make([]openAIChatMessage, 0, len(p.Messages))
	for _, m := range p.Messages {
		msgs = append(msgs, openAIChatMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
		})
	}
	return msgs
}

// buildToolDefinitions 构建 OpenAI function tools 定义。
func buildToolDefinitions(tools []ToolSpec) []openAIFunctionTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openAIFunctionTool, 0, len(tools))
	for _, t := range tools {
		if len(t.Parameters) == 0 || string(t.Parameters) == "null" {
			continue
		}
		out = append(out, openAIFunctionTool{
			Type: "function",
			Function: openAIFunctionDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}
	return out
}

func (c *OpenAIClient) Complete(ctx context.Context, p Prompt) (*LLMResult, error) {
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
		return nil, fmt.Errorf("OpenAI API 返回非 2xx 状态码: %d, body: %s", status, string(respBody))
	}

	resp, err := decodeChatResponse(respBody, start)
	if err != nil {
		return nil, err
	}

	msg, err := firstChoiceMessage(resp, start)
	if err != nil {
		return nil, err
	}

	log.Printf("[llm] success provider=%s model=%s elapsed=%s", c.cfg.Provider, c.cfg.Model, time.Since(start))
	return buildLLMResult(msg), nil
}

// chatCompletionsURL 返回完整的 chat/completions URL。
func (c *OpenAIClient) chatCompletionsURL() string {
	return fmt.Sprintf("%s/chat/completions", c.cfg.BaseURL)
}

// marshalRequestBody 将请求体序列化并生成日志用的美化 JSON。
func marshalRequestBody(req openAIChatRequest) ([]byte, string, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, "", err
	}
	return data, formatJSONForLog(data), nil
}

// formatJSONForLog 将 JSON 数据格式化为可读字符串。
func formatJSONForLog(data []byte) string {
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err != nil {
		return string(data)
	}
	return pretty.String()
}

// logRequest 记录发送请求的摘要。
func logRequest(cfg LLMConfig, url string, data []byte, pretty string) {
	log.Printf("[llm] request provider=%s model=%s url=%s body_bytes=%d body=\n%s", cfg.Provider, cfg.Model, url, len(data), pretty)
}

// doChatRequest 发送请求并返回响应体与状态码。
func (c *OpenAIClient) doChatRequest(ctx context.Context, url string, data []byte, start time.Time) ([]byte, int, error) {
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

// logRawResponse 打印原始响应体，便于排查问题。
func logRawResponse(status int, respBody []byte) {
	log.Printf("[llm] raw response status=%d body_bytes=%d body=\n%s", status, len(respBody), string(respBody))
}

// decodeChatResponse 解析响应 JSON。
func decodeChatResponse(respBody []byte, start time.Time) (openAIChatResponse, error) {
	var out openAIChatResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		log.Printf("[llm] decode response error: %v (elapsed=%s)", err, time.Since(start))
		return openAIChatResponse{}, err
	}
	return out, nil
}

// firstChoiceMessage 获取首条消息，确保 choices 非空。
func firstChoiceMessage(resp openAIChatResponse, start time.Time) (openAIChatResponseMessage, error) {
	if len(resp.Choices) == 0 {
		log.Printf("[llm] empty choices (elapsed=%s)", time.Since(start))
		return openAIChatResponseMessage{}, errors.New("OpenAI 响应中没有 choices")
	}
	return resp.Choices[0].Message, nil
}

// buildLLMResult 将响应消息转换为内部结构。
func buildLLMResult(msg openAIChatResponseMessage) *LLMResult {
	return &LLMResult{
		Message: LLMMessage{
			Role:    Role(msg.Role),
			Content: msg.Content,
		},
		ToolCalls: extractToolCalls(msg),
	}
}

// extractToolCalls 提取 OpenAI 返回的 tool_calls。
func extractToolCalls(msg openAIChatResponseMessage) []ToolCall {
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
