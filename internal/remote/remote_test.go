package remote

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		raw     string
		user    string
		host    string
		port    int
		wantErr bool
	}{
		{"ssh://maci@192.168.0.211", "maci", "192.168.0.211", 22, false},
		{"ssh://root@gpu-box:2222", "root", "gpu-box", 2222, false},
		{"ssh://192.168.1.5", "", "192.168.1.5", 22, false},
		{"http://x", "", "", 0, true},
		{"ssh://", "", "", 0, true},
		{"ssh://h:notaport", "", "h", 0, true},
	}
	for _, c := range cases {
		got, err := ParseTarget(c.raw)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseTarget(%q) expected error", c.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseTarget(%q): %v", c.raw, err)
			continue
		}
		if got.User != c.user || got.Host != c.host || got.Port != c.port {
			t.Errorf("ParseTarget(%q) = %+v, want user=%q host=%q port=%d",
				c.raw, got, c.user, c.host, c.port)
		}
	}
}

func TestParseTargetSSHConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	os.WriteFile(cfg, []byte(`
# comment
Host gpu
  hostname 192.168.0.212
  user maci
  port 2022
  identityfile ~/.ssh/gpu_key

Host *.lab
  user labadmin

Host *
  user fallback
`), 0o600)
	oldPath, oldRead := sshConfigPath, configReader
	defer func() { sshConfigPath, configReader = oldPath, oldRead }()
	sshConfigPath = func() string { return cfg }
	configReader = func(path string) ([]byte, error) { return os.ReadFile(path) }

	tgt, err := ParseTarget("ssh://gpu")
	if err != nil {
		t.Fatal(err)
	}
	want := Target{User: "maci", Host: "192.168.0.212", Port: 2022,
		KeyFile: filepath.Join(dir, ".ssh", "gpu_key")}
	// expandTilde resolves to $HOME, not dir; just verify suffix
	if tgt.User != want.User || tgt.Host != want.Host || tgt.Port != want.Port {
		t.Errorf("resolved = %+v, want %+v", tgt, want)
	}
	if !strings.HasSuffix(tgt.KeyFile, "gpu_key") {
		t.Errorf("keyfile = %q", tgt.KeyFile)
	}

	tgt, _ = ParseTarget("ssh://box.lab")
	if tgt.User != "labadmin" || tgt.Port != 22 {
		t.Errorf("wildcard match = %+v", tgt)
	}

	// explicit URL fields win over config
	tgt, _ = ParseTarget("ssh://root@gpu:23")
	if tgt.User != "root" || tgt.Port != 23 {
		t.Errorf("URL precedence = %+v", tgt)
	}
}

func TestCutConfigField(t *testing.T) {
	cases := []struct {
		line, key, val string
		ok             bool
	}{
		{"HostName foo", "HostName", "foo", true},
		{"  identityfile = ~/keys/id ", "identityfile", "~/keys/id", true},
		{"#comment", "", "", false},
		{"", "", "", false},
		{"solokeyword", "", "", false},
	}
	for _, c := range cases {
		k, v, ok := cutConfigField(c.line)
		if k != c.key || v != c.val || ok != c.ok {
			t.Errorf("cutConfigField(%q) = %q,%q,%v want %q,%q,%v",
				c.line, k, v, ok, c.key, c.val, c.ok)
		}
	}
}

func TestPatternMatch(t *testing.T) {
	cases := []struct {
		pat, s string
		want   bool
	}{
		{"*", "anything", true},
		{"gpu-*", "gpu-box1", true},
		{"*.lab", "box.lab", true},
		{"*.lab", "box.example.com", false},
		{"host?", "host1", true},
		{"host?", "host12", false},
		{"exact", "exact", true},
		{"a*b*c", "axxbyyc", true},
	}
	for _, c := range cases {
		if got := patternMatch(c.pat, c.s); got != c.want {
			t.Errorf("patternMatch(%q,%q) = %v", c.pat, c.s, got)
		}
	}
}

func TestTOFUStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	old := knownHostsPath
	defer func() { knownHostsPath = old }()
	knownHostsPath = func() string { return path }

	cb1, err := tofu()
	if err != nil {
		t.Fatal(err)
	}
	key1 := fakePublicKey("first")
	if err := cb1("h:22", nil, key1); err != nil {
		t.Fatalf("first contact rejected: %v", err)
	}

	// A fresh callback over the same store accepts the remembered key.
	cb2, _ := tofu()
	if err := cb2("h:22", nil, key1); err != nil {
		t.Fatalf("remembered key rejected: %v", err)
	}
	// A different key must be refused.
	if err := cb2("h:22", nil, fakePublicKey("second")); err == nil {
		t.Fatal("changed key accepted")
	} else if !strings.Contains(err.Error(), "changed") {
		t.Errorf("mismatch error should say changed: %v", err)
	}

	store, _ := readKnownHosts(path)
	if len(store) != 1 {
		t.Errorf("store = %v", store)
	}
}

// The default-key leg of the auth chain must pick up a passphrase-less key
// from ~/.ssh and silently skip unreadable or encrypted files.
func TestDefaultKeyPathsAndAuthChain(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "") // no agent interference
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on windows

	tgt := Target{}
	methods, cleanup, err := tgt.authMethods()
	cleanup()
	if err != nil {
		t.Fatalf("empty home: %v", err)
	}
	if len(methods) != 0 {
		t.Fatalf("empty home yielded %d methods, want 0", len(methods))
	}
	if got, want := len(defaultKeyPaths()), 3; got != want {
		t.Fatalf("defaultKeyPaths = %d paths, want %d", got, want)
	}

	key := filepath.Join(home, ".ssh", "id_ed25519")
	if err := os.MkdirAll(filepath.Dir(key), 0o700); err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "toktop test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	// junk next to it must be skipped without breaking the chain
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_rsa"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}

	methods, cleanup, err = tgt.authMethods()
	defer cleanup()
	if err != nil {
		t.Fatalf("one valid default key: %v", err)
	}
	if len(methods) != 1 {
		t.Fatalf("authMethods = %d methods with one valid key present, want 1", len(methods))
	}
}

// An explicitly configured key (--ssh-key) that cannot be loaded must abort
// authentication with the offending path, not silently fall through to a
// generic credentials-rejected failure later.
// The password source must ask once: the env-var answer is cached so the
// password and keyboard-interactive legs of one connection chain share it
// instead of re-reading the environment or prompting twice.
func TestPasswordSourceEnvWinsAndAsksOnce(t *testing.T) {
	t.Setenv("TOKTOP_SSH_PASSWORD", "sekrit")
	tgt := Target{User: "u", Host: "h", Port: 22}
	ps := &passwordSource{}

	pw, err := ps.get(tgt)
	if err != nil || pw != "sekrit" {
		t.Fatalf("first get = %q, %v", pw, err)
	}
	t.Setenv("TOKTOP_SSH_PASSWORD", "") // env gone after the first ask
	pw, err = ps.get(tgt)
	if err != nil || pw != "sekrit" {
		t.Errorf("cached answer lost: %q, %v", pw, err)
	}
}

// Without TOKTOP_SSH_PASSWORD and without a terminal there is no way to
// prompt: get must fail naming the env var, and cache that failure instead
// of retrying (or blocking) for every auth mechanism in the chain.
func TestPasswordSourceHeadlessFailureCached(t *testing.T) {
	t.Setenv("TOKTOP_SSH_PASSWORD", "")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = r // a pipe is never a terminal
	t.Cleanup(func() { os.Stdin = oldStdin; r.Close(); w.Close() })

	ps := &passwordSource{}
	tgt := Target{}
	_, err = ps.get(tgt)
	if err == nil || !strings.Contains(err.Error(), "TOKTOP_SSH_PASSWORD") {
		t.Fatalf("headless get err = %v, want guidance naming the env var", err)
	}
	_, err = ps.get(tgt)
	if err == nil {
		t.Fatal("cached failure must persist across calls")
	}
}

func TestExplicitKeyFileFailureAborts(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	bogus := filepath.Join(home, "nope", "missing_key")
	tgt := Target{KeyFile: bogus}
	methods, cleanup, err := tgt.authMethods()
	cleanup()
	if err == nil {
		t.Fatal("explicit unloadable key must fail authMethods")
	}
	if len(methods) != 0 {
		t.Fatalf("failed chain yielded %d methods, want 0", len(methods))
	}
	if !strings.Contains(err.Error(), bogus) {
		t.Errorf("error should name the key path: %v", err)
	}
}
