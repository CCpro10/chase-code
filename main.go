package main

import "chase-code/cli"

// main 是 chase-code 的入口函数。
// 它负责把控制权转交给 cli 包，由 cli.Run() 完成后续命令行解析与执行。
// 如需新增子命令或修改启动流程，请在 cli 包内调整，保持 main 简洁。
func main() {
	cli.Run()
}
