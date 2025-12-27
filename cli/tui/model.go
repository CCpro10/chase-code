package tui

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"chase-code/server"
)

// replModel 负责管理 TUI 的状态与渲染。
type replModel struct {
	input              textinput.Model
	list               list.Model
	events             <-chan server.Event
	dispatcher         Dispatcher
	pendingApprovalID  string
	exiting            bool
	streamActive       bool
	initialInput       string
	autoExitOnTurnDone bool

	// 补全列表相关
	allSuggestions []Suggestion
	showList       bool

	// 流式 Markdown 渲染状态
	streamBuffer             string // 原始流式 Markdown 累积内容
	streamCommittedLineCount int    // 已提交的渲染行数
	streamWrapWidth          int
}

// suggestionItem 实现 list.Item 接口，用于补全列表。
type suggestionItem struct {
	suggestion   Suggestion
	displayTitle string
}

func (i suggestionItem) Title() string       { return i.displayTitle }
func (i suggestionItem) Description() string { return i.suggestion.Description() }
func (i suggestionItem) FilterValue() string { return i.suggestion.Name() }

type replEventMsg struct {
	event server.Event
}

type replEventClosedMsg struct{}

type replAutoRunMsg struct {
	input string
}

type replDispatchMsg struct {
	result DispatchResult
	err    error
}

// Run 启动基于 Bubble Tea 的交互终端（仅保留输入框渲染）。
func Run(events <-chan server.Event, initialInput string, dispatcher Dispatcher, suggestions []Suggestion) error {
	if events == nil {
		return fmt.Errorf("事件通道未初始化")
	}
	model := newReplModel(events, initialInput, dispatcher, suggestions)
	program := tea.NewProgram(model)
	_, err := program.Run()
	return err
}

// newReplModel 构造 TUI 模型。
func newReplModel(events <-chan server.Event, initialInput string, dispatcher Dispatcher, suggestions []Suggestion) replModel {
	// 初始化 Input
	input := textinput.New()
	input.Prompt = ""
	input.TextStyle = styleInput
	input.CursorStyle = styleInput
	input.Focus()

	// 初始化 list 作为补全面板
	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = false
	delegate.SetSpacing(0)
	// 让 list 的选中/非选中风格与旧实现接近
	delegate.Styles.SelectedTitle = styleSelected.Padding(0, 1)
	delegate.Styles.SelectedDesc = styleSelected.Padding(0, 1)
	delegate.Styles.NormalTitle = styleDim.Padding(0, 1)
	delegate.Styles.NormalDesc = styleDim.Padding(0, 1)

	l := list.New(nil, delegate, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowFilter(false)
	l.SetFilteringEnabled(false)
	l.SetShowPagination(false)

	return replModel{
		input:              input,
		list:               l,
		events:             events,
		dispatcher:         dispatcher,
		initialInput:       initialInput,
		autoExitOnTurnDone: strings.TrimSpace(os.Getenv("CHASE_TUI_EXIT_ON_DONE")) != "",
		allSuggestions:     suggestions,
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
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.resize(msg.Width)
		return m, cmd

	case tea.KeyMsg:
		// 补全列表可见时，优先处理 list 导航/选择
		if m.showList {
			switch msg.Type {
			case tea.KeyUp, tea.KeyDown:
				var cmd tea.Cmd
				m.list, cmd = m.list.Update(msg)
				return m, cmd
			case tea.KeyTab, tea.KeyEnter:
				if it, ok := m.list.SelectedItem().(suggestionItem); ok {
					m.input.SetValue("/" + it.suggestion.Name() + " ")
					m.input.CursorEnd()
					m.showList = false
				}
				return m, nil
			case tea.KeyEsc:
				m.showList = false
				return m, nil
			default:
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
			m.replDispatchCmd(msg.input, m.pendingApprovalID),
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
	if m.streamWrapWidth > 0 {
		if m.streamWrapWidth < 20 {
			return 20
		}
		return m.streamWrapWidth
	}
	return 80
}

// View 渲染输入框组件。
func (m replModel) View() string {
	if m.exiting {
		return ""
	}

	inputView := m.input.View()
	if !m.showList {
		return inputView
	}

	listView := m.list.View()

	// 补全列表放在输入框下方
	return lipgloss.JoinVertical(lipgloss.Left, inputView, listView)
}

// updateSuggestions 根据当前输入更新补全列表。
func (m *replModel) updateSuggestions() {
	val := m.input.Value()
	if !strings.HasPrefix(val, "/") || strings.Contains(val, " ") {
		m.showList = false
		return
	}

	// 记录当前选中的命令名，尽量在列表更新后恢复
	var selectedName string
	if it, ok := m.list.SelectedItem().(suggestionItem); ok {
		selectedName = it.suggestion.Name()
	}

	prefix := strings.TrimPrefix(val, "/")
	var matches []Suggestion
	maxLen := 0

	for _, cmd := range m.allSuggestions {
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
			matches = append(matches, cmd)
			if l := len(cmd.Name()); l > maxLen {
				maxLen = l
			}
		}
	}

	if len(matches) == 0 {
		m.showList = false
		return
	}

	items := make([]list.Item, 0, len(matches))
	selectedIdx := -1

	// 确保最小对齐宽度
	padWidth := maxLen
	if padWidth < 12 {
		padWidth = 12
	}

	for i, cmd := range matches {
		if cmd.Name() == selectedName {
			selectedIdx = i
		}
		// 格式化：/cmdName + spaces + description
		display := fmt.Sprintf("/%-*s  %s", padWidth, cmd.Name(), cmd.Description())
		items = append(items, suggestionItem{suggestion: cmd, displayTitle: display})
	}

	m.list.SetItems(items)
	if selectedIdx >= 0 {
		m.list.Select(selectedIdx)
	} else {
		m.list.Select(0)
	}

	// 动态调整列表高度：最多显示 8 条；现在每条只占 1 行
	visible := len(items)
	if visible > 8 {
		visible = 8
	}
	m.list.SetHeight(visible)
	m.showList = true
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
		m.replDispatchCmd(line, m.pendingApprovalID),
	)
}

// handleDispatch 处理命令执行结果并决定是否退出。
func (m replModel) handleDispatch(msg replDispatchMsg) (tea.Model, tea.Cmd) {
	lines := make([]string, 0, len(msg.result.Lines)+1)
	if msg.err != nil {
		lines = append(lines, styleError.Render("错误: "+msg.err.Error()))
	}
	if len(msg.result.Lines) > 0 {
		lines = append(lines, msg.result.Lines...)
	}
	cmd := printReplLinesCmd(lines)
	if msg.result.Quit {
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
func (m replModel) replDispatchCmd(line string, pendingApprovalID string) tea.Cmd {
	return func() tea.Msg {
		if m.dispatcher == nil {
			return replDispatchMsg{err: fmt.Errorf("dispatcher not initialized")}
		}
		result, err := m.dispatcher(line, pendingApprovalID)
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
			lines := m.flushStreamFinal("")
			m.resetStreamState()
			return lines
		}
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
func (m *replModel) streamCommitCompleteLines() []string {
	lastNewline := strings.LastIndex(m.streamBuffer, "\n")
	if lastNewline == -1 {
		return nil
	}

	source := m.streamBuffer[:lastNewline+1]
	rendered := RenderMarkdownToANSI(source, m.streamWrapWidth)
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

	if strings.TrimSpace(m.streamBuffer) == "" {
		return nil
	}

	source := m.streamBuffer
	if !strings.HasSuffix(source, "\n") {
		source += "\n"
	}

	rendered := RenderMarkdownToANSI(source, m.streamWrapWidth)
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
	// 保留上一次的 wrap 宽度（通常来自 WindowSizeMsg），避免下一轮回退到默认 80。
}

// resize 根据窗口尺寸调整输入框与补全列表的宽度。
func (m *replModel) resize(width int) {
	if width < 0 {
		width = 0
	}
	if width < 20 {
		m.streamWrapWidth = 20
	} else {
		m.streamWrapWidth = width
	}

	m.list.SetWidth(width)
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

// sanitizeLines 清理控制字符并拆分潜在的多行输入。
func sanitizeLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		var parts []string
		if line == "" {
			parts = []string{""}
		} else {
			parts = splitLines(line)
			if len(parts) == 0 {
				parts = []string{""}
			}
		}

		for _, part := range parts {
			s := sanitizeLine(part)
			isVisualEmpty := isVisualEmptyLine(s)
			if isVisualEmpty {
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

var ansiRegex = regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]|\x1b\\([0-9A-Za-z]")

func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

func isVisualEmptyLine(line string) bool {
	stripped := stripANSI(line)
	return strings.TrimSpace(stripped) == ""
}

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

var (
	tuiLogOnce sync.Once
	tuiLogPath string
	tuiLogMu   sync.Mutex
)

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

func getTUILogPath() string {
	tuiLogOnce.Do(func() {
		tuiLogPath = strings.TrimSpace(os.Getenv("CHASE_TUI_LOG_PATH"))
	})
	return tuiLogPath
}

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
