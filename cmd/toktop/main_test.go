package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/maci0/toktop/agentusage"
	"github.com/maci0/toktop/internal/core"
)

func TestValidateFlags(t *testing.T) {
	tests := []struct {
		name      string
		once      bool
		interval  time.Duration
		probeSecs int
		frames    int
		wantErr   string
	}{
		{name: "defaults are valid", interval: time.Second, frames: 2},
		{name: "probe off is zero, not negative", interval: time.Second, probeSecs: 0},
		{name: "negative interval rejected", interval: -time.Second, wantErr: "--interval"},
		{name: "zero interval rejected", interval: 0, wantErr: "--interval"},
		{name: "negative probe rejected", interval: time.Second, probeSecs: -5, wantErr: "--probe"},
		{name: "frames unchecked without --once", interval: time.Second, frames: 0},
		{name: "zero frames with --once rejected", once: true, interval: time.Second, frames: 0, wantErr: "--frames"},
		{name: "negative frames with --once rejected", once: true, interval: time.Second, frames: -1, wantErr: "--frames"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFlags(tt.once, tt.interval, tt.probeSecs, tt.frames)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateFlags() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateFlags() = %v, want error mentioning %q", err, tt.wantErr)
			}
		})
	}
}

// captureStderr runs f with stderr redirected and returns what it printed.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()
	f()
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func captureWarnUnknownEnv(t *testing.T) string {
	t.Helper()
	return captureStderr(t, warnUnknownEnv)
}

func TestWarnUnknownEnv(t *testing.T) {
	t.Run("known variables pass silently", func(t *testing.T) {
		t.Setenv("TOKTOP_BEARER", "x")
		t.Setenv("TOKTOP_SSH_PASSWORD", "x")
		t.Setenv("TOKTOP_COLUMNS", "80")
		t.Setenv("TOKTOP_LINES", "24")
		if got := captureWarnUnknownEnv(t); got != "" {
			t.Fatalf("warnUnknownEnv() printed %q, want silence", got)
		}
	})
	t.Run("misspelled variable is named", func(t *testing.T) {
		t.Setenv("TOKTOP_BEARE", "x") // typo: must not be swallowed
		got := captureWarnUnknownEnv(t)
		if !strings.Contains(got, "TOKTOP_BEARE") {
			t.Fatalf("warnUnknownEnv() printed %q, want mention of TOKTOP_BEARE", got)
		}
	})
	t.Run("unrelated variables ignored", func(t *testing.T) {
		t.Setenv("OTHER_VAR", "x")
		if got := captureWarnUnknownEnv(t); got != "" {
			t.Fatalf("warnUnknownEnv() printed %q, want silence", got)
		}
	})
}

func TestValidateOnceEnv(t *testing.T) {
	tests := []struct {
		name    string
		columns string
		lines   string
		wantErr string // empty means silence expected
	}{
		{name: "unset passes"},
		{name: "empty means unset", columns: "", lines: ""},
		{name: "typical values pass", columns: "120", lines: "38"},
		{name: "floors accepted", columns: "41", lines: "21"},
		{name: "below floor rejected", columns: "40", wantErr: "TOKTOP_COLUMNS"},
		{name: "not a number rejected", lines: "full-hd", wantErr: "TOKTOP_LINES"},
		{name: "negative rejected", columns: "-1", wantErr: "TOKTOP_COLUMNS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TOKTOP_COLUMNS", tt.columns)
			t.Setenv("TOKTOP_LINES", tt.lines)
			err := validateOnceEnv()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateOnceEnv() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateOnceEnv() = %v, want error mentioning %q", err, tt.wantErr)
			}
		})
	}
}

// writeAgentsJSON points GAUNTLET_HOME at a temp dir holding the given file
// body ("" writes nothing, leaving agents.json absent).
func writeAgentsJSON(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if body != "" {
		path := filepath.Join(dir, "agents.json")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadAgentDefs(t *testing.T) {
	t.Run("missing file is silent", func(t *testing.T) {
		t.Setenv("GAUNTLET_HOME", writeAgentsJSON(t, ""))
		if got := captureStderr(t, loadAgentDefs); got != "" {
			t.Fatalf("loadAgentDefs() printed %q, want silence", got)
		}
	})

	t.Run("malformed file names itself and names the consequence", func(t *testing.T) {
		t.Setenv("GAUNTLET_HOME", writeAgentsJSON(t, "{oops"))
		got := captureStderr(t, loadAgentDefs)
		for _, want := range []string{"agents.json", "built-in agents"} {
			if !strings.Contains(got, want) {
				t.Fatalf("loadAgentDefs() printed %q, want mention of %q", got, want)
			}
		}
	})

	t.Run("valid file loads quietly", func(t *testing.T) {
		t.Setenv("GAUNTLET_HOME", writeAgentsJSON(t,
			`{"deftest-agent":{"usage":{"roots":["~/.deftest/sessions"]}}}`))
		if got := captureStderr(t, loadAgentDefs); got != "" {
			t.Fatalf("loadAgentDefs() printed %q, want silence", got)
		}
		if !slices.Contains(agentusage.Agents(), "deftest-agent") {
			t.Fatalf("defined agent missing from %v", agentusage.Agents())
		}
	})
}

func TestUsage(t *testing.T) {
	var buf strings.Builder
	usage(&buf)
	got := buf.String()
	for _, want := range []string{
		"toktop -",             // what the tool is
		"Usage:",               // invocation line
		"[ssh://user@host ...", // positional targets documented
		"Examples:",            // worked examples section
		"-demo",                // generated flag docs survive
		"OMNIROUTE_API_KEY",    // env fallbacks named
	} {
		if !strings.Contains(got, want) {
			t.Errorf("usage() missing %q", want)
		}
	}
}

// runUpdate must answer -h/--help the way the top-level command does: full
// usage on stdout with exit 0, so `toktop update --help | grep repo` works.
func TestRunUpdateHelp(t *testing.T) {
	for _, arg := range []string{"--help", "-h"} {
		t.Run(arg, func(t *testing.T) {
			var out bytes.Buffer
			var code int
			got := captureStderr(t, func() {
				code = runUpdate(context.Background(), &out, []string{arg})
			})
			if code != 0 {
				t.Fatalf("runUpdate(%q) = %d, want 0", arg, code)
			}
			if out.Len() == 0 {
				t.Fatalf("runUpdate(%q) wrote nothing to stdout", arg)
			}
			if got != "" {
				t.Fatalf("runUpdate(%q) leaked %q to stderr", arg, got)
			}
			for _, want := range []string{"Usage:", "--check", "--repo"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("update help missing %q", want)
				}
			}
		})
	}
}

func TestRunUpdateUsageErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantSub string // required substring on stderr
	}{
		{name: "unknown flag", args: []string{"--bogus"}, wantSub: "flag provided but not defined"},
		{name: "unexpected argument", args: []string{"extra"}, wantSub: "unexpected argument"},
		{name: "unexpected argument points at help", args: []string{"extra"}, wantSub: "toktop update --help"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var code int
			got := captureStderr(t, func() {
				code = runUpdate(context.Background(), io.Discard, tt.args)
			})
			if code != 2 {
				t.Fatalf("runUpdate(%v) = %d, want 2", tt.args, code)
			}
			if !strings.Contains(got, tt.wantSub) {
				t.Fatalf("stderr = %q, want mention of %q", got, tt.wantSub)
			}
		})
	}
}

func TestWarnIgnoredFlags(t *testing.T) {
	tests := []struct {
		name    string
		set     map[string]bool
		demo    bool
		once    bool
		agents  bool
		wantSub string // empty means silence expected
	}{
		{name: "seed outside demo warns", set: map[string]bool{"seed": true}, wantSub: "--seed"},
		{name: "seed inside demo silent", set: map[string]bool{"seed": true}, demo: true},
		{name: "seed default silent", set: map[string]bool{}, demo: false},
		{name: "frames outside once warns", set: map[string]bool{"frames": true}, wantSub: "--frames"},
		{name: "frames inside once silent", set: map[string]bool{"frames": true}, once: true},
		{name: "both no-ops warn twice", set: map[string]bool{"seed": true, "frames": true},
			wantSub: "--seed"},
		{name: "opencode-db without agents warns", set: map[string]bool{"opencode-db": true},
			wantSub: "--opencode-db"},
		{name: "opencode-db with agents silent", set: map[string]bool{"opencode-db": true}, agents: true},
		{name: "plain outside once warns", set: map[string]bool{"plain": true}, wantSub: "--plain"},
		{name: "plain inside once silent", set: map[string]bool{"plain": true}, once: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := captureStderr(t, func() { warnIgnoredFlags(tt.set, tt.demo, tt.once, tt.agents) })
			if tt.wantSub == "" {
				if got != "" {
					t.Fatalf("warnIgnoredFlags() printed %q, want silence", got)
				}
				return
			}
			if !strings.Contains(got, tt.wantSub) {
				t.Fatalf("warnIgnoredFlags() printed %q, want mention of %q", got, tt.wantSub)
			}
		})
	}
}

func TestWaitForFrames(t *testing.T) {
	mark := core.Snapshot{Uptime: 7 * time.Second} // comparable field identifies the frame
	t.Run("collects the requested frames", func(t *testing.T) {
		ch := make(chan core.Snapshot, 2)
		ch <- mark
		ch <- mark
		got, err := waitForFrames(context.Background(), ch, 2, time.Second)
		if err != nil || got.Uptime != mark.Uptime {
			t.Fatalf("waitForFrames() = %+v, %v; want the sent frame, nil", got, err)
		}
	})
	t.Run("timeout names the cause", func(t *testing.T) {
		ch := make(chan core.Snapshot)
		if _, err := waitForFrames(context.Background(), ch, 1, 10*time.Millisecond); err == nil ||
			strings.Contains(err.Error(), "interrupted") || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("waitForFrames() = %q, want timeout error", err)
		}
	})
	// A Ctrl+C during --once must abort the wait at once rather than hang
	// until the per-frame timeout fires.
	t.Run("canceled context interrupts immediately", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		start := time.Now()
		_, err := waitForFrames(ctx, make(chan core.Snapshot), 3, time.Minute)
		if !errors.Is(err, errInterrupted) {
			t.Fatalf("waitForFrames() = %v, want errInterrupted", err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("waitForFrames() took %s after cancellation, want prompt return", elapsed)
		}
	})
}

func TestWarnIgnoredFrameEnv(t *testing.T) {
	tests := []struct {
		name    string
		once    bool
		columns string
		lines   string
		wantSub string // empty means silence expected
	}{
		{name: "unset is silent"},
		{name: "columns outside once warns", columns: "120", wantSub: "TOKTOP_COLUMNS"},
		{name: "lines outside once warns", lines: "38", wantSub: "TOKTOP_LINES"},
		{name: "both set warn twice", columns: "120", lines: "38", wantSub: "TOKTOP_COLUMNS"},
		{name: "inside once silent", once: true, columns: "120", lines: "38"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TOKTOP_COLUMNS", tt.columns)
			t.Setenv("TOKTOP_LINES", tt.lines)
			got := captureStderr(t, func() { warnIgnoredFrameEnv(tt.once) })
			if tt.wantSub == "" {
				if got != "" {
					t.Fatalf("warnIgnoredFrameEnv() printed %q, want silence", got)
				}
				return
			}
			if n := strings.Count(got, "has no effect"); tt.columns != "" && tt.lines != "" && n < 2 {
				t.Fatalf("warnIgnoredFrameEnv() printed %d warnings, want 2 for both variables", n)
			}
			if !strings.Contains(got, tt.wantSub) {
				t.Fatalf("warnIgnoredFrameEnv() printed %q, want mention of %q", got, tt.wantSub)
			}
		})
	}
}

func TestRoutableBind(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8420", false},
		{"[::1]:8420", false},
		{"localhost:8420", false}, // resolved form is what Addr reports; a literal name errs into quiet
		{":8420", true},
		{"0.0.0.0:8420", true},
		{"[::]:8420", true},
		{"192.168.1.7:8420", true},
	}
	for _, tt := range tests {
		if got := routableBind(tt.addr); got != tt.want {
			t.Errorf("routableBind(%q) = %v, want %v", tt.addr, got, tt.want)
		}
	}
}
