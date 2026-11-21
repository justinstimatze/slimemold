package extract

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// appendLines appends mixed transcript lines: turns, a non-turn event, a non-JSON line and a turn with no text, so
// a cursor has to skip exactly what the full scan skips.
func appendLines(t *testing.T, path string, from, n int) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for i := from; i < from+n; i++ {
		switch i % 7 {
		case 3:
			fmt.Fprintf(f, `{"type":"attachment","n":%d}`+"\n", i)
		case 5:
			fmt.Fprintf(f, "not json %d\n\n", i)
		case 6:
			fmt.Fprintf(f, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use"}]}}`+"\n")
		default:
			fmt.Fprintf(f, `{"type":"user","message":{"role":"user","content":"line %d"}}`+"\n", i)
		}
	}
}

// The seek path must return exactly what the full scan returns — the same chunk, the same boundary and the same
// count — across a growing transcript, through backlogs larger than the 50-message cap.
func TestCursorMatchesFullScan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	appendLines(t, path, 0, 40)
	var cur Cursor
	since := 0
	for round, grow := range []int{0, 30, 200, 1, 0, 75, 500} {
		appendLines(t, path, 1000*(round+1), grow)
		wantCount, _ := CountTranscriptTurns(path)
		gotCount, err := CountTranscriptTurnsFrom(path, cur)
		if err != nil || gotCount.Turn != wantCount {
			t.Fatalf("round %d: count from cursor %d, full scan %d (err %v)", round, gotCount.Turn, wantCount, err)
		}
		// drain the backlog the way successive fires do
		for since < wantCount {
			wantChunk, wantThrough, wantEnd, _ := readRecentTranscriptFrom(path, since, Cursor{})
			gotChunk, gotThrough, gotEnd, _ := readRecentTranscriptFrom(path, since, cur)
			if since > 0 && (gotChunk != wantChunk || gotThrough != wantThrough || gotEnd != wantEnd) {
				t.Fatalf("round %d since %d: cursor read differs (through %d vs %d, end %+v vs %+v)",
					round, since, gotThrough, wantThrough, gotEnd, wantEnd)
			}
			if since == 0 {
				since, cur = wantCount, gotCount // a baseline fire saves the full count's cursor
				continue
			}
			since, cur = gotThrough, gotEnd
		}
	}
}

// A cursor for another file, another turn, or an offset that no longer sits after a newline is ignored.
func TestUnusableCursorFallsBackToScan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	appendLines(t, path, 0, 120)
	good, _ := CountTranscriptTurnsFrom(path, Cursor{})
	_, _, at20, _ := readRecentTranscriptFrom(path, 1, Cursor{}) // cursor at some real boundary
	for name, tc := range map[string]struct {
		c     Cursor
		since int
	}{
		"other path":      {Cursor{Path: path + "x", Turn: at20.Turn, Offset: at20.Offset}, at20.Turn},
		"other sinceTurn": {at20, at20.Turn + 1}, // a good cursor, offered for a turn it is not at
		"mid-line":        {Cursor{Path: path, Turn: at20.Turn, Offset: at20.Offset - 3}, at20.Turn},
		"past the end":    {Cursor{Path: path, Turn: at20.Turn, Offset: good.Offset + 1<<20}, at20.Turn},
		"zero offset":     {Cursor{Path: path, Turn: at20.Turn}, at20.Turn},
	} {
		if tc.c.usable(mustOpen(t, path), path, tc.since) {
			t.Errorf("%s: cursor accepted", name)
		}
		want, wantT, wantE, _ := readRecentTranscriptFrom(path, tc.since, Cursor{})
		got, gotT, gotE, _ := readRecentTranscriptFrom(path, tc.since, tc.c)
		if got != want || gotT != wantT || gotE != wantE {
			t.Errorf("%s: read differs from the full scan", name)
		}
		if name != "other sinceTurn" { // that cursor is good for counting
			if n, _ := CountTranscriptTurnsFrom(path, tc.c); n.Turn != good.Turn {
				t.Errorf("%s: count %d, want %d", name, n.Turn, good.Turn)
			}
		}
	}
	if !at20.usable(mustOpen(t, path), path, at20.Turn) {
		t.Fatal("a cursor from a real read was rejected")
	}
}

func mustOpen(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// Real-transcript check (skipped unless SLIMEMOLD_CURSOR_TRANSCRIPT names one): every cursor the full scan would
// save, at a spread of turns, reproduces the full scan's chunk and boundary.
func TestCursorMatchesFullScanOnRealTranscript(t *testing.T) {
	path := os.Getenv("SLIMEMOLD_CURSOR_TRANSCRIPT")
	if path == "" {
		t.Skip("set SLIMEMOLD_CURSOR_TRANSCRIPT")
	}
	total, err := CountTranscriptTurnsFrom(path, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	for _, frac := range []float64{0.01, 0.25, 0.5, 0.9, 0.999} {
		since := int(float64(total.Turn) * frac)
		if since < 1 {
			since = 1
		}
		// the cursor a fire reading from `since` would save: the end of that read
		_, through, cur, _ := readRecentTranscriptFrom(path, since, Cursor{})
		want, wantT, wantE, _ := readRecentTranscriptFrom(path, through, Cursor{})
		got, gotT, gotE, _ := readRecentTranscriptFrom(path, through, cur)
		if got != want || gotT != wantT || gotE != wantE {
			t.Fatalf("turn %d: cursor read differs from full scan (through %d vs %d)", through, gotT, wantT)
		}
		if n, _ := CountTranscriptTurnsFrom(path, cur); n != total {
			t.Fatalf("turn %d: count from cursor %+v, full %+v", through, n, total)
		}
	}
}
