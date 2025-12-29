# 总是使用中文回复

# 为了可读性和可维护性，高质量的代码应该有必要的注释，函数注释是最基本的
    - 可维护性、日志、错误处理、函数单一原则、严禁高圈复杂度！高质量代码
    
# 务必一次性、完整尽全力完成任务
    - 一次就把代码实现出来
    - 完成后的总结简洁清晰

# 代码修改后需要编译通过


# TUI 集成测试经验（实机输出观察）
    - 目标：在伪终端环境中跑一轮 TUI，抓取真实输出，便于排查 Markdown 渲染空行。
    - 关键环境变量：
        - CHASE_TUI_LOG_PATH：TUI 输出日志路径（建议使用 `.chase-code/tui.log`）。
        - CHASE_TUI_LOG_PLAIN=1：日志去掉 ANSI，便于肉眼排查空行。
        - CHASE_TUI_EXIT_ON_DONE=1：首轮 turn 结束自动退出，便于脚本化运行。
        - CHASE_TUI_DEBUG=1：打印 Markdown 归一化/渲染诊断日志（仅排查时启用）。
    - 推荐命令（必须在伪终端内运行）：
        - script -q /dev/null /bin/zsh -lc 'CHASE_TUI_LOG_PATH=/Users/bytedance/Projects/ccpro/chase-code/.chase-code/tui.log CHASE_TUI_LOG_PLAIN=1 CHASE_TUI_EXIT_ON_DONE=1 CHASE_TUI_DEBUG=1 go run . "你有什么工具"'
    - 注意事项：
        - 直接 `go run` 在非 TTY 下会卡住或无法正确渲染，需 `script` 创建伪终端。
        - 若无输出，确认 LLM 配置已就绪（依赖项目现有配置与环境变量）。
        - 日志路径目录需提前创建（可用 `mkdir -p /Users/bytedance/Projects/ccpro/chase-code/.chase-code`）。
