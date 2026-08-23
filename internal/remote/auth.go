package remote

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/term"
)

// defaultKeyPaths lists the private keys tried when nothing explicit is
// configured, mirroring ssh's defaults.
func defaultKeyPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	names := []string{"id_ed25519", "id_ecdsa", "id_rsa"}
	paths := make([]string, 0, len(names))
	for _, n := range names {
		paths = append(paths, filepath.Join(home, ".ssh", n))
	}
	return paths
}

// loadSigner reads one private key file. Encrypted keys are skipped (they
// would block headless runs); use an agent or a passphrase-less key.
func loadSigner(path string) (ssh.Signer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s, err := ssh.ParsePrivateKey(b)
	var pme *ssh.PassphraseMissingError
	if errors.As(err, &pme) {
		return nil, fmt.Errorf("%s: encrypted; use a passphrase-less key or ssh-agent", filepath.Base(path))
	}
	return s, err
}

// keyFileAuth builds an AuthMethod from one key file. required marks an
// explicitly configured key (--ssh-key): its load failure must reach the
// operator instead of degrading into a confusing generic auth rejection;
// optional keys (the ~/.ssh defaults) may return an error the caller ignores.
func keyFileAuth(path string, required bool) (ssh.AuthMethod, error) {
	if path == "" {
		return nil, nil
	}
	s, err := loadSigner(path)
	if err != nil {
		if !required {
			return nil, nil
		}
		return nil, err
	}
	return ssh.PublicKeys(s), nil
}

// passwordSource yields a secret at most once per connection attempt chain:
// TOKENTOP_SSH_PASSWORD for headless runs, otherwise a terminal prompt. It
// remembers the answer so password and keyboard-interactive mechanisms share
// it without asking twice, and remembers why no answer was produced so an
// aborted prompt surfaces as its real cause instead of a generic auth failure.
type passwordSource struct {
	mu    sync.Mutex
	pw    string
	asked bool
	err   error // set when asked but no password could be obtained
}

var interactivePassword = func(t Target) (string, error) {
	fmt.Printf("tokentop: password for %s: ", t.userHost())
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", fmt.Errorf("empty password")
	}
	return string(b), nil
}

func (p *passwordSource) get(t Target) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.asked {
		if p.pw != "" {
			return p.pw, nil
		}
		return "", p.err
	}
	p.asked = true
	if v := os.Getenv("TOKENTOP_SSH_PASSWORD"); v != "" {
		p.pw = v
		return v, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		p.err = fmt.Errorf("no password available (stdin is not a terminal; set TOKENTOP_SSH_PASSWORD)")
		return "", p.err
	}
	v, err := interactivePassword(t)
	if err != nil {
		p.err = fmt.Errorf("password prompt failed: %w", err)
		return "", p.err
	}
	p.pw = v
	return v, nil
}

// authCallbacks turns a passwordSource into the two standard mechanisms so
// servers preferring either password or keyboard-interactive both work.
func (p *passwordSource) authCallbacks(t Target) []ssh.AuthMethod {
	get := func() (string, error) { return p.get(t) }
	pw := ssh.PasswordCallback(get)
	ki := ssh.KeyboardInteractive(func(_ string, _ string, questions []string, _ []bool) ([]string, error) {
		s, err := get()
		if err != nil {
			return nil, err
		}
		ans := make([]string, len(questions))
		for i := range ans {
			ans[i] = s
		}
		return ans, nil
	})
	return []ssh.AuthMethod{pw, ki}
}

// dialAgent is swappable in tests.
var dialAgent = func(sock string) (agent.Agent, func(), error) {
	c, err := net.Dial("unix", sock)
	if err != nil {
		return nil, nil, err
	}
	return agent.NewClient(c), func() { c.Close() }, nil
}

// authMethods assembles the credential chain in preference order: explicit
// key file, config/default keys, then the agent if SSH_AUTH_SOCK is set. A
// load failure of the explicitly configured key aborts the chain: dialing
// without it could only end in a misleading credentials-rejected error.
func (t Target) authMethods() ([]ssh.AuthMethod, func(), error) {
	cleanup := func() {}
	var methods []ssh.AuthMethod
	if m, err := keyFileAuth(t.KeyFile, true); err != nil {
		return nil, cleanup, fmt.Errorf("key %s: %w", t.KeyFile, err)
	} else if m != nil {
		methods = append(methods, m)
	}
	for _, p := range defaultKeyPaths() {
		if m, _ := keyFileAuth(p, false); m != nil { // defaults are best effort
			methods = append(methods, m)
		}
	}
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if ag, ac, err := dialAgent(sock); err == nil {
			methods = append(methods, ssh.PublicKeysCallback(ag.Signers))
			old := cleanup
			cleanup = func() { old(); ac() }
		}
	}
	return methods, cleanup, nil
}
