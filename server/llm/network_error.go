package llm

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"syscall"
)

// NetworkError 用于标记与网络连接相关的错误。
type NetworkError struct {
	Err error
}

// Error 返回错误信息。
func (e NetworkError) Error() string {
	return e.Err.Error()
}

// Unwrap 便于 errors.Is / errors.As 继续向下匹配。
func (e NetworkError) Unwrap() error {
	return e.Err
}

// IsNetworkError 判断错误是否为网络层问题。
func IsNetworkError(err error) bool {
	if err == nil {
		return false
	}
	var marker NetworkError
	if errors.As(err, &marker) {
		return true
	}
	return isNetworkError(err)
}

// wrapNetworkError 将网络错误包装为 NetworkError，便于上层识别。
func wrapNetworkError(err error) error {
	if err == nil {
		return nil
	}
	if isNetworkError(err) {
		return NetworkError{Err: err}
	}
	return err
}

func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ETIMEDOUT) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}

	lower := strings.ToLower(err.Error())
	indicators := []string{
		"no such host",
		"temporary failure in name resolution",
		"connection refused",
		"connection reset",
		"network is unreachable",
		"i/o timeout",
		"tls handshake timeout",
		"broken pipe",
	}
	for _, indicator := range indicators {
		if strings.Contains(lower, indicator) {
			return true
		}
	}
	return false
}
