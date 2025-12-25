package cli

import (
	"fmt"
	"strings"

	"chase-code/server"
)

const (
	toolOutputPreviewLines = 4
)

// formatEvent 将事件转为可渲染的多行文本。
func formatEvent(ev server.Event) []string {
	switch ev.Kind {
	case server.EventTurnStarted:
		return formatTurnStarted()
	case server.EventTurnFinished:
		return formatTurnFinished(ev.Step, ev.Message)
	case server.EventTurnError:
		return formatTurnError(ev.Message)
	case server.EventAgentThinking:
		return formatAgentThinking(ev.Step)
	case server.EventToolPlanned:
		return formatToolPlanned(ev.Step, ev.Message)
	case server.EventToolStarted:
		return formatToolStarted(ev.ToolName)
	case server.EventToolOutputDelta:
		return formatToolOutput(ev.ToolName, ev.Message)
	case server.EventToolFinished:
		return formatToolFinished(ev.ToolName, ev.Message)
	case server.EventPatchApprovalRequest:
		return formatPatchApprovalRequest(ev)
	case server.EventPatchApprovalResult:
		return formatPatchApprovalResult(ev)
	case server.EventAgentTextDone:
		return formatAgentText(ev.Message)
	default:
		return nil
	}
}

// formatTurnStarted 渲染 turn 开始提示。
func formatTurnStarted() []string {
	return []string{styleMagenta.Render("[turn] 开始")}
}

// formatAgentThinking 渲染思考阶段提示。
func formatAgentThinking(step int) []string {
	return []string{styleDim.Render(fmt.Sprintf("  [agent] 正在思考（step=%d）...", step))}
}

// formatToolPlanned 渲染工具规划事件。
func formatToolPlanned(step int, message string) []string {
	lines := []string{styleDim.Render(fmt.Sprintf("  [agent] 规划工具调用（step=%d）：", step))}
	if strings.TrimSpace(message) == "" {
		return lines
	}
	return append(lines, indentLines(message, 4)...)
}

// formatToolStarted 渲染工具开始执行事件。
func formatToolStarted(toolName string) []string {
	return []string{styleYellow.Render(fmt.Sprintf("    [tool %s] 开始执行", toolName))}
}

// formatToolOutput 渲染工具输出事件。
func formatToolOutput(toolName, message string) []string {
	if strings.TrimSpace(message) == "" {
		return nil
	}
	lines := []string{styleDim.Render(fmt.Sprintf("      [tool %s 输出]", toolName))}
	preview := truncateToolOutputLines(message, toolOutputPreviewLines)
	return append(lines, indentLines(strings.Join(preview, "\n"), 8)...)
}

// formatToolFinished 渲染工具结束事件。
func formatToolFinished(toolName, message string) []string {
	if message != "" {
		return []string{styleGreen.Render(fmt.Sprintf("    [tool %s 完成] %s", toolName, message))}
	}
	if toolName == "" {
		return nil
	}
	return []string{styleGreen.Render(fmt.Sprintf("    [tool %s 完成]", toolName))}
}

// truncateToolOutputLines 仅保留前几行工具输出，其余用摘要行表示。
func truncateToolOutputLines(message string, maxLines int) []string {
	trimmed := strings.TrimRight(message, "\n")
	lines := splitLines(trimmed)
	if maxLines <= 0 || len(lines) <= maxLines {
		return lines
	}
	remaining := len(lines) - maxLines
	summary := fmt.Sprintf("... (+%d line(s))", remaining)
	out := append(lines[:maxLines], summary)
	return out
}

// formatPatchApprovalRequest 渲染补丁审批请求事件。
func formatPatchApprovalRequest(ev server.Event) []string {
	lines := []string{styleMagenta.Render(fmt.Sprintf("[apply_patch 审批请求] id=%s", ev.RequestID))}
	if len(ev.Paths) > 0 {
		lines = append(lines, "  涉及文件:")
		for _, p := range ev.Paths {
			lines = append(lines, fmt.Sprintf("    - %s", p))
		}
	}
	if strings.TrimSpace(ev.Message) != "" {
		lines = append(lines, fmt.Sprintf("  原因: %s", ev.Message))
	}
	lines = append(lines, styleDim.Render(fmt.Sprintf("  直接输入 y 批准，s 跳过；或使用 /approve %s / /reject %s。", ev.RequestID, ev.RequestID)))
	return lines
}

// formatPatchApprovalResult 渲染补丁审批结果事件。
func formatPatchApprovalResult(ev server.Event) []string {
	if strings.TrimSpace(ev.RequestID) == "" && strings.TrimSpace(ev.Message) == "" {
		return nil
	}
	return []string{styleDim.Render(fmt.Sprintf("[apply_patch] %s id=%s", ev.Message, ev.RequestID))}
}

// formatAgentText 渲染最终回答内容。
func formatAgentText(message string) []string {
	lines := []string{styleCyan.Render("[agent] 最终回答：")}
	if strings.TrimSpace(message) == "" {
		return lines
	}
	return append(lines, splitLines(message)...)
}

// formatTurnFinished 渲染 turn 结束提示。
func formatTurnFinished(step int, message string) []string {
	if message != "" {
		return []string{styleMagenta.Render(fmt.Sprintf("[turn] 结束（step=%d）：%s", step, message))}
	}
	return []string{styleMagenta.Render(fmt.Sprintf("[turn] 结束（step=%d）", step))}
}

// formatTurnError 渲染 turn 错误提示。
func formatTurnError(message string) []string {
	if strings.TrimSpace(message) == "" {
		return nil
	}
	return []string{styleError.Render(message)}
}

// indentLines 将文本按行缩进。
func indentLines(s string, spaces int) []string {
	pad := strings.Repeat(" ", spaces)
	lines := splitLines(s)
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines[i] = pad + line
	}
	return lines
}

// splitLines 安全拆分文本行，移除末尾多余换行但保留中间的空行。
func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
