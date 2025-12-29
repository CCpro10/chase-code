// main.go 是 chase-code 命令行工具的入口文件。
//
// 职责：
//  1. 定义 main 函数，作为整个程序的起始点。
//  2. 将控制权转交给 cli.Run，由后者完成具体的命令行解析与业务逻辑。
package main

import "chase-code/cli"

// main 是 chase-code 命令行工具的入口函数。
// 这里不包含具体业务逻辑，只是简单地将控制权交给 cli.Run，
// 由 cli 包负责解析命令行参数并执行相应的子命令。
func main() {
	cli.Run()
}
