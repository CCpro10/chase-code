package tui

// Suggestion 定义了自动补全项的接口。
type Suggestion interface {
	Name() string
	Description() string
	Aliases() []string
}

// DispatchResult 包含命令执行后的 UI 反馈信息。
type DispatchResult struct {
	Lines []string // 要输出的文本行
	Quit  bool     // 是否请求退出程序
}

// Dispatcher 定义了处理用户输入的回调函数。
type Dispatcher func(input string, pendingApprovalID string) (DispatchResult, error)
