package query

import (
	"path/filepath"
	"testing"

	"meta-lattice/src/models"
	"meta-lattice/src/storage"
)

func newTestStorage(t *testing.T) *storage.GraphStorage {
	t.Helper()
	return storage.NewGraphStorage(filepath.Join(t.TempDir(), "knowledge.lattice"))
}

func TestGraphQueryEngine(t *testing.T) {
	db := newTestStorage(t)

	// Node 1: High complexity, called by Node 2 (InDegree = 1)
	db.CreateNode("symbol:a.go:ComplexFunc", []string{"L3Symbol"}, map[string]any{
		"id":                    "symbol:a.go:ComplexFunc",
		"name":                  "ComplexFunc",
		"file_path":             "a.go",
		"level":                 string(models.LevelL3),
		"cyclomatic_complexity": 22,
		"is_public":             true,
	})

	// Node 2: Caller (calls ComplexFunc, called by nobody -> InDegree = 0, OutDegree = 1)
	db.CreateNode("symbol:b.go:CallerFunc", []string{"L3Symbol"}, map[string]any{
		"id":                    "symbol:b.go:CallerFunc",
		"name":                  "CallerFunc",
		"file_path":             "b.go",
		"level":                 string(models.LevelL3),
		"cyclomatic_complexity": 3,
		"is_public":             false,
	})

	// Node 3: Dead code (called by nobody, calls nobody -> InDegree = 0, OutDegree = 0)
	db.CreateNode("symbol:c.go:UnusedFunc", []string{"L3Symbol"}, map[string]any{
		"id":                    "symbol:c.go:UnusedFunc",
		"name":                  "UnusedFunc",
		"file_path":             "c.go",
		"level":                 string(models.LevelL3),
		"cyclomatic_complexity": 2,
		"is_public":             false,
	})

	db.CreateEdge("symbol:b.go:CallerFunc", "symbol:a.go:ComplexFunc", string(models.EdgeCalls), nil)

	engine := NewGraphQueryEngine(db)

	// 1. Filter by min complexity >= 20
	res1 := engine.QueryGraph(GraphQueryOptions{
		MinComplexity: 20,
	})
	if res1.TotalMatched != 1 || res1.Nodes[0].Name != "ComplexFunc" {
		t.Fatalf("expected ComplexFunc for complexity >= 20, got %+v", res1)
	}

	// 2. Filter by dead code (MaxInDegree = 0, MaxOutDegree = 0)
	zero := 0
	res2 := engine.QueryGraph(GraphQueryOptions{
		MaxInDegree:  &zero,
		MaxOutDegree: &zero,
	})
	if res2.TotalMatched != 1 || res2.Nodes[0].Name != "UnusedFunc" {
		t.Fatalf("expected UnusedFunc for dead code, got %+v", res2)
	}

	// 3. Filter by file pattern
	res3 := engine.QueryGraph(GraphQueryOptions{
		FilePattern: "a.go",
	})
	if res3.TotalMatched != 1 || res3.Nodes[0].Name != "ComplexFunc" {
		t.Fatalf("expected ComplexFunc in a.go, got %+v", res3)
	}
}
