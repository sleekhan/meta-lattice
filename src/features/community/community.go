package community

import (
	"math"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"meta-lattice/src/models"
	"meta-lattice/src/storage"
)

type Community struct {
	ID                 string         `json:"id"`
	SuggestedName      string         `json:"suggested_name"`
	Modules            []string       `json:"modules"`
	Size               int            `json:"size"`
	InternalWeight     float64        `json:"internal_weight"`
	ExternalWeight     float64        `json:"external_weight"`
	Cohesion           float64        `json:"cohesion"`
	PhysicalDomains    map[string]int `json:"physical_domains"`
	SpansMultipleDirs  bool           `json:"spans_multiple_dirs"`
}

type DriftAlert struct {
	FilePath          string  `json:"file_path"`
	PhysicalDomain    string  `json:"physical_domain"`
	AssignedCommunity string  `json:"assigned_community"`
	AffinityScore     float64 `json:"affinity_score"`
	Recommendation    string  `json:"recommendation"`
}

type CommunityDetectionResult struct {
	ModularityScore    float64      `json:"modularity_score"`
	ArchitectureHealth string       `json:"architecture_health"`
	TotalModules       int          `json:"total_modules"`
	CommunityCount     int          `json:"community_count"`
	Communities        []Community  `json:"communities"`
	DriftAlerts        []DriftAlert `json:"drift_alerts"`
	Summary            string       `json:"summary"`
}

type CommunityDetector struct {
	db *storage.GraphStorage
}

func NewCommunityDetector(db *storage.GraphStorage) *CommunityDetector {
	return &CommunityDetector{db: db}
}

type moduleGraph struct {
	nodes        []string
	nodeIndex    map[string]int
	domainOf     map[string]string
	adj          map[int]map[int]float64
	degree       []float64
	totalWeight2 float64 // 2m
}

func (cd *CommunityDetector) buildGraph() *moduleGraph {
	g := &moduleGraph{
		nodeIndex: make(map[string]int),
		domainOf:  make(map[string]string),
		adj:       make(map[int]map[int]float64),
	}

	// 1. Gather all L1 modules in deterministic sorted order
	l1Nodes := cd.db.QueryNodes(string(models.LevelL1), "", 0)
	type modEntry struct {
		path string
		dom  string
	}
	entries := make([]modEntry, 0, len(l1Nodes))
	for _, n := range l1Nodes {
		p, _ := n["path"].(string)
		if p == "" {
			idStr, _ := n["id"].(string)
			p = strings.TrimPrefix(idStr, "file:")
		}
		p = strings.ReplaceAll(p, "\\", "/")
		dom, _ := n["domain"].(string)
		if dom == "" {
			dom = strings.Split(p, "/")[0]
		}
		entries = append(entries, modEntry{path: p, dom: dom})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].path < entries[j].path
	})
	for _, entry := range entries {
		if _, exists := g.nodeIndex[entry.path]; !exists {
			idx := len(g.nodes)
			g.nodeIndex[entry.path] = idx
			g.nodes = append(g.nodes, entry.path)
			g.domainOf[entry.path] = entry.dom
			g.adj[idx] = make(map[int]float64)
		}
	}

	addEdge := func(u, v string, weight float64) {
		if u == v {
			return
		}
		uIdx, okU := g.nodeIndex[u]
		vIdx, okV := g.nodeIndex[v]
		if !okU || !okV {
			return
		}
		g.adj[uIdx][vIdx] += weight
		g.adj[vIdx][uIdx] += weight
		g.totalWeight2 += weight * 2
	}

	// 2. Add IMPORTS edges (weight = 2.0)
	for _, e := range cd.db.GetEdgesByType(string(models.EdgeImports)) {
		u := strings.ReplaceAll(strings.TrimPrefix(e.SourceID, "file:"), "\\", "/")
		v := strings.ReplaceAll(strings.TrimPrefix(e.TargetID, "file:"), "\\", "/")
		addEdge(u, v, 2.0)
	}

	// 3. Add cross-file CALLS edges (weight = 1.0) using fast ID extraction + memoized fallback
	symFileCache := make(map[string]string)
	resolveSymbolFile := func(symID string) string {
		if f, ok := symFileCache[symID]; ok {
			return f
		}
		if strings.HasPrefix(symID, "symbol:") {
			rest := symID[len("symbol:"):]
			if lastColon := strings.LastIndex(rest, ":"); lastColon > 0 {
				cand := strings.ReplaceAll(rest[:lastColon], "\\", "/")
				if _, exists := g.nodeIndex[cand]; exists {
					symFileCache[symID] = cand
					return cand
				}
			}
		}
		if node, ok := cd.db.GetNode(symID); ok {
			f, _ := node["file_path"].(string)
			f = strings.ReplaceAll(f, "\\", "/")
			symFileCache[symID] = f
			return f
		}
		symFileCache[symID] = ""
		return ""
	}

	for _, e := range cd.db.GetEdgesByType(string(models.EdgeCalls)) {
		sFile := resolveSymbolFile(e.SourceID)
		tFile := resolveSymbolFile(e.TargetID)
		if sFile != "" && tFile != "" && sFile != tFile {
			addEdge(sFile, tFile, 1.0)
		}
	}

	// Compute degrees
	g.degree = make([]float64, len(g.nodes))
	for u, neighbors := range g.adj {
		deg := 0.0
		for _, w := range neighbors {
			deg += w
		}
		g.degree[u] = deg
	}

	return g
}

// DetectCommunities performs Louvain community detection to discover cohesive functional modules.
func (cd *CommunityDetector) DetectCommunities(minSize int) CommunityDetectionResult {
	defer cd.db.BeginReadSession()()

	if minSize <= 0 {
		minSize = 1
	}

	g := cd.buildGraph()
	n := len(g.nodes)

	if n == 0 || g.totalWeight2 == 0 {
		return CommunityDetectionResult{
			TotalModules:       n,
			ModularityScore:    0,
			ArchitectureHealth: "INSUFFICIENT_DATA",
			Summary:            "No module call/import edges found to perform community detection.",
		}
	}

	// Louvain Phase 1: Local modularity optimization
	community := make([]int, n)
	for i := 0; i < n; i++ {
		community[i] = i
	}

	totalTot := make(map[int]float64)
	for i := 0; i < n; i++ {
		totalTot[i] = g.degree[i]
	}

	m2 := g.totalWeight2
	maxIterations := 20

	for iter := 0; iter < maxIterations; iter++ {
		moved := false
		for i := 0; i < n; i++ {
			ki := g.degree[i]
			if ki == 0 {
				continue
			}
			currComm := community[i]

			// Weights from i to neighboring communities
			commWeights := make(map[int]float64)
			for neighbor, w := range g.adj[i] {
				c := community[neighbor]
				commWeights[c] += w
			}

			// Remove i from current community
			totalTot[currComm] -= ki

			bestComm := currComm
			bestGain := (commWeights[currComm] / m2) - (totalTot[currComm]*ki)/(m2*m2)

			candComms := make([]int, 0, len(commWeights))
			for candComm := range commWeights {
				candComms = append(candComms, candComm)
			}
			sort.Ints(candComms)

			for _, candComm := range candComms {
				kIn := commWeights[candComm]
				totC := totalTot[candComm]
				// Delta Q formula
				gain := (kIn / m2) - (totC*ki)/(m2*m2)
				if gain > bestGain+1e-12 {
					bestGain = gain
					bestComm = candComm
				}
			}

			// Place i into best community
			community[i] = bestComm
			totalTot[bestComm] += ki

			if bestComm != currComm {
				moved = true
			}
		}

		if !moved {
			break
		}
	}

	// Calculate overall Modularity Q
	modularityQ := 0.0
	for i := 0; i < n; i++ {
		for j, w := range g.adj[i] {
			if community[i] == community[j] {
				modularityQ += (w - (g.degree[i]*g.degree[j])/m2)
			}
		}
	}
	modularityQ /= m2

	// Group modules by community in deterministic order
	commMap := make(map[int][]string)
	var commKeys []int
	for i, c := range community {
		if _, exists := commMap[c]; !exists {
			commKeys = append(commKeys, c)
		}
		commMap[c] = append(commMap[c], g.nodes[i])
	}
	sort.Ints(commKeys)

	var communities []Community
	var driftAlerts []DriftAlert

	for _, cKey := range commKeys {
		modules := commMap[cKey]
		if len(modules) < minSize {
			continue
		}

		sort.Strings(modules)
		modSet := make(map[string]bool, len(modules))
		for _, m := range modules {
			modSet[m] = true
		}

		// Calculate internal & external edge weights
		intWeight := 0.0
		extWeight := 0.0
		physDomains := make(map[string]int)

		for _, m := range modules {
			mIdx := g.nodeIndex[m]
			physDomains[g.domainOf[m]]++

			for nbrIdx, w := range g.adj[mIdx] {
				nbrPath := g.nodes[nbrIdx]
				if modSet[nbrPath] {
					intWeight += w
				} else {
					extWeight += w
				}
			}
		}
		intWeight /= 2.0 // each internal edge counted twice

		cohesion := 0.0
		if (intWeight + extWeight) > 0 {
			cohesion = intWeight / (intWeight + extWeight)
		}

		suggestedName := inferCommunityName(modules)

		// Find dominant physical domain in this community
		domDomain := ""
		domCount := 0
		for d, cnt := range physDomains {
			if cnt > domCount || (cnt == domCount && d < domDomain) {
				domCount = cnt
				domDomain = d
			}
		}

		// Check for drift: if a module resides in a minority physical domain (different from dominant domain)
		// and has stronger edge weight to this community than to its own physical domain
		if len(physDomains) > 1 && domCount >= 2 {
			for _, m := range modules {
				physDom := g.domainOf[m]
				if physDom != domDomain && physDomains[physDom] == 1 {
					mIdx := g.nodeIndex[m]
					wComm := 0.0
					wTotal := 0.0
					for nbrIdx, w := range g.adj[mIdx] {
						wTotal += w
						if modSet[g.nodes[nbrIdx]] {
							wComm += w
						}
					}
					affinity := cohesion
					if wTotal > 0 {
						affinity = math.Round((wComm/wTotal)*100) / 100
					}
					driftAlerts = append(driftAlerts, DriftAlert{
						FilePath:          m,
						PhysicalDomain:    physDom,
						AssignedCommunity: suggestedName,
						AffinityScore:     affinity,
						Recommendation:    "Module '" + m + "' physically resides in '" + physDom + "' but has high topological coupling with the '" + suggestedName + "' cluster (dominant domain: '" + domDomain + "').",
					})
				}
			}
		}

		communities = append(communities, Community{
			SuggestedName:     suggestedName,
			Modules:           modules,
			Size:              len(modules),
			InternalWeight:    math.Round(intWeight*100) / 100,
			ExternalWeight:    math.Round(extWeight*100) / 100,
			Cohesion:          math.Round(cohesion*100) / 100,
			PhysicalDomains:   physDomains,
			SpansMultipleDirs: len(physDomains) > 1,
		})
	}

	sort.Slice(communities, func(i, j int) bool {
		if communities[i].Size != communities[j].Size {
			return communities[i].Size > communities[j].Size
		}
		if communities[i].Cohesion != communities[j].Cohesion {
			return communities[i].Cohesion > communities[j].Cohesion
		}
		if len(communities[i].Modules) > 0 && len(communities[j].Modules) > 0 {
			return communities[i].Modules[0] < communities[j].Modules[0]
		}
		return communities[i].SuggestedName < communities[j].SuggestedName
	})

	for i := range communities {
		communities[i].ID = "community_" + strconv.Itoa(i+1)
	}

	sort.Slice(driftAlerts, func(i, j int) bool {
		return driftAlerts[i].FilePath < driftAlerts[j].FilePath
	})

	health := "EXCELLENT"
	if modularityQ < 0.2 {
		health = "HIGHLY_ENTANGLED"
	} else if modularityQ < 0.35 {
		health = "MODERATE_COUPLING"
	}

	summary := "Discovered " + strconv.Itoa(len(communities)) + " functional communities with Modularity Q = " +
		strconv.FormatFloat(modularityQ, 'f', 3, 64) + " (" + health + ")."

	return CommunityDetectionResult{
		ModularityScore:    math.Round(modularityQ*1000) / 1000,
		ArchitectureHealth: health,
		TotalModules:       n,
		CommunityCount:     len(communities),
		Communities:        communities,
		DriftAlerts:        driftAlerts,
		Summary:            summary,
	}
}

func inferCommunityName(modules []string) string {
	wordCounts := make(map[string]int)
	stopWords := map[string]bool{"src": true, "go": true, "py": true, "ts": true, "js": true, "main": true, "util": true, "test": true}

	for _, m := range modules {
		base := strings.TrimSuffix(path.Base(m), filepath.Ext(m))
		parts := strings.FieldsFunc(base, func(r rune) bool {
			return r == '_' || r == '-' || r == '.'
		})
		for _, p := range parts {
			clean := strings.ToLower(p)
			if len(clean) >= 3 && !stopWords[clean] {
				wordCounts[clean]++
			}
		}

		// Also check directory name
		dir := path.Dir(m)
		dirParts := strings.Split(dir, "/")
		for _, dp := range dirParts {
			clean := strings.ToLower(dp)
			if len(clean) >= 3 && !stopWords[clean] {
				wordCounts[clean]++
			}
		}
	}

	topWord := "module-cluster"
	maxCount := 0
	for word, count := range wordCounts {
		if count > maxCount || (count == maxCount && word < topWord) {
			maxCount = count
			topWord = word
		}
	}
	return topWord
}
