package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/cellbuf"

	"chase-code/server"
)

const (
	replMaxLogLinesDefault = 8000
	replInputHeight        = 3
	replStatusHeight       = 1
	replScrollLines        = 6
	replMouseScrollLines   = 3
	replViewportPad        = 1
	inputBoxPadding        = 1
)

// replModel 负责管理 TUI 的状态与渲染。
type replModel struct {
	input             textinput.Model
	viewport          viewport.Model
	lines             []string
	events            <-chan server.Event
	pendingApprovalID string
	ready             bool
	width             int
	height            int
	maxLogLines       int
	mouseEnabled      bool
}

type replEventMsg struct {
	event server.Event
}

type replEventClosedMsg struct{}

type replDispatchMsg struct {
	result replDispatchResult
	err    error
}

// runReplTUI 启动基于 Bubble Tea 的交互终端。
func runReplTUI(events <-chan server.Event) error {
	if events == nil {
		return fmt.Errorf("事件通道未初始化")
	}
	model := newReplModel(events)
	options := []tea.ProgramOption{tea.WithAltScreen()}
	if model.mouseEnabled {
		options = append(options, tea.WithMouseAllMotion())
	}
	program := tea.NewProgram(model, options...)
	_, err := program.Run()
	return err
}

// newReplModel 构造 TUI 模型并写入启动提示。
func newReplModel(events <-chan server.Event) replModel {
	input := textinput.New()
	input.Prompt = "chase> "
	input.PromptStyle = stylePrompt
	input.TextStyle = styleInput
	input.CursorStyle = styleInput
	input.Focus()

	vp := viewport.New(0, 0)
	vp.Style = lipgloss.NewStyle().PaddingLeft(replViewportPad)

	model := replModel{
		input:        input,
		viewport:     vp,
		events:       events,
		maxLogLines:  resolveMaxLogLines(),
		mouseEnabled: resolveMouseEnabled(),
	}
	model.appendLines(replBannerLines()...)
	return model
}

// Init 启动事件监听与光标闪烁。
func (m replModel) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, listenForReplEvent(m.events))
}

// Update 处理输入、事件与窗口尺寸变化。
func (m replModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyCtrlD:
			return m, tea.Quit
		case tea.KeyPgUp:
			m.scrollByLines(-replScrollLines)
			return m, nil
		case tea.KeyPgDown:
			m.scrollByLines(replScrollLines)
			return m, nil
		case tea.KeyHome:
			m.viewport.GotoTop()
			return m, nil
		case tea.KeyEnd:
			m.viewport.GotoBottom()
			return m, nil
		case tea.KeyEnter:
			line := strings.TrimSpace(m.input.Value())
			m.input.SetValue("")
			if line == "" {
				return m, nil
			}
			m.appendLines(styleUser.Render("> " + line))
			return m, replDispatchCmd(line, m.pendingApprovalID)
		}
	case tea.MouseMsg:
		if m.mouseEnabled {
			switch msg.Type {
			case tea.MouseWheelUp:
				m.scrollByLines(-replMouseScrollLines)
				return m, nil
			case tea.MouseWheelDown:
				m.scrollByLines(replMouseScrollLines)
				return m, nil
			}
		}
	case replEventMsg:
		m.applyEvent(msg.event)
		return m, listenForReplEvent(m.events)
	case replEventClosedMsg:
		m.appendLines(styleDim.Render("[event] 通道已关闭"))
		return m, nil
	case replDispatchMsg:
		if msg.err != nil {
			m.appendLines(styleError.Render("错误: " + msg.err.Error()))
		}
		if len(msg.result.lines) > 0 {
			m.appendLines(msg.result.lines...)
		}
		if msg.result.quit {
			return m, tea.Quit
		}
		return m, nil
	}

	var cmd tea.Cmd
	if msg, ok := msg.(tea.KeyMsg); ok && msg.Alt {
		if msg.Type == tea.KeyUp {
			m.scrollByLines(-1)
			return m, nil
		}
		if msg.Type == tea.KeyDown {
			m.scrollByLines(1)
			return m, nil
		}
	}
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// View 渲染 TUI 界面。
func (m replModel) View() string {
	if !m.ready {
		return "loading..."
	}
	return lipgloss.JoinVertical(
		lipgloss.Left,
		m.viewport.View(),
		m.inputBoxView(),
		m.statusLine(),
	)
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

// applyEvent 将事件写入日志并更新审批状态。
func (m *replModel) applyEvent(ev server.Event) {
	if ev.Kind == server.EventPatchApprovalRequest {
		m.pendingApprovalID = ev.RequestID
	}
	if ev.Kind == server.EventPatchApprovalResult && ev.RequestID == m.pendingApprovalID {
		m.pendingApprovalID = ""
	}
	if lines := formatEvent(ev); len(lines) > 0 {
		m.appendLines(lines...)
	}
}

// appendLines 追加日志并控制滚动位置。
func (m *replModel) appendLines(lines ...string) {
	if len(lines) == 0 {
		return
	}
	follow := m.viewport.AtBottom()
	m.lines = append(m.lines, sanitizeLines(lines)...)
	if m.maxLogLines <= 0 {
		m.maxLogLines = replMaxLogLinesDefault
	}
	if len(m.lines) > m.maxLogLines {
		m.lines = m.lines[len(m.lines)-m.maxLogLines:]
	}
	m.viewport.SetContent(renderWrappedContent(m.lines, m.viewport.Width))
	if follow {
		m.viewport.GotoBottom()
	}
}

// resize 调整视口尺寸。
func (m *replModel) resize(width, height int) {
	m.ready = true
	m.width = width
	m.height = height
	m.viewport.Width = width
	contentWidth := inputBoxContentWidth(width)
	promptWidth := lipgloss.Width(m.input.Prompt)
	inputWidth := contentWidth - promptWidth - 1
	if inputWidth < 0 {
		inputWidth = 0
	}
	m.input.Width = inputWidth
	viewportHeight := height - replInputHeight - replStatusHeight
	if viewportHeight < 1 {
		viewportHeight = 1
	}
	m.viewport.Height = viewportHeight
	m.viewport.SetContent(renderWrappedContent(m.lines, m.viewport.Width))
	m.viewport.GotoBottom()
}

// statusLine 渲染底部状态栏。
func (m replModel) statusLine() string {
	parts := []string{"Ctrl+C 退出", "Enter 发送", "PgUp/PgDn 滚动"}
	if m.mouseEnabled {
		parts = append(parts, "滚轮滚动")
	}
	if isAgentRunning() {
		parts = append(parts, "agent 处理中")
	}
	if m.pendingApprovalID != "" {
		parts = append(parts, fmt.Sprintf("待审批: %s (y/s)", m.pendingApprovalID))
	}
	return styleStatus.Render(strings.Join(parts, " | "))
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
		"  ____ _                      ____          _      ",
		" / ___| |__   __ _ ___  ___  / ___|___   __| | ___ ",
		"| |   | '_ \\ / _` / __|/ _ \\| |   / _ \\ / _` |/ _ \\",
		"| |___| | | | (_| \\__ \\  __/| |__| (_) | (_| |  __/",
		" \\____|_| |_|\\__,_|___/\\___| \\____\\___/ \\__,_|\\___|",
	}

	lines := make([]string, 0, len(logo)+6)
	for i, line := range logo {
		if i%2 == 0 {
			lines = append(lines, styleBanner.Render(line))
		} else {
			lines = append(lines, styleBannerA.Render(line))
		}
	}
	lines = append(lines, "")
	lines = append(lines, styleDim.Render(fmt.Sprintf("chase-code repl（agent 优先），当前工作目录: %s", cwd)))
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
	content := totalWidth - 2 - inputBoxPadding*2
	if content < 0 {
		return 0
	}
	return content
}

// renderWrappedContent 对日志内容做软换行，避免窗口缩窄后内容溢出。
func renderWrappedContent(lines []string, width int) string {
	if len(lines) == 0 {
		return ""
	}
	contentWidth := width - replViewportPad
	if contentWidth < 1 {
		return strings.Join(lines, "\n")
	}
	wrapped := make([]string, 0, len(lines))
	for _, line := range lines {
		wrapped = appendWrappedLines(wrapped, line, contentWidth)
	}
	return strings.Join(wrapped, "\n")
}

// scrollByLines 以指定行数滚动视口。
func (m *replModel) scrollByLines(delta int) {
	if delta == 0 {
		return
	}
	if delta < 0 {
		m.viewport.LineUp(-delta)
		return
	}
	m.viewport.LineDown(delta)
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

// sanitizeLine 移除 \r 等控制字符，避免破坏 TUI 布局。
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

// appendWrappedLines 对单行内容做软换行，并保留空行。
func appendWrappedLines(dst []string, line string, width int) []string {
	if line == "" {
		return append(dst, "")
	}
	return append(dst, splitLines(cellbuf.Wrap(line, width, ""))...)
}

// resolveMaxLogLines 读取 TUI 最大日志行数配置。
func resolveMaxLogLines() int {
	raw := strings.TrimSpace(os.Getenv("CHASE_CODE_TUI_MAX_LINES"))
	if raw == "" {
		return replMaxLogLinesDefault
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return replMaxLogLinesDefault
	}
	return value
}

// resolveMouseEnabled 读取鼠标事件开关，避免影响终端文本选择。
func resolveMouseEnabled() bool {
	raw := strings.TrimSpace(os.Getenv("CHASE_CODE_TUI_MOUSE"))
	if raw == "" {
		return false
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}
