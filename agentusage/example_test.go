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
	_ = agentusage.LoadDefinitions(agentusage.DefinitionsPath())
	// EnableOpenCodeDB(true) opts into opencode's SQLite store when the
	// binary was built with -tags sqlite; call it before Watch.

	for _, p := range agentusage.Discover() {
		w := agentusage.Watch(p.Tool, p.Dir, time.Now())
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
