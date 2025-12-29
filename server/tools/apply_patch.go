package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ApplyPatchRequest describes the parsed apply_patch request.
type ApplyPatchRequest struct {
	Patch   string
	Summary PatchSummary
}

// PatchSummary summarizes files touched by a patch.
type PatchSummary struct {
	Paths    []string
	Added    []string
	Modified []string
	Deleted  []string
}

// HasDeletes reports whether the patch deletes files.
func (s PatchSummary) HasDeletes() bool {
	return len(s.Deleted) > 0
}

// ParseApplyPatchArguments parses apply_patch arguments.
func ParseApplyPatchArguments(args json.RawMessage) (ApplyPatchRequest, error) {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 {
		return ApplyPatchRequest{}, fmt.Errorf("apply_patch 参数为空")
	}

	patchText, err := extractPatchText(trimmed)
	if err != nil {
		return ApplyPatchRequest{}, err
	}

	patch, err := ParsePatch(patchText)
	if err != nil {
		return ApplyPatchRequest{}, err
	}

	summary := SummarizePatch(patch)
	return ApplyPatchRequest{
		Patch:   patchText,
		Summary: summary,
	}, nil
}

// SummarizePatch builds a summary from a parsed patch.
func SummarizePatch(patch Patch) PatchSummary {
	summary := PatchSummary{}
	pathSeen := make(map[string]struct{})
	addedSeen := make(map[string]struct{})
	modifiedSeen := make(map[string]struct{})
	deletedSeen := make(map[string]struct{})

	addPath := func(path string) {
		if path == "" {
			return
		}
		if _, ok := pathSeen[path]; ok {
			return
		}
		pathSeen[path] = struct{}{}
		summary.Paths = append(summary.Paths, path)
	}

	for _, hunk := range patch.Hunks {
		switch hunk.Kind {
		case PatchHunkAdd:
			summary.Added = appendUnique(summary.Added, hunk.Path, addedSeen)
			addPath(hunk.Path)
		case PatchHunkDelete:
			summary.Deleted = appendUnique(summary.Deleted, hunk.Path, deletedSeen)
			addPath(hunk.Path)
		case PatchHunkUpdate:
			if hunk.HasMove {
				path := fmt.Sprintf("%s -> %s", hunk.Path, hunk.MoveTo)
				addPath(path)
				summary.Modified = appendUnique(summary.Modified, hunk.MoveTo, modifiedSeen)
			} else {
				addPath(hunk.Path)
				summary.Modified = appendUnique(summary.Modified, hunk.Path, modifiedSeen)
			}
		}
	}

	return summary
}

// extractPatchText extracts the raw patch text.
func extractPatchText(args []byte) (string, error) {
	if json.Valid(args) {
		var payload struct {
			Patch string `json:"patch"`
			Input string `json:"input"`
		}
		if err := json.Unmarshal(args, &payload); err == nil {
			if patch := strings.TrimSpace(payload.Input); patch != "" {
				return patch, nil
			}
			if patch := strings.TrimSpace(payload.Patch); patch != "" {
				return patch, nil
			}
		}

		var asString string
		if err := json.Unmarshal(args, &asString); err == nil {
			asString = strings.TrimSpace(asString)
			if asString == "" {
				return "", fmt.Errorf("apply_patch 参数为空")
			}
			if json.Valid([]byte(asString)) {
				return extractPatchText([]byte(asString))
			}
			return asString, nil
		}

		return "", fmt.Errorf("apply_patch 参数缺少 input/patch 字段")
	}

	patch := strings.TrimSpace(string(args))
	if patch == "" {
		return "", fmt.Errorf("apply_patch 参数为空")
	}
	return patch, nil
}

// appendUnique appends a value if not seen.
func appendUnique(list []string, value string, seen map[string]struct{}) []string {
	if value == "" {
		return list
	}
	if _, ok := seen[value]; ok {
		return list
	}
	seen[value] = struct{}{}
	return append(list, value)
}
