package httperr

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSnippetBoundedAndOneLine(t *testing.T) {
	got := Snippet([]byte(strings.Repeat("boom ", 400) + "\r\n\ttail"))
	if strings.ContainsAny(got, "\n\r\t") {
		t.Errorf("snippet kept line breaks: %q", got)
	}
	if n := len([]rune(got)); n > SnippetCap {
		t.Errorf("snippet = %d runes, cap is %d", n, SnippetCap)
	}
	if got := Snippet(nil); got != "" {
		t.Errorf("empty body snippet = %q, want empty", got)
	}
}

func TestStatusCarriesStatusLineAndBodySnippet(t *testing.T) {
	resp := &http.Response{
		Status: "500 Internal Server Error",
		Body:   io.NopCloser(strings.NewReader("CUDA out of memory\n")),
	}
	err := Status("http://x/metrics", resp)
	if err == nil {
		t.Fatal("Status = nil, want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "http://x/metrics") ||
		!strings.Contains(msg, "500 Internal Server Error") ||
		!strings.Contains(msg, "CUDA out of memory") {
		t.Errorf("error = %q, want url, status and body snippet", msg)
	}

	resp = &http.Response{Status: "503 Service Unavailable", Body: io.NopCloser(strings.NewReader(""))}
	if msg := Status("http://x/health", resp).Error(); strings.HasSuffix(msg, ": ") || strings.Contains(msg, ": \n") {
		t.Errorf("empty body produced dangling separator: %q", msg)
	}
}
