package main

import (
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

// captureWarnUnknownEnv runs warnUnknownEnv with stderr redirected and
// returns what it printed.
func captureWarnUnknownEnv(t *testing.T) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()
	warnUnknownEnv()
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestWarnUnknownEnv(t *testing.T) {
	t.Run("known variables pass silently", func(t *testing.T) {
		t.Setenv("TOKENTOP_BEARER", "x")
		t.Setenv("TOKENTOP_SSH_PASSWORD", "x")
		t.Setenv("TOKENTOP_COLUMNS", "80")
		t.Setenv("TOKENTOP_LINES", "24")
		if got := captureWarnUnknownEnv(t); got != "" {
			t.Fatalf("warnUnknownEnv() printed %q, want silence", got)
		}
	})
	t.Run("misspelled variable is named", func(t *testing.T) {
		t.Setenv("TOKENTOP_BEARE", "x") // typo: must not be swallowed
		got := captureWarnUnknownEnv(t)
		if !strings.Contains(got, "TOKENTOP_BEARE") {
			t.Fatalf("warnUnknownEnv() printed %q, want mention of TOKENTOP_BEARE", got)
		}
	})
	t.Run("unrelated variables ignored", func(t *testing.T) {
		t.Setenv("OTHER_VAR", "x")
		if got := captureWarnUnknownEnv(t); got != "" {
			t.Fatalf("warnUnknownEnv() printed %q, want silence", got)
		}
	})
}
