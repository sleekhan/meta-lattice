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

func TestAuditorFindsAlternateCycles(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"a.py": "from b import b\nfrom c import c\ndef a(): pass\n",
		"b.py": "from a import a\ndef b(): pass\n",
		"c.py": "from b import b\ndef c(): pass\n",
	}
	for name, source := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.NewLatticeConfig(root)
	db := storage.NewGraphStorage(cfg.DBPath)
	indexer.NewCacheEngine(cfg, db).Sync(false)
	res := auditor.NewArchitectureBoundaryAuditor(db, cfg).CheckLayerViolation("", nil)
	cycles, _ := res["circular_dependencies"].([]map[string]any)
	if len(cycles) < 2 {
		t.Fatalf("expected alternate cycles, got %+v", cycles)
	}
}

func TestAuditorReportsEachCycleOnce(t *testing.T) {
	root := t.TempDir()
	// h imports a and b; a and b both import h. There are exactly two cycles
	// (a<->h and b<->h) inside one SCC.
	files := map[string]string{
		"h.py": "from a import a\nfrom b import b\ndef h(): pass\n",
		"a.py": "from h import h\ndef a(): pass\n",
		"b.py": "from h import h\ndef b(): pass\n",
	}
	for name, source := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.NewLatticeConfig(root)
	db := storage.NewGraphStorage(cfg.DBPath)
	indexer.NewCacheEngine(cfg, db).Sync(false)
	res := auditor.NewArchitectureBoundaryAuditor(db, cfg).CheckLayerViolation("", nil)
	cycles, _ := res["circular_dependencies"].([]map[string]any)
	if len(cycles) != 2 {
		t.Fatalf("expected 2 distinct cycles, got %d: %+v", len(cycles), cycles)
	}
	seen := make(map[string]bool, len(cycles))
	for _, c := range cycles {
		chain, _ := c["display_chain"].(string)
		if seen[chain] {
			t.Fatalf("cycle reported twice: %s", chain)
		}
		seen[chain] = true
	}
	if summary, ok := res["summary"].(map[string]any); ok {
		if total, _ := summary["total_circular_dependencies"].(int); total != 2 {
			t.Fatalf("summary counted %d cycles, want 2", total)
		}
	}
}
