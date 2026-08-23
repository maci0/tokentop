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

// keyFileAuth builds an AuthMethod from one key file, or nil.
func keyFileAuth(path string) ssh.AuthMethod {
	if path == "" {
		return nil
	}
	s, err := loadSigner(path)
	if err != nil {
		return nil
	}
	return ssh.PublicKeys(s)
}

// passwordSource yields a secret at most once per connection attempt chain:
// TOKENTOP_SSH_PASSWORD for headless runs, otherwise a terminal prompt. It
// remembers the answer so password and keyboard-interactive mechanisms share
// it without asking twice.
type passwordSource struct {
	mu    sync.Mutex
	pw    string
	asked bool
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

func (p *passwordSource) get(t Target) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.asked {
		return p.pw, p.pw != ""
	}
	p.asked = true
	if v := os.Getenv("TOKENTOP_SSH_PASSWORD"); v != "" {
		p.pw = v
		return v, true
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", false
	}
	v, err := interactivePassword(t)
	if err != nil {
		return "", false
	}
	p.pw = v
	return v, true
}

// authCallbacks turns a passwordSource into the two standard mechanisms so
// servers preferring either password or keyboard-interactive both work.
func (p *passwordSource) authCallbacks(t Target) []ssh.AuthMethod {
	get := func() (string, bool) { return p.get(t) }
	pw := ssh.PasswordCallback(func() (string, error) {
		if s, ok := get(); ok {
			return s, nil
		}
		return "", fmt.Errorf("no password available")
	})
	ki := ssh.KeyboardInteractive(func(_ string, _ string, questions []string, _ []bool) ([]string, error) {
		s, ok := get()
		if !ok {
			return nil, fmt.Errorf("no password available")
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
// key file, config/default keys, then the agent if SSH_AUTH_SOCK is set.
func (t Target) authMethods() ([]ssh.AuthMethod, func()) {
	cleanup := func() {}
	var methods []ssh.AuthMethod
	if m := keyFileAuth(t.KeyFile); m != nil {
		methods = append(methods, m)
	}
	for _, p := range defaultKeyPaths() {
		if m := keyFileAuth(p); m != nil {
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
	return methods, cleanup
}
