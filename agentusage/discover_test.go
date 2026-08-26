// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package agentusage

import "testing"

func TestAgentName(t *testing.T) {
	known := map[string]bool{"claude": true, "codex": true, "node": false}

	cases := []struct {
		name string
		comm string
		argv []string
		want string
	}{
		{"kernel name wins", "claude", []string{"/opt/node", "/x/claude"}, "claude"},
		{"argv0 basename", "", []string{"/usr/local/bin/codex", "--full-auto"}, "codex"},
		{"node script in argv1", "", []string{"node", "/home/u/.local/bin/codex", "--full-auto"}, "codex"},
		{"extension is not stripped", "", []string{"node", "/home/u/.claude/local/claude.js"}, ""},
		{"later mention is not the agent", "", []string{"node", "server.js", "--agent=claude"}, ""},
		{"empty argv", "bash", nil, ""},
		{"unknown everything", "vim", []string{"vim", "notes.txt"}, ""},
		{"comm whitespace trimmed", " claude ", []string{}, "claude"},
	}
	for _, tc := range cases {
		if got := agentName(tc.comm, tc.argv, known); got != tc.want {
			t.Errorf("%s: agentName(%q, %q) = %q, want %q", tc.name, tc.comm, tc.argv, got, tc.want)
		}
	}
}
