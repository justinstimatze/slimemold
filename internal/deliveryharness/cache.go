package deliveryharness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Cache is a content-addressed JSON cache for model responses. Per the
// global CLAUDE.md API-cost rule, a re-run that hasn't changed the
// prompt should not re-pay. Keys are sha256 over a stable JSON
// serialization of all params that affect output (model, prompt,
// temperature, max tokens, sample index). Values are whatever the
// caller chooses to store — typically the raw response text plus
// metadata.
//
// Re-running with a different grader prompt invalidates only grader
// entries (which key on grader-prompt content) — host entries stay
// warm. That's the asymmetric win: host responses are expensive and
// stable across grader-prompt iteration.
type Cache struct {
	Dir string // directory containing the hashed files; "" disables caching
}

// CacheKey captures all inputs that affect a model response. Marshaled
// to canonical JSON before hashing so field-order changes in Go don't
// drift the hash. Added fields default-zero — old cache entries simply
// stop matching, which is fail-safe (a missed cache hit re-fetches).
type CacheKey struct {
	Kind         string  `json:"kind"` // "host" or "grader"
	Model        string  `json:"model"`
	Prompt       string  `json:"prompt"`
	Temperature  float64 `json:"temperature"`
	MaxTokens    int     `json:"max_tokens"`
	SampleIndex  int     `json:"sample_index"`            // distinguishes the N=15 samples per cell
	PromptVer    int     `json:"prompt_ver,omitempty"`    // grader-prompt version; bumps invalidate grader cache only
	FixtureLabel string  `json:"fixture_label,omitempty"` // optional annotation for debugging
}

// Hash returns the hex-encoded sha256 of the canonical JSON encoding
// of the key. Stable across Go releases as long as encoding/json keeps
// emitting struct fields in declared order.
func (k CacheKey) Hash() (string, error) {
	buf, err := json.Marshal(k)
	if err != nil {
		return "", fmt.Errorf("marshal cache key: %w", err)
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]), nil
}

// Entry is what gets written to disk per cache hit. Wraps the value
// with provenance fields so an old cache directory can be inspected
// without consulting the originating run.
type Entry struct {
	Key      CacheKey        `json:"key"`
	Response string          `json:"response"`
	Meta     json.RawMessage `json:"meta,omitempty"`
}

// Get returns the cached entry for `key`, or nil if there's no hit.
// A cache miss is not an error — only IO/decode failures are. An
// unreadable cache file is treated as a miss but logged via error
// return so the caller can decide whether to surface it.
func (c *Cache) Get(key CacheKey) (*Entry, error) {
	if c == nil || c.Dir == "" {
		return nil, nil
	}
	h, err := key.Hash()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(c.Dir, h+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read cache %s: %w", path, err)
	}
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("decode cache %s: %w", path, err)
	}
	return &e, nil
}

// Put writes an entry for `key`. Best-effort: if the cache dir can't
// be created or the file can't be written, returns the error but the
// caller can choose to log-and-continue. mkdir is idempotent.
func (c *Cache) Put(key CacheKey, response string, meta json.RawMessage) error {
	if c == nil || c.Dir == "" {
		return nil
	}
	if err := os.MkdirAll(c.Dir, 0o750); err != nil {
		return fmt.Errorf("mkdir cache dir %s: %w", c.Dir, err)
	}
	h, err := key.Hash()
	if err != nil {
		return err
	}
	e := Entry{Key: key, Response: response, Meta: meta}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cache entry: %w", err)
	}
	path := filepath.Join(c.Dir, h+".json")
	// Write to a temp file then rename — partial writes on crash would
	// otherwise leave a corrupt cache file that subsequent Get calls
	// would surface as a decode error rather than a clean miss.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write cache tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename cache %s: %w", path, err)
	}
	return nil
}

// DefaultCacheDir returns the standard cache location:
// $XDG_CACHE_HOME/slimemold/delivery-eval, or
// $HOME/.cache/slimemold/delivery-eval as fallback. Returns "" when
// neither var is set — callers should treat "" as "caching disabled."
func DefaultCacheDir() string {
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "slimemold", "delivery-eval")
	}
	if h := os.Getenv("HOME"); h != "" {
		return filepath.Join(h, ".cache", "slimemold", "delivery-eval")
	}
	return ""
}

// sanitizeLabel is a small convenience for callers that want to embed
// a human-readable fixture label without breaking hash stability —
// strips characters that JSON-marshal differently across environments.
func sanitizeLabel(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// NewKey is the public constructor for a CacheKey — kept narrow so the
// fields stay aligned with what actually affects an Anthropic response.
// Callers that need additional dimensions should extend CacheKey and
// hand-build the struct.
func NewKey(kind, model, prompt, fixtureLabel string, temperature float64, maxTokens, sampleIndex, promptVer int) CacheKey {
	return CacheKey{
		Kind:         kind,
		Model:        model,
		Prompt:       prompt,
		Temperature:  temperature,
		MaxTokens:    maxTokens,
		SampleIndex:  sampleIndex,
		PromptVer:    promptVer,
		FixtureLabel: sanitizeLabel(fixtureLabel),
	}
}
