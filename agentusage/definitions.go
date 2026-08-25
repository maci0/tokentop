// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package agentusage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// knownAgents are the CLIs this package recognizes by name.
//
// Recognition and readability are separate questions: an agent here is one
// whose process can be identified, which is what makes it appear in Discover.
// Whether its tokens can be read is decided by the adapters in watch.go, and
// most of this list keeps no transcript worth reading.
var knownAgents = []string{
	"agy", "claude", "clanker", "codex", "cursor-agent", "dsh", "feynman",
	"gemini", "grok", "kimi", "omp", "opencode", "pi", "prime-agent", "qwen",
}

// Agents lists every agent name this package knows, built in and defined.
func Agents() []string {
	defsMu.RLock()
	extra := make([]string, 0, len(defs))
	for name := range defs {
		if !isKnown(name) {
			extra = append(extra, name)
		}
	}
	defsMu.RUnlock()

	out := append(append([]string(nil), knownAgents...), extra...)
	sort.Strings(out)
	return out
}

func isKnown(name string) bool {
	for _, a := range knownAgents {
		if a == name {
			return true
		}
	}
	return false
}

var (
	defsMu sync.RWMutex
	// The pi family keeps ordinary JSONL transcripts, so they need locations
	// rather than adapters. They ship here so both this tool and gauntlet read
	// them without a definitions file.
	defs = map[string]Spec{
		"pi":          {Roots: []string{"~/.pi/agent/sessions"}},
		"prime-agent": {Roots: []string{"~/.prime/agent/sessions"}},
		"feynman":     {Roots: []string{"~/.feynman/sessions"}},
	}
)

// definedSpec returns a transcript location supplied at runtime rather than
// compiled in.
func definedSpec(tool string) (Spec, bool) {
	defsMu.RLock()
	defer defsMu.RUnlock()
	s, ok := defs[tool]
	return s, ok
}

// definitionFile mirrors the agent definitions gauntlet keeps in
// ~/.gauntlet/agents.json. Only the transcript location matters here: the rest
// of that file describes how to launch an agent, which is not this package's
// business.
type definitionFile map[string]struct {
	Usage *struct {
		Roots      []string `json:"roots"`
		Suffix     string   `json:"suffix,omitempty"`
		Cumulative bool     `json:"cumulative,omitempty"`
		HeaderCwd  bool     `json:"header_cwd,omitempty"`
	} `json:"usage,omitempty"`
}

// LoadDefinitions reads agent definitions from a JSON file, teaching this
// package about agents it was not compiled to know, including where they keep
// their transcripts:
//
//	{"myagent": {"usage": {"roots": ["~/.myagent/sessions"]}}}
//
// A missing file is not an error, since most machines have none. A malformed
// one is: running with a half-loaded agent set is worse than refusing.
func LoadDefinitions(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var file definitionFile
	if err := json.Unmarshal(data, &file); err != nil {
		return err
	}
	for name, def := range file {
		if def.Usage == nil || len(def.Usage.Roots) == 0 || strings.TrimSpace(name) == "" {
			continue // a launch-only definition says nothing about tokens
		}
		defsMu.Lock()
		defs[name] = Spec{
			Roots:      def.Usage.Roots,
			Suffix:     def.Usage.Suffix,
			Cumulative: def.Usage.Cumulative,
			HeaderCwd:  def.Usage.HeaderCwd,
		}
		defsMu.Unlock()
	}
	return nil
}

// DefinitionsPath is where agent definitions live by default. It follows
// gauntlet's location so one file serves both tools.
func DefinitionsPath() string {
	if h := os.Getenv("GAUNTLET_HOME"); h != "" {
		return filepath.Join(h, "agents.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gauntlet", "agents.json")
}
