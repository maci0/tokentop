// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package selfupdate

import "testing"

// NewerThan decides whether --update replaces the running binary. The
// comparison is deliberately exact rather than semver-aware: releases are
// the source of truth, so any different tag applies, a published downgrade
// included, and only an identical or tagless release must be skipped.
func TestNewerThanIsExactNotSemver(t *testing.T) {
	cases := []struct {
		tag, current string
		want         bool
	}{
		{"v1.2.3", "1.2.3", false},  // same release, both spellings of v
		{"1.2.3", "v1.2.3", false},  //
		{"v1.3.0", "v1.3.0", false}, // dev build reporting its own release
		{"v1.3.0", "1.2.3", true},   // ordinary upgrade
		{"v1.2.2", "1.2.3", true},   // a published downgrade applies on purpose
		{"v2.0.0-pre.1", "2.0.0-pre.0", true},
		{"", "1.2.3", false}, // tagless release must not loop updates forever
		{"", "", false},      //
	}
	for _, c := range cases {
		rel := &Release{TagName: c.tag}
		if got := rel.NewerThan(c.current); got != c.want {
			t.Errorf("(&Release{TagName:%q}).NewerThan(%q) = %v, want %v",
				c.tag, c.current, got, c.want)
		}
	}
}
