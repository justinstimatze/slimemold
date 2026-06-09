package verify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/justinstimatze/slimemold/internal/store"
)

// cache is a disk-backed key→Reconciled store. Entries persist as a
// single JSON file per project so a session restart preserves prior
// fetches. Concurrency is guarded by mu for the in-memory map; writeMu
// serializes the marshal+write+rename sequence so concurrent put()s
// can't rename their snapshots out of order and drop entries on disk.
type cache struct {
	path string

	mu      sync.RWMutex
	entries map[string]Reconciled

	writeMu sync.Mutex
}

func openCache(dataDir, project string) (*cache, error) {
	project = store.SanitizeProject(project)
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
	// writeMu serializes the marshal+write+rename for all callers.
	// Without it, two concurrent puts can take their snapshots in one
	// order and rename in the opposite order, leaving the on-disk file
	// missing the newer entry even though both put()s returned nil.
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.mu.Lock()
	c.entries[key] = r
	snapshot := make(map[string]Reconciled, len(c.entries))
	for k, v := range c.entries {
		snapshot[k] = v
	}
	c.mu.Unlock()

	b, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("verify: marshal cache: %w", err)
	}
	tmp := c.path + ".tmp"
	// 0o600: cached Kagi snippets may quote text from privately
	// ingested documents; matches graph.sqlite's restrictiveness and
	// the project's convention on user-data files. The parent dir is
	// 0o700; file-mode hardening is defense in depth.
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("verify: write tmp: %w", err)
	}
	if err := os.Rename(tmp, c.path); err != nil {
		return fmt.Errorf("verify: rename: %w", err)
	}
	return nil
}
