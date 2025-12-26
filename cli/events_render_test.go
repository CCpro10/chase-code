package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeMarkdownForTUI_BulletList(t *testing.T) {
	input := "1. 看项目\n\n    • 看目录里有哪些文件和子目录\n\n    • 打开某个文件看内容"
	expected := "1. 看项目\n    - 看目录里有哪些文件和子目录\n    - 打开某个文件看内容"

	actual := normalizeMarkdownForTUI(input)
	assert.Equal(t, expected, actual)
}

func TestNormalizeMarkdownForTUI_PreserveParagraphGap(t *testing.T) {
	input := "- Item A\n\nParagraph B"
	expected := "- Item A\n\nParagraph B"

	actual := normalizeMarkdownForTUI(input)
	assert.Equal(t, expected, actual)
}

func TestNormalizeMarkdownForTUI_UserSample_NoListGaps(t *testing.T) {
	input := "1. 看项目\n\n    • 看目录里有哪些文件和子目录\n\n    • 打开某个文件看内容\n\n    • 在整个项目里搜索某个关键字或正则"
	normalized := normalizeMarkdownForTUI(input)
	lines := splitLinesPreserveTrailing(normalized)
	assert.False(t, hasListBlankGap(lines), "should not keep blank lines between list items")
}

func TestNormalizeMarkdownForTUI_BulletOnlyLine(t *testing.T) {
	input := "1. 看项目\n    •\n    看目录里有哪些文件和子目录"
	expected := "1. 看项目\n    - 看目录里有哪些文件和子目录"

	actual := normalizeMarkdownForTUI(input)
	assert.Equal(t, expected, actual)
}

func TestRenderMarkdown_UserSample(t *testing.T) {
	input := "1. 看项目\n\n    • 看目录里有哪些文件和子目录\n\n    • 打开某个文件看内容\n\n    • 在整个项目里搜索某个关键字或正则"
	rendered := renderMarkdownToANSI(input, 80)
	assert.NotEmpty(t, rendered)
}

func hasListBlankGap(lines []string) bool {
	for i := 1; i < len(lines)-1; i++ {
		if strings.TrimSpace(lines[i]) != "" {
			continue
		}
		if isListLine(lines[i-1]) && isListLine(lines[i+1]) {
			return true
		}
	}
	return false
}
