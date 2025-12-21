package cli

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"chase-code/agent"
	"chase-code/config"
	"chase-code/server"
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
	session *agent.Session
	events  chan server.Event
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

	session, err := initReplAgentSession()
	if err != nil {
		return nil, err
	}
	replAgent = session
	return replAgent, nil
}

// initReplAgentSession 初始化 REPL 使用的 agent 会话。
func initReplAgentSession() (*replAgentSession, error) {
	client, err := initLLMClient()
	if err != nil {
		return nil, err
	}

	_, router := initToolRouter()
	systemPrompt := servertools.BuildToolSystemPrompt(router.Specs())

	events := make(chan server.Event, 128)
	as := agent.NewSession(client, router, server.ChanEventSink{Ch: events}, maxSteps)
	as.ResetHistoryWithSystemPrompt(systemPrompt)
	go renderEvents(events, as.ApprovalsChan())

	return &replAgentSession{
		session: as,
		events:  events,
	}, nil
}

// initLLMClient 构建 LLM 配置并初始化客户端。
func initLLMClient() (server.LLMClient, error) {
	cfg, err := server.NewLLMConfigFromEnv()
	if err != nil {
		return nil, err
	}
	client, err := server.NewLLMClient(cfg)
	if err != nil {
		return nil, err
	}
	log.Printf("[config] %s", config.Get().Summary())
	return client, nil
}

// initToolRouter 初始化本地工具并按需接入 MCP。
func initToolRouter() ([]server.ToolSpec, *servertools.ToolRouter) {
	tools := servertools.DefaultToolSpecs()
	router := servertools.NewToolRouter(tools)

	// 可选：通过配置接入 MCP tools（仿照 codex 的 mcp-server 能力）
	// 配置路径通过环境变量 CHASE_CODE_MCP_CONFIG 指定，格式为 JSON：
	// {
	//   "servers": [
	//     {"name": "fs", "command": "mcp-filesystem", "args": ["--root", "/path"], "env": ["FOO=bar"], "cwd": "/path"}
	//   ]
	// }
	if cfgPath := config.Get().MCPConfigPath; cfgPath != "" {
		var mcpErr error
		tools, router, mcpErr = initMCPTools(cfgPath, tools, router)
		if mcpErr != nil {
			fmt.Fprintf(os.Stderr, "初始化 MCP 失败: %v\n", mcpErr)
		}
	}

	return tools, router
}

func runRepl() error {
	ensureTerminalEraseKey()
	printReplBanner()

	scanner := bufio.NewScanner(os.Stdin)
	for {
		line, ok := readReplLine(scanner)
		if !ok {
			return nil
		}
		if line == "" {
			continue
		}

		handled, err := tryHandleApprovalShortcut(line)
		if handled {
			if err != nil {
				fmt.Fprintf(os.Stderr, "审批失败: %v\n", err)
			}
			continue
		}

		if isAgentRunning() && !isAllowedWhileAgentRunning(line) {
			fmt.Fprintln(os.Stderr, "当前有任务在执行，请先处理审批或等待完成")
			continue
		}

		if err := dispatchReplLine(line); err != nil {
			handleReplDispatchError(line, err)
		}
	}
}

// printReplBanner 输出启动提示信息。
func printReplBanner() {
	cwd, _ := os.Getwd()
	fmt.Fprintf(os.Stderr, "chase-code repl（agent 优先），当前工作目录: %s\n", cwd)
	fmt.Fprintln(os.Stderr, "直接输入问题或指令时，将通过 LLM+工具以 agent 方式执行；输入 :help 查看可用命令，:q 退出。")
}

// readReplLine 读取一行输入，返回是否读取成功。
func readReplLine(scanner *bufio.Scanner) (string, bool) {
	fmt.Fprintf(os.Stderr, "%schase> %s", colorCyan, colorReset)
	if !scanner.Scan() {
		fmt.Fprintln(os.Stderr)
		return "", false
	}
	return strings.TrimSpace(scanner.Text()), true
}

// tryHandleApprovalShortcut 处理 y/s 快捷审批输入。
func tryHandleApprovalShortcut(line string) (bool, error) {
	if !isApprovalShortcut(line) {
		return false, nil
	}

	if id := consumePendingApprovalID(); id != "" {
		approved := strings.EqualFold(line, "y")
		return true, sendApproval(id, approved)
	}
	if isAgentRunning() {
		fmt.Fprintln(os.Stderr, "当前没有待审批请求")
		return true, nil
	}
	return false, nil
}

// isApprovalShortcut 判断输入是否为 y/s 快捷审批。
func isApprovalShortcut(line string) bool {
	return strings.EqualFold(line, "y") || strings.EqualFold(line, "s")
}

// dispatchReplLine 根据输入前缀分发命令。
func dispatchReplLine(line string) error {
	if strings.HasPrefix(line, "/") {
		return handleSlashCommand(line)
	}
	if strings.HasPrefix(line, ":") {
		return handleReplCommand(line)
	}
	return startAgentTurn(line)
}

// handleReplDispatchError 根据命令类型输出不同的错误提示。
func handleReplDispatchError(line string, err error) {
	if strings.HasPrefix(line, "/") || strings.HasPrefix(line, ":") {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "%sagent 执行失败: %v%s\n", colorYellow, err, colorReset)
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
	cmd := parseReplCommand(line)
	if cmd.name == "" {
		return nil
	}

	switch cmd.name {
	case "q", "quit", "exit":
		os.Exit(0)
	case "help":
		printReplHelp()
		return nil
	case "shell":
		return handleShellCommand(cmd.args)
	case "agent":
		return handleAgentCommand(line)
	case "approve":
		return handleApprovalCommand(cmd.args, true)
	case "reject":
		return handleApprovalCommand(cmd.args, false)
	case "y", "s":
		return handleApprovalShortcutCommand(cmd.name)
	default:
		return fmt.Errorf("未知 repl 命令: %s", cmd.name)
	}
	return nil
}

type replCommand struct {
	name string
	args []string
}

// parseReplCommand 解析 : 开头的 repl 命令。
func parseReplCommand(line string) replCommand {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return replCommand{}
	}
	return replCommand{
		name: strings.TrimPrefix(fields[0], ":"),
		args: fields[1:],
	}
}

// handleShellCommand 处理 :shell 命令。
func handleShellCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: :shell <命令>")
	}
	return runShell([]string{"--", strings.Join(args, " ")})
}

// handleAgentCommand 处理 :agent 命令。
func handleAgentCommand(line string) error {
	rest := strings.TrimSpace(strings.TrimPrefix(line, ":agent"))
	if rest == "" {
		return fmt.Errorf("用法: :agent <任务描述>")
	}
	return startAgentTurn(rest)
}

// handleApprovalCommand 处理 :approve/:reject 命令。
func handleApprovalCommand(args []string, approved bool) error {
	if len(args) != 1 {
		if approved {
			return fmt.Errorf("用法: :approve <请求ID>")
		}
		return fmt.Errorf("用法: :reject <请求ID>")
	}
	return sendApproval(args[0], approved)
}

// handleApprovalShortcutCommand 处理 :y/:s 命令。
func handleApprovalShortcutCommand(cmd string) error {
	if id := consumePendingApprovalID(); id != "" {
		approved := cmd == "y"
		return sendApproval(id, approved)
	}
	return fmt.Errorf("当前没有待审批请求")
}

const maxSteps = 40

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
	return sess.session.RunTurn(ctx, userInput)
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
