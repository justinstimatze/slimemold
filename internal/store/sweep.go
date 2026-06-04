package store

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/justinstimatze/slimemold/types"
)

// Sweep criteria — duplicated from the CLI for use inside CoreParseTranscript's
// auto-sweep path. The conservative defaults favor false negatives (keeping a
// claim that could've been archived) over false positives (archiving a claim
// someone wanted). To adjust on a one-off basis without rebuilding, edit these
// constants and rebuild — they are NOT wired to env vars (the only env knob
// today is SLIMEMOLD_SWEEP_CAP, which bounds per-fire batch size, not the
// eligibility criteria).
//
// SweepStructuralMin and internal/analysis.LegacyMinDeps are paired thresholds
// that together partition "structurally inert" (sweep candidate) from
// "load-bearing" (legacy_load_bearer candidate) at the same value. Bump one,
// bump the other — otherwise old claims at deps=2 either fall through both
// buckets (gap) or fire both (overlap). The intentional invariant:
//
//	SweepStructuralMin == LegacyMinDeps
const (
	SweepAgeDays       = 30
	SweepIdleDays      = 30
	SweepStructuralMin = 2 // paired with analysis.LegacyMinDeps — keep equal
)

// SweepWeakBasis is the set of basis values that are eligible for the
// "no structural deps" archive branch. Strong-basis claims (research,
// empirical, definition, convention) are kept regardless of dep count —
// they earned their position by being grounded, not by being popular.
var SweepWeakBasis = map[string]bool{
	"vibes":      true,
	"llm_output": true,
	"assumption": true,
}

// SweepCap returns the per-fire archive cap, read from SLIMEMOLD_SWEEP_CAP.
// Default 1000 — enough to drain a fresh backlog over a handful of days on
// the heaviest known projects (~5K candidates on lucida) without archiving
// thousands in one fire. Set the env var to "0" or a negative number to
// disable the cap. Malformed values fall back to the default with a stderr
// warning so a typo doesn't silently revert behavior.
//
// Shared by main.go's cmdSweep --apply and internal/mcp's auto-sweep so the
// two paths agree on the cap. If only one consumer read the env, manual
// --apply on a lucida-class backlog would archive 5K+ in one shot even
// though the user's env said "max 1000 per pass."
//
// The malformed-value warning is emitted at most once per process: the MCP
// daemon calls SweepCap() on every parse_transcript turn, so without the
// guard a typo'd env would spam the user's terminal dozens of times per
// session.
func SweepCap() int {
	v := os.Getenv("SLIMEMOLD_SWEEP_CAP")
	if v == "" {
		return 1000
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		sweepCapWarnOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "slimemold: SLIMEMOLD_SWEEP_CAP=%q is not a number — using default 1000\n", v)
		})
		return 1000
	}
	return n
}

var sweepCapWarnOnce sync.Once

// SortCandidatesOldestFirst sorts ids in place by the corresponding claim's
// CreatedAt (oldest first). Shared by SweepStaleClaims and main.go's
// cmdSweep --apply so the per-fire archival policy stays identical between
// the daemon and the CLI — without this, the two paths could drift on
// which N of K candidates get archived first, and a user comparing dry-run
// output to actual sweep behavior would see a confusing mismatch.
//
// Builds a CreatedAt lookup keyed on the candidate set only (not all
// claims) so the map size scales with len(ids), not len(claims). Stable
// sort so ties (same CreatedAt) preserve the input order.
func SortCandidatesOldestFirst(ids []string, claims []types.Claim) {
	if len(ids) < 2 {
		return
	}
	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	byID := make(map[string]time.Time, len(ids))
	for _, c := range claims {
		if idSet[c.ID] {
			byID[c.ID] = c.CreatedAt
		}
	}
	sort.SliceStable(ids, func(i, j int) bool {
		return byID[ids[i]].Before(byID[ids[j]])
	})
}

// SweepCandidates returns the IDs of claims that meet the archive criteria
// given the provided claims/edges snapshot. Idempotent: already-archived
// claims are skipped (re-running yields zero new candidates).
//
// Two qualifying branches:
//   - claim.Closed == true → archive (model explicitly resolved it)
//   - weak basis AND fewer than SweepStructuralMin incoming structural edges →
//     archive (the "conversational chatter that nobody references" case)
//
// Both branches additionally require age >= SweepAgeDays AND idle >=
// SweepIdleDays. The idle check is the key safety net: even an old,
// dep-less, weak-basis claim is kept if something has touched it recently.
func SweepCandidates(claims []types.Claim, edges []types.Edge) []string {
	ids, _ := SweepCandidatesWithDeps(claims, edges)
	return ids
}

// SweepCandidatesWithDeps is SweepCandidates plus the incoming-structural-
// edge count map it computes internally. The cmdSweep reporter wants the
// same map for its breakdown — exposing it lets the CLI skip a redundant
// O(E) pass. Internal callers (the SweepStaleClaims tx path) use
// SweepCandidates and discard the second return.
func SweepCandidatesWithDeps(claims []types.Claim, edges []types.Edge) ([]string, map[string]int) {
	now := time.Now()
	ageThreshold := time.Duration(SweepAgeDays) * 24 * time.Hour
	idleThreshold := time.Duration(SweepIdleDays) * 24 * time.Hour

	// Count incoming structural edges per claim. supports + depends_on
	// only — contradicts and questions don't make a claim load-bearing.
	deps := make(map[string]int)
	for _, e := range edges {
		switch e.Relation {
		case types.RelSupports, types.RelDependsOn:
			deps[e.ToID]++
		}
	}

	var ids []string
	for i := range claims {
		c := &claims[i]
		if c.Archived {
			continue
		}
		age := now.Sub(c.CreatedAt)
		lastRef := c.LastReferencedAt
		if lastRef.IsZero() {
			lastRef = c.CreatedAt
		}
		idle := now.Sub(lastRef)
		if age < ageThreshold || idle < idleThreshold {
			continue
		}
		structural := deps[c.ID] >= SweepStructuralMin
		if c.Closed {
			ids = append(ids, c.ID)
			continue
		}
		if !structural && SweepWeakBasis[string(c.Basis)] {
			ids = append(ids, c.ID)
		}
	}
	return ids, deps
}

// SweepStaleClaims runs SweepCandidates against the project's full claim
// graph and archives matching claims in one transaction. Returns the number
// archived and the number of candidates that exceeded the cap (>0 means more
// work pending for the next fire).
//
// maxArchive < 1 means "no cap." maxArchive >= 1 archives at most that many
// candidates per call, preferring the OLDEST (most stale) — older claims
// are more likely to be genuine archive targets, and bounding per-fire
// blast radius gives the user time to notice false positives before the
// whole backlog drains.
//
// Atomicity: read + write run in one transaction. With the current
// SetMaxOpenConns(1) serialization in Open(), there isn't an actual writer
// to race against; the tx mainly guarantees that the candidate selection
// and the UPDATE see the same snapshot. Kept anyway because (a) it's cheap
// and (b) loosening the connection-cap later wouldn't reintroduce a race.
func (d *DB) SweepStaleClaims(project string, maxArchive int) (archived int64, overflow int, err error) {
	// Capture archived/overflow inside the closure; assign to named returns
	// before returning err. The previous form (`return archived, overflow,
	// d.inTx(...)`) relied on Go's unspecified evaluation order for the
	// three operands of a return — the inTx call MUST complete before the
	// named returns are read, but reading "archived, overflow" before the
	// call returns would yield zero values. Spelling it out removes the
	// dependency on argument-evaluation order.
	err = d.inTx(func(q querier) error {
		// q-only tx wrapper; tx-local DB shares the parent's project (unused
		// by the methods called here, which take project explicitly).
		tx := &DB{db: d.db, q: q}
		claims, e := tx.GetClaimsByProjectAll(project)
		if e != nil {
			return e
		}
		edges, e := tx.GetEdgesByProject(project)
		if e != nil {
			return e
		}
		ids := SweepCandidates(claims, edges)
		if len(ids) == 0 {
			return nil
		}
		// Apply cap: keep oldest candidates first. SweepCandidates iterates
		// claims in stored order (ORDER BY created_at from
		// GetClaimsByProjectAll), so the slice is already oldest-first.
		// Below the cap, no sort needed. At/above the cap, sort explicitly
		// via SortCandidatesOldestFirst so the policy stays in lockstep
		// with main.go's cmdSweep --apply path.
		if maxArchive >= 1 && len(ids) > maxArchive {
			SortCandidatesOldestFirst(ids, claims)
			overflow = len(ids) - maxArchive
			ids = ids[:maxArchive]
		}
		n, e := tx.ArchiveClaims(project, ids)
		archived = n
		return e
	})
	return archived, overflow, err
}

// SweepStaleClaimsDebounced is the auto-sweep entry point: runs at most
// once per `minInterval` per project, tracked via slimemold_meta. If the
// last run was within the interval, returns ran=false. On a fresh run,
// archives up to `maxArchive` candidates (oldest first) and updates the
// timestamp.
//
// Returns (archived, overflow, ran, err). overflow > 0 means the cap bit
// and there are more candidates queued for the next fire — surface this
// to the user so they know the queue is draining over multiple days.
func (d *DB) SweepStaleClaimsDebounced(project string, minInterval time.Duration, maxArchive int) (int64, int, bool, error) {
	metaKey := "last_sweep_at:" + project
	now := time.Now()
	if last, ok := d.getMeta(metaKey); ok {
		if t, err := time.Parse(time.RFC3339, last); err == nil {
			// Future-dated stamp guard: clock skew (NTP correction, a DB
			// copied across machines, a laptop with a wrong RTC at boot)
			// can park a stamp in the future. Without the guard,
			// time.Since(t) is negative and trivially less than any positive
			// minInterval, so the debounce never expires until real time
			// catches up — could be hours or weeks of suppressed sweeps.
			// Treat anything in the future as "stamp is invalid, run now."
			if !t.After(now) && now.Sub(t) < minInterval {
				return 0, 0, false, nil
			}
		}
	}
	archived, overflow, err := d.SweepStaleClaims(project, maxArchive)
	if err != nil {
		return 0, 0, false, err
	}
	// Stamp regardless of archive count — the debounce is "did we attempt
	// a sweep recently", not "did we archive recently." But on overflow,
	// stamp at NOW - (minInterval/2) so the next fire is sooner — drain
	// the backlog at twice the steady-state rate without surrendering the
	// debounce entirely.
	stamp := now
	if overflow > 0 && minInterval > 0 {
		stamp = stamp.Add(-minInterval / 2)
	}
	if err := d.setMeta(metaKey, stamp.Format(time.RFC3339)); err != nil {
		// Stamp write failure breaks debounce — the next fire will see no
		// meta key and run again immediately. Log to stderr so the user can
		// see the auto-sweep is over-firing (otherwise it looks like the
		// debounce is misconfigured). Don't propagate — the archive already
		// happened and that's the load-bearing work.
		fmt.Fprintf(os.Stderr, "slimemold: sweep stamp write failed for %q (debounce will skip): %v\n", project, err)
	}
	return archived, overflow, true, nil
}

// GetMeta reads a slimemold_meta key. Exposed for tests and diagnostic
// commands that need to inspect bookkeeping state (e.g., when the last
// auto-sweep ran for a project). Returns ("", false) if the key isn't set.
func (d *DB) GetMeta(key string) (string, bool) {
	return d.getMeta(key)
}

func (d *DB) getMeta(key string) (string, bool) {
	var v string
	err := d.q.QueryRow(`SELECT value FROM slimemold_meta WHERE key = ?`, key).Scan(&v)
	if err != nil {
		return "", false
	}
	return v, true
}

func (d *DB) setMeta(key, value string) error {
	_, err := d.q.Exec(`INSERT INTO slimemold_meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
