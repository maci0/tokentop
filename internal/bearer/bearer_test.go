package bearer

import (
	"net/http"
	"testing"
)

func TestSetAndApply(t *testing.T) {
	t.Cleanup(func() { Set("") })
	Set("")

	req, _ := http.NewRequest("GET", "http://x", nil)
	Apply(req)
	if req.Header.Get("Authorization") != "" {
		t.Error("header must stay unset without a token")
	}

	Set("sk-test")
	if Token() != "sk-test" {
		t.Errorf("Token() = %q", Token())
	}
	Apply(req)
	if got := req.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("Authorization = %q", got)
	}
}
