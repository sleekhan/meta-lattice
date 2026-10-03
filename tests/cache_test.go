package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"meta-lattice/src/config"
	"meta-lattice/src/indexer"
	"meta-lattice/src/models"
	"meta-lattice/src/storage"
)

func TestIncrementalCacheLifecycleAndEdgesSurvive(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "lattice-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	pkgDir := filepath.Join(tmpDir, "pkg")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatal(err)
	}

	coreFile := filepath.Join(pkgDir, "core.py")
	userFile := filepath.Join(pkgDir, "user.py")

	_ = os.WriteFile(coreFile, []byte("def base():\n    return 1\n"), 0644)
	_ = os.WriteFile(userFile, []byte("from .core import base\n\ndef use(\n    a: int,\n    b: str = 'x:y',\n) -> int:\n    return base()\n"), 0644)

	cfg := config.NewLatticeConfig(tmpDir)
	db := storage.NewGraphStorage(cfg.DBPath)
	engine := indexer.NewCacheEngine(cfg, db)

	stats := engine.Sync(false)
	if stats.AddedCount != 2 {
		t.Fatalf("expected 2 added files, got %d", stats.AddedCount)
	}

	// Verify edges
	impEdges := db.GetOutgoingEdges("file:pkg/user.py", string(models.EdgeImports))
	if len(impEdges) != 1 || impEdges[0].TargetID != "file:pkg/core.py" {
		t.Fatalf("expected IMPORTS edge to file:pkg/core.py, got %+v", impEdges)
	}

	callEdges := db.GetOutgoingEdges("symbol:pkg/user.py:use", string(models.EdgeCalls))
	if len(callEdges) != 1 || callEdges[0].TargetID != "symbol:pkg/core.py:base" {
		t.Fatalf("expected CALLS edge to symbol:pkg/core.py:base, got %+v", callEdges)
	}

	// Callee modified
	time.Sleep(50 * time.Millisecond)
	_ = os.WriteFile(coreFile, []byte("def base():\n    return 2\n"), 0644)

	stats2 := engine.Sync(false)
	if stats2.ModifiedCount != 1 || stats2.CachedCount != 1 {
		t.Fatalf("expected 1 modified and 1 cached, got %+v", stats2)
	}

	// Edges survive
	impEdges2 := db.GetOutgoingEdges("file:pkg/user.py", string(models.EdgeImports))
	if len(impEdges2) != 1 || impEdges2[0].TargetID != "file:pkg/core.py" {
		t.Fatalf("expected IMPORTS edge to persist, got %+v", impEdges2)
	}

	callEdges2 := db.GetOutgoingEdges("symbol:pkg/user.py:use", string(models.EdgeCalls))
	if len(callEdges2) != 1 || callEdges2[0].TargetID != "symbol:pkg/core.py:base" {
		t.Fatalf("expected CALLS edge to persist, got %+v", callEdges2)
	}
}

func TestAmbiguousCallsAreNotMerged(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "lattice-test-ambig-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	_ = os.WriteFile(filepath.Join(tmpDir, "a.py"), []byte("class A:\n    def save(self): pass\n    def run(self): self.save()\n"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "b.py"), []byte("class B:\n    def save(self): pass\n"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "c.py"), []byte("def go(x):\n    x.save()\n"), 0644)

	cfg := config.NewLatticeConfig(tmpDir)
	db := storage.NewGraphStorage(cfg.DBPath)
	engine := indexer.NewCacheEngine(cfg, db)
	engine.Sync(false)

	// A.run should resolve to A.save (same class)
	callsA := db.GetOutgoingEdges("symbol:a.py:A.run", string(models.EdgeCalls))
	if len(callsA) != 1 || callsA[0].TargetID != "symbol:a.py:A.save" {
		t.Fatalf("expected A.run -> A.save, got %+v", callsA)
	}

	// c.go should NOT resolve because two save candidates exist and neither is in c.py or imported
	callsC := db.GetOutgoingEdges("symbol:c.py:go", string(models.EdgeCalls))
	if len(callsC) != 0 {
		t.Fatalf("expected c.go to be ambiguous and not merged, got %+v", callsC)
	}
}

func TestStaleCacheVersionReindexesAndEmptyDomainRemoved(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "lattice-test-stale-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	pkgDir := filepath.Join(tmpDir, "pkg")
	_ = os.MkdirAll(pkgDir, 0755)
	modFile := filepath.Join(pkgDir, "m.py")
	_ = os.WriteFile(modFile, []byte("def f(): pass\n"), 0644)

	cfg := config.NewLatticeConfig(tmpDir)
	db := storage.NewGraphStorage(cfg.DBPath)
	engine := indexer.NewCacheEngine(cfg, db)
	engine.Sync(false)

	// Invalidate cache version in cache_state.json
	data, err := os.ReadFile(cfg.CacheStateFile)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	_ = json.Unmarshal(data, &state)
	state["version"] = "0.0"
	invalidated, _ := json.MarshalIndent(state, "", "  ")
	_ = os.WriteFile(cfg.CacheStateFile, invalidated, 0644)

	// New engine loading stale cache
	engine2 := indexer.NewCacheEngine(cfg, db)
	stats := engine2.Sync(false)
	if stats.ModifiedCount != 1 {
		t.Fatalf("expected modified_count 1 for stale cache version, got %d", stats.ModifiedCount)
	}

	// Delete file and verify domain:pkg is removed
	_ = os.Remove(modFile)
	engine2.Sync(false)
	if _, ok := db.GetNode("domain:pkg"); ok {
		t.Fatal("expected domain:pkg to be removed after deleting last file")
	}
}
