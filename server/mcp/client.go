package mcp

import (
	"context"
	"encoding/json"
	"log"

	gosdkclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	pkgtools "chase-code/server/tools"
)

// MCPTool 描述来自 MCP 服务器的一条工具定义信息。
// 这里不直接依赖具体 MCP SDK，而是抽象出 chase-code 所需的最小字段。
type MCPTool struct {
	Name        string          // 工具名称，在调用时作为唯一标识
	Description string          // 简要描述，最终会出现在 ToolSpec.Description 中
	Parameters  json.RawMessage // JSON Schema 或参数描述，透传给 ToolSpec.Parameters
}

// MCPClient 抽象了一个可以调用 MCP 工具的客户端。
// 具体的实现可以由上层按各自的 MCP SDK / 传输方式提供。
type MCPClient interface {
	// ListTools 返回当前可用的 MCP 工具列表。
	ListTools(ctx context.Context) ([]MCPTool, error)

	// CallTool 调用指定的 MCP 工具，并返回其输出文本。
	// 参数 arguments 的结构由具体工具定义负责解释。
	CallTool(ctx context.Context, name string, arguments json.RawMessage) (string, error)
}

// ToolSpecsFromMCP 将 MCPTool 列表转换为 chase-code 内部使用的 ToolSpec 列表，
// 方便统一拼接到 DefaultToolSpecs 或自定义工具集合中。
func ToolSpecsFromMCP(tools []MCPTool) []pkgtools.ToolSpec {
	out := make([]pkgtools.ToolSpec, 0, len(tools))
	for _, t := range tools {
		out = append(out, pkgtools.ToolSpec{
			Kind:        pkgtools.ToolKindCustom,
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		})
	}
	return out
}

// GoSDKMCPClient 是对 github.com/mark3labs/mcp-go 客户端的一个适配器，
// 实现了当前 mcp 包中的 MCPClient 接口，方便与 ToolRouter 集成。
type GoSDKMCPClient struct {
	inner gosdkclient.MCPClient
}

// NewGoSDKMCPClient 将 go-sdk 的 MCPClient 包装为 mcp.MCPClient。
func NewGoSDKMCPClient(inner gosdkclient.MCPClient) MCPClient {
	return &GoSDKMCPClient{inner: inner}
}

// ListTools 调用 go-sdk 的 ListTools API，并转换成简化的 MCPTool 描述。
func (c *GoSDKMCPClient) ListTools(ctx context.Context) ([]MCPTool, error) {
	if c == nil || c.inner == nil {
		return nil, nil
	}

	res, err := c.inner.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, err
	}

	out := make([]MCPTool, 0, len(res.Tools))
	for _, t := range res.Tools {
		var params json.RawMessage

		b, err := mcp.ToolArgumentsSchema(t.InputSchema).MarshalJSON()
		if err != nil {
			log.Println(err)
			continue
		}
		params = b

		out = append(out, MCPTool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		})
	}
	return out, nil
}

// normalizeMCPJSONSchema 尝试修正来自 MCP server 的 JSON Schema，
// 以满足 OpenAI Responses tools 的校验要求。
// 目前主要处理类似 `{ "type": "object" }` 但缺少 `properties`
// 的场景，否则会触发 "object schema missing properties" 错误。
func normalizeMCPJSONSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		// 无法解析时直接透传，避免静默丢弃。
		log.Printf("[mcp] 无法解析工具参数 schema，直接透传原始数据: %v", err)
		return raw
	}

	typ, _ := m["type"].(string)
	if typ == "object" {
		_, hasProps := m["properties"]
		if !hasProps {
			// 对于不带任何字段的工具（如 GetCurrentTime），这里补一个空 properties，
			// 以通过 Responses 的 JSON Schema 校验。
			m["properties"] = map[string]any{}
		}
	}

	fixed, err := json.Marshal(m)
	if err != nil {
		log.Printf("[mcp] 序列化修正后的 schema 失败，回退到原始数据: %v", err)
		return raw
	}
	return fixed
}

// CallTool 调用 go-sdk 的 CallTool API，并将返回内容序列化为字符串。
func (c *GoSDKMCPClient) CallTool(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	if c == nil || c.inner == nil {
		return "", nil
	}

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      name,
			Arguments: arguments,
		},
	}

	res, err := c.inner.CallTool(ctx, req)
	if err != nil {
		return "", err
	}

	// 优先尝试从 content 中提取文本；若没有，则回退到 structuredContent 或整体 JSON。
	if len(res.Content) > 0 {
		if b, err := json.Marshal(res.Content); err == nil {
			return string(b), nil
		}
	}

	if res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			return string(b), nil
		}
	}

	// 没有任何内容时返回空串
	return "", nil
}
