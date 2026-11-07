package tracer

import (
	"path/filepath"
	"testing"

	"meta-lattice/src/models"
	"meta-lattice/src/storage"
)

func newTestStorage(t *testing.T) *storage.GraphStorage {
	t.Helper()
	return storage.NewGraphStorage(filepath.Join(t.TempDir(), "knowledge.lattice"))
}

func TestCallTracerTreeAndPath(t *testing.T) {
	db := newTestStorage(t)

	// A -> B -> C
	// A -> D
	db.CreateNode("symbol:a.go:A", []string{"L3Symbol"}, map[string]any{
		"id":         "symbol:a.go:A",
		"name":       "A",
		"file_path":  "a.go",
		"level":      string(models.LevelL3),
		"signature":  "func A()",
		"line_start": 10,
		"line_end":   20,
	})
	db.CreateNode("symbol:b.go:B", []string{"L3Symbol"}, map[string]any{
		"id":         "symbol:b.go:B",
		"name":       "B",
		"file_path":  "b.go",
		"level":      string(models.LevelL3),
		"signature":  "func B()",
		"line_start": 5,
		"line_end":   15,
	})
	db.CreateNode("symbol:c.go:C", []string{"L3Symbol"}, map[string]any{
		"id":         "symbol:c.go:C",
		"name":       "C",
		"file_path":  "c.go",
		"level":      string(models.LevelL3),
		"signature":  "func C()",
		"line_start": 1,
		"line_end":   8,
	})
	db.CreateNode("symbol:d.go:D", []string{"L3Symbol"}, map[string]any{
		"id":         "symbol:d.go:D",
		"name":       "D",
		"file_path":  "d.go",
		"level":      string(models.LevelL3),
		"signature":  "func D()",
		"line_start": 2,
		"line_end":   12,
	})

	db.CreateEdge("symbol:a.go:A", "symbol:b.go:B", string(models.EdgeCalls), nil)
	db.CreateEdge("symbol:b.go:B", "symbol:c.go:C", string(models.EdgeCalls), nil)
	db.CreateEdge("symbol:a.go:A", "symbol:d.go:D", string(models.EdgeCalls), nil)

	tracer := NewCallTracer(db)

	// 1. Test Outbound Tree from A
	outRes := tracer.TraceCallPath("A", "", "outbound", 3)
	if outRes.Error != "" {
		t.Fatalf("unexpected error: %s", outRes.Error)
	}
	if outRes.TotalHops != 3 {
		t.Fatalf("expected 3 total hops in tree (A->B, B->C, A->D), got %d", outRes.TotalHops)
	}
	if outRes.UniqueSymbols != 4 {
		t.Fatalf("expected 4 unique symbols, got %d", outRes.UniqueSymbols)
	}

	// 2. Test Point-to-Point Path from A to C
	pathRes := tracer.TraceCallPath("A", "C", "outbound", 3)
	if pathRes.Error != "" {
		t.Fatalf("unexpected error: %s", pathRes.Error)
	}
	if len(pathRes.Paths) != 1 {
		t.Fatalf("expected 1 path from A to C, got %d", len(pathRes.Paths))
	}
	p := pathRes.Paths[0]
	if len(p) != 2 {
		t.Fatalf("expected 2 hops in path (A->B, B->C), got %d", len(p))
	}
	if p[0].FromID != "symbol:a.go:A" || p[0].ToID != "symbol:b.go:B" {
		t.Errorf("hop 0 mismatch: %+v", p[0])
	}
	if p[1].FromID != "symbol:b.go:B" || p[1].ToID != "symbol:c.go:C" {
		t.Errorf("hop 1 mismatch: %+v", p[1])
	}

	// 3. Test Inbound Tree from C
	inRes := tracer.TraceCallPath("C", "", "inbound", 3)
	if inRes.Error != "" {
		t.Fatalf("unexpected error: %s", inRes.Error)
	}
	if inRes.TotalHops != 2 {
		t.Fatalf("expected 2 inbound hops (C<-B, B<-A), got %d", inRes.TotalHops)
	}
}

