// Package hookevents owns the structured-event surface of the Stop hook:
// the JSONL emitter, the rotation primitive with kernel-locked safety,
// the tail-from-end reader, the status pretty-printer, and a bounded
// panic-reason formatter. Lives in internal/ rather than package main
// so other components (MCP server, future events-tail subcommand,
// integration tests outside cmd/) can emit and read the same stream.
//
// All write paths are best-effort: callers MUST be safe to call any
// function here from inside a panic recover defer. Every public function
// wraps its body in a recover so a misbehaving FS or marshaler can't
// propagate out and break the Stop hook's strict-additive discipline.
package hookevents

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// MaxReasonBytes is the per-event byte cap on free-form reason strings
// (panic stacks, error chains). Sized to leave headroom under Linux's
// PIPE_BUF (4096 bytes) so a full event row stays atomic under O_APPEND
// even with the fixed fields (kind/project/duration_ms/ts) and any
// per-kind extras. If panic events sprout new fields and total marshaled
// size approaches 4096, retighten this number rather than mutate
// individual call sites.
const MaxReasonBytes = 1024

// MaxLogBytes is the rotation threshold for hook.log and hook-events.jsonl.
// Hit it, rename to <path>.1, lose any earlier .1. Tuned for ~2 months of
// light use on the heaviest known project before rotation kicks in.
const MaxLogBytes = 5 * 1024 * 1024

// EventsFilename is the basename of the JSONL stream this package writes.
// Exported so cmdStatus and downstream tooling don't drift on the literal.
const EventsFilename = "hook-events.jsonl"

// Emitter holds per-fire context (logDir, project, fire-entry timestamp)
// that every event row needs. Fields are exported so cmdHook can update
// Project mid-fire (it's derived AFTER the Emitter is constructed, to
// keep the recover defer able to emit even for pre-decode panics).
//
// A zero Emitter is unusable: LogDir must be set, Start must be set.
// Project may be empty for events emitted before input.CWD is known
// (panic during decode, noinput).
type Emitter struct {
	LogDir  string
	Project string
	Start   time.Time
}

// Emit writes one JSONL row with kind/project/duration_ms attached plus
// every key in extra. Fixed fields are assigned AFTER the merge so a
// caller's extra cannot override kind/project/duration_ms — the kind
// taxonomy is load-bearing for downstream `mlr stats1 -g kind` analysis.
//
// extra may be nil. logDir must be set on the Emitter, otherwise this is
// a no-op (early-return guard).
func (e *Emitter) Emit(kind string, extra map[string]any) {
	if e == nil || e.LogDir == "" {
		return
	}
	out := make(map[string]any, len(extra)+4)
	maps.Copy(out, extra)
	out["kind"] = kind
	out["project"] = e.Project
	out["duration_ms"] = time.Since(e.Start).Milliseconds()
	write(e.LogDir, out)
}

// Panic emits the "panic" event with a reason already-truncated by the
// caller (via TruncateReason). Separate method because the cmdHook outer
// recover constructs the reason itself and we want to avoid double-
// allocating.
func (e *Emitter) Panic(reason string) {
	e.Emit("panic", map[string]any{"reason": reason})
}

// Noinput emits the "noinput" event with cwd / transcript / decode_err as
// structured keys (not a single Sprintf'd reason string), so downstream
// queries like "how often did decode_err fire vs empty fields?" stay
// millerable. decode_err is omitted when err is nil, so a nil error
// doesn't render as the misleading literal "<nil>".
func (e *Emitter) Noinput(cwd, transcript string, decodeErr error) {
	extra := map[string]any{
		"cwd":             cwd,
		"transcript_path": transcript,
	}
	if decodeErr != nil {
		extra["decode_err"] = decodeErr.Error()
	}
	e.Emit("noinput", extra)
}

// write is the single low-level append path. ONE syscall per event:
// json.Marshal + append '\n' + f.Write. POSIX guarantees O_APPEND
// atomicity for a single write() up to PIPE_BUF, so concurrent processes
// from sibling Stop hooks can append without interleaving as long as each
// event stays under that ceiling (which is why TruncateReason caps the
// panic reason field).
//
// Mutation-safe: copies into a fresh map before injecting ts, so a caller
// reusing the same base map across calls won't see ts leak between rows.
//
// Inner recover guarantees this function NEVER propagates a panic. The
// strict-additive Stop hook discipline depends on this: cmdHook's outer
// recover defer calls Emit, and a panic-in-panic would escape the defer.
func write(logDir string, fields map[string]any) {
	defer func() { _ = recover() }()
	if logDir == "" || fields == nil {
		return
	}
	out := make(map[string]any, len(fields)+1)
	maps.Copy(out, fields)
	if _, ok := out["ts"]; !ok {
		out["ts"] = time.Now().Format(time.RFC3339)
	}
	data, err := json.Marshal(out)
	if err != nil {
		return
	}
	data = append(data, '\n')
	path := filepath.Join(logDir, EventsFilename)
	RotateLogIfLarge(path)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(data)
}

// RotateLogIfLarge renames a log file to "<path>.1" when it exceeds
// MaxLogBytes, capping unbounded growth across months of hook fires. One
// backup is kept (overwriting any prior backup). Called by cmdHook for
// hook.log and by write() for hook-events.jsonl — same threshold and
// policy for both.
//
// Concurrency: an advisory flock(2) on the source file makes the
// rotation single-writer across processes. Unlike an O_EXCL ".rotating"
// sidecar, flock is released by the kernel when the holding process
// dies — so a SIGKILL between lock acquisition and rename does NOT
// orphan the lock and permanently block future rotations. The trade-off
// is a Linux/Unix-only build constraint; that's fine for slimemold,
// which targets local Linux installs.
//
// Re-stat under the lock so we don't rotate a file that's already been
// rotated by a sibling racer (its rename completed while we were waiting
// to acquire the lock, leaving us looking at a brand-new tiny file).
//
// All errors swallowed — rotation is best-effort and must never block
// the hook.
func RotateLogIfLarge(logPath string) {
	defer func() { _ = recover() }()
	info, err := os.Stat(logPath)
	if err != nil || info.Size() < MaxLogBytes {
		return
	}
	f, err := os.OpenFile(logPath, os.O_RDONLY, 0)
	if err != nil {
		return
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	// Re-check under the lock — another racer may have rotated already.
	if info, err := f.Stat(); err == nil && info.Size() < MaxLogBytes {
		return
	}
	_ = os.Rename(logPath, logPath+".1")
}

// TruncateReason renders a recovered panic value into a bounded string
// suitable for the "reason" event field. The byte cap (MaxReasonBytes)
// keeps single-event size well under PIPE_BUF so atomic-append
// concurrency holds.
//
// Type-switches to avoid an intermediate fmt.Sprintf("%v") allocation in
// the common cases (error, string, fmt.Stringer). The fallback Sprintf
// is the only path that materializes the full string before truncation
// — for pathological multi-MB error chains the allocation cost is
// bounded by Go's runtime, not by this function.
//
// Rune-safe truncation: cuts on a rune boundary so multi-byte panics
// (UTF-8 file paths in the reason) don't produce invalid UTF-8 in the
// JSONL row.
func TruncateReason(r any) string {
	defer func() { _ = recover() }()
	var s string
	switch v := r.(type) {
	case nil:
		return ""
	case string:
		s = v
	case error:
		s = v.Error()
	case fmt.Stringer:
		s = v.String()
	default:
		s = fmt.Sprintf("%v", v)
	}
	return truncateRunes(s, MaxReasonBytes)
}

// truncateRunes is rune-safe so a multi-byte cut doesn't produce
// invalid UTF-8. Note: MaxReasonBytes is a BYTE cap on output; we
// truncate by runes to stay UTF-8 valid, accepting that a string of
// all 4-byte runes produces a shorter output than MaxReasonBytes. The
// PIPE_BUF guarantee is what matters; rune fairness is a bonus.
func truncateRunes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Walk runes, accumulating byte length, stop when next rune would
	// overflow. Simpler than `[]rune(s)` allocation for huge strings.
	cutoff := 0
	for i := range s {
		if i > maxBytes {
			break
		}
		cutoff = i
	}
	return s[:cutoff] + "...(truncated)"
}

// TailLines returns the last n lines of a file by reading a bounded
// window from the END, not the whole file. For the hook-events.jsonl
// case where the file can reach 5MB before rotation but cmdStatus only
// wants the last 5 rows, this avoids a per-invocation 5MB allocation
// plus a 25K-element string slice.
//
// Bounded-window strategy: read the last 32KB. If the file is smaller,
// read all of it. If we started mid-line (start > 0), drop the partial
// first line. Then split on '\n' and return the last n. 32KB is enough
// to hold ~200 typical event rows; tail requests beyond that return
// fewer lines without erroring.
//
// Returns (nil, err) on read errors; ([], nil) on empty file. Never
// panics — wrapped in inner recover.
func TailLines(path string, n int) (lines []string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("TailLines recovered: %v", r)
		}
	}()
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const window = 32 * 1024
	size := stat.Size()
	start := int64(0)
	if size > window {
		start = size - window
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return nil, err
	}
	text := string(buf)
	// If we started mid-line, drop the leading partial line.
	if start > 0 {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil, nil
	}
	all := strings.Split(text, "\n")
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
}

// FormatRecentForStatus renders the last n events from eventsFile as a
// human-readable block for `slimemold status`. Each row shows ts, kind,
// project, duration_ms. Returns the formatted string including a leading
// "\nRecent events:\n" header so callers can tack it onto status output.
// Returns "" if the file doesn't exist or contains no events.
//
// Type-assertions use comma-ok so missing/malformed fields render as "-"
// instead of silently coalescing to a zero value (which would be
// indistinguishable from a real zero-duration event).
func FormatRecentForStatus(eventsFile string, n int) string {
	lines, err := TailLines(eventsFile, n)
	if err != nil || len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nRecent events:\n")
	for _, line := range lines {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			fmt.Fprintf(&b, "  %s\n", line)
			continue
		}
		ts := stringField(ev, "ts")
		kind := stringField(ev, "kind")
		proj := stringField(ev, "project")
		dur := durField(ev, "duration_ms")
		fmt.Fprintf(&b, "  %s  %-7s  %-20s  %s\n", ts, kind, proj, dur)
	}
	return b.String()
}

func stringField(ev map[string]any, key string) string {
	v, ok := ev[key].(string)
	if !ok {
		return "-"
	}
	return v
}

// durField renders the duration as "Nms" when present and well-typed,
// "-" otherwise. Comma-ok is load-bearing: silently coalescing a missing
// duration_ms to 0 would produce "0ms" indistinguishable from a real
// zero-duration event.
func durField(ev map[string]any, key string) string {
	v, ok := ev[key].(float64)
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%dms", int(v))
}
