package cli

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"chase-code/server"
)

const (
	inputBoxPadding = 1
)

// replModel 负责管理 TUI 的状态与渲染。
type replModel struct {
	input              textinput.Model
	events             <-chan server.Event
	pendingApprovalID  string
	exiting            bool
	width              int
	height             int
	streamActive       bool
	initialInput       string
	autoExitOnTurnDone bool

	// 补全列表相关
	showSuggestions bool
	suggestions     []CLICommand
	suggestionIdx   int

	// 流式 Markdown 渲染状态
	streamBuffer             string // 原始流式 Markdown 累积内容
	streamCommittedLineCount int    // 已提交的渲染行数
	streamWrapWidth          int
}

type replEventMsg struct {
	event server.Event
}

type replEventClosedMsg struct{}

type replAutoRunMsg struct {
	input string
}

type replDispatchMsg struct {
	result replDispatchResult
	err    error
}

// runReplTUI 启动基于 Bubble Tea 的交互终端（仅保留输入框渲染）。
func runReplTUI(events <-chan server.Event, initialInput string) error {
	if events == nil {
		return fmt.Errorf("事件通道未初始化")
	}
	model := newReplModel(events, initialInput)
	program := tea.NewProgram(model)
	_, err := program.Run()
	return err
}

// newReplModel 构造 TUI 模型。
func newReplModel(events <-chan server.Event, initialInput string) replModel {
	input := textinput.New()
	input.Prompt = "chase> "
	input.PromptStyle = stylePrompt
	input.TextStyle = styleInput
	input.CursorStyle = styleInput
	input.Focus()

	return replModel{
		input:              input,
		events:             events,
		initialInput:       initialInput,
		autoExitOnTurnDone: strings.TrimSpace(os.Getenv("CHASE_TUI_EXIT_ON_DONE")) != "",
	}
}

// Init 启动事件监听、光标闪烁，并输出启动提示。
func (m replModel) Init() tea.Cmd {
	cmds := []tea.Cmd{
		textinput.Blink,
		listenForReplEvent(m.events),
		printReplLinesCmd(replBannerLines()),
	}
	if m.initialInput != "" {
		cmds = append(cmds, func() tea.Msg {
			return replAutoRunMsg{input: m.initialInput}
		})
	}
	return tea.Batch(cmds...)
}

// Update 处理输入、事件与窗口尺寸变化。
func (m replModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyMsg:
		if m.showSuggestions {
			switch msg.Type {
			case tea.KeyUp:
				m.suggestionIdx--
				if m.suggestionIdx < 0 {
					m.suggestionIdx = len(m.suggestions) - 1
				}
				return m, nil
			case tea.KeyDown, tea.KeyTab:
				m.suggestionIdx++
				if m.suggestionIdx >= len(m.suggestions) {
					m.suggestionIdx = 0
				}
				return m, nil
			case tea.KeyEnter:
				if len(m.suggestions) > 0 {
					cmd := m.suggestions[m.suggestionIdx]
					m.input.SetValue("/" + cmd.Name() + " ")
					m.input.CursorEnd()
					m.showSuggestions = false
					return m, nil
				}
			case tea.KeyEsc:
				m.showSuggestions = false
				return m, nil
			}
		}

		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyCtrlD:
			m.exiting = true
			return m, tea.Quit
		case tea.KeyEnter:
			return m.handleEnter()
		}
	case replEventMsg:
		lines := m.applyEvent(msg.event)
		cmd := printReplLinesCmd(lines)
		if m.autoExitOnTurnDone && shouldExitAfterEvent(msg.event) {
			m.exiting = true
			if cmd == nil {
				return m, tea.Quit
			}
			return m, tea.Sequence(cmd, tea.Quit)
		}
		return m, tea.Batch(
			listenForReplEvent(m.events),
			cmd,
		)
	case replEventClosedMsg:
		return m, printReplLinesCmd([]string{styleDim.Render("[event] 通道已关闭")})
	case replAutoRunMsg:
		echo := []string{styleUser.Render("> " + msg.input)}
		return m, tea.Batch(
			printReplLinesCmd(echo),
			replDispatchCmd(msg.input, m.pendingApprovalID),
		)
	case replDispatchMsg:
		return m.handleDispatch(msg)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.updateSuggestions()
	return m, cmd
}

func (m replModel) resolveStreamWrapWidth() int {
	// 使用首次进入流式时的宽度，避免窗口变化导致渲染不稳定（会破坏增量前缀判断）。
	if m.streamWrapWidth > 0 {
		return m.streamWrapWidth
	}
	// 经验值：按终端宽度换行；没有窗口尺寸时回退 80。
	if m.width > 0 {
		w := m.width
		if w < 20 {
			w = 20
		}
		return w
	}
	return 80
}

// updateSuggestions 根据当前输入更新补全列表。
func (m *replModel) updateSuggestions() {
	val := m.input.Value()
	if !strings.HasPrefix(val, "/") || strings.Contains(val, " ") {
		m.showSuggestions = false
		m.suggestions = nil
		m.suggestionIdx = 0
		return
	}

	// 记录当前选中的命令名，以便在列表更新后尝试恢复选中状态
	var currentSelectedName string
	if m.showSuggestions && m.suggestionIdx < len(m.suggestions) {
		currentSelectedName = m.suggestions[m.suggestionIdx].Name()
	}

	prefix := strings.TrimPrefix(val, "/")
	var matches []CLICommand
	newSelectedIdx := -1

	for _, cmd := range ListCommands() {
		matched := false
		if strings.HasPrefix(cmd.Name(), prefix) {
			matched = true
		} else {
			for _, alias := range cmd.Aliases() {
				if strings.HasPrefix(alias, prefix) {
					matched = true
					break
				}
			}
		}

		if matched {
			if cmd.Name() == currentSelectedName {
				newSelectedIdx = len(matches)
			}
			matches = append(matches, cmd)
		}
	}

	if len(matches) > 0 {
		m.suggestions = matches
		m.showSuggestions = true
		if newSelectedIdx != -1 {
			m.suggestionIdx = newSelectedIdx
		} else if m.suggestionIdx >= len(matches) {
			m.suggestionIdx = 0
		}
	} else {
		m.showSuggestions = false
		m.suggestions = nil
		m.suggestionIdx = 0
	}
}

// View 渲染输入框组件。
func (m replModel) View() string {
	if m.exiting {
		return ""
	}

	inputView := m.inputBoxView()
	if !m.showSuggestions || len(m.suggestions) == 0 {
		return inputView
	}

	// 补全列表放在输入框上方，这样输入框在终端底部的物理位置最稳定
	return lipgloss.JoinVertical(lipgloss.Left, m.suggestionsView(), inputView)
}

// suggestionsView 渲染补全列表。
func (m replModel) suggestionsView() string {
	if len(m.suggestions) == 0 {
		return ""
	}

	var lines []string
	for i, cmd := range m.suggestions {
		name := "/" + cmd.Name()
		desc := cmd.Description()
		line := fmt.Sprintf(" %-15s  %s ", name, desc)
		if i == m.suggestionIdx {
			line = styleSelected.Render(line)
		} else {
			line = styleDim.Render(line)
		}
		lines = append(lines, line)
	}

	// 设置与输入框一致的宽度，并去掉底部边距，让它与输入框无缝衔接
	return lipgloss.NewStyle().
		Border(asciiBorder).
		BorderForeground(lipgloss.Color("8")).
		Padding(0, 1).
		Width(m.width).
		Render(strings.Join(lines, "\n"))
}

// handleEnter 处理回车输入并触发异步分发。
func (m replModel) handleEnter() (tea.Model, tea.Cmd) {
	line := strings.TrimSpace(m.input.Value())
	m.input.SetValue("")
	if line == "" {
		return m, nil
	}
	echo := []string{styleUser.Render("> " + line)}
	return m, tea.Batch(
		printReplLinesCmd(echo),
		replDispatchCmd(line, m.pendingApprovalID),
	)
}

// handleDispatch 处理命令执行结果并决定是否退出。
func (m replModel) handleDispatch(msg replDispatchMsg) (tea.Model, tea.Cmd) {
	lines := make([]string, 0, len(msg.result.lines)+1)
	if msg.err != nil {
		lines = append(lines, styleError.Render("错误: "+msg.err.Error()))
	}
	if len(msg.result.lines) > 0 {
		lines = append(lines, msg.result.lines...)
	}
	cmd := printReplLinesCmd(lines)
	if msg.result.quit {
		m.exiting = true
		if cmd == nil {
			return m, tea.Quit
		}
		return m, tea.Sequence(cmd, tea.Quit)
	}
	return m, cmd
}

// listenForReplEvent 等待下一条事件。
func listenForReplEvent(ch <-chan server.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return replEventClosedMsg{}
		}
		return replEventMsg{event: ev}
	}
}

// replDispatchCmd 在后台处理用户输入，避免阻塞 UI。
func replDispatchCmd(line string, pendingApprovalID string) tea.Cmd {
	return func() tea.Msg {
		result, err := dispatchReplInput(line, pendingApprovalID)
		return replDispatchMsg{result: result, err: err}
	}
}

// printReplLinesCmd 将输出写入终端滚动区，而不是渲染在 TUI 视口内。
func printReplLinesCmd(lines []string) tea.Cmd {
	clean := sanitizeLines(lines)
	if len(clean) == 0 {
		return nil
	}
	writeTUILog(clean)
	text := strings.Join(clean, "\n")

	// todo text 尾部可能会缺失换行符 导致粘连
	return tea.Printf("%s", text)
}

// applyEvent 将事件写入终端输出并更新审批状态。
func (m *replModel) applyEvent(ev server.Event) []string {
	if ev.Kind == server.EventPatchApprovalRequest {
		m.pendingApprovalID = ev.RequestID
	}
	if ev.Kind == server.EventPatchApprovalResult && ev.RequestID == m.pendingApprovalID {
		m.pendingApprovalID = ""
	}

	switch ev.Kind {
	case server.EventAgentTextDelta:
		return m.appendStreamDelta(ev.Message)
	case server.EventAgentTextDone:
		// 如果在流式模式下，忽略 Done 消息中的文本（通常是全量），只 Flush 缓冲区
		if m.streamActive {
			lines := m.flushStreamFinal("")
			m.resetStreamState()
			return lines
		}
		// 非流式模式（或者流式未激活），则使用消息中的文本
		lines := m.flushStreamFinal(ev.Message)
		m.resetStreamState()
		return lines
	case server.EventToolPlanned, server.EventTurnError, server.EventTurnFinished:
		m.resetStreamState()
	}

	return formatEvent(ev)
}

// shouldExitAfterEvent 判断自动退出模式下是否应结束程序。
func shouldExitAfterEvent(ev server.Event) bool {
	switch ev.Kind {
	case server.EventTurnFinished, server.EventTurnError:
		return true
	default:
		return false
	}
}

// appendStreamDelta 追加流式增量并输出已完成的完整行。
func (m *replModel) appendStreamDelta(delta string) []string {
	if delta == "" {
		return nil
	}
	m.streamActive = true
	m.streamBuffer += delta
	if m.streamWrapWidth <= 0 {
		m.streamWrapWidth = m.resolveStreamWrapWidth()
	}

	if !strings.Contains(delta, "\n") {
		return nil
	}

	return m.streamCommitCompleteLines()
}

// streamCommitCompleteLines 渲染到最后一个换行符并提交新增完整行。
// 该策略参考 codex-rs 的流式 Markdown 处理方式，避免输出未稳定的行。
func (m *replModel) streamCommitCompleteLines() []string {
	lastNewline := strings.LastIndex(m.streamBuffer, "\n")
	if lastNewline == -1 {
		return nil
	}

	source := m.streamBuffer[:lastNewline+1]
	rendered := renderMarkdownToANSI(source, m.streamWrapWidth)
	lines := splitLines(rendered)
	if len(lines) == 0 {
		return nil
	}

	completeLineCount := len(lines)
	if completeLineCount > 0 && isVisualEmptyLine(lines[completeLineCount-1]) {
		completeLineCount--
	}

	if m.streamCommittedLineCount >= completeLineCount {
		return nil
	}

	out := lines[m.streamCommittedLineCount:completeLineCount]
	m.streamCommittedLineCount = completeLineCount
	return out
}

// flushStreamFinal 在流式结束时渲染并输出剩余部分。
func (m *replModel) flushStreamFinal(final string) []string {
	if final != "" {
		m.streamBuffer += final
	}

	if m.streamWrapWidth <= 0 {
		m.streamWrapWidth = m.resolveStreamWrapWidth()
	}

	// 此时无论是否有闭合 fence，都强制渲染
	if strings.TrimSpace(m.streamBuffer) == "" {
		return nil
	}

	source := m.streamBuffer
	if !strings.HasSuffix(source, "\n") {
		source += "\n"
	}

	rendered := renderMarkdownToANSI(source, m.streamWrapWidth)
	lines := splitLines(rendered)
	if len(lines) == 0 || m.streamCommittedLineCount >= len(lines) {
		return nil
	}

	return lines[m.streamCommittedLineCount:]
}

// resetStreamState 清理流式输出状态，避免跨事件残留。
func (m *replModel) resetStreamState() {
	m.streamBuffer = ""
	m.streamActive = false
	m.streamCommittedLineCount = 0
	m.streamWrapWidth = 0
}

// resize 根据窗口尺寸调整输入框宽度。
func (m *replModel) resize(width, height int) {
	m.width = width
	m.height = height
	contentWidth := inputBoxContentWidth(width)
	promptWidth := lipgloss.Width(m.input.Prompt)
	inputWidth := contentWidth - promptWidth - 1
	if inputWidth < 0 {
		inputWidth = 0
	}
	m.input.Width = inputWidth
}

// inputBoxView 将输入框包裹成带边框的视图。
func (m replModel) inputBoxView() string {
	view := m.input.View()
	if m.width <= 0 {
		return view
	}
	style := styleInputBox
	if m.input.Focused() {
		style = styleInputOn
	}
	return style.Width(m.width).Render(view)
}

// replBannerLines 返回启动提示。
func replBannerLines() []string {
	cwd, _ := os.Getwd()
	logo := []string{
		" ██████╗██╗  ██╗ █████╗ ███████╗███████╗     ██████╗ ██████╗ ██████╗ ███████╗",
		"██╔════╝██║  ██║██╔══██╗██╔════╝██╔════╝    ██╔════╝██╔═══██╗██╔══██╗██╔════╝",
		"██║     ███████║███████║███████╗█████╗      ██║     ██║   ██║██║  ██║█████╗  ",
		"██║     ██╔══██║██╔══██║╚════██║██╔══╝      ██║     ██║   ██║██║  ██║██╔══╝  ",
		"╚██████╗██║  ██║██║  ██║███████║███████╗    ╚██████╗╚██████╔╝██████╔╝███████╗",
		" ╚═════╝╚═╝  ╚═╝╚═╝  ╚═╝╚══════╝╚══════╝     ╚═════╝ ╚═════╝ ╚═════╝ ╚══════╝",
	}

	lines := make([]string, 0, len(logo)+6)
	for _, line := range logo {
		lines = append(lines, styleBanner.Render(line))
	}
	lines = append(lines, styleDim.Render(fmt.Sprintf("当前工作目录: %s", cwd)))
	lines = append(lines, replGuideBox())
	lines = append(lines, styleDim.Render("输入 /help 查看可用命令，/q 退出。"))
	return lines
}

// replGuideBox 返回启动提示信息的盒子渲染。
func replGuideBox() string {
	tips := []string{
		styleGuideHead.Render("Quick start"),
		"1) 直接输入问题或指令，agent 会自动调用工具。",
		"2) 使用 @path 引用文件，/help 查看命令列表。",
		"3) /q 退出，y/s 或 /approve / /reject 处理审批。",
	}
	return styleGuideBox.Render(strings.Join(tips, "\n"))
}

// inputBoxContentWidth 计算输入框可用于文本的实际宽度。
func inputBoxContentWidth(totalWidth int) int {
	content := totalWidth - inputBoxPadding*2
	if content < 0 {
		return 0
	}
	return content
}

// sanitizeLines 清理控制字符并拆分潜在的多行输入。
func sanitizeLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		var parts []string
		// 保留显式的空行（防止被 splitLines 吞掉）
		if line == "" {
			parts = []string{""}
		} else {
			parts = splitLines(line)
			// 如果 line 非空但 parts 为空（例如仅包含换行符），也视为空行保留
			if len(parts) == 0 {
				parts = []string{""}
			}
		}

		for _, part := range parts {
			s := sanitizeLine(part)

			// 检查是否为视觉空行（去除 ANSI 码后为空）
			isVisualEmpty := isVisualEmptyLine(s)

			// 压缩连续空行
			if isVisualEmpty {
				// 强制将其视为空字符串处理，避免残留仅含 ANSI 码的行占据高度
				s = ""
				if len(out) > 0 && out[len(out)-1] == "" {
					continue
				}
			}
			out = append(out, s)
		}
	}
	return out
}

// ansiRegex 匹配常见的 ANSI 转义序列
// 包括 CSI (如 \x1b[0m, \x1b[?25h) 和 G0/G1 charset (如 \x1b(B)
var ansiRegex = regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]|\x1b\\([0-9A-Za-z]")

// stripANSI 移除字符串中的 ANSI 转义序列。
func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

// isVisualEmptyLine 判断一行在去除 ANSI 转义后是否为空白。
func isVisualEmptyLine(line string) bool {
	stripped := stripANSI(line)
	return strings.TrimSpace(stripped) == ""
}

// sanitizeLine 移除 \r 等控制字符，避免破坏终端输出。
// 注意：这里必须保留 ANSI 转义序列（ESC, 0x1b），否则颜色会退化为纯文本（例如显示成 "1;96m" / "0m"）。
func sanitizeLine(line string) string {
	line = strings.ReplaceAll(line, "\r", "")
	line = strings.ReplaceAll(line, "\t", "    ")
	return strings.Map(func(r rune) rune {
		// 保留 ANSI ESC 序列
		if r == '\x1b' {
			return r
		}
		// 过滤其他不可见控制字符
		if r < 32 {
			return -1
		}
		return r
	}, line)
}

var (
	tuiLogOnce sync.Once
	tuiLogPath string
	tuiLogMu   sync.Mutex
)

// writeTUILog 将 TUI 输出写入指定日志文件，便于离线排查渲染效果。
func writeTUILog(lines []string) {
	path := getTUILogPath()
	if path == "" || len(lines) == 0 {
		return
	}
	plain := strings.TrimSpace(os.Getenv("CHASE_TUI_LOG_PLAIN")) != ""
	payload := formatTUILogPayload(lines, plain)
	if payload == "" {
		return
	}

	tuiLogMu.Lock()
	defer tuiLogMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(payload)
}

// getTUILogPath 读取日志路径并缓存，避免频繁读取环境变量。
func getTUILogPath() string {
	tuiLogOnce.Do(func() {
		tuiLogPath = strings.TrimSpace(os.Getenv("CHASE_TUI_LOG_PATH"))
	})
	return tuiLogPath
}

// formatTUILogPayload 格式化日志内容，必要时移除 ANSI 码。
func formatTUILogPayload(lines []string, plain bool) string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if plain {
			out = append(out, stripANSI(line))
			continue
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return ""
	}
	ts := time.Now().Format(time.RFC3339Nano)
	return fmt.Sprintf("[%s]\n%s\n\n", ts, strings.Join(out, "\n"))
}
