package tools

// PatchSafetyLevel 表示补丁的安全级别。
type PatchSafetyLevel int

const (
	PatchSafe PatchSafetyLevel = iota
	PatchAskUser
	PatchReject
)

// PatchSafetyDecision 封装一次补丁安全评估的结果。
type PatchSafetyDecision struct {
	Level  PatchSafetyLevel
	Reason string   // AskUser 或 Reject 时给出的原因
	Paths  []string // 涉及到的文件路径摘要，便于在 CLI 中展示
}

// EvaluatePatchSafety 针对 apply_patch 补丁格式做安全评估。
func EvaluatePatchSafety(summary PatchSummary) PatchSafetyDecision {
	paths := summary.Paths
	if len(paths) == 0 {
		paths = append(paths, summary.Added...)
		paths = append(paths, summary.Modified...)
		paths = append(paths, summary.Deleted...)
	}
	if summary.HasDeletes() {
		return PatchSafetyDecision{
			Level:  PatchAskUser,
			Reason: "补丁包含文件删除操作，建议人工确认",
			Paths:  paths,
		}
	}
	return PatchSafetyDecision{
		Level: PatchSafe,
		Paths: paths,
	}
}
