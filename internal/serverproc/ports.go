package serverproc

import (
	"fmt"
	"net"
)

const (
	// PreferredPortStart is the stable local port used first by P-Chat.
	PreferredPortStart = 15150
	// PreferredPortEnd is the inclusive end of the fallback range.
	PreferredPortEnd = 15159
)

// PickPreferredPort returns the first available loopback TCP port in
// P-Chat's preferred range. If the range is full, it asks the OS for
// any free ephemeral port as a last-resort fallback.
func PickPreferredPort() (int, error) {
	for port := PreferredPortStart; port <= PreferredPortEnd; port++ {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			_ = l.Close()
			return port, nil
		}
	}
	return pickFreePort()
}

func pickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
