// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package core

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

func TestTruncateClusters(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"empty", "", 5, ""},
		{"zero cap", "abc", 0, ""},
		{"negative cap", "abc", -1, ""},
		{"within cap", "hello", 10, "hello"},
		{"exact cap", "hello", 5, "hello"},
		{"ascii cut", "hello", 3, "hel"},
		// A flag is two regional indicators forming one character; cutting
		// between them would render half a flag.
		{"flags kept whole", "\U0001F1E9\U0001F1EA\U0001F1EB\U0001F1F7", 2, "\U0001F1E9\U0001F1EA\U0001F1EB\U0001F1F7"},
		{"cut between flags, never inside one", "\U0001F1E9\U0001F1EA\U0001F1EB\U0001F1F7", 1, "\U0001F1E9\U0001F1EA"},
		// An emoji ZWJ sequence is several code points joined by U+200D.
		{"zwj sequence kept whole", "👩‍💻👩‍💻👩‍💻", 2, "👩‍💻👩‍💻"},
		// Combining marks stay attached to their base letter.
		{"combining mark stays attached", "cafe\u0301", 4, "cafe\u0301"},
		{"combining mark not split off", "a\u0301bc", 1, "a\u0301"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TruncateClusters(tc.in, tc.n)
			if got != tc.want {
				t.Errorf("TruncateClusters(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
			if tc.n > 0 {
				if clusters := uniseg.GraphemeClusterCount(got); clusters > tc.n {
					t.Errorf("result %q holds %d clusters, cap is %d", got, clusters, tc.n)
				}
			}
			if !utf8.ValidString(got) {
				t.Errorf("result %q is not valid UTF-8", got)
			}
		})
	}
}

// Truncation must never manufacture a partial sequence: whatever comes back
// must be exactly a whole-cluster prefix of the input, pinning the guarantee
// against a future segmentation or implementation change.
func TestTruncateClustersIsWholeClusterPrefix(t *testing.T) {
	hostile := "\U0001F1E9\U0001F1EA" + strings.Repeat("e\u0301", 8) + "👩‍💻" + strings.Repeat("x", 16)
	total := uniseg.GraphemeClusterCount(hostile)
	var clusters []string
	state := -1
	for s := hostile; s != ""; {
		var c string
		c, s, _, state = uniseg.FirstGraphemeClusterInString(s, state)
		clusters = append(clusters, c)
	}
	for n := 0; n <= total; n++ {
		want := strings.Join(clusters[:n], "")
		if got := TruncateClusters(hostile, n); got != want {
			t.Fatalf("n=%d: TruncateClusters = %q, want the first %d whole clusters %q", n, got, n, want)
		}
	}
}
