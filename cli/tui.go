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

	// 流式 Markdown 渲染状态
	streamRenderedLines []string // 已输出的渲染行缓存
	streamWrapWidth     int
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
	text := strings.Join(clean, "\n")
	// 必须显式添加换行，否则多次输出会粘连；tea.Printf 只是 fmt.Fprintf 的封装。
	return tea.Printf("%s\n", text)
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

// popSafeChunk 从 buffer 中提取可以安全渲染的完整段落（以双换行结束）。
// 如果在代码块内，则忽略双换行，直到代码块闭合。
// 返回 extracted chunk (包含结尾换行) 和 remaining buffer。
func popSafeChunk(buffer string) (string, string) {
	if buffer == "" {
		return "", ""
	}

	idx := 0
	inFence := false

	// 简单的状态机扫描
	// 我们寻找不在 fence 内的 \n\n 序列
	length := len(buffer)
	lastSafeEnd := -1

	for idx < length {
		// 检查 fence
		// 必须是行首，或者前一个字符是 \n
		isLineStart := (idx == 0) || (buffer[idx-1] == '\n')
		if isLineStart && strings.HasPrefix(buffer[idx:], "```") {
			// 确定这是否真的是 fence 行
			// 找到这一行的结束
			lineEnd := strings.IndexByte(buffer[idx:], '\n')
			if lineEnd == -1 {
				// 这一行还没写完，肯定不能 split，直接跳出循环等待更多输入
				break
			}

			// 这是一个完整的 fence 行
			if !inFence {
				inFence = true
			} else {
				inFence = false
			}
			idx += lineEnd + 1
			continue
		}

		if inFence {
			// 在 fence 内，直接跳到下一行
			lineEnd := strings.IndexByte(buffer[idx:], '\n')
			if lineEnd == -1 {
				break
			}
			idx += lineEnd + 1
			continue
		}

		// 不在 fence 内，检查是否是双换行
		// 我们的目标是找到 \n\n
		// 注意：idx 目前指向行首（或者某行的开始）
		// 如果 buffer[idx] 是 \n，且 buffer[idx-1] 也是 \n (由于循环逻辑，我们其实是在寻找连续的换行)

		// 让我们简化逻辑：直接查找 \n\n
		// 从当前 idx 开始找
		nextNL := strings.IndexByte(buffer[idx:], '\n')
		if nextNL == -1 {
			break
		}

		// 找到了一个换行
		nlPos := idx + nextNL

		// 检查这个换行之后是否紧接着另一个换行
		// 注意：如果是 Windows 的 \r\n，这里可能需要更复杂的逻辑。
		// 但 glamour/bubbletea 通常处理 LF。
		// 检查 nlPos+1 是否也是 \n
		if nlPos+1 < length && buffer[nlPos+1] == '\n' {
			// 这是一个段落边界
			// chunk 应该包含这个边界
			lastSafeEnd = nlPos + 2 // include \n\n
			idx = lastSafeEnd
			continue // 继续找，贪婪匹配？不，我们流式输出，找到一个就可以输出了，减少延迟
			// 但为了性能，可以一次输出多个段落?
			// 这里我们返回找到的第一个段落即可
			return buffer[:lastSafeEnd], buffer[lastSafeEnd:]
		}

		idx = nlPos + 1
	}

	return "", buffer
}

// appendStreamDelta 追加流式增量并输出已完成的块。
func (m *replModel) appendStreamDelta(delta string) []string {
	if delta == "" {
		return nil
	}
	m.streamActive = true
	m.streamBuffer += delta
	if m.streamWrapWidth <= 0 {
		m.streamWrapWidth = m.resolveStreamWrapWidth()
	}

	var output []string

	for {
		chunk, remaining := popSafeChunk(m.streamBuffer)
		if chunk == "" {
			break
		}

		// 渲染这个 chunk
		rendered := renderMarkdownToANSI(chunk, m.streamWrapWidth)

		// 清理渲染结果的首尾空行，避免块之间叠加过大的 margin
		rendered = strings.TrimSpace(rendered)
		if rendered != "" {
			// 在输出前，我们需要决定是否加换行。
			// 因为 popSafeChunk 是按段落切的，段落之间应该有空行。
			// 但 glamour 可能已经在内部渲染了边距。
			// 策略：输出 rendered 后，再手动 append 一个空行，模拟段落间距。
			// 注意：splitLines 会把字符串切成行数组
			lines := splitLines(rendered)
			output = append(output, lines...)
			output = append(output, "") // 段落间距
		}

		m.streamBuffer = remaining
	}

	return output
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

	rendered := renderMarkdownToANSI(m.streamBuffer, m.streamWrapWidth)
	rendered = strings.TrimSpace(rendered)

	if rendered == "" {
		return nil
	}

	return splitLines(rendered)
}

// resetStreamState 清理流式输出状态，避免跨事件残留。
func (m *replModel) resetStreamState() {
	m.streamBuffer = ""
	m.streamActive = false
	m.streamHeaderPrinted = false
	m.streamRenderedLines = nil
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
		// 保留显式的空行（防止被 splitLines 吞掉）
		if line == "" {
			out = append(out, "")
			continue
		}
		parts := splitLines(line)
		// 如果 line 非空但 parts 为空（例如仅包含换行符），也视为空行保留
		if len(parts) == 0 {
			out = append(out, "")
			continue
		}
		for _, part := range parts {
			out = append(out, sanitizeLine(part))
		}
	}
	return out
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
