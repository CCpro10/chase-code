package llm

import (
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"chase-code/config"
)

// NewLLMClient 每次被调用时都会初始化一个新的 LLMClient 实例。
//
// 为了便于调试不同对话会话（Session）的行为，这里仿照 codex 的做法，
// 为每次会话生成一个简单的 SessionID，并将日志输出到按 SessionID 区分的
// 独立日志文件中：
//   - SessionID 由当前日期(YYYYMMDD)、时间(HHMMSS)和 4 位随机数组成；
//   - 日志文件路径形如：
//     $CWD/.chase-code/logs/chase-code-<SessionID>.log
//   - 如需覆盖默认路径，可通过环境变量 CHASE_CODE_LOG_FILE 指定完整文件名。
//
// 注意：这里仍然使用标准库 log 作为输出后端，log.SetOutput 是进程级别的，
// chase-code 默认在单会话模式下运行，因此该行为是可以接受的。
func NewLLMClient(model *LLMModel) (LLMClient, error) {
	if model == nil {
		return nil, errors.New("LLMModel 为空")
	}
	if model.Client == nil {
		return nil, errors.New("LLMModel 未绑定 Client")
	}
	initLLMLogger()
	return model.Client, nil
}

// initLLMLogger 初始化日志输出位置。
func initLLMLogger() {
	path := resolveLogFilePath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("[llm] 创建日志目录失败: %v", err)
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("[llm] 打开日志文件失败: %v", err)
		return
	}
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)
	log.Printf("[llm] 使用日志文件: %s", path)
}

// resolveLogFilePath 计算日志输出路径。
func resolveLogFilePath() string {
	path := config.Get().LogFile
	if path != "" {
		return path
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Join(cwd, ".chase-code", "logs", fmt.Sprintf("chase-code-%s.log", newSessionID()))
}

// newSessionID 生成用于日志文件的会话标识。
func newSessionID() string {
	now := time.Now()
	datePart := now.Format("20060102-150405")
	rnd := rand.New(rand.NewSource(now.UnixNano()))
	randPart := rnd.Intn(10000)
	return fmt.Sprintf("%s-%04d", datePart, randPart)
}
