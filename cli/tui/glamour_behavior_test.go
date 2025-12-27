package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/glamour"
	"github.com/stretchr/testify/assert"
)

// TestGlamourBehavior 验证 glamour 的渲染特性，
// 确认它是否会在段落、列表项之间产生空行，以及是否会产生连续空行。
func TestGlamourBehavior(t *testing.T) {
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(80),
	)
	assert.NoError(t, err)

	cases := []struct {
		name        string
		input       string
		description string
	}{
		{
			name:        "Paragraphs",
			input:       "Para 1\n\nPara 2",
			description: "段落之间通常会有空行",
		},
		{
			name: "Tight List",
			input: `1. Item A
2. Item B`,
			description: "紧凑列表（源码无空行）通常渲染较紧凑，但也可能包含 Margin",
		},
		{
			name: "Loose List",
			input: `1. Item A

2. Item B`,
			description: "松散列表（源码有空行）glamour 渲染时倾向于保留更大的间距",
		},
		{
			name:        "Explicit Line Breaks",
			input:       "Line 1  \nLine 2", // 两个空格 + 换行
			description: "硬换行应该在同一段落内换行，不产生段落间距",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := r.Render(tc.input)
			assert.NoError(t, err)

			// 原始输出分析
			// glamour 的输出通常包含 ANSI 码，我们需要移除或忽略它们来观察换行
			rawLines := strings.Split(strings.TrimRight(out, "\n"), "\n")

			t.Logf("=== Case: %s ===", tc.name)
			t.Logf("Input:\n%q", tc.input)
			t.Logf("Rendered Line Count: %d", len(rawLines))

			// 统计空行（包含仅有不可见字符的行）
			emptyLineCount := 0
			consecutiveEmptyLines := 0
			maxConsecutiveEmpty := 0

			for i, line := range rawLines {
				// 简单的清理 ANSI 码用于日志查看（不完全准确，但足够看结构）
				clean := sanitizeLine(line) // 复用 cli 包内的工具
				t.Logf("[%02d] (raw len=%d) clean=%q", i, len(line), clean)

				if clean == "" {
					emptyLineCount++
					consecutiveEmptyLines++
					if consecutiveEmptyLines > maxConsecutiveEmpty {
						maxConsecutiveEmpty = consecutiveEmptyLines
					}
				} else {
					consecutiveEmptyLines = 0
				}
			}
			t.Logf("Stats: Total Empty=%d, Max Consecutive=%d", emptyLineCount, maxConsecutiveEmpty)
			t.Logf("Expectation: %s", tc.description)
		})
	}
}

// TestSanitizeLines_Compression 验证 sanitizeLines 是否按预期压缩了连续空行。
// 这是基于 glamour 行为（确实产生多余空行）之后的应对措施。
func TestSanitizeLines_Compression(t *testing.T) {
	// 模拟 glamour 可能产生的情况：内容行 - 空行 - 空行 - 内容行
	// 这种连续空行可能是由于 Block 之间的 Margin 叠加导致的
	input := []string{
		"Content A",
		"",
		"",
		"Content B",
		"",
		"Content C",
	}

	expected := []string{
		"Content A",
		"",
		"Content B",
		"",
		"Content C",
	}

	actual := sanitizeLines(input)
	assert.Equal(t, expected, actual, "Should compress consecutive empty lines to a single one")
}
