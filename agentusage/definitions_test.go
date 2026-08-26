// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package agentusage

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// addDef registers a definition directly and removes it when the test ends.
func addDef(t *testing.T, name string, spec Spec) {
	t.Helper()
	defsMu.Lock()
	defs[name] = spec
	defsMu.Unlock()
	t.Cleanup(func() {
		defsMu.Lock()
		delete(defs, name)
		defsMu.Unlock()
	})
}

// Agents backs the --agents picker and Discover: built-ins must stay listed,
// runtime definitions join them sorted, and a definition shadowing a built-in
// must not appear twice.
func TestAgentsMergesBuiltinsAndDefinitions(t *testing.T) {
	addDef(t, "zzdefined", Spec{Roots: []string{"~/.zz/sessions"}})
	addDef(t, "codex", Spec{Roots: []string{"~/.shadowing/sessions"}})

	got := Agents()
	if !slices.Contains(got, "claude") {
		t.Errorf("built-in claude missing from %v", got)
	}
	if !slices.Contains(got, "zzdefined") {
		t.Errorf("defined agent missing from %v", got)
	}
	if !slices.IsSorted(got) {
		t.Errorf("agent list not sorted: %v", got)
	}
	n := 0
	for _, a := range got {
		if a == "codex" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("codex appears %d times, want exactly once: %v", n, got)
	}
}

// A machine without agent definitions is ordinary; the loader must say so by
// succeeding quietly.
func TestLoadDefinitionsMissingFileIsNotAnError(t *testing.T) {
	if err := LoadDefinitions(filepath.Join(t.TempDir(), "absent.json")); err != nil {
		t.Fatalf("missing file = %v, want nil", err)
	}
}

// A malformed file must be refused: running with a half-loaded agent set is
// worse than refusing.
func TestLoadDefinitionsRejectsMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	if err := os.WriteFile(path, []byte("{oops"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := LoadDefinitions(path); err == nil {
		t.Fatal("malformed definitions accepted")
	}
}

// Only token-bearing definitions mean anything here: a launch-only entry says
// nothing about transcripts and must be skipped, a blank name is unusable,
// and a full entry carries every usage field through.
func TestLoadDefinitionsRegistersOnlyTokenBearingSpecs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	body := `{
		"launchonly": {"cmd": ["x"]},
		"noroots": {"usage": {}},
		"": {"usage": {"roots": ["/tmp"]}},
		"full": {"usage": {"roots": ["~/.full/sessions"], "suffix": ".ndjson",
			"cumulative": true, "header_cwd": true}}
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := LoadDefinitions(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defsMu.Lock()
		delete(defs, "full")
		defsMu.Unlock()
	})

	if _, ok := definedSpec("launchonly"); ok {
		t.Error("launch-only definition registered")
	}
	if _, ok := definedSpec("noroots"); ok {
		t.Error("roots-less definition registered")
	}
	if _, ok := definedSpec(""); ok {
		t.Error("blank-named definition registered")
	}
	spec, ok := definedSpec("full")
	if !ok {
		t.Fatal("full definition not registered")
	}
	want := Spec{Roots: []string{"~/.full/sessions"}, Suffix: ".ndjson",
		Cumulative: true, HeaderCwd: true}
	if !slices.Equal(spec.Roots, want.Roots) || spec.Suffix != want.Suffix ||
		spec.Cumulative != want.Cumulative || spec.HeaderCwd != want.HeaderCwd {
		t.Errorf("spec = %+v, want %+v", spec, want)
	}
}

// DefinitionsPath follows gauntlet's file so one definition serves both tools;
// GAUNTLET_HOME wins over the home-relative default.
func TestDefinitionsPathPrefersGauntletHome(t *testing.T) {
	gh := t.TempDir()
	t.Setenv("GAUNTLET_HOME", gh)
	if got, want := DefinitionsPath(), filepath.Join(gh, "agents.json"); got != want {
		t.Errorf("DefinitionsPath() = %q, want %q", got, want)
	}

	home := t.TempDir()
	t.Setenv("GAUNTLET_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on windows
	got := DefinitionsPath()
	if !strings.HasSuffix(got, filepath.Join(".gauntlet", "agents.json")) ||
		!strings.HasPrefix(got, home) {
		t.Errorf("DefinitionsPath() = %q, want the home-relative default under %q", got, home)
	}
}
