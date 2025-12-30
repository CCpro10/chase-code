package main

// 程序入口。
// 主要职责是初始化命令行接口并将控制权交给 cli 包。
// 具体的子命令、参数解析和业务逻辑都在 cli.Run 中实现。
import "chase-code/cli"

func main() {
	cli.Run()
}
