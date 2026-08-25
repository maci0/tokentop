package bearer

import (
	"net/http"
	"testing"
)

func resetAllowed() {
	mu.Lock()
	allowed = map[string]bool{}
	mu.Unlock()
}

func TestSetAndApply(t *testing.T) {
	t.Cleanup(func() { Set(""); resetAllowed() })
	Set("")

	req, _ := http.NewRequest("GET", "http://x/metrics", nil)
	Apply(req)
	if req.Header.Get("Authorization") != "" {
		t.Error("header must stay unset without a token")
	}

	Set("sk-test")
	if Token() != "sk-test" {
		t.Errorf("Token() = %q", Token())
	}
	// A token alone is not enough: undiscovered, un-admitted destinations
	// get nothing, so a hostile listener on a probed port cannot collect it.
	Apply(req)
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q for a destination never allowed", got)
	}

	Allow("http://x")
	Apply(req)
	if got := req.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("Authorization = %q after Allow, want %q", got, "Bearer sk-test")
	}
}

func TestAllowScopesByOrigin(t *testing.T) {
	t.Cleanup(func() { Set(""); resetAllowed() })
	Set("sk-test")

	cases := []struct {
		allowed string
		target  string
		want    bool
	}{
		{"http://10.0.0.5:8000/", "http://10.0.0.5:8000/v1/models", true},
		{"http://10.0.0.5:8000", "http://10.0.0.5:8001/v1/models", false}, // other port
		{"http://127.0.0.1:20128", "https://127.0.0.1:20128/v1/models", false},
		{"http://omni.lan", "http://omni.lan/v1/models", true}, // default port implied
		{"http://OMNI.lan", "http://omni.lan/v1/models", true}, // host case folds
		{"http://[::1]:8420", "http://[::1]:8420/api/version", true},
		{"http://[::1]:8420", "http://127.0.0.1:8420/api/version", false},
	}
	for _, tc := range cases {
		resetAllowed()
		Allow(tc.allowed)
		req, err := http.NewRequest("GET", tc.target, nil)
		if err != nil {
			t.Fatalf("bad target %q: %v", tc.target, err)
		}
		Apply(req)
		got := req.Header.Get("Authorization") == "Bearer sk-test"
		if got != tc.want {
			t.Errorf("Allow(%q) then request %q: attached = %v, want %v",
				tc.allowed, tc.target, got, tc.want)
		}
	}

	// Malformed bases and non-HTTP(S) schemes are refused rather than admitted.
	for _, bad := range []string{"", "not a url", "ftp://x", "http://"} {
		resetAllowed()
		Allow(bad)
		if len(allowed) != 0 {
			t.Errorf("Allow(%q) admitted something", bad)
		}
	}
}
