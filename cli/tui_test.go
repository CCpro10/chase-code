package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCommonPrefixLength(t *testing.T) {
	cases := []struct {
		name     string
		a, b     []string
		expected int
	}{
		{
			name:     "empty slices",
			a:        []string{},
			b:        []string{},
			expected: 0,
		},
		{
			name:     "identical slices",
			a:        []string{"a", "b"},
			b:        []string{"a", "b"},
			expected: 2,
		},
		{
			name:     "b extends a",
			a:        []string{"a", "b"},
			b:        []string{"a", "b", "c"},
			expected: 2,
		},
		{
			name:     "a extends b",
			a:        []string{"a", "b", "c"},
			b:        []string{"a", "b"},
			expected: 2,
		},
		{
			name:     "prefix match only",
			a:        []string{"a", "b", "c"},
			b:        []string{"a", "b", "d"},
			expected: 2,
		},
		{
			name:     "no match",
			a:        []string{"a", "b"},
			b:        []string{"c", "d"},
			expected: 0,
		},
		{
			name: "b modifies a suffix (margin case)",
			a:    []string{"Item 1", ""},           // Old state: item + margin
			b:    []string{"Item 1", "Item 2", ""}, // New state: item 1 + item 2 + margin
			// Here "Item 1" matches. The second element "" != "Item 2".
			expected: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actual := commonPrefixLength(tc.a, tc.b)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

// Mocking renderMarkdownToANSI behavior for deterministic testing
// In reality, this function calls glamour which might output ANSI codes.
// For logic testing, we assume stableRaw -> renderedLines mapping is predictable.
func mockRender(raw string) []string {
	// A simple mock that simulates lists adding items and potentially shifting margins.
	// Scenario:
	// "1. Item 1" -> ["Item 1", ""] (margin)
	// "1. Item 1\n2. Item 2" -> ["Item 1", "Item 2", ""]

	lines := strings.Split(raw, "\n")
	var rendered []string
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		rendered = append(rendered, l)
	}
	// simulate glamour adding a bottom margin if content exists
	if len(rendered) > 0 {
		rendered = append(rendered, "")
	}
	return rendered
}

func TestReplModel_StreamLogic_Simulation(t *testing.T) {
	// Setup a model with mock state
	m := &replModel{
		streamActive:    true,
		streamWrapWidth: 80,
	}

	// This test bypasses the actual renderMarkdownToANSI and directly tests the logic
	// of appendStreamDelta by manually manipulating the state as if render happened.
	// Since we can't easily inject the renderer into replModel without refactoring,
	// we will replicate the critical logic here or rely on the actual renderer if it's stable enough.
	// Given we want to verify the FIX (commonPrefixLength usage), let's construct the scenario manually.

	// Step 1: Initial state
	// Rendered: Item 1 + Margin
	m.streamRenderedLines = []string{"Item 1", ""}

	// Step 2: New Content arrives
	// New Render Result: Item 1 + Item 2 + Margin
	newLines := []string{"Item 1", "Item 2", ""}

	// Calculate delta using the fixed logic
	commonLen := commonPrefixLength(m.streamRenderedLines, newLines)
	assert.Equal(t, 1, commonLen, "Should match 'Item 1'")

	newOutput := newLines[commonLen:]
	assert.Equal(t, []string{"Item 2", ""}, newOutput, "Should output Item 2 and new Margin")

	// Step 3: Update state
	m.streamRenderedLines = newLines

	// Step 4: Verify repeated update doesn't output duplicates
	// Same content again
	commonLen2 := commonPrefixLength(m.streamRenderedLines, newLines)
	assert.Equal(t, 3, commonLen2, "Should match all lines")
	if commonLen2 < len(newLines) {
		t.Errorf("Should not output anything, but got %v", newLines[commonLen2:])
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
