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
		switch ev.Kind {
		case server.EventTurnStarted:
			fmt.Fprintf(os.Stderr, "%s[turn]%s 开始\n", colorMagenta, colorReset)

		case server.EventAgentThinking:
			fmt.Fprintf(os.Stderr, "%s  [agent] 正在思考（step=%d）...%s\n", colorDim, ev.Step, colorReset)

		case server.EventToolPlanned:
			// 显示原始 JSON，做轻微缩进
			fmt.Fprintf(os.Stderr, "%s  [agent] 规划工具调用（step=%d）：%s\n", colorDim, ev.Step, colorReset)
			if strings.TrimSpace(ev.Message) != "" {
				fmt.Fprintf(os.Stderr, "%s%s%s\n", colorDim, indent(ev.Message, 4), colorReset)
			}

		case server.EventToolStarted:
			fmt.Fprintf(os.Stderr, "%s    [tool %s] 开始执行%s\n", colorYellow, ev.ToolName, colorReset)

		case server.EventToolOutputDelta:
			if strings.TrimSpace(ev.Message) != "" {
				fmt.Fprintf(os.Stderr, "%s      [tool %s 输出]%s\n", colorGreen, ev.ToolName, colorReset)
				fmt.Println(indent(ev.Message, 8))
			}

		case server.EventToolFinished:
			if ev.Message != "" {
				fmt.Fprintf(os.Stderr, "%s    [tool %s 完成]%s %s\n", colorYellow, ev.ToolName, colorReset, ev.Message)
			} else if ev.ToolName != "" {
				fmt.Fprintf(os.Stderr, "%s    [tool %s 完成]%s\n", colorYellow, ev.ToolName, colorReset)
			}

		case server.EventPatchApprovalRequest:
			// 打印补丁审批请求摘要，用户可以直接输入 y/s 快速审批，或使用 :approve/:reject 命令确认
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

		case server.EventAgentTextDone:
			// 最终回答直接输出到 stdout，前面加一个前缀
			fmt.Fprintf(os.Stderr, "%s[agent]%s 最终回答：\n", colorCyan, colorReset)
			fmt.Println(ev.Message)

		case server.EventTurnFinished:
			if ev.Message != "" {
				fmt.Fprintf(os.Stderr, "%s[turn]%s 结束（step=%d）：%s\n", colorMagenta, colorReset, ev.Step, ev.Message)
			} else {
				fmt.Fprintf(os.Stderr, "%s[turn]%s 结束（step=%d）\n", colorMagenta, colorReset, ev.Step)
			}
		}
	}
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
