// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package core

import (
	"strings"

	"github.com/rivo/uniseg"
)

// TruncateClusters caps s at n grapheme clusters, cutting only between
// clusters. A grapheme cluster is one user-perceived character: a base letter
// with its combining marks, an emoji ZWJ sequence, a flag built from two
// regional indicators. Cutting inside one renders garbage (a dangling
// zero-width joiner, half a flag), so length caps on retained text must move
// in whole clusters even though they are counted in code points.
//
// The result therefore never holds more than n characters and never ends
// mid-character. n <= 0 yields "".
func TruncateClusters(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if uniseg.GraphemeClusterCount(s) <= n {
		return s
	}
	var b strings.Builder
	state := -1
	for remaining := n; remaining > 0 && s != ""; remaining-- {
		var cluster string
		cluster, s, _, state = uniseg.FirstGraphemeClusterInString(s, state)
		b.WriteString(cluster)
	}
	return b.String()
}
