package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Config 汇总所有通过环境变量控制的运行配置。
// 统一集中读取，方便日志输出与调试。
type Config struct {
	MCPConfigPath      string
	LogFile            string
	LLMProvider        string
	OpenAIAPIKey       string
	OpenAIModel        string
	OpenAIBaseURL      string
	KimiAPIKey         string
	KimiModel          string
	KimiBaseURL        string
	MoonshotAPIKey     string
	CocoJWTKey         string
	CocoCacheKey       string
	CocoModel          string
	CocoBaseURL        string
	ApplyPatchApproval string

	// 多模型配置支持
	LLMConfig *LLMConfig
}

type LLMConfig struct {
	Model  ModelNameRef `yaml:"model"`
	Models []Model      `yaml:"models"`
}

type ModelNameRef struct {
	Name string `yaml:"name"`
}

type Model struct {
	Name        string             `yaml:"name"`
	Completions *CompletionsConfig `yaml:"completions,omitempty"`
	Claude      *ClaudeConfig      `yaml:"claude,omitempty"`
	Responses   *ResponsesConfig   `yaml:"responses,omitempty"`
}

type CompletionsConfig struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
}

type ClaudeConfig struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
}

type ResponsesConfig struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
}

var (
	once sync.Once
	cfg  Config
)

// Get 返回全局配置（延迟加载）。
func Get() *Config {
	once.Do(func() {
		cfg = loadFromEnv()
		cfg.LLMConfig = loadFromFile()
	})
	return &cfg
}

func loadFromEnv() Config {
	return Config{
		MCPConfigPath:      strings.TrimSpace(os.Getenv("CHASE_CODE_MCP_CONFIG")),
		LogFile:            strings.TrimSpace(os.Getenv("CHASE_CODE_LOG_FILE")),
		LLMProvider:        strings.TrimSpace(os.Getenv("CHASE_CODE_LLM_PROVIDER")),
		OpenAIAPIKey:       strings.TrimSpace(os.Getenv("CHASE_CODE_OPENAI_API_KEY")),
		OpenAIModel:        strings.TrimSpace(os.Getenv("CHASE_CODE_OPENAI_MODEL")),
		OpenAIBaseURL:      strings.TrimSpace(os.Getenv("CHASE_CODE_OPENAI_BASE_URL")),
		KimiAPIKey:         strings.TrimSpace(os.Getenv("CHASE_CODE_KIMI_API_KEY")),
		KimiModel:          strings.TrimSpace(os.Getenv("CHASE_CODE_KIMI_MODEL")),
		KimiBaseURL:        strings.TrimSpace(os.Getenv("CHASE_CODE_KIMI_BASE_URL")),
		MoonshotAPIKey:     strings.TrimSpace(os.Getenv("MOONSHOT_API_KEY")),
		CocoJWTKey:         strings.TrimSpace(os.Getenv("cocojwtkey")),
		CocoCacheKey:       strings.TrimSpace(os.Getenv("cococachekey")),
		CocoModel:          strings.TrimSpace(os.Getenv("CHASE_CODE_COCO_MODEL")),
		CocoBaseURL:        strings.TrimSpace(os.Getenv("CHASE_CODE_COCO_BASE_URL")),
		ApplyPatchApproval: strings.TrimSpace(os.Getenv("CHASE_CODE_APPLY_PATCH_APPROVAL")),
	}
}

func loadFromFile() *LLMConfig {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	configPath := filepath.Join(home, ".chase-code", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		// 尝试读取 config.yml
		configPath = filepath.Join(home, ".chase-code", "config.yml")
		data, err = os.ReadFile(configPath)
		if err != nil {
			return nil
		}
	}

	var llc LLMConfig
	if err := yaml.Unmarshal(data, &llc); err != nil {
		fmt.Fprintf(os.Stderr, "解析配置文件失败: %v\n", err)
		return nil
	}

	return &llc
}

// Summary 返回可安全打印的配置摘要（会脱敏 key）。
func (c Config) Summary() string {
	s := fmt.Sprintf(
		"llm_selector=%s mcp_config=%s log_file=%s openai_model=%s openai_base_url=%s openai_api_key=%s kimi_model=%s kimi_base_url=%s kimi_api_key=%s moonshot_api_key=%s coco_model=%s coco_base_url=%s coco_jwt_key=%s coco_cache_key=%s apply_patch_approval=%s",
		emptyAsDefault(c.LLMProvider, "(default)"),
		emptyAsDefault(c.MCPConfigPath, "(empty)"),
		emptyAsDefault(c.LogFile, "(empty)"),
		emptyAsDefault(c.OpenAIModel, "(default)"),
		emptyAsDefault(c.OpenAIBaseURL, "(default)"),
		maskSecret(c.OpenAIAPIKey),
		emptyAsDefault(c.KimiModel, "(default)"),
		emptyAsDefault(c.KimiBaseURL, "(default)"),
		maskSecret(c.KimiAPIKey),
		maskSecret(c.MoonshotAPIKey),
		emptyAsDefault(c.CocoModel, "(default)"),
		emptyAsDefault(c.CocoBaseURL, "(default)"),
		maskSecret(c.CocoJWTKey),
		maskSecret(c.CocoCacheKey),
		emptyAsDefault(c.ApplyPatchApproval, "(default)"),
	)

	if c.LLMConfig != nil {
		s += fmt.Sprintf(" file_model=%s models_count=%d", c.LLMConfig.Model.Name, len(c.LLMConfig.Models))
	}
	return s
}

func emptyAsDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func maskSecret(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "(empty)"
	}
	if len(v) <= 6 {
		return "***"
	}
	return v[:2] + "***" + v[len(v)-2:]
}
