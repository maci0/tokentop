// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/maci0/toktop/internal/selfupdate"
)

// updateUsage prints the subcommand's help screen to w. Error paths send it
// to stderr; -h/--help sends it to stdout so piping works (`toktop update
// --help | grep repo`), matching how the top-level command treats --help.
func updateUsage(w io.Writer, fs *flag.FlagSet) {
	prev := fs.Output()
	fs.SetOutput(w)
	defer fs.SetOutput(prev)
	fmt.Fprint(w, `toktop update - install the latest release

Usage:
  toktop update [--check] [--repo owner/name]
  toktop update --version

The download is verified against the release's checksums before anything is
replaced; a mismatch leaves the running binary untouched.

Flags:
`)
	fs.PrintDefaults()
	fmt.Fprint(w, `
$GITHUB_TOKEN authenticates GitHub API calls past the anonymous rate limit.
`)
}

// runUpdate implements `toktop update`, which replaces this binary with the
// latest release after verifying its checksum.
//
// A running dashboard needs no restart: it watches its own executable and
// re-execs when it changes (see internal/selfreload), so an update applied in
// another terminal lands in the session already open.
func runUpdate(ctx context.Context, out io.Writer, args []string) int {
	fs := flag.NewFlagSet("toktop update", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	check := fs.Bool("check", false, "report the latest release without installing it")
	repo := fs.String("repo", selfupdate.DefaultRepo, "GitHub repository to fetch releases from")
	var showHelp, showVer bool
	fs.BoolVar(&showHelp, "help", false, "show help and exit")
	fs.BoolVar(&showHelp, "h", false, "show help and exit")
	fs.BoolVar(&showVer, "version", false, "print version and exit")
	// Defining -h/--help as real flags keeps the flag package from treating
	// them as a parse error, so they can land on stdout with exit 0 the way
	// the top-level command's --help does. --version matches the parent.
	fs.Usage = func() { updateUsage(os.Stderr, fs) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if showHelp {
		updateUsage(out, fs)
		return 0
	}
	if showVer {
		fmt.Fprintln(out, "toktop", version)
		return 0
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "toktop update: unexpected argument %q (see 'toktop update --help')\n", fs.Arg(0))
		return 2
	}

	repoName := *repo
	if repoName == "" {
		repoName = selfupdate.DefaultRepo
	}
	if err := selfupdate.ValidateRepo(repoName); err != nil {
		fmt.Fprintf(os.Stderr, "toktop update: %v\n", err)
		return 2
	}

	rel, err := selfupdate.Check(ctx, repoName)
	if err != nil {
		return updateErr("cannot check for updates", err)
	}
	if !rel.NewerThan(version) {
		fmt.Fprintf(out, "toktop %s is current (latest release: %s)\n", version, rel.TagName)
		return 0
	}
	fmt.Fprintf(out, "New release: %s (running %s)\n", rel.TagName, version)
	if *check {
		fmt.Fprintln(out, rel.HTMLURL)
		return 0
	}
	fmt.Fprintf(os.Stderr, "toktop: installing %s...\n", rel.TagName)
	path, err := selfupdate.Apply(ctx, rel)
	if err != nil {
		return updateErr("update failed", err)
	}
	fmt.Fprintf(out, "Installed %s to %s\n", rel.TagName, path)
	return 0
}

// updateErr maps a canceled context to the same 130 the --once path uses
// for Ctrl+C, and prints a short cause instead of leaking context.Canceled.
func updateErr(op string, err error) int {
	if errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "toktop: interrupted")
		return 130
	}
	fmt.Fprintf(os.Stderr, "toktop: %s: %v\n", op, err)
	return 1
}
