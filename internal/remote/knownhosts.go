package remote

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// knownHostsFile is the trust-on-first-use store. Overridable in tests.
var knownHostsPath = func() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "toktop", "known_hosts")
}

// tofu returns a HostKeyCallback implementing trust-on-first-use against a
// small local store: first contact is remembered, changed keys are refused
// with both fingerprints so the operator can judge.
func tofu() (ssh.HostKeyCallback, error) {
	path := knownHostsPath()
	if path == "" {
		return nil, fmt.Errorf("cannot resolve config dir for host key store")
	}
	store, err := readKnownHosts(path)
	if err != nil {
		return nil, err
	}
	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		line := hostname + " " + string(ssh.MarshalAuthorizedKey(key))
		line = strings.TrimSpace(line)
		if old, ok := store[hostname]; ok {
			if old == line {
				return nil
			}
			return fmt.Errorf(
				"host key for %s changed!\n  stored:    %s (%s)\n  presented: %s (%s)\nrefusing to connect; remove the stale line from %s if this host was rebuilt",
				hostname,
				short(old), fingerprintOf(old),
				short(line), fingerprintOf(line),
				path)
		}
		store[hostname] = line
		return writeKnownHosts(path, store)
	}, nil
}

func readKnownHosts(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if host, rest, ok := strings.Cut(line, " "); ok && rest != "" {
			out[host] = strings.TrimSpace(rest)
		}
	}
	return out, nil
}

func writeKnownHosts(path string, store map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var b strings.Builder
	for host, key := range store {
		b.WriteString(host + " " + key + "\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func short(s string) string {
	fields := strings.Fields(s)
	if len(fields) > 2 {
		return fields[2]
	}
	if len(fields) > 1 {
		return fields[len(fields)-1]
	}
	return s
}

// fingerprintOf renders the stored key's SHA-256 fingerprint; unknown shapes
// degrade to a short hash of the raw text.
func fingerprintOf(line string) string {
	fields := strings.FieldsSeq(line)
	for f := range fields {
		if strings.HasPrefix(f, "AAAA") {
			if k, _, _, _, err := ssh.ParseAuthorizedKey([]byte("ssh-ed25519 " + f)); err == nil {
				return ssh.FingerprintSHA256(k)
			}
		}
	}
	sum := sha256.Sum256([]byte(line))
	return base64.RawStdEncoding.EncodeToString(sum[:8])
}
