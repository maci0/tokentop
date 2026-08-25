package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
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
