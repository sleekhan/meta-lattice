package indexer

import (
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
		t.Fatalf("follow-up full sync failed: %s", stats.Error)
	}
	if !engine.IsFullyIndexed() {
		t.Fatal("follow-up sync should restore the full-index marker")
	}
	if _, ok := db.GetNode("file:c.py"); !ok {
		t.Fatal("follow-up full sync did not index the unedited file")
	}
}
