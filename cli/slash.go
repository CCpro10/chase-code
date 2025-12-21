package cli

import (
	"fmt"
	"strings"

	"chase-code/agent"
)

// handleSlashCommand 处理以 "/" 开头的命令，例如 /approvals。
// 这些命令主要用于运行时调整会话配置等高阶操作。
func handleSlashCommand(line string) ([]string, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil, nil
	}

	cmd := strings.TrimPrefix(fields[0], "/")
	switch cmd {
	case "approvals":
		return handleApprovalsCommand(fields[1:])
	default:
		return nil, fmt.Errorf("未知 / 命令: %s", cmd)
	}
}

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

	var mode agent.ApprovalMode
	switch strings.ToLower(args[0]) {
	case "auto":
		mode = agent.ApprovalModeAuto
	case "ask", "always_ask":
		mode = agent.ApprovalModeAlwaysAsk
	case "approve", "always_approve":
		mode = agent.ApprovalModeAlwaysApprove
	default:
		return nil, fmt.Errorf("未知审批模式: %s（可选: auto|ask|approve）", args[0])
	}

	sess.session.Config.ToolApproval.ApplyPatch = mode
	return []string{fmt.Sprintf("已将 apply_patch 审批模式切换为: %s", mode)}, nil
}
