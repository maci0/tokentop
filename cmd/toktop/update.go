// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package main

import (
	"context"
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

The download is verified against the release's checksums before anything is
replaced; a mismatch leaves the running binary untouched.

Flags:
`)
	fs.PrintDefaults()
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
	var showHelp bool
	fs.BoolVar(&showHelp, "help", false, "show help and exit")
	fs.BoolVar(&showHelp, "h", false, "shorthand for --help")
	// Defining -h/--help as real flags keeps the flag package from treating
	// them as a parse error, so they can land on stdout with exit 0 the way
	// the top-level command's --help does.
	fs.Usage = func() { updateUsage(os.Stderr, fs) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if showHelp {
		updateUsage(out, fs)
		return 0
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "toktop update: unexpected argument %q (see 'toktop update --help')\n", fs.Arg(0))
		return 2
	}

	rel, err := selfupdate.Check(ctx, *repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "toktop: cannot check for updates: %v\n", err)
		return 1
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
	path, err := selfupdate.Apply(ctx, rel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "toktop: update failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "Installed %s to %s\n", rel.TagName, path)
	return 0
}
