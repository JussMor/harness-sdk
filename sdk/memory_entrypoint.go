package autobuild

import (
	"fmt"
	"strings"
	"time"
)

// ─── Entrypoint (MEMORY.md) ────────────────────────────────────────────────

// EntrypointName is the canonical filename for the always-loaded memory index.
const EntrypointName = "MEMORY.md"

// MaxEntrypointLines caps how many lines from MEMORY.md are injected into
// LayerMemory. Anything past this is truncated — the model sees a warning.
const MaxEntrypointLines = 200

// MaxEntrypointBytes caps the byte size of MEMORY.md content that is
// injected. Catches long-line indexes that slip past the line cap.
const MaxEntrypointBytes = 25_000

// EntrypointTruncation is the result of applying the line+byte caps to
// MEMORY.md content. The Content field is what should be injected.
type EntrypointTruncation struct {
	Content         string
	LineCount       int
	ByteCount       int
	LineTruncated   bool
	BytesTruncated  bool
}

// TruncateEntrypoint applies MaxEntrypointLines and MaxEntrypointBytes,
// appending a warning that names which cap fired. Lines first (natural
// boundary), then bytes at the last newline before the cap so we never
// cut mid-line.
func TruncateEntrypoint(raw string) EntrypointTruncation {
	trimmed := strings.TrimSpace(raw)
	contentLines := strings.Split(trimmed, "\n")
	lineCount := len(contentLines)
	byteCount := len(trimmed)

	lineTruncated := lineCount > MaxEntrypointLines
	bytesTruncated := byteCount > MaxEntrypointBytes

	if !lineTruncated && !bytesTruncated {
		return EntrypointTruncation{
			Content:   trimmed,
			LineCount: lineCount,
			ByteCount: byteCount,
		}
	}

	var truncated string
	if lineTruncated {
		truncated = strings.Join(contentLines[:MaxEntrypointLines], "\n")
	} else {
		truncated = trimmed
	}
	if len(truncated) > MaxEntrypointBytes {
		cutAt := strings.LastIndex(truncated[:MaxEntrypointBytes], "\n")
		if cutAt <= 0 {
			cutAt = MaxEntrypointBytes
		}
		truncated = truncated[:cutAt]
	}

	var reason string
	switch {
	case bytesTruncated && !lineTruncated:
		reason = fmt.Sprintf("%d bytes (limit %d) — index entries are too long",
			byteCount, MaxEntrypointBytes)
	case lineTruncated && !bytesTruncated:
		reason = fmt.Sprintf("%d lines (limit %d)", lineCount, MaxEntrypointLines)
	default:
		reason = fmt.Sprintf("%d lines and %d bytes", lineCount, byteCount)
	}

	return EntrypointTruncation{
		Content: truncated +
			"\n\n> WARNING: " + EntrypointName + " is " + reason +
			". Only part of it was loaded. Keep index entries to one line under ~150 chars; move detail into topic files.",
		LineCount:      lineCount,
		ByteCount:      byteCount,
		LineTruncated:  lineTruncated,
		BytesTruncated: bytesTruncated,
	}
}

// ─── Freshness / staleness ─────────────────────────────────────────────────

// MemoryAgeDays returns floor(days since mtime). Negative inputs (clock
// skew / future timestamps) clamp to 0. Today=0, yesterday=1.
func MemoryAgeDays(mtime time.Time) int {
	if mtime.IsZero() {
		return 0
	}
	d := int(time.Since(mtime).Hours() / 24)
	if d < 0 {
		return 0
	}
	return d
}

// MemoryAge returns a human-readable age string. Models reason better
// about "47 days ago" than ISO timestamps.
func MemoryAge(mtime time.Time) string {
	d := MemoryAgeDays(mtime)
	switch d {
	case 0:
		return "today"
	case 1:
		return "yesterday"
	default:
		return fmt.Sprintf("%d days ago", d)
	}
}

// MemoryFreshnessText returns a plain-text staleness caveat for memories
// older than 1 day. Returns "" for fresh memories — adding a warning
// there would be noise.
//
// Motivated by stale `file:line` citations sounding more authoritative,
// not less, when surfaced as fact.
func MemoryFreshnessText(mtime time.Time) string {
	d := MemoryAgeDays(mtime)
	if d <= 1 {
		return ""
	}
	return fmt.Sprintf(
		"This memory is %d days old. Memories are point-in-time observations, "+
			"not live state — claims about code behavior or file:line citations "+
			"may be outdated. Verify against current code before asserting as fact.",
		d,
	)
}

// MemoryFreshnessNote wraps MemoryFreshnessText in <system-reminder> tags.
// Use this when the consumer doesn't add its own wrapper.
func MemoryFreshnessNote(mtime time.Time) string {
	text := MemoryFreshnessText(mtime)
	if text == "" {
		return ""
	}
	return "<system-reminder>" + text + "</system-reminder>\n"
}
