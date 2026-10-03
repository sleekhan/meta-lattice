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

func TestLayerViolationAndCycles(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "lattice-test-audit-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	src := filepath.Join(tmpDir, "src")
	ctrlDir := filepath.Join(src, "controllers")
	svcDir := filepath.Join(src, "services")
	repoDir := filepath.Join(src, "repositories")

	_ = os.MkdirAll(ctrlDir, 0755)
	_ = os.MkdirAll(svcDir, 0755)
	_ = os.MkdirAll(repoDir, 0755)

	// Controller imports Service
	_ = os.WriteFile(filepath.Join(ctrlDir, "user_controller.py"), []byte(`
from src.services.user_service import UserService

class UserController:
    def __init__(self, service: UserService):
        self.service = service
`), 0644)

	// Service imports Repository
	_ = os.WriteFile(filepath.Join(svcDir, "user_service.py"), []byte(`
from src.repositories.user_repo import UserRepository

class UserService:
    def __init__(self, repo: UserRepository):
        self.repo = repo
`), 0644)

	// Repository with violation: imports Controller!
	_ = os.WriteFile(filepath.Join(repoDir, "user_repo.py"), []byte(`
# VIOLATION: Repository must not import Controller!
from src.controllers.user_controller import UserController

class UserRepository:
    def find_user(self, id: str):
        return None
`), 0644)

	cfg := config.NewLatticeConfig(tmpDir)
	db := storage.NewGraphStorage(cfg.DBPath)
	engine := indexer.NewCacheEngine(cfg, db)
	engine.Sync(false)

	aud := auditor.NewArchitectureBoundaryAuditor(db, cfg)
	res := aud.CheckLayerViolation("", nil)

	if res["status"] != "VIOLATIONS_DETECTED" {
		t.Fatalf("expected VIOLATIONS_DETECTED, got %v", res["status"])
	}

	violations, _ := res["layer_violations"].([]map[string]any)
	if len(violations) == 0 {
		t.Fatal("expected at least 1 layer violation")
	}
	v := violations[0]
	if v["source_layer"] != "repository" || v["target_layer"] != "controller" {
		t.Fatalf("expected repository -> controller violation, got %+v", v)
	}

	// Circular dependency
	cycles, _ := res["circular_dependencies"].([]map[string]any)
	if len(cycles) == 0 {
		t.Fatal("expected circular dependency detected")
	}
	cycle := cycles[0]
	disp, _ := cycle["display_chain"].(string)
	if !strings.Contains(disp, "user_controller.py") || !strings.Contains(disp, "user_service.py") || !strings.Contains(disp, "user_repo.py") {
		t.Fatalf("expected cycle involving all 3 files, got %s", disp)
	}

	// Coupling metrics
	metrics, _ := res["coupling_metrics"].(map[string]any)
	svcMetrics, ok := metrics["src/services/user_service.py"].(map[string]any)
	if !ok {
		t.Fatalf("expected metrics for src/services/user_service.py, got %+v", metrics)
	}
	if svcMetrics["afferent_coupling_Ca"] != 1 || svcMetrics["efferent_coupling_Ce"] != 1 {
		t.Fatalf("expected Ca=1, Ce=1, got %+v", svcMetrics)
	}
	if svcMetrics["instability_I"] != 0.5 {
		t.Fatalf("expected Instability=0.5, got %+v", svcMetrics)
	}
}
