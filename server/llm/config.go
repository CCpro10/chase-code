package llm

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"chase-code/config"
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

// NewLLMModelsFromEnv 从环境变量加载所有模型，并选择当前模型。
func NewLLMModelsFromEnv() (*LLMModels, error) {
	env := config.Get()
	entries := []modelEntry{
		buildOpenAIEntry(env),
		buildKimiEntry(env),
		buildCocoEntry(env),
	}

	all := collectAvailableModels(entries)
	current, err := selectModel(entries, env.LLMProvider)
	if err != nil {
		return &LLMModels{All: all}, err
	}

	return &LLMModels{All: all, Current: current}, nil
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

func collectAvailableModels(entries []modelEntry) []*LLMModel {
	out := make([]*LLMModel, 0, len(entries))
	for _, entry := range entries {
		if entry.err == nil && entry.model != nil {
			out = append(out, entry.model)
		}
	}
	return out
}

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
	if apiKey == "" {
		return modelEntry{alias: defaultAliasOpenAI, modelName: modelName, err: errors.New("缺少环境变量 CHASE_CODE_OPENAI_API_KEY")}
	}

	cfg := clientConfig{
		Alias:   defaultAliasOpenAI,
		Model:   modelName,
		BaseURL: baseURL,
		APIKey:  apiKey,
		Timeout: defaultTimeout,
	}
	client := &CompletionsClient{cfg: cfg, httpClient: newHTTPClient(cfg.Timeout)}
	return modelEntry{alias: cfg.Alias, modelName: modelName, model: modelFromConfig(cfg, client)}
}

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
	if apiKey == "" {
		return modelEntry{alias: defaultAliasKimi, modelName: modelName, err: errors.New("缺少环境变量 CHASE_CODE_KIMI_API_KEY 或 MOONSHOT_API_KEY")}
	}

	cfg := clientConfig{
		Alias:   defaultAliasKimi,
		Model:   modelName,
		BaseURL: baseURL,
		APIKey:  apiKey,
		Timeout: defaultTimeout,
	}
	client := &CompletionsClient{cfg: cfg, httpClient: newHTTPClient(cfg.Timeout)}
	return modelEntry{alias: cfg.Alias, modelName: modelName, model: modelFromConfig(cfg, client)}
}

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
	if jwtKey == "" {
		return modelEntry{alias: defaultAliasCoco, modelName: modelName, err: errors.New("缺少环境变量 cocojwtkey")}
	}
	cacheKey := strings.TrimSpace(env.CocoCacheKey)

	cfg := clientConfig{
		Alias:   defaultAliasCoco,
		Model:   modelName,
		BaseURL: baseURL,
		APIKey:  jwtKey,
		Timeout: defaultTimeout,
	}
	if cacheKey != "" {
		cfg.CacheKey = cacheKey
	}
	client := &ResponsesClient{cfg: cfg, httpClient: newHTTPClient(cfg.Timeout)}
	return modelEntry{alias: cfg.Alias, modelName: modelName, model: modelFromConfig(cfg, client)}
}

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
