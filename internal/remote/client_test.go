package remote

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakePublicKey derives a deterministic ed25519-backed ssh.PublicKey.
func fakePublicKey(label string) ssh.PublicKey {
	seed := sha256.Sum256([]byte("tokentop-test:" + label))
	pub, _, err := ed25519.GenerateKey(bytes.NewReader(seed[:]))
	if err != nil {
		panic(err)
	}
	pk, err := ssh.NewPublicKey(pub)
	if err != nil {
		panic(err)
	}
	return pk
}

// testSSHServer is an in-process sshd covering exactly what the client uses:
// publickey/password/keyboard-interactive auth, exec sessions and
// direct-tcpip channels. This keeps the whole remote path testable without
// spawning an external ssh binary anywhere.
type testSSHServer struct {
	lis      net.Listener
	hostKey  ssh.Signer
	password string // empty => publickey-only

	mu      sync.Mutex
	conns   []net.Conn
	closers []io.Closer
}

func newTestSSHServer(t *testing.T, password string, port int) *testSSHServer {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("session handler shells out to sh")
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privAny := any(priv)
	signer, err := ssh.NewSignerFromKey(privAny)
	if err != nil {
		t.Fatal(err)
	}
	s := &testSSHServer{hostKey: signer, password: password}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, _ ssh.PublicKey) (*ssh.Permissions, error) {
			if password != "" {
				return nil, fmt.Errorf("publickey disabled") // force password paths when configured
			}
			return &ssh.Permissions{}, nil
		},
	}
	if password != "" {
		cfg.PasswordCallback = func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) != password {
				return nil, fmt.Errorf("wrong password")
			}
			return &ssh.Permissions{}, nil
		}
		cfg.KeyboardInteractiveCallback = func(_ ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			ans, err := challenge("auth", "", []string{"Password:"}, []bool{false})
			if err != nil || len(ans) != 1 || ans[0] != password {
				return nil, fmt.Errorf("ki denied")
			}
			return &ssh.Permissions{}, nil
		}
	}
	cfg.AddHostKey(signer)

	var lis net.Listener
	if port > 0 {
		lis, err = net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	} else {
		lis, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatal(err)
	}
	s.lis = lis
	go s.accept(cfg)
	return s
}

func (s *testSSHServer) Port() int {
	return s.lis.Addr().(*net.TCPAddr).Port
}

func (s *testSSHServer) Close() {
	s.lis.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.Close()
	}
}

func (s *testSSHServer) accept(cfg *ssh.ServerConfig) {
	for {
		nc, err := s.lis.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns = append(s.conns, nc)
		s.mu.Unlock()
		conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
		if err != nil {
			nc.Close()
			continue
		}
		go s.handleConn(conn, chans, reqs)
	}
}

func (s *testSSHServer) handleConn(conn *ssh.ServerConn, chans <-chan ssh.NewChannel, reqs <-chan *ssh.Request) {
	defer conn.Close()
	go func() { // drain global requests; keepalive gets automatic replies
		for range reqs {
		}
	}()
	for nch := range chans {
		switch nch.ChannelType() {
		case "session":
			ch, creqs, err := nch.Accept()
			if err != nil {
				continue
			}
			go s.serveSession(ch, creqs)
		case "direct-tcpip":
			s.serveDirect(nch)
		default:
			nch.Reject(ssh.UnknownChannelType, "unsupported")
		}
	}
}

func (s *testSSHServer) serveSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "exec":
			var p struct{ Command string }
			if ssh.Unmarshal(req.Payload, &p) != nil {
				req.Reply(false, nil)
				continue
			}
			cmd := exec.Command("sh", "-c", p.Command)
			cmd.Stdout = ch
			cmd.Stderr = ch.Stderr()
			err := cmd.Run()
			status := uint32(0)
			if err != nil {
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					status = uint32(ee.ExitCode())
				} else {
					status = 127
				}
			}
			req.Reply(true, nil)
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
			return // one command per session
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

func (s *testSSHServer) serveDirect(nch ssh.NewChannel) {
	var p struct {
		Addr string
		Port uint32
		Orig string
		OPrt uint32
	}
	if ssh.Unmarshal(nch.ExtraData(), &p) != nil {
		nch.Reject(ssh.ConnectionFailed, "bad payload")
		return
	}
	upstream, err := net.Dial("tcp", net.JoinHostPort(p.Addr, strconv.Itoa(int(p.Port))))
	if err != nil {
		nch.Reject(ssh.ConnectionFailed, "dial failed")
		return
	}
	ch, _, err := nch.Accept()
	if err != nil {
		upstream.Close()
		return
	}
	go func() {
		io.Copy(ch, upstream)
		ch.CloseWrite()
	}()
	go func() {
		io.Copy(upstream, ch)
		upstream.Close()
	}()
}

func testTarget(t *testing.T, port int) Target {
	t.Helper()
	t.Setenv("USER", "tester") // currentUser() reads it; scoped to this test
	return Target{User: "tester", Host: "127.0.0.1", Port: port}
}

func withKnownHosts(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old := knownHostsPath
	knownHostsPath = func() string { return dir + "/known_hosts" }
	t.Cleanup(func() { knownHostsPath = old })
}

func TestClientConnectRunForward(t *testing.T) {
	withKnownHosts(t)
	srv := newTestSSHServer(t, "", 0)
	defer srv.Close()

	cli, err := Connect(t.Context(), testTarget(t, srv.Port()))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cli.Close()

	out, err := cli.Run(t.Context(), "echo hello-remote")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.TrimSpace(out); got != "hello-remote" {
		t.Errorf("run output = %q", got)
	}

	// Relay an engine-ish HTTP-less TCP service: raw echo.
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	go func() {
		for {
			c, err := up.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	rport := up.Addr().(*net.TCPAddr).Port
	fwd, err := cli.Forward([]int{rport})
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	lc, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(fwd[rport]))
	if err != nil {
		t.Fatalf("relay dial: %v", err)
	}
	fmt.Fprint(lc, "ping")
	buf := make([]byte, 4)
	io.ReadFull(lc, buf)
	lc.Close()
	if string(buf) != "ping" {
		t.Errorf("relay roundtrip = %q", buf)
	}

	// Close must be idempotent and fire Done.
	done := cli.Done()
	cli.Close()
	cli.Close()
	select {
	case <-done:
	default:
		t.Error("Done should be closed after Close")
	}
}

// The keepalive goroutine must exit promptly when the client is torn down,
// not keep ticking on a dead connection for its full miss window.
func TestKeepaliveStopsOnClose(t *testing.T) {
	withKnownHosts(t)
	srv := newTestSSHServer(t, "", 0)
	defer srv.Close()

	cli, err := Connect(t.Context(), testTarget(t, srv.Port()))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	done := cli.keepaliveDone
	cli.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("keepalive goroutine still running 2s after Close")
	}
}

// newSilentSSHServer completes handshakes and then goes dark: channels and
// global requests are drained but never serviced, TCP stays open. It models
// a blackholed route or a hung remote sshd, the failure mode keepalive must
// detect.
func newSilentSSHServer(t *testing.T) *testSSHServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(signer)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &testSSHServer{lis: lis, hostKey: signer}
	go func() {
		for {
			nc, err := lis.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, nc)
			s.mu.Unlock()
			go func(nc net.Conn) {
				conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
				if err != nil {
					nc.Close()
					return
				}
				defer conn.Close()
				go func() { // accept no channels
					for range chans {
					}
				}()
				for range reqs { // reply to no global requests
				}
				// hold TCP until the client gives up on us
			}(nc)
		}
	}()
	t.Cleanup(s.Close)
	return s
}

// A peer that stops replying without closing TCP must be force-closed by
// keepalive after three unanswered probes; Done must fire.
func TestKeepaliveDetectsSilentPeer(t *testing.T) {
	withKnownHosts(t)
	oldEvery, oldWait := keepaliveEvery, keepaliveReplies
	keepaliveEvery, keepaliveReplies = 10*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { keepaliveEvery, keepaliveReplies = oldEvery, oldWait })

	srv := newSilentSSHServer(t)
	cli, err := Connect(t.Context(), testTarget(t, srv.Port()))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cli.Close()

	select {
	case <-cli.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("silent peer not detected within probe window")
	}
}

// A host that completes TCP accept but never sends its ssh banner must fail
// Connect promptly via the handshake deadline, not hang forever.
func TestConnectFailsOnSilentHost(t *testing.T) {
	withKnownHosts(t)
	old := bannerTimeout
	bannerTimeout = 50 * time.Millisecond
	t.Cleanup(func() { bannerTimeout = old })

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lis.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := lis.Accept()
		if err == nil {
			accepted <- c // hold TCP open, send nothing
		}
	}()
	t.Cleanup(func() {
		select {
		case c := <-accepted:
			c.Close()
		default:
		}
	})

	start := time.Now()
	_, err = Connect(t.Context(), testTarget(t, lis.Addr().(*net.TCPAddr).Port))
	if err == nil {
		t.Fatal("silent host must fail Connect")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("silent host took %v to fail, want ~bannerTimeout", d)
	}
}

func TestClientRunFailureCarriesStderr(t *testing.T) {
	withKnownHosts(t)
	srv := newTestSSHServer(t, "", 0)
	defer srv.Close()
	cli, err := Connect(t.Context(), testTarget(t, srv.Port()))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	_, err = cli.Run(t.Context(), "echo boom >&2; exit 3")
	if err == nil {
		t.Fatal("expected failure")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("boom")) {
		t.Errorf("error should carry stderr tail: %v", err)
	}
}

func TestClientPasswordAuth(t *testing.T) {
	withKnownHosts(t)
	srv := newTestSSHServer(t, "secret", 0) // publickey rejected
	defer srv.Close()
	t.Setenv("TOKENTOP_SSH_PASSWORD", "secret")

	cli, err := Connect(t.Context(), testTarget(t, srv.Port()))
	if err != nil {
		t.Fatalf("password connect: %v", err)
	}
	defer cli.Close()
	if _, err := cli.Run(t.Context(), "true"); err != nil {
		t.Fatalf("run over password auth: %v", err)
	}
}

func TestClientWrongPasswordFails(t *testing.T) {
	withKnownHosts(t)
	srv := newTestSSHServer(t, "secret", 0)
	defer srv.Close()
	t.Setenv("TOKENTOP_SSH_PASSWORD", "wrong")

	_, err := Connect(t.Context(), testTarget(t, srv.Port()))
	if err == nil {
		t.Fatal("expected auth failure")
	}
	if !strings.Contains(err.Error(), "all credentials rejected") {
		t.Errorf("expected rejection hint, got: %v", err)
	}
}

func TestClientHostKeyChangeRefused(t *testing.T) {
	withKnownHosts(t)
	srv := newTestSSHServer(t, "", 0)
	port := srv.Port()
	cli, err := Connect(t.Context(), testTarget(t, port))
	if err != nil {
		t.Fatal(err)
	}
	cli.Close()
	srv.Close()

	// Same address, different host key.
	srv2 := newTestSSHServer(t, "", port)
	defer srv2.Close()
	_, err = Connect(t.Context(), testTarget(t, port))
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed host key must be refused, got: %v", err)
	}
}
