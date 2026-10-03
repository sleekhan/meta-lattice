package zoomer

import (
	"fmt"
	"path"
	"strings"

	"meta-lattice/src/models"
	"meta-lattice/src/storage"
)

type HierarchicalZoomer struct {
	db *storage.GraphStorage
}

func NewHierarchicalZoomer(db *storage.GraphStorage) *HierarchicalZoomer {
	return &HierarchicalZoomer{db: db}
}

// ZoomOverview returns L0/L1 overview of domains and module catalogs without full code bodies.
func (z *HierarchicalZoomer) ZoomOverview(targetDomain string) map[string]any {
	l0Nodes := z.db.QueryNodes(string(models.LevelL0), "", 0)
	var domains []map[string]any

	totalModulesCount := 0
	type domData struct {
		node     map[string]any
		name     string
		id       string
		outgoing []models.GraphEdge
	}
	var domList []domData

	for _, d := range l0Nodes {
		domName, _ := d["name"].(string)
		if targetDomain != "" && domName != targetDomain {
			continue
		}

		domID, _ := d["id"].(string)
		outgoing := z.db.GetOutgoingEdges(domID, string(models.EdgeContains))
		totalModulesCount += len(outgoing)
		domList = append(domList, domData{
			node:     d,
			name:     domName,
			id:       domID,
			outgoing: outgoing,
		})
	}

	// Token Guard: If no target domain is specified and total modules > 50,
	// produce an architectural domain summary to protect LLM context windows.
	summaryMode := (targetDomain == "" && totalModulesCount > 50)

	for _, item := range domList {
		if summaryMode {
			// Compact summary per domain: top 5 key modules with basic stats
			sampleLimit := 5
			if len(item.outgoing) < sampleLimit {
				sampleLimit = len(item.outgoing)
			}
			var keyModules []map[string]any
			for _, e := range item.outgoing[:sampleLimit] {
				if mod, ok := z.db.GetNode(e.TargetID); ok {
					loc, _ := mod["loc"].(int)
					if loc == 0 {
						if locFloat, ok := mod["loc"].(float64); ok {
							loc = int(locFloat)
						}
					}
					keyModules = append(keyModules, map[string]any{
						"path":     mod["path"],
						"language": mod["language"],
						"loc":      loc,
					})
				}
			}

			domains = append(domains, map[string]any{
				"domain_id":      item.id,
				"name":           item.name,
				"description":    item.node["description"],
				"layer":          item.node["layer"],
				"total_modules":  len(item.outgoing),
				"sample_modules": keyModules,
			})
		} else {
			// Detailed module list
			var modulesSummary []map[string]any
			limit := 50
			if len(item.outgoing) < limit {
				limit = len(item.outgoing)
			}

			for _, e := range item.outgoing[:limit] {
				if mod, ok := z.db.GetNode(e.TargetID); ok {
					loc, _ := mod["loc"].(int)
					if loc == 0 {
						if locFloat, ok := mod["loc"].(float64); ok {
							loc = int(locFloat)
						}
					}
					docstring, _ := mod["docstring"].(string)
					docFirstLine := ""
					if docstring != "" {
						lines := strings.Split(docstring, "\n")
						docFirstLine = lines[0]
						if len(docFirstLine) > 120 {
							docFirstLine = docFirstLine[:120]
						}
					}

					exportsRaw := mod["exports"]
					var exports []string
					if exList, ok := exportsRaw.([]string); ok {
						exports = exList
					} else if exAny, ok := exportsRaw.([]any); ok {
						for _, itm := range exAny {
							if str, ok := itm.(string); ok {
								exports = append(exports, str)
							}
						}
					}
					if len(exports) > 8 {
						exports = exports[:8]
					}

					modulesSummary = append(modulesSummary, map[string]any{
						"id":        mod["id"],
						"path":      mod["path"],
						"language":  mod["language"],
						"loc":       loc,
						"exports":   exports,
						"docstring": docFirstLine,
					})
				}
			}

			domains = append(domains, map[string]any{
				"domain_id":     item.id,
				"name":          item.name,
				"description":   item.node["description"],
				"layer":         item.node["layer"],
				"total_modules": len(item.outgoing),
				"modules":       modulesSummary,
			})
		}
	}

	guidance := "Use `zoom_module(file_path)` to drill into L2 class/method interfaces, or `zoom_symbol(name)` to inspect L3 function bodies."
	if summaryMode {
		guidance = fmt.Sprintf("Large codebase detected (%d modules across %d domains). Domain-level summary shown to preserve LLM token context. Drill into a specific domain via `zoom_overview(domain=\"<name>\")` or inspect a module via `zoom_module(file_path).`", totalModulesCount, len(domains))
	}

	return map[string]any{
		"view_level":    "L0_Overview",
		"summary_mode":  summaryMode,
		"total_domains": len(domains),
		"total_modules": totalModulesCount,
		"stats":         z.db.CountNodesAndEdges(),
		"domains":       domains,
		"guidance":      guidance,
	}
}

// ZoomModule returns L1->L2 module view: classes, methods signatures (NO method bodies), and imports.
func (z *HierarchicalZoomer) ZoomModule(filePathOrID string) map[string]any {
	fileID := filePathOrID
	if !strings.HasPrefix(fileID, "file:") {
		fileID = "file:" + filePathOrID
	}

	fileNode, exists := z.db.GetNode(fileID)
	if !exists {
		// Fuzzy match by path
		nodes := z.db.FindNodesByLabelProperty("L1Module", "path", filePathOrID, 1)
		if len(nodes) > 0 {
			fileNode = nodes[0]
			fileID, _ = fileNode["id"].(string)
		} else {
			return map[string]any{
				"error":      "Module '" + filePathOrID + "' not found in LatticeDB.",
				"suggestion": "Run `sync_index` to index latest workspace files.",
			}
		}
	}

	outgoing := z.db.GetOutgoingEdges(fileID, string(models.EdgeContains))
	var classSummaries []map[string]any
	var functionSummaries []map[string]any

	for _, e := range outgoing {
		child, ok := z.db.GetNode(e.TargetID)
		if !ok {
			continue
		}
		lvl, _ := child["level"].(string)

		if lvl == string(models.LevelL2) {
			classSummaries = append(classSummaries, map[string]any{
				"id":                child["id"],
				"name":              child["name"],
				"kind":              child["kind"],
				"bases":             child["bases"],
				"lines":             formatLines(child["line_start"], child["line_end"]),
				"docstring":         child["docstring"],
				"is_public":         child["is_public"],
				"method_signatures": child["method_signatures"],
				"field_signatures":  child["field_signatures"],
			})
		} else if lvl == string(models.LevelL3) {
			doc, _ := child["docstring"].(string)
			docLine := ""
			if doc != "" {
				lines := strings.Split(doc, "\n")
				docLine = lines[0]
				if len(docLine) > 150 {
					docLine = docLine[:150]
				}
			}
			functionSummaries = append(functionSummaries, map[string]any{
				"id":                    child["id"],
				"name":                  child["name"],
				"kind":                  child["kind"],
				"signature":             child["signature"],
				"lines":                 formatLines(child["line_start"], child["line_end"]),
				"docstring":             docLine,
				"is_public":             child["is_public"],
				"cyclomatic_complexity": child["cyclomatic_complexity"],
			})
		}
	}

	// Dependencies and dependents
	var imports []string
	for _, e := range z.db.GetOutgoingEdges(fileID, string(models.EdgeImports)) {
		imports = append(imports, strings.TrimPrefix(e.TargetID, "file:"))
	}

	var dependents []string
	for _, e := range z.db.GetIncomingEdges(fileID, string(models.EdgeImports)) {
		dependents = append(dependents, strings.TrimPrefix(e.SourceID, "file:"))
	}

	return map[string]any{
		"view_level": "L1_Module_To_L2_Interfaces",
		"file": map[string]any{
			"id":           fileNode["id"],
			"path":         fileNode["path"],
			"language":     fileNode["language"],
			"loc":          fileNode["loc"],
			"layer":        fileNode["layer"],
			"docstring":    fileNode["docstring"],
			"exports":      fileNode["exports"],
			"dependencies": imports,
			"dependents":   dependents,
		},
		"classes":             classSummaries,
		"top_level_functions": functionSummaries,
		"token_savings_note":  "Method bodies omitted. Call `zoom_symbol(symbol_name)` only for functions requiring edits.",
	}
}

// ZoomSymbol returns L3 symbol view: full code implementation, incoming callers, outgoing calls, complexity.
func (z *HierarchicalZoomer) ZoomSymbol(symbolNameOrID string, filePath string) map[string]any {
	var targetNode map[string]any

	if strings.HasPrefix(symbolNameOrID, "symbol:") {
		if node, ok := z.db.GetNode(symbolNameOrID); ok {
			targetNode = node
		}
	}

	if targetNode == nil && filePath != "" {
		candID := "symbol:" + filePath + ":" + symbolNameOrID
		if node, ok := z.db.GetNode(candID); ok {
			targetNode = node
		}
	}

	if targetNode == nil {
		// Query by symbol name
		allNodes := z.db.AllNodes()
		for id, d := range allNodes {
			lvl, _ := d["level"].(string)
			if lvl == string(models.LevelL3) {
				name, _ := d["name"].(string)
				if name == symbolNameOrID || strings.HasSuffix(name, "."+symbolNameOrID) {
					fPath, _ := d["file_path"].(string)
					if filePath == "" || fPath == filePath {
						targetNode = d
						targetNode["id"] = id
						break
					}
				}
			}
		}
	}

	if targetNode == nil {
		return map[string]any{
			"error": "Symbol '" + symbolNameOrID + "' not found.",
			"hint":  "Try `zoom_search(symbol_name)` to locate its identifier.",
		}
	}

	symID, _ := targetNode["id"].(string)

	var outgoingCalls []string
	for _, e := range z.db.GetOutgoingEdges(symID, string(models.EdgeCalls)) {
		outgoingCalls = append(outgoingCalls, e.TargetID)
	}

	var incomingCallers []string
	for _, e := range z.db.GetIncomingEdges(symID, string(models.EdgeCalls)) {
		incomingCallers = append(incomingCallers, e.SourceID)
	}

	return map[string]any{
		"view_level": "L3_Symbol_Implementation",
		"symbol": map[string]any{
			"id":                      targetNode["id"],
			"name":                    targetNode["name"],
			"file_path":               targetNode["file_path"],
			"kind":                    targetNode["kind"],
			"lines":                   formatLines(targetNode["line_start"], targetNode["line_end"]),
			"signature":               targetNode["signature"],
			"docstring":               targetNode["docstring"],
			"is_public":               targetNode["is_public"],
			"cyclomatic_complexity":   targetNode["cyclomatic_complexity"],
			"calls":                   targetNode["calls"],
			"resolved_outgoing_calls": outgoingCalls,
			"incoming_callers":        incomingCallers,
			"code":                    targetNode["code"],
		},
	}
}

// ZoomSearch executes BM25 search across LatticeDB nodes.
func (z *HierarchicalZoomer) ZoomSearch(query string, level string, limit int) map[string]any {
	if limit <= 0 {
		limit = 15
	}
	rawResults := z.db.FTSSearch(query, limit*2)
	var filtered []map[string]any

	for _, r := range rawResults {
		lvl, _ := r["level"].(string)
		if level != "" && lvl != level {
			continue
		}

		name, _ := r["name"].(string)
		fpath, _ := r["file_path"].(string)
		if fpath == "" {
			fpath, _ = r["path"].(string)
		}
		if name == "" {
			name = path.Base(fpath)
		}

		sig, _ := r["signature"].(string)
		doc, _ := r["docstring"].(string)
		if len(doc) > 100 {
			doc = doc[:100] + "..."
		}

		filtered = append(filtered, map[string]any{
			"id":        r["id"],
			"level":     lvl,
			"name":      name,
			"file_path": fpath,
			"signature": sig,
			"docstring": doc,
			"score":     r["_rank"],
		})

		if len(filtered) >= limit {
			break
		}
	}

	return map[string]any{
		"query":         query,
		"level_filter":  level,
		"total_matches": len(filtered),
		"results":       filtered,
	}
}

func formatLines(start any, end any) string {
	s := toInt(start)
	e := toInt(end)
	return fmt.Sprintf("%d-%d", s, e)
}

func toInt(v any) int {
	switch val := v.(type) {
	case int:
		return val
	case float64:
		return int(val)
	}
	return 0
}
