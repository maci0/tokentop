// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package agentusage

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// dsh's default session log is concatenated independent Zstandard frames
// (RFC 8878): a header frame, then one frame per durable append. The
// uncompressed spelling is still `.jsonl`. Both are JSONL of the same
// records; only the physical encoding differs.

const (
	dshZstdSuffix = ".jsonl.zstd"
	// dshHeaderBytes is enough for the opening header frame (one JSON
	// object). A larger prefix is fine: extra complete frames are ignored
	// when deciding ownership.
	dshHeaderBytes = 64 << 10
	// zstdBlockHeaderLen is the 3-byte Block_Header that precedes every
	// block (RFC 8878 §3.1.1.2). Header.HeaderSize does not include it.
	zstdBlockHeaderLen = 3
	// zstdChecksumLen is the optional 4-byte content checksum after the
	// last block.
	zstdChecksumLen = 4
	// zstdMaxFrameBytes rejects a claimed frame larger than this. A real
	// append batch is a few events; anything past the cap is hostility or
	// a desynced reader, and walking it would pin a huge buffer.
	zstdMaxFrameBytes = 8 << 20
	// zstdMaxDecodeMemory bounds decompressed output of one frame.
	zstdMaxDecodeMemory = 32 << 20
)

// RFC 8878 block types in the 3-byte block header.
const (
	zstdBlockRaw        = 0
	zstdBlockRLE        = 1
	zstdBlockCompressed = 2
	zstdBlockReserved   = 3
)

var (
	dshDecOnce sync.Once
	dshDec     *zstd.Decoder
	dshDecErr  error
)

func dshDecoder() (*zstd.Decoder, error) {
	dshDecOnce.Do(func() {
		dshDec, dshDecErr = zstd.NewReader(nil,
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderMaxMemory(zstdMaxDecodeMemory),
		)
	})
	return dshDec, dshDecErr
}

func isDshZstd(path string) bool {
	return strings.HasSuffix(path, dshZstdSuffix)
}

// parseDsh reads one session-log line. Usage lives on the completed
// `assistant/message` record (camelCase, the provider's own counts). The
// streaming `assistant/chunk` with `chunk.type=usage` repeats the same
// numbers, so counting both would double every turn. The older test
// spelling `assistant-message` with snake_case fields is accepted too.
func parseDsh(line []byte) (values, string, bool) {
	var rec struct {
		Type  string `json:"type"`
		Usage *struct {
			InputTokens      int `json:"inputTokens"`
			OutputTokens     int `json:"outputTokens"`
			ReasoningTokens  int `json:"reasoningTokens"`
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			ReasoningSnake   int `json:"reasoning_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(line, &rec); err != nil || rec.Usage == nil {
		return values{}, "", false
	}
	if rec.Type != "assistant/message" && rec.Type != "assistant-message" {
		return values{}, "", false
	}
	u := rec.Usage
	out := counter(u.OutputTokens)
	if out == 0 {
		out = counter(u.CompletionTokens)
	}
	in := counter(u.InputTokens)
	if in == 0 {
		in = counter(u.PromptTokens)
	}
	think := counter(u.ReasoningTokens)
	if think == 0 {
		think = counter(u.ReasoningSnake)
	}
	if out == 0 && in == 0 && think == 0 {
		return values{}, "", false
	}
	tot := in
	if in > 0 {
		tot = satAdd(in, out)
	}
	return values{output: out, thinking: think, total: tot, input: in}, "", true
}

// consumeZstd reads newly appended concatenated frames from off, counts
// complete ones, and leaves a torn last frame uncommitted so the next poll
// re-reads it whole.
func (w *Watcher) consumeZstd(f *os.File, off int64) (recs []values, complete int64, ok bool) {
	src, err := io.ReadAll(f)
	if err != nil {
		return nil, 0, false
	}
	plain, n, err := decodeZstdPrefix(src)
	if err != nil && n == 0 {
		return nil, 0, false
	}
	for line := range bytes.SplitSeq(plain, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		recs = w.collect(recs, line)
	}
	return recs, off + int64(n), true
}

// ownsZstd decides ownership from the header frame. An incomplete opening
// frame is not a refusal: the writer may still be flushing it.
func (w *Watcher) ownsZstd(path string, f *os.File) (mine, decided bool) {
	buf := make([]byte, dshHeaderBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return false, false
	}
	if n == 0 {
		return false, false
	}
	plain, consumed, _ := decodeZstdPrefix(buf[:n])
	if consumed == 0 {
		return false, false
	}
	for line := range bytes.SplitSeq(plain, []byte("\n")) {
		if cwd, ok := w.ad.sessionCwd(line); ok {
			mine := w.sameDir(cwd)
			w.owner[path] = mine
			return mine, true
		}
	}
	// The first complete frames had no cwd. That is the header; waiting
	// longer will not invent one.
	w.owner[path] = false
	return false, true
}

// decodeZstdPrefix decompresses every complete frame at the front of src
// and reports how many compressed bytes those frames occupied. An
// incomplete tail is left unconsumed. A complete frame that fails to
// decode stops the walk: skipping it would desync the reader from the
// next magic.
func decodeZstdPrefix(src []byte) (plain []byte, consumed int, err error) {
	dec, err := dshDecoder()
	if err != nil {
		return nil, 0, err
	}
	off := 0
	for off < len(src) {
		n, ok := zstdFrameLen(src[off:])
		if !ok {
			break
		}
		out, derr := dec.DecodeAll(src[off:off+n], nil)
		if derr != nil {
			return plain, off, derr
		}
		plain = append(plain, out...)
		off += n
	}
	return plain, off, nil
}

// zstdFrameLen is the compressed size of the first complete frame in src,
// or not-ok when the frame is truncated, too large, or not zstd.
func zstdFrameLen(src []byte) (int, bool) {
	var h zstd.Header
	if err := h.Decode(src); err != nil {
		return 0, false
	}
	if h.Skippable {
		n := h.HeaderSize + int(h.SkippableSize)
		if n < h.HeaderSize || n > zstdMaxFrameBytes || len(src) < n {
			return 0, false
		}
		return n, true
	}
	off := h.HeaderSize
	for {
		if off+zstdBlockHeaderLen > len(src) {
			return 0, false
		}
		bh := uint32(src[off]) | uint32(src[off+1])<<8 | uint32(src[off+2])<<16
		last := bh&1 != 0
		btype := (bh >> 1) & 3
		size := int(bh >> 3)
		off += zstdBlockHeaderLen
		switch btype {
		case zstdBlockRLE:
			off++
		case zstdBlockRaw, zstdBlockCompressed:
			off += size
		default:
			return 0, false
		}
		if off < h.HeaderSize || off > zstdMaxFrameBytes {
			return 0, false
		}
		if last {
			break
		}
	}
	if h.HasCheckSum {
		off += zstdChecksumLen
	}
	if off > zstdMaxFrameBytes || len(src) < off {
		return 0, false
	}
	return off, true
}
