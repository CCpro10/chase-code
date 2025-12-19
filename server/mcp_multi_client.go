package server

import (
	"context"
	"encoding/json"
)

// MultiMCPClient 是一个简单的 MCPClient 聚合器，用于在同一 ToolRouter 中
// 复用多个 MCP server。当前实现非常保守：
//   - ListTools: 聚合所有底层客户端的 tools
//   - CallTool: 依次尝试所有客户端，直到第一个调用成功或全部失败
//
// 这与 codex 的 "多 MCP server" 模式类似，但实现上更简化。
type MultiMCPClient []MCPClient

func (m MultiMCPClient) ListTools(ctx context.Context) ([]MCPTool, error) {
	var all []MCPTool
	for _, c := range m {
		tools, err := c.ListTools(ctx)
		if err != nil {
			return nil, err
		}
		all = append(all, tools...)
	}
	return all, nil
}

func (m MultiMCPClient) CallTool(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	var lastErr error
	for _, c := range m {
		out, err := c.CallTool(ctx, name, arguments)
		if err == nil {
			return out, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", nil
}
