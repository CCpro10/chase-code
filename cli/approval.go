package cli

import (
	"fmt"
	"os"

	"chase-code/agent"
)

// sendApproval 将用户在 repl 中输入的 :approve/:reject 命令转换为审批结果，
// 写入当前 agent Session 的审批通道。
func sendApproval(reqID string, approved bool) error {
	sess, err := getOrInitReplAgent()
	if err != nil {
		return err
	}

	ch := sess.session.ApprovalsChan()
	select {
	case ch <- agent.ApprovalDecision{RequestID: reqID, Approved: approved}:
		if approved {
			fmt.Fprintf(os.Stderr, "已批准补丁请求: %s\n", reqID)
		} else {
			fmt.Fprintf(os.Stderr, "已拒绝补丁请求: %s\n", reqID)
		}
		return nil
	default:
		return fmt.Errorf("审批通道暂不可用，请稍后重试")
	}
}
