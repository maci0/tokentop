package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// runTimeout bounds one remote command (discovery or vitals poll).
const runTimeout = 15 * time.Second

// Client is one long-lived ssh connection carrying everything tokentop needs
// from a remote host: command sessions for discovery and vitals, plus direct
// TCP channels relayed onto local listeners for engine traffic. No ssh
// binary, no local port-forward races: Forward binds its listeners before
// returning, so backends can attach immediately.
type Client struct {
	Target Target

	conn    *ssh.Client
	closed  chan struct{}
	closeMu sync.Mutex // guards the close-once below
	errMu   sync.Mutex
	err     error

	mu        sync.Mutex
	listeners []net.Listener
}

// Done fires when the connection drops for any reason, including Close.
func (c *Client) Done() <-chan struct{} { return c.closed }

// Err reports why the connection dropped; nil while alive.
func (c *Client) Err() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

func (c *Client) setErr(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		err = nil // deliberate shutdown is not a loss
	}
	c.errMu.Lock()
	c.err = err
	c.errMu.Unlock()
}

var currentUser = func() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

// Connect establishes an authenticated connection to t. Credentials are tried
// in order: explicit key file, config/default keys, agent, then a password
// (TOKENTOP_SSH_PASSWORD first, else an interactive prompt when stdin is a
// TTY). Host keys are trust-on-first-use with change detection.
func Connect(ctx context.Context, t Target) (*Client, error) {
	methods, cleanup := t.authMethods()
	defer cleanup()

	pw := &passwordSource{}
	methods = append(methods, pw.authCallbacks(t)...)

	hk, err := tofu()
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            t.userOr(currentUser()),
		Auth:            methods,
		HostKeyCallback: hk,
		Timeout:         8 * time.Second,
	}

	addr := net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	cc, chans, reqs, err := ssh.NewClientConn(nc, addr, cfg)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("ssh %s: %w", t.userHost(), authHint(err))
	}
	c := &Client{Target: t, conn: ssh.NewClient(cc, chans, reqs), closed: make(chan struct{})}
	go c.watchClose()
	go c.keepalive()
	return c, nil
}

// userOr falls back to the given default when no explicit user was resolved.
func (t Target) userOr(def string) string {
	if t.User != "" {
		return t.User
	}
	return def
}

// authHint annotates auth failures with what to try next.
func authHint(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "unable to authenticate") {
		if strings.Contains(msg, "password") { // a password was tried and refused too
			return fmt.Errorf("%w (all credentials rejected: wrong password, or your public key is not installed on this host)", err)
		}
		return fmt.Errorf("%w (keys rejected: install your public key on the host, load an agent, or answer the password prompt)", err)
	}
	return err
}

// keepalive keeps NAT/firewall state alive and turns silent network death
// into a closed connection within ~45s instead of a hung session.
func (c *Client) keepalive() {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	misses := 0
	for range t.C {
		_, _, err := c.conn.SendRequest("keepalive@tokentop", true, nil)
		if err != nil {
			misses++
			if misses >= 3 {
				c.conn.Close()
				return
			}
			continue
		}
		misses = 0
	}
}

// Run executes script in the remote login shell and returns stdout. On
// failure the error carries the tail of stderr so problems are diagnosable.
func (c *Client) Run(ctx context.Context, script string) (string, error) {
	sess, err := c.conn.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", connLost(err))
	}
	var stderr bytes.Buffer
	sess.Stderr = &stderr

	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, oerr := sess.Output(script)
		done <- result{string(out), oerr}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			return r.out, fmt.Errorf("remote command failed: %w%s", r.err, stderrTail(stderr.String()))
		}
		return r.out, nil
	case <-time.After(runTimeout):
		sess.Close()
		return "", fmt.Errorf("remote command timed out after %s", runTimeout)
	case <-ctx.Done():
		sess.Close()
		return "", ctx.Err()
	}
}

func stderrTail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) > 300 {
		s = s[len(s)-300:]
	}
	return ": " + s
}

// connLost classifies session-open failures so callers can tell a dead
// connection from a transient hiccup.
func connLost(err error) error {
	if strings.Contains(err.Error(), "connection lost") ||
		strings.Contains(err.Error(), "EOF") ||
		strings.Contains(err.Error(), "closed") {
		return fmt.Errorf("ssh connection lost")
	}
	return err
}

// Forward binds a local listener per remote port and pipes every accepted
// connection through an ssh TCP channel. The returned map (remote port ->
// bound local port) is usable the moment this returns.
func (c *Client) Forward(rports []int) (map[int]int, error) {
	out := make(map[int]int, len(rports))
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, rp := range rports {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			continue
		}
		out[rp] = l.Addr().(*net.TCPAddr).Port
		c.listeners = append(c.listeners, l)
		go c.relay(l, rp)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no local ports available for forwarding")
	}
	return out, nil
}

func (c *Client) relay(l net.Listener, rport int) {
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(rport))
	for {
		local, err := l.Accept()
		if err != nil {
			return // listener closed by Close()
		}
		go func(local net.Conn) {
			defer local.Close()
			remote, derr := c.conn.Dial("tcp", target)
			if derr != nil {
				return
			}
			defer remote.Close()
			piped := make(chan struct{}, 2)
			go func() { io.Copy(remote, local); piped <- struct{}{} }()
			go func() { io.Copy(local, remote); piped <- struct{}{} }()
			<-piped
		}(local)
	}
}

// Close tears down relays and the connection. Safe more than once.
func (c *Client) Close() {
	c.closeMu.Lock()
	select {
	case <-c.closed: // already closed
		c.closeMu.Unlock()
		return
	default:
	}
	close(c.closed)
	c.closeMu.Unlock()

	c.mu.Lock()
	for _, l := range c.listeners {
		l.Close()
	}
	c.listeners = nil
	c.mu.Unlock()

	c.setErr(nil)
	c.conn.Close()
}

// watchClose marks abnormal termination when the underlying connection dies
// outside Close(): still-open relays prove this was not a deliberate shutdown.
// Call once at Connect time.
func (c *Client) watchClose() {
	c.conn.Conn.Wait() // returns when the connection is torn down
	c.mu.Lock()
	unexpected := len(c.listeners) > 0
	c.mu.Unlock()
	if !unexpected {
		return
	}
	c.setErr(fmt.Errorf("ssh connection lost"))
	c.closeMu.Lock()
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	c.closeMu.Unlock()
}
