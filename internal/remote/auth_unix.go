//go:build !windows

package remote

import (
	"net"
	"time"
)

func defaultAgentSock() string { return "" }

func dialAgentConn(sock string) (net.Conn, error) {
	// Match the Windows agent dial: a wedged ssh-agent (full backlog,
	// unresponsive daemon) must not pin Connect until the caller cancels.
	return net.DialTimeout("unix", sock, 2*time.Second)
}
