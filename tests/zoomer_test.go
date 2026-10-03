package tests

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meta-lattice/src/config"
	"meta-lattice/src/features/zoomer"
	"meta-lattice/src/indexer"
	"meta-lattice/src/models"
	"meta-lattice/src/storage"
)

func TestHierarchicalZoom(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "lattice-test-zoom-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	billingDir := filepath.Join(tmpDir, "src", "billing")
	_ = os.MkdirAll(billingDir, 0755)

	svcFile := filepath.Join(billingDir, "service.py")
	code := `"""Billing Service Module."""

class PaymentService:
    """Handles payments and invoices."""
    def __init__(self, api_key: str):
        self.api_key = api_key

    def process_payment(self, amount: float, currency: str = "USD") -> bool:
        if amount <= 0:
            raise ValueError("Amount must be positive")
        return True

def calculate_tax(subtotal: float, rate: float = 0.1) -> float:
    """Calculates sales tax."""
    return subtotal * rate
`
	_ = os.WriteFile(svcFile, []byte(code), 0644)

	cfg := config.NewLatticeConfig(tmpDir)
	db := storage.NewGraphStorage(cfg.DBPath)
	engine := indexer.NewCacheEngine(cfg, db)
	syncStats := engine.Sync(false)
	if syncStats.AddedCount != 1 {
		t.Fatalf("expected 1 file added, got %d", syncStats.AddedCount)
	}

	z := zoomer.NewHierarchicalZoomer(db)

	// 1. L0 Overview
	overview := z.ZoomOverview("")
	if overview["view_level"] != "L0_Overview" {
		t.Fatalf("expected L0_Overview, got %v", overview["view_level"])
	}
	domains, _ := overview["domains"].([]map[string]any)
	if len(domains) == 0 {
		t.Fatal("expected at least 1 domain")
	}

	// 2. L1 -> L2 Module Zoom
	moduleZoom := z.ZoomModule("src/billing/service.py")
	if moduleZoom["view_level"] != "L1_Module_To_L2_Interfaces" {
		t.Fatalf("expected L1_Module_To_L2_Interfaces, got %v", moduleZoom["view_level"])
	}
	classes, _ := moduleZoom["classes"].([]map[string]any)
	if len(classes) != 1 || classes[0]["name"] != "PaymentService" {
		t.Fatalf("expected PaymentService class, got %+v", classes)
	}

	// Method signatures only: verify body logic is not present
	sigs, _ := classes[0]["method_signatures"].([]string)
	sigsCombined := strings.Join(sigs, " ")
	if strings.Contains(sigsCombined, "raise ValueError") {
		t.Fatalf("method bodies should be omitted from class signatures: %s", sigsCombined)
	}

	// 3. L3 Symbol Zoom
	symbolZoom := z.ZoomSymbol("process_payment", "src/billing/service.py")
	if symbolZoom["view_level"] != "L3_Symbol_Implementation" {
		t.Fatalf("expected L3_Symbol_Implementation, got %v", symbolZoom["view_level"])
	}
	sym, _ := symbolZoom["symbol"].(map[string]any)
	if sym["name"] != "PaymentService.process_payment" {
		t.Fatalf("expected PaymentService.process_payment, got %v", sym["name"])
	}
	codeSlice, _ := sym["code"].(string)
	if !strings.Contains(codeSlice, "raise ValueError") {
		t.Fatalf("expected full body implementation in L3 symbol, got: %s", codeSlice)
	}

	// 4. BM25 Search
	searchRes := z.ZoomSearch("calculate_tax", "", 15)
	results, _ := searchRes["results"].([]map[string]any)
	if len(results) == 0 || results[0]["name"] != "calculate_tax" {
		t.Fatalf("expected calculate_tax match in search, got %+v", results)
	}
}

func TestZoomOverviewSummaryMode(t *testing.T) {
	db := storage.NewGraphStorage("")

	// Add 2 domains and 55 modules
	dom1 := models.L0DomainNode{
		ID:          "domain:services",
		Name:        "services",
		Description: "Services domain",
		Layer:       "service",
		Level:       string(models.LevelL0),
		Labels:      []string{"L0Domain", "Domain"},
	}
	db.CreateNode(dom1.ID, dom1.Labels, dom1.ToProperties())

	for i := 0; i < 55; i++ {
		modID := fmt.Sprintf("file:src/services/mod_%d.go", i)
		mod := models.L1ModuleNode{
			ID:       modID,
			Path:     fmt.Sprintf("src/services/mod_%d.go", i),
			Filename: fmt.Sprintf("mod_%d.go", i),
			Language: "go",
			Domain:   "services",
			Layer:    "service",
			Level:    string(models.LevelL1),
			Labels:   []string{"L1Module", "Module"},
		}
		db.CreateNode(mod.ID, mod.Labels, mod.ToProperties())
		db.CreateEdge(dom1.ID, mod.ID, string(models.EdgeContains), nil)
	}

	z := zoomer.NewHierarchicalZoomer(db)

	// Overview with empty domain filter on 55 modules -> summaryMode true
	overview := z.ZoomOverview("")
	if sm, ok := overview["summary_mode"].(bool); !ok || !sm {
		t.Fatalf("expected summary_mode true for >50 modules, got %+v", overview["summary_mode"])
	}

	// Overview targeted to 'services' domain -> summaryMode false (drill-down)
	targetOverview := z.ZoomOverview("services")
	if sm, ok := targetOverview["summary_mode"].(bool); !ok || sm {
		t.Fatalf("expected summary_mode false when target domain specified, got %+v", targetOverview["summary_mode"])
	}
}
