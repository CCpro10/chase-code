package utils

import (
	"encoding/json"
)

// ToIndentJSONString 将任意结构体转换为格式化（缩进、美化）的 JSON 字符串，便于日志阅读。
//
// 注意：为了使用方便，此函数会在发生错误时返回空字符串，而不是向外传播错误。
func ToIndentJSONString(v any) string {
	if v == nil {
		return "" // 保持调用方简单语义
	}
	// 使用 MarshalIndent 生成带缩进、换行的 JSON，提升可读性
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "" // 避免因为日志序列化失败影响主流程
	}
	return string(b)
}
