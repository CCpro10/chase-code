package tools

import (
	"io"
	"os"
)

// 为了便于后续扩展（比如对 stdout/stderr 加颜色、做前缀等），
// 将它们封装成可替换的变量。server 包内的执行逻辑统一使用这些变量，
// CLI 层可以在需要时替换它们以实现差异化输出。
var (
	StdoutColored io.Writer = os.Stdout
	StderrColored io.Writer = os.Stderr
)
