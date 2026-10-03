package blast

import (
	"fmt"
	"math"
	"path"
	"sort"
	"strings"

	"meta-lattice/src/models"
	"meta-lattice/src/storage"
)

type BlastRadiusEstimator struct {
	db *storage.GraphStorage
}

func NewBlastRadiusEstimator(db *storage.GraphStorage) *BlastRadiusEstimator {
	return &BlastRadiusEstimator{db: db}
}

func (b *BlastRadiusEstimator) resolveTargetNodes(symbolOrPath string) []map[string]any {
	// 1. Exact node id lookup
	if node, ok := b.db.GetNode(symbolOrPath); ok {
		return []map[string]any{node}
	}

	// 2. File path lookup
	cleanPath := strings.ReplaceAll(symbolOrPath, "\\", "/")
	fileID := "file:" + cleanPath
	if fNode, ok := b.db.GetNode(fileID); ok {
		outgoing := b.db.GetOutgoingEdges(fileID, string(models.EdgeContains))
		var l3Children []map[string]any
		for _, e := range outgoing {
			if child, exists := b.db.GetNode(e.TargetID); exists {
				lvl, _ := child["level"].(string)
				if lvl == string(models.LevelL3) {
					l3Children = append(l3Children, child)
				}
			}
		}
		if len(l3Children) > 0 {
			return l3Children
		}
		return []map[string]any{fNode}
	}

	// 3. Symbol name match
	var matches []map[string]any
	for id, d := range b.db.AllNodes() {
		name, _ := d["name"].(string)
		if name == symbolOrPath || strings.HasSuffix(name, "."+symbolOrPath) {
			cp := make(map[string]any, len(d)+1)
			for k, v := range d {
				cp[k] = v
			}
			cp["id"] = id
			matches = append(matches, cp)
		}
	}
	return matches
}

type queueItem struct {
	nodeID string
	hop    int
}

// domainOf returns the domain of a node. L3 symbols and L2 classes do not carry
// a domain property themselves, so fall back to the domain of their file node.
func (b *BlastRadiusEstimator) domainOf(node map[string]any) string {
	if dom, _ := node["domain"].(string); dom != "" {
		return dom
	}
	filePath, _ := node["file_path"].(string)
	if filePath == "" {
		filePath, _ = node["path"].(string)
	}
	if filePath == "" {
		return ""
	}
	if fileNode, ok := b.db.GetNode("file:" + filePath); ok {
		dom, _ := fileNode["domain"].(string)
		return dom
	}
	return ""
}

// EstimateBlastRadius simulates ripple effects, breaking change risks, and top 10 impacted sites.
func (b *BlastRadiusEstimator) EstimateBlastRadius(symbolOrPath string, changeType string, maxHops int) map[string]any {
	if maxHops <= 0 {
		maxHops = 4
	}
	if changeType == "" {
		changeType = "signature"
	}

	targets := b.resolveTargetNodes(symbolOrPath)
	if len(targets) == 0 {
		return map[string]any{
			"error":      "Target '" + symbolOrPath + "' could not be resolved in LatticeDB.",
			"suggestion": "Run `zoom_search` to find valid symbols or sync the index.",
		}
	}

	changeMultipliers := map[string]float64{
		"signature": 2.0,
		"removal":   2.5,
		"rename":    2.0,
		"body":      1.0,
	}
	changeMult, ok := changeMultipliers[strings.ToLower(changeType)]
	if !ok {
		changeMult = 1.5
	}

	visited := make(map[string]bool)
	var queue []queueItem
	initialDomains := make(map[string]bool)
	targetIsPublic := false

	for _, t := range targets {
		tID, _ := t["id"].(string)
		visited[tID] = true
		queue = append(queue, queueItem{nodeID: tID, hop: 0})

		if isPub, ok := t["is_public"].(bool); ok && isPub {
			targetIsPublic = true
		}
		if dom := b.domainOf(t); dom != "" {
			initialDomains[dom] = true
		}
	}

	dependencyEdges := map[string]bool{
		string(models.EdgeCalls):      true,
		string(models.EdgeImports):    true,
		string(models.EdgeReferences): true,
	}

	var affectedItems []map[string]any
	maxExploration := 250
	isTruncated := false

	// Breadth-First Search reverse traversal
	for len(queue) > 0 {
		if len(visited) >= maxExploration {
			isTruncated = true
			break
		}

		curr := queue[0]
		queue = queue[1:]

		if curr.hop >= maxHops {
			continue
		}

		// Incoming edges
		incoming := b.db.GetIncomingEdges(curr.nodeID, "")
		for _, edge := range incoming {
			if !dependencyEdges[edge.EdgeType] {
				continue
			}

			callerID := edge.SourceID
			if visited[callerID] {
				continue
			}

			visited[callerID] = true
			nextHop := curr.hop + 1
			queue = append(queue, queueItem{nodeID: callerID, hop: nextHop})

			callerNode, exists := b.db.GetNode(callerID)
			if !exists {
				continue
			}

			callerName, _ := callerNode["name"].(string)
			if callerName == "" {
				callerPath, _ := callerNode["path"].(string)
				if callerPath != "" {
					callerName = path.Base(callerPath)
				} else {
					callerName = callerID
				}
			}

			callerFile, _ := callerNode["file_path"].(string)
			if callerFile == "" {
				callerFile, _ = callerNode["path"].(string)
			}

			callerDomain := b.domainOf(callerNode)
			if callerDomain == "" {
				callerDomain = "root"
			}

			callerLevel, _ := callerNode["level"].(string)
			if callerLevel == "" {
				callerLevel = "L3"
			}

			// Weight computation
			hopDecay := math.Pow(0.5, float64(nextHop-1))
			visMult := 1.0
			if targetIsPublic {
				visMult = 1.8
			}

			crossDomainMult := 1.0
			if callerDomain != "root" && !initialDomains[callerDomain] {
				crossDomainMult = 1.5
			}

			itemScore := math.Round(10.0*hopDecay*visMult*changeMult*crossDomainMult*100) / 100

			riskDesc := fmt.Sprintf("Direct %s consumer in '%s'. High risk of immediate compilation or runtime failure.", edge.EdgeType, callerDomain)
			if nextHop > 1 {
				riskDesc = fmt.Sprintf("Transitive consumer (%d hops away). May receive invalid data or contract drift.", nextHop)
			}

			lineStart := 0
			if ls, ok := callerNode["line_start"].(int); ok {
				lineStart = ls
			} else if lsFloat, ok := callerNode["line_start"].(float64); ok {
				lineStart = int(lsFloat)
			}

			affectedItems = append(affectedItems, map[string]any{
				"node_id":          callerID,
				"symbol_name":      callerName,
				"file_path":        callerFile,
				"domain":           callerDomain,
				"level":            callerLevel,
				"edge_type":        edge.EdgeType,
				"hop_distance":     nextHop,
				"impact_score":     itemScore,
				"risk_description": riskDesc,
				"line":             lineStart,
			})
		}
	}

	// Sort by impact_score descending
	sort.Slice(affectedItems, func(i, j int) bool {
		sI, _ := affectedItems[i]["impact_score"].(float64)
		sJ, _ := affectedItems[j]["impact_score"].(float64)
		return sI > sJ
	})

	top10Limit := 10
	if len(affectedItems) < top10Limit {
		top10Limit = len(affectedItems)
	}
	top10 := affectedItems[:top10Limit]

	totalImpactRaw := 0.0
	for _, item := range affectedItems {
		if s, ok := item["impact_score"].(float64); ok {
			totalImpactRaw += s
		}
	}

	normalizedScore := int(totalImpactRaw * 1.5)
	if normalizedScore > 100 {
		normalizedScore = 100
	}

	riskTier := "LOW"
	recommendation := "Minimal side-effect risk. Symbol is either private, a leaf, or has very few dependents."
	if normalizedScore >= 75 {
		riskTier = "CRITICAL"
		recommendation = "Severe blast radius. High risk of breaking core cross-domain services. Create regression tests and update call sites before modifying."
	} else if normalizedScore >= 45 {
		riskTier = "HIGH"
		recommendation = "Substantial ripple effect across multiple modules. Review top 10 impacted locations carefully."
	} else if normalizedScore >= 20 {
		riskTier = "MODERATE"
		recommendation = "Localized impact within direct callers. Safe to proceed with normal caution."
	}

	affectedDomainsMap := make(map[string]bool)
	affectedFilesMap := make(map[string]bool)
	for _, it := range affectedItems {
		if dom, ok := it["domain"].(string); ok && dom != "" {
			affectedDomainsMap[dom] = true
		}
		if f, ok := it["file_path"].(string); ok && f != "" {
			affectedFilesMap[f] = true
		}
	}

	var affectedDomains []string
	for dom := range affectedDomainsMap {
		affectedDomains = append(affectedDomains, dom)
	}
	sort.Strings(affectedDomains)

	targetDisplayName, _ := targets[0]["name"].(string)
	if targetDisplayName == "" {
		targetDisplayName = symbolOrPath
	}

	blastTree := b.buildASCIITree(targetDisplayName, top10)

	result := map[string]any{
		"target":         symbolOrPath,
		"change_type":    changeType,
		"blast_score":    normalizedScore,
		"risk_tier":      riskTier,
		"recommendation": recommendation,
		"metrics": map[string]any{
			"total_affected_symbols": len(affectedItems),
			"total_affected_files":   len(affectedFilesMap),
			"affected_domains":       affectedDomains,
			"truncated":              isTruncated,
		},
		"top_10_breaking_points": top10,
		"blast_tree":             blastTree,
	}
	if isTruncated {
		result["truncation_note"] = fmt.Sprintf("BFS traversal capped at %d nodes to preserve latency and context window on large graph. Highest-impact direct callers are prioritized.", maxExploration)
	}

	return result
}

func (b *BlastRadiusEstimator) buildASCIITree(targetName string, topItems []map[string]any) string {
	lines := []string{fmt.Sprintf("[Target: %s]", targetName)}
	if len(topItems) == 0 {
		lines = append(lines, "  └── (No external dependents detected)")
		return strings.Join(lines, "\n")
	}

	for i, item := range topItems {
		prefix := "  ├── "
		if i == len(topItems)-1 {
			prefix = "  └── "
		}
		hopStr := fmt.Sprintf("Hop %v", item["hop_distance"])
		scoreStr := fmt.Sprintf("Score %v", item["impact_score"])
		lineStr := fmt.Sprintf("%s[%s] %v (%v:%v) - %s", prefix, hopStr, item["symbol_name"], item["file_path"], item["line"], scoreStr)
		lines = append(lines, lineStr)
	}

	return strings.Join(lines, "\n")
}
