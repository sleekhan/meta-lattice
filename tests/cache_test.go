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

func TestSyncTargetFileAndDirtyOptimization(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "lattice-test-target-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	fileA := filepath.Join(tmpDir, "a.py")
	fileB := filepath.Join(tmpDir, "b.py")
	_ = os.WriteFile(fileA, []byte("def a_func(): return 1\n"), 0644)
	_ = os.WriteFile(fileB, []byte("def b_func(): return 2\n"), 0644)

	cfg := config.NewLatticeConfig(tmpDir)
	db := storage.NewGraphStorage(cfg.DBPath)
	engine := indexer.NewCacheEngine(cfg, db)

	// First sync
	stats := engine.Sync(false)
	if stats.AddedCount != 2 {
		t.Fatalf("expected 2 added, got %d", stats.AddedCount)
	}
	if db.IsDirty() {
		t.Fatal("expected db not dirty after Save")
	}

	// Repeated sync without changes should not dirty db or re-save
	stats2 := engine.Sync(false)
	if stats2.AddedCount != 0 || stats2.ModifiedCount != 0 || stats2.DeletedCount != 0 {
		t.Fatalf("expected 0 changes on clean sync, got %+v", stats2)
	}
	if db.IsDirty() {
		t.Fatal("expected db to remain not dirty")
	}

	// Target sync of modified b.py
	time.Sleep(50 * time.Millisecond)
	_ = os.WriteFile(fileB, []byte("def b_func(): return 42\ndef b_new(): return 99\n"), 0644)

	tStats, err := engine.SyncTargetFile("b.py")
	if err != nil {
		t.Fatalf("SyncTargetFile failed: %v", err)
	}
	if tStats.ModifiedCount != 1 {
		t.Fatalf("expected 1 modified file from SyncTargetFile, got %d", tStats.ModifiedCount)
	}

	// Verify symbol:b.py:b_new exists in db
	if _, ok := db.GetNode("symbol:b.py:b_new"); !ok {
		t.Fatal("expected symbol:b.py:b_new in db after SyncTargetFile")
	}

	// ReloadIfModified check
	db2 := storage.NewGraphStorage(cfg.DBPath)
	if db2.ReloadIfModified() {
		t.Fatal("expected ReloadIfModified to return false when not modified")
	}

	// Now modify fileA and sync with engine to write db to disk
	time.Sleep(50 * time.Millisecond)
	_ = os.WriteFile(fileA, []byte("def a_func(): return 100\n"), 0644)
	_, _ = engine.SyncTargetFile("a.py")

	// db2 should detect modification on disk and reload
	if !db2.ReloadIfModified() {
		t.Fatal("expected db2.ReloadIfModified to return true after disk change")
	}
	if node, ok := db2.GetNode("symbol:b.py:b_new"); !ok || node == nil {
		t.Fatal("expected db2 to have reloaded new symbol")
	}
}
