// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build darwin

package agentusage

import (
	"slices"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// macOS file systems (HFS+, APFS) look names up normalization-insensitively
// and, in the default configuration, case-insensitively, while storing
// whichever form was created: "café" spelled NFC and NFD address one and the
// same directory yet differ byte for byte, and so do "Users" and "users". An
// agent records the spelling its own argv carried, so directory identity
// cannot be decided by bytes alone here: a session started from an NFC
// spelling would silently vanish from a review resolved through NFD paths.

// dirVariants lists the spellings p can be recorded under: itself, then its
// NFC and NFD forms when they differ. The given spelling stays first so
// callers preferring it keep seeing it first.
func dirVariants(p string) []string {
	out := []string{p}
	for _, v := range []string{norm.NFC.String(p), norm.NFD.String(p)} {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// sameSpelling reports whether two recorded paths denote the same directory:
// byte equality, or agreement once both are brought to one normalization form
// and compared without regard to case (the APFS default).
func sameSpelling(a, b string) bool {
	return a == b || strings.EqualFold(norm.NFC.String(a), norm.NFC.String(b))
}
