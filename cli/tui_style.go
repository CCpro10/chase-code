package cli

import "github.com/charmbracelet/lipgloss"

// 终端样式集中管理，便于 TUI 统一调色。
var (
	styleDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Faint(true)
	styleCyan    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleYellow  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleGreen   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleMagenta = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	styleError   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	stylePrompt  = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	styleInput   = lipgloss.NewStyle().Foreground(lipgloss.Color("7"))
	styleStatus  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleUser    = lipgloss.NewStyle().Foreground(lipgloss.Color("7")).Bold(true)
)
