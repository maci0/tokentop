package core

import (
	"strings"
	"testing"
)

func TestSanitizeTextPassthrough(t *testing.T) {
	cases := []string{
		"",
		"plain text",
		"model: llama-3.1-8b-instruct.Q4_K_M",
		"shell(git status)",
		"héllo wörld ✓ 日本語 🎉",
		"line one\nline two\ttabbed",
	}
	for _, in := range cases {
		if got := SanitizeText(in); got != in {
			t.Errorf("SanitizeText(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestSanitizeTextStripsTerminalInjection(t *testing.T) {
	cases := []struct{ name, in string }{
		{"osc52 clipboard hijack", "\x1b]52;c;YU9UQw==\x07stealth"},
		{"osc0 title spoof (ST terminated)", "\x1b]0;evil title\x1b\\x"},
		{"csi cursor move + redraw", "\x1b[2J\x1b[3;5Hfake"},
		{"sgr recolor", "ok\x1b[31mred\x1b[0m"},
		{"8-bit CSI", "a\xc2\x9b2Jb"},
		{"carriage return overwrite", "safe\rERASED"},
		{"backspace", "ab\bX"},
		{"bel", "ring\a"},
		{"dcs", "\x1bP1$r\x1b\\after"},
		{"bare esc", "pre\x1b"},
		{"esc mid-sequence truncation", "\x1b]52;c;no terminator at all"},
		{"c1 others", "a\xc2\x85b\xc2\x90c"},
		{"nul and controls", "a\x00b\x01c\x1fd"},
		{"del", "a\x7fb"},
	}
	for _, tc := range cases {
		got := SanitizeText(tc.in)
		for i := 0; i < len(got); i++ {
			c := got[i]
			if (c < 0x20 && c != '\n' && c != '\t') || c == 0x7f || c == 0x1b {
				t.Errorf("%s: SanitizeText(%q) = %q still contains control byte %#x", tc.name, tc.in, got, c)
			}
		}
	}
}

func TestSanitizeTextKeepsVisibleTailAfterStrippedPayload(t *testing.T) {
	if got := SanitizeText("\x1b]52;c;QUJD\x07visible"); !strings.HasSuffix(got, "visible") || got != "visible" {
		t.Errorf("OSC payload leaked or tail lost: %q", got)
	}
	if got := SanitizeText("\x1b[31mred\x1b[0mclean"); got != "redclean" {
		t.Errorf("CSI handling broke visible text: %q", got)
	}
}

func TestSanitizeTextPreservesUTF8(t *testing.T) {
	in := "café \x1b[31m rouge"
	got := SanitizeText(in)
	want := "café  rouge" // the SGR sequence vanishes, é survives
	if got != want {
		t.Errorf("SanitizeText(%q) = %q, want %q", in, got, want)
	}
	if !strings.Contains(SanitizeText("日本語モデル"), "日本語モデル") {
		t.Error("multibyte text must survive sanitization")
	}
}

func TestSanitizeTextNewlineTabSurvive(t *testing.T) {
	if got := SanitizeText("a\nb\tc\rd"); got != "a\nb\tcd" {
		t.Errorf("newline/tab handling changed: %q", got)
	}
}

func TestSanitizeTextStripsBidiAndZeroWidth(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"RLO spoofs claude", "\u202eedualc", "edualc"},
		{"LRM/RLM marks", "cla\u200eude\u200f", "claude"},
		{"isolate wrap", "\u2066claude\u2069", "claude"},
		{"zero-width space in name", "clau\u200bde", "claude"},
		{"BOM prefix", "\ufeffclaude", "claude"},
		{"soft hyphen", "cla\u00adude", "claude"},
		{"ZWJ emoji stays one sequence", "👩\u200d💻", "👩\u200d💻"},
	}
	for _, tc := range cases {
		if got := SanitizeText(tc.in); got != tc.want {
			t.Errorf("%s: SanitizeText(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}
