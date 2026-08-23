// Package remote attaches tokentop to inference engines on other hosts over
// SSH. Discovery runs a tiny POSIX port probe remotely; traffic is tunneled
// through one long-lived `ssh -N` with local forwards; load and memory come
// from periodic /proc reads. No agent is installed on the remote side.
package remote

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Target struct {
	User string
	Host string
	Port int // 22 when unset
}

// ParseTarget parses ssh://[user@]host[:port].
func ParseTarget(raw string) (Target, error) {
	if !strings.HasPrefix(raw, "ssh://") {
		return Target{}, fmt.Errorf("target %q must start with ssh://", raw)
	}
	u, err := parseURL(raw)
	if err != nil {
		return Target{}, fmt.Errorf("bad ssh target: %w", err)
	}
	t := Target{Host: u.Hostname()}
	if u.User != nil {
		t.User = u.User.Username()
	}
	if p := u.Port(); p != "" {
		t.Port, err = strconv.Atoi(p)
		if err != nil || t.Port <= 0 || t.Port > 65535 {
			return Target{}, fmt.Errorf("bad ssh port %q", p)
		}
	} else {
		t.Port = 22
	}
	if t.Host == "" {
		return Target{}, fmt.Errorf("ssh target missing host")
	}
	return t, nil
}

func (t Target) userHost() string {
	if t.User == "" {
		return t.Host
	}
	return t.User + "@" + t.Host
}

func (t Target) sshArgs(extra ...string) []string {
	args := []string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=6",
		"-o", "BatchMode=yes",
	}
	if t.Port != 22 {
		args = append(args, "-p", strconv.Itoa(t.Port))
	}
	return append(append(args, t.userHost()), extra...)
}

// ProbeScript prints listening ports from our candidate list. Uses bash
// /dev/tcp first, falling back to nc.
func probeScript(ports []int) string {
	var b strings.Builder
	b.WriteString("for p in")
	for _, p := range ports {
		b.WriteString(" " + strconv.Itoa(p))
	}
	b.WriteString(`; do
  if (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then echo "$p"
  elif command -v nc >/dev/null 2>&1 && nc -z -w1 127.0.0.1 "$p" >/dev/null 2>&1; then echo "$p"
  fi
done`)
	return b.String()
}

// DiscoverPorts returns candidate ports that are listening on the target.
func DiscoverPorts(ctx context.Context, t Target, ports []int) ([]int, error) {
	c, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	out, err := exec.CommandContext(c, "ssh", t.sshArgs("--", probeScript(ports))...).Output()
	if err != nil {
		return nil, fmt.Errorf("ssh %s: %w", t.userHost(), err)
	}
	var found []int
	for _, line := range strings.Fields(string(out)) {
		if p, err := strconv.Atoi(line); err == nil && p > 0 {
			found = append(found, p)
		}
	}
	return found, nil
}

// Tunnel holds one ssh process carrying local port forwards.
type Tunnel struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	// Local maps remote port -> bound local port.
	Local map[int]int
}

// StartTunnel opens an ssh connection forwarding each remote port to a free
// local one. ExitOnForwardFailure stays off so a single busy local port does
// not tear down the whole session.
func StartTunnel(ctx context.Context, t Target, rports []int) (*Tunnel, error) {
	cctx, cancel := context.WithCancel(ctx)
	tun := &Tunnel{cancel: cancel, Local: map[int]int{}}
	args := []string{
		"-N",
		"-o", "ExitOnForwardFailure=no",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if t.Port != 22 {
		args = append(args, "-p", strconv.Itoa(t.Port))
	}
	for _, rp := range rports {
		lport := freeLocalPort()
		tun.Local[rp] = lport
		args = append(args, "-L",
			fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", lport, rp))
	}
	args = append(args, t.userHost())
	cmd := exec.CommandContext(cctx, "ssh", args...)
	cmd.Stdout = ioDiscard
	cmd.Stderr = ioDiscard
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("ssh tunnel to %s: %w", t.userHost(), err)
	}
	tun.cmd = cmd
	go func() {
		<-cctx.Done()
		if tun.cmd.Process != nil {
			tun.cmd.Process.Kill()
		}
	}()
	return tun, nil
}

func (t *Tunnel) Close() { t.cancel() }

func freeLocalPort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
