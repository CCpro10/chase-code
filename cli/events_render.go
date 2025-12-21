package cli

import (
	"fmt"
	"os"
	"strings"

	"chase-code/agent"
	"chase-code/server"
)

// renderEvents 负责从事件通道中消费 server.Event，并以带颜色和缩进的形式
// 渲染到终端上，模拟类似 codex-rs 的实时反馈体验。
// approvals 通道用于在收到补丁审批请求时，将用户的审批决策写回给 agent.Session。
func renderEvents(ch <-chan server.Event, approvals chan<- agent.ApprovalDecision) {
	for ev := range ch {
		renderEvent(ev)
	}
}

// renderEvent 渲染单条事件。
func renderEvent(ev server.Event) {
	switch ev.Kind {
	case server.EventTurnStarted:
		renderTurnStarted()
	case server.EventAgentThinking:
		renderAgentThinking(ev.Step)
	case server.EventToolPlanned:
		renderToolPlanned(ev.Step, ev.Message)
	case server.EventToolStarted:
		renderToolStarted(ev.ToolName)
	case server.EventToolOutputDelta:
		renderToolOutput(ev.ToolName, ev.Message)
	case server.EventToolFinished:
		renderToolFinished(ev.ToolName, ev.Message)
	case server.EventPatchApprovalRequest:
		renderPatchApprovalRequest(ev)
	case server.EventAgentTextDone:
		renderAgentText(ev.Message)
	case server.EventTurnFinished:
		renderTurnFinished(ev.Step, ev.Message)
	}
}

// renderTurnStarted 渲染 turn 开始提示。
func renderTurnStarted() {
	fmt.Fprintf(os.Stderr, "%s[turn]%s 开始\n", colorMagenta, colorReset)
}

// renderAgentThinking 渲染思考阶段提示。
func renderAgentThinking(step int) {
	fmt.Fprintf(os.Stderr, "%s  [agent] 正在思考（step=%d）...%s\n", colorDim, step, colorReset)
}

// renderToolPlanned 渲染工具规划事件。
func renderToolPlanned(step int, message string) {
	fmt.Fprintf(os.Stderr, "%s  [agent] 规划工具调用（step=%d）：%s\n", colorDim, step, colorReset)
	if strings.TrimSpace(message) != "" {
		fmt.Fprintf(os.Stderr, "%s%s%s\n", colorDim, indent(message, 4), colorReset)
	}
}

// renderToolStarted 渲染工具开始执行事件。
func renderToolStarted(toolName string) {
	fmt.Fprintf(os.Stderr, "%s    [tool %s] 开始执行%s\n", colorYellow, toolName, colorReset)
}

// renderToolOutput 渲染工具输出事件。
func renderToolOutput(toolName, message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "%s      [tool %s 输出]%s\n", colorGreen, toolName, colorReset)
	fmt.Println(indent(message, 8))
}

// renderToolFinished 渲染工具结束事件。
func renderToolFinished(toolName, message string) {
	if message != "" {
		fmt.Fprintf(os.Stderr, "%s    [tool %s 完成]%s %s\n", colorGreen, toolName, colorReset, message)
		return
	}
	if toolName != "" {
		fmt.Fprintf(os.Stderr, "%s    [tool %s 完成]%s\n", colorGreen, toolName, colorReset)
	}
}

// renderPatchApprovalRequest 渲染补丁审批请求事件。
func renderPatchApprovalRequest(ev server.Event) {
	fmt.Fprintf(os.Stderr, "%s[apply_patch 审批请求]%s id=%s\n", colorMagenta, colorReset, ev.RequestID)
	if len(ev.Paths) > 0 {
		fmt.Fprintln(os.Stderr, "  涉及文件:")
		for _, p := range ev.Paths {
			fmt.Fprintf(os.Stderr, "    - %s\n", p)
		}
	}
	if strings.TrimSpace(ev.Message) != "" {
		fmt.Fprintf(os.Stderr, "  原因: %s\n", ev.Message)
	}
	fmt.Fprintf(os.Stderr, "%s  直接输入 y 批准，s 跳过；或使用 :approve %s / :reject %s。%s\n",
		colorDim, ev.RequestID, ev.RequestID, colorReset)
	setPendingApprovalID(ev.RequestID)
}

// renderAgentText 渲染最终回答内容。
func renderAgentText(message string) {
	fmt.Fprintf(os.Stderr, "%s[agent]%s 最终回答：\n", colorCyan, colorReset)
	fmt.Println(message)
}

// renderTurnFinished 渲染 turn 结束提示。
func renderTurnFinished(step int, message string) {
	if message != "" {
		fmt.Fprintf(os.Stderr, "%s[turn]%s 结束（step=%d）：%s\n", colorMagenta, colorReset, step, message)
		return
	}
	fmt.Fprintf(os.Stderr, "%s[turn]%s 结束（step=%d）\n", colorMagenta, colorReset, step)
}

func indent(s string, spaces int) string {
	pad := strings.Repeat(" ", spaces)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}
