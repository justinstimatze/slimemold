package deliveryharness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCache_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := &Cache{Dir: dir}
	key := NewKey("host", "claude-sonnet-4-6", "hello world", "fix1", 0.7, 1024, 3, 0)

	got, err := c.Get(key)
	if err != nil {
		t.Fatalf("Get on empty cache: %v", err)
	}
	if got != nil {
		t.Fatalf("expected miss on empty cache, got %+v", got)
	}

	if err := c.Put(key, "the response text", json.RawMessage(`{"latency_ms":123}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err = c.Get(key)
	if err != nil {
		t.Fatalf("Get after Put: %v", err)
	}
	if got == nil {
		t.Fatal("expected hit, got nil")
	}
	if got.Response != "the response text" {
		t.Errorf("Response mismatch: %q", got.Response)
	}
	// Meta is re-indented by MarshalIndent on Put, so compare
	// semantic JSON rather than bytes.
	var gotMeta, wantMeta map[string]any
	if err := json.Unmarshal(got.Meta, &gotMeta); err != nil {
		t.Fatalf("meta unmarshal: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"latency_ms":123}`), &wantMeta); err != nil {
		t.Fatal(err)
	}
	if gotMeta["latency_ms"] != wantMeta["latency_ms"] {
		t.Errorf("Meta latency_ms mismatch: got %v, want %v", gotMeta["latency_ms"], wantMeta["latency_ms"])
	}
}

func TestCache_KeyFieldsAffectHash(t *testing.T) {
	base := NewKey("host", "m", "p", "", 0.7, 1024, 0, 0)
	mutations := []struct {
		name string
		mod  func(*CacheKey)
	}{
		{"kind", func(k *CacheKey) { k.Kind = "grader" }},
		{"model", func(k *CacheKey) { k.Model = "haiku" }},
		{"prompt", func(k *CacheKey) { k.Prompt = "different" }},
		{"temperature", func(k *CacheKey) { k.Temperature = 0.9 }},
		{"max_tokens", func(k *CacheKey) { k.MaxTokens = 2048 }},
		{"sample_index", func(k *CacheKey) { k.SampleIndex = 1 }},
		{"prompt_ver", func(k *CacheKey) { k.PromptVer = 2 }},
	}

	baseHash, err := base.Hash()
	if err != nil {
		t.Fatalf("base hash: %v", err)
	}
	for _, m := range mutations {
		k := base
		m.mod(&k)
		h, err := k.Hash()
		if err != nil {
			t.Fatalf("%s hash: %v", m.name, err)
		}
		if h == baseHash {
			t.Errorf("changing %s did not change hash — caching would silently collide", m.name)
		}
	}
}

func TestCache_StableAcrossInstances(t *testing.T) {
	// Two CacheKey values with identical fields must hash identically.
	// Hash stability across runs is the load-bearing property of the
	// cache — if it drifts, every existing entry effectively
	// invalidates.
	a := NewKey("host", "m", "p", "label", 0.7, 1024, 0, 0)
	b := NewKey("host", "m", "p", "label", 0.7, 1024, 0, 0)
	ha, err := a.Hash()
	if err != nil {
		t.Fatal(err)
	}
	hb, err := b.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Errorf("identical keys produced different hashes: %s vs %s", ha, hb)
	}
}

func TestCache_DisabledWhenDirEmpty(t *testing.T) {
	c := &Cache{Dir: ""}
	key := NewKey("host", "m", "p", "", 0.0, 1, 0, 0)
	if err := c.Put(key, "resp", nil); err != nil {
		t.Errorf("disabled-cache Put should no-op, got %v", err)
	}
	got, err := c.Get(key)
	if err != nil {
		t.Errorf("disabled-cache Get should no-op, got %v", err)
	}
	if got != nil {
		t.Errorf("disabled-cache Get returned non-nil entry: %+v", got)
	}
}

func TestCache_CorruptFileIsDecodeError(t *testing.T) {
	// A partial-write that wasn't covered by the temp-rename pattern
	// is the failure mode we'd want to surface, not silently mask.
	dir := t.TempDir()
	c := &Cache{Dir: dir}
	key := NewKey("host", "m", "p", "", 0.0, 1, 0, 0)
	h, err := key.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, h+".json"), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = c.Get(key)
	if err == nil {
		t.Error("expected decode error on corrupt cache file, got nil")
	}
}

func TestCache_SanitizeLabelStripsControl(t *testing.T) {
	if got := sanitizeLabel("foo\x00bar\x1fbaz"); got != "foobarbaz" {
		t.Errorf("sanitize stripped wrong runes: %q", got)
	}
	if got := sanitizeLabel("normal-label"); got != "normal-label" {
		t.Errorf("sanitize touched a safe label: %q", got)
	}
}

func TestDefaultCacheDir_Honors_XDG(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-test")
	t.Setenv("HOME", "/tmp/home-test")
	if got := DefaultCacheDir(); got != "/tmp/xdg-test/slimemold/delivery-eval" {
		t.Errorf("XDG_CACHE_HOME not honored: %q", got)
	}
	t.Setenv("XDG_CACHE_HOME", "")
	if got := DefaultCacheDir(); got != "/tmp/home-test/.cache/slimemold/delivery-eval" {
		t.Errorf("HOME fallback wrong: %q", got)
	}
	t.Setenv("HOME", "")
	if got := DefaultCacheDir(); got != "" {
		t.Errorf("no env vars should return empty, got %q", got)
	}
}
