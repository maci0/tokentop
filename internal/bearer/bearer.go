// Package bearer carries one process-wide optional Bearer token applied to
// engine HTTP requests (routers like OmniRoute require API keys even for
// model listing). Set once at startup, before discovery spawns goroutines.
package bearer

import (
	"net/http"
	"sync"
)

var (
	mu  sync.RWMutex
	tok string
)

// Set stores the token used for every subsequent engine request.
func Set(token string) {
	mu.Lock()
	tok = token
	mu.Unlock()
}

// Token returns the configured token, possibly empty.
func Token() string {
	mu.RLock()
	defer mu.RUnlock()
	return tok
}

// Apply sets the Authorization header when a token is configured.
func Apply(req *http.Request) {
	if tok := Token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}
