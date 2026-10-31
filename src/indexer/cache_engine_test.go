package indexer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"meta-lattice/src/config"
	"meta-lattice/src/storage"
)

func TestSyncCompletesTargetedUpdateAfterWorkspaceDrift(t *testing.T) {
	root := t.TempDir()
	write := func(name, source string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.py", "def first():\n    return 1\n")
	write("b.py", "def second():\n    return 2\n")

	cfg := config.NewLatticeConfig(root)
	db := storage.NewGraphStorage(cfg.DBPath)
	engine := NewCacheEngine(cfg, db)
	if stats := engine.Sync(false); stats.Error != "" {
		t.Fatalf("initial full sync failed: %s", stats.Error)
	}
	if !engine.IsFullyIndexed() {
		t.Fatal("initial sync should mark the index complete")
	}

	write("a.py", "def first():\n    return 10\n")
	write("c.py", "def third():\n    return 3\n")
	if _, err := engine.SyncTargetFile("a.py"); err != nil {
		t.Fatal(err)
	}
	if engine.IsFullyIndexed() {
		t.Fatal("targeted sync should invalidate the full-index marker")
	}

	stats := engine.Sync(false)
	if stats.Error != "" {
		t.Fatalf("follow-up sync failed: %s", stats.Error)
	}
	if !engine.IsFullyIndexed() {
		t.Fatal("follow-up sync should restore the full-index marker")
	}
	if _, ok := db.GetNode("file:c.py"); !ok {
		t.Fatal("follow-up sync did not index the unedited file")
	}
	// The follow-up must stay incremental: only the new file is parsed, the
	// file already refreshed by the targeted sync is a cache hit.
	if stats.AddedCount != 1 || stats.ModifiedCount != 0 || stats.CachedCount != 2 {
		t.Fatalf("follow-up sync was not incremental: added=%d modified=%d cached=%d",
			stats.AddedCount, stats.ModifiedCount, stats.CachedCount)
	}
}

func TestRefreshStateSeesExternalCacheUpdate(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.py"), []byte("def first():\n    return 1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := config.NewLatticeConfig(root)
	engine := NewCacheEngine(cfg, storage.NewGraphStorage(cfg.DBPath))
	if stats := engine.Sync(false); stats.Error != "" {
		t.Fatalf("initial sync failed: %s", stats.Error)
	}
	if engine.RefreshState() {
		t.Fatal("no external write happened yet, RefreshState should be a no-op")
	}

	// Simulate the post-tool hook running in another process: it rewrites the
	// cache state with FullSync=false and a new mtime/size.
	data, err := os.ReadFile(cfg.CacheStateFile)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["full_sync"] = false
	raw["last_sync"] = float64(1)
	updated, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.CacheStateFile, updated, 0644); err != nil {
		t.Fatal(err)
	}

	if !engine.RefreshState() {
		t.Fatal("RefreshState did not detect the externally written state")
	}
	if engine.IsFullyIndexed() {
		t.Fatal("RefreshState kept the stale full-index marker")
	}
}
