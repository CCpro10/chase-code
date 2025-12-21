package cli

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"chase-code/server"
	servermcp "chase-code/server/mcp"
	servertools "chase-code/server/tools"
)

func initMCPTools(cfgPath string, tools []server.ToolSpec, router *servertools.ToolRouter) ([]server.ToolSpec, *servertools.ToolRouter, error) {
	cfgPath = strings.TrimSpace(cfgPath)
	if cfgPath == "" {
		return tools, router, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	log.Printf("[mcp] loading config path=%s", cfgPath)
	mcpCfg, err := servermcp.LoadMCPConfig(cfgPath)
	if err != nil {
		return tools, router, fmt.Errorf("加载 MCP 配置失败: %w", err)
	}
	if mcpCfg == nil || len(mcpCfg.MCPServers) == 0 {
		log.Printf("[mcp] empty config, skip")
		return tools, router, nil
	}

	keys := make([]string, 0, len(mcpCfg.MCPServers))
	for k := range mcpCfg.MCPServers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := mcpCfg.MCPServers[k]
		log.Printf("[mcp] server=%s type=%s url=%s disabled=%t timeout=%ds auto_approve=%d",
			k,
			strings.TrimSpace(s.Type),
			strings.TrimSpace(s.URL),
			s.Disabled,
			s.Timeout,
			len(s.AutoApprove),
		)
	}

	clients, err := servermcp.NewMCPClientsFromConfig(mcpCfg)
	if err != nil {
		return tools, router, fmt.Errorf("创建 MCP 客户端失败: %w", err)
	}
	if len(clients) == 0 {
		log.Printf("[mcp] no clients created, skip")
		return tools, router, nil
	}

	_, mcpSpecs, err := servermcp.MergeMCPTools(ctx, clients)
	if err != nil {
		return tools, router, fmt.Errorf("获取 MCP tools 列表失败: %w", err)
	}

	base := len(tools)
	tools = append(tools, mcpSpecs...)
	router = servertools.NewToolRouterWithMCP(tools, servermcp.MultiMCPClient(clients))
	log.Printf("[mcp] merged tools total=%d (mcp=%d base=%d)", len(tools), len(mcpSpecs), base)

	return tools, router, nil
}
