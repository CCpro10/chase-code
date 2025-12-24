package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	gosdkclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"chase-code/server/tools"
)

// MCPRemoteServerConfig 描述一个通过 HTTP/SSE 方式连接的 MCP 服务。
// 示例 JSON 配置（多个 mcpServers）:
//
//	{
//	  "mcpServers": {
//	    "test": {
//	      "autoApprove": [],
//	      "disabled": false,
//	      "timeout": 60,
//	      "type": "streamableHttp",
//	      "url": "https://example.com/streamable"
//	    }
//	  }
//	}
type MCPRemoteServerConfig struct {
	AutoApprove []string `json:"autoApprove,omitempty"`
	Disabled    bool     `json:"disabled,omitempty"`
	Timeout     int      `json:"timeout,omitempty"` // 秒
	Type        string   `json:"type"`
	URL         string   `json:"url"`
}

// MCPConfig 是顶层 MCP 配置。
type MCPConfig struct {
	MCPServers map[string]MCPRemoteServerConfig `json:"mcpServers,omitempty"`
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

type startableMCPClient interface {
	Start(ctx context.Context) error
}

// NewMCPClientsFromConfig 基于 MCPConfig 创建一组 MCPClient 适配器。
// 支持 stdio / sse / streamable_http 三种连接方式。
func NewMCPClientsFromConfig(cfg *MCPConfig) ([]MCPClient, error) {
	if cfg == nil || len(cfg.MCPServers) == 0 {
		return nil, nil
	}

	clients := make([]MCPClient, 0, len(cfg.MCPServers))
	if len(cfg.MCPServers) > 0 {
		keys := make([]string, 0, len(cfg.MCPServers))
		for k := range cfg.MCPServers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, name := range keys {
			s := cfg.MCPServers[name]
			if s.Disabled {
				continue
			}
			if strings.TrimSpace(s.URL) == "" {
				return nil, fmt.Errorf("MCP server %q 缺少 url 字段", name)
			}
			mcpType := normalizeMCPType(s.Type)
			if mcpType == "" {
				return nil, fmt.Errorf("MCP server %q type 不支持: %q", name, s.Type)
			}
			timeout := resolveTimeout(s.Timeout)

			var client *gosdkclient.Client
			switch mcpType {
			case "sse":
				httpClient := &http.Client{Timeout: timeout}
				var err error
				client, err = gosdkclient.NewSSEMCPClient(s.URL, gosdkclient.WithHTTPClient(httpClient))
				if err != nil {
					return nil, fmt.Errorf("创建 MCP SSE client %q 失败: %w", name, err)
				}
			case "streamable_http":
				var err error
				client, err = gosdkclient.NewStreamableHttpClient(s.URL, transport.WithHTTPTimeout(timeout))
				if err != nil {
					return nil, fmt.Errorf("创建 MCP HTTP client %q 失败: %w", name, err)
				}
			default:
				return nil, fmt.Errorf("MCP server %q type 不支持: %q", name, s.Type)
			}

			if err := initMCPClient(context.Background(), name, client, timeout); err != nil {
				return nil, err
			}
			clients = append(clients, NewGoSDKMCPClient(client))
		}
	}
	return clients, nil
}

func normalizeMCPType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "sse":
		return "sse"
	case "streamablehttp", "streamable_http", "streamable-http":
		return "streamable_http"
	default:
		return ""
	}
}

func resolveTimeout(v int) time.Duration {
	if v <= 0 {
		return 60 * time.Second
	}
	return time.Duration(v) * time.Second
}

func initMCPClient(ctx context.Context, name string, client *gosdkclient.Client, timeout time.Duration) error {
	if client == nil {
		return fmt.Errorf("MCP client %q 为空", name)
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if startable, ok := any(client).(startableMCPClient); ok {
		if err := startable.Start(ctx); err != nil {
			return fmt.Errorf("启动 MCP client %q 失败: %w", name, err)
		}
	}
	initReq := mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name:    "chase-code",
				Version: "0.0.1",
			},
			Capabilities: mcp.ClientCapabilities{},
		},
	}
	if _, err := client.Initialize(ctx, initReq); err != nil {
		return fmt.Errorf("初始化 MCP client %q 失败: %w", name, err)
	}
	return nil
}

// MergeMCPTools 使用多个 MCPClient 拉取工具列表，并合并为 MCPTool/ToolSpec。
func MergeMCPTools(ctx context.Context, clients []MCPClient) ([]MCPTool, []tools.ToolSpec, error) {
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
