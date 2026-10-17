package tests

import (
	"os"
	"path/filepath"
	"testing"

	"meta-lattice/src/config"
	"meta-lattice/src/features/auditor"
	"meta-lattice/src/indexer"
	"meta-lattice/src/storage"
)

func TestHookSingleFileTargetedWorkflow(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "lattice-test-hook-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	srcDir := filepath.Join(tmpDir, "src")
	_ = os.MkdirAll(srcDir, 0755)

	svcFile := filepath.Join(srcDir, "service.py")
	repoFile := filepath.Join(srcDir, "repo.py")

	_ = os.WriteFile(repoFile, []byte("class Repo:\n    def get(self): return 1\n"), 0644)
	_ = os.WriteFile(svcFile, []byte("from src.repo import Repo\nclass Service:\n    def __init__(self, r: Repo):\n        self.r = r\n"), 0644)

	cfg := config.NewLatticeConfig(tmpDir)
	db := storage.NewGraphStorage(cfg.DBPath)
	engine := indexer.NewCacheEngine(cfg, db)

	// Initial sync
	stats := engine.Sync(false)
	if stats.AddedCount != 2 {
		t.Fatalf("expected 2 added, got %d", stats.AddedCount)
	}

	// Targeted post-tool edit to service.py
	_ = os.WriteFile(svcFile, []byte("from src.repo import Repo\nclass Service:\n    def __init__(self, r: Repo):\n        self.r = r\n    def run(self): return self.r.get()\n"), 0644)

	tStats, err := engine.SyncTargetFile("src/service.py")
	if err != nil {
		t.Fatalf("SyncTargetFile failed: %v", err)
	}
	if tStats.ModifiedCount != 1 {
		t.Fatalf("expected 1 modified file, got %d", tStats.ModifiedCount)
	}

	// Audit target file
	aud := auditor.NewArchitectureBoundaryAuditor(db, cfg)
	res := aud.CheckLayerViolation("src/service.py", nil)
	if res["status"] != "PASSED" {
		t.Fatalf("expected PASSED status, got %v", res["status"])
	}
}
