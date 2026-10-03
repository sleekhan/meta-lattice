package tests

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meta-lattice/src/config"
	"meta-lattice/src/features/blast"
	"meta-lattice/src/indexer"
	"meta-lattice/src/storage"
)

func TestBlastRadiusEstimation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "lattice-test-blast-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	src := filepath.Join(tmpDir, "src")
	coreDir := filepath.Join(src, "core")
	authDir := filepath.Join(src, "auth")
	apiDir := filepath.Join(src, "api")

	_ = os.MkdirAll(coreDir, 0755)
	_ = os.MkdirAll(authDir, 0755)
	_ = os.MkdirAll(apiDir, 0755)

	_ = os.WriteFile(filepath.Join(coreDir, "crypto.py"), []byte(`
def hash_token(token: str) -> str:
    """Core token hasher."""
    return token + "_hashed"
`), 0644)

	_ = os.WriteFile(filepath.Join(authDir, "service.py"), []byte(`
from src.core.crypto import hash_token

def authenticate(token: str) -> bool:
    h = hash_token(token)
    return True
`), 0644)

	_ = os.WriteFile(filepath.Join(apiDir, "routes.py"), []byte(`
from src.auth.service import authenticate

def login_handler(raw_token: str):
    return authenticate(raw_token)
`), 0644)

	cfg := config.NewLatticeConfig(tmpDir)
	db := storage.NewGraphStorage(cfg.DBPath)
	engine := indexer.NewCacheEngine(cfg, db)
	engine.Sync(false)

	estimator := blast.NewBlastRadiusEstimator(db)
	res := estimator.EstimateBlastRadius("hash_token", "signature", 4)

	if _, hasErr := res["error"]; hasErr {
		t.Fatalf("unexpected error in blast radius: %+v", res)
	}

	if res["target"] != "hash_token" {
		t.Fatalf("expected target hash_token, got %v", res["target"])
	}

	score, _ := res["blast_score"].(int)
	if score <= 0 {
		t.Fatalf("expected positive blast score, got %d", score)
	}

	topPoints, _ := res["top_10_breaking_points"].([]map[string]any)
	if len(topPoints) == 0 {
		t.Fatal("expected at least 1 breaking point")
	}

	hasHop1 := false
	for _, p := range topPoints {
		if p["hop_distance"] == 1 {
			hasHop1 = true
			symName, _ := p["symbol_name"].(string)
			fPath, _ := p["file_path"].(string)
			if !strings.Contains(symName, "authenticate") && !strings.Contains(fPath, "service") {
				t.Fatalf("unexpected hop 1 consumer: %+v", p)
			}
		}
		// Verify CONTAINS is never in edge_type
		edgeType, _ := p["edge_type"].(string)
		if edgeType == "CONTAINS" {
			t.Fatalf("CONTAINS edge must not be included in blast radius: %+v", p)
		}
	}
	if !hasHop1 {
		t.Fatal("expected at least 1 hop 1 direct consumer")
	}

	tree, _ := res["blast_tree"].(string)
	if !strings.Contains(tree, "[Target: hash_token]") {
		t.Fatalf("expected ASCII tree containing target, got %s", tree)
	}
}

func TestBlastRadiusExplorationLimit(t *testing.T) {
	db := storage.NewGraphStorage("")

	targetID := "func:src/core/utils.go:format_string"
	db.CreateNode(targetID, []string{"L3Symbol", "Function"}, map[string]any{
		"id":         targetID,
		"name":       "format_string",
		"file_path":  "src/core/utils.go",
		"is_public":  true,
		"level":      "L3",
		"line_start": 10,
	})

	// Add 300 callers
	for i := 0; i < 300; i++ {
		callerID := fmt.Sprintf("func:src/services/svc_%d.go:call_format_%d", i, i)
		db.CreateNode(callerID, []string{"L3Symbol", "Function"}, map[string]any{
			"id":         callerID,
			"name":       fmt.Sprintf("call_format_%d", i),
			"file_path":  fmt.Sprintf("src/services/svc_%d.go", i),
			"domain":     "services",
			"level":      "L3",
			"line_start": 20,
		})
		db.CreateEdge(callerID, targetID, "CALLS", nil)
	}

	estimator := blast.NewBlastRadiusEstimator(db)
	res := estimator.EstimateBlastRadius("format_string", "signature", 2)

	metrics, ok := res["metrics"].(map[string]any)
	if !ok {
		t.Fatalf("expected metrics map in blast result, got %+v", res)
	}

	truncated, _ := metrics["truncated"].(bool)
	if !truncated {
		t.Fatalf("expected truncated true when > 250 callers exist, got %v", truncated)
	}

	if note, ok := res["truncation_note"].(string); !ok || !strings.Contains(note, "capped at 250 nodes") {
		t.Fatalf("expected truncation note mentioning 250 nodes, got %v", note)
	}
}
