package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meta-lattice/src/config"
	"meta-lattice/src/features/auditor"
	"meta-lattice/src/indexer"
	"meta-lattice/src/storage"
)

// writeAuditFixture materializes files (slash-separated relative paths) under a
// temporary workspace and returns the configured engine.
func writeAuditFixture(t *testing.T, files map[string]string) (*config.LatticeConfig, *storage.GraphStorage) {
	t.Helper()
	root := t.TempDir()
	for rel, source := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(source), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.NewLatticeConfig(root)
	db := storage.NewGraphStorage(cfg.DBPath)
	indexer.NewCacheEngine(cfg, db).Sync(false)
	return cfg, db
}

// A scoped (post-edit hook) audit must keep the same verdict as a full audit:
// layer violations, the cycles that pass through the target, and its coupling
// metrics are all derived without hydrating every module node.
func TestScopedAuditKeepsCycleAndLayerDetection(t *testing.T) {
	cfg, db := writeAuditFixture(t, map[string]string{
		"src/controllers/user_controller.py": "from src.services.user_service import UserService\n\nclass UserController:\n    pass\n",
		"src/services/user_service.py":       "from src.repositories.user_repo import UserRepository\n\nclass UserService:\n    pass\n",
		"src/repositories/user_repo.py":      "from src.controllers.user_controller import UserController\n\nclass UserRepository:\n    pass\n",
	})

	res := auditor.NewArchitectureBoundaryAuditor(db, cfg).CheckLayerViolation("src/repositories/user_repo.py", nil)
	if res["status"] != "VIOLATIONS_DETECTED" {
		t.Fatalf("expected VIOLATIONS_DETECTED, got %v", res["status"])
	}

	violations, _ := res["layer_violations"].([]map[string]any)
	if len(violations) == 0 {
		t.Fatal("expected a repository -> controller violation")
	}
	if violations[0]["source_layer"] != "repository" || violations[0]["target_layer"] != "controller" {
		t.Fatalf("unexpected violation: %+v", violations[0])
	}

	cycles, _ := res["circular_dependencies"].([]map[string]any)
	if len(cycles) == 0 {
		t.Fatal("expected the scoped audit to report the cycle through the target")
	}
	display, _ := cycles[0]["display_chain"].(string)
	for _, want := range []string{"user_repo.py", "user_controller.py", "user_service.py"} {
		if !strings.Contains(display, want) {
			t.Fatalf("cycle %q does not contain %s", display, want)
		}
	}

	metrics, _ := res["coupling_metrics"].(map[string]any)
	repoMetrics, ok := metrics["src/repositories/user_repo.py"].(map[string]any)
	if !ok {
		t.Fatalf("expected coupling metrics for the target, got %+v", metrics)
	}
	if repoMetrics["afferent_coupling_Ca"] != 1 || repoMetrics["efferent_coupling_Ce"] != 1 {
		t.Fatalf("expected Ca=1, Ce=1 for the target, got %+v", repoMetrics)
	}
}

// A file without import edges cannot violate a boundary or close a cycle, so the
// scoped audit skips the global edge scan but must still report 0/0 coupling.
func TestScopedAuditLeafFileKeepsZeroCoupling(t *testing.T) {
	cfg, db := writeAuditFixture(t, map[string]string{
		"src/leaf.py": "def leaf():\n    return 1\n",
	})

	res := auditor.NewArchitectureBoundaryAuditor(db, cfg).CheckLayerViolation("src/leaf.py", nil)
	if res["status"] != "PASSED" {
		t.Fatalf("expected PASSED, got %v", res["status"])
	}
	if cycles, _ := res["circular_dependencies"].([]map[string]any); len(cycles) != 0 {
		t.Fatalf("expected no cycles, got %+v", cycles)
	}

	metrics, _ := res["coupling_metrics"].(map[string]any)
	leaf, ok := metrics["src/leaf.py"].(map[string]any)
	if !ok {
		t.Fatalf("expected coupling metrics for the leaf file, got %+v", metrics)
	}
	if leaf["afferent_coupling_Ca"] != 0 || leaf["efferent_coupling_Ce"] != 0 {
		t.Fatalf("expected Ca=0, Ce=0, got %+v", leaf)
	}
}
