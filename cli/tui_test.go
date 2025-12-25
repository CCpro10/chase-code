package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// streamLinesForTest 模拟流式渲染输出并返回去除 ANSI 的行。
func streamLinesForTest(deltas []string, width int) []string {
	m := &replModel{
		streamWrapWidth: width,
	}
	var out []string
	for _, delta := range deltas {
		out = append(out, m.appendStreamDelta(delta)...)
	}
	out = append(out, m.flushStreamFinal("")...)
	return normalizeStreamTestLines(out)
}

// fullLinesForTest 渲染完整 Markdown 并返回去除 ANSI 的行。
func fullLinesForTest(input string, width int) []string {
	rendered := renderMarkdownToANSI(input, width)
	return normalizeStreamTestLines(splitLines(rendered))
}

// normalizeStreamTestLines 统一清理测试行，保证对比一致。
func normalizeStreamTestLines(lines []string) []string {
	cleaned := sanitizeLines(lines)
	out := make([]string, 0, len(cleaned))
	for _, line := range cleaned {
		out = append(out, stripANSI(line))
	}
	return out
}

func TestStreamMarkdownMatchesFullRender(t *testing.T) {
	cases := []struct {
		name   string
		deltas []string
	}{
		{
			name:   "plain_no_newline",
			deltas: []string{"Hello, world"},
		},
		{
			name:   "heading_after_paragraph",
			deltas: []string{"Hello.\n", "## Heading\n"},
		},
		{
			name:   "list_split",
			deltas: []string{"- a\n- ", "b\n- c\n"},
		},
		{
			name:   "fenced_code",
			deltas: []string{"```", "\ncode 1\ncode 2\n", "```\n"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fullInput := strings.Join(tc.deltas, "")
			full := fullLinesForTest(fullInput, 80)
			streamed := streamLinesForTest(tc.deltas, 80)
			assert.Equal(t, full, streamed)
		})
	}
}

// TestSanitizeLines verifies the fix for empty lines being swallowed.
func TestSanitizeLines(t *testing.T) {
	input := []string{"Para 1", "", "Para 2"}
	output := sanitizeLines(input)
	assert.Equal(t, []string{"Para 1", "", "Para 2"}, output, "Should preserve empty strings")

	input2 := []string{"Line 1\nLine 2", "\n", "Line 3"}
	// splitLines("Line 1\nLine 2") -> ["Line 1", "Line 2"]
	// splitLines("\n") -> splitLines("") -> nil ?? Wait, let's check splitLines behavior in tui.go
	// In tui.go: splitLines checks s == "" -> return nil.
	// But sanitizeLines handles len(parts)==0 case now.

	output2 := sanitizeLines(input2)
	// "Line 1\nLine 2" -> ["Line 1", "Line 2"]
	// "\n" -> trimmed suffix "\n" -> "" -> splitLines returns nil -> sanitizedLines handles this as ""
	// "Line 3" -> ["Line 3"]
	expected := []string{"Line 1", "Line 2", "", "Line 3"}
	assert.Equal(t, expected, output2)
}

// TestSanitizeLines_StripANSI 验证 sanitizeLines 是否能识别仅包含 ANSI 码的行并将其视为空行处理。
func TestSanitizeLines_StripANSI(t *testing.T) {
	// 模拟场景：
	// renderMarkdownToANSI 产生了一些包含颜色重置码的行，虽然视觉上是空行，但字符串不为空。
	input := []string{
		"Line 1",
		"\x1b[0m",       // ANSI reset code, visually empty
		"   \x1b[0m   ", // spaces + ANSI, visually empty
		"",
		"Line 2",
	}

	// 我们期望 sanitizeLines 能识别出中间那些都是空行，并压缩为一个空行。
	expected := []string{
		"Line 1",
		"",
		"Line 2",
	}

	actual := sanitizeLines(input)

	// 在目前的实现中（未修复前），这可能会失败。
	// 我们先看看现在的行为。
	// 如果失败，我们再修复。
	assert.Equal(t, expected, actual)
}

func TestSanitizeLines_ComplexANSI(t *testing.T) {
	input := []string{
		"Start",
		"\x1b[0m",    // CSI: Reset
		"\x1b[1;31m", // CSI: Bold Red
		"\x1b(B",     // G0 Set (not matched by simple CSI regex)
		"\x1b[?25h",  // CSI: Show Cursor (private mode)
		"End",
	}

	// 如果我们的正则只匹配 CSI，那么 \x1b(B 就会被残留下来，导致非空判断。
	// 我们期望这些都被视为 "视觉空行"。
	expected := []string{
		"Start",
		"",
		"End",
	}

	actual := sanitizeLines(input)
	// 如果这里失败，说明正则需要改进
	assert.Equal(t, expected, actual)
}
