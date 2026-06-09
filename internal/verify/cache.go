package verify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// cache is a disk-backed key→Reconciled store. Entries persist as a
// single JSON file per project so a session restart preserves prior
// fetches. Concurrency is guarded by a single mutex — the read path
// is fast, writes are batched on every put with an atomic rename.
type cache struct {
	path string

	mu      sync.RWMutex
	entries map[string]Reconciled
}

var projectRe = regexp.MustCompile(`[^a-zA-Z0-9_\-]`)

// sanitizeProject mirrors internal/store's sanitizer so the verify
// cache file lands in the same project directory as graph.sqlite.
func sanitizeProject(name string) string {
	name = filepath.Base(name)
	name = projectRe.ReplaceAllString(name, "")
	if name == "" {
		name = "default"
	}
	return name
}

func openCache(dataDir, project string) (*cache, error) {
	project = sanitizeProject(project)
	dir := filepath.Join(dataDir, project)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("verify: create dir: %w", err)
	}
	c := &cache{
		path:    filepath.Join(dir, "verify-cache.json"),
		entries: make(map[string]Reconciled),
	}
	if err := c.load(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *cache) load() error {
	b, err := os.ReadFile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("verify: read cache: %w", err)
	}
	if len(b) == 0 {
		return nil
	}
	var entries map[string]Reconciled
	if err := json.Unmarshal(b, &entries); err != nil {
		// Corrupt cache shouldn't kill the verifier — start fresh
		// and let the next write rewrite the file with valid JSON.
		// Intentional: we'd rather lose cached fetches than refuse
		// to load. Suppress nilerr accordingly.
		c.entries = make(map[string]Reconciled)
		return nil //nolint:nilerr
	}
	c.entries = entries
	return nil
}

func (c *cache) get(key string) (Reconciled, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.entries[key]
	return r, ok
}

func (c *cache) put(key string, r Reconciled) error {
	c.mu.Lock()
	c.entries[key] = r
	snapshot := make(map[string]Reconciled, len(c.entries))
	for k, v := range c.entries {
		snapshot[k] = v
	}
	c.mu.Unlock()

	b, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("verify: marshal cache: %w", err)
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("verify: write tmp: %w", err)
	}
	if err := os.Rename(tmp, c.path); err != nil {
		return fmt.Errorf("verify: rename: %w", err)
	}
	return nil
}
