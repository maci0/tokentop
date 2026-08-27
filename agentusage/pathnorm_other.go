// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build !darwin && !windows

package agentusage

// Outside macOS and Windows no file system in play applies Unicode
// normalization or case-insensitive lookup: on Linux two spellings of one
// accented or differently-cased name are genuinely different directories, so
// equating them would attribute another project's tokens to this review.
// Paths are therefore compared exactly as recorded, and each has exactly one
// spelling.

// dirVariants lists the spellings p can be recorded under: just itself.
func dirVariants(p string) []string { return []string{p} }

// sameSpelling reports whether two recorded paths denote the same directory.
func sameSpelling(a, b string) bool { return a == b }
