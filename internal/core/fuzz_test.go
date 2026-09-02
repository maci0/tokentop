package core

import (
	"testing"
	"unicode/utf8"
)

// FuzzSanitizeText drives the terminal sanitizer with arbitrary bytes.
// Engine names, ingest fields, and SSH vitals all land here before they
// are drawn, so a malformed sequence must not panic, must not grow the
// string, must be idempotent, and must not leave C0/C1 controls, ESC, DEL,
// bidi marks, or the zero-width format characters the sanitizer claims to
// strip.
func FuzzSanitizeText(f *testing.F) {
	for _, seed := range []string{
		"",
		"plain text",
		"model: llama-3.1-8b-instruct.Q4_K_M",
		"héllo wörld ✓ 日本語 🎉",
		"line one\nline two\ttabbed",
		"\x1b]52;c;YU9UQw==\x07stealth",
		"\x1b]0;evil title\x1b\\x",
		"\x1b[2J\x1b[3;5Hfake",
		"ok\x1b[31mred\x1b[0m",
		"a\xc2\x9b2Jb",
		"a\x9b2Jb",
		"safe\rERASED",
		"ab\bX",
		"ring\a",
		"\x1bP1$r\x1b\\after",
		"pre\x1b",
		"\x1b]52;c;no terminator at all",
		"a\xc2\x85b\xc2\x90c",
		"a\x00b\x01c\x1fd",
		"a\x7fb",
		"\u202eedualc",
		"cla\u200eude\u200f",
		"\u2066claude\u2069",
		"clau\u200bde",
		"\ufeffclaude",
		"cla\u00adude",
		"👩\u200d💻",
		"\x1b]0;" + string(make([]byte, 256)) + "\x07tail",
		"\x1b[" + string([]byte{0x30, 0x3f, 0x20, 0x2f}) + "@done",
		"\xff\xfe\xfd",
		"\xc0\x80overlong",
		"\x1b]\x1b]52;c;nested\x07x",
		"café \x1b[31m rouge",
		"\xc2\v\xad",
		"\xc2\xad",
		"\xc2",
		"clau\U000E0064e",
		"\U000E0001claude",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		got := SanitizeText(s)
		if len(got) > len(s) {
			t.Fatalf("SanitizeText grew the string: %d -> %d", len(s), len(got))
		}
		if again := SanitizeText(got); again != got {
			t.Fatalf("SanitizeText is not idempotent: %q -> %q -> %q", s, got, again)
		}
		if again := SanitizeText(s); again != got {
			t.Fatal("SanitizeText is not deterministic")
		}
		if !utf8.ValidString(got) {
			t.Fatalf("SanitizeText emitted invalid UTF-8: %q from %q", got, s)
		}
		assertSanitized(t, s, got)
	})
}

func assertSanitized(t *testing.T, in, out string) {
	t.Helper()
	for i := 0; i < len(out); {
		c := out[i]
		if c == 0x1b || c == 0x7f || (c < 0x20 && c != '\n' && c != '\t') {
			t.Fatalf("control byte %#02x remains at %d in %q (from %q)", c, i, out, in)
		}
		r, size := utf8.DecodeRuneInString(out[i:])
		if r == utf8.RuneError && size == 1 {
			if c >= 0x80 && c <= 0x9f {
				t.Fatalf("raw C1 byte %#02x remains at %d in %q (from %q)", c, i, out, in)
			}
			i++
			continue
		}
		if r >= 0x80 && r <= 0x9f {
			t.Fatalf("C1 rune %U remains in %q (from %q)", r, out, in)
		}
		if unsafeRune(r) {
			t.Fatalf("unsafe rune %U remains in %q (from %q)", r, out, in)
		}
		i += size
	}
}
