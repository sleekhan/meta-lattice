package diff

import (
	"path/filepath"
	"testing"

	"meta-lattice/src/config"
	"meta-lattice/src/models"
	"meta-lattice/src/storage"
)

func newTestStorage(t *testing.T) *storage.GraphStorage {
	t.Helper()
	return storage.NewGraphStorage(filepath.Join(t.TempDir(), "knowledge.lattice"))
}

func TestParseGitDiff(t *testing.T) {
	sampleDiff := `diff --git a/src/service.go b/src/service.go
index 111..222 100644
--- a/src/service.go
+++ b/src/service.go
@@ -10,3 +10,4 @@ func Process() {
+	newStep()
@@ -40,0 +41,5 @@ func Helper() {
+	line1
+	line2
diff --git a/src/util.go b/src/util.go
--- a/src/util.go
+++ b/src/util.go
@@ -5 +5,2 @@ func Util() {
+	extra
`
	files := ParseGitDiff(sampleDiff)
	if len(files) != 2 {
		t.Fatalf("expected 2 changed files, got %d", len(files))
	}
	if files[0].FilePath != "src/service.go" {
		t.Errorf("expected src/service.go, got %s", files[0].FilePath)
	}
	if len(files[0].Spans) != 2 {
		t.Fatalf("expected 2 spans in service.go, got %d", len(files[0].Spans))
	}
	if files[0].Spans[0].Start != 10 || files[0].Spans[0].End != 13 {
		t.Errorf("span 0 mismatch: %+v", files[0].Spans[0])
	}
	if files[0].Spans[1].Start != 41 || files[0].Spans[1].End != 45 {
		t.Errorf("span 1 mismatch: %+v", files[0].Spans[1])
	}
	if files[1].FilePath != "src/util.go" {
		t.Errorf("expected src/util.go, got %s", files[1].FilePath)
	}
}

func TestDetectChangesWithASTAndTests(t *testing.T) {
	db := newTestStorage(t)
	cfg := config.NewLatticeConfig(t.TempDir())

	// File nodes
	db.CreateNode("file:src/service.go", []string{"L1Module"}, map[string]any{
		"id":    "file:src/service.go",
		"path":  "src/service.go",
		"level": string(models.LevelL1),
	})
	db.CreateNode("file:src/service_test.go", []string{"L1Module"}, map[string]any{
		"id":    "file:src/service_test.go",
		"path":  "src/service_test.go",
		"level": string(models.LevelL1),
	})

	// Symbols
	db.CreateNode("symbol:src/service.go:ProcessPayment", []string{"L3Symbol"}, map[string]any{
		"id":         "symbol:src/service.go:ProcessPayment",
		"name":       "ProcessPayment",
		"file_path":  "src/service.go",
		"level":      string(models.LevelL3),
		"is_public":  true,
		"line_start": 10,
		"line_end":   30,
		"signature":  "func ProcessPayment(amount int)",
	})
	db.CreateNode("symbol:src/service.go:UntouchedFunc", []string{"L3Symbol"}, map[string]any{
		"id":         "symbol:src/service.go:UntouchedFunc",
		"name":       "UntouchedFunc",
		"file_path":  "src/service.go",
		"level":      string(models.LevelL3),
		"line_start": 80,
		"line_end":   95,
	})
	db.CreateNode("symbol:src/service_test.go:TestProcessPayment", []string{"L3Symbol"}, map[string]any{
		"id":         "symbol:src/service_test.go:TestProcessPayment",
		"name":       "TestProcessPayment",
		"file_path":  "src/service_test.go",
		"level":      string(models.LevelL3),
		"line_start": 5,
		"line_end":   15,
	})

	// Contains edges
	db.CreateEdge("file:src/service.go", "symbol:src/service.go:ProcessPayment", string(models.EdgeContains), nil)
	db.CreateEdge("file:src/service.go", "symbol:src/service.go:UntouchedFunc", string(models.EdgeContains), nil)
	db.CreateEdge("file:src/service_test.go", "symbol:src/service_test.go:TestProcessPayment", string(models.EdgeContains), nil)

	// Call edge from test to ProcessPayment
	db.CreateEdge("symbol:src/service_test.go:TestProcessPayment", "symbol:src/service.go:ProcessPayment", string(models.EdgeCalls), nil)

	detector := NewChangeDetector(cfg, db)

	rawDiff := `diff --git a/src/service.go b/src/service.go
--- a/src/service.go
+++ b/src/service.go
@@ -15,2 +15,2 @@
-	tax := 0.05
+	tax := 0.10
`
	res := detector.DetectChanges("", "all", 3, rawDiff)
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}

	if len(res.ChangedSymbols) != 1 {
		t.Fatalf("expected 1 changed symbol, got %d", len(res.ChangedSymbols))
	}
	if res.ChangedSymbols[0].Name != "ProcessPayment" {
		t.Errorf("expected ProcessPayment, got %s", res.ChangedSymbols[0].Name)
	}

	if len(res.AffectedTests) != 1 {
		t.Fatalf("expected 1 affected test, got %d", len(res.AffectedTests))
	}
	if res.AffectedTests[0].TestSymbol != "TestProcessPayment" {
		t.Errorf("expected TestProcessPayment, got %s", res.AffectedTests[0].TestSymbol)
	}
	if res.AffectedTests[0].TestFile != "src/service_test.go" {
		t.Errorf("expected src/service_test.go, got %s", res.AffectedTests[0].TestFile)
	}
}
