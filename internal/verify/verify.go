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
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Reconciled is the cached verification result for a single claim.
// Failed=true marks a negative-cache entry: the fetch errored or the
// result set was empty, and the caller stored the failure stamp so
// the next Prefetch can skip re-spawning the same losing call for
// FailureTTL. Lookup must treat Failed entries as misses since there
// is nothing to inline.
type Reconciled struct {
	Query     string    `json:"query"`
	Source    string    `json:"source"`
	Title     string    `json:"title"`
	Snippet   string    `json:"snippet"`
	Failed    bool      `json:"failed,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
}

// Stale reports whether a Reconciled entry has aged past TTL.
func (r Reconciled) Stale(ttl time.Duration) bool {
	return time.Since(r.FetchedAt) > ttl
}

// effectiveTTL returns the freshness window appropriate to this
// entry's nature: short for negative-cache failures so a flapping
// endpoint heals quickly, long for genuine results.
func (r Reconciled) effectiveTTL() time.Duration {
	if r.Failed {
		return FailureTTL
	}
	return CacheTTL
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
	wg       sync.WaitGroup      // tracks fetchAndStore goroutines
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
	if r.Stale(r.effectiveTTL()) {
		return Reconciled{}, false
	}
	// Negative-cache hits are intentionally a Lookup miss: there is
	// no snippet/source to inline. The cache hit still benefits
	// Prefetch's freshness check (it suppresses respawning the
	// failed query within FailureTTL).
	if r.Failed {
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
	// would otherwise spawn a duplicate fetch. A negative-cache hit
	// (Failed=true) within its effectiveTTL window also skips: a
	// flapping endpoint shouldn't get hammered every fire.
	if r, hit := v.cache.get(key); hit && !r.Stale(r.effectiveTTL()) {
		v.mu.Unlock()
		return
	}
	if _, busy := v.inflight[key]; busy {
		v.mu.Unlock()
		return
	}
	v.inflight[key] = struct{}{}
	v.wg.Add(1)
	v.mu.Unlock()

	go v.fetchAndStore(claimText, key)
}

// Wait blocks until every in-flight Prefetch goroutine has finished
// or ctx expires, whichever comes first. Returns nil on drain, ctx.Err()
// on timeout. Lets a one-shot caller (the slimemold Stop hook) give
// async fetches a bounded window to land their cache entries before
// the process exits — without this, fetchAndStore can be killed
// mid-flight and the cache stays cold across fires.
func (v *Verifier) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		v.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// FailureTTL is the freshness window for a negative-cache entry. Failed
// fetches (network error, Kagi rate-limit, empty result set) get stamped
// into the cache with Failed=true so the next hook fire's Prefetch can
// skip re-spawning the same losing call. 1 hour is long enough to spare
// the budget on a flapping endpoint but short enough that a genuine
// transient (rate-limit, dns blip) heals before the next session.
const FailureTTL = time.Hour

func (v *Verifier) fetchAndStore(claimText, key string) {
	defer v.wg.Done()
	defer func() {
		v.mu.Lock()
		delete(v.inflight, key)
		v.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	r, err := v.kagi.search(ctx, queryFromClaim(claimText))
	if err != nil {
		// Negative-cache the failure so the next fire's Prefetch
		// skips this query for FailureTTL. Structured stderr log so
		// a flapping endpoint is visible without surfacing to the
		// model (which would just see "External check missing").
		fmt.Fprintf(os.Stderr, "slimemold: verify: kagi fetch failed for %q: %v\n", queryFromClaim(claimText), err)
		_ = v.cache.put(key, Reconciled{
			Query:     queryFromClaim(claimText),
			Failed:    true,
			FetchedAt: time.Now(),
		})
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
