package cli

import (
	"fmt"
	"strings"

	"chase-code/server/config"
)

// handleApprovalsCommand 实现 /approvals 命令：
//   - /approvals           显示当前 apply_patch 审批模式；
//   - /approvals auto      设置为自动模式；
//   - /approvals ask       设置为总是人工审批；
//   - /approvals approve   设置为自动批准需要审批的补丁。
func handleApprovalsCommand(args []string) ([]string, error) {
	sess, err := getOrInitReplAgent()
	if err != nil {
		return nil, err
	}

	current := sess.session.Config.ToolApproval.ApplyPatch
	if len(args) == 0 {
		return []string{
			fmt.Sprintf("当前 apply_patch 审批模式: %s", current),
			"可选值: auto | ask | approve",
		}, nil
	}

	var mode config.ApprovalMode
	switch strings.ToLower(args[0]) {
	case "auto":
		mode = config.ApprovalModeAuto
	case "ask", "always_ask":
		mode = config.ApprovalModeAlwaysAsk
	case "approve", "always_approve":
		mode = config.ApprovalModeAlwaysApprove
	default:
		return nil, fmt.Errorf("未知审批模式: %s（可选: auto|ask|approve）", args[0])
	}

	sess.session.Config.ToolApproval.ApplyPatch = mode
	return []string{fmt.Sprintf("已将 apply_patch 审批模式切换为: %s", mode)}, nil
}
