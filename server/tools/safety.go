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

// EvaluateSimplePatchSafety 针对旧版基于字符串替换的 apply_patch 模式，
// 做一个保守的安全评估：
//   - 默认视为安全；
//   - 若 replaceAll 且 to 为空（批量删除），则要求用户确认；
//   - 后续可以在这里增加更多规则（路径白名单、敏感目录、文件类型等）。
func EvaluateSimplePatchSafety(filePath, from, to string, replaceAll bool) PatchSafetyDecision {
	// 批量删除：from 非空，to 为空，且 replaceAll=true 时，认为风险较大，需要用户确认。
	if replaceAll && from != "" && to == "" {
		return PatchSafetyDecision{
			Level:  PatchAskUser,
			Reason: "补丁将删除文件中多处内容，建议人工确认",
			Paths:  []string{filePath},
		}
	}

	// TODO: 在此扩展更多规则，例如：
	//   - 限制只能在当前工作目录下修改文件；
	//   - 针对 .git、.ssh 等敏感路径直接 Reject；
	//   - 针对过多文件/过大修改量触发 AskUser。

	return PatchSafetyDecision{
		Level: PatchSafe,
		Paths: []string{filePath},
	}
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
