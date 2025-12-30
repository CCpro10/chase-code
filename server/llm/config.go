package llm

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"chase-code/config"
)

var (
	globalModels *LLMModels
	loadOnce     sync.Once
	loadErr      error
)

const (
	defaultTimeout       = 300 * time.Second
	defaultOpenAIModel   = "gpt-4.1-mini"
	defaultOpenAIBaseURL = "https://api.openai.com/v1"
	defaultKimiModel     = "kimi-k2-0905-preview"
	defaultKimiBaseURL   = "https://api.moonshot.cn/v1"
	defaultCocoModel     = "gpt-5.1"
	defaultCocoBaseURL   = "https://api.openai.com/v1"

	defaultAliasOpenAI = "openai"
	defaultAliasKimi   = "kimi"
	defaultAliasCoco   = "coco"
)

// LLMModel 表示一个可用的模型配置。
// Alias 可为空，用于区分同名模型；Client 为实际调用的实现，包含 Completions(messages) 与 Responses(input) 两种格式。
type LLMModel struct {
	Client   LLMClient
	Alias    string
	Model    string
	BaseURL  string
	APIKey   string
	CacheKey string
}

// LLMModels 汇总所有模型及当前选择项。
type LLMModels struct {
	All     []*LLMModel
	Current *LLMModel
}

type clientConfig struct {
	Alias    string
	Model    string
	BaseURL  string
	APIKey   string
	CacheKey string
	Timeout  time.Duration
}

type modelEntry struct {
	alias     string
	modelName string
	model     *LLMModel
	err       error
}

type modelClientBuilder func(clientConfig) LLMClient

// Init 初始化 LLM 配置，通常在应用启动时调用。
func Init() error {
	_, err := NewLLMModelsFromEnv()
	return err
}

// NewLLMModelsFromEnv 从环境变量加载所有模型，并选择当前模型。
func NewLLMModelsFromEnv() (*LLMModels, error) {
	loadOnce.Do(func() {
		globalModels, loadErr = loadLLMModelsFromEnv()
	})
	return globalModels, loadErr
}

// GetModels 返回所有已加载的模型。
func GetModels() []*LLMModel {
	ms, _ := NewLLMModelsFromEnv()
	if ms == nil {
		return nil
	}
	return ms.All
}

// GetCurrentModel 返回当前选择的模型。
func GetCurrentModel() *LLMModel {
	ms, _ := NewLLMModelsFromEnv()
	if ms == nil {
		return nil
	}
	return ms.Current
}

// FindModel 根据 alias 查找模型。
func FindModel(alias string) (*LLMModel, error) {
	ms, err := NewLLMModelsFromEnv()
	if err != nil {
		return nil, err
	}
	for _, m := range ms.All {
		if strings.EqualFold(m.Alias, alias) {
			return m, nil
		}
	}
	return nil, fmt.Errorf("未找到别名为 %s 的模型", alias)
}

// loadLLMModelsFromEnv 实际执行模型加载逻辑。
func loadLLMModelsFromEnv() (*LLMModels, error) {
	env := config.Get()
	entries := []modelEntry{
		buildOpenAIEntry(env),
		buildKimiEntry(env),
		buildCocoEntry(env),
	}

	// 加载配置文件中的模型
	if env.LLMConfig != nil {
		for _, m := range env.LLMConfig.Models {
			entries = append(entries, buildFileModelEntry(m))
		}
	}

	all := collectAvailableModels(entries)

	desired := env.LLMProvider
	if desired == "" && env.LLMConfig != nil && env.LLMConfig.Model.Name != "" {
		desired = env.LLMConfig.Model.Name
	}

	current, err := selectModel(entries, desired)
	if err != nil {
		return &LLMModels{All: all}, err
	}

	return &LLMModels{All: all, Current: current}, nil
}

// buildFileModelEntry 从配置文件模型条目构建 modelEntry。
func buildFileModelEntry(m config.Model) modelEntry {
	cfg, client, err := buildClientFromModelConfig(m)
	if err != nil {
		return modelEntry{alias: m.Name, err: err}
	}

	return modelEntry{
		alias:     cfg.Alias,
		modelName: cfg.Model,
		model:     modelFromConfig(cfg, client),
	}
}

// buildClientFromModelConfig 从配置文件模型条目创建 clientConfig 和 LLMClient。
func buildClientFromModelConfig(m config.Model) (clientConfig, LLMClient, error) {
	switch {
	case m.Completions != nil:
		cfg := clientConfig{
			Alias:   m.Name,
			Model:   strings.TrimSpace(m.Completions.Model),
			BaseURL: strings.TrimSpace(m.Completions.BaseURL),
			APIKey:  strings.TrimSpace(m.Completions.APIKey),
			Timeout: defaultTimeout,
		}
		return cfg, NewCompletionsClient(cfg), nil
	case m.Claude != nil:
		cfg := clientConfig{
			Alias:   m.Name,
			Model:   strings.TrimSpace(m.Claude.Model),
			BaseURL: strings.TrimSpace(m.Claude.BaseURL),
			APIKey:  strings.TrimSpace(m.Claude.APIKey),
			Timeout: defaultTimeout,
		}
		// Claude 暂走 OpenAI 兼容的 Completions 接口。
		return cfg, NewCompletionsClient(cfg), nil
	case m.Responses != nil:
		cfg := clientConfig{
			Alias:   m.Name,
			Model:   strings.TrimSpace(m.Responses.Model),
			BaseURL: strings.TrimSpace(m.Responses.BaseURL),
			APIKey:  strings.TrimSpace(m.Responses.APIKey),
			Timeout: defaultTimeout,
		}
		return cfg, NewResponsesClient(cfg), nil
	default:
		return clientConfig{}, nil, fmt.Errorf("模型 %s 缺少配置内容", m.Name)
	}
}

// NewLLMModelFromEnv 返回当前选择的模型。
func NewLLMModelFromEnv() (*LLMModel, error) {
	models, err := NewLLMModelsFromEnv()
	if err != nil {
		return nil, err
	}
	if models.Current == nil {
		return nil, errors.New("没有可用的 LLM 模型")
	}
	return models.Current, nil
}

// collectAvailableModels 收集加载成功的模型列表。
func collectAvailableModels(entries []modelEntry) []*LLMModel {
	out := make([]*LLMModel, 0, len(entries))
	for _, entry := range entries {
		if entry.err == nil && entry.model != nil {
			out = append(out, entry.model)
		}
	}
	return out
}

// selectModel 根据 alias 或模型名称选择模型。
func selectModel(entries []modelEntry, desired string) (*LLMModel, error) {
	desired = strings.TrimSpace(desired)
	if desired == "" {
		desired = defaultAliasOpenAI
	}

	for _, entry := range entries {
		if strings.EqualFold(entry.alias, desired) {
			if entry.err != nil {
				return nil, entry.err
			}
			if entry.model == nil {
				return nil, fmt.Errorf("模型配置为空: %s", desired)
			}
			return entry.model, nil
		}
	}

	var matches []*LLMModel
	for _, entry := range entries {
		if !strings.EqualFold(entry.modelName, desired) {
			continue
		}
		if entry.err != nil {
			continue
		}
		if entry.model != nil {
			matches = append(matches, entry.model)
		}
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		for _, entry := range entries {
			if strings.EqualFold(entry.modelName, desired) && entry.err != nil {
				return nil, entry.err
			}
		}
		return nil, fmt.Errorf("未找到指定模型: %s", desired)
	default:
		return nil, fmt.Errorf("模型名称 %s 重复，请使用 alias 选择", desired)
	}
}

// buildEnvEntry 根据环境变量构造默认模型 entry。
func buildEnvEntry(alias, modelName, baseURL, apiKey, cacheKey string, builder modelClientBuilder, missingKeyErr error) modelEntry {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return modelEntry{alias: alias, modelName: modelName, err: missingKeyErr}
	}

	cfg := clientConfig{
		Alias:    alias,
		Model:    modelName,
		BaseURL:  baseURL,
		APIKey:   apiKey,
		CacheKey: strings.TrimSpace(cacheKey),
		Timeout:  defaultTimeout,
	}
	client := builder(cfg)
	return modelEntry{alias: cfg.Alias, modelName: modelName, model: modelFromConfig(cfg, client)}
}

// buildCompletionsClient 适配构造 CompletionsClient 的 builder。
func buildCompletionsClient(cfg clientConfig) LLMClient {
	return NewCompletionsClient(cfg)
}

// buildResponsesClient 适配构造 ResponsesClient 的 builder。
func buildResponsesClient(cfg clientConfig) LLMClient {
	return NewResponsesClient(cfg)
}

// buildOpenAIEntry 构造 OpenAI 默认模型 entry。
func buildOpenAIEntry(env *config.Config) modelEntry {
	modelName := defaultOpenAIModel
	if strings.TrimSpace(env.OpenAIModel) != "" {
		modelName = strings.TrimSpace(env.OpenAIModel)
	}
	baseURL := defaultOpenAIBaseURL
	if strings.TrimSpace(env.OpenAIBaseURL) != "" {
		baseURL = strings.TrimSpace(env.OpenAIBaseURL)
	}
	apiKey := strings.TrimSpace(env.OpenAIAPIKey)
	return buildEnvEntry(
		defaultAliasOpenAI,
		modelName,
		baseURL,
		apiKey,
		"",
		buildCompletionsClient,
		errors.New("缺少环境变量 CHASE_CODE_OPENAI_API_KEY"),
	)
}

// buildKimiEntry 构造 Kimi 默认模型 entry。
func buildKimiEntry(env *config.Config) modelEntry {
	modelName := defaultKimiModel
	if strings.TrimSpace(env.KimiModel) != "" {
		modelName = strings.TrimSpace(env.KimiModel)
	}
	baseURL := defaultKimiBaseURL
	if strings.TrimSpace(env.KimiBaseURL) != "" {
		baseURL = strings.TrimSpace(env.KimiBaseURL)
	}
	apiKey := strings.TrimSpace(env.KimiAPIKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(env.MoonshotAPIKey)
	}
	return buildEnvEntry(
		defaultAliasKimi,
		modelName,
		baseURL,
		apiKey,
		"",
		buildCompletionsClient,
		errors.New("缺少环境变量 CHASE_CODE_KIMI_API_KEY 或 MOONSHOT_API_KEY"),
	)
}

// buildCocoEntry 构造 Coco 默认模型 entry。
func buildCocoEntry(env *config.Config) modelEntry {
	modelName := defaultCocoModel
	if strings.TrimSpace(env.CocoModel) != "" {
		modelName = strings.TrimSpace(env.CocoModel)
	}
	baseURL := defaultCocoBaseURL
	if strings.TrimSpace(env.CocoBaseURL) != "" {
		baseURL = strings.TrimSpace(env.CocoBaseURL)
	}
	jwtKey := strings.TrimSpace(env.CocoJWTKey)
	cacheKey := strings.TrimSpace(env.CocoCacheKey)
	return buildEnvEntry(
		defaultAliasCoco,
		modelName,
		baseURL,
		jwtKey,
		cacheKey,
		buildResponsesClient,
		errors.New("缺少环境变量 cocojwtkey"),
	)
}

// modelFromConfig 将 clientConfig 写回到 LLMModel 结构。
func modelFromConfig(cfg clientConfig, client LLMClient) *LLMModel {
	return &LLMModel{
		Client:   client,
		Alias:    cfg.Alias,
		Model:    cfg.Model,
		BaseURL:  cfg.BaseURL,
		APIKey:   cfg.APIKey,
		CacheKey: cfg.CacheKey,
	}
}
