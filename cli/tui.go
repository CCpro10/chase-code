package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/cellbuf"

	"chase-code/server"
)

const (
	replMaxLogLines  = 2000
	replInputHeight  = 1
	replStatusHeight = 1
	replScrollLines  = 6
	replViewportPad  = 1
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
	program := tea.NewProgram(model, tea.WithAltScreen())
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
		input:    input,
		viewport: vp,
		events:   events,
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
			m.viewport.LineUp(replScrollLines)
			return m, nil
		case tea.KeyPgDown:
			m.viewport.LineDown(replScrollLines)
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
		m.input.View(),
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
	m.lines = append(m.lines, lines...)
	if len(m.lines) > replMaxLogLines {
		m.lines = m.lines[len(m.lines)-replMaxLogLines:]
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
	promptWidth := lipgloss.Width(m.input.Prompt)
	inputWidth := width - promptWidth - 1
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
	parts := []string{"Ctrl+C 退出", "Enter 发送"}
	if isAgentRunning() {
		parts = append(parts, "agent 处理中")
	}
	if m.pendingApprovalID != "" {
		parts = append(parts, fmt.Sprintf("待审批: %s (y/s)", m.pendingApprovalID))
	}
	return styleStatus.Render(strings.Join(parts, " | "))
}

// replBannerLines 返回启动提示。
func replBannerLines() []string {
	cwd, _ := os.Getwd()
	return []string{
		styleDim.Render(fmt.Sprintf("chase-code repl（agent 优先），当前工作目录: %s", cwd)),
		styleDim.Render("直接输入问题或指令时，将通过 LLM+工具以 agent 方式执行；输入 :help 查看可用命令，:q 退出。"),
	}
}

// renderWrappedContent 对日志内容做软换行，避免窗口缩窄后内容溢出。
func renderWrappedContent(lines []string, width int) string {
	if len(lines) == 0 {
		return ""
	}
	content := strings.Join(lines, "\n")
	contentWidth := width - replViewportPad
	if contentWidth < 1 {
		return content
	}
	return cellbuf.Wrap(content, contentWidth, "")
}
