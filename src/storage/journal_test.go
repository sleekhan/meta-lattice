package storage

import (
	"path/filepath"
	"strconv"
	"testing"

	"meta-lattice/src/models"
)

func newJournalStorage(t *testing.T) *GraphStorage {
	t.Helper()
	return NewGraphStorage(filepath.Join(t.TempDir(), "knowledge.lattice"))
}

func TestJournalKeepsFinalIntentPerKey(t *testing.T) {
	db := newJournalStorage(t)
	db.CreateNode("file:a.py", []string{"L1Module"}, map[string]any{"loc": 1})
	db.CreateNode("symbol:a.py:fn", []string{"L3Symbol"}, nil)
	db.CreateEdge("file:a.py", "symbol:a.py:fn", string(models.EdgeContains), nil)

	// A node that is created, deleted and re-created must end up as an upsert.
	db.CreateNode("file:b.py", []string{"L1Module"}, nil)
	db.DeleteNode("file:b.py")
	db.CreateNode("file:b.py", []string{"L1Module"}, map[string]any{"loc": 2})

	// An edge that is created and then removed must end up as a delete.
	db.CreateEdge("file:b.py", "file:a.py", string(models.EdgeImports), nil)
	db.DeleteEdgesForSource("file:b.py", string(models.EdgeImports))

	if db.journal == nil {
		t.Fatal("expected a journal after mutating the graph")
	}
	if got := db.journal.nodes["file:b.py"]; got.delete {
		t.Fatal("re-created node was journaled as a delete")
	}
	if got := db.journal.edges[edgeKey("file:b.py", "file:a.py", string(models.EdgeImports))]; !got.delete {
		t.Fatal("removed edge was not journaled as a delete")
	}
	if got := db.journal.edges[edgeKey("file:a.py", "symbol:a.py:fn", string(models.EdgeContains))]; got.delete {
		t.Fatal("existing edge was journaled as a delete")
	}
}

func TestJournalRecordsIncidentEdgesOnNodeDelete(t *testing.T) {
	db := newJournalStorage(t)
	db.CreateNode("domain:pkg", []string{"L0Domain"}, nil)
	db.CreateNode("file:a.py", []string{"L1Module"}, nil)
	db.CreateEdge("domain:pkg", "file:a.py", string(models.EdgeContains), nil)

	db.DeleteNode("file:a.py")

	if got := db.journal.nodes["file:a.py"]; !got.delete {
		t.Fatal("deleted node was not journaled")
	}
	// LatticeDB does not cascade, so the edge has to be removed explicitly.
	key := edgeKey("domain:pkg", "file:a.py", string(models.EdgeContains))
	if got := db.journal.edges[key]; !got.delete {
		t.Fatal("incident edge was not journaled for deletion")
	}
}

func TestJournalIgnoresNoOpMutations(t *testing.T) {
	db := newJournalStorage(t)
	db.CreateNode("file:a.py", []string{"L1Module"}, nil)
	db.CreateNode("file:b.py", []string{"L1Module"}, nil)
	db.CreateEdge("file:a.py", "file:b.py", string(models.EdgeImports), nil)
	db.journal = nil
	db.dirty = false

	// Nothing to remove: the graph must stay clean and the journal empty.
	db.DeleteNode("file:missing.py")
	db.DeleteEdgesForSource("file:missing.py")
	db.DeleteEdgesForTarget("file:missing.py")
	db.DeleteEdgesByType(string(models.EdgeCalls))

	if db.dirty {
		t.Fatal("no-op deletions marked the graph dirty")
	}
	if db.journal != nil && db.journal.ops() != 0 {
		t.Fatalf("no-op deletions journaled %d operations", db.journal.ops())
	}

	// Re-asserting an identical edge is not a change either, even when slice or
	// integer types differ after a database load ([]any vs []string, int64 vs int).
	db.CreateEdge("file:a.py", "file:b.py", string(models.EdgeImports), nil)
	if db.dirty || (db.journal != nil && db.journal.ops() != 0) {
		t.Fatal("identical edge was journaled")
	}

	db.CreateEdge("file:a.py", "file:b.py", string(models.EdgeImports), map[string]any{
		"imported_names": []any{"helper", "util"},
		"spec":           "./b",
		"weight":         int64(2),
	})
	db.journal = nil
	db.dirty = false

	db.CreateEdge("file:a.py", "file:b.py", string(models.EdgeImports), map[string]any{
		"imported_names": []string{"helper", "util"},
		"spec":           "./b",
		"weight":         2,
	})
	if db.dirty || (db.journal != nil && db.journal.ops() != 0) {
		t.Fatal("re-asserted edge with equivalent []string/int properties was journaled")
	}
}

func TestJournalOverflowFallsBackToFullRebuild(t *testing.T) {
	db := newJournalStorage(t)
	db.CreateNode("domain:pkg", []string{"L0Domain"}, nil)

	// A delta far larger than a quarter of the graph cannot be cheaper than a
	// rebuild, so the journal asks for one.
	for i := 0; i < 2500; i++ {
		id := "file:gen" + strconv.Itoa(i) + ".py"
		db.CreateNode(id, []string{"L1Module"}, map[string]any{"loc": i})
		db.CreateEdge("domain:pkg", id, string(models.EdgeContains), nil)
	}
	if db.journal == nil || !db.journal.forceRebuild {
		t.Fatal("a huge delta should request a full rebuild")
	}

	// A small delta keeps the incremental path.
	small := newJournalStorage(t)
	small.CreateNode("domain:pkg", []string{"L0Domain"}, nil)
	small.CreateNode("file:a.py", []string{"L1Module"}, nil)
	small.CreateEdge("domain:pkg", "file:a.py", string(models.EdgeContains), nil)
	if small.journal.forceRebuild {
		t.Fatal("a small delta must stay incremental")
	}
}

func TestRequestFullSaveForcesRebuild(t *testing.T) {
	db := newJournalStorage(t)
	db.CreateNode("file:a.py", []string{"L1Module"}, nil)
	db.RequestFullSave()
	if db.journal == nil || !db.journal.forceRebuild {
		t.Fatal("RequestFullSave did not request a rebuild")
	}
}
