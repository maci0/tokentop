package core

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestHasAgentID(t *testing.T) {
	events := []AgentEvent{
		{ID: "a", Agent: "coder"},
		{ID: "b", Agent: "coder"},
		{Agent: "no-id"},
	}
	if !HasAgentID(events, "a") || !HasAgentID(events, "b") {
		t.Fatal("present ids must match")
	}
	if HasAgentID(events, "c") {
		t.Fatal("unknown id matched")
	}
	if HasAgentID(events, "") {
		t.Fatal("empty id must never match")
	}
	if HasAgentID(nil, "a") {
		t.Fatal("empty feed matched")
	}
}

// The ingest id-dedup window is the retained event ring. README's Agent feed
// table must name AgentHistoryLen so a ring bump cannot ship with a stale cap.
func TestREADMEDocumentsAgentHistoryLen(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("last %d events", AgentHistoryLen)
	if !strings.Contains(string(b), want) {
		t.Fatalf("README.md ingest id row must say %q (matches AgentHistoryLen)", want)
	}
}
