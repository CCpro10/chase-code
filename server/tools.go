package server

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseToolCallsJSON 解析 LLM 输出的 JSON 字符串，支持单个对象或对象数组。
func ParseToolCallsJSON(raw string) ([]ToolCall, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("工具调用 JSON 为空")
	}

	if strings.HasPrefix(raw, "[") {
		var calls []ToolCall
		if err := json.Unmarshal([]byte(raw), &calls); err != nil {
			return nil, err
		}
		return calls, nil
	}

	var call ToolCall
	if err := json.Unmarshal([]byte(raw), &call); err != nil {
		return nil, err
	}
	return []ToolCall{call}, nil
}

// DefaultToolSpecs 返回 chase-code 默认暴露给 LLM 的工具集合。
func DefaultToolSpecs() []ToolSpec {
	return []ToolSpec{
		{Kind: ToolKindFunction, Name: "shell", Description: "执行 shell 命令。arguments: {\"command\": string, \"timeout_ms\"?: int, \"policy\"?: \"full\"|\"readonly\"|\"workspace\"}"},
		{Kind: ToolKindFunction, Name: "read_file", Description: "读取文件内容。arguments: {\"path\": string, \"max_bytes\"?: int}"},
		{Kind: ToolKindFunction, Name: "list_dir", Description: "列出目录内容。arguments: {\"path\": string}"},
		{Kind: ToolKindFunction, Name: "grep_files", Description: "使用 ripgrep 在代码中查找匹配 pattern 的行，支持正则/模糊搜索。arguments: {\"root\": string, \"pattern\": string, \"max_matches\"?: int}"},
		{Kind: ToolKindFunction, Name: "apply_patch", Description: "对单个文件应用简单补丁（基于字符串替换）。arguments: {\"file\": string, \"from\": string, \"to\": string, \"all\"?: bool}"},
	}
}

// BuildToolSystemPrompt 基于工具列表构造一段 system prompt，
// 模仿 codex 的 prompt engineering 风格，明确区分：
//   - 需要继续调用工具时，只输出工具调用 JSON；
//   - 已经可以回答用户时，直接输出自然语言答案。
func BuildToolSystemPrompt(tools []ToolSpec) string {
	var b strings.Builder

	// 角色与目标
	b.WriteString("你是 chase-code 的本地代码助手，运行在用户的工作目录中，可以通过一组工具来查看代码、编辑文件以及执行命令。\n")
	b.WriteString("你的目标是：在保证安全和谨慎修改代码的前提下，尽量自动完成用户的开发任务，并用中文解释你的思路。\n\n")

	// 输出模式
	b.WriteString("=== 输出模式 ===\n\n")
	b.WriteString("你有两种输出模式，每一轮必须二选一：\n\n")

	b.WriteString("1. 工具调用模式（tool_calls）\n")
	b.WriteString("   当你认为需要更多上下文或要对代码做操作时，请只输出 JSON，不要包含任何解释文字。\n")
	b.WriteString("   具体格式：\n")
	b.WriteString("   - 单个工具调用：\n")
	b.WriteString("     {\"tool_name\": \"<工具名>\", \"arguments\": { ... }}\n")
	b.WriteString("   - 多个工具调用（顺序执行）：\n")
	b.WriteString("     [\n")
	b.WriteString("       {\"tool_name\": \"<工具名1>\", \"arguments\": { ... }},\n")
	b.WriteString("       {\"tool_name\": \"<工具名2>\", \"arguments\": { ... }}\n")
	b.WriteString("     ]\n\n")
	b.WriteString("   说明：\n")
	b.WriteString("   - arguments 必须是 JSON 对象，字段名和含义要严格符合工具描述。\n")
	b.WriteString("   - 不要把自然语言包在 JSON 里，也不要在 JSON 前后加文字或 ``` 代码块。\n")
	b.WriteString("   - 如果某一步只需要一个工具，就只输出一个对象，而不是数组。\n\n")

	b.WriteString("2. 直接回答模式（final_answer）\n")
	b.WriteString("   当你觉得已有足够信息可以回答用户的问题或给出下一步建议时，\n")
	b.WriteString("   不要再输出 JSON，而是直接输出一段自然语言回答（用中文）。\n\n")
	b.WriteString("   要求：\n")
	b.WriteString("   - 明确说明你的结论。\n")
	b.WriteString("   - 简要说明你刚才用过哪些工具（如果有）以及得到的关键信息。\n")
	b.WriteString("   - 如果还存在不确定性或需要用户决策的地方，要说清楚。\n\n")

	// 使用工具的原则
	b.WriteString("=== 使用工具的原则 ===\n\n")
	b.WriteString("- 先思考再行动：\n")
	b.WriteString("  在决定调用工具之前，先推理你缺什么信息、用哪个工具最合适。不要盲目或重复调用同一个工具。\n\n")

	b.WriteString("- 合理使用工具：\n")
	b.WriteString("  - 想了解项目结构 → 优先使用 list_dir 或 grep_files。\n")
	b.WriteString("  - 想理解某个文件的实现 → 使用 read_file。\n")
	b.WriteString("  - 想做小范围修改 → 使用 edit_file 或 apply_patch，修改前要尽量通过 read_file 确认上下文。\n")
	b.WriteString("  - 想执行当前目录下的命令（如 go test / go build）→ 使用 shell，但要谨慎，不要执行危险命令。\n\n")

	b.WriteString("- 避免死循环：\n")
	b.WriteString("  - 不要在多轮对话中重复对同一文件、同一 pattern 做完全相同的工具调用。\n")
	b.WriteString("  - 如果工具输出已经明显无法继续推进问题，就应该转入 final_answer 模式，解释现状或向用户提问。\n\n")

	// 工具列表
	b.WriteString("=== 可用工具列表 ===\n")
	for i, t := range tools {
		fmt.Fprintf(&b, "%d. %s — %s\n", i+1, t.Name, t.Description)
	}
	b.WriteString("\n")

	// 总体策略
	b.WriteString("=== 总体策略 ===\n\n")
	b.WriteString("收到用户问题后，你的流程应类似于：\n")
	b.WriteString("1. 在脑中分析当前上下文，判断是否需要工具。\n")
	b.WriteString("2. 如果需要 → 输出工具调用 JSON（tool_calls 模式），不夹带文字。\n")
	b.WriteString("3. 等工具结果返回后，再根据新的上下文重新思考：\n")
	b.WriteString("   - 如果还缺信息 → 再输出下一轮工具调用 JSON；\n")
	b.WriteString("   - 如果已经足够 → 输出自然语言答案（final_answer 模式），结束本轮。\n\n")

	b.WriteString("记住：\n")
	b.WriteString("- 当你输出 JSON 时，只能有 JSON，没有任何自然语言。\n")
	b.WriteString("- 当你输出自然语言回答时，不能再嵌入工具 JSON。\n")

	return b.String()
}
