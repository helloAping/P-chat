package serverproc

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

const (
	// PreferredPortStart is the stable local port used first by P-Chat.
	PreferredPortStart = 15150
	// PreferredPortEnd is the inclusive end of the fallback range.
	PreferredPortEnd = 15159
)

// Listen 只绑定一次 server 地址并返回已持有的 listener。PCHAT_PORT（含 0）
// 优先，其次使用 PCHAT_PORT_RANGE，否则严格绑定配置端口。
// Listen binds the server address exactly once and returns the owned listener.
func Listen(host string, configuredPort int) (net.Listener, error) {
	if raw, ok := os.LookupEnv("PCHAT_PORT"); ok && strings.TrimSpace(raw) != "" {
		port, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || port < 0 || port > 65535 {
			return nil, fmt.Errorf("invalid PCHAT_PORT %q", raw)
		}
		return listenPort(host, port)
	}
	if raw := strings.TrimSpace(os.Getenv("PCHAT_PORT_RANGE")); raw != "" {
		start, end, err := parsePortRange(raw)
		if err != nil {
			return nil, err
		}
		for port := start; port <= end; port++ {
			listener, listenErr := listenPort(host, port)
			if listenErr == nil {
				return listener, nil
			}
		}
		return listenPort(host, 0)
	}
	return listenPort(host, configuredPort)
}

func listenPort(host string, port int) (net.Listener, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("listen on %s:%d: %w", host, port, err)
	}
	return listener, nil
}

func parsePortRange(raw string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(raw), "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid PCHAT_PORT_RANGE %q; want start-end", raw)
	}
	start, startErr := strconv.Atoi(strings.TrimSpace(parts[0]))
	end, endErr := strconv.Atoi(strings.TrimSpace(parts[1]))
	if startErr != nil || endErr != nil || start < 1 || end > 65535 || start > end {
		return 0, 0, fmt.Errorf("invalid PCHAT_PORT_RANGE %q; want ports 1-65535", raw)
	}
	return start, end, nil
}
