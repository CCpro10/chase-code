package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	gosdkclient "github.com/mark3labs/mcp-go/client"
)

// MCPServerConfig 描述一个通过 MCP 连接的外部工具服务器。
// 当前仿照 codex 的 "mcp-server" 能力，只关注 stdio 进程方式。
//
// 示例 JSON 配置（多个 server）:
//
// {
//   "servers": [
//     {
//       "name": "filesystem",
//       "command": "mcp-filesystem",
//       "args": ["--root", "/Users/me/project"],
//       "env": ["FOO=bar"],
//       "cwd": "/Users/me/project"
//     }
//   ]
// }
//
// chase-code 通过 go-sdk 创建 stdio MCP client 并与这些 server 通信。
//
type MCPServerConfig struct {
	Name    string   `json:"name"`              // 在 LLM 工具名中使用的前缀/标识
	Command string   `json:"command"`          // 可执行文件名，如 "mcp-filesystem" 或 "codex-mcp-server"
	Args    []string `json:"args,omitempty"`   // 额外参数
	Env     []string `json:"env,omitempty"`    // 传递给子进程的环境变量，默认继承 os.Environ
	Cwd     string   `json:"cwd,omitempty"`    // 子进程工作目录，不填则使用当前 cwd
}

// MCPConfig 是顶层 MCP 配置。
type MCPConfig struct {
	Servers []MCPServerConfig `json:"servers"`
}

// LoadMCPConfig 从 JSON 文件加载 MCPConfig。
// 若路径为空或文件不存在，则返回 (nil, nil)。
func LoadMCPConfig(path string) (*MCPConfig, error) {
	if path == "" {
		return nil, nil
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析 MCP 配置路径失败: %w", err)
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 MCP 配置失败: %w", err)
	}

	var cfg MCPConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析 MCP 配置 JSON 失败: %w", err)
	}
	return &cfg, nil
}

// NewMCPClientsFromConfig 基于 MCPConfig 创建一组 MCPClient 适配器。
// 每个 server 对应一个 go-sdk stdio client，并包装成 GoSDKMCPClient。
func NewMCPClientsFromConfig(cfg *MCPConfig) ([]MCPClient, error) {
	if cfg == nil || len(cfg.Servers) == 0 {
		return nil, nil
	}

	clients := make([]MCPClient, 0, len(cfg.Servers))
	for _, s := range cfg.Servers {
		if s.Command == "" {
			return nil, fmt.Errorf("MCP server %q 缺少 command 字段", s.Name)
		}

		// 合并环境变量: 先继承当前进程，再追加配置中 env。
		env := os.Environ()
		if len(s.Env) > 0 {
			env = append(env, s.Env...)
		}

		client, err := gosdkclient.NewStdioMCPClient(s.Command, env, s.Args...)
		if err != nil {
			return nil, fmt.Errorf("启动 MCP server %q 失败: %w", s.Name, err)
		}

		clients = append(clients, NewGoSDKMCPClient(client))
	}
	return clients, nil
}

// MergeMCPTools 使用多个 MCPClient 拉取工具列表，并合并为 MCPTool/ToolSpec。
func MergeMCPTools(ctx context.Context, clients []MCPClient) ([]MCPTool, []ToolSpec, error) {
	if len(clients) == 0 {
		return nil, nil, nil
	}

	var allTools []MCPTool
	for _, c := range clients {
		tools, err := c.ListTools(ctx)
		if err != nil {
			return nil, nil, err
		}
		allTools = append(allTools, tools...)
	}

	specs := ToolSpecsFromMCP(allTools)
	return allTools, specs, nil
}
