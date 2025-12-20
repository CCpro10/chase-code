package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"chase-code/server"
	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	mcpm "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

type fakeMCPClient struct {
	tools   []MCPTool
	callOut map[string]string
	listErr error
	callErr error
}

func (f *fakeMCPClient) ListTools(ctx context.Context) ([]MCPTool, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.tools, nil
}

func (f *fakeMCPClient) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if f.callErr != nil {
		return "", f.callErr
	}
	if f.callOut == nil {
		return "", nil
	}
	return f.callOut[name], nil
}

func TestToolSpecsFromMCP(t *testing.T) {
	tools := []MCPTool{
		{
			Name:        "fs.read",
			Description: "读取文件",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		},
	}

	specs := ToolSpecsFromMCP(tools)
	if len(specs) != 1 {
		t.Fatalf("expect 1 ToolSpec, got %d", len(specs))
	}
	s := specs[0]
	if s.Kind != server.ToolKindCustom {
		t.Errorf("unexpected kind: %v", s.Kind)
	}
	if s.Name != "fs.read" {
		t.Errorf("unexpected name: %s", s.Name)
	}
	if s.Description != "读取文件" {
		t.Errorf("unexpected description: %s", s.Description)
	}
	if len(s.Parameters) == 0 {
		t.Errorf("expect non-empty parameters")
	}
}

func TestMultiMCPClient_ListToolsAndCallTool(t *testing.T) {
	ctx := context.Background()

	c1 := &fakeMCPClient{
		tools: []MCPTool{{Name: "a"}},
		callOut: map[string]string{
			"echo": "from-1",
		},
	}
	c2 := &fakeMCPClient{
		tools: []MCPTool{{Name: "b"}},
		callOut: map[string]string{
			"echo": "from-2",
		},
	}

	multi := MultiMCPClient{c1, c2}

	tools, err := multi.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools error: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expect 2 tools, got %d", len(tools))
	}

	out, err := multi.CallTool(ctx, "echo", nil)
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if out != "from-1" {
		t.Fatalf("expect first client result 'from-1', got %q", out)
	}
}

func TestMergeMCPTools_WithFakeClients(t *testing.T) {
	ctx := context.Background()

	c1 := &fakeMCPClient{tools: []MCPTool{{Name: "a"}}}
	c2 := &fakeMCPClient{tools: []MCPTool{{Name: "b"}}}

	all, specs, err := MergeMCPTools(ctx, []MCPClient{c1, c2})
	if err != nil {
		t.Fatalf("MergeMCPTools error: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expect 2 MCPTool, got %d", len(all))
	}
	if len(specs) != 2 {
		t.Fatalf("expect 2 ToolSpec, got %d", len(specs))
	}
}

func TestLoadMCPConfig_EmptyAndMissing(t *testing.T) {
	cfg, err := LoadMCPConfig("")
	if err != nil {
		t.Fatalf("empty path should not error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("empty path should return nil config")
	}

	cfg, err = LoadMCPConfig("/path/does/not/exist/mcp.json")
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("missing file should return nil config")
	}
}

// getFreePort 返回一个可用的本地 TCP 端口，用于测试 HTTP/SSE 服务器。
func getFreePort(t *testing.T) int {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("failed to listen on random port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestHTTPTimeMCPServer 启动一个基于 HTTP SSE 的 MCP Server，并通过 mcp-go client 调用 now 工具获取当前时间。
func TestHTTPTimeMCPServer(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式下跳过 HTTP SSE MCP 测试")
	}

	port := getFreePort(t)
	addr := fmt.Sprintf(":%d", port)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	// 1. 创建 MCP Server，注册 now 工具
	srv := mcpserver.NewMCPServer("chase-code-time-server", "0.0.1")

	srv.AddTool(mcpm.Tool{
		Name:        "now",
		Description: "返回当前时间（RFC3339）",
		InputSchema: mcpm.ToolInputSchema{
			Type:       "object",
			Properties: map[string]any{},
		},
	}, func(ctx context.Context, req mcpm.CallToolRequest) (*mcpm.CallToolResult, error) {
		now := time.Now().UTC().Format(time.RFC3339)
		return &mcpm.CallToolResult{
			Content: []mcpm.Content{
				mcpm.TextContent{Type: "text", Text: now},
			},
		}, nil
	})

	httpSrv := mcpserver.NewStreamableHTTPServer(srv)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := httpSrv.Start(addr); err != nil && err != http.ErrServerClosed {
			t.Logf("HTTP MCP server error: %v", err)
		}
	}()

	// 等待服务启动
	time.Sleep(300 * time.Millisecond)

	// 2. 创建 HTTP SSE 传输和 MCP client
	transport, err := mcptransport.NewStreamableHTTP(baseURL + "/mcp")
	if err != nil {
		t.Fatalf("NewStreamableHTTP error: %v", err)
	}
	defer transport.Close()

	client := mcpclient.NewClient(transport)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Start(ctx); err != nil {
		t.Fatalf("client.Start error: %v", err)
	}

	// 3. 初始化 MCP session
	initReq := mcpm.InitializeRequest{
		Params: mcpm.InitializeParams{
			ProtocolVersion: mcpm.LATEST_PROTOCOL_VERSION,
			Capabilities:    mcpm.ClientCapabilities{},
			ClientInfo: mcpm.Implementation{
				Name:    "chase-code-mcp-http-test",
				Version: "0.0.1",
			},
		},
	}

	if _, err := client.Initialize(ctx, initReq); err != nil {
		t.Fatalf("client.Initialize error: %v", err)
	}

	// 4. 调用 now 工具
	res, err := client.CallTool(ctx, mcpm.CallToolRequest{
		Params: mcpm.CallToolParams{
			Name: "now",
		},
	})
	if err != nil {
		t.Fatalf("CallTool(now) error: %v", err)
	}

	if res.IsError {
		t.Fatalf("now tool returned error: %#v", res.Content)
	}
	if len(res.Content) == 0 {
		t.Fatalf("now tool should return at least one content item")
	}

	tc, ok := res.Content[0].(mcpm.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}

	if _, err := time.Parse(time.RFC3339, tc.Text); err != nil {
		t.Fatalf("now tool response is not valid RFC3339 time: %v (value=%q)", err, tc.Text)
	}

	// 5. 清理
	_ = client.Close()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelShutdown()
	_ = httpSrv.Shutdown(shutdownCtx)
	<-done
}
