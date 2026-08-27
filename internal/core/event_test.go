package core

import "testing"

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
