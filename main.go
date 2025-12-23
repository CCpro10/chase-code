// main.go
//
// chase-code 的启动入口。
// 负责初始化并调用 CLI 模块，将控制权交给命令行子系统。
package main

import "chase-code/cli"

func main() {
	cli.Run()
}
