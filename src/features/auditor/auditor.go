package auditor

import (
	"math"
	"sort"
	"strings"

	"meta-lattice/src/config"
	"meta-lattice/src/models"
	"meta-lattice/src/storage"
)

type ArchitectureBoundaryAuditor struct {
	db     *storage.GraphStorage
	config *config.LatticeConfig
}

func NewArchitectureBoundaryAuditor(db *storage.GraphStorage, cfg *config.LatticeConfig) *ArchitectureBoundaryAuditor {
	return &ArchitectureBoundaryAuditor{
		db:     db,
		config: cfg,
	}
}

type moduleEdge struct {
	source        string
	target        string
	importedNames []string
}

type moduleGraph struct {
	nodes    map[string]map[string]any
	outEdges map[string][]moduleEdge
	inEdges  map[string][]moduleEdge
}

func (a *ArchitectureBoundaryAuditor) getModuleGraph() *moduleGraph {
	mg := &moduleGraph{
		nodes:    make(map[string]map[string]any),
		outEdges: make(map[string][]moduleEdge),
		inEdges:  make(map[string][]moduleEdge),
	}

	for id, n := range a.db.AllNodes() {
		lvl, _ := n["level"].(string)
		if lvl == string(models.LevelL1) {
			filePath, _ := n["path"].(string)
			if filePath == "" {
				filePath = strings.TrimPrefix(id, "file:")
			}
			mg.nodes[filePath] = n
		}
	}

	for _, e := range a.db.AllEdges() {
		if e.EdgeType == string(models.EdgeImports) {
			srcNode, okSrc := a.db.GetNode(e.SourceID)
			tgtNode, okTgt := a.db.GetNode(e.TargetID)
			if okSrc && okTgt {
				srcPath, _ := srcNode["path"].(string)
				if srcPath == "" {
					srcPath = strings.TrimPrefix(e.SourceID, "file:")
				}
				tgtPath, _ := tgtNode["path"].(string)
				if tgtPath == "" {
					tgtPath = strings.TrimPrefix(e.TargetID, "file:")
				}

				var impNames []string
				if raw, ok := e.Properties["imported_names"].([]string); ok {
					impNames = raw
				} else if rawAny, ok := e.Properties["imported_names"].([]any); ok {
					for _, item := range rawAny {
						if str, ok := item.(string); ok {
							impNames = append(impNames, str)
						}
					}
				}

				medge := moduleEdge{
					source:        srcPath,
					target:        tgtPath,
					importedNames: impNames,
				}
				mg.outEdges[srcPath] = append(mg.outEdges[srcPath], medge)
				mg.inEdges[tgtPath] = append(mg.inEdges[tgtPath], medge)
			}
		}
	}

	return mg
}

func (a *ArchitectureBoundaryAuditor) detectLayer(filePath string, customLayer any) string {
	if cl, ok := customLayer.(string); ok && cl != "" {
		return cl
	}
	return "general"
}

// CheckLayerViolation audits architecture boundaries, circular dependencies, and coupling instability.
func (a *ArchitectureBoundaryAuditor) CheckLayerViolation(filePath string, customRules []config.ArchRule) map[string]any {
	mg := a.getModuleGraph()
	rules := customRules
	if len(rules) == 0 && a.config != nil && len(a.config.ArchRules.Rules) > 0 {
		rules = a.config.ArchRules.Rules
	}
	if len(rules) == 0 {
		rules = config.DefaultArchRules().Rules
	}

	var violations []map[string]any
	targetPath := strings.ReplaceAll(filePath, "\\", "/")

	// 1. Check Layer Rule Violations
	for src, edges := range mg.outEdges {
		if targetPath != "" && src != targetPath {
			continue
		}
		srcNode := mg.nodes[src]
		srcLayer := a.detectLayer(src, srcNode["layer"])

		for _, edge := range edges {
			tgt := edge.target
			tgtNode := mg.nodes[tgt]
			tgtLayer := a.detectLayer(tgt, tgtNode["layer"])

			if srcLayer == tgtLayer {
				continue
			}

			for _, rule := range rules {
				if srcLayer == rule.FromLayer {
					isForbidden := false
					for _, fb := range rule.ForbiddenTo {
						if tgtLayer == fb {
							isForbidden = true
							break
						}
					}
					if isForbidden {
						reason := rule.Reason
						if reason == "" {
							reason = "Forbidden dependency from " + srcLayer + " to " + tgtLayer
						}
						violations = append(violations, map[string]any{
							"type":           "LAYER_BOUNDARY_VIOLATION",
							"severity":       "ERROR",
							"source_file":    src,
							"source_layer":   srcLayer,
							"target_file":    tgt,
							"target_layer":   tgtLayer,
							"imported_names": edge.importedNames,
							"reason":         reason,
							"remediation":    "Refactor '" + src + "' so it does not directly import '" + tgt + "'. Invert the dependency using an interface/dependency injection.",
						})
					}
				}
			}
		}
	}

	// 2. Check Circular Dependencies using Tarjan SCC
	var circularDependencies []map[string]any
	allowCycles := false
	if a.config != nil && a.config.ArchRules.AllowCycles {
		allowCycles = true
	}

	if !allowCycles {
		sccs := tarjanSCC(mg)
		foundCount := 0

		for _, scc := range sccs {
			if len(scc) <= 1 {
				continue
			}
			cycles := findSimpleCycles(mg, scc, 10-foundCount)
			for _, cycle := range cycles {
				if targetPath != "" && !containsString(cycle, targetPath) {
					continue
				}
				cycleClosed := append(append([]string(nil), cycle...), cycle[0])
				circularDependencies = append(circularDependencies, map[string]any{
					"type":          "CIRCULAR_DEPENDENCY",
					"severity":      "CRITICAL",
					"cycle_length":  len(cycle),
					"cycle_chain":   cycleClosed,
					"display_chain": strings.Join(cycleClosed, " -> "),
					"remediation":   "Break cycle by extracting shared types to a separate leaf module or using interface abstraction.",
				})
				foundCount++
				if foundCount >= 10 {
					break
				}
			}
			if foundCount >= 10 {
				break
			}
		}
	}

	// 3. Compute Coupling & Instability Metrics
	couplingMetrics := make(map[string]any)
	var filesForMetrics []string
	if targetPath != "" && mg.nodes[targetPath] != nil {
		filesForMetrics = []string{targetPath}
	} else {
		for f := range mg.nodes {
			filesForMetrics = append(filesForMetrics, f)
		}
		sort.Strings(filesForMetrics)
	}

	type metricItem struct {
		file  string
		ca    int
		ce    int
		total int
		data  map[string]any
	}
	var allMetrics []metricItem

	for _, f := range filesForMetrics {
		ca := len(mg.inEdges[f])  // Afferent coupling (incoming)
		ce := len(mg.outEdges[f]) // Efferent coupling (outgoing)
		var instability float64
		if ca+ce > 0 {
			instability = math.Round((float64(ce)/float64(ca+ce))*1000) / 1000
		}

		stabilityAssessment := "Highly Stable (Core/Abstract)"
		if instability > 0.7 {
			stabilityAssessment = "Highly Instable (Flexible/Volatile)"
		} else if instability >= 0.3 {
			stabilityAssessment = "Balanced"
		}

		itemData := map[string]any{
			"afferent_coupling_Ca": ca,
			"efferent_coupling_Ce": ce,
			"instability_I":        instability,
			"stability_assessment": stabilityAssessment,
		}

		allMetrics = append(allMetrics, metricItem{
			file:  f,
			ca:    ca,
			ce:    ce,
			total: ca + ce,
			data:  itemData,
		})
	}

	if targetPath != "" {
		for _, m := range allMetrics {
			couplingMetrics[m.file] = m.data
		}
	} else {
		// Sort by total coupling descending, top 20
		sort.Slice(allMetrics, func(i, j int) bool {
			return allMetrics[i].total > allMetrics[j].total
		})
		limit := 20
		if len(allMetrics) < limit {
			limit = len(allMetrics)
		}
		for _, m := range allMetrics[:limit] {
			couplingMetrics[m.file] = m.data
		}
	}

	passed := len(violations) == 0 && len(circularDependencies) == 0

	auditedTarget := "Entire Repository"
	if filePath != "" {
		auditedTarget = filePath
	}

	status := "VIOLATIONS_DETECTED"
	if passed {
		status = "PASSED"
	}

	guidance := "Review the detected boundary violations and cycle chains above. Refactor the imports before proceeding with commit."
	if passed {
		guidance = "All architectural boundaries and dependency directions are respected."
	}

	return map[string]any{
		"audited_target": auditedTarget,
		"status":         status,
		"summary": map[string]any{
			"total_violations":            len(violations),
			"total_circular_dependencies": len(circularDependencies),
			"total_modules_audited":       len(filesForMetrics),
		},
		"layer_violations":      violations,
		"circular_dependencies": circularDependencies,
		"coupling_metrics":      couplingMetrics,
		"guidance_for_claude":   guidance,
	}
}

func containsString(list []string, item string) bool {
	for _, v := range list {
		if v == item {
			return true
		}
	}
	return false
}

// Tarjan's Strongly Connected Components algorithm
func tarjanSCC(mg *moduleGraph) [][]string {
	index := 0
	indices := make(map[string]int)
	lowlink := make(map[string]int)
	onStack := make(map[string]bool)
	var stack []string
	var sccs [][]string

	var strongconnect func(v string)
	strongconnect = func(v string) {
		indices[v] = index
		lowlink[v] = index
		index++
		stack = append(stack, v)
		onStack[v] = true

		for _, edge := range mg.outEdges[v] {
			w := edge.target
			if _, ok := mg.nodes[w]; !ok {
				continue
			}
			if _, visited := indices[w]; !visited {
				strongconnect(w)
				if lowlink[w] < lowlink[v] {
					lowlink[v] = lowlink[w]
				}
			} else if onStack[w] {
				if indices[w] < lowlink[v] {
					lowlink[v] = indices[w]
				}
			}
		}

		if lowlink[v] == indices[v] {
			var scc []string
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				scc = append(scc, w)
				if w == v {
					break
				}
			}
			sccs = append(sccs, scc)
		}
	}

	for node := range mg.nodes {
		if _, visited := indices[node]; !visited {
			strongconnect(node)
		}
	}

	return sccs
}

// findSimpleCycles enumerates cycles within an SCC using DFS
func findSimpleCycles(mg *moduleGraph, scc []string, maxCycles int) [][]string {
	sccSet := make(map[string]bool, len(scc))
	for _, n := range scc {
		sccSet[n] = true
	}

	var cycles [][]string
	visited := make(map[string]bool)
	var path []string
	pathIndex := make(map[string]int)

	var dfs func(curr string)
	dfs = func(curr string) {
		if len(cycles) >= maxCycles {
			return
		}
		pathIndex[curr] = len(path)
		path = append(path, curr)
		visited[curr] = true

		for _, edge := range mg.outEdges[curr] {
			next := edge.target
			if !sccSet[next] {
				continue
			}

			if idx, onPath := pathIndex[next]; onPath {
				// Cycle detected
				cycle := append([]string(nil), path[idx:]...)
				if len(cycle) >= 2 {
					cycles = append(cycles, cycle)
					if len(cycles) >= maxCycles {
						return
					}
				}
			} else if !visited[next] {
				dfs(next)
				if len(cycles) >= maxCycles {
					return
				}
			}
		}

		delete(pathIndex, curr)
		path = path[:len(path)-1]
	}

	for _, startNode := range scc {
		if !visited[startNode] {
			dfs(startNode)
			if len(cycles) >= maxCycles {
				break
			}
		}
	}

	return cycles
}
