//go:build !nolattice && cgo

package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	latticedb "github.com/jeffhajewski/latticedb/bindings/go"
	"meta-lattice/src/models"
)

func newNativeStorage(t *testing.T, path string) *GraphStorage {
	t.Helper()
	return NewGraphStorage(path)
}

// seedGraph writes the baseline graph used by the persistence tests.
func seedGraph(t *testing.T, db *GraphStorage) {
	t.Helper()
	db.CreateNode("domain:pkg", []string{"L0Domain", "Domain"}, map[string]any{
		"level": string(models.LevelL0), "name": "pkg", "module_count": 1,
	})
	db.CreateNode("file:pkg/a.py", []string{"L1Module", "Module", "File"}, map[string]any{
		"level": string(models.LevelL1), "path": "pkg/a.py", "loc": 10, "exports": []string{"fn"},
	})
	db.CreateNode("symbol:pkg/a.py:fn", []string{"L3Symbol", "Symbol", "Function"}, map[string]any{
		"level": string(models.LevelL3), "name": "fn", "code": "def fn(): pass\n",
	})
	db.CreateEdge("domain:pkg", "file:pkg/a.py", string(models.EdgeContains), nil)
	db.CreateEdge("file:pkg/a.py", "symbol:pkg/a.py:fn", string(models.EdgeContains), nil)
}

func TestIncrementalSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge.lattice")

	db := newNativeStorage(t, path)
	seedGraph(t, db)
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}
	if mode := db.LastSaveMode(); mode != "full" {
		t.Fatalf("first save should create the file, got mode %q", mode)
	}

	// One-file edit: change a property, add a file, drop a symbol (and with it
	// the CONTAINS edge from the module).
	db.CreateNode("file:pkg/a.py", []string{"L1Module", "Module", "File"}, map[string]any{
		"level": string(models.LevelL1), "path": "pkg/a.py", "loc": 42, "exports": []string{"fn2"},
	})
	db.CreateNode("file:pkg/b.py", []string{"L1Module", "Module", "File"}, map[string]any{
		"level": string(models.LevelL1), "path": "pkg/b.py", "loc": 3,
	})
	db.CreateEdge("domain:pkg", "file:pkg/b.py", string(models.EdgeContains), nil)
	db.DeleteNode("symbol:pkg/a.py:fn")

	if err := db.Save(); err != nil {
		t.Fatal(err)
	}
	if mode := db.LastSaveMode(); mode != "incremental" {
		t.Fatalf("delta save should be incremental, got mode %q", mode)
	}

	reopened := newNativeStorage(t, path)
	if err := reopened.Load(); err != nil {
		t.Fatal(err)
	}
	counts := reopened.CountNodesAndEdges()
	if counts["total_nodes"] != 3 || counts["total_edges"] != 2 {
		t.Fatalf("unexpected graph after reopen: %v", counts)
	}

	node, ok := reopened.GetNode("file:pkg/a.py")
	if !ok {
		t.Fatal("file:pkg/a.py is missing after reopen")
	}
	if loc, _ := node["loc"].(int); loc != 42 {
		t.Fatalf("property update was not persisted, loc=%v", node["loc"])
	}
	if _, ok := reopened.GetNode("symbol:pkg/a.py:fn"); ok {
		t.Fatal("deleted node came back after reopen")
	}
	// The symbol's CONTAINS edge must be gone as well: LatticeDB does not
	// cascade deletes, so the journal has to remove it explicitly.
	if edges := reopened.GetOutgoingEdges("file:pkg/a.py", string(models.EdgeContains)); len(edges) != 0 {
		t.Fatalf("deleted node left %d dangling edges", len(edges))
	}
	if edges := reopened.GetOutgoingEdges("domain:pkg", string(models.EdgeContains)); len(edges) != 2 {
		t.Fatalf("expected two domain edges, got %d", len(edges))
	}
}

func TestIncrementalSaveIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge.lattice")

	db := newNativeStorage(t, path)
	seedGraph(t, db)
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}

	db.CreateNode("file:pkg/a.py", []string{"L1Module", "Module", "File"}, map[string]any{
		"level": string(models.LevelL1), "path": "pkg/a.py", "loc": 7,
	})
	db.CreateEdge("domain:pkg", "file:pkg/a.py", string(models.EdgeContains), nil)

	journal := db.journal
	nodes := db.nodes
	if journal == nil || journal.ops() == 0 {
		t.Fatal("expected a non-empty journal")
	}

	// Replaying the same journal twice must not duplicate nodes or edges
	// (LatticeDB happily creates duplicate edges otherwise).
	for i := 0; i < 2; i++ {
		if err := applyJournalToLatticeDB(path, journal, nodes); err != nil {
			t.Fatalf("replay %d failed: %v", i+1, err)
		}
	}

	reopened := newNativeStorage(t, path)
	if err := reopened.Load(); err != nil {
		t.Fatal(err)
	}
	counts := reopened.CountNodesAndEdges()
	if counts["total_nodes"] != 3 || counts["total_edges"] != 2 {
		t.Fatalf("replay duplicated graph entries: %v", counts)
	}
}

func TestSaveWithoutChangesDoesNotTouchFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge.lattice")
	db := newNativeStorage(t, path)
	seedGraph(t, db)
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := db.Save(); err != nil {
		t.Fatal(err)
	}
	if mode := db.LastSaveMode(); mode != "none" {
		t.Fatalf("clean save should be a no-op, got mode %q", mode)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("a clean save rewrote the database file")
	}
}

func TestRequestFullSaveRebuildsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge.lattice")
	db := newNativeStorage(t, path)
	seedGraph(t, db)
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}

	db.CreateNode("file:pkg/b.py", []string{"L1Module", "Module", "File"}, map[string]any{
		"level": string(models.LevelL1), "path": "pkg/b.py",
	})
	db.RequestFullSave()
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}
	if mode := db.LastSaveMode(); mode != "full" {
		t.Fatalf("RequestFullSave should rebuild, got mode %q", mode)
	}

	reopened := newNativeStorage(t, path)
	if err := reopened.Load(); err != nil {
		t.Fatal(err)
	}
	if counts := reopened.CountNodesAndEdges(); counts["total_nodes"] != 4 {
		t.Fatalf("rebuild lost nodes: %v", counts)
	}
}

// legacyLatticeFile creates a database the way older versions wrote it: nodes
// with an "id" property but only the useless ("Node", "id") index.
func legacyLatticeFile(t *testing.T, path string) {
	t.Helper()
	db, err := latticedb.Open(path, latticedb.OpenOptions{Create: true, CacheSizeMB: 64})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *latticedb.Tx) error {
		if _, err := tx.CreateNode(latticedb.CreateNodeOptions{
			Labels:     []string{"L1Module", "Module", "File"},
			Properties: map[string]any{"id": "file:pkg/a.py", "path": "pkg/a.py", "loc": int64(1), "level": "L1"},
		}); err != nil {
			return err
		}
		_, err := tx.CreateNode(latticedb.CreateNodeOptions{
			Labels:     []string{"L0Domain", "Domain"},
			Properties: map[string]any{"id": "domain:pkg", "name": "pkg", "module_count": int64(1), "level": "L0"},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateNodePropertyIndex("Node", "id"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestIncrementalSaveMigratesLegacyIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge.lattice")
	legacyLatticeFile(t, path)

	db := newNativeStorage(t, path)
	if err := db.Load(); err != nil {
		t.Fatal(err)
	}
	if counts := db.CountNodesAndEdges(); counts["total_nodes"] != 2 {
		t.Fatalf("legacy file was not loaded: %v", counts)
	}

	db.CreateNode("file:pkg/a.py", []string{"L1Module", "Module", "File"}, map[string]any{
		"level": string(models.LevelL1), "path": "pkg/a.py", "loc": 99,
	})
	db.CreateEdge("domain:pkg", "file:pkg/a.py", string(models.EdgeContains), nil)
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}
	if mode := db.LastSaveMode(); mode != "incremental" {
		t.Fatalf("legacy file should be updated incrementally, got mode %q", mode)
	}

	reopened := newNativeStorage(t, path)
	if err := reopened.Load(); err != nil {
		t.Fatal(err)
	}
	node, ok := reopened.GetNode("file:pkg/a.py")
	if !ok {
		t.Fatal("node missing after migration")
	}
	if loc, _ := node["loc"].(int); loc != 99 {
		t.Fatalf("update was not persisted, loc=%v", node["loc"])
	}
	if edges := reopened.GetOutgoingEdges("domain:pkg", string(models.EdgeContains)); len(edges) != 1 {
		t.Fatalf("expected one edge after migration, got %d", len(edges))
	}
}

func TestSaveKeepsChangesWhenDatabaseIsLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge.lattice")
	db := newNativeStorage(t, path)
	seedGraph(t, db)
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}

	holder, err := latticedb.Open(path, latticedb.OpenOptions{Create: false, CacheSizeMB: 64})
	if err != nil {
		t.Fatal(err)
	}

	db.CreateNode("file:pkg/b.py", []string{"L1Module", "Module", "File"}, map[string]any{
		"level": string(models.LevelL1), "path": "pkg/b.py",
	})
	err = db.Save()
	if err == nil {
		t.Fatal("expected a lock error while another writer holds the file")
	}
	if !errors.Is(err, errDatabaseLocked) {
		t.Fatalf("expected errDatabaseLocked, got %v", err)
	}
	if !db.IsDirty() {
		t.Fatal("a failed save must keep the graph dirty")
	}
	if db.journal == nil || db.journal.ops() == 0 {
		t.Fatal("a failed save must keep the journal for the retry")
	}

	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Save(); err != nil {
		t.Fatalf("retry after the lock was released failed: %v", err)
	}
	if db.IsDirty() {
		t.Fatal("graph should be clean after a successful retry")
	}

	reopened := newNativeStorage(t, path)
	if err := reopened.Load(); err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.GetNode("file:pkg/b.py"); !ok {
		t.Fatal("the pending change was lost")
	}
}

func TestLazyNativeQueriesAndMutations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge.lattice")
	db := newNativeStorage(t, path)
	db.CreateNode("domain:pkg", []string{"L0Domain", "Domain"}, map[string]any{
		"level": string(models.LevelL0), "name": "pkg", "module_count": 1,
	})
	db.CreateNode("file:pkg/a.py", []string{"L1Module", "Module", "File"}, map[string]any{
		"level": string(models.LevelL1), "path": "pkg/a.py", "domain": "pkg", "loc": 10,
	})
	db.CreateNode("symbol:pkg/a.py:AuthService.parseHTTP", []string{"L3Symbol", "Symbol", "Method"}, map[string]any{
		"level": string(models.LevelL3), "name": "AuthService.parseHTTP", "file_path": "pkg/a.py", "code": "def parseHTTP(): pass\n",
	})
	db.CreateEdge("domain:pkg", "file:pkg/a.py", string(models.EdgeContains), nil)
	db.CreateEdge("file:pkg/a.py", "symbol:pkg/a.py:AuthService.parseHTTP", string(models.EdgeContains), nil)
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}

	reopened := newNativeStorage(t, path)
	if !reopened.lazyNative {
		t.Fatal("expected reopened v2 database to start in lazyNative mode")
	}
	if len(reopened.nodes) != 0 {
		t.Fatalf("expected 0 hydrated nodes on lazy open, got %d", len(reopened.nodes))
	}

	// Point lookup, B-Tree symbol lookup, FTS search, and edge traversal must
	// all answer without hydrating the full graph into reopened.nodes.
	if !reopened.HasNode("file:pkg/a.py") {
		t.Fatal("HasNode failed in lazyNative mode")
	}
	syms := reopened.FindSymbolsByName("parseHTTP", "pkg/a.py")
	if len(syms) != 1 {
		t.Fatalf("FindSymbolsByName short match = %d, want 1", len(syms))
	}
	if len(reopened.FTSSearch("http", 5)) == 0 {
		t.Fatal("native FTS search failed to match camelCase token 'http'")
	}
	if len(reopened.GetOutgoingEdges("file:pkg/a.py", string(models.EdgeContains))) != 1 {
		t.Fatal("GetOutgoingEdges failed in lazyNative mode")
	}
	counts := reopened.CountNodesAndEdges()
	if counts["total_nodes"] != 3 || counts["total_edges"] != 2 {
		t.Fatalf("CountNodesAndEdges in lazyNative mode = %v", counts)
	}
	if len(reopened.nodes) != 0 {
		t.Fatalf("read queries in lazyNative mode hydrated %d nodes into s.nodes", len(reopened.nodes))
	}

	// Incremental mutation on a lazyNative instance must stay lazy and update
	// disk counts without hydrating the whole graph.
	reopened.CreateNode("file:pkg/b.py", []string{"L1Module", "Module", "File"}, map[string]any{
		"level": string(models.LevelL1), "path": "pkg/b.py", "domain": "pkg", "loc": 5,
	})
	reopened.CreateEdge("domain:pkg", "file:pkg/b.py", string(models.EdgeContains), nil)
	reopened.CreateEdge("file:pkg/b.py", "file:pkg/a.py", string(models.EdgeImports), map[string]any{"spec": "./a"})
	if err := reopened.Save(); err != nil {
		t.Fatal(err)
	}
	if mode := reopened.LastSaveMode(); mode != "incremental" {
		t.Fatalf("expected incremental save from lazyNative mode, got %q", mode)
	}
	if !reopened.lazyNative {
		t.Fatal("expected lazyNative mode to remain active after incremental Save")
	}
	counts = reopened.CountNodesAndEdges()
	if counts["total_nodes"] != 4 || counts["total_edges"] != 4 {
		t.Fatalf("unexpected counts after lazy incremental save: %v", counts)
	}
	if len(reopened.GetEdgesByType(string(models.EdgeImports))) != 1 {
		t.Fatal("expected 1 IMPORTS edge in lazyNative mode")
	}
}
