package mcp

import (
	"encoding/json"
	"testing"

	"meta-lattice/src/models"
)

func TestMCPServerTools(t *testing.T) {
	tempDir := t.TempDir()
	server := NewMCPServer(tempDir)

	// Pre-populate some nodes
	server.db.CreateNode("symbol:a.go:Init", []string{"L3Symbol"}, map[string]any{
		"id":                    "symbol:a.go:Init",
		"name":                  "Init",
		"file_path":             "a.go",
		"level":                 string(models.LevelL3),
		"cyclomatic_complexity": 5,
		"is_public":             true,
	})
	server.db.CreateNode("symbol:b.go:Process", []string{"L3Symbol"}, map[string]any{
		"id":                    "symbol:b.go:Process",
		"name":                  "Process",
		"file_path":             "b.go",
		"level":                 string(models.LevelL3),
		"cyclomatic_complexity": 15,
		"is_public":             true,
	})
	server.db.CreateEdge("symbol:a.go:Init", "symbol:b.go:Process", string(models.EdgeCalls), nil)

	// 1. Verify listTools includes all 14 tools
	tools := server.listTools()
	toolMap := make(map[string]bool)
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		toolMap[name] = true
	}

	expectedTools := []string{
		"zoom_overview",
		"zoom_module",
		"zoom_symbol",
		"zoom_search",
		"check_layer_violation",
		"estimate_blast_radius",
		"sync_index",
		"get_index_status",
		"scaffold_module",
		"apply_plan",
		"trace_call_path",
		"detect_changes",
		"query_graph",
		"manage_adr",
		"list_routes",
		"detect_communities",
	}

	for _, exp := range expectedTools {
		if !toolMap[exp] {
			t.Errorf("expected tool %s to be listed", exp)
		}
	}

	// 2. Test trace_call_path via MCP
	traceResText, err := server.callTool("trace_call_path", map[string]any{
		"source": "Init",
	})
	if err != nil {
		t.Fatalf("trace_call_path failed: %v", err)
	}
	var traceObj map[string]any
	if err := json.Unmarshal([]byte(traceResText), &traceObj); err != nil {
		t.Fatalf("json unmarshal failed: %v", err)
	}
	if hops, ok := traceObj["total_hops"].(float64); !ok || hops != 1 {
		t.Errorf("expected 1 hop, got %v", traceObj["total_hops"])
	}

	// 3. Test query_graph via MCP
	queryResText, err := server.callTool("query_graph", map[string]any{
		"min_complexity": float64(10),
	})
	if err != nil {
		t.Fatalf("query_graph failed: %v", err)
	}
	var queryObj map[string]any
	if err := json.Unmarshal([]byte(queryResText), &queryObj); err != nil {
		t.Fatalf("json unmarshal failed: %v", err)
	}
	if matched, ok := queryObj["total_matched"].(float64); !ok || matched != 1 {
		t.Errorf("expected 1 matched node with complexity >= 10, got %v", queryObj["total_matched"])
	}

	// 4. Test manage_adr via MCP
	adrCreateText, err := server.callTool("manage_adr", map[string]any{
		"action":   "create",
		"title":    "ADR Test via MCP",
		"context":  "Testing MCP ADR tool",
		"decision": "Use JSON-RPC",
	})
	if err != nil {
		t.Fatalf("manage_adr create failed: %v", err)
	}
	var adrObj map[string]any
	if err := json.Unmarshal([]byte(adrCreateText), &adrObj); err != nil {
		t.Fatalf("json unmarshal failed: %v", err)
	}
	if adrObj["id"] != "ADR-0001" {
		t.Errorf("expected ADR-0001, got %v", adrObj["id"])
	}

	// 5. Test detect_communities via MCP
	commText, err := server.callTool("detect_communities", map[string]any{
		"min_size": float64(1),
	})
	if err != nil {
		t.Fatalf("detect_communities failed: %v", err)
	}
	var commObj map[string]any
	if err := json.Unmarshal([]byte(commText), &commObj); err != nil {
		t.Fatalf("json unmarshal failed: %v", err)
	}
	if _, ok := commObj["architecture_health"]; !ok {
		t.Errorf("expected architecture_health in result: %+v", commObj)
	}

	// 6. Test list_routes via MCP
	routesText, err := server.callTool("list_routes", map[string]any{
		"path_pattern": "users",
	})
	if err != nil {
		t.Fatalf("list_routes failed: %v", err)
	}
	var routesList []any
	if err := json.Unmarshal([]byte(routesText), &routesList); err != nil {
		t.Fatalf("json unmarshal failed: %v", err)
	}
}
