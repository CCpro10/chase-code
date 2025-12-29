package tools

import (
	"fmt"
	"strings"
)

const (
	patchBeginMarker   = "*** Begin Patch"
	patchEndMarker     = "*** End Patch"
	patchAddMarker     = "*** Add File: "
	patchDeleteMarker  = "*** Delete File: "
	patchUpdateMarker  = "*** Update File: "
	patchMoveMarker    = "*** Move to: "
	patchEOFMarker     = "*** End of File"
	patchContextMarker = "@@ "
	patchContextEmpty  = "@@"
)

// PatchHunkKind identifies the file operation type.
type PatchHunkKind int

const (
	PatchHunkAdd PatchHunkKind = iota
	PatchHunkDelete
	PatchHunkUpdate
)

// Patch is the parsed patch structure.
type Patch struct {
	Hunks []PatchHunk
	Raw   string
}

// PatchHunk describes a file-level patch operation.
type PatchHunk struct {
	Kind    PatchHunkKind
	Path    string
	MoveTo  string
	HasMove bool

	AddLines []string
	Chunks   []PatchChunk
}

// PatchChunk describes a change chunk within Update File.
type PatchChunk struct {
	ChangeContext string
	HasContext    bool
	OldLines      []string
	NewLines      []string
	EndOfFile     bool
}

// ParsePatch parses apply_patch text into a structured patch.
func ParsePatch(patchText string) (Patch, error) {
	normalized := strings.TrimSpace(patchText)
	if normalized == "" {
		return Patch{}, fmt.Errorf("补丁内容为空")
	}

	lines := splitPatchLines(normalized)
	if len(lines) < 2 {
		return Patch{}, fmt.Errorf("补丁格式不完整")
	}
	if strings.TrimSpace(lines[0]) != patchBeginMarker {
		return Patch{}, fmt.Errorf("补丁首行必须是 %q", patchBeginMarker)
	}
	if strings.TrimSpace(lines[len(lines)-1]) != patchEndMarker {
		return Patch{}, fmt.Errorf("补丁末行必须是 %q", patchEndMarker)
	}

	var hunks []PatchHunk
	lineNumber := 2
	for idx := 1; idx < len(lines)-1; {
		if strings.TrimSpace(lines[idx]) == "" {
			idx++
			lineNumber++
			continue
		}
		hunk, consumed, err := parseOneHunk(lines[idx:], lineNumber)
		if err != nil {
			return Patch{}, err
		}
		hunks = append(hunks, hunk)
		idx += consumed
		lineNumber += consumed
	}

	return Patch{Hunks: hunks, Raw: normalized}, nil
}

// parseOneHunk parses one hunk and returns consumed lines.
func parseOneHunk(lines []string, lineNumber int) (PatchHunk, int, error) {
	if len(lines) == 0 {
		return PatchHunk{}, 0, fmt.Errorf("补丁为空")
	}
	first := strings.TrimSpace(lines[0])
	switch {
	case strings.HasPrefix(first, patchAddMarker):
		path := strings.TrimSpace(strings.TrimPrefix(first, patchAddMarker))
		if path == "" {
			return PatchHunk{}, 0, patchParseError(lineNumber, "Add File 缺少路径")
		}
		parsed := 1
		addLines := make([]string, 0)
		for _, line := range lines[1:] {
			if strings.HasPrefix(line, "+") {
				addLines = append(addLines, line[1:])
				parsed++
				continue
			}
			break
		}
		if len(addLines) == 0 {
			return PatchHunk{}, 0, patchParseError(lineNumber, "Add File 需要至少一行内容")
		}
		return PatchHunk{Kind: PatchHunkAdd, Path: path, AddLines: addLines}, parsed, nil
	case strings.HasPrefix(first, patchDeleteMarker):
		path := strings.TrimSpace(strings.TrimPrefix(first, patchDeleteMarker))
		if path == "" {
			return PatchHunk{}, 0, patchParseError(lineNumber, "Delete File 缺少路径")
		}
		return PatchHunk{Kind: PatchHunkDelete, Path: path}, 1, nil
	case strings.HasPrefix(first, patchUpdateMarker):
		path := strings.TrimSpace(strings.TrimPrefix(first, patchUpdateMarker))
		if path == "" {
			return PatchHunk{}, 0, patchParseError(lineNumber, "Update File 缺少路径")
		}

		remaining := lines[1:]
		parsed := 1
		moveTo := ""
		hasMove := false
		if len(remaining) > 0 {
			moveLine := strings.TrimSpace(remaining[0])
			if strings.HasPrefix(moveLine, patchMoveMarker) {
				moveTo = strings.TrimSpace(strings.TrimPrefix(moveLine, patchMoveMarker))
				if moveTo == "" {
					return PatchHunk{}, 0, patchParseError(lineNumber+parsed, "Move to 缺少路径")
				}
				hasMove = true
				remaining = remaining[1:]
				parsed++
			}
		}

		chunks := make([]PatchChunk, 0)
		for len(remaining) > 0 {
			if strings.TrimSpace(remaining[0]) == "" {
				remaining = remaining[1:]
				parsed++
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(remaining[0]), "***") {
				break
			}
			chunk, consumed, err := parseUpdateChunk(remaining, lineNumber+parsed, len(chunks) == 0)
			if err != nil {
				return PatchHunk{}, 0, err
			}
			chunks = append(chunks, chunk)
			remaining = remaining[consumed:]
			parsed += consumed
		}
		if len(chunks) == 0 {
			return PatchHunk{}, 0, patchParseError(lineNumber, fmt.Sprintf("Update File %q 没有变更内容", path))
		}
		return PatchHunk{
			Kind:    PatchHunkUpdate,
			Path:    path,
			MoveTo:  moveTo,
			HasMove: hasMove,
			Chunks:  chunks,
		}, parsed, nil
	default:
		return PatchHunk{}, 0, patchParseError(lineNumber, fmt.Sprintf("未知补丁头: %q", first))
	}
}

// parseUpdateChunk parses a chunk inside Update File.
func parseUpdateChunk(lines []string, lineNumber int, allowMissingContext bool) (PatchChunk, int, error) {
	if len(lines) == 0 {
		return PatchChunk{}, 0, patchParseError(lineNumber, "Update 块为空")
	}

	chunk := PatchChunk{}
	startIndex := 0

	if lines[0] == patchContextEmpty {
		startIndex = 1
	} else if strings.HasPrefix(lines[0], patchContextMarker) {
		chunk.ChangeContext = strings.TrimPrefix(lines[0], patchContextMarker)
		chunk.HasContext = true
		startIndex = 1
	} else if !allowMissingContext {
		return PatchChunk{}, 0, patchParseError(lineNumber, fmt.Sprintf("Update 块需要 @@ 上下文，收到: %q", lines[0]))
	}

	if startIndex >= len(lines) {
		return PatchChunk{}, 0, patchParseError(lineNumber+1, "Update 块没有变更内容")
	}

	parsed := 0
	for _, line := range lines[startIndex:] {
		switch {
		case line == patchEOFMarker:
			if parsed == 0 {
				return PatchChunk{}, 0, patchParseError(lineNumber+1, "Update 块没有变更内容")
			}
			chunk.EndOfFile = true
			parsed++
			return chunk, parsed + startIndex, nil
		case line == "":
			chunk.OldLines = append(chunk.OldLines, "")
			chunk.NewLines = append(chunk.NewLines, "")
			parsed++
			continue
		}

		switch line[0] {
		case ' ':
			chunk.OldLines = append(chunk.OldLines, line[1:])
			chunk.NewLines = append(chunk.NewLines, line[1:])
			parsed++
		case '+':
			chunk.NewLines = append(chunk.NewLines, line[1:])
			parsed++
		case '-':
			chunk.OldLines = append(chunk.OldLines, line[1:])
			parsed++
		default:
			if parsed == 0 {
				return PatchChunk{}, 0, patchParseError(lineNumber+1, fmt.Sprintf("Update 块出现非法行: %q", line))
			}
			return chunk, parsed + startIndex, nil
		}
	}

	if parsed == 0 {
		return PatchChunk{}, 0, patchParseError(lineNumber+1, "Update 块没有变更内容")
	}
	return chunk, parsed + startIndex, nil
}

// splitPatchLines splits patch text and trims trailing \r.
func splitPatchLines(patch string) []string {
	lines := strings.Split(patch, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, "\r")
	}
	return lines
}

// patchParseError builds a parse error with line number.
func patchParseError(line int, message string) error {
	return fmt.Errorf("补丁解析失败(行 %d): %s", line, message)
}
