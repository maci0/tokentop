// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build windows

package agentusage

import (
	"path/filepath"
	"slices"
	"strings"
)

// NTFS and the Windows APIs look names up case-insensitively and accept both
// separators. An agent records whichever spelling its runtime produced (Node
// likes '/', cmd.exe likes '\'), so directory identity cannot be decided by
// bytes the way it is on Linux: a session started as C:/Users/Foo would
// silently vanish from a watcher resolved as c:\users\foo.

// dirVariants lists the spellings p can be recorded under: itself, then the
// cleaned, slash-normalized, and lowercased forms when they differ. The given
// spelling stays first so callers preferring it keep seeing it first.
func dirVariants(p string) []string {
	cleaned := filepath.Clean(p)
	slash := filepath.ToSlash(cleaned)
	out := []string{p}
	for _, v := range []string{cleaned, slash, strings.ToLower(cleaned), strings.ToLower(slash)} {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// sameSpelling reports whether two recorded paths denote the same directory
// after cleaning, slash folding, and ASCII/Unicode case folding.
func sameSpelling(a, b string) bool {
	return strings.EqualFold(filepath.ToSlash(filepath.Clean(a)), filepath.ToSlash(filepath.Clean(b)))
}
