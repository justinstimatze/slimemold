package analysis

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/justinstimatze/slimemold/types"
)

// Analyze runs all structural analyses on the claim graph.
func Analyze(claims []types.Claim, edges []types.Edge, project string) (*types.Topology, *types.Vulnerabilities) {
	topo := buildTopology(claims, edges, project)
	vulns := findVulnerabilities(claims, edges, topo)
	return topo, vulns
}

func buildTopology(claims []types.Claim, edges []types.Edge, project string) *types.Topology {
	basisCounts := make(map[types.Basis]int)
	for _, c := range claims {
		basisCounts[c.Basis]++
	}

	claimMap := make(map[string]*types.Claim)
	for i := range claims {
		claimMap[claims[i].ID] = &claims[i]
	}

	adj := buildAdjacency(claims, edges)
	orphans := findOrphans(claims, adj)
	clusters := findClusters(claims, edges, adj)
	maxDepth := findMaxDepth(claims, edges)

	return &types.Topology{
		Project:     project,
		ClaimCount:  len(claims),
		EdgeCount:   len(edges),
		BasisCounts: basisCounts,
		Clusters:    clusters,
		Orphans:     orphans,
		MaxDepth:    maxDepth,
	}
}

func findVulnerabilities(claims []types.Claim, edges []types.Edge, topo *types.Topology) *types.Vulnerabilities {
	var items []types.Vulnerability

	// Load-bearing vibes: claims with weak basis that support 2+ other claims
	items = append(items, findLoadBearingVibes(claims, edges)...)

	// Bottleneck detection: high betweenness centrality
	items = append(items, findBottlenecks(claims, edges)...)

	// Unchallenged chains: long dependency chains where nothing has been questioned
	items = append(items, findUnchallengedChains(claims, edges)...)

	// Fluency traps: high confidence on weak basis
	items = append(items, findFluencyTraps(claims, edges)...)

	// Coverage imbalance: uneven attention vs importance across clusters
	items = append(items, findCoverageImbalance(claims, edges, topo)...)

	// Abandoned topics: clusters explored briefly then dropped
	items = append(items, findAbandonedClusters(claims, edges, topo)...)

	// Echo chamber: assistant validates without challenging
	items = append(items, findEchoChamber(claims, edges)...)

	// Premature closure: thought-terminating cliches capping open inquiry
	items = append(items, findPrematureClosure(claims, edges)...)

	// Moore et al. 2026 inventory detectors (see internal/analysis/inventory.go).
	// Fire on flagged assistant claims that are also doing structural work in
	// the graph (load-bearing, in cascades, or paired with weak-basis user
	// claims that are going unchallenged).
	items = append(items, findSycophancySaturation(claims, edges)...)
	items = append(items, findAbilityOverstatement(claims, edges)...)
	items = append(items, findSentienceDrift(claims, edges)...)
	items = append(items, findAmplificationCascade(claims, edges)...)
	items = append(items, findConsequentialAction(claims, edges)...)

	// Bright patterns — structural strengths (see brights.go). Emitted at
	// severity=info so the hook formatter skips them; the audit formatter
	// surfaces them in a separate "Strengths" section.
	items = append(items, findWellSourcedLoadBearer(claims, edges)...)
	items = append(items, findProductiveStressTest(claims, edges)...)
	items = append(items, findGroundedPremiseAdopted(claims, edges)...)

	// Orphan warnings
	for _, o := range topo.Orphans {
		items = append(items, types.Vulnerability{
			Severity:    "warning",
			Type:        "orphan",
			Description: fmt.Sprintf("Orphan claim (unconnected): %q", truncate(o.Text, 80)),
			ClaimIDs:    []string{o.ID},
		})
	}

	var crit, warn, info, strength int
	for _, v := range items {
		if strings.HasPrefix(v.Type, "strength_") {
			strength++
			continue
		}
		switch v.Severity {
		case "critical":
			crit++
		case "warning":
			warn++
		case "info":
			info++
		}
	}

	return &types.Vulnerabilities{
		Project:       topo.Project,
		Items:         items,
		CriticalCount: crit,
		WarningCount:  warn,
		InfoCount:     info,
		StrengthCount: strength,
	}
}

// adjacency maps claim ID → set of connected claim IDs (undirected).
type adjacency map[string]map[string]bool

func buildAdjacency(claims []types.Claim, edges []types.Edge) adjacency {
	adj := make(adjacency)
	for _, c := range claims {
		adj[c.ID] = make(map[string]bool)
	}
	for _, e := range edges {
		if _, ok := adj[e.FromID]; ok {
			adj[e.FromID][e.ToID] = true
		}
		if _, ok := adj[e.ToID]; ok {
			adj[e.ToID][e.FromID] = true
		}
	}
	return adj
}

func findOrphans(claims []types.Claim, adj adjacency) []types.Claim {
	var orphans []types.Claim
	for _, c := range claims {
		if len(adj[c.ID]) == 0 {
			orphans = append(orphans, c)
		}
	}
	return orphans
}

func findClusters(claims []types.Claim, edges []types.Edge, adj adjacency) []types.ClusterInfo {
	// Union-Find for connected components
	parent := make(map[string]string)
	for _, c := range claims {
		parent[c.ID] = c.ID
	}

	var find func(string) string
	find = func(x string) string {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	for _, e := range edges {
		if _, ok := parent[e.FromID]; !ok {
			continue
		}
		if _, ok := parent[e.ToID]; !ok {
			continue
		}
		union(e.FromID, e.ToID)
	}

	// Group claims by component
	components := make(map[string][]types.Claim)
	for _, c := range claims {
		root := find(c.ID)
		components[root] = append(components[root], c)
	}

	// Count edges per component
	compEdges := make(map[string]int)
	for _, e := range edges {
		if _, ok := parent[e.FromID]; !ok {
			continue
		}
		root := find(e.FromID)
		compEdges[root]++
	}

	var clusters []types.ClusterInfo
	id := 0
	for root, members := range components {
		if len(members) < 2 {
			continue // Skip singletons
		}
		n := len(members)
		possibleEdges := n * (n - 1) / 2
		density := 0.0
		if possibleEdges > 0 {
			density = float64(compEdges[root]) / float64(possibleEdges)
		}

		label := summarizeCluster(members)

		clusters = append(clusters, types.ClusterInfo{
			ID:      id,
			Label:   label,
			Claims:  members,
			Density: density,
			Edges:   compEdges[root],
		})
		id++
	}

	// Sort by size descending
	sort.Slice(clusters, func(i, j int) bool {
		return len(clusters[i].Claims) > len(clusters[j].Claims)
	})

	return clusters
}

func findLoadBearingVibes(claims []types.Claim, edges []types.Edge) []types.Vulnerability {
	weakBases := map[types.Basis]bool{
		types.BasisVibes:      true,
		types.BasisLLMOutput:  true,
		types.BasisAssumption: true,
	}

	// Hybrid load-bearing: a claim fires if EITHER it has recent activity
	// (LoadBearingRecentThreshold dependents within HookConversationalWindow)
	// OR persistent weight (LoadBearingPersistentThreshold total dependents
	// across all time). The recent-activity branch captures "what's currently
	// being built on"; the persistent-weight branch ensures genuinely
	// foundational claims keep surfacing even during dormant stretches when
	// they aren't getting fresh dependents but the rest of the graph still
	// rests on them.
	now := time.Now()
	recentIDs := make(map[string]bool, len(claims))
	for _, c := range claims {
		if c.CreatedAt.IsZero() || now.Sub(c.CreatedAt) <= HookConversationalWindow {
			recentIDs[c.ID] = true
		}
	}

	// Build claim map upfront — needed both for vulnerability construction
	// and for the dependent-session lookup in the stress-test signal.
	claimMap := make(map[string]*types.Claim)
	for i := range claims {
		claimMap[claims[i].ID] = &claims[i]
	}

	// Track recent dependents (current activity), total dependents (persistent
	// weight), distinct dependent-claim sessions (stress-test signal), and
	// contradicts presence (contested claims aren't stress-tested even if
	// spread out).
	recentDeps := make(map[string]int)
	totalDeps := make(map[string]int)
	depSessions := make(map[string]map[string]bool)
	hasContradicts := make(map[string]bool)
	addDepSession := func(anchor, depID string) {
		if dep, ok := claimMap[depID]; ok && dep.SessionID != "" {
			if depSessions[anchor] == nil {
				depSessions[anchor] = make(map[string]bool)
			}
			depSessions[anchor][dep.SessionID] = true
		}
	}
	for _, e := range edges {
		switch e.Relation {
		case types.RelSupports:
			totalDeps[e.FromID]++
			if recentIDs[e.ToID] {
				recentDeps[e.FromID]++
			}
			addDepSession(e.FromID, e.ToID)
		case types.RelDependsOn:
			totalDeps[e.ToID]++
			if recentIDs[e.FromID] {
				recentDeps[e.ToID]++
			}
			addDepSession(e.ToID, e.FromID)
		case types.RelContradicts:
			hasContradicts[e.FromID] = true
			hasContradicts[e.ToID] = true
		}
	}

	// Merge into a single dependents map. The persistent branch is suppressed
	// for stress-tested claims (deps span StressTestedSessionThreshold+
	// distinct sessions, no contradicts) — the conversation has implicitly
	// accepted them through repeated use across contexts.
	dependents := make(map[string]int)
	firedViaPersistent := make(map[string]bool)
	for id, total := range totalDeps {
		recent := recentDeps[id]
		switch {
		case recent >= LoadBearingRecentThreshold:
			dependents[id] = recent
		case total >= LoadBearingPersistentThreshold:
			if !hasContradicts[id] && len(depSessions[id]) >= StressTestedSessionThreshold {
				continue // stress-tested through use, suppress
			}
			dependents[id] = total
			firedViaPersistent[id] = true
		}
	}

	var vulns []types.Vulnerability
	for id, deg := range dependents {
		c, ok := claimMap[id]
		if !ok {
			continue
		}
		if !weakBases[c.Basis] {
			continue
		}
		vulns = append(vulns, types.Vulnerability{
			Severity:           "critical",
			Type:               "load_bearing_vibes",
			Description:        fmt.Sprintf("Load-bearing %s: %q supports %d other claims (never challenged: %v)", c.Basis, truncate(c.Text, 60), deg, !c.Challenged),
			ClaimIDs:           []string{c.ID},
			FiredViaPersistent: firedViaPersistent[id],
		})
	}
	return vulns
}

func findBottlenecks(claims []types.Claim, edges []types.Edge) []types.Vulnerability {
	if len(claims) < 8 {
		return nil // Too few claims for meaningful centrality analysis
	}

	// Build directed adjacency for BFS
	fwd := make(map[string][]string)
	rev := make(map[string][]string)
	for _, e := range edges {
		fwd[e.FromID] = append(fwd[e.FromID], e.ToID)
		rev[e.ToID] = append(rev[e.ToID], e.FromID)
	}

	// Approximate betweenness centrality via BFS from each node
	centrality := make(map[string]float64)
	ids := make([]string, len(claims))
	for i, c := range claims {
		ids[i] = c.ID
	}

	for _, src := range ids {
		// BFS
		dist := map[string]int{src: 0}
		paths := map[string]int{src: 1}
		queue := []string{src}
		order := []string{}

		for len(queue) > 0 {
			v := queue[0]
			queue = queue[1:]
			order = append(order, v)

			neighbors := slices.Concat(fwd[v], rev[v])
			for _, w := range neighbors {
				if _, ok := dist[w]; !ok {
					dist[w] = dist[v] + 1
					queue = append(queue, w)
				}
				if dist[w] == dist[v]+1 {
					paths[w] += paths[v]
				}
			}
		}

		// Accumulate
		delta := make(map[string]float64)
		for i := len(order) - 1; i >= 0; i-- {
			w := order[i]
			neighbors := slices.Concat(fwd[w], rev[w])
			for _, v := range neighbors {
				if dist[v] == dist[w]-1 && paths[v] > 0 {
					delta[v] += float64(paths[v]) / float64(paths[w]) * (1 + delta[w])
				}
			}
			if w != src {
				centrality[w] += delta[w]
			}
		}
	}

	// Find top centrality nodes
	claimMap := make(map[string]*types.Claim)
	for i := range claims {
		claimMap[claims[i].ID] = &claims[i]
	}

	type scored struct {
		id    string
		score float64
	}
	var sorted []scored
	for id, score := range centrality {
		sorted = append(sorted, scored{id, score})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].score > sorted[j].score
	})

	// Flag true outliers: mean + 2*stddev, capped at 5
	var vulns []types.Vulnerability
	if len(sorted) == 0 {
		return nil
	}

	var sum, sumSq float64
	for _, s := range sorted {
		sum += s.score
		sumSq += s.score * s.score
	}
	n := float64(len(sorted))
	mean := sum / n
	variance := sumSq/n - mean*mean
	stddev := 0.0
	if variance > 0 {
		stddev = math.Sqrt(variance)
	}
	threshold := mean + 2*stddev
	if threshold < 1.0 {
		threshold = 1.0 // absolute floor
	}

	const maxBottlenecks = 5
	for _, s := range sorted {
		if s.score < threshold {
			break // sorted descending, so we're done
		}
		if len(vulns) >= maxBottlenecks {
			break
		}
		c, ok := claimMap[s.id]
		if !ok {
			continue
		}
		severity := "info"
		if !c.Challenged {
			severity = "warning"
		}
		vulns = append(vulns, types.Vulnerability{
			Severity:    severity,
			Type:        "bottleneck",
			Description: fmt.Sprintf("Bottleneck (centrality %.1f): %q [%s] — many reasoning paths flow through this claim", s.score, truncate(c.Text, 60), c.Basis),
			ClaimIDs:    []string{s.id},
		})
	}

	return vulns
}

func findUnchallengedChains(claims []types.Claim, edges []types.Edge) []types.Vulnerability {
	claimMap := make(map[string]*types.Claim)
	for i := range claims {
		claimMap[claims[i].ID] = &claims[i]
	}

	// Build directed adjacency (depends_on, supports) and a set of claims
	// with incoming pushback (contradicts or questions). A claim that has
	// been contradicted or questioned mid-chain breaks the "unchallenged"
	// property — nobody pushed back is only true if no pushback edge ends
	// at this claim. Previously the detector only checked c.Challenged,
	// which is set by explicit claims.challenge calls; incoming-edge
	// pushback from the conversation itself was ignored.
	children := make(map[string][]string)
	pushedBack := make(map[string]bool)
	for _, e := range edges {
		switch e.Relation {
		case types.RelDependsOn, types.RelSupports:
			children[e.FromID] = append(children[e.FromID], e.ToID)
		case types.RelContradicts, types.RelQuestions:
			pushedBack[e.ToID] = true
		}
	}

	// DFS to find longest unchallenged chain
	var longest []string
	visited := make(map[string]bool)

	var dfs func(id string, chain []string)
	dfs = func(id string, chain []string) {
		c, ok := claimMap[id]
		if !ok || c.Challenged || pushedBack[id] || visited[id] {
			if len(chain) > len(longest) {
				longest = make([]string, len(chain))
				copy(longest, chain)
			}
			return
		}
		visited[id] = true
		chain = append(chain, id)

		kids := children[id]
		if len(kids) == 0 {
			if len(chain) > len(longest) {
				longest = make([]string, len(chain))
				copy(longest, chain)
			}
		} else {
			for _, kid := range kids {
				dfs(kid, chain)
			}
		}
		visited[id] = false
	}

	for _, c := range claims {
		dfs(c.ID, nil)
	}

	if len(longest) < 3 {
		return nil
	}

	texts := make([]string, len(longest))
	for i, id := range longest {
		if c, ok := claimMap[id]; ok {
			texts[i] = truncate(c.Text, 40)
		}
	}

	return []types.Vulnerability{{
		Severity:    "warning",
		Type:        "unchallenged_chain",
		Description: fmt.Sprintf("Unchallenged chain (%d claims): %s", len(longest), strings.Join(texts, " → ")),
		ClaimIDs:    longest,
	}}
}

// FormatAuditSummary produces the text block injected into conversations.
func FormatAuditSummary(topo *types.Topology, vulns *types.Vulnerabilities) string {
	var b strings.Builder

	fmt.Fprintf(&b, "SLIMEMOLD TOPOLOGY AUDIT [%s] — %d claims, %d edges\n", topo.Project, topo.ClaimCount, topo.EdgeCount)

	// Basis distribution
	fmt.Fprintf(&b, "  Basis: ")
	var parts []string
	for basis, count := range topo.BasisCounts {
		parts = append(parts, fmt.Sprintf("%s=%d", basis, count))
	}
	sort.Strings(parts)
	fmt.Fprintf(&b, "%s\n", strings.Join(parts, " "))

	if vulns.CriticalCount > 0 || vulns.WarningCount > 0 {
		for _, v := range vulns.Items {
			if v.Severity == "critical" {
				fmt.Fprintf(&b, "  CRITICAL: %s\n", v.Description)
			}
		}
		for _, v := range vulns.Items {
			if v.Severity == "warning" {
				fmt.Fprintf(&b, "  WARNING: %s\n", v.Description)
			}
		}
		for _, v := range vulns.Items {
			if v.Severity == "info" && !strings.HasPrefix(v.Type, "strength_") {
				fmt.Fprintf(&b, "  INFO: %s\n", v.Description)
			}
		}
	}

	// Strengths — bright/symmetric findings. Surfaced in audit output only
	// (FormatHookFindings filters these out; see that function's comment).
	var strengths []types.Vulnerability
	for _, v := range vulns.Items {
		if strings.HasPrefix(v.Type, "strength_") {
			strengths = append(strengths, v)
		}
	}
	if len(strengths) > 0 {
		fmt.Fprintf(&b, "  Strengths:\n")
		for _, s := range strengths {
			fmt.Fprintf(&b, "    + %s\n", s.Description)
		}
	}

	if len(topo.Orphans) > 0 {
		fmt.Fprintf(&b, "  Orphans: %d unconnected claims\n", len(topo.Orphans))
	}

	if len(topo.Clusters) > 0 {
		fmt.Fprintf(&b, "  Clusters: ")
		var clusterParts []string
		for _, cl := range topo.Clusters {
			clusterParts = append(clusterParts, fmt.Sprintf("%s (%d, density %.2f)", cl.Label, len(cl.Claims), cl.Density))
		}
		fmt.Fprintf(&b, "%s\n", strings.Join(clusterParts, ", "))
	}

	return b.String()
}

// findFluencyTraps flags claims where confidence exceeds what the basis warrants
// AND the claim has structural importance (at least 1 dependent).
// This is the essay's core phenomenon: processing fluency masquerading as truth.
func findFluencyTraps(claims []types.Claim, edges []types.Edge) []types.Vulnerability {
	// Basis → maximum warranted confidence (no buffer — the ceiling IS the threshold)
	// Definition claims are stipulative: an author-declared meaning ("`bd close` closes
	// the issue") is 1.0 by construction. Instructional documents were producing dozens
	// of false positives here before the ceiling was raised to 1.0. The tradeoff is we
	// miss cases where "X is Y by definition" smuggles a knowledge claim in as a
	// definition — but that pattern is hard to detect mechanically, and the false-
	// positive volume on instructional prose made the real findings hard to see.
	ceilings := map[types.Basis]float64{
		types.BasisResearch:   0.95,
		types.BasisEmpirical:  0.95,
		types.BasisDeduction:  0.95,
		types.BasisDefinition: 1.0,
		// Conventions are stipulative like definitions — a project declaring
		// "we use X" is correct-by-fiat at confidence 1.0.
		types.BasisConvention: 1.0,
		types.BasisAnalogy:    0.7,
		types.BasisVibes:      0.5,
		types.BasisLLMOutput:  0.5,
		types.BasisAssumption: 0.5,
	}

	// Hybrid structural importance, same as findLoadBearingVibes: a claim has
	// "current" structural importance if FluencyTrapRecentThreshold or more
	// recent claims depend on it, OR FluencyTrapPersistentThreshold or more
	// total claims depend on it across all time. Recent activity catches
	// what's currently being built; persistent weight catches genuinely
	// foundational claims that have gone dormant but still underpin a lot.
	now := time.Now()
	recentIDs := make(map[string]bool, len(claims))
	for _, c := range claims {
		if c.CreatedAt.IsZero() || now.Sub(c.CreatedAt) <= HookConversationalWindow {
			recentIDs[c.ID] = true
		}
	}
	claimMap := make(map[string]*types.Claim)
	for i := range claims {
		claimMap[claims[i].ID] = &claims[i]
	}
	recentDeps := make(map[string]int)
	totalDeps := make(map[string]int)
	depSessions := make(map[string]map[string]bool)
	hasContradicts := make(map[string]bool)
	addDepSession := func(anchor, depID string) {
		if dep, ok := claimMap[depID]; ok && dep.SessionID != "" {
			if depSessions[anchor] == nil {
				depSessions[anchor] = make(map[string]bool)
			}
			depSessions[anchor][dep.SessionID] = true
		}
	}
	for _, e := range edges {
		switch e.Relation {
		case types.RelSupports:
			totalDeps[e.FromID]++
			if recentIDs[e.ToID] {
				recentDeps[e.FromID]++
			}
			addDepSession(e.FromID, e.ToID)
		case types.RelDependsOn:
			totalDeps[e.ToID]++
			if recentIDs[e.FromID] {
				recentDeps[e.ToID]++
			}
			addDepSession(e.ToID, e.FromID)
		case types.RelContradicts:
			hasContradicts[e.FromID] = true
			hasContradicts[e.ToID] = true
		}
	}
	hasDependents := make(map[string]bool)
	firedViaPersistent := make(map[string]bool)
	for id, total := range totalDeps {
		switch {
		case recentDeps[id] >= FluencyTrapRecentThreshold:
			hasDependents[id] = true
		case total >= FluencyTrapPersistentThreshold:
			// Stress-test suppression — same rationale as findLoadBearingVibes.
			if !hasContradicts[id] && len(depSessions[id]) >= StressTestedSessionThreshold {
				continue
			}
			hasDependents[id] = true
			firedViaPersistent[id] = true
		}
	}

	var vulns []types.Vulnerability
	for _, c := range claims {
		if c.Challenged || !hasDependents[c.ID] {
			continue
		}
		ceiling, ok := ceilings[c.Basis]
		if !ok {
			continue
		}
		if c.Confidence > ceiling {
			vulns = append(vulns, types.Vulnerability{
				Severity:           "critical",
				Type:               "fluency_trap",
				Description:        fmt.Sprintf("Fluency trap: %q stated at confidence %.1f but basis is %s — processing fluency may masquerade as truth", truncate(c.Text, 60), c.Confidence, c.Basis),
				ClaimIDs:           []string{c.ID},
				FiredViaPersistent: firedViaPersistent[c.ID],
			})
		}
	}
	return vulns
}

// findCoverageImbalance detects clusters getting disproportionate attention relative to
// their foundational importance — the slime mold foraging unevenly.
//
// Importance = how many directed dependents a cluster's claims have (via depends_on/supports edges).
// Attention = cluster size + internal edge count.
// Clusters are formed by union-find (undirected), so cross-cluster edges don't exist.
// Instead, importance is measured by directed in-degree within the cluster: how many
// claims point TO this cluster's members via depends_on/derived_from (they're depended upon).
func findCoverageImbalance(claims []types.Claim, edges []types.Edge, topo *types.Topology) []types.Vulnerability {
	if len(topo.Clusters) < 2 {
		return nil
	}

	// Count directed dependents per claim (how many other claims rely on this one)
	dependents := make(map[string]int)
	for _, e := range edges {
		switch e.Relation {
		case types.RelSupports:
			dependents[e.FromID]++
		case types.RelDependsOn:
			dependents[e.ToID]++
		}
	}

	type clusterMetrics struct {
		importance float64 // sum of directed dependents for all claims in cluster
		attention  float64 // size + internal edges
	}
	metrics := make([]clusterMetrics, len(topo.Clusters))

	for i, cl := range topo.Clusters {
		for _, c := range cl.Claims {
			metrics[i].importance += float64(dependents[c.ID])
		}
		metrics[i].attention = float64(len(cl.Claims) + cl.Edges)
	}

	// Normalize to [0,1]
	var maxImportance, maxAttention float64
	for _, m := range metrics {
		if m.importance > maxImportance {
			maxImportance = m.importance
		}
		if m.attention > maxAttention {
			maxAttention = m.attention
		}
	}
	if maxImportance == 0 || maxAttention == 0 {
		return nil
	}

	var vulns []types.Vulnerability
	for i, cl := range topo.Clusters {
		if len(cl.Claims) < 3 {
			continue
		}
		normImp := metrics[i].importance / maxImportance
		normAtt := metrics[i].attention / maxAttention

		if normAtt > 0.7 && normImp < 0.3 {
			vulns = append(vulns, types.Vulnerability{
				Severity:    "warning",
				Type:        "coverage_imbalance",
				Description: fmt.Sprintf("Rabbit hole: cluster %q has high internal activity but low foundational importance — following the interesting gradient?", truncate(cl.Label, 40)),
				ClaimIDs:    clusterClaimIDs(cl),
			})
		} else if normImp > 0.7 && normAtt < 0.3 {
			vulns = append(vulns, types.Vulnerability{
				Severity:    "warning",
				Type:        "coverage_imbalance",
				Description: fmt.Sprintf("Neglected foundation: cluster %q is heavily depended on but has little internal development", truncate(cl.Label, 40)),
				ClaimIDs:    clusterClaimIDs(cl),
			})
		}
	}
	return vulns
}

// findAbandonedClusters detects topics explored briefly then dropped — hedonic halting itself.
func findAbandonedClusters(claims []types.Claim, edges []types.Edge, topo *types.Topology) []types.Vulnerability {
	// Collect distinct sessions ordered by earliest CreatedAt
	sessionFirst := make(map[string]int64) // session → earliest unix timestamp
	for _, c := range claims {
		ts := c.CreatedAt.Unix()
		if first, ok := sessionFirst[c.SessionID]; !ok || ts < first {
			sessionFirst[c.SessionID] = ts
		}
	}
	if len(sessionFirst) < 2 {
		return nil
	}

	// Find the most recent session
	var latestSession string
	var latestTime int64
	for sid, ts := range sessionFirst {
		if ts > latestTime {
			latestTime = ts
			latestSession = sid
		}
	}

	var vulns []types.Vulnerability
	for _, cl := range topo.Clusters {
		if len(cl.Claims) < 2 {
			continue
		}

		// Check if any claim in this cluster is from the latest session
		hasRecent := false
		sessions := make(map[string]bool)
		for _, c := range cl.Claims {
			sessions[c.SessionID] = true
			if c.SessionID == latestSession {
				hasRecent = true
				break
			}
		}

		if !hasRecent && len(sessions) > 0 {
			vulns = append(vulns, types.Vulnerability{
				Severity:    "info",
				Type:        "abandoned_topic",
				Description: fmt.Sprintf("Abandoned topic: cluster %q (%d claims) has no activity in the most recent session — explored then dropped?", truncate(cl.Label, 40), len(cl.Claims)),
				ClaimIDs:    clusterClaimIDs(cl),
			})
		}
	}
	return vulns
}

// findEchoChamber detects when the assistant systematically validates the user
// without challenging — structural sycophancy visible in the graph.
//
// Detection approach: since the extractor creates mostly same-speaker edges,
// cross-speaker edge counting is unreliable. Instead we look at:
// 1. Whether ANY contradicts edges exist between speakers (in either direction)
// 2. The ratio of user vibes/assumption claims to total user claims
// 3. Whether user claims ever get challenged (marked as challenged in the DB)
func findEchoChamber(claims []types.Claim, edges []types.Edge) []types.Vulnerability {
	if len(claims) < 10 {
		return nil
	}

	claimMap := make(map[string]*types.Claim)
	for i := range claims {
		claimMap[claims[i].ID] = &claims[i]
	}

	weakBases := map[types.Basis]bool{
		types.BasisVibes:      true,
		types.BasisAssumption: true,
		types.BasisLLMOutput:  true,
	}

	// Count cross-speaker contradictions (any direction)
	crossSpeakerContradictions := 0
	// Count any cross-speaker edges at all
	crossSpeakerEdges := 0

	for _, e := range edges {
		from, fromOK := claimMap[e.FromID]
		to, toOK := claimMap[e.ToID]
		if !fromOK || !toOK || from.Speaker == to.Speaker {
			continue
		}
		crossSpeakerEdges++
		// Both contradicts and questions count as cross-speaker pushback for
		// echo-chamber detection. A questions edge is epistemic challenge
		// without counter-claim — still friction, still absence of echo.
		if e.Relation == types.RelContradicts || e.Relation == types.RelQuestions {
			crossSpeakerContradictions++
		}
	}

	// Count user claims by basis strength
	var userClaims, userWeakClaims, userChallenged int
	var assistantClaims, assistantWeakClaims int

	for _, c := range claims {
		switch c.Speaker {
		case types.SpeakerUser:
			userClaims++
			if weakBases[c.Basis] {
				userWeakClaims++
			}
			if c.Challenged {
				userChallenged++
			}
		case types.SpeakerAssistant:
			assistantClaims++
			if weakBases[c.Basis] {
				assistantWeakClaims++
			}
		}
	}

	var vulns []types.Vulnerability

	// Pattern 1: Zero cross-speaker contradictions with substantial claims from both speakers.
	// Threshold is high (10+) because contradicts edges are rarely extracted by the LLM,
	// so this only fires when there's enough data to make the absence meaningful.
	if crossSpeakerContradictions == 0 && userClaims >= 10 && assistantClaims >= 10 {
		vulns = append(vulns, types.Vulnerability{
			Severity: "warning",
			Type:     "echo_chamber",
			Description: fmt.Sprintf(
				"Echo chamber: %d user claims and %d assistant claims with zero contradictions between them — no disagreement in the entire conversation",
				userClaims, assistantClaims),
			ClaimIDs: nil,
		})
	}

	// Pattern 2: High weak-basis rate with no challenges.
	// Skip if graph has only one session — on first extraction, nothing has had
	// a chance to be challenged yet, so this would always false-positive.
	sessions := make(map[string]bool)
	for _, c := range claims {
		sessions[c.SessionID] = true
	}
	if userWeakClaims >= 3 && userChallenged == 0 && len(sessions) >= 2 {
		weakRate := float64(userWeakClaims) / float64(userClaims)
		if weakRate > 0.5 {
			vulns = append(vulns, types.Vulnerability{
				Severity: "warning",
				Type:     "echo_chamber",
				Description: fmt.Sprintf(
					"Echo chamber: %.0f%% of user claims (%d/%d) have weak basis and none were challenged — unsourced assertions going unexamined",
					weakRate*100, userWeakClaims, userClaims),
				ClaimIDs: nil,
			})
		}
	}

	return vulns
}

// findPrematureClosure detects claims that function as rhetorical stop signals —
// phrases that feel like conclusions but don't actually resolve open questions.
// "Turtles all the way down" is the canonical example: it frames an infinite
// regress as wisdom and everyone stops thinking.
//
// Detection uses two signals:
//  1. The extraction model flagged terminates_inquiry=true (rhetorical judgment)
//  2. The claim sits at a leaf position in the graph with unresolved upstream claims
//     (structural context — the closure is capping something that was still open)
//
// Either signal alone produces an info-level finding. Both together produce a warning.
// The upstream context matters: a thought-terminating cliche in isolation is just
// a cliche. A thought-terminating cliche that caps a chain of weak-basis claims
// is actively preventing the investigation that would strengthen the argument.
func findPrematureClosure(claims []types.Claim, edges []types.Edge) []types.Vulnerability {
	if len(claims) < 3 {
		return nil
	}

	weakBases := map[types.Basis]bool{
		types.BasisVibes:      true,
		types.BasisLLMOutput:  true,
		types.BasisAssumption: true,
	}

	// Build maps: who depends on whom, who has dependents
	claimMap := make(map[string]*types.Claim)
	for i := range claims {
		claimMap[claims[i].ID] = &claims[i]
	}

	hasDependents := make(map[string]bool) // claims that other claims depend on
	upstream := make(map[string][]string)  // claim ID → IDs of claims that feed into it

	for _, e := range edges {
		switch e.Relation {
		case types.RelSupports:
			hasDependents[e.FromID] = true
			upstream[e.ToID] = append(upstream[e.ToID], e.FromID)
		case types.RelDependsOn:
			hasDependents[e.ToID] = true
			upstream[e.FromID] = append(upstream[e.FromID], e.ToID)
		}
	}

	// Find leaf claims (nothing depends on them — they're terminal)
	var leaves []string
	for _, c := range claims {
		if !hasDependents[c.ID] {
			leaves = append(leaves, c.ID)
		}
	}

	// For each leaf, check if it's a premature closure
	var vulns []types.Vulnerability
	for _, leafID := range leaves {
		c := claimMap[leafID]
		if c == nil {
			continue
		}

		flaggedByLLM := c.TerminatesInquiry

		// Skip upstream walk for strong-basis leaves without an LLM flag —
		// a deduction leaf capping weak upstream is normal reasoning, not
		// premature closure. Only weak-basis leaves (or LLM-flagged ones)
		// warrant the structural check.
		if !weakBases[c.Basis] && !flaggedByLLM {
			continue
		}

		// Check upstream context: does this leaf cap weak-basis claims?
		capsWeakUpstream := false
		upstreamIDs := upstream[leafID]
		for _, uid := range upstreamIDs {
			if uc, ok := claimMap[uid]; ok && weakBases[uc.Basis] && !uc.Challenged {
				capsWeakUpstream = true
				break
			}
		}

		// Also walk one more level — the immediate parent might be strong
		// but ITS parents might be weak
		if !capsWeakUpstream {
			for _, uid := range upstreamIDs {
				for _, grandUID := range upstream[uid] {
					if uc, ok := claimMap[grandUID]; ok && weakBases[uc.Basis] && !uc.Challenged {
						capsWeakUpstream = true
						break
					}
				}
				if capsWeakUpstream {
					break
				}
			}
		}

		if !flaggedByLLM && !capsWeakUpstream {
			continue
		}

		severity := "info"
		if flaggedByLLM && capsWeakUpstream {
			severity = "warning"
		}

		desc := fmt.Sprintf("Premature closure: %q terminates a line of reasoning", truncate(c.Text, 60))
		if capsWeakUpstream {
			desc += " that still has unverified claims upstream"
		}
		if flaggedByLLM {
			desc += " — flagged as thought-terminating cliche"
		}

		vulns = append(vulns, types.Vulnerability{
			Severity:    severity,
			Type:        "premature_closure",
			Description: desc,
			ClaimIDs:    []string{c.ID},
		})

		if len(vulns) >= 3 {
			break // cap to avoid flooding the audit with info-level leaf findings
		}
	}

	return vulns
}

func clusterClaimIDs(cl types.ClusterInfo) []string {
	ids := make([]string, 0, len(cl.Claims))
	for _, c := range cl.Claims {
		ids = append(ids, c.ID)
	}
	return ids
}

// Hook filter constants. Tunable but deliberately conservative:
//
// HookCooldownWindow suppresses the same (claim, finding_type) from firing
// twice within a day.
//
// HookMaxClaimAge drops any claim older than a week from priority selection
// — stale findings from prior sessions otherwise dominate the priority slot
// because slimemold's graph accumulates cross-session by design. Age decay
// is the substitute for session isolation (see README "Session Model").
//
// HookColdStartMinClaims gates the whole hook on graph size. On a tiny
// graph (first few turns of a conversation), load-bearing analysis is
// unreliable regardless of whether individual claims cross the per-detector
// dependent thresholds — three claims in a chain look load-bearing but
// it's just small-N artifact. Imported from buddy's COLD_START_MIN_CLAIMS.
const (
	HookCooldownWindow = 24 * time.Hour

	// HookPersistentCooldown applies to findings that fired only via the
	// persistent-weight branch (Vulnerability.FiredViaPersistent=true). These
	// are "still underpinning stuff but no fresh activity" callbacks rather
	// than "currently being built on" findings. Surfacing them daily would
	// be the noise the user explicitly named — they should arrive as
	// occasional reminders, not standing repeats.
	HookPersistentCooldown = 7 * 24 * time.Hour

	HookMaxClaimAge        = 7 * 24 * time.Hour
	HookColdStartMinClaims = 6

	// HookConversationalWindow defines what counts as "current conversation"
	// for the load-bearing / fluency / centrality detectors. A claim is
	// conversationally load-bearing only if recent claims depend on it; a
	// claim is a current bottleneck only if it sits on paths through the
	// recent subgraph. This shifts these detectors from "graph-historical
	// weight" to "what's currently being built on" — what a thoughtful
	// collaborator would surface, not what has the most cross-session
	// graph centrality.
	//
	// 6 hours captures a typical working session including breaks. Tighter
	// windows (e.g. 2h) drop too many still-relevant claims when a session
	// is long; wider windows (24h+) let yesterday's stale stuff dominate.
	// Claims with zero CreatedAt (tests, legacy) are treated as recent so
	// the change is non-breaking.
	HookConversationalWindow = 6 * time.Hour

	// LoadBearingRecentThreshold and LoadBearingPersistentThreshold define the
	// hybrid load-bearing criterion. A claim fires as load-bearing if EITHER
	// it has Recent-Threshold or more dependents created within the
	// HookConversationalWindow (current activity), OR it has Persistent-
	// Threshold or more total dependents across all time (persistent weight).
	//
	// Recent threshold matches the original detector's bar (2+). Persistent
	// threshold is calibrated against the actual graph distribution: top ~15
	// claims by dependent count meet 8+, so this branch surfaces only truly
	// foundational unverified claims, not every old claim with a few stale
	// supports. Persistent-only firings carry FiredViaPersistent=true so
	// FormatHookFindings can apply HookPersistentCooldown (longer cooldown)
	// rather than the standard 24h — they surface as occasional reminders.
	LoadBearingRecentThreshold     = 2
	LoadBearingPersistentThreshold = 8

	// FluencyTrapRecentThreshold and FluencyTrapPersistentThreshold use the
	// same hybrid logic. The original detector required ≥1 dependent (any
	// structural importance); the hybrid keeps that bar for recent activity
	// while requiring genuine persistence (8+) for the dormant-callback path.
	FluencyTrapRecentThreshold     = 1
	FluencyTrapPersistentThreshold = 8

	// StressTestedSessionThreshold suppresses the persistent-weight branch for
	// claims whose dependents span this many distinct sessions without any
	// contradicts edges in the graph. The intuition: if the conversation has
	// implicitly added support to a claim across multiple sessions over time
	// without anyone pushing back, the claim has been stress-tested through
	// use. It's still structurally load-bearing, but the conversation has
	// effectively accepted it as a working premise — surfacing it as a finding
	// adds noise rather than signal.
	//
	// Burst-pattern claims (8+ deps all from a single session) still fire
	// from the persistent branch — those are the high-risk "everything
	// rested on this for one session and we never came back to verify" cases.
	//
	// Sessions rather than calendar days are the right granularity: a single
	// long session can span days but is one context; multiple short sessions
	// in one day represent multiple distinct contexts.
	StressTestedSessionThreshold = 3
)

// FormatHookFindings produces a terse, directive summary for hook injection.
// Prioritizes: criticals → unchallenged chains → top bottleneck. Caps at maxFindings.
// Skips already-challenged claims, claims older than HookMaxClaimAge, and any
// (claim_id, finding_type) whose last fire is still inside the appropriate
// cooldown window (HookCooldownWindow for normal findings, HookPersistentCooldown
// for findings that fired only via the persistent-weight branch).
//
// recentFires maps (claim_id|finding_type) → last fire timestamp. Callers
// should query the DB with the longer window (HookPersistentCooldown) so
// every potentially-suppressed fire is visible; differential cooldown is
// applied here based on the candidate's FiredViaPersistent flag.
//
// Returns (summary, pickedClaimID, pickedFindingType, pickedFiredViaPersistent).
// Callers should call db.LogHookFire(...) to record the fire so subsequent
// invocations within the cooldown window skip it; the persistent-flag is
// passed back so the caller can decide whether to use a longer cooldown
// when logging.
//
// Discipline: **never fabricate a finding to fill silence.** If no detector
// fires, no finding passes the cooldown/age filters, or the graph is below
// HookColdStartMinClaims, return "". The hook is allowed to produce a
// finding or be silent — it is not allowed to manufacture one because the
// slot exists.
func FormatHookFindings(topo *types.Topology, vulns *types.Vulnerabilities, claims []types.Claim, recentFires map[string]time.Time, newClaims, newEdges, maxFindings int) (string, string, string, bool) {
	// Cold-start gate: below a minimum graph size, load-bearing analysis is
	// dominated by small-sample artifacts. Suppress the hook entirely.
	if len(claims) < HookColdStartMinClaims {
		return "", "", "", false
	}
	if len(vulns.Items) == 0 {
		return "", "", "", false
	}

	// Index claims by ID for age lookups.
	claimByID := make(map[string]*types.Claim, len(claims))
	for i := range claims {
		claimByID[claims[i].ID] = &claims[i]
	}
	now := time.Now()

	// Collect findings by priority
	type finding struct {
		priority int
		item     types.Vulnerability
	}
	var findings []finding

	for _, v := range vulns.Items {
		// Bright/strength findings are deliberately excluded from the hook.
		// The hook path is deficit-only by design — bright findings here
		// would pile extra validation onto a channel meant for redirection.
		// They surface via FormatAuditSummary instead.
		if strings.HasPrefix(v.Type, "strength_") {
			continue
		}
		// Cooldown + age filters: skip candidates whose anchor claim has
		// already fired this finding type recently, or whose anchor claim
		// is too old to be worth nagging about. Findings with no anchor
		// claim (e.g. coverage_imbalance) are always eligible.
		if skipAnchor(v, claimByID, recentFires, now) {
			continue
		}
		switch v.Type {
		case "load_bearing_vibes", "fluency_trap":
			findings = append(findings, finding{0, v})
		case "unchallenged_chain", "echo_chamber", "premature_closure":
			findings = append(findings, finding{1, v})
		case "coverage_imbalance":
			findings = append(findings, finding{2, v})
		case "bottleneck":
			findings = append(findings, finding{3, v})
		case "abandoned_topic":
			findings = append(findings, finding{4, v})
		}
	}

	if len(findings) == 0 {
		return "", "", "", false
	}

	// Sort by detector priority, then within priority prefer findings on
	// claims with any Moore inventory flag set. The inventory flags
	// (grand_significance, sentience_claim, dismisses_counterevidence, etc.)
	// are explicit risk markers extracted by the LLM annotator — a load-
	// bearing vibes claim that's also marked grand_significance is
	// structurally riskier than a neutral one and should win the priority slot.
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].priority != findings[j].priority {
			return findings[i].priority < findings[j].priority
		}
		iFlagged := vulnerabilityHasInventoryFlag(findings[i].item, claimByID)
		jFlagged := vulnerabilityHasInventoryFlag(findings[j].item, claimByID)
		if iFlagged != jFlagged {
			return iFlagged // flagged claims first within same priority
		}
		return false // stable: preserve detector order for equal-priority unflagged
	})

	// Pick a phrasing for the top finding. Phrasings are rotated per-claim
	// (hash-of-claim-text → template index) so the same claim stays stable
	// across re-runs but different claims get different wording. Avoids the
	// literal-quote leakage the single-template path used to cause.
	top := findings[0].item
	claimText := extractClaimText(top.Description)
	phrasing := renderPhrasing(top.Type, top.Description, claimText)

	var b strings.Builder
	fmt.Fprintf(&b, "Reasoning topology observation (slimemold):\n\n")
	fmt.Fprintf(&b, "Priority finding: %s\n", top.Description)
	fmt.Fprintf(&b, "Suggested response: %s\n", phrasing)

	pickedClaimID := ""
	if len(top.ClaimIDs) > 0 {
		pickedClaimID = top.ClaimIDs[0]
	}
	pickedFindingType := top.Type

	// Include remaining findings as context (lower priority)
	if len(findings) > 1 {
		remaining := findings[1:]
		if len(remaining) > maxFindings-1 {
			remaining = remaining[:maxFindings-1]
		}
		b.WriteString("\nAdditional structural observations:\n")
		for _, f := range remaining {
			fmt.Fprintf(&b, "- %s\n", f.item.Description)
		}
	}

	return strings.TrimRight(b.String(), "\n"), pickedClaimID, pickedFindingType, top.FiredViaPersistent
}

// vulnerabilityHasInventoryFlag reports whether the anchor claim of a
// vulnerability has any Moore inventory flag set — used as a secondary
// priority criterion so flagged claims surface ahead of neutral ones at
// the same detector-priority tier.
func vulnerabilityHasInventoryFlag(v types.Vulnerability, claimByID map[string]*types.Claim) bool {
	if len(v.ClaimIDs) == 0 {
		return false
	}
	c, ok := claimByID[v.ClaimIDs[0]]
	if !ok {
		return false
	}
	return c.GrandSignificance || c.UniqueConnection || c.DismissesCounterevidence ||
		c.AbilityOverstatement || c.SentienceClaim || c.RelationalDrift ||
		c.ConsequentialAction
}

// skipAnchor reports whether a vulnerability should be filtered from the
// priority pool because its anchor claim has fired this finding type
// recently (cooldown) or is too old to be worth surfacing (age decay).
// Findings with no ClaimIDs (e.g. coverage_imbalance) never skip.
//
// Differential cooldown: persistent-only findings (FiredViaPersistent=true)
// use HookPersistentCooldown so they surface as occasional reminders rather
// than daily noise; everything else uses HookCooldownWindow.
func skipAnchor(v types.Vulnerability, claimByID map[string]*types.Claim, recentFires map[string]time.Time, now time.Time) bool {
	if len(v.ClaimIDs) == 0 {
		return false
	}
	anchor := v.ClaimIDs[0]
	if firedAt, ok := recentFires[anchor+"|"+v.Type]; ok {
		cooldown := HookCooldownWindow
		if v.FiredViaPersistent {
			cooldown = HookPersistentCooldown
		}
		if now.Sub(firedAt) < cooldown {
			return true
		}
	}
	if c, ok := claimByID[anchor]; ok {
		if !c.CreatedAt.IsZero() && now.Sub(c.CreatedAt) > HookMaxClaimAge {
			return true
		}
	}
	return false
}

// extractClaimText pulls the quoted claim text from a vulnerability description.
// Descriptions look like: `Load-bearing vibes: "some claim text..." supports N other claims`
func extractClaimText(desc string) string {
	start := strings.Index(desc, `"`)
	if start < 0 {
		return desc
	}
	end := strings.Index(desc[start+1:], `"`)
	if end < 0 {
		return desc[start+1:]
	}
	text := desc[start+1 : start+1+end]
	// Remove truncation ellipsis
	text = strings.TrimSuffix(text, "...")
	return text
}

// generateQuestion is superseded by renderPhrasing in phrasings.go, which
// rotates among multiple templates per finding type keyed on claim text.
// Design informed by: Miller et al. (1993) motivational interviewing — roll
// with resistance, don't confront. Deci & Ryan (1987) — autonomy-supportive
// framing produces internalized change. Mangels et al. (2006) — gain framing
// causes people to attend to corrective content instead of emotional threat.
// Graesser et al. (1995) — effective tutors use indirect prompts, not confrontation.

func summarizeCluster(claims []types.Claim) string {
	// Use first few words of the first claim as label
	if len(claims) == 0 {
		return "unnamed"
	}
	text := claims[0].Text
	words := strings.Fields(text)
	if len(words) > 4 {
		words = words[:4]
	}
	return strings.Join(words, " ")
}

func findMaxDepth(claims []types.Claim, edges []types.Edge) int {
	// Build directed adjacency
	children := make(map[string][]string)
	hasParent := make(map[string]bool)
	claimSet := make(map[string]bool)

	for _, c := range claims {
		claimSet[c.ID] = true
	}
	for _, e := range edges {
		if !claimSet[e.FromID] || !claimSet[e.ToID] {
			continue
		}
		children[e.FromID] = append(children[e.FromID], e.ToID)
		hasParent[e.ToID] = true
	}

	// BFS from roots (nodes with no parents)
	maxD := 0
	for _, c := range claims {
		if hasParent[c.ID] {
			continue
		}
		// BFS
		type item struct {
			id    string
			depth int
		}
		queue := []item{{c.ID, 1}}
		visited := map[string]bool{c.ID: true}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			if cur.depth > maxD {
				maxD = cur.depth
			}
			for _, kid := range children[cur.id] {
				if !visited[kid] {
					visited[kid] = true
					queue = append(queue, item{kid, cur.depth + 1})
				}
			}
		}
	}
	return maxD
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
