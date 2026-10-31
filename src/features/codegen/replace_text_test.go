package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceTextReplaceAll(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "sample.py")
	if err := os.WriteFile(file, []byte("x = 1\nx = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cg := NewCodegen(root)
	_, err := cg.ApplyPlan([]PlanOperation{{Op: "replace_text", Path: "sample.py", OldText: "x = 1", NewText: "x = 2", ReplaceAll: true}}, false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "x = 2\nx = 2\n" {
		t.Fatalf("replace_all produced %q", data)
	}
}
