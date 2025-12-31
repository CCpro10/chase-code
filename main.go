// chase-code 程序入口
package main

// cli 包负责命令行解析与主逻辑
import "chase-code/cli"

// main 是程序的入口函数，在这里只做一件事：
// 把控制权交给 cli.Run，由 cli 统一处理所有子命令和业务逻辑。
func main() {
	cli.Run()
}
