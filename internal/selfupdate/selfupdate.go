// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

// Package selfupdate replaces the running binary with a newer release.
//
// Fetch the release asset for this GOOS/GOARCH, verify its SHA-256 against the
// checksums the release ships, and rename it over the current executable.
// Nothing is executed before it is verified, and a failed verification leaves
// the running binary untouched.
//
// The dashboard notices the replacement on its own: internal/selfreload
// watches the executable and restarts into whatever is there now, so an update
// applied from another terminal takes effect without anyone quitting.
package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultRepo is the GitHub repository releases are fetched from.
const DefaultRepo = "maci0/toktop"

// maxAssetBytes bounds a download. A release binary that large is a mistake or
// an attack, and either way should not fill the disk.
const maxAssetBytes = 256 << 20

// Release is the subset of a GitHub release that matters here.
type Release struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// Version is the release version without a leading "v".
func (r *Release) Version() string { return strings.TrimPrefix(r.TagName, "v") }

// AssetName is the binary this platform needs from a release. It must match
// what the Makefile's dist target produces, or self-update finds nothing.
func AssetName(version string) string {
	name := fmt.Sprintf("toktop_%s_%s_%s", version, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// checksumsName is the archive the release keeps its checksums.txt in.
func checksumsName(version string) string {
	return fmt.Sprintf("toktop_%s_checksums.tar.gz", version)
}

var client = &http.Client{Timeout: 5 * time.Minute}

// Check queries the latest release. It is never called on the startup path:
// a version check must not stand between the user and the first review.
func Check(ctx context.Context, repo string) (*Release, error) {
	if repo == "" {
		repo = DefaultRepo
	}
	url := "https://api.github.com/repos/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github returned %s for %s", resp.Status, url)
	}
	var rel Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return nil, err
	}
	if rel.TagName == "" {
		return nil, errors.New("release has no tag")
	}
	return &rel, nil
}

// NewerThan reports whether the release is a different version from current.
// Comparison is deliberately exact rather than semver-aware: releases are the
// source of truth, and a "downgrade" published on purpose should be applied.
func (r *Release) NewerThan(current string) bool {
	return r.Version() != "" && r.Version() != strings.TrimPrefix(current, "v")
}

// Apply downloads, verifies, and installs the release over the running
// executable. It returns the path that was replaced.
//
// The new binary is written next to the current one (same filesystem, so the
// rename is atomic) and only renamed after its checksum matches. A failed
// verification leaves the running binary untouched.
func Apply(ctx context.Context, rel *Release) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return "", err
	}
	return applyTo(ctx, rel, self)
}

// applyTo is Apply with an explicit target, so the install path can be tested
// without replacing the test binary.
func applyTo(ctx context.Context, rel *Release, self string) (string, error) {
	want := AssetName(rel.Version())
	sumsFile := checksumsName(rel.Version())
	var assetURL, sumsURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case want:
			assetURL = a.URL
		case sumsFile:
			sumsURL = a.URL
		}
	}
	if assetURL == "" {
		return "", fmt.Errorf("release %s has no asset %s", rel.TagName, want)
	}
	if sumsURL == "" {
		return "", fmt.Errorf("release %s has no %s; refusing to install unverified binary", rel.TagName, sumsFile)
	}

	archive, err := fetch(ctx, sumsURL, 1<<20)
	if err != nil {
		return "", fmt.Errorf("cannot fetch checksums: %w", err)
	}
	sums, err := ChecksumListing(archive)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", sumsFile, err)
	}
	expect, ok := ChecksumFor(sums, want)
	if !ok {
		return "", fmt.Errorf("checksums.txt has no entry for %s", want)
	}

	dir := filepath.Dir(self)
	tmp, err := os.CreateTemp(dir, ".toktop-update-*")
	if err != nil {
		return "", fmt.Errorf("cannot write next to %s: %w", self, err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // no-op once the rename succeeded
	}()

	sum, err := download(ctx, assetURL, tmp)
	if err != nil {
		return "", err
	}
	if sum != expect {
		return "", fmt.Errorf("checksum mismatch for %s: got %s, want %s", want, sum, expect)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return "", err
	}
	if err := install(tmpName, self); err != nil {
		return "", fmt.Errorf("cannot replace %s: %w", self, err)
	}
	return self, nil
}

// install puts the verified binary in place.
//
// A rename is atomic, and on Unix it works even while the old binary is
// running. Windows refuses to replace a running image but does allow renaming
// it out of the way first, so that is what happens there; the displaced file
// is removed on the next update, since it is still locked during this one.
func install(tmpName, self string) error {
	if runtime.GOOS != "windows" {
		return os.Rename(tmpName, self)
	}
	displaced := self + ".old"
	_ = os.Remove(displaced) // left by a previous update, now unlocked
	if err := os.Rename(self, displaced); err != nil {
		return err
	}
	if err := os.Rename(tmpName, self); err != nil {
		// Put the running binary back rather than leaving nothing installed.
		_ = os.Rename(displaced, self)
		return err
	}
	return nil
}

func fetch(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// download streams url into w and returns the hex SHA-256 of what was written.
func download(ctx context.Context, url string, w io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned %s", url, resp.Status)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(resp.Body, maxAssetBytes+1))
	if err != nil {
		return "", err
	}
	if n > maxAssetBytes {
		return "", fmt.Errorf("asset exceeds %d bytes", int64(maxAssetBytes))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ChecksumListing pulls checksums.txt out of the tar.gz the release ships it
// in. Entries are matched by base name, so a wrapper directory around the
// file does not matter; everything else in the archive is skipped.
func ChecksumListing(archive []byte) (string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return "", err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", errors.New("no checksums.txt in the archive")
		}
		if err != nil {
			return "", err
		}
		if filepath.Base(h.Name) != "checksums.txt" {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, 1<<20))
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
}

// ChecksumFor finds one file's expected hash in a `sha256sum` style listing
// ("<hex>  <name>", with an optional binary-mode asterisk).
func ChecksumFor(listing, name string) (string, bool) {
	for line := range strings.SplitSeq(listing, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		sum, file := fields[0], strings.TrimPrefix(fields[1], "*")
		if filepath.Base(file) == name && len(sum) == 64 {
			return strings.ToLower(sum), true
		}
	}
	return "", false
}
