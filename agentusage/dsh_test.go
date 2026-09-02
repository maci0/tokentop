// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package agentusage

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func dshHeader(cwd string) string {
	return `{"type":"session","version":0,"id":"s","cwd":` + jsonPath(cwd) + `,"delegationDepth":0}`
}

func dshMessage(out, think, in int) string {
	return `{"type":"assistant/message","usage":{"inputTokens":` +
		strconv.Itoa(in) + `,"outputTokens":` + strconv.Itoa(out) + `,"reasoningTokens":` + strconv.Itoa(think) +
		`,"cacheReadTokens":900}}`
}

func dshUsageChunk(out, think, in int) string {
	return `{"type":"assistant/chunk","data":{"chunk":{"type":"usage","usage":{"inputTokens":` +
		strconv.Itoa(in) + `,"outputTokens":` + strconv.Itoa(out) + `,"reasoningTokens":` + strconv.Itoa(think) + `}}}}`
}

func zstdFrame(t *testing.T, plain string) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderCRC(true), zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	return enc.EncodeAll([]byte(plain), nil)
}

func appendBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
}

func TestParseDshCountsAssistantMessageOnly(t *testing.T) {
	v, _, ok := parseDsh([]byte(dshMessage(260, 90, 1200)))
	if !ok {
		t.Fatal("assistant/message with usage was rejected")
	}
	if v.output != 260 || v.thinking != 90 || v.input != 1200 {
		t.Fatalf("got %+v, want output 260 thinking 90 input 1200", v)
	}

	if _, _, ok := parseDsh([]byte(dshUsageChunk(260, 90, 1200))); ok {
		t.Fatal("usage chunk must not count: it repeats the message")
	}

	legacy := `{"type":"assistant-message","usage":{"prompt_tokens":50,"completion_tokens":12,"reasoning_tokens":4}}`
	v, _, ok = parseDsh([]byte(legacy))
	if !ok || v.output != 12 || v.thinking != 4 || v.input != 50 {
		t.Fatalf("snake_case spelling lost: ok=%v %+v", ok, v)
	}
}

func TestDshZstdSessionLog(t *testing.T) {
	store := withStore(t, "dsh")
	work := t.TempDir()
	path := filepath.Join(store, "session.jsonl.zstd")

	w := Watch("dsh", work, time.Now())
	if w == nil {
		t.Fatal("dsh should be supported")
	}

	header := zstdFrame(t, dshHeader(work)+"\n")
	first := zstdFrame(t, dshUsageChunk(110, 40, 800)+"\n"+dshMessage(110, 40, 800)+"\n")
	appendBytes(t, path, append(header, first...))
	w.poll(nil)
	if got := w.Sample().Output; got != 110 {
		t.Fatalf("output %d, want 110 (message only, not the chunk)", got)
	}
	if got := w.Sample().Thinking; got != 40 {
		t.Fatalf("thinking %d, want 40", got)
	}
	if got := w.Sample().Input; got != 800 {
		t.Fatalf("input %d, want 800", got)
	}

	second := zstdFrame(t, dshMessage(150, 50, 200)+"\n")
	appendBytes(t, path, second)
	w.poll(nil)
	if got := w.Sample().Output; got != 260 {
		t.Fatalf("output %d, want 260 after second frame", got)
	}
	if got := w.Sample().Thinking; got != 90 {
		t.Fatalf("thinking %d, want 90 after second frame", got)
	}
}

func TestDshZstdIgnoresOtherProjects(t *testing.T) {
	store := withStore(t, "dsh")
	work, other := t.TempDir(), t.TempDir()
	w := Watch("dsh", work, time.Now())

	mine := filepath.Join(store, "mine", "session.jsonl.zstd")
	theirs := filepath.Join(store, "theirs", "session.jsonl.zstd")
	if err := os.MkdirAll(filepath.Dir(mine), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(theirs), 0o700); err != nil {
		t.Fatal(err)
	}
	appendBytes(t, mine, append(zstdFrame(t, dshHeader(work)+"\n"), zstdFrame(t, dshMessage(10, 0, 1)+"\n")...))
	appendBytes(t, theirs, append(zstdFrame(t, dshHeader(other)+"\n"), zstdFrame(t, dshMessage(9999, 0, 1)+"\n")...))
	w.poll(nil)
	if got := w.Sample().Output; got != 10 {
		t.Fatalf("another project's tokens leaked in: %d", got)
	}
}

func TestDshZstdIncompleteFrameIsReread(t *testing.T) {
	store := withStore(t, "dsh")
	work := t.TempDir()
	path := filepath.Join(store, "session.jsonl.zstd")
	w := Watch("dsh", work, time.Now())

	header := zstdFrame(t, dshHeader(work)+"\n")
	full := zstdFrame(t, dshMessage(77, 3, 10)+"\n")
	appendBytes(t, path, header)
	appendBytes(t, path, full[:len(full)/2])
	w.poll(nil)
	if got := w.Sample().Output; got != 0 {
		t.Fatalf("half frame counted as %d", got)
	}

	appendBytes(t, path, full[len(full)/2:])
	w.poll(nil)
	if got := w.Sample().Output; got != 77 {
		t.Fatalf("completed frame lost: %d", got)
	}
}

func TestDshPlainJSONLStillWorks(t *testing.T) {
	store := withStore(t, "dsh")
	work := t.TempDir()
	path := filepath.Join(store, "session.jsonl")
	w := Watch("dsh", work, time.Now())
	append_(t, path, dshHeader(work), dshMessage(40, 5, 9))
	w.poll(nil)
	if got := w.Sample().Output; got != 40 {
		t.Fatalf("plain jsonl output %d, want 40", got)
	}
}

func TestDshZstdTailCapContinuesNextPoll(t *testing.T) {
	store := withStore(t, "dsh")
	work := t.TempDir()
	path := filepath.Join(store, "session.jsonl.zstd")
	w := Watch("dsh", work, time.Now())

	header := zstdFrame(t, dshHeader(work)+"\n")
	first := zstdFrame(t, dshMessage(11, 0, 1)+"\n")
	second := zstdFrame(t, dshMessage(22, 0, 1)+"\n")
	appendBytes(t, path, header)
	appendBytes(t, path, first)
	appendBytes(t, path, second)

	old := zstdTailBytes
	// Big enough for the header and first message frame, too small for
	// the second: the leftover must be picked up on the next poll rather
	// than dropped or forcing a full-file buffer.
	zstdTailBytes = int64(len(header) + len(first) + len(second)/2)
	t.Cleanup(func() { zstdTailBytes = old })

	w.poll(nil)
	if got := w.Sample().Output; got != 11 {
		t.Fatalf("first poll output %d, want 11 (only the frames inside the cap)", got)
	}
	w.poll(nil)
	if got := w.Sample().Output; got != 33 {
		t.Fatalf("second poll output %d, want 33 after reading the leftover frame", got)
	}
}

func TestZstdFrameLenMatchesEncodeAll(t *testing.T) {
	a := zstdFrame(t, "one\n")
	b := zstdFrame(t, "two\n")
	src := append(append([]byte{}, a...), b...)
	n, ok := zstdFrameLen(src)
	if !ok || n != len(a) {
		t.Fatalf("first frame %d ok=%v, want %d", n, ok, len(a))
	}
	n2, ok := zstdFrameLen(src[n:])
	if !ok || n2 != len(b) {
		t.Fatalf("second frame %d ok=%v, want %d", n2, ok, len(b))
	}
	if _, ok := zstdFrameLen(src[:len(a)/2]); ok {
		t.Fatal("half frame must not look complete")
	}

	plain, consumed, err := decodeZstdPrefix(append(src, 0x28, 0xb5))
	if err != nil {
		t.Fatal(err)
	}
	if consumed != len(src) || string(plain) != "one\ntwo\n" {
		t.Fatalf("prefix decode consumed %d want %d, plain %q", consumed, len(src), plain)
	}
}
