package cli

import "github.com/charmbracelet/lipgloss"

// 终端样式集中管理，便于 TUI 统一调色。
var (
	asciiBorder    = lipgloss.Border{Top: "-", Bottom: "-", Left: "|", Right: "|", TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+"}
	styleDim       = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Faint(true)
	styleCyan      = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleYellow    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleGreen     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleMagenta   = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	styleError     = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	stylePrompt    = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	styleInput     = lipgloss.NewStyle().Foreground(lipgloss.Color("7")).Background(lipgloss.Color("235"))
	styleStatus    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleUser      = lipgloss.NewStyle().Foreground(lipgloss.Color("7")).Bold(true)
	styleBanner    = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	styleBannerA   = lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Bold(true)
	styleGuideBox  = lipgloss.NewStyle().Border(asciiBorder).BorderForeground(lipgloss.Color("8")).Padding(0, 1)
	styleGuideHead = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	styleInputBox  = lipgloss.NewStyle().Padding(1, 1).Background(lipgloss.Color("235"))
	styleInputOn   = lipgloss.NewStyle().Padding(1, 1).Background(lipgloss.Color("235"))
)
