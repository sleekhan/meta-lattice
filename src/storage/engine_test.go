package storage

import (
	"path/filepath"
	"testing"

	"meta-lattice/src/models"
)

func newTestStorage(t *testing.T) *GraphStorage {
	t.Helper()
	return NewGraphStorage(filepath.Join(t.TempDir(), "knowledge.lattice"))
}

func TestCreateEdgeKeepsGraphCleanWhenUnchanged(t *testing.T) {
	db := newTestStorage(t)
	db.CreateNode("domain:pkg", []string{"L0Domain"}, map[string]any{"level": string(models.LevelL0)})
	db.CreateNode("file:pkg/a.py", []string{"L1Module"}, map[string]any{"level": string(models.LevelL1)})
	db.CreateEdge("domain:pkg", "file:pkg/a.py", string(models.EdgeContains), nil)
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}
	if db.IsDirty() {
		t.Fatal("expected a clean graph after Save")
	}

	// Re-asserting an identical edge (the L0 -> L1 domain link on every sync)
	// must not mark the whole graph dirty.
	db.CreateEdge("domain:pkg", "file:pkg/a.py", string(models.EdgeContains), nil)
	if db.IsDirty() {
		t.Fatal("identical edge marked the graph dirty")
	}

	// A changed payload is a real change and must be kept.
	db.CreateEdge("domain:pkg", "file:pkg/a.py", string(models.EdgeContains), map[string]any{"weight": 1})
	if !db.IsDirty() {
		t.Fatal("changed edge properties did not mark the graph dirty")
	}
	edges := db.GetOutgoingEdges("domain:pkg", string(models.EdgeContains))
	if len(edges) != 1 {
		t.Fatalf("expected exactly one edge, got %d", len(edges))
	}
	if got, _ := edges[0].Properties["weight"].(int); got != 1 {
		t.Fatalf("expected updated edge properties, got %v", edges[0].Properties)
	}

	// Deleting the edge and adding it back must work with the index in sync.
	db.DeleteEdgesByType(string(models.EdgeContains))
	if got := len(db.GetOutgoingEdges("domain:pkg", string(models.EdgeContains))); got != 0 {
		t.Fatalf("expected the edge to be deleted, got %d", got)
	}
	db.CreateEdge("domain:pkg", "file:pkg/a.py", string(models.EdgeContains), nil)
	if got := len(db.GetOutgoingEdges("domain:pkg", string(models.EdgeContains))); got != 1 {
		t.Fatalf("expected the edge to be recreated, got %d", got)
	}
	if got := len(db.GetIncomingEdges("file:pkg/a.py", string(models.EdgeContains))); got != 1 {
		t.Fatalf("expected the reverse edge to be recreated, got %d", got)
	}
}

func TestDeleteNodeDropsEdgesFromIndex(t *testing.T) {
	db := newTestStorage(t)
	db.CreateNode("domain:pkg", []string{"L0Domain"}, nil)
	db.CreateNode("file:pkg/a.py", []string{"L1Module"}, nil)
	db.CreateEdge("domain:pkg", "file:pkg/a.py", string(models.EdgeContains), nil)
	db.CreateEdge("file:pkg/a.py", "symbol:pkg/a.py:fn", string(models.EdgeContains), nil)

	db.DeleteNode("file:pkg/a.py")
	db.CreateEdge("domain:pkg", "file:pkg/a.py", string(models.EdgeContains), nil)
	// The node is gone, but the edge must be created exactly once.
	if got := len(db.GetOutgoingEdges("domain:pkg", string(models.EdgeContains))); got != 1 {
		t.Fatalf("expected one edge after recreate, got %d", got)
	}
}

func TestCountNodesAndEdgesTracksMutations(t *testing.T) {
	db := newTestStorage(t)
	if got := db.CountNodesAndEdges()["total_nodes"]; got != 0 {
		t.Fatalf("expected an empty graph, got %d nodes", got)
	}

	db.CreateNode("domain:pkg", []string{"L0Domain"}, map[string]any{"level": string(models.LevelL0)})
	db.CreateNode("file:pkg/a.py", []string{"L1Module"}, map[string]any{"level": string(models.LevelL1)})
	db.CreateNode("symbol:pkg/a.py:fn", []string{"L3Symbol"}, map[string]any{"level": string(models.LevelL3)})
	db.CreateEdge("domain:pkg", "file:pkg/a.py", string(models.EdgeContains), nil)

	counts := db.CountNodesAndEdges()
	if counts["total_nodes"] != 3 || counts["total_edges"] != 1 {
		t.Fatalf("unexpected counts after inserts: %v", counts)
	}
	if counts["l0_domains"] != 1 || counts["l1_modules"] != 1 || counts["l3_symbols"] != 1 {
		t.Fatalf("unexpected per-level counts: %v", counts)
	}

	db.DeleteNode("symbol:pkg/a.py:fn")
	counts = db.CountNodesAndEdges()
	if counts["total_nodes"] != 2 || counts["l3_symbols"] != 0 {
		t.Fatalf("counts were not invalidated by DeleteNode: %v", counts)
	}

	// Callers must not be able to corrupt the memoized counters.
	counts["total_nodes"] = 99
	if got := db.CountNodesAndEdges()["total_nodes"]; got != 2 {
		t.Fatalf("CountNodesAndEdges returned an aliased map, got %d", got)
	}
}

func TestRangeNodesDoesNotCopyProperties(t *testing.T) {
	db := newTestStorage(t)
	db.CreateNode("file:pkg/a.py", []string{"L1Module"}, map[string]any{"level": string(models.LevelL1), "path": "pkg/a.py"})

	var seen int
	db.RangeNodes(func(id string, props map[string]any) {
		seen++
		if id != "file:pkg/a.py" {
			t.Fatalf("unexpected node %q", id)
		}
		if props["path"] != "pkg/a.py" {
			t.Fatalf("unexpected properties: %v", props)
		}
	})
	if seen != 1 {
		t.Fatalf("expected 1 node, got %d", seen)
	}
}

func TestFindSymbolsByNameAndSecondaryIndexes(t *testing.T) {
	db := newTestStorage(t)
	db.CreateNode("file:pkg/a.py", []string{"L1Module"}, map[string]any{
		"level": string(models.LevelL1), "path": "pkg/a.py", "domain": "pkg",
	})
	db.CreateNode("symbol:pkg/a.py:AuthService.login", []string{"L3Symbol"}, map[string]any{
		"level": string(models.LevelL3), "name": "AuthService.login", "file_path": "pkg/a.py",
	})
	db.CreateNode("symbol:pkg/b.py:login", []string{"L3Symbol"}, map[string]any{
		"level": string(models.LevelL3), "name": "login", "file_path": "pkg/b.py",
	})

	if got := db.FindNodesByLabelProperty("L1Module", "path", "pkg/a.py", 1); len(got) != 1 {
		t.Fatalf("expected 1 module by path index, got %d", len(got))
	}
	if got := db.FindSymbolsByName("login", ""); len(got) != 2 {
		t.Fatalf("expected 2 symbols matching short name 'login', got %d", len(got))
	}
	if got := db.FindSymbolsByName("login", "pkg/a.py"); len(got) != 1 {
		t.Fatalf("expected 1 symbol matching 'login' in pkg/a.py, got %d", len(got))
	}
	if got := db.FindSymbolsByName("login", `.\pkg\a.py`); len(got) != 1 {
		t.Fatalf("expected 1 symbol matching Windows path .\\pkg\\a.py, got %d", len(got))
	}
	if got := db.FindSymbolsByName("AuthService.login", ""); len(got) != 1 {
		t.Fatalf("expected 1 symbol matching qualified name 'AuthService.login', got %d", len(got))
	}

	db.DeleteNode("symbol:pkg/a.py:AuthService.login")
	if got := db.FindSymbolsByName("login", ""); len(got) != 1 {
		t.Fatalf("deleted symbol remained in secondary index, got %d", len(got))
	}
}
