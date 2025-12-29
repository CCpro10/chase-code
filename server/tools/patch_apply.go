package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ApplyPatchResult describes apply_patch execution results.
type ApplyPatchResult struct {
	Summary PatchSummary
	Patch   string
}

// ApplyPatchText parses and applies patch text.
func ApplyPatchText(baseDir string, patchText string) (ApplyPatchResult, error) {
	patch, err := ParsePatch(patchText)
	if err != nil {
		return ApplyPatchResult{}, err
	}
	return ApplyPatch(baseDir, patch)
}

// ApplyPatch applies a parsed patch to the filesystem.
func ApplyPatch(baseDir string, patch Patch) (ApplyPatchResult, error) {
	if len(patch.Hunks) == 0 {
		return ApplyPatchResult{}, fmt.Errorf("补丁未包含任何文件变更")
	}
	if strings.TrimSpace(baseDir) == "" {
		return ApplyPatchResult{}, fmt.Errorf("工作目录为空")
	}

	for _, hunk := range patch.Hunks {
		switch hunk.Kind {
		case PatchHunkAdd:
			if err := applyAddFile(baseDir, hunk); err != nil {
				return ApplyPatchResult{}, err
			}
		case PatchHunkDelete:
			if err := applyDeleteFile(baseDir, hunk); err != nil {
				return ApplyPatchResult{}, err
			}
		case PatchHunkUpdate:
			if err := applyUpdateFile(baseDir, hunk); err != nil {
				return ApplyPatchResult{}, err
			}
		default:
			return ApplyPatchResult{}, fmt.Errorf("未知补丁类型")
		}
	}

	summary := SummarizePatch(patch)
	return ApplyPatchResult{Summary: summary, Patch: patch.Raw}, nil
}

// applyAddFile creates a new file and writes contents.
func applyAddFile(baseDir string, hunk PatchHunk) error {
	absPath, err := resolvePatchPath(baseDir, hunk.Path)
	if err != nil {
		return err
	}
	if err := ensureParentDir(absPath); err != nil {
		return err
	}
	contents := strings.Join(hunk.AddLines, "\n")
	if contents != "" && !strings.HasSuffix(contents, "\n") {
		contents += "\n"
	}
	if err := os.WriteFile(absPath, []byte(contents), 0o644); err != nil {
		return fmt.Errorf("写入文件失败 %s: %w", absPath, err)
	}
	return nil
}

// applyDeleteFile deletes the target file.
func applyDeleteFile(baseDir string, hunk PatchHunk) error {
	absPath, err := resolvePatchPath(baseDir, hunk.Path)
	if err != nil {
		return err
	}
	if err := os.Remove(absPath); err != nil {
		return fmt.Errorf("删除文件失败 %s: %w", absPath, err)
	}
	return nil
}

// applyUpdateFile updates a file with chunks.
func applyUpdateFile(baseDir string, hunk PatchHunk) error {
	absPath, err := resolvePatchPath(baseDir, hunk.Path)
	if err != nil {
		return err
	}

	originalLines, mode, err := readFileLines(absPath)
	if err != nil {
		return err
	}

	newLines, err := deriveNewLines(originalLines, hunk.Chunks, absPath)
	if err != nil {
		return err
	}

	contents := strings.Join(newLines, "\n")
	if hunk.HasMove {
		destPath, err := resolvePatchPath(baseDir, hunk.MoveTo)
		if err != nil {
			return err
		}
		if destPath == absPath {
			if err := os.WriteFile(absPath, []byte(contents), mode); err != nil {
				return fmt.Errorf("写入文件失败 %s: %w", absPath, err)
			}
			return nil
		}
		if err := ensureParentDir(destPath); err != nil {
			return err
		}
		if err := os.WriteFile(destPath, []byte(contents), mode); err != nil {
			return fmt.Errorf("写入文件失败 %s: %w", destPath, err)
		}
		if err := os.Remove(absPath); err != nil {
			return fmt.Errorf("删除原文件失败 %s: %w", absPath, err)
		}
		return nil
	}

	if err := os.WriteFile(absPath, []byte(contents), mode); err != nil {
		return fmt.Errorf("写入文件失败 %s: %w", absPath, err)
	}
	return nil
}

// resolvePatchPath validates and resolves a patch path.
func resolvePatchPath(baseDir string, relPath string) (string, error) {
	clean := strings.TrimSpace(relPath)
	if clean == "" {
		return "", fmt.Errorf("补丁路径为空")
	}
	if filepath.IsAbs(clean) {
		return "", fmt.Errorf("补丁路径必须是相对路径: %s", clean)
	}
	clean = filepath.Clean(clean)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("补丁路径非法: %s", relPath)
	}
	return filepath.Join(baseDir, clean), nil
}

// ensureParentDir ensures the parent directory exists.
func ensureParentDir(path string) error {
	parent := filepath.Dir(path)
	if parent == "." || parent == "" {
		return nil
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("创建目录失败 %s: %w", parent, err)
	}
	return nil
}

// readFileLines reads file contents and returns lines and mode.
func readFileLines(path string) ([]string, os.FileMode, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, fmt.Errorf("读取文件信息失败 %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("目标不是普通文件: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("读取文件失败 %s: %w", path, err)
	}
	content := string(data)
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, info.Mode().Perm(), nil
}

// deriveNewLines computes new file lines from chunks.
func deriveNewLines(original []string, chunks []PatchChunk, path string) ([]string, error) {
	replacements, err := computeReplacements(original, chunks, path)
	if err != nil {
		return nil, err
	}
	newLines, err := applyReplacements(original, replacements)
	if err != nil {
		return nil, err
	}
	if len(newLines) == 0 || newLines[len(newLines)-1] != "" {
		newLines = append(newLines, "")
	}
	return newLines, nil
}

type replacement struct {
	Start    int
	OldLen   int
	NewLines []string
}

// computeReplacements computes replacement ranges.
func computeReplacements(original []string, chunks []PatchChunk, path string) ([]replacement, error) {
	var replacements []replacement
	lineIndex := 0

	for _, chunk := range chunks {
		if chunk.HasContext {
			ctx := []string{chunk.ChangeContext}
			found := seekSequence(original, ctx, lineIndex, false)
			if found < 0 {
				return nil, fmt.Errorf("未找到上下文 %q: %s", chunk.ChangeContext, path)
			}
			lineIndex = found + 1
		}

		if len(chunk.OldLines) == 0 {
			insertion := len(original)
			if len(original) > 0 && original[len(original)-1] == "" {
				insertion = len(original) - 1
			}
			replacements = append(replacements, replacement{Start: insertion, OldLen: 0, NewLines: chunk.NewLines})
			continue
		}

		pattern := chunk.OldLines
		newLines := chunk.NewLines
		found := seekSequence(original, pattern, lineIndex, chunk.EndOfFile)
		if found < 0 && len(pattern) > 0 && pattern[len(pattern)-1] == "" {
			pattern = pattern[:len(pattern)-1]
			if len(newLines) > 0 && newLines[len(newLines)-1] == "" {
				newLines = newLines[:len(newLines)-1]
			}
			found = seekSequence(original, pattern, lineIndex, chunk.EndOfFile)
		}
		if found < 0 {
			return nil, fmt.Errorf("未找到预期变更块: %s", path)
		}
		replacements = append(replacements, replacement{Start: found, OldLen: len(pattern), NewLines: newLines})
		lineIndex = found + len(pattern)
	}

	sort.Slice(replacements, func(i, j int) bool {
		return replacements[i].Start < replacements[j].Start
	})
	return replacements, nil
}

// applyReplacements applies replacements to original lines.
func applyReplacements(original []string, replacements []replacement) ([]string, error) {
	out := make([]string, 0, len(original))
	idx := 0
	for _, repl := range replacements {
		if repl.Start < idx {
			return nil, fmt.Errorf("补丁块存在重叠")
		}
		if repl.Start > len(original) {
			return nil, fmt.Errorf("补丁块超出文件范围")
		}
		out = append(out, original[idx:repl.Start]...)
		out = append(out, repl.NewLines...)
		idx = repl.Start + repl.OldLen
	}
	out = append(out, original[idx:]...)
	return out, nil
}
