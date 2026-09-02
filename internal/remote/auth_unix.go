//go:build !windows

package remote

import "net"

func defaultAgentSock() string { return "" }

func dialAgentConn(sock string) (net.Conn, error) {
	return net.Dial("unix", sock)
}
