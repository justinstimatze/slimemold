package analysis

import (
	"fmt"
	"sort"
	"strings"

	"github.com/justinstimatze/slimemold/types"
)

// CalibrationReport summarizes Moore et al. inventory-flag activity per
// session so the user can see what saturation/cascade thresholds would and
// would not fire on real data. Used by `slimemold calibrate` to expose the
// numbers behind the (uncalibrated by default) detector thresholds, letting
// the user pick values that fit their actual usage instead of trusting a
// guess from the codebase.
type CalibrationReport struct {
	Project string               `json:"project"`
	Total   CalibrationTotals    `json:"total"`
	Session []SessionCalibration `json:"sessions"`
	Notes   []string             `json:"notes"`
	// FiringAtThreshold maps a percentage threshold to the number of sessions
	// that would trip findSycophancySaturation at that threshold. Keys are
	// the integer percent (10, 20, 30, ...). Lets the user see the
	// cliff-edge for their data.
	SaturationFiringAtThreshold map[int]int `json:"saturation_firing_at_threshold"`
}

// CalibrationTotals aggregates flag counts across all sessions in the report.
type CalibrationTotals struct {
	Sessions            int `json:"sessions"`
	BotClaims           int `json:"bot_claims"`
	UserClaims          int `json:"user_claims"`
	GrandSignificance   int `json:"grand_significance"`
	UniqueConnection    int `json:"unique_connection"`
	DismissesEvidence   int `json:"dismisses_counterevidence"`
	AbilityOver         int `json:"ability_overstatement"`
	SentienceClaims     int `json:"sentience_claim"`
	RelationalDrift     int `json:"relational_drift"`
	ConsequentialAction int `json:"consequential_action"`
}

// SessionCalibration is one row of per-session statistics.
type SessionCalibration struct {
	SessionID         string  `json:"session_id"`
	TotalClaims       int     `json:"total_claims"`
	BotClaims         int     `json:"bot_claims"`
	BotFlagged        int     `json:"bot_flagged"`
	BotFlagRate       float64 `json:"bot_flag_rate"`
	WeakBasisClaims   int     `json:"weak_basis_claims"`
	LongestCascadeRun int     `json:"longest_cascade_run"`
}

// Calibrate computes the report from the given claims and edges. Pass the
// full project graph; per-session breakdowns are derived internally. The
// returned report can be marshaled to JSON or rendered to text via
// FormatCalibrationReport.
func Calibrate(project string, claims []types.Claim, edges []types.Edge) CalibrationReport {
	rep := CalibrationReport{
		Project:                     project,
		SaturationFiringAtThreshold: map[int]int{},
	}

	// Group by session.
	bySession := make(map[string][]*types.Claim)
	for i := range claims {
		c := &claims[i]
		bySession[c.SessionID] = append(bySession[c.SessionID], c)
	}

	for sessID, sessionClaims := range bySession {
		s := SessionCalibration{SessionID: sessID, TotalClaims: len(sessionClaims)}
		for _, c := range sessionClaims {
			switch c.Speaker {
			case types.SpeakerAssistant:
				s.BotClaims++
				if hasSycophancyFlag(c) {
					s.BotFlagged++
				}
				if c.GrandSignificance {
					rep.Total.GrandSignificance++
				}
				if c.UniqueConnection {
					rep.Total.UniqueConnection++
				}
				if c.DismissesCounterevidence {
					rep.Total.DismissesEvidence++
				}
				if c.AbilityOverstatement {
					rep.Total.AbilityOver++
				}
				if c.SentienceClaim {
					rep.Total.SentienceClaims++
				}
				if c.RelationalDrift {
					rep.Total.RelationalDrift++
				}
				if c.ConsequentialAction {
					rep.Total.ConsequentialAction++
				}
			case types.SpeakerUser:
				rep.Total.UserClaims++
				if c.GrandSignificance {
					rep.Total.GrandSignificance++
				}
				if c.SentienceClaim {
					rep.Total.SentienceClaims++
				}
				if c.RelationalDrift {
					rep.Total.RelationalDrift++
				}
				if c.ConsequentialAction {
					rep.Total.ConsequentialAction++
				}
			}
			if c.Basis == types.BasisVibes && !c.Challenged && !c.Closed {
				s.WeakBasisClaims++
			}
		}
		if s.BotClaims > 0 {
			s.BotFlagRate = float64(s.BotFlagged) / float64(s.BotClaims)
		}
		s.LongestCascadeRun = longestCascadeRun(sessionClaims, edges)
		rep.Session = append(rep.Session, s)
	}

	// Sort sessions by flag rate (descending) so the most-flagged surface first.
	sort.SliceStable(rep.Session, func(i, j int) bool {
		return rep.Session[i].BotFlagRate > rep.Session[j].BotFlagRate
	})

	// Threshold sweep: how many sessions would fire saturation at 10, 20, 30,
	// 40, 50, 60, 70, 80%? Mirrors evaluateSaturation's gates exactly so the
	// sweep accurately predicts detector behavior — total ≥ 8, bot ≥ 5,
	// weak-basis ≥ 2, plus the rate threshold.
	for _, t := range []int{10, 20, 30, 40, 50, 60, 70, 80} {
		count := 0
		for _, s := range rep.Session {
			if s.TotalClaims < 8 {
				continue
			}
			if s.BotClaims < 5 {
				continue
			}
			if s.WeakBasisClaims < 2 {
				continue
			}
			if s.BotFlagRate*100 >= float64(t) {
				count++
			}
		}
		rep.SaturationFiringAtThreshold[t] = count
	}

	rep.Total.Sessions = len(rep.Session)
	rep.Total.BotClaims = sumBotClaims(rep.Session)

	if rep.Total.Sessions == 0 {
		rep.Notes = append(rep.Notes,
			"No sessions found. Run some hook fires first, then re-run calibration.")
	}
	if rep.Total.BotClaims > 0 && rep.Total.GrandSignificance == 0 &&
		rep.Total.SentienceClaims == 0 && rep.Total.RelationalDrift == 0 {
		rep.Notes = append(rep.Notes,
			"No inventory flags fired at all. Either your sessions are clean, or the extractor is not setting flags. Compare against a known-spiral transcript to confirm.")
	}

	return rep
}

// longestCascadeRun walks claims in turn order and returns the length of the
// longest run of cascade-fitting claims uninterrupted by a questions/contradicts
// edge or a non-fitting claim. Same logic as findAmplificationCascade but
// returns the length instead of emitting a finding — used in calibration to
// see "what would the threshold need to be to fire on this session?"
func longestCascadeRun(claims []*types.Claim, edges []types.Edge) int {
	challenged := make(map[string]bool)
	for _, e := range edges {
		if e.Relation == types.RelQuestions || e.Relation == types.RelContradicts {
			challenged[e.ToID] = true
		}
	}

	ordered := make([]*types.Claim, len(claims))
	copy(ordered, claims)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].TurnNumber != ordered[j].TurnNumber {
			return ordered[i].TurnNumber < ordered[j].TurnNumber
		}
		return ordered[i].CreatedAt.Before(ordered[j].CreatedAt)
	})

	longest, run := 0, 0
	for _, c := range ordered {
		if challenged[c.ID] || !claimFitsCascade(c) {
			if run > longest {
				longest = run
			}
			run = 0
			continue
		}
		run++
	}
	if run > longest {
		longest = run
	}
	return longest
}

func sumBotClaims(sessions []SessionCalibration) int {
	n := 0
	for _, s := range sessions {
		n += s.BotClaims
	}
	return n
}

// FormatCalibrationReport renders a CalibrationReport as a human-readable
// text block suitable for terminal display. Designed to fit in a single
// screen for typical session counts.
func FormatCalibrationReport(rep CalibrationReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Calibration report — project %q\n", rep.Project)
	fmt.Fprintf(&b, "Sessions: %d   Bot claims: %d   User claims: %d\n",
		rep.Total.Sessions, rep.Total.BotClaims, rep.Total.UserClaims)
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "Inventory flag totals (across all speakers):")
	fmt.Fprintf(&b, "  grand_significance:        %d\n", rep.Total.GrandSignificance)
	fmt.Fprintf(&b, "  unique_connection:         %d  (assistant-only)\n", rep.Total.UniqueConnection)
	fmt.Fprintf(&b, "  dismisses_counterevidence: %d  (assistant-only)\n", rep.Total.DismissesEvidence)
	fmt.Fprintf(&b, "  ability_overstatement:     %d  (assistant-only)\n", rep.Total.AbilityOver)
	fmt.Fprintf(&b, "  sentience_claim:           %d\n", rep.Total.SentienceClaims)
	fmt.Fprintf(&b, "  relational_drift:          %d\n", rep.Total.RelationalDrift)
	fmt.Fprintf(&b, "  consequential_action:      %d  (Yang et al. 2026)\n", rep.Total.ConsequentialAction)
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "Saturation threshold sweep — sessions that would fire at each %:")
	thresholds := []int{10, 20, 30, 40, 50, 60, 70, 80}
	for _, t := range thresholds {
		bar := strings.Repeat("▓", rep.SaturationFiringAtThreshold[t])
		fmt.Fprintf(&b, "  %d%%  %s  (%d)\n", t, bar, rep.SaturationFiringAtThreshold[t])
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Default detector threshold is 30%. If many sessions cluster above")
	fmt.Fprintln(&b, "the cliff, raise the threshold; if very few do, lower it.")
	fmt.Fprintln(&b)

	if n := len(rep.Session); n > 0 {
		shown := n
		if shown > 10 {
			shown = 10
		}
		fmt.Fprintf(&b, "Top %d sessions by flag rate:\n", shown)
		fmt.Fprintln(&b, "  flag-rate  bot-claims  weak-basis  cascade-run  session")
		for i := 0; i < shown; i++ {
			s := rep.Session[i]
			id := s.SessionID
			if len(id) > 16 {
				id = id[:16]
			}
			fmt.Fprintf(&b, "  %5.1f%%      %4d        %3d         %2d           %s\n",
				s.BotFlagRate*100, s.BotClaims, s.WeakBasisClaims, s.LongestCascadeRun, id)
		}
		fmt.Fprintln(&b)
	}

	for _, note := range rep.Notes {
		fmt.Fprintf(&b, "Note: %s\n", note)
	}
	return b.String()
}
