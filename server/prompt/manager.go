package prompt

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	servertools "chase-code/server/tools"
)

//go:embed system.md
var defaultSystemPrompt string

//go:embed compact.md
var defaultCompactPrompt string

type ToolInfo struct {
	Index       int
	Name        string
	Description string
}

type SystemPromptData struct {
	Tools []ToolInfo
}

// Manager 负责管理提示词模板。
type Manager struct {
	systemTemplate *template.Template
}

var globalManager *Manager

// Init 初始化 Prompt Manager。
func Init() error {
	// 尝试从用户配置目录加载自定义模板
	userPath := filepath.Join(os.Getenv("HOME"), ".chase-code", "prompts", "system.md")
	content := defaultSystemPrompt

	if data, err := os.ReadFile(userPath); err == nil {
		content = string(data)
	}

	tmpl, err := template.New("system").Parse(content)
	if err != nil {
		return fmt.Errorf("解析系统提示词模板失败: %w", err)
	}

	globalManager = &Manager{
		systemTemplate: tmpl,
	}
	return nil
}

// BuildSystemPrompt 根据工具列表渲染 System Prompt。
func BuildSystemPrompt(tools []servertools.ToolSpec) (string, error) {
	if globalManager == nil {
		if err := Init(); err != nil {
			return "", err
		}
	}

	var toolInfos []ToolInfo
	for i, t := range tools {
		toolInfos = append(toolInfos, ToolInfo{
			Index:       i + 1,
			Name:        t.Name,
			Description: t.Description,
		})
	}

	var buf bytes.Buffer
	if err := globalManager.systemTemplate.Execute(&buf, SystemPromptData{Tools: toolInfos}); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// GetCompactPrompt 返回压缩提示词。
func GetCompactPrompt() string {
	return defaultCompactPrompt
}
