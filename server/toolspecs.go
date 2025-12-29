package server

import (
	"strings"

	"chase-code/server/llm"
	servertools "chase-code/server/tools"
)

// BuildToolSpecsForModel 根据模型能力选择合适的工具集合。
func BuildToolSpecsForModel(model *llm.LLMModel) []servertools.ToolSpec {
	mode := resolveApplyPatchToolMode(model)
	return servertools.ToolSpecsWithApplyPatchMode(mode)
}

// resolveApplyPatchToolMode 为 apply_patch 选择工具形态。
func resolveApplyPatchToolMode(model *llm.LLMModel) servertools.ApplyPatchToolMode {
	if model == nil {
		return servertools.ApplyPatchToolModeFunction
	}
	switch model.Client.(type) {
	case *llm.ResponsesClient:
		return servertools.ApplyPatchToolModeCustom
	default:
		modelName := strings.ToLower(strings.TrimSpace(model.Model))
		if strings.Contains(modelName, "gpt-oss") {
			return servertools.ApplyPatchToolModeFunction
		}
		return servertools.ApplyPatchToolModeFunction
	}
}
