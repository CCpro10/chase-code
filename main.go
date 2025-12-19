package main

import "chase-code/cli"

// main 是程序入口。
// 这里仅负责将控制权转交给 cli 包，
// 由 cli.Run 统一完成子命令解析并调用 server 包的核心逻辑。
func main() {
	cli.Run()
}
