package analysis

import (
	"fmt"
	"time"

	"github.com/justinstimatze/slimemold/types"
)

// LegacyLoadBearer is the detector for "old but still actively touched"
// claims — the case the user flagged as "would be very interesting" when we
// discussed pruning. The hypothesis: most months-old claims are stale state
// observations whose work has been done. A few are not — they're underpinning
// current reasoning despite being old enough that a naive prune would have
// dropped them. Those are the ones worth surfacing.
//
// Firing criteria (all three must hold):
//
//   - LegacyAgeThreshold or older: claim was created at least this long ago
//   - LegacyRecentRefThreshold or newer: claim's last_referenced_at falls
//     within this many days (i.e., an edge touched it recently, OR a recent
//     session_claims insert touched it — both maintained by the store)
//   - LegacyMinDeps or more incoming supports/depends_on edges: the claim
//     is actually doing structural work, not just being passed in passing
//
// The thresholds are conservative on purpose. The point isn't to fire on
// every old claim — it's to surface the small set where age + activity +
// structural weight all coincide. False positives erode signal more than
// false negatives here.
// LegacyMinDeps is paired with internal/store.SweepStructuralMin: together
// they define the boundary between "structurally inert" (sweep archives) and
// "load-bearing" (legacy_load_bearer fires). Keep them equal — at deps =
// LegacyMinDeps an old claim must fall into exactly one bucket, not both
// (overlap) or neither (gap).
//
//	LegacyMinDeps == store.SweepStructuralMin
const (
	LegacyAgeThreshold       = 30 * 24 * time.Hour // claim must be at least this old
	LegacyRecentRefThreshold = 7 * 24 * time.Hour  // ...AND referenced within this recent window
	LegacyMinDeps            = 2                   // paired with store.SweepStructuralMin — keep equal
)

// findLegacyLoadBearers surfaces claims that are old but still being actively
// referenced in current reasoning. See the LegacyLoadBearer doc comment for
// rationale and threshold semantics.
func findLegacyLoadBearers(claims []types.Claim, edges []types.Edge) []types.Vulnerability {
	now := time.Now()

	// Count incoming structural edges per claim. Supports and depends_on
	// count as "structural" (the from claim is grounded in the to claim);
	// contradicts and questions are deliberately excluded — being argued
	// with isn't the same as load-bearing dependence.
	incomingDeps := make(map[string]int)
	for _, e := range edges {
		switch e.Relation {
		case types.RelSupports, types.RelDependsOn:
			incomingDeps[e.ToID]++
		}
	}

	var out []types.Vulnerability
	for i := range claims {
		c := &claims[i]
		// Guard against zero-valued timestamps (test fixtures, partial
		// migration). Without CreatedAt we can't say it's old; without
		// LastReferencedAt we treat it as never-referenced (skip).
		if c.CreatedAt.IsZero() || c.LastReferencedAt.IsZero() {
			continue
		}
		if c.Closed {
			// Closed claims are by definition resolved; not interesting as
			// "still alive" findings even if something touched them recently.
			continue
		}
		age := now.Sub(c.CreatedAt)
		sinceRef := now.Sub(c.LastReferencedAt)
		if age < LegacyAgeThreshold || sinceRef > LegacyRecentRefThreshold {
			continue
		}
		if incomingDeps[c.ID] < LegacyMinDeps {
			continue
		}
		out = append(out, types.Vulnerability{
			Severity: "warning",
			Type:     "legacy_load_bearer",
			Description: fmt.Sprintf(
				"Old claim still doing work: %q (created %dd ago, referenced %s ago, %d incoming deps)",
				truncate(c.Text, 60),
				int(age.Hours()/24),
				humanizeDuration(sinceRef),
				incomingDeps[c.ID],
			),
			ClaimIDs: []string{c.ID},
			// Mark as persistent so skipAnchor's HookMaxClaimAge (7-day) cap
			// doesn't drop these — by definition every legacy_load_bearer
			// is on a claim >30 days old. The 7-day cooldown applies, so
			// the same legacy anchor won't repeat-fire daily.
			FiredViaPersistent: true,
		})
	}
	return out
}

// humanizeDuration formats a duration as a compact "%d{m,h,d}" string suitable
// for slotting into "referenced %s ago" without breaking grammar at any
// magnitude. Sub-minute durations show as "<1m" rather than "just now" so
// the surrounding "%s ago" template still reads naturally ("referenced <1m
// ago" — slightly clipped, but consistent).
func humanizeDuration(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
