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
