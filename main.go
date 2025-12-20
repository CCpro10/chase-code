package main

import "chase-code/cli"

// main 是 chase-code 的入口函数，负责启动 CLI 命令行界面。
// 实际业务逻辑由 cli.Run() 统一分发处理。
func main() {
	cli.Run()
}
