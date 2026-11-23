package update

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// requireSecureURL 要求更新元数据与产物使用 HTTPS。
// 仅 loopback（测试用 httptest）允许 http。
// requireSecureURL demands HTTPS for update metadata and artifacts.
// Loopback http is allowed for local tests (httptest).
func requireSecureURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("update url is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse update url: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("update url must use https (got http://%s)", u.Host)
	default:
		return fmt.Errorf("unsupported update url scheme %q", u.Scheme)
	}
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
