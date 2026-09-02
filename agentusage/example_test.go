// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package agentusage_test

import (
	"fmt"
	"time"

	"github.com/maci0/toktop/agentusage"
)

func Example() {
	// Typical integration: load extra agent definitions, discover running
	// agents, and tail the one that is working in this directory.
	if err := agentusage.LoadDefinitions(agentusage.DefinitionsPath()); err != nil {
		fmt.Println(err) // malformed or unreadable; a missing file is not an error
	}
	// EnableOpenCodeDB reports whether this build can read opencode's store.
	// Call it before Watch; a false return is a build without -tags sqlite.

	for _, p := range agentusage.Discover() {
		w := p.Watch(time.Now())
		if w == nil {
			continue // this agent keeps no readable transcript
		}
		s := w.Poll()
		if s.Empty() {
			continue
		}
		fmt.Printf("%s pid %d: %d output, %d prompt\n", p.Tool, p.PID, s.Output, s.Input)
	}
}

func ExampleRate() {
	t0 := time.Unix(1_000_000, 0)
	prev := agentusage.Sample{Output: 100, At: t0}
	cur := agentusage.Sample{Output: 350, At: t0.Add(time.Second)}
	r, ok := agentusage.Rate(prev, cur)
	fmt.Println(int(r), ok)
	// Output: 250 true
}

func ExampleInputRate() {
	t0 := time.Unix(1_000_000, 0)
	prev := agentusage.Sample{Input: 80, At: t0}
	cur := agentusage.Sample{Input: 200, At: t0.Add(time.Second)}
	r, ok := agentusage.InputRate(prev, cur)
	fmt.Println(int(r), ok)
	// Output: 120 true
}

func ExampleProcess_Watch() {
	for _, p := range agentusage.Discover() {
		if w := p.Watch(time.Now()); w != nil {
			fmt.Println(w.Tool(), w.Poll().Output)
		}
	}
}

func ExampleLoadDefinitions() {
	err := agentusage.LoadDefinitions("/no/such/agents.json")
	fmt.Println(err)
	// Output: <nil>
}

func ExampleRegisterSpec() {
	err := agentusage.RegisterSpec("", agentusage.Spec{})
	fmt.Println(err)
	// Output: usage spec needs an agent name
}

func ExampleSample_Empty() {
	fmt.Println(agentusage.Sample{}.Empty())
	fmt.Println(agentusage.Sample{Thinking: 12}.Empty())
	// Output:
	// true
	// false
}
