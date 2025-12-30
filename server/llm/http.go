package llm

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

// marshalRequestBody 将请求体序列化并生成日志用的美化 JSON。
func marshalRequestBody(req any) ([]byte, string, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, "", err
	}
	return data, formatJSONForLog(data), nil
}

// formatJSONForLog 将 JSON 数据格式化为可读字符串。
func formatJSONForLog(data []byte) string {
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err != nil {
		return string(data)
	}
	return pretty.String()
}

// logRequest 记录发送请求的摘要。
func logRequest(cfg clientConfig, url string, data []byte, pretty string) {
	alias := strings.TrimSpace(cfg.Alias)
	if alias == "" {
		alias = "(empty)"
	}
	log.Printf("[llm] request alias=%s model=%s url=%s body_bytes=%d body=\n%s", alias, cfg.Model, url, len(data), pretty)
}

// logRawResponse 打印原始响应体，便于排查问题。
func logRawResponse(status int, respBody []byte) {
	log.Printf("[llm] raw response status=%d body_bytes=%d body=\n%s", status, len(respBody), string(respBody))
}

// newHTTPClient 构造带超时的 HTTP 客户端。
func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}
