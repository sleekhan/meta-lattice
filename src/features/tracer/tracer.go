package tracer

import (
	"sort"
	"strconv"
	"strings"

	"meta-lattice/src/models"
	"meta-lattice/src/storage"
)

type CallHop struct {
	FromID       string `json:"from_id"`
	FromName     string `json:"from_name"`
	FromFile     string `json:"from_file"`
	FromLines    string `json:"from_lines,omitempty"`
	ToID         string `json:"to_id"`
	ToName       string `json:"to_name"`
	ToFile       string `json:"to_file"`
	ToLines      string `json:"to_lines,omitempty"`
	EdgeType     string `json:"edge_type"`
	HopDepth     int    `json:"hop_depth"`
}

type CallPathResult struct {
	Source         string         `json:"source"`
	Target         string         `json:"target,omitempty"`
	Direction      string         `json:"direction"`
	MaxDepth       int            `json:"max_depth"`
	TotalHops      int            `json:"total_hops"`
	PathsFound     int            `json:"paths_found"`
	Paths          [][]CallHop    `json:"paths,omitempty"`
	CallTree       []map[string]any `json:"call_tree,omitempty"`
	UniqueSymbols  int            `json:"unique_symbols"`
	Truncated      bool           `json:"truncated,omitempty"`
	Error          string         `json:"error,omitempty"`
}

type CallTracer struct {
	db *storage.GraphStorage
}

func NewCallTracer(db *storage.GraphStorage) *CallTracer {
	return &CallTracer{db: db}
}

func (t *CallTracer) resolveSymbol(symbolOrPath string) []map[string]any {
	// 1. Exact node ID
	if node, ok := t.db.GetNode(symbolOrPath); ok {
		return []map[string]any{node}
	}

	// 2. File path lookup -> all L3 symbols within file (including methods nested under L2 classes)
	cleanPath := strings.TrimPrefix(strings.ReplaceAll(symbolOrPath, "\\", "/"), "./")
	fileID := "file:" + cleanPath
	if _, ok := t.db.GetNode(fileID); ok {
		outgoing := t.db.GetOutgoingEdges(fileID, string(models.EdgeContains))
		var l3Children []map[string]any
		for _, e := range outgoing {
			if child, exists := t.db.GetNode(e.TargetID); exists {
				lvl, _ := child["level"].(string)
				if lvl == string(models.LevelL3) {
					l3Children = append(l3Children, child)
				} else if lvl == string(models.LevelL2) {
					for _, subE := range t.db.GetOutgoingEdges(e.TargetID, string(models.EdgeContains)) {
						if subChild, subOk := t.db.GetNode(subE.TargetID); subOk {
							if subLvl, _ := subChild["level"].(string); subLvl == string(models.LevelL3) {
								l3Children = append(l3Children, subChild)
							}
						}
					}
				}
			}
		}
		if len(l3Children) > 0 {
			return l3Children
		}
	}

	// 3. Symbol name match
	if syms := t.db.FindSymbolsByName(symbolOrPath, ""); len(syms) > 0 {
		return syms
	}

	// 4. Route path or endpoint match (e.g. "/api/v1/users" or "GET /api/v1/users")
	var matchedRoutes []map[string]any
	for _, rNode := range t.db.QueryNodes("Route", "", 0) {
		rPath, _ := rNode["path"].(string)
		rName, _ := rNode["name"].(string)
		if strings.EqualFold(rPath, symbolOrPath) || strings.EqualFold(rName, symbolOrPath) {
			matchedRoutes = append(matchedRoutes, rNode)
		}
	}
	return matchedRoutes
}

func (t *CallTracer) getNodeSummary(nodeID string) (name, file, lines, signature string) {
	node, ok := t.db.GetNode(nodeID)
	if !ok {
		return nodeID, "", "", ""
	}
	name, _ = node["name"].(string)
	if name == "" {
		name = nodeID
	}
	file, _ = node["file_path"].(string)
	if file == "" {
		file, _ = node["path"].(string)
	}
	if file != "" {
		file = strings.ReplaceAll(file, "\\", "/")
	}
	signature, _ = node["signature"].(string)
	start := node["line_start"]
	end := node["line_end"]
	if start != nil && end != nil {
		lines = formatLines(start, end)
	}
	return
}

func formatLines(start, end any) string {
	s := toInt(start)
	e := toInt(end)
	if s > 0 && e > 0 {
		return strconv.Itoa(s) + "-" + strconv.Itoa(e)
	}
	return ""
}

func toInt(v any) int {
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	}
	return 0
}

// TraceCallPath performs multi-hop call chain tracing between symbols or explores call trees.
func (t *CallTracer) TraceCallPath(source string, target string, direction string, maxDepth int) CallPathResult {
	defer t.db.BeginReadSession()()

	if maxDepth <= 0 {
		maxDepth = 3
	}
	if maxDepth > 10 {
		maxDepth = 10
	}
	dir := strings.ToLower(direction)
	if dir == "" {
		dir = "outbound"
	}
	if dir != "inbound" && dir != "outbound" && dir != "both" {
		dir = "outbound"
	}

	res := CallPathResult{
		Source:    source,
		Target:    target,
		Direction: dir,
		MaxDepth:  maxDepth,
	}

	sources := t.resolveSymbol(source)
	if len(sources) == 0 {
		res.Error = "Source symbol '" + source + "' not found in LatticeDB."
		return res
	}

	// Case 1: Point-to-Point Path Finding (source -> target)
	if target != "" {
		targets := t.resolveSymbol(target)
		if len(targets) == 0 {
			res.Error = "Target symbol '" + target + "' not found in LatticeDB."
			return res
		}
		targetSet := make(map[string]bool)
		for _, tgt := range targets {
			if tid, _ := tgt["id"].(string); tid != "" {
				targetSet[tid] = true
			}
		}

		paths := t.findPathsBetween(sources, targetSet, dir, maxDepth)
		res.Paths = paths
		res.PathsFound = len(paths)
		totalHops := 0
		uniqueSyms := make(map[string]bool)
		for _, p := range paths {
			totalHops += len(p)
			for _, hop := range p {
				uniqueSyms[hop.FromID] = true
				uniqueSyms[hop.ToID] = true
			}
		}
		res.TotalHops = totalHops
		res.UniqueSymbols = len(uniqueSyms)
		return res
	}

	// Case 2: Tree Exploration (Inbound, Outbound, or Both)
	tree, uniqueSyms, truncated := t.buildCallTree(sources, dir, maxDepth)
	res.CallTree = tree
	res.TotalHops = len(tree)
	res.UniqueSymbols = len(uniqueSyms)
	res.Truncated = truncated
	return res
}

type queuePathItem struct {
	currID string
	path   []CallHop
}

func (t *CallTracer) findPathsBetween(sources []map[string]any, targetSet map[string]bool, direction string, maxDepth int) [][]CallHop {
	var foundPaths [][]CallHop
	var queue []queuePathItem

	for _, s := range sources {
		sID, _ := s["id"].(string)
		if sID == "" {
			continue
		}
		if targetSet[sID] {
			continue
		}
		queue = append(queue, queuePathItem{
			currID: sID,
			path:   nil,
		})
	}

	maxPaths := 20
	visitedInPath := func(p []CallHop, nodeID string) bool {
		for _, hop := range p {
			if hop.FromID == nodeID || hop.ToID == nodeID {
				return true
			}
		}
		return false
	}

	for len(queue) > 0 && len(foundPaths) < maxPaths {
		item := queue[0]
		queue = queue[1:]

		currentDepth := len(item.path)
		if currentDepth >= maxDepth {
			continue
		}

		nextEdges := t.getEdgesForDirection(item.currID, direction)
		for _, edge := range nextEdges {
			nextID := edge.targetID
			if visitedInPath(item.path, nextID) {
				continue // cycle guard
			}

			fromName, fromFile, fromLines, _ := t.getNodeSummary(item.currID)
			toName, toFile, toLines, _ := t.getNodeSummary(nextID)

			newHop := CallHop{
				FromID:    item.currID,
				FromName:  fromName,
				FromFile:  fromFile,
				FromLines: fromLines,
				ToID:      nextID,
				ToName:    toName,
				ToFile:    toFile,
				ToLines:   toLines,
				EdgeType:  edge.edgeType,
				HopDepth:  currentDepth + 1,
			}

			newPath := append(append([]CallHop(nil), item.path...), newHop)

			if targetSet[nextID] {
				foundPaths = append(foundPaths, newPath)
				if len(foundPaths) >= maxPaths {
					break
				}
			} else {
				queue = append(queue, queuePathItem{
					currID: nextID,
					path:   newPath,
				})
			}
		}
	}

	return foundPaths
}

type directedEdge struct {
	targetID string
	edgeType string
}

func (t *CallTracer) getEdgesForDirection(nodeID string, direction string) []directedEdge {
	var res []directedEdge
	if direction == "outbound" || direction == "both" {
		for _, e := range t.db.GetOutgoingEdges(nodeID, string(models.EdgeCalls)) {
			res = append(res, directedEdge{targetID: e.TargetID, edgeType: "CALLS"})
		}
		for _, e := range t.db.GetOutgoingEdges(nodeID, "HANDLED_BY") {
			res = append(res, directedEdge{targetID: e.TargetID, edgeType: "HANDLED_BY"})
		}
	}
	if direction == "inbound" || direction == "both" {
		for _, e := range t.db.GetIncomingEdges(nodeID, string(models.EdgeCalls)) {
			res = append(res, directedEdge{targetID: e.SourceID, edgeType: "CALLED_BY"})
		}
		for _, e := range t.db.GetIncomingEdges(nodeID, "HANDLED_BY") {
			res = append(res, directedEdge{targetID: e.SourceID, edgeType: "EXPOSES_ROUTE"})
		}
	}
	return res
}

func (t *CallTracer) buildCallTree(sources []map[string]any, direction string, maxDepth int) ([]map[string]any, map[string]bool, bool) {
	var flatTree []map[string]any
	uniqueSyms := make(map[string]bool)
	visitedEdges := make(map[string]bool)

	type treeQueueItem struct {
		nodeID string
		depth  int
	}

	var queue []treeQueueItem
	for _, s := range sources {
		sID, _ := s["id"].(string)
		if sID != "" {
			queue = append(queue, treeQueueItem{nodeID: sID, depth: 0})
			uniqueSyms[sID] = true
		}
	}

	maxNodes := 150
	truncated := false

	for len(queue) > 0 {
		if len(flatTree) >= maxNodes {
			truncated = true
			break
		}

		item := queue[0]
		queue = queue[1:]

		if item.depth >= maxDepth {
			continue
		}

		edges := t.getEdgesForDirection(item.nodeID, direction)
		for _, edge := range edges {
			edgeKey := item.nodeID + "->" + edge.targetID + ":" + edge.edgeType
			if visitedEdges[edgeKey] {
				continue
			}
			visitedEdges[edgeKey] = true

			fromName, fromFile, fromLines, fromSig := t.getNodeSummary(item.nodeID)
			toName, toFile, toLines, toSig := t.getNodeSummary(edge.targetID)

			uniqueSyms[edge.targetID] = true

			flatTree = append(flatTree, map[string]any{
				"depth":          item.depth + 1,
				"relationship":   edge.edgeType,
				"from_id":        item.nodeID,
				"from_name":      fromName,
				"from_file":      fromFile,
				"from_lines":     fromLines,
				"from_signature": fromSig,
				"to_id":          edge.targetID,
				"to_name":        toName,
				"to_file":        toFile,
				"to_lines":       toLines,
				"to_signature":   toSig,
			})

			queue = append(queue, treeQueueItem{
				nodeID: edge.targetID,
				depth:  item.depth + 1,
			})

			if len(flatTree) >= maxNodes {
				truncated = true
				break
			}
		}
	}

	sort.Slice(flatTree, func(i, j int) bool {
		dI := flatTree[i]["depth"].(int)
		dJ := flatTree[j]["depth"].(int)
		if dI != dJ {
			return dI < dJ
		}
		return flatTree[i]["to_name"].(string) < flatTree[j]["to_name"].(string)
	})

	return flatTree, uniqueSyms, truncated
}
