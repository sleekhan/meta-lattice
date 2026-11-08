package adr

import (
	"testing"

	"meta-lattice/src/config"
)

func TestADRManagerCRUD(t *testing.T) {
	tempDir := t.TempDir()
	cfg := config.NewLatticeConfig(tempDir)
	mgr := NewADRManager(cfg)

	// 1. Create
	adr1, err := mgr.Create(CreateADROptions{
		Title:        "Use Single Binary Standalone Architecture",
		Status:       "accepted",
		Context:      "We need zero external runtime dependencies.",
		Decision:     "Bundle pure-Go graph storage engine alongside LatticeDB.",
		Consequences: "Easy cross-platform distribution.",
		Tags:         []string{"architecture", "storage"},
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if adr1.ID != "ADR-0001" {
		t.Errorf("expected ADR-0001, got %s", adr1.ID)
	}

	// 2. List
	all := mgr.List("", "")
	if len(all) != 1 {
		t.Fatalf("expected 1 ADR, got %d", len(all))
	}

	// 3. Get
	fetched, err := mgr.Get("1")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if fetched.Title != adr1.Title {
		t.Errorf("title mismatch: %s vs %s", fetched.Title, adr1.Title)
	}

	// 4. Update
	newStatus := "superseded"
	supBy := "ADR-0002"
	updated, err := mgr.Update(UpdateADROptions{
		ID:           "ADR-0001",
		Status:       &newStatus,
		SupersededBy: &supBy,
	})
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if updated.Status != StatusSuperseded || updated.SupersededBy != "ADR-0002" {
		t.Errorf("update mismatch: %+v", updated)
	}

	// 5. Delete
	if err := mgr.Delete("ADR-0001"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if len(mgr.List("", "")) != 0 {
		t.Fatalf("expected 0 ADRs after delete")
	}
}
