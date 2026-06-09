// Package verify performs active external verification of STOP-class
// claims (weak basis extracted from authored documents). The point is to
// move friction from the consumer's path-to-ignore to their path-to-act:
// instead of a uniform flag the consumer can scroll past, slimemold
// surfaces reconciled state ("you asserted X; an external source says
// Y") inline with the finding. The bright-pattern voice still wraps it;
// the substance is verification.
//
// Design:
//   - Lookup is in-memory + disk cache, no network. Safe in hook hot
//     path.
//   - Prefetch kicks off an async fetch when no entry exists. Bounded
//     concurrency, deduped by content hash. The next hook fire (5
//     turns later, typically minutes) picks up the result.
//   - Enabled() reports whether the Kagi search backend has a token
//     configured. Without one the verifier is a no-op and analysis
//     falls back to the pre-step-3 rendering.
package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Reconciled is the cached verification result for a single claim.
type Reconciled struct {
	Query     string    `json:"query"`
	Source    string    `json:"source"`
	Title     string    `json:"title"`
	Snippet   string    `json:"snippet"`
	FetchedAt time.Time `json:"fetched_at"`
}

// Stale reports whether a Reconciled entry has aged past TTL.
func (r Reconciled) Stale(ttl time.Duration) bool {
	return time.Since(r.FetchedAt) > ttl
}

// CacheTTL is the disk-cache freshness window. Claim assertions on
// shipped docs don't change every day; 7d cuts the cost of repeat
// fires on the same STOP-class anchor while still refreshing on a
// human timescale when the state of the world shifts.
const CacheTTL = 7 * 24 * time.Hour

// Verifier wires a Kagi search backend together with a disk-backed
// cache. The zero value is not usable — call New.
type Verifier struct {
	cache *cache
	kagi  *kagiClient

	mu       sync.Mutex
	inflight map[string]struct{} // hash → in-flight fetch
}

// New constructs a Verifier rooted at dataDir/project. The cache file
// is loaded eagerly so first Lookup is cheap. If Kagi credentials are
// absent, the verifier is in "lookup-only" mode: cached entries from
// prior sessions still resolve; new claims will not trigger fetches.
func New(dataDir, project string) (*Verifier, error) {
	c, err := openCache(dataDir, project)
	if err != nil {
		return nil, err
	}
	return &Verifier{
		cache:    c,
		kagi:     newKagiClient(),
		inflight: make(map[string]struct{}),
	}, nil
}

// Enabled reports whether external fetches will succeed. False
// indicates either no token or no backend configured — callers should
// skip Prefetch in that case but Lookup still works for previously
// cached entries.
func (v *Verifier) Enabled() bool {
	return v.kagi.enabled()
}

// Lookup returns the snippet and source of a cached fresh entry for a
// claim. Matches the analysis.HookVerifier interface so the analysis
// package can consume verify without importing it. Never makes a
// network call. The pair (snippet, source) is what gets rendered
// inline; callers that need the full Reconciled struct (cache TTL,
// timing, raw query) can call the package-private lookupReconciled.
func (v *Verifier) Lookup(claimText string) (snippet, source string, ok bool) {
	r, hit := v.lookupReconciled(claimText)
	if !hit {
		return "", "", false
	}
	return r.Snippet, r.Source, true
}

func (v *Verifier) lookupReconciled(claimText string) (Reconciled, bool) {
	key := claimKey(claimText)
	r, ok := v.cache.get(key)
	if !ok {
		return Reconciled{}, false
	}
	if r.Stale(CacheTTL) {
		return Reconciled{}, false
	}
	return r, true
}

// Prefetch kicks off a background fetch for a claim if none is in
// flight and no fresh cache entry exists. Returns immediately. The
// hook hot path is never blocked. Idempotent across concurrent calls
// with the same claim text.
func (v *Verifier) Prefetch(claimText string) {
	if !v.Enabled() {
		return
	}
	key := claimKey(claimText)
	v.mu.Lock()
	// Re-check freshness AND inflight under the same lock the
	// goroutine clears inflight under. A coarser scope but it closes
	// the TOCTOU window where another goroutine's fetchAndStore
	// finishes (writes cache + drops inflight) between an out-of-lock
	// freshness check and the inflight check — the second caller
	// would otherwise spawn a duplicate fetch.
	if _, fresh := v.cache.get(key); fresh {
		v.mu.Unlock()
		return
	}
	if _, busy := v.inflight[key]; busy {
		v.mu.Unlock()
		return
	}
	v.inflight[key] = struct{}{}
	v.mu.Unlock()

	go v.fetchAndStore(claimText, key)
}

func (v *Verifier) fetchAndStore(claimText, key string) {
	defer func() {
		v.mu.Lock()
		delete(v.inflight, key)
		v.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	r, err := v.kagi.search(ctx, queryFromClaim(claimText))
	if err != nil {
		return
	}
	r.FetchedAt = time.Now()
	_ = v.cache.put(key, r)
}

// claimFolder casefolds in a unicode-aware way; constructed once at
// package init since cases.Caser is safe for concurrent use after
// construction. Fold() applies the unicode locale-independent
// case-folding mapping — claim text can be any language and we want
// stable keys regardless of the system locale.
var claimFolder = cases.Fold()

// claimKey produces the cache key for a claim text. Sha256 over the
// normalized form (NFC-composed, casefolded, whitespace-collapsed) so
// trivial formatting differences — including decomposed-vs-composed
// accented characters and ASCII-vs-unicode case variations — don't
// fragment cache entries. Curly-vs-straight quotes still fragment;
// catching those would need a heuristic that risks collapsing
// genuinely-distinct claims.
func claimKey(s string) string {
	composed := norm.NFC.String(s)
	folded := claimFolder.String(composed)
	collapsed := strings.Join(strings.Fields(folded), " ")
	sum := sha256.Sum256([]byte(collapsed))
	return hex.EncodeToString(sum[:])[:24]
}

// queryFromClaim turns a claim into a search query. Claim text is
// already terse declarative prose; the search engine handles the
// rest. We cap at 200 chars so we don't blow query limits.
func queryFromClaim(claim string) string {
	q := strings.Join(strings.Fields(claim), " ")
	const maxLen = 200
	if len(q) > maxLen {
		q = q[:maxLen]
	}
	return q
}
