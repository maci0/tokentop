// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checksumsArchive packs a listing the way the release workflow does.
func checksumsArchive(t *testing.T, listing string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte(listing)
	if err := tw.WriteHeader(&tar.Header{Name: "checksums.txt", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// releaseServer serves one asset and the checksums archive, optionally lying
// about the hash.
func releaseServer(t *testing.T, payload []byte, sum string) (*httptest.Server, *Release) {
	t.Helper()
	name := AssetName("9.9.9")
	sums := checksumsArchive(t, sum+"  "+name+"\n")
	mux := http.NewServeMux()
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) { w.Write(payload) })
	mux.HandleFunc("/checksums", func(w http.ResponseWriter, r *http.Request) { w.Write(sums) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	rel := &Release{TagName: "v9.9.9"}
	body := `{"tag_name":"v9.9.9","assets":[
		{"name":"` + name + `","browser_download_url":"` + srv.URL + `/asset"},
		{"name":"` + checksumsName("9.9.9") + `","browser_download_url":"` + srv.URL + `/checksums"}]}`
	if err := json.Unmarshal([]byte(body), rel); err != nil {
		t.Fatal(err)
	}
	return srv, rel
}

func TestApplyRejectsChecksumMismatch(t *testing.T) {
	payload := []byte("#!/bin/sh\necho new\n")
	_, rel := releaseServer(t, payload, strings.Repeat("0", 64))

	target := filepath.Join(t.TempDir(), "toktop")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := applyTo(context.Background(), rel, target)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want a checksum mismatch, got %v", err)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "old binary" {
		t.Fatal("a failed verification must leave the existing binary untouched")
	}
	// And no partial download is left lying around.
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".toktop-update-*"))
	if len(matches) != 0 {
		t.Fatalf("temp files left behind: %v", matches)
	}
}

func TestApplyRefusesReleaseWithoutChecksums(t *testing.T) {
	rel := &Release{TagName: "v9.9.9"}
	rel.Assets = append(rel.Assets, struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	}{Name: AssetName("9.9.9"), URL: "http://127.0.0.1:1/asset"})

	if _, err := Apply(context.Background(), rel); err == nil ||
		!strings.Contains(err.Error(), "checksums") {
		t.Fatalf("unverifiable release must be refused, got %v", err)
	}
}

func TestApplyReplacesTargetOnMatch(t *testing.T) {
	payload := []byte("#!/bin/sh\necho new\n")
	h := sha256.Sum256(payload)
	_, rel := releaseServer(t, payload, hex.EncodeToString(h[:]))

	target := filepath.Join(t.TempDir(), "toktop")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := applyTo(context.Background(), rel, target)
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Fatalf("replaced %q, want %q", got, target)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(payload) {
		t.Fatalf("binary not replaced: %q", body)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("replacement is not executable: %v", fi.Mode())
	}
	// No temp files survive a successful install.
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".toktop-update-*"))
	if len(matches) != 0 {
		t.Fatalf("temp files left behind: %v", matches)
	}
}
