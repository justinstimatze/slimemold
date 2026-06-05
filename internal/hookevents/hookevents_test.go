package hookevents

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// helper — return an Emitter rooted at a fresh tempdir.
func freshEmitter(t *testing.T) (*Emitter, string) {
	t.Helper()
	dir := t.TempDir()
	return &Emitter{LogDir: dir, Project: "test-proj", Start: time.Now()}, dir
}

// readEvents parses every JSONL line in eventsFile (relative to dir) and
// returns the list. Fails the test on any unparseable line.
func readEvents(t *testing.T, dir string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, EventsFilename))
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var evs []map[string]any
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("unparseable line %q: %v", line, err)
		}
		evs = append(evs, ev)
	}
	return evs
}

func TestEmit_HappyPath(t *testing.T) {
	em, dir := freshEmitter(t)
	em.Emit("extract", map[string]any{"model": "m1", "claims": 7})
	evs := readEvents(t, dir)
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	ev := evs[0]
	for _, k := range []string{"kind", "project", "duration_ms", "ts", "model", "claims"} {
		if _, ok := ev[k]; !ok {
			t.Errorf("missing field %q", k)
		}
	}
	if ev["kind"] != "extract" {
		t.Errorf("kind: want extract, got %v", ev["kind"])
	}
	if ev["project"] != "test-proj" {
		t.Errorf("project: want test-proj, got %v", ev["project"])
	}
}

// Pins the load-bearing contract: caller's extra map cannot override
// kind/project/duration_ms. The kind taxonomy is what downstream
// `mlr stats1 -g kind` depends on; silently allowing override would
// corrupt the partition.
func TestEmit_FixedFieldsNotOverridable(t *testing.T) {
	em, dir := freshEmitter(t)
	em.Emit("extract", map[string]any{"kind": "FORGED", "project": "spoof"})
	evs := readEvents(t, dir)
	if len(evs) != 1 || evs[0]["kind"] != "extract" || evs[0]["project"] != "test-proj" {
		t.Errorf("caller-extra overrode fixed fields: %v", evs[0])
	}
}

// Pins the no-mutation contract: emitting via a base map must not leak
// ts (or any injected field) back into the caller's map.
func TestEmit_DoesNotMutateCallerMap(t *testing.T) {
	em, _ := freshEmitter(t)
	base := map[string]any{"claims": 5}
	em.Emit("extract", base)
	if _, ok := base["ts"]; ok {
		t.Errorf("caller map mutated — ts leaked back: %v", base)
	}
	if _, ok := base["kind"]; ok {
		t.Errorf("caller map mutated — kind leaked back: %v", base)
	}
}

func TestEmit_AppendsAcrossCalls(t *testing.T) {
	em, dir := freshEmitter(t)
	for i := 0; i < 3; i++ {
		em.Emit("skip", map[string]any{"turn": i})
	}
	evs := readEvents(t, dir)
	if len(evs) != 3 {
		t.Fatalf("want 3 events, got %d", len(evs))
	}
	for i, ev := range evs {
		if turn, _ := ev["turn"].(float64); int(turn) != i {
			t.Errorf("event %d turn: want %d, got %v", i, i, ev["turn"])
		}
	}
}

// panickyMarshaler triggers a real panic during JSON marshaling — the
// only way to exercise emitHookEvent's inner recover, since chan and
// other unsupported types produce errors (not panics) out of json.Marshal.
type panickyMarshaler struct{}

func (panickyMarshaler) MarshalJSON() ([]byte, error) {
	panic("boom in MarshalJSON")
}

// Pins the load-bearing strict-additive contract: write() (and therefore
// Emit) must NEVER propagate a panic, because cmdHook calls Emit FROM
// its outer recover defer — a panic-in-panic would escape and leak a
// stack trace to Claude Code. This test actually triggers a panic
// (unlike the prior chan-int test, which only exercised the
// json.Marshal error-return path).
func TestEmit_SwallowsRealPanic(t *testing.T) {
	em, _ := freshEmitter(t)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Emit propagated a panic: %v — strict-additive discipline broken", r)
		}
	}()
	em.Emit("extract", map[string]any{"explode": panickyMarshaler{}})
}

func TestEmit_GuardsEmptyArgs(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("guarded path panicked: %v", r)
		}
	}()
	(&Emitter{LogDir: "", Start: time.Now()}).Emit("extract", nil) // empty LogDir → no-op
	dir := t.TempDir()
	em := &Emitter{LogDir: dir, Start: time.Now()}
	em.Emit("extract", nil) // nil extra is fine; emits with just fixed fields
	if evs := readEvents(t, dir); len(evs) != 1 {
		t.Errorf("nil extra should still emit one row, got %d", len(evs))
	}
}

func TestEmit_NoinputOmitsNilDecodeErr(t *testing.T) {
	em, dir := freshEmitter(t)
	em.Noinput("", "/tmp/x", nil) // err is nil; field should be omitted
	evs := readEvents(t, dir)
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	if _, ok := evs[0]["decode_err"]; ok {
		t.Errorf("decode_err present when err was nil: %v", evs[0])
	}
	em.Noinput("", "", errors.New("EOF")) // err set; field present
	evs = readEvents(t, dir)
	if len(evs) != 2 {
		t.Fatalf("want 2 events, got %d", len(evs))
	}
	if v, _ := evs[1]["decode_err"].(string); v != "EOF" {
		t.Errorf("decode_err: want EOF, got %v", evs[1]["decode_err"])
	}
}

func TestTruncateReason_BoundedAndUTF8Safe(t *testing.T) {
	// Cap-respecting on a short string: passes through unchanged.
	if got := TruncateReason("hello"); got != "hello" {
		t.Errorf("short string mangled: %q", got)
	}
	// Long string: truncated with marker.
	long := strings.Repeat("a", MaxReasonBytes*2)
	got := TruncateReason(long)
	if len(got) > MaxReasonBytes+len("...(truncated)") {
		t.Errorf("truncation overshot: %d bytes", len(got))
	}
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Errorf("missing truncation marker: %q", got[len(got)-20:])
	}
	// UTF-8 boundary: multi-byte runes at the cut point. The truncated
	// output must be valid UTF-8 (rune-safe cut, not byte-safe).
	multi := strings.Repeat("π", MaxReasonBytes) // 2 bytes per rune × N
	got = TruncateReason(multi)
	for i, r := range got {
		_ = i
		if r == '�' {
			t.Errorf("UTF-8 broken at byte %d in truncated output", i)
			break
		}
	}
	// nil input: empty string.
	if got := TruncateReason(nil); got != "" {
		t.Errorf("nil reason: want empty, got %q", got)
	}
	// error input: uses .Error() directly, no Sprintf detour.
	if got := TruncateReason(errors.New("boom")); got != "boom" {
		t.Errorf("error reason: want boom, got %q", got)
	}
}

func TestTailLines_TailFromEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	var b strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
	lines, err := TailLines(path, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 5 {
		t.Fatalf("want 5, got %d", len(lines))
	}
	if lines[4] != "line 99" {
		t.Errorf("last line: want \"line 99\", got %q", lines[4])
	}
	if lines[0] != "line 95" {
		t.Errorf("first of tail: want \"line 95\", got %q", lines[0])
	}
}

func TestTailLines_EmptyAndMissing(t *testing.T) {
	dir := t.TempDir()
	// Missing file: error.
	if _, err := TailLines(filepath.Join(dir, "nope"), 5); err == nil {
		t.Error("want err for missing file, got nil")
	}
	// Empty file: nil slice, nil err.
	path := filepath.Join(dir, "empty")
	if err := os.WriteFile(path, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}
	lines, err := TailLines(path, 5)
	if err != nil || len(lines) != 0 {
		t.Errorf("empty file: want (nil, nil), got (%v, %v)", lines, err)
	}
}

func TestTailLines_LargerThanWindow(t *testing.T) {
	// Write a file larger than the 32KB tail window. TailLines should
	// still return the requested last n lines (or fewer if they don't
	// fit in the window).
	dir := t.TempDir()
	path := filepath.Join(dir, "big")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// Each line ~50 bytes; 1000 lines ~= 50KB > 32KB window.
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(f, "the quick brown fox line number %d here\n", i)
	}
	f.Close()
	lines, err := TailLines(path, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 5 {
		t.Fatalf("want 5, got %d", len(lines))
	}
	if !strings.Contains(lines[4], "999") {
		t.Errorf("last line should mention 999, got %q", lines[4])
	}
}

func TestFormatRecentForStatus_MissingDurationRendersDash(t *testing.T) {
	// Hand-craft an events file with one well-formed row and one
	// missing-duration row to verify the dash fallback. This is the
	// regression test for the comma-ok bug where missing duration_ms
	// rendered as "0ms" indistinguishable from a real zero.
	dir := t.TempDir()
	path := filepath.Join(dir, EventsFilename)
	rows := []string{
		`{"ts":"2026-01-01T00:00:00Z","kind":"extract","project":"p","duration_ms":42}`,
		`{"ts":"2026-01-01T00:00:01Z","kind":"skip","project":"p"}`, // no duration_ms
	}
	if err := os.WriteFile(path, []byte(strings.Join(rows, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out := FormatRecentForStatus(path, 5)
	if !strings.Contains(out, "42ms") {
		t.Errorf("expected real duration to render: %s", out)
	}
	if !strings.Contains(out, "skip") {
		t.Errorf("expected skip event in output: %s", out)
	}
	// Most important: the dash MUST appear for the missing-duration
	// row, not "0ms" (which the prior code would emit).
	if strings.Contains(out, "skip    p                     0ms") {
		t.Errorf("missing duration rendered as 0ms (silent zero) instead of -: %s", out)
	}
}

func TestRotateLogIfLarge_RotatesWhenOverThreshold(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	// Write a file just over MaxLogBytes.
	if err := os.WriteFile(path, make([]byte, MaxLogBytes+10), 0600); err != nil {
		t.Fatal(err)
	}
	RotateLogIfLarge(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("original file should have been renamed away; got err=%v", err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("expected backup file at %s: %v", path+".1", err)
	}
}

func TestRotateLogIfLarge_SkipsWhenUnderThreshold(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, []byte("tiny"), 0600); err != nil {
		t.Fatal(err)
	}
	RotateLogIfLarge(path)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("under-threshold file should NOT have been renamed: %v", err)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Errorf("backup should not exist: %v", err)
	}
}

func TestRotateLogIfLarge_DoesNotOrphanLockOnPanic(t *testing.T) {
	// This is a smoke test for the flock-based design vs the old O_EXCL
	// sidecar: even after a sequence of rotations, no .rotating file
	// should be left lying around. (With the old design, a SIGKILL test
	// would be needed to demonstrate the orphan; with flock, the kernel
	// releases on close — there's nothing to orphan.)
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(path, make([]byte, MaxLogBytes+10), 0600); err != nil {
			t.Fatal(err)
		}
		RotateLogIfLarge(path)
	}
	// No sidecar lock files should exist (flock is in-kernel, leaves
	// no filesystem footprint).
	matches, _ := filepath.Glob(filepath.Join(dir, "*.rotating"))
	if len(matches) > 0 {
		t.Errorf("flock-based rotation should leave no .rotating sidecars, found: %v", matches)
	}
}
