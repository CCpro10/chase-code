package tui

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/charmbracelet/glamour"

	"chase-code/server"
)

const (
	toolOutputPreviewLines = 4
)

var (
	mdRendererOnce sync.Once
	mdRenderer     *glamour.TermRenderer
)

func getMarkdownRenderer(wordWrap int) *glamour.TermRenderer {
	// glamour 的渲染器会捕获 wordWrap 等参数；这里用 once 初始化一个默认渲染器。
	// 对于需要不同宽度的场景，可以直接 new 一个 renderer（当前调用频率很低）。
	mdRendererOnce.Do(func() {
		r, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle("dark"),
			glamour.WithWordWrap(wordWrap),
		)
		if err == nil {
			mdRenderer = r
		}
	})
	return mdRenderer
}

// RenderMarkdownToANSI 将 Markdown 渲染为 ANSI 文本。
// - wordWrap<=0 时让 glamour 使用默认策略（或不强制换行）。
// - 渲染失败时回退为原文。
func RenderMarkdownToANSI(md string, wordWrap int) string {
	md = normalizeMarkdownForTUI(md)
	md = strings.TrimRight(md, "\n")
	if strings.TrimSpace(md) == "" {
		return md
	}

	// 低频调用：优先复用默认 renderer；如需要宽度控制则创建一个新的。
	var r *glamour.TermRenderer
	if wordWrap > 0 {
		nr, err := glamour.NewTermRenderer(glamour.WithStandardStyle("dark"), glamour.WithWordWrap(wordWrap))
		if err == nil {
			r = nr
		}
	} else {
		r = getMarkdownRenderer(wordWrap)
	}
	if r == nil {
		return md
	}

	out, err := r.Render(md)
	if err != nil {
		return md
	}
	raw := strings.TrimRight(out, "\n")
	debugMarkdownRender(md, raw)
	trimmed := trimRenderedBlankEdges(raw)
	if shouldDebugTUI() && trimmed != raw {
		log.Printf("markdown render: trimmed edge blank lines")
	}
	return trimmed
}

// normalizeMarkdownForTUI 统一流式与全量渲染的 Markdown 预处理，减少非预期空行。
func normalizeMarkdownForTUI(md string) string {
	if strings.TrimSpace(md) == "" {
		return md
	}

	lines := splitLinesPreserveTrailing(md)
	var bulletConverted int
	var blankRemoved int

	lines, bulletConverted = normalizeListBulletChars(lines)
	lines, blankRemoved = compactListBlankLines(lines)
	normalized := strings.Join(lines, "\n")

	if shouldDebugTUI() && normalized != md {
		log.Printf("markdown normalize: bullets=%d blanks_removed=%d", bulletConverted, blankRemoved)
		log.Printf("markdown normalize input:\n---\n%s\n---", md)
		log.Printf("markdown normalize output:\n---\n%s\n---", normalized)
	}

	return normalized
}

// splitLinesPreserveTrailing 将文本按行拆分，并保留末尾空行。
func splitLinesPreserveTrailing(s string) []string {
	if s == "" {
		return []string{""}
	}
	lines := strings.Split(s, "\n")
	return lines
}

// normalizeListBulletChars 将常见的非标准项目符号转换为 Markdown 列表标记。
func normalizeListBulletChars(lines []string) ([]string, int) {
	out := make([]string, 0, len(lines))
	converted := 0
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if isBulletOnlyLine(line) {
			nextIdx := nextNonEmptyIndex(lines, i+1)
			if nextIdx != -1 && !isListLine(lines[nextIdx]) && !isBulletOnlyLine(lines[nextIdx]) {
				merged := mergeBulletWithContent(line, lines[nextIdx])
				out = append(out, merged)
				converted++
				i = nextIdx
				continue
			}
		}

		normalized := normalizeBulletLine(line)
		if normalized != line {
			converted++
		}
		out = append(out, normalized)
	}
	return out, converted
}

// normalizeBulletLine 将单行的 "• xxx" 形式转换为 "- xxx"，保留缩进。
func normalizeBulletLine(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return line
	}

	r, size := utf8.DecodeRuneInString(trimmed)
	if !isBulletRune(r) {
		return line
	}

	rest := strings.TrimLeft(trimmed[size:], " \t")
	if rest == "" {
		return line
	}

	indent := line[:len(line)-len(trimmed)]
	return indent + "- " + rest
}

func isBulletRune(r rune) bool {
	switch r {
	case '•', '·', '∙', '◦', '‣', '▪':
		return true
	default:
		return false
	}
}

// isBulletOnlyLine 判断是否为仅包含项目符号的占位行。
func isBulletOnlyLine(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return false
	}
	r, size := utf8.DecodeRuneInString(trimmed)
	if !isBulletRune(r) {
		return false
	}
	rest := strings.TrimSpace(trimmed[size:])
	return rest == ""
}

// mergeBulletWithContent 将项目符号行与下一行内容合并成标准列表项。
func mergeBulletWithContent(bulletLine string, contentLine string) string {
	indent := bulletLine[:len(bulletLine)-len(strings.TrimLeft(bulletLine, " \t"))]
	content := strings.TrimLeft(contentLine, " \t")
	return indent + "- " + content
}

// compactListBlankLines 压缩列表项之间的空行，避免渲染时产生过大的间距。
func compactListBlankLines(lines []string) ([]string, int) {
	out := make([]string, 0, len(lines))
	removed := 0
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
			continue
		}

		prev := prevNonEmptyIndex(lines, i-1)
		next := nextNonEmptyIndex(lines, i+1)
		if prev >= 0 && next >= 0 && isListLine(lines[prev]) && isListLine(lines[next]) {
			removed++
			continue
		}

		out = append(out, line)
	}
	return out, removed
}

func prevNonEmptyIndex(lines []string, start int) int {
	for i := start; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return i
		}
	}
	return -1
}

func nextNonEmptyIndex(lines []string, start int) int {
	for i := start; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "" {
			return i
		}
	}
	return -1
}

func isListLine(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return false
	}
	if isBulletOnlyLine(line) {
		return true
	}
	if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ ") {
		return true
	}

	dot := strings.IndexByte(trimmed, '.')
	if dot <= 0 || dot+1 >= len(trimmed) {
		return false
	}
	for i := 0; i < dot; i++ {
		if trimmed[i] < '0' || trimmed[i] > '9' {
			return false
		}
	}
	return trimmed[dot+1] == ' ' || trimmed[dot+1] == '\t'
}

func shouldDebugTUI() bool {
	return strings.TrimSpace(os.Getenv("CHASE_TUI_DEBUG")) != ""
}

func debugMarkdownRender(md string, rendered string) {
	if !shouldDebugTUI() {
		return
	}
	lines := splitLines(rendered)
	if len(lines) == 0 {
		return
	}
	blankCount, maxBlankRun := countBlankRuns(lines)
	listGaps := findListBlankGaps(lines)
	if blankCount == 0 && len(listGaps) == 0 {
		return
	}
	log.Printf("markdown render stats: lines=%d blank=%d max_blank_run=%d list_gaps=%d", len(lines), blankCount, maxBlankRun, len(listGaps))
	if len(listGaps) > 0 {
		log.Printf("markdown render list gaps at lines: %v", listGaps)
		logRenderedGapContext(lines, listGaps, 2)
	}
	if maxBlankRun > 1 {
		logRenderedBlankRuns(lines, 2)
	}
	log.Printf("markdown render source:\n---\n%s\n---", md)
}

func trimRenderedBlankEdges(rendered string) string {
	lines := splitLines(rendered)
	if len(lines) == 0 {
		return rendered
	}
	start := 0
	for start < len(lines) && isVisualEmptyLine(stripANSI(lines[start])) {
		start++
	}
	end := len(lines)
	for end > start && isVisualEmptyLine(stripANSI(lines[end-1])) {
		end--
	}
	if start == 0 && end == len(lines) {
		return rendered
	}
	if start >= end {
		return ""
	}
	return strings.Join(lines[start:end], "\n")
}

func countBlankRuns(lines []string) (int, int) {
	blankCount := 0
	maxRun := 0
	run := 0
	for _, line := range lines {
		if isVisualEmptyLine(stripANSI(line)) {
			blankCount++
			run++
			if run > maxRun {
				maxRun = run
			}
			continue
		}
		run = 0
	}
	return blankCount, maxRun
}

func findListBlankGaps(lines []string) []int {
	var gaps []int
	for i := 1; i < len(lines)-1; i++ {
		if !isVisualEmptyLine(stripANSI(lines[i])) {
			continue
		}
		prev := strings.TrimSpace(stripANSI(lines[i-1]))
		next := strings.TrimSpace(stripANSI(lines[i+1]))
		if isListLine(prev) && isListLine(next) {
			gaps = append(gaps, i)
		}
	}
	return gaps
}

func logRenderedGapContext(lines []string, gaps []int, radius int) {
	maxGaps := 5
	for idx, gap := range gaps {
		if idx >= maxGaps {
			log.Printf("markdown render gap context truncated at %d gaps", maxGaps)
			return
		}
		start := gap - radius
		if start < 0 {
			start = 0
		}
		end := gap + radius
		if end >= len(lines) {
			end = len(lines) - 1
		}
		for i := start; i <= end; i++ {
			content := stripANSI(lines[i])
			log.Printf("render[%d]=%q", i, content)
		}
	}
}

func logRenderedBlankRuns(lines []string, maxRuns int) {
	run := 0
	seen := 0
	for i, line := range lines {
		if isVisualEmptyLine(stripANSI(line)) {
			run++
			continue
		}
		if run > 1 {
			seen++
			log.Printf("markdown render blank run: end=%d len=%d", i-1, run)
			if seen >= maxRuns {
				return
			}
		}
		run = 0
	}
	if run > 1 {
		log.Printf("markdown render blank run: end=%d len=%d", len(lines)-1, run)
	}
}

// formatEvent 将事件转为可渲染的多行文本。
func formatEvent(ev server.Event) []string {
	switch ev.Kind {
	case server.EventTurnStarted:
		return formatTurnStarted()
	case server.EventTurnFinished:
		return formatTurnFinished(ev.Step, ev.Message)
	case server.EventTurnError:
		return formatTurnError(ev.Message)
	case server.EventToolOutputDelta:
		return formatToolOutput(ev.ToolName, ev.Message)
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

// formatToolOutput 渲染工具输出事件。
func formatToolOutput(toolName, message string) []string {
	if strings.TrimSpace(message) == "" {
		return nil
	}
	if toolName == "shell_command" || toolName == "shell" {
		return formatShellToolOutput(message)
	}
	lines := []string{styleDim.Render(fmt.Sprintf("      [tool %s 输出]", toolName))}
	body := message
	if !shouldShowFullToolOutput(toolName, message) {
		preview := truncateToolOutputLines(message, toolOutputPreviewLines)
		body = strings.Join(preview, "\n")
	}
	return append(lines, indentLines(body, 8)...)
}

// formatShellToolOutput 渲染 shell_command 的输出，突出命令与参数。
func formatShellToolOutput(message string) []string {
	output, summary := splitToolOutput(message)
	command := parseShellCommand(summary)
	header := "      " + command
	lines := []string{styleYellow.Render(header)}
	body := output
	if !shouldShowFullToolOutput("shell_command", output) {
		preview := truncateToolOutputLines(output, toolOutputPreviewLines)
		body = strings.Join(preview, "\n")
	}
	return append(lines, indentLines(body, 8)...)
}

// splitToolOutput 将工具输出拆分为正文与摘要（summary）。
func splitToolOutput(message string) (string, string) {
	parts := strings.SplitN(message, "\n---\n", 2)
	output := strings.TrimRight(parts[0], "\n")
	if len(parts) == 1 {
		return output, ""
	}
	return output, strings.TrimSpace(parts[1])
}

// parseShellCommand 从 summary 中提取 command=... 信息。
func parseShellCommand(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "(unknown)"
	}
	idx := strings.Index(summary, "command=")
	if idx == -1 {
		return summary
	}
	rest := summary[idx+len("command="):]
	endIdx := strings.Index(rest, " exit_code=")
	if endIdx == -1 {
		endIdx = len(rest)
	}
	raw := strings.TrimSpace(rest[:endIdx])
	unquoted, err := strconv.Unquote(raw)
	if err != nil {
		return raw
	}
	return unquoted
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

// shouldShowFullToolOutput 对 apply_patch 的 diff 输出保留完整内容。
func shouldShowFullToolOutput(toolName, message string) bool {
	if toolName == "apply_patch" {
		return true
	}
	if strings.Contains(message, "*** Begin Patch") || strings.Contains(message, "diff --git") {
		return true
	}
	return false
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
	if strings.TrimSpace(message) == "" {
		return nil
	}
	rendered := RenderMarkdownToANSI(message, 0)
	return splitLines(rendered)
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
