// Sanitization for untrusted text rendered into a terminal: engine-supplied
// model names and versions, agent-event fields, SSH-sourced host vitals.
// Escape sequences and control characters in such strings would otherwise be
// interpreted by the operator's terminal (clipboard hijack via OSC 52, cursor
// redraw via CSI, title spoofing), the terminal equivalent of XSS.

package core

import (
	"strings"
	"unicode/utf8"
)

// SanitizeText strips ANSI/ECMA-48 escape sequences (CSI, OSC, DCS, SOS, PM,
// APC and two-byte forms) plus C0 and C1 control characters from s. Newlines
// and tabs survive so layout text is unaffected. Other multi-byte runes are
// preserved: only control characters and well-formed escape sequences are
// removed.
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
			if r >= 0x80 && r <= 0x9f {
				// C1 control (e.g. U+009B, the 8-bit CSI): unsafe to render
				i += size
				continue
			}
			b.WriteString(s[i : i+size])
			i += size
		}
	}
	return b.String()
}

// needsSanitize reports whether s contains any byte that SanitizeText would
// remove. Such bytes are the ASCII controls below 0x20 (including ESC), DEL,
// or lead bytes 0xC2-0xC3, which may encode a C1 control; other multi-byte
// UTF-8 takes the slow path.
func needsSanitize(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 0x20 && c != '\n' && c != '\t') || c == 0x7f {
			return true
		}
		if c >= 0xc2 && c <= 0xc3 { // possible UTF-8 encoding of a C1 control
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
