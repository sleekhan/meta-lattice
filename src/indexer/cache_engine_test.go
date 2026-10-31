package indexer

import (
	"os"
	"path/filepath"
	"testing"

	"meta-lattice/src/config"
	"meta-lattice/src/storage"
)

func TestSyncCompletesPartialTargetedIndex(t *testing.T) {
	root := t.TempDir()
	for name, source := range map[string]string{
		"a.py": "def first():\n    return 1\n",
		"b.py": "def second():\n    return 2\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.NewLatticeConfig(root)
	db := storage.NewGraphStorage(cfg.DBPath)
	engine := NewCacheEngine(cfg, db)
	if _, err := engine.SyncTargetFile("a.py"); err != nil {
		t.Fatal(err)
	}
	if engine.IsFullyIndexed() {
		t.Fatal("targeted sync marked the workspace fully indexed")
	}

	stats := engine.Sync(false)
	if stats.Error != "" {
		t.Fatalf("full sync failed: %s", stats.Error)
	}
	if !engine.IsFullyIndexed() {
		t.Fatal("full sync did not mark workspace fully indexed")
	}
	if _, ok := db.GetNode("file:b.py"); !ok {
		t.Fatal("full sync did not index untouched file")
	}
}
