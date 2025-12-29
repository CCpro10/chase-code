package tui

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	rw "github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"

	"chase-code/server"
)

const inputMaxLines = 10

// replModel 负责管理 TUI 的状态与渲染。
type replModel struct {
	input              textarea.Model
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

	// 终端尺寸，用于输入法光标定位与布局计算。
	windowWidth  int
	windowHeight int

	// 输入法光标跟踪器，保证真实光标位置正确。
	imeCursor *imeCursorTracker

	// 输入框视口偏移量，避免 IME 光标定位漂移。
	inputViewportOffset int
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
	imeCursor := newIMECursorTracker()
	output := newIMECursorWriter(os.Stdout, imeCursor)
	model := newReplModel(events, initialInput, dispatcher, suggestions, imeCursor)
	program := tea.NewProgram(model, tea.WithOutput(output))
	_, err := program.Run()
	return err
}

// newReplModel 构造 TUI 模型。
func newReplModel(events <-chan server.Event, initialInput string, dispatcher Dispatcher, suggestions []Suggestion, imeCursor *imeCursorTracker) replModel {
	// 初始化 textarea 作为输入框
	input := textarea.New()
	input.Prompt = ""
	input.ShowLineNumbers = false
	input.MaxHeight = inputMaxLines
	input.SetHeight(1)
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "ctrl+j"))
	inputBase := styleInput.
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("8")).
		Padding(0, 1)
	input.FocusedStyle.Base = inputBase
	input.FocusedStyle.Text = styleInput
	input.FocusedStyle.Prompt = styleInput
	input.FocusedStyle.CursorLine = styleInput
	input.FocusedStyle.CursorLineNumber = styleInput
	input.FocusedStyle.LineNumber = styleInput
	input.FocusedStyle.Placeholder = styleInput
	input.FocusedStyle.EndOfBuffer = styleInput
	input.BlurredStyle = input.FocusedStyle
	// 让光标样式与输入区背景一致，避免光标被“吞掉”误判为停在起始位置。
	input.Cursor.Style = styleInput
	input.Cursor.TextStyle = styleInput
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
		imeCursor:          imeCursor,
	}
}

// Init 启动事件监听、光标闪烁，并输出启动提示。
func (m replModel) Init() tea.Cmd {
	cmds := []tea.Cmd{
		textarea.Blink,
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
		return m.handleWindowSizeMsg(msg)
	case tea.KeyMsg:
		return m.handleKeyMsg(msg)
	case replEventMsg:
		return m.handleReplEventMsg(msg)
	case replEventClosedMsg:
		return m.handleReplEventClosedMsg()
	case replAutoRunMsg:
		return m.handleAutoRunMsg(msg)
	case replDispatchMsg:
		return m.handleDispatch(msg)
	}
	return m.handleInputMsg(msg)
}

// handleWindowSizeMsg 处理窗口尺寸变化并同步输入框与列表布局。
func (m replModel) handleWindowSizeMsg(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	// 同步 textarea 的宽度，避免光标与换行位置偏移。
	m.input.SetWidth(msg.Width)
	m.updateInputLayout()
	// 更新补全列表宽度与流式渲染的 wrap 宽度。
	m.resize(msg.Width, msg.Height)
	return m, nil
}

// handleKeyMsg 处理键盘输入，优先消费补全列表相关按键。
func (m replModel) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyEnter && msg.Alt {
		return m.handleInputMsg(msg)
	}
	if m.showList {
		if model, cmd, handled := m.handleListKeyMsg(msg); handled {
			return model, cmd
		}
	}

	switch msg.Type {
	case tea.KeyCtrlC, tea.KeyCtrlD:
		m.exiting = true
		return m, tea.Quit
	case tea.KeyEnter:
		return m.handleEnter()
	}

	return m.handleInputMsg(msg)
}

// handleListKeyMsg 处理补全列表的导航、选择与关闭。
func (m replModel) handleListKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.Type {
	case tea.KeyUp, tea.KeyDown:
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd, true
	case tea.KeyTab, tea.KeyEnter:
		if it, ok := m.list.SelectedItem().(suggestionItem); ok {
			m.input.SetValue("/" + it.suggestion.Name() + " ")
			m.input.CursorEnd()
			m.updateInputLayout()
			m.showList = false
		}
		return m, nil, true
	case tea.KeyEsc:
		m.showList = false
		return m, nil, true
	default:
		return m, nil, false
	}
}

// handleReplEventMsg 处理来自后端的事件并决定是否自动退出。
func (m replModel) handleReplEventMsg(msg replEventMsg) (tea.Model, tea.Cmd) {
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
}

// handleReplEventClosedMsg 输出事件通道关闭提示。
func (m replModel) handleReplEventClosedMsg() (tea.Model, tea.Cmd) {
	return m, printReplLinesCmd([]string{styleDim.Render("[event] 通道已关闭")})
}

// handleAutoRunMsg 处理启动时自动输入的指令。
func (m replModel) handleAutoRunMsg(msg replAutoRunMsg) (tea.Model, tea.Cmd) {
	echo := []string{styleUser.Render("> " + msg.input)}
	return m, tea.Batch(
		printReplLinesCmd(echo),
		m.replDispatchCmd(msg.input, m.pendingApprovalID),
	)
}

// handleInputMsg 将消息交给输入框处理并刷新补全列表。
func (m replModel) handleInputMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.updateInputLayout()
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
		m.updateIMECursorTracker(false)
		return ""
	}

	m.updateIMECursorTracker(true)
	inputView := m.input.View()
	if !m.showList {
		return inputView
	}

	listView := m.list.View()

	// 补全列表放在输入框下方
	return lipgloss.JoinVertical(lipgloss.Left, inputView, listView)
}

// updateIMECursorTracker 在渲染阶段同步真实光标位置，供输入法定位使用。
func (m replModel) updateIMECursorTracker(active bool) {
	if m.imeCursor == nil {
		return
	}
	if !active || !m.input.Focused() {
		m.imeCursor.Set(false, 0, 0)
		return
	}

	upLines := 0
	if m.showList {
		upLines = m.list.Height()
	}
	upLines += m.inputBottomOffset()

	inputHeight := m.input.Height()
	if inputHeight < 1 {
		inputHeight = 1
	}
	cursorRow := m.inputCursorVisualRow()
	cursorRowInView := cursorRow - m.inputViewportOffset
	if cursorRowInView < 0 {
		cursorRowInView = 0
	}
	if cursorRowInView >= inputHeight {
		cursorRowInView = inputHeight - 1
	}
	upLines += inputHeight - 1 - cursorRowInView

	if m.windowHeight > 0 && upLines >= m.windowHeight {
		upLines = m.windowHeight - 1
		if upLines < 0 {
			upLines = 0
		}
	}

	lineInfo := m.input.LineInfo()
	col := lineInfo.CharOffset + m.inputLeftOffset()
	if promptWidth := lipgloss.Width(m.input.Prompt); promptWidth > 0 {
		col += promptWidth
	}
	if m.input.ShowLineNumbers {
		col += 4
	}
	if m.windowWidth > 0 && col >= m.windowWidth {
		col = m.windowWidth - 1
	}
	if col < 0 {
		col = 0
	}
	m.imeCursor.Set(true, upLines, col)
}

// updateSuggestions 根据当前输入更新补全列表。
func (m *replModel) updateSuggestions() {
	val := m.input.Value()
	if !strings.HasPrefix(val, "/") || strings.IndexFunc(val, unicode.IsSpace) != -1 {
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
	raw := m.input.Value()
	line := strings.TrimSpace(raw)
	m.input.SetValue("")
	m.updateInputLayout()
	if line == "" {
		return m, nil
	}
	echo := []string{styleUser.Render("> " + raw)}
	return m, tea.Batch(
		printReplLinesCmd(echo),
		m.replDispatchCmd(raw, m.pendingApprovalID),
	)
}

// updateInputLayout 根据内容更新输入框高度与视口偏移。
func (m *replModel) updateInputLayout() {
	height := m.inputVisualLineCount()
	if height < 1 {
		height = 1
	}
	if height > inputMaxLines {
		height = inputMaxLines
	}
	if m.input.Height() != height {
		m.input.SetHeight(height)
	}
	m.updateInputViewportOffset()
}

// updateInputViewportOffset 保持 IME 与 textarea 的垂直滚动对齐。
func (m *replModel) updateInputViewportOffset() {
	height := m.input.Height()
	if height < 1 {
		height = 1
	}
	totalLines := m.inputVisualLineCount()
	cursorLine := m.inputCursorVisualRow()
	offset := m.inputViewportOffset

	minVisible := offset
	maxVisible := offset + height - 1
	if cursorLine < minVisible {
		offset -= minVisible - cursorLine
	} else if cursorLine > maxVisible {
		offset += cursorLine - maxVisible
	}

	if offset < 0 {
		offset = 0
	}
	maxOffset := totalLines - 1
	if maxOffset < 0 {
		maxOffset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	m.inputViewportOffset = offset
}

// inputVisualLineCount 计算 textarea 内容的可视行数（含软换行）。
func (m replModel) inputVisualLineCount() int {
	width := m.inputTextWidth()
	if width <= 0 {
		return 1
	}
	lines := strings.Split(m.input.Value(), "\n")
	if len(lines) == 0 {
		return 1
	}
	total := 0
	for _, line := range lines {
		total += wrappedLineCount([]rune(line), width)
	}
	if total < 1 {
		return 1
	}
	return total
}

// inputCursorVisualRow 返回光标所在的可视行号（0-based）。
func (m replModel) inputCursorVisualRow() int {
	width := m.inputTextWidth()
	if width <= 0 {
		return 0
	}
	lines := strings.Split(m.input.Value(), "\n")
	if len(lines) == 0 {
		return 0
	}
	row := m.input.Line()
	if row < 0 {
		row = 0
	}
	if row >= len(lines) {
		row = len(lines) - 1
	}
	total := 0
	for i := 0; i < row; i++ {
		total += wrappedLineCount([]rune(lines[i]), width)
	}
	lineInfo := m.input.LineInfo()
	return total + lineInfo.RowOffset
}

// inputTextWidth 返回 textarea 内容区的可用宽度。
func (m replModel) inputTextWidth() int {
	width := m.input.Width()
	if width <= 0 {
		width = 1
	}
	return width
}

// inputLeftOffset 计算输入框左侧边框与内边距的列偏移。
func (m replModel) inputLeftOffset() int {
	frameHorizontal := m.input.FocusedStyle.Base.GetHorizontalFrameSize()
	return frameHorizontal / 2
}

// inputBottomOffset 计算输入框底部边框与内边距占用的行数。
func (m replModel) inputBottomOffset() int {
	frameVertical := m.input.FocusedStyle.Base.GetVerticalFrameSize()
	if frameVertical <= 0 {
		return 0
	}
	return frameVertical / 2
}

// wrappedLineCount 计算一行文本在指定宽度下的软换行行数。
func wrappedLineCount(runes []rune, width int) int {
	if width <= 0 {
		return 1
	}
	if len(runes) == 0 {
		return 1
	}
	return len(wrapRunes(runes, width))
}

// wrapRunes 使用 textarea 的换行规则对文本进行软换行。
func wrapRunes(runes []rune, width int) [][]rune {
	if width <= 0 {
		return [][]rune{{}}
	}

	lines := [][]rune{{}}
	word := []rune{}
	row := 0
	spaces := 0

	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}

		if spaces > 0 {
			if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces > width {
				row++
				lines = append(lines, []rune{})
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], repeatSpaces(spaces)...)
				spaces = 0
				word = nil
			} else {
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], repeatSpaces(spaces)...)
				spaces = 0
				word = nil
			}
		} else if len(word) > 0 {
			lastCharLen := rw.RuneWidth(word[len(word)-1])
			if uniseg.StringWidth(string(word))+lastCharLen > width {
				if len(lines[row]) > 0 {
					row++
					lines = append(lines, []rune{})
				}
				lines[row] = append(lines[row], word...)
				word = nil
			}
		}
	}

	if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces >= width {
		lines = append(lines, []rune{})
		lines[row+1] = append(lines[row+1], word...)
		spaces++
		lines[row+1] = append(lines[row+1], repeatSpaces(spaces)...)
	} else {
		lines[row] = append(lines[row], word...)
		spaces++
		lines[row] = append(lines[row], repeatSpaces(spaces)...)
	}

	return lines
}

// repeatSpaces 生成指定数量的空格 rune 切片。
func repeatSpaces(n int) []rune {
	if n <= 0 {
		return nil
	}
	return []rune(strings.Repeat(string(' '), n))
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
func (m *replModel) resize(width int, height int) {
	if width < 0 {
		width = 0
	}
	if height < 0 {
		height = 0
	}
	m.windowWidth = width
	m.windowHeight = height
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
