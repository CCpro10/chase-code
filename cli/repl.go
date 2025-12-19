package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func getOrInitReplAgent() (*replAgentSession, error) {
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
	as := &agent.Session{
		Client:   client,
		Router:   router,
		Sink:     server.ChanEventSink{Ch: events},
		MaxSteps: maxSteps,
	}

	// 启动事件渲染 goroutine
	go renderEvents(events)

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

		if strings.HasPrefix(line, ":") {
			if err := handleReplCommand(line); err != nil {
				fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			}
			continue
		}

		// 默认当成 agent 指令执行（而不是 shell 命令）。
		if err := runAgentTurn(line); err != nil {
			fmt.Fprintf(os.Stderr, "%sagent 执行失败: %v%s\n", colorYellow, err, colorReset)
		}
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
	case "read":
		if len(fields) != 2 {
			return fmt.Errorf("用法: :read <文件路径>")
		}
		return runRead([]string{fields[1]})
	case "shell":
		if len(fields) < 2 {
			return fmt.Errorf("用法: :shell <命令>")
		}
		return runShell([]string{"--", strings.Join(fields[1:], " ")})
	case "edit":
		// :edit <file> <from> <to> [all]
		if len(fields) < 4 {
			return fmt.Errorf("用法: :edit <file> <from> <to> [all]")
		}
		file := fields[1]
		from := fields[2]
		to := fields[3]
		all := len(fields) >= 5 && fields[4] == "all"

		abs, err := filepath.Abs(file)
		if err != nil {
			return err
		}
		if err := servertools.ApplyEdit(abs, from, to, all); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "已更新文件: %s\n", abs)
		return nil
	case "agent":
		// :agent <指令>
		rest := strings.TrimSpace(strings.TrimPrefix(line, ":agent"))
		if rest == "" {
			return fmt.Errorf("用法: :agent <任务描述>")
		}
		return runAgentTurn(rest)
	default:
		return fmt.Errorf("未知 repl 命令: %s", cmd)
	}
	return nil
}

const maxSteps = 10

func runAgentTurn(userInput string) error {
	sess, err := getOrInitReplAgent()
	if err != nil {
		return err
	}

	baseCtx := context.Background()
	ctx, cancel := context.WithTimeout(baseCtx, 60*time.Second)
	defer cancel()

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
  :read <file>         读取并打印文件内容
  :edit <f> <from> <to> [all]  在文件中做字符串替换（all 表示替换全部）
  :agent <指令>        通过 LLM+工具自动完成一步任务

默认行为:
  直接输入不以冒号开头的内容时，等价于 :agent <输入行>。`)
}
