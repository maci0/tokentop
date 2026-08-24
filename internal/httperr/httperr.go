// Package httperr renders non-200 engine responses as single-line errors.
// Engines explain rejections in their response bodies ("model not found",
// bad api key, OOM) and the status line alone does not, so the body rides
// along as a bounded snippet.
package httperr

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// SnippetCap bounds how much of an error response body is quoted into
// failure messages.
const SnippetCap = 256

// Status consumes resp's body (the caller must still close it) and returns
// an error carrying the status line plus a one-line snippet of the body.
func Status(url string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4*SnippetCap))
	msg := fmt.Sprintf("%s: http %s", url, resp.Status)
	if s := Snippet(b); s != "" {
		msg += ": " + s
	}
	return errors.New(msg)
}

// Snippet collapses raw bytes to at most SnippetCap runes on one line.
func Snippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	r := []rune(s)
	if len(r) > SnippetCap {
		r = r[:SnippetCap]
	}
	return string(r)
}
