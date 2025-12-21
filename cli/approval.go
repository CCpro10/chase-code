package cli

import (
	"fmt"
	"time"

	"chase-code/agent"
)

// sendApproval 将审批结果写入当前 agent Session 的审批通道，并返回提示信息。
func sendApproval(reqID string, approved bool) (string, error) {
	sess, err := getOrInitReplAgent()
	if err != nil {
		return "", err
	}

	ch := sess.session.ApprovalsChan()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case ch <- agent.ApprovalDecision{RequestID: reqID, Approved: approved}:
		if approved {
			return fmt.Sprintf("已批准补丁请求: %s", reqID), nil
		}
		return fmt.Sprintf("已拒绝补丁请求: %s", reqID), nil
	case <-timer.C:
		return "", fmt.Errorf("审批通道暂不可用，请稍后重试")
	}
}
