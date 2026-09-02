// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package agentusage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	"agy", "claude", "clanker", "codex", "copilot", "crush", "cursor-agent",
	"dsh", "feynman", "gemini", "grok", "kimi", "omp", "opencode", "pi",
	"prime-agent", "qwen",
}

// Agents lists every agent name this package knows, built in and defined.
func Agents() []string {
	defsMu.RLock()
	extra := make([]string, 0, len(defs))
	for name := range defs {
		if !slices.Contains(knownAgents, name) {
			extra = append(extra, name)
		}
	}
	defsMu.RUnlock()

	out := append(append([]string(nil), knownAgents...), extra...)
	sort.Strings(out)
	return out
}

// Spec describes where a defined agent keeps its transcripts, so live usage
// works for agents gauntlet was not compiled to know about (pi and the CLIs
// built on it, in-house wrappers). The records are parsed generically: any
// JSONL whose objects carry recognizable token counters works, and one whose
// objects do not simply reports nothing.
type Spec struct {
	// Roots are directories to search, with ~ expanded.
	Roots []string `json:"roots"`
	// Suffix filters transcript files (default ".jsonl").
	Suffix string `json:"suffix,omitempty"`
	// Cumulative says the counters already include everything before them, so
	// the first value seen becomes a baseline. Default is per message.
	Cumulative bool `json:"cumulative,omitempty"`

	// HeaderCwd says the working directory appears once in a session header
	// rather than on every record, so ownership is decided from the head of
	// the file. Without it, a transcript whose usage lines carry no cwd is
	// attributed by location alone.
	HeaderCwd bool `json:"header_cwd,omitempty"`
}

// canonicalTool is the map key for an agent name. Surrounding whitespace is
// not part of the name, and would otherwise make Watch miss a spec registered
// as "claude " while Discover reports "claude".
func canonicalTool(tool string) string {
	return strings.TrimSpace(tool)
}

// specRoots is the usable transcript directories in spec. Blank entries are
// not roots, matching RegisterSpec.
func specRoots(spec Spec) []string {
	out := make([]string, 0, len(spec.Roots))
	for _, r := range spec.Roots {
		if strings.TrimSpace(r) != "" {
			out = append(out, r)
		}
	}
	return out
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

// definedSpec returns an agent's transcript location, whether compiled in
// (the pi family) or loaded at runtime by LoadDefinitions.
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

// ErrInvalidDefinitions is returned by LoadDefinitions when the file exists
// but is not valid JSON. The json.SyntaxError (or Decoder error) is wrapped,
// so errors.As still recovers the parse position.
var ErrInvalidDefinitions = errors.New("malformed agent definitions")

// LoadDefinitions reads agent definitions from a JSON file, teaching this
// package about agents it was not compiled to know, including where they keep
// their transcripts:
//
//	{"myagent": {"usage": {"roots": ["~/.myagent/sessions"]}}}
//
// A missing file is not an error, since most machines have none. A malformed
// or unreadable one is: running with a half-loaded agent set is worse than
// refusing. The error names the file; errors.Is matches ErrInvalidDefinitions
// when the contents are not valid JSON.
func LoadDefinitions(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("agent definitions %s: %w", path, err)
	}
	var file definitionFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalidDefinitions, path, err)
	}
	for name, def := range file {
		name = canonicalTool(name)
		if name == "" || def.Usage == nil {
			continue // a launch-only definition says nothing about tokens
		}
		spec := Spec{
			Roots:      def.Usage.Roots,
			Suffix:     def.Usage.Suffix,
			Cumulative: def.Usage.Cumulative,
			HeaderCwd:  def.Usage.HeaderCwd,
		}
		if len(specRoots(spec)) == 0 {
			continue
		}
		defsMu.Lock()
		defs[name] = spec
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
