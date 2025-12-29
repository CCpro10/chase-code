// main.go: chase-code 命令行工具入口。
// 负责调用 cli.Run 启动命令行解析和执行逻辑。
package main

import "chase-code/cli"

// main 为程序入口，交由 cli.Run 处理后续流程。
func main() {
	cli.Run()
}
