package cli

import (
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func init() {
	// 强制使用 TrueColor 配置文件并禁用终端查询，避免出现 [43;1R 或 ]11;rgb: 等干扰字符。
	// 同时设置 lipgloss 和 termenv 的默认输出，确保全局禁用自动检测。
	renderer := lipgloss.NewRenderer(os.Stdout)
	renderer.SetColorProfile(termenv.TrueColor)
	lipgloss.SetDefaultRenderer(renderer)

	termenv.SetDefaultOutput(termenv.NewOutput(os.Stdout, termenv.WithProfile(termenv.TrueColor)))
}

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
	styleSelected  = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("4")).Bold(true)
)
