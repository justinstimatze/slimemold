package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStaleBinaryCheck(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "slimemold")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	// No go.mod adjacent → installed-binary case → not applicable, single stat.
	if _, _, ok := staleBinaryCheck(exe); ok {
		t.Fatal("expected applicable=false without an adjacent go.mod")
	}

	// Make it a source tree.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Binary OLDER than source → stale.
	old := time.Now().Add(-2 * time.Hour)
	now := time.Now()
	mustChtimes(t, exe, old)
	mustChtimes(t, src, now)
	stale, by, ok := staleBinaryCheck(exe)
	if !ok || !stale {
		t.Fatalf("expected stale: applicable=%v stale=%v", ok, stale)
	}
	if by < time.Hour {
		t.Fatalf("expected ~2h delta, got %v", by)
	}

	// Binary NEWER than source → fresh.
	mustChtimes(t, exe, time.Now().Add(time.Minute))
	if stale, _, ok := staleBinaryCheck(exe); !ok || stale {
		t.Fatalf("expected fresh: applicable=%v stale=%v", ok, stale)
	}

	// A newer *_test.go must NOT count — test files aren't compiled in.
	tf := filepath.Join(dir, "z_test.go")
	if err := os.WriteFile(tf, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustChtimes(t, tf, time.Now().Add(2*time.Hour))
	if stale, _, _ := staleBinaryCheck(exe); stale {
		t.Fatal("a newer _test.go should not mark the binary stale")
	}
}

func mustChtimes(t *testing.T, path string, mod time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}
