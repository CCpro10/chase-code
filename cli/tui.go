package cli

import (
	"fmt"
	"os"
	"strings"

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
	input               textinput.Model
	events              <-chan server.Event
	pendingApprovalID   string
	exiting             bool
	width               int
	height              int
	streamBuffer        string
	streamActive        bool
	streamHeaderPrinted bool
	initialInput        string

	// 补全列表相关
	showSuggestions bool
	suggestions     []CLICommand
	suggestionIdx   int
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
		input:        input,
		events:       events,
		initialInput: initialInput,
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
		return m, tea.Batch(
			listenForReplEvent(m.events),
			printReplLinesCmd(lines),
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
	text := strings.Join(clean, "\n")
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
		if m.streamActive {
			lines := m.flushStreamBuffer()
			m.resetStreamState()
			return lines
		}
		return formatEvent(ev)
	case server.EventToolPlanned, server.EventTurnError, server.EventTurnFinished:
		m.resetStreamState()
	}

	return formatEvent(ev)
}

// appendStreamDelta 追加流式增量并尽量输出完整行。
func (m *replModel) appendStreamDelta(delta string) []string {
	if delta == "" {
		return nil
	}
	m.streamActive = true
	m.streamBuffer += delta
	return m.consumeStreamLines()
}

// consumeStreamLines 从缓冲区中提取完整行并保留尾部半行。
func (m *replModel) consumeStreamLines() []string {
	lastBreak := strings.LastIndex(m.streamBuffer, "\n")
	if lastBreak == -1 {
		return nil
	}
	chunk := m.streamBuffer[:lastBreak]
	m.streamBuffer = m.streamBuffer[lastBreak+1:]
	return m.decorateStreamLines(splitLines(chunk))
}

// flushStreamBuffer 在结束时输出缓冲区剩余内容。
func (m *replModel) flushStreamBuffer() []string {
	if !m.streamActive {
		return nil
	}
	lines := m.decorateStreamLines(splitLines(m.streamBuffer))
	m.streamBuffer = ""
	return lines
}

// decorateStreamLines 为流式输出添加头部提示。
func (m *replModel) decorateStreamLines(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	if !m.streamHeaderPrinted {
		m.streamHeaderPrinted = true
		header := styleCyan.Render("[agent] 正在生成：")
		lines = append([]string{header}, lines...)
	}
	return lines
}

// resetStreamState 清理流式输出状态，避免跨事件残留。
func (m *replModel) resetStreamState() {
	m.streamBuffer = ""
	m.streamActive = false
	m.streamHeaderPrinted = false
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
		for _, part := range splitLines(line) {
			out = append(out, sanitizeLine(part))
		}
	}
	return out
}

// sanitizeLine 移除 \r 等控制字符，避免破坏终端输出。
func sanitizeLine(line string) string {
	line = strings.ReplaceAll(line, "\r", "")
	line = strings.ReplaceAll(line, "\t", "    ")
	return strings.Map(func(r rune) rune {
		if r == '\x1b' {
			return r
		}
		if r < 32 {
			return -1
		}
		return r
	}, line)
}
