// Sanitization for untrusted text rendered into a terminal: engine-supplied
// model names and versions, agent-event fields, SSH-sourced host vitals.
// Escape sequences and control characters in such strings would otherwise be
// interpreted by the operator's terminal (clipboard hijack via OSC 52, cursor
// redraw via CSI, title spoofing), the terminal equivalent of XSS.

package core

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// SanitizeText strips ANSI/ECMA-48 escape sequences (CSI, OSC, DCS, SOS, PM,
// APC and two-byte forms) plus C0 and C1 control characters from s. Newlines
// and tabs survive so layout text is unaffected. Bidi overrides, isolates,
// and zero-width format characters that spoof identity-bearing fields
// (agent names, host labels) are removed too. ZWJ (U+200D) stays so emoji
// sequences remain whole. Other multi-byte runes are preserved.
func SanitizeText(s string) string {
	if !needsSanitize(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b:
			i = skipEscapeSeq(s, i+1)
		case c == '\n' || c == '\t':
			b.WriteByte(c)
			i++
		case c < 0x20 || c == 0x7f:
			i++
		case c < 0x80:
			b.WriteByte(c)
			i++
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			if unsafeRune(r) {
				i += size
				continue
			}
			b.WriteString(s[i : i+size])
			i += size
		}
	}
	return b.String()
}

// unsafeRune reports a code point that must not reach a terminal or an
// identity field: C1 controls, bidi overrides/isolates, and the zero-width
// format characters used to spoof or hide names. U+200D (ZWJ) is kept so
// 👩‍💻-style emoji survive as one cluster.
func unsafeRune(r rune) bool {
	if r >= 0x80 && r <= 0x9f {
		return true
	}
	if unicode.Is(unicode.Bidi_Control, r) {
		return true
	}
	switch r {
	case 0x00AD, // soft hyphen
		0x034F,                         // combining grapheme joiner
		0x180E,                         // mongolian vowel separator
		0x200B,                         // zero-width space
		0x200C,                         // zero-width non-joiner
		0x2060,                         // word joiner
		0x2061, 0x2062, 0x2063, 0x2064, // invisible math operators
		0xFEFF: // BOM / ZWNBSP
		return true
	}
	return false
}

// needsSanitize reports whether s contains any byte that SanitizeText would
// remove. ASCII controls (except newline/tab) and DEL are one class; any
// non-ASCII byte takes the slow path because bidi and zero-width marks are
// 2- and 3-byte UTF-8 that the previous C2-C3-only check missed.
func needsSanitize(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 0x20 && c != '\n' && c != '\t') || c == 0x7f || c >= 0x80 {
			return true
		}
	}
	return false
}

// skipEscapeSeq consumes one complete escape sequence starting just after the
// ESC byte and returns the index of the first byte past it. Unterminated
// sequences swallow the rest of the string: fail closed rather than emit
// half a payload as visible text.
func skipEscapeSeq(s string, i int) int {
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[': // CSI: parameter bytes 0x30-0x3F, intermediates 0x20-0x2F, final 0x40-0x7E
		i++
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x3f {
			i++
		}
		if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7e {
			i++
		}
	case ']', 'P', 'X', '^', '_': // OSC/DCS/SOS/PM/APC: terminated by BEL or ST
		i++
		for i < len(s) {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
			i++
		}
	default: // two-byte escape: ESC plus one byte
		i++
	}
	return i
}
