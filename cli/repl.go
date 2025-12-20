package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"chase-code/agent"
	"chase-code/server"
	servermcp "chase-code/server/mcp"
	servertools "chase-code/server/tools"
)

const (
	colorReset   = "\033[0m"
	colorDim     = "\033[2m"
	colorCyan    = "\033[36m"
	colorYellow  = "\033[33m"
	colorGreen   = "\033[32m"
	colorMagenta = "\033[35m"
)

type replAgentSession struct {
	session  *agent.Session
	messages []server.Message
	events   chan server.Event
}

var replAgent *replAgentSession

var (
	replAgentMu sync.Mutex

	pendingApprovalMu sync.Mutex
	pendingApprovalID string

	agentRunningMu sync.Mutex
	agentRunning   bool
)

// setPendingApprovalID 在收到补丁审批请求事件时记录当前待审批的请求ID。
func setPendingApprovalID(id string) {
	pendingApprovalMu.Lock()
	defer pendingApprovalMu.Unlock()
	pendingApprovalID = id
}

// consumePendingApprovalID 在用户通过快捷键 y/s 做出决策时，取出并清空当前待审批请求ID。
func consumePendingApprovalID() string {
	pendingApprovalMu.Lock()
	defer pendingApprovalMu.Unlock()
	id := pendingApprovalID
	pendingApprovalID = ""
	return id
}

func isAgentRunning() bool {
	agentRunningMu.Lock()
	defer agentRunningMu.Unlock()
	return agentRunning
}

func tryStartAgentTurn() bool {
	agentRunningMu.Lock()
	defer agentRunningMu.Unlock()
	if agentRunning {
		return false
	}
	agentRunning = true
	return true
}

func finishAgentTurn() {
	agentRunningMu.Lock()
	defer agentRunningMu.Unlock()
	agentRunning = false
}

func getOrInitReplAgent() (*replAgentSession, error) {
	replAgentMu.Lock()
	defer replAgentMu.Unlock()
	if replAgent != nil {
		return replAgent, nil
	}

	cfg, err := server.NewLLMConfigFromEnv()
	if err != nil {
		return nil, err
	}
	client, err := server.NewLLMClient(cfg)
	if err != nil {
		return nil, err
	}

	// 1. 基础本地工具
	tools := servertools.DefaultToolSpecs()
	router := servertools.NewToolRouter(tools)

	// 2. 可选：通过配置接入 MCP tools（仿照 codex 的 mcp-server 能力）
	// 配置路径通过环境变量 CHASE_CODE_MCP_CONFIG 指定，格式为 JSON：
	// {
	//   "servers": [
	//     {"name": "fs", "command": "mcp-filesystem", "args": ["--root", "/path"], "env": ["FOO=bar"], "cwd": "/path"}
	//   ]
	// }
	if cfgPath := os.Getenv("CHASE_CODE_MCP_CONFIG"); cfgPath != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if mcpCfg, err := servermcp.LoadMCPConfig(cfgPath); err != nil {
			fmt.Fprintf(os.Stderr, "加载 MCP 配置失败: %v\n", err)
		} else if mcpCfg != nil {
			clients, err := servermcp.NewMCPClientsFromConfig(mcpCfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "创建 MCP 客户端失败: %v\n", err)
			} else if len(clients) > 0 {
				_, mcpSpecs, err := servermcp.MergeMCPTools(ctx, clients)
				if err != nil {
					fmt.Fprintf(os.Stderr, "获取 MCP tools 列表失败: %v\n", err)
				} else {
					// 将 MCP tools 追加到工具列表，并用带 MCP 的 router 替换本地 router。
					tools = append(tools, mcpSpecs...)
					router = servertools.NewToolRouterWithMCP(tools, servermcp.MultiMCPClient(clients))
				}
			}
		}
	}

	systemPrompt := servertools.BuildToolSystemPrompt(router.Specs())

	// 创建事件通道和 Agent Session
	events := make(chan server.Event, 128)
	as := agent.NewSession(client, router, server.ChanEventSink{Ch: events}, maxSteps)

	// 启动事件渲染 goroutine
	go renderEvents(events, as.ApprovalsChan())

	replAgent = &replAgentSession{
		session:  as,
		messages: []server.Message{{Role: server.RoleSystem, Content: systemPrompt}},
		events:   events,
	}
	return replAgent, nil
}

func runRepl() error {
	cwd, _ := os.Getwd()
	fmt.Fprintf(os.Stderr, "chase-code repl（agent 优先），当前工作目录: %s\n", cwd)
	fmt.Fprintln(os.Stderr, "直接输入问题或指令时，将通过 LLM+工具以 agent 方式执行；输入 :help 查看可用命令，:q 退出。")

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Fprintf(os.Stderr, "%schase> %s", colorCyan, colorReset)
		if !scanner.Scan() {
			fmt.Fprintln(os.Stderr)
			return nil
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// 如果当前存在待审批的补丁请求，则支持直接输入 y / s 快捷审批。
		if strings.EqualFold(line, "y") || strings.EqualFold(line, "s") {
			if id := consumePendingApprovalID(); id != "" {
				approved := strings.EqualFold(line, "y")
				if err := sendApproval(id, approved); err != nil {
					fmt.Fprintf(os.Stderr, "审批失败: %v\n", err)
				}
				continue
			}
			if isAgentRunning() {
				fmt.Fprintln(os.Stderr, "当前没有待审批请求")
				continue
			}
			// 如果没有待审批请求，则按普通输入处理
		}

		if isAgentRunning() && !isAllowedWhileAgentRunning(line) {
			fmt.Fprintln(os.Stderr, "当前有任务在执行，请先处理审批或等待完成")
			continue
		}

		if strings.HasPrefix(line, "/") {
			if err := handleSlashCommand(line); err != nil {
				fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			}
			continue
		}

		if strings.HasPrefix(line, ":") {
			if err := handleReplCommand(line); err != nil {
				fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			}
			continue
		}

		// 默认当成 agent 指令执行（而不是 shell 命令）。
		if err := startAgentTurn(line); err != nil {
			fmt.Fprintf(os.Stderr, "%sagent 执行失败: %v%s\n", colorYellow, err, colorReset)
		}
	}
}

func isAllowedWhileAgentRunning(line string) bool {
	if strings.EqualFold(line, "y") || strings.EqualFold(line, "s") {
		return true
	}
	if !strings.HasPrefix(line, ":") {
		return false
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return true
	}
	cmd := strings.TrimPrefix(fields[0], ":")
	switch cmd {
	case "approve", "reject", "y", "s", "q", "quit", "exit", "help":
		return true
	default:
		return false
	}
}

func handleReplCommand(line string) (err error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil
	}

	cmd := strings.TrimPrefix(fields[0], ":")
	switch cmd {
	case "q", "quit", "exit":
		os.Exit(0)
	case "help":
		printReplHelp()
		return nil
	case "shell":
		if len(fields) < 2 {
			return fmt.Errorf("用法: :shell <命令>")
		}
		return runShell([]string{"--", strings.Join(fields[1:], " ")})
	case "agent":
		// :agent <指令>
		rest := strings.TrimSpace(strings.TrimPrefix(line, ":agent"))
		if rest == "" {
			return fmt.Errorf("用法: :agent <任务描述>")
		}
		return startAgentTurn(rest)
	case "approve":
		if len(fields) != 2 {
			return fmt.Errorf("用法: :approve <请求ID>")
		}
		return sendApproval(fields[1], true)
	case "reject":
		if len(fields) != 2 {
			return fmt.Errorf("用法: :reject <请求ID>")
		}
		return sendApproval(fields[1], false)
	case "y", "s":
		if id := consumePendingApprovalID(); id != "" {
			approved := cmd == "y"
			return sendApproval(id, approved)
		}
		return fmt.Errorf("当前没有待审批请求")
	default:
		return fmt.Errorf("未知 repl 命令: %s", cmd)
	}
	return nil
}

const maxSteps = 10

func startAgentTurn(userInput string) error {
	if !tryStartAgentTurn() {
		return fmt.Errorf("当前有任务在执行，请先处理审批或等待完成")
	}
	go func() {
		defer finishAgentTurn()
		if err := runAgentTurn(userInput); err != nil {
			fmt.Fprintf(os.Stderr, "%sagent 执行失败: %v%s\n", colorYellow, err, colorReset)
		}
	}()
	return nil
}

func runAgentTurn(userInput string) error {
	sess, err := getOrInitReplAgent()
	if err != nil {
		return err
	}

	ctx := context.Background()
	newHistory, err := sess.session.RunTurn(ctx, userInput, sess.messages)
	if err != nil {
		return err
	}
	sess.messages = newHistory
	return nil
}

func printReplHelp() {
	fmt.Fprintln(os.Stderr, `可用命令:
  :help                显示帮助
  :q / :quit / :exit   退出 repl
  :shell <cmd>         通过用户默认 shell 执行命令
  :agent <指令>        通过 LLM+工具自动完成一步任务
  :approve <id>        批准指定补丁请求（来自 apply_patch）
  :reject <id>         拒绝指定补丁请求

默认行为:
  直接输入不以冒号开头的内容时，等价于 :agent <输入行>。`)
}
