package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"chase-code/server"
	servermcp "chase-code/server/mcp"
	servertools "chase-code/server/tools"
)

type replAgentSession struct {
	client   server.LLMClient
	router   *servertools.ToolRouter
	messages []server.Message
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

	replAgent = &replAgentSession{
		client:   client,
		router:   router,
		messages: []server.Message{{Role: server.RoleSystem, Content: systemPrompt}},
	}
	return replAgent, nil
}

func runRepl() error {
	cwd, _ := os.Getwd()
	fmt.Fprintf(os.Stderr, "chase-code repl（agent 优先），当前工作目录: %s\n", cwd)
	fmt.Fprintln(os.Stderr, "直接输入问题或指令时，将通过 LLM+工具以 agent 方式执行；输入 :help 查看可用命令，:q 退出。")

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Fprint(os.Stderr, "chase> ")
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
			fmt.Fprintf(os.Stderr, "agent 执行失败: %v\n", err)
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

	// 将本轮用户输入记录到对话历史
	sess.messages = append(sess.messages, server.Message{Role: server.RoleUser, Content: userInput})

	// 为防止死循环，这里限制最多连续进行若干步工具调用 + 回复

	baseCtx := context.Background()

	for step := 0; step < maxSteps; step++ {
		// 用当前历史构造 Prompt
		prompt := server.Prompt{Messages: sess.messages}

		ctx, cancel := context.WithTimeout(baseCtx, 60*time.Second)
		res, err := sess.client.Complete(ctx, prompt)
		cancel()
		if err != nil {
			return err
		}
		reply := res.Message.Content

		// 尝试将回复解析为工具调用 JSON
		calls, err := servertools.ParseToolCallsJSON(reply)
		fromFallback := false
		if err != nil {
			// 解析失败时，尝试从自然语言中提取“调用工具 X，参数: {...}”这种模式，
			// 以兼容模型没有严格遵守“只输出 JSON”的情况。
			if fallbackCalls, ok := parseToolCallsFromText(reply); ok {
				calls = fallbackCalls
				fromFallback = true
			} else {
				fmt.Fprintln(os.Stderr, "[agent 回复]")
				fmt.Println(reply)
				sess.messages = append(sess.messages, server.Message{Role: server.RoleAssistant, Content: reply})
				return nil
			}
		}

		// 解析成功，认为这是工具调用指令
		if fromFallback {
			// 保留模型输出的自然语言工具规划，方便用户观察内部决策过程。
			fmt.Fprintln(os.Stderr, "[agent 内部工具规划]")
			fmt.Println(reply)
			fmt.Fprintln(os.Stderr, "[agent 工具调用（从自然语言解析）]")
			for _, c := range calls {
				fmt.Fprintf(os.Stderr, "  - %s\n", c.ToolName)
			}
		} else {
			fmt.Fprintln(os.Stderr, "[agent 工具调用 JSON]")
			fmt.Fprintln(os.Stderr, reply)
		}

		// 依次执行所有工具调用，并把结果写回对话历史
		for _, c := range calls {
			item, err := sess.router.Execute(ctx, c)
			if err != nil {
				fmt.Fprintf(os.Stderr, "工具 %s 执行失败: %v\n", c.ToolName, err)
				continue
			}
			fmt.Fprintf(os.Stderr, "[tool %s 输出]\n", c.ToolName)
			fmt.Println(item.ToolOutput)

			// 把工具结果写回对话历史，便于后续多轮推理。
			sess.messages = append(sess.messages,
				server.Message{Role: server.RoleAssistant, Content: fmt.Sprintf("工具 %s 的输出:\n%s", item.ToolName, item.ToolOutput)},
			)
		}

		// 本轮结束后，继续下一轮循环，LLM 将在新的历史基础上决定是再次调用工具还是直接给出回答
	}

	fmt.Fprintln(os.Stderr, "agent 工具调用已达到最大步数，停止。")
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
