package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meta-lattice/src/features/codegen"
)

func TestScaffoldCreatesTemplate(t *testing.T) {
	dir := t.TempDir()
	cg := codegen.NewCodegen(dir)

	res, err := cg.ScaffoldModule(codegen.ScaffoldOptions{
		FilePath:  "svc/user_service.py",
		Kind:      "class",
		Name:      "UserService",
		Namespace: "",
		Imports:   []string{"repo.base"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res["language"] != "python" {
		t.Fatalf("expected python language, got %v", res)
	}
	data, err := os.ReadFile(filepath.Join(dir, "svc", "user_service.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "class UserService") {
		t.Fatalf("expected class skeleton, got:\n%s", data)
	}
}

func TestScaffoldRefusesOverwriteAndEscape(t *testing.T) {
	dir := t.TempDir()
	cg := codegen.NewCodegen(dir)

	opts := codegen.ScaffoldOptions{FilePath: "a.go", Kind: "struct", Name: "A", Namespace: "main"}
	if _, err := cg.ScaffoldModule(opts); err != nil {
		t.Fatal(err)
	}
	if _, err := cg.ScaffoldModule(opts); err == nil {
		t.Fatal("expected overwrite refusal")
	}
	opts.Overwrite = true
	if _, err := cg.ScaffoldModule(opts); err != nil {
		t.Fatalf("expected overwrite to succeed: %v", err)
	}

	for _, bad := range []string{"../escape.go", "/abs.go", "notes.txt"} {
		o := codegen.ScaffoldOptions{FilePath: bad, Kind: "module", Name: "X"}
		if _, err := cg.ScaffoldModule(o); err == nil {
			t.Fatalf("expected rejection for path %q", bad)
		}
	}
}

func TestApplyPlanDryRunAndExecute(t *testing.T) {
	dir := t.TempDir()
	cg := codegen.NewCodegen(dir)

	ops := []codegen.PlanOperation{
		{Op: "create_file", Path: "pkg/core.py", Content: "def base():\n    return 1\n"},
	}
	res, err := cg.ApplyPlan(ops, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pkg", "core.py")); !os.IsNotExist(err) {
		t.Fatal("dry run must not write files")
	}

	res, err = cg.ApplyPlan(ops, false)
	if err != nil {
		t.Fatal(err)
	}
	if res["applied"] != 1 {
		t.Fatalf("expected 1 applied, got %v", res)
	}

	ops2 := []codegen.PlanOperation{
		{Op: "replace_text", Path: "pkg/core.py", OldText: "return 1", NewText: "return 2"},
		{Op: "insert_after", Path: "pkg/core.py", Anchor: "return 2", Content: "\n# touched\n"},
	}
	if _, err := cg.ApplyPlan(ops2, false); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "pkg", "core.py"))
	if !strings.Contains(string(data), "return 2") || !strings.Contains(string(data), "# touched") {
		t.Fatalf("edits not applied:\n%s", data)
	}
}

func TestApplyPlanRollsBackOnFailure(t *testing.T) {
	dir := t.TempDir()
	cg := codegen.NewCodegen(dir)

	before := "def keep():\n    return 1\n"
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "keep.py"), []byte(before), 0644); err != nil {
		t.Fatal(err)
	}

	ops := []codegen.PlanOperation{
		{Op: "replace_text", Path: "pkg/keep.py", OldText: "return 1", NewText: "return 2"},
		{Op: "create_file", Path: "pkg/extra.py", Content: "x = 1\n"},
		{Op: "replace_text", Path: "pkg/keep.py", OldText: "missing-anchor", NewText: "boom"},
	}
	if _, err := cg.ApplyPlan(ops, false); err == nil {
		t.Fatal("expected failure on missing old_text")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "pkg", "keep.py"))
	if string(data) != before {
		t.Fatalf("expected rollback of keep.py, got:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "pkg", "extra.py")); !os.IsNotExist(err) {
		t.Fatal("expected rollback to remove extra.py")
	}
}

func TestApplyPlanRejectsAmbiguousReplace(t *testing.T) {
	dir := t.TempDir()
	cg := codegen.NewCodegen(dir)

	dup := "x = 1\nx = 1\n"
	if err := os.WriteFile(filepath.Join(dir, "d.py"), []byte(dup), 0644); err != nil {
		t.Fatal(err)
	}
	ops := []codegen.PlanOperation{
		{Op: "replace_text", Path: "d.py", OldText: "x = 1", NewText: "x = 2"},
	}
	if _, err := cg.ApplyPlan(ops, false); err == nil {
		t.Fatal("expected ambiguity error without replace_all")
	}
	ops[0].ReplaceAll = true
	if _, err := cg.ApplyPlan(ops, false); err != nil {
		t.Fatalf("expected replace_all to succeed: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "d.py"))
	if strings.Contains(string(data), "x = 1") {
		t.Fatalf("expected all occurrences replaced:\n%s", data)
	}
}
