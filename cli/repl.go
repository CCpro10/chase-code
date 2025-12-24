package cli

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"chase-code/config"
	"chase-code/server"
	"chase-code/server/llm"
	servertools "chase-code/server/tools"
)

type replAgentSession struct {
	session *server.Session
	events  chan server.Event
}

var replAgent *replAgentSession

var (
	replAgentMu sync.Mutex

	agentRunningMu sync.Mutex
	agentRunning   bool
)

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
	if replAgent != nil && replAgent.session != nil {
		return replAgent, nil
	}

	var events chan server.Event
	if replAgent != nil {
		events = replAgent.events
	}
	session, err := initReplAgentSession(events)
	if err != nil {
		return nil, err
	}
	if replAgent == nil {
		replAgent = session
	} else {
		replAgent.session = session.session
		replAgent.events = session.events
	}
	return replAgent, nil
}

// getReplEvents 获取 REPL 的事件通道，必要时会创建占位通道。
func getReplEvents() chan server.Event {
	replAgentMu.Lock()
	defer replAgentMu.Unlock()
	if replAgent != nil && replAgent.events != nil {
		return replAgent.events
	}
	events := make(chan server.Event, 128)
	if replAgent == nil {
		replAgent = &replAgentSession{}
	}
	replAgent.events = events
	return events
}

// initReplAgentSession 初始化 REPL 使用的 agent 会话。
func initReplAgentSession(events chan server.Event) (*replAgentSession, error) {
	client, err := initLLMClient()
	if err != nil {
		return nil, err
	}

	_, router := initToolRouter()
	systemPrompt := server.BuildToolSystemPrompt(router.Specs())

	if events == nil {
		events = make(chan server.Event, 128)
	}
	as := server.NewSession(client, router, server.ChanEventSink{Ch: events}, maxSteps)
	as.ResetHistoryWithSystemPrompt(systemPrompt)

	return &replAgentSession{
		session: as,
		events:  events,
	}, nil
}

// initLLMClient 构建 LLM 配置并初始化客户端。
func initLLMClient() (llm.LLMClient, error) {
	model, err := llm.NewLLMModelFromEnv()
	if err != nil {
		return nil, err
	}
	client, err := llm.NewLLMClient(model)
	if err != nil {
		return nil, err
	}
	log.Printf("[config] %s", config.Get().Summary())
	return client, nil
}

// initToolRouter 初始化本地工具并按需接入 MCP。
func initToolRouter() ([]servertools.ToolSpec, *servertools.ToolRouter) {
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
	events := getReplEvents()
	return runReplTUI(events)
}

func isAllowedWhileAgentRunning(line string) bool {
	if strings.EqualFold(line, "y") || strings.EqualFold(line, "s") {
		return true
	}
	if !hasCommandPrefix(line) {
		return false
	}
	cmd := parseReplCommand(line)
	if cmd.name == "" {
		return true
	}
	switch cmd.name {
	case "approve", "reject", "y", "s", "q", "quit", "exit", "help":
		return true
	default:
		return false
	}
}

type replDispatchResult struct {
	lines []string
	quit  bool
}

// dispatchReplInput 根据输入内容分发命令并返回渲染所需的输出。
func dispatchReplInput(line string, pendingApprovalID string) (replDispatchResult, error) {
	if strings.TrimSpace(line) == "" {
		return replDispatchResult{}, nil
	}

	if handled, result, err := handleApprovalShortcut(line, pendingApprovalID); handled {
		return result, err
	}

	if isAgentRunning() && !isAllowedWhileAgentRunning(line) {
		return replDispatchResult{}, fmt.Errorf("当前有任务在执行，请先处理审批或等待完成")
	}

	if hasCommandPrefix(line) {
		return handleReplCommand(line, pendingApprovalID)
	}
	return replDispatchResult{}, startAgentTurn(line)
}

// handleApprovalShortcut 处理 y/s 快捷审批输入。
func handleApprovalShortcut(line string, pendingApprovalID string) (bool, replDispatchResult, error) {
	if !isApprovalShortcut(line) {
		return false, replDispatchResult{}, nil
	}
	if pendingApprovalID == "" {
		if isAgentRunning() {
			return true, replDispatchResult{lines: []string{"当前没有待审批请求"}}, nil
		}
		return false, replDispatchResult{}, nil
	}
	approved := strings.EqualFold(line, "y")
	msg, err := sendApproval(pendingApprovalID, approved)
	return true, replDispatchResult{lines: []string{msg}}, err
}

// isApprovalShortcut 判断输入是否为 y/s 快捷审批。
func isApprovalShortcut(line string) bool {
	return strings.EqualFold(line, "y") || strings.EqualFold(line, "s")
}

// handleReplCommand 解析 / 开头的命令并生成输出。
func handleReplCommand(line string, pendingApprovalID string) (replDispatchResult, error) {
	cmd := parseReplCommand(line)
	if cmd.name == "" {
		return replDispatchResult{}, nil
	}

	switch cmd.name {
	case "q", "quit", "exit":
		return replDispatchResult{quit: true}, nil
	case "help":
		return replDispatchResult{lines: replHelpLines()}, nil
	case "model":
		lines, err := handleModelCommand(cmd.args)
		return replDispatchResult{lines: lines}, err
	case "shell":
		lines, err := handleShellCommand(cmd.args)
		return replDispatchResult{lines: lines}, err
	case "agent":
		return replDispatchResult{}, handleAgentCommand(cmd.args)
	case "approvals":
		lines, err := handleApprovalsCommand(cmd.args)
		return replDispatchResult{lines: lines}, err
	case "approve":
		lines, err := handleApprovalCommand(cmd.args, true)
		return replDispatchResult{lines: lines}, err
	case "reject":
		lines, err := handleApprovalCommand(cmd.args, false)
		return replDispatchResult{lines: lines}, err
	case "y", "s":
		lines, err := handleApprovalShortcutCommand(cmd.name, pendingApprovalID)
		return replDispatchResult{lines: lines}, err
	default:
		return replDispatchResult{}, fmt.Errorf("未知 repl 命令: %s", cmd.name)
	}
}

type replCommand struct {
	name string
	args []string
}

// parseReplCommand 解析 / 开头的 repl 命令。
func parseReplCommand(line string) replCommand {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return replCommand{}
	}
	return replCommand{
		name: normalizeCommandName(fields[0]),
		args: fields[1:],
	}
}

// handleModelCommand 实现 /model 命令：
//   - /model           显示所有可用模型及当前使用的模型；
//   - /model <alias>   切换到指定别名的模型。
func handleModelCommand(args []string) ([]string, error) {
	sess, err := getOrInitReplAgent()
	if err != nil {
		return nil, err
	}

	if len(args) == 0 {
		models := llm.GetModels()
		current := sess.session.Client
		var currentAlias string
		// 尝试从 client 获取 alias，如果 client 结构体中有的话
		// 这里的实现依赖于 client 内部保存了别名，或者我们记录在 session 中
		// 目前 LLMModel 中有 Alias，但 Client 接口没有返回 Alias 的方法。
		// 我们暂且通过匹配 client 来判断，或者直接显示当前 session 的 client 信息。

		lines := []string{"可用模型列表:"}
		for _, m := range models {
			prefix := "  "
			// 简单的启发式判断：如果 client 内存地址一致，或者是同名的
			if m.Client == current {
				prefix = "* "
				currentAlias = m.Alias
			}
			lines = append(lines, fmt.Sprintf("%s%s (%s)", prefix, m.Alias, m.Model))
		}
		if currentAlias != "" {
			lines = append(lines, "", fmt.Sprintf("当前使用: %s", currentAlias))
		}
		return lines, nil
	}

	alias := args[0]
	model, err := llm.FindModel(alias)
	if err != nil {
		return nil, err
	}

	client, err := llm.NewLLMClient(model)
	if err != nil {
		return nil, err
	}

	sess.session.Client = client
	return []string{fmt.Sprintf("已切换到模型: %s (%s)", model.Alias, model.Model)}, nil
}

// handleShellCommand 处理 /shell 命令。
func handleShellCommand(args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("用法: /shell <命令>")
	}
	result, err := executeShellCommand([]string{"--", strings.Join(args, " ")})
	if result == nil {
		return nil, err
	}
	return formatShellResult(result), err
}

// handleAgentCommand 处理 /agent 命令。
func handleAgentCommand(args []string) error {
	rest := strings.TrimSpace(strings.Join(args, " "))
	if rest == "" {
		return fmt.Errorf("用法: /agent <任务描述>")
	}
	return startAgentTurn(rest)
}

// handleApprovalCommand 处理 /approve、/reject 命令。
func handleApprovalCommand(args []string, approved bool) ([]string, error) {
	if len(args) != 1 {
		if approved {
			return nil, fmt.Errorf("用法: /approve <请求ID>")
		}
		return nil, fmt.Errorf("用法: /reject <请求ID>")
	}
	msg, err := sendApproval(args[0], approved)
	if err != nil {
		return nil, err
	}
	return []string{msg}, nil
}

// handleApprovalShortcutCommand 处理 /y、/s 命令。
func handleApprovalShortcutCommand(cmd string, pendingApprovalID string) ([]string, error) {
	if pendingApprovalID == "" {
		return nil, fmt.Errorf("当前没有待审批请求")
	}
	approved := cmd == "y"
	msg, err := sendApproval(pendingApprovalID, approved)
	if err != nil {
		return nil, err
	}
	return []string{msg}, nil
}

const maxSteps = 40

// startAgentTurn 异步启动一次 agent turn。
func startAgentTurn(userInput string) error {
	if !tryStartAgentTurn() {
		return fmt.Errorf("当前有任务在执行，请先处理审批或等待完成")
	}
	sess, err := getOrInitReplAgent()
	if err != nil {
		finishAgentTurn()
		return err
	}
	go func() {
		defer finishAgentTurn()
		if err := runAgentTurn(context.Background(), sess, userInput); err != nil {
			emitAgentTurnError(sess, err)
		}
	}()
	return nil
}

// runAgentTurn 执行一次同步的 agent turn。
func runAgentTurn(ctx context.Context, sess *replAgentSession, userInput string) error {
	if sess == nil || sess.session == nil {
		return fmt.Errorf("agent 会话未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return sess.session.RunTurn(ctx, userInput)
}

// emitAgentTurnError 将 agent 执行失败信息写入事件流。
func emitAgentTurnError(sess *replAgentSession, err error) {
	if sess == nil || sess.session == nil || err == nil {
		return
	}
	sess.session.Sink.SendEvent(server.Event{
		Kind:    server.EventTurnError,
		Time:    time.Now(),
		Message: fmt.Sprintf("agent 执行失败: %v", err),
	})
}

// hasCommandPrefix 判断输入是否以命令前缀开头。
func hasCommandPrefix(line string) bool {
	return strings.HasPrefix(line, "/")
}

// normalizeCommandName 去除命令前缀并返回规范化名称。
func normalizeCommandName(raw string) string {
	return strings.TrimPrefix(raw, "/")
}

// replHelpLines 返回 REPL 帮助信息。
func replHelpLines() []string {
	return []string{`可用命令:
  /help                显示帮助
  /q / /quit / /exit   退出 repl
  /model [alias]       查看或切换 LLM 模型
  /shell <cmd>         通过用户默认 shell 执行命令
  /agent <指令>        通过 LLM+工具自动完成一步任务
  /approve <id>        批准指定补丁请求（来自 apply_patch）
  /reject <id>         拒绝指定补丁请求
  /approvals           查看/设置 apply_patch 审批模式

默认行为:
  直接输入不以 / 开头的内容时，等价于 /agent <输入行>。`}
}
