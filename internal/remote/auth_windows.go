//go:build windows

package remote

import (
	"net"
	"os"
	"strings"
	"syscall"
	"time"
)

// Windows OpenSSH's ssh-agent listens on this named pipe and does not set
// SSH_AUTH_SOCK, so ssh.exe finds it without an env var. Match that.
const windowsOpenSSHAgentPipe = `\\.\pipe\openssh-ssh-agent`

func defaultAgentSock() string { return windowsOpenSSHAgentPipe }

func dialAgentConn(sock string) (net.Conn, error) {
	if c, err := net.DialTimeout("unix", sock, 2*time.Second); err == nil {
		return c, nil
	}
	pipe := sock
	if !isWindowsNamedPipe(sock) {
		pipe = windowsOpenSSHAgentPipe
	}
	return dialNamedPipe(pipe)
}

func isWindowsNamedPipe(p string) bool {
	p = strings.ReplaceAll(p, `/`, `\`)
	return strings.HasPrefix(strings.ToLower(p), `\\.\pipe\`)
}

func dialNamedPipe(path string) (net.Conn, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(name,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return &pipeConn{File: os.NewFile(uintptr(h), path)}, nil
}

type pipeConn struct{ *os.File }

func (c *pipeConn) LocalAddr() net.Addr                { return pipeAddr(c.Name()) }
func (c *pipeConn) RemoteAddr() net.Addr               { return pipeAddr(c.Name()) }
func (c *pipeConn) SetDeadline(t time.Time) error      { return c.File.SetDeadline(t) }
func (c *pipeConn) SetReadDeadline(t time.Time) error  { return c.File.SetReadDeadline(t) }
func (c *pipeConn) SetWriteDeadline(t time.Time) error { return c.File.SetWriteDeadline(t) }

type pipeAddr string

func (pipeAddr) Network() string  { return "pipe" }
func (a pipeAddr) String() string { return string(a) }
