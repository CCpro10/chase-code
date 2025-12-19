package agent

import "os"

// ApprovalMode 控制工具相关操作（目前主要是 apply_patch）的审批行为。
// 借鉴 codex-rs 中 SessionConfiguration 的思想，这里提供三种模式：
//   - ApprovalModeAuto: 默认模式，根据安全评估结果决定是否需要人工审批；
//   - ApprovalModeAlwaysAsk: 所有补丁在执行前都需要人工审批；
//   - ApprovalModeAlwaysApprove: 对于需要人工审批的补丁直接自动批准（仍会拒绝明确不安全的补丁）。
type ApprovalMode string

const (
	ApprovalModeAuto          ApprovalMode = "auto"
	ApprovalModeAlwaysAsk     ApprovalMode = "always_ask"
	ApprovalModeAlwaysApprove ApprovalMode = "always_approve"
)

// ToolApprovalConfig 描述各类工具的审批策略，目前只覆盖 apply_patch。
type ToolApprovalConfig struct {
	ApplyPatch ApprovalMode
}

// SessionConfig 对应一次会话的整体配置。
type SessionConfig struct {
	ToolApproval ToolApprovalConfig
}

// DefaultSessionConfigFromEnv 从环境变量构造默认的 SessionConfig。
// 当前支持：
//   - CHASE_CODE_APPLY_PATCH_APPROVAL: auto|always_ask|always_approve
func DefaultSessionConfigFromEnv() SessionConfig {
	modeStr := os.Getenv("CHASE_CODE_APPLY_PATCH_APPROVAL")
	mode := ApprovalModeAuto
	switch ApprovalMode(modeStr) {
	case ApprovalModeAlwaysAsk, ApprovalModeAlwaysApprove:
		mode = ApprovalMode(modeStr)
	case ApprovalModeAuto, "":
		mode = ApprovalModeAuto
	default:
		mode = ApprovalModeAuto
	}

	return SessionConfig{
		ToolApproval: ToolApprovalConfig{
			ApplyPatch: mode,
		},
	}
}
