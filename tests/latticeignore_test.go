package tests

import (
	"os"
	"path/filepath"
	"testing"

	"meta-lattice/src/config"
	"meta-lattice/src/indexer"
)

// .latticeignore in the workspace root must extend the built-in ignore
// patterns (blank lines and '#' comments are skipped, duplicates collapse).
func TestLatticeIgnoreExtendsDefaults(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "lattice-ignore-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ignore := "# generated snapshots\n\ngenerated\nskip_me.py\ngenerated\n"
	if err := os.WriteFile(filepath.Join(tmpDir, ".latticeignore"), []byte(ignore), 0644); err != nil {
		t.Fatal(err)
	}

	write := func(rel, body string) {
		p := filepath.Join(tmpDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("pkg/keep.py", "def keep():\n    return 1\n")
	write("pkg/skip_me.py", "def skip():\n    return 2\n")
	write("generated/gen.py", "def gen():\n    return 3\n")

	cfg := config.NewLatticeConfig(tmpDir)

	found := map[string]bool{}
	for _, p := range cfg.IgnorePatterns {
		found[p] = true
	}
	for _, want := range []string{"generated", "skip_me.py", ".git", "node_modules"} {
		if !found[want] {
			t.Fatalf("expected ignore pattern %q in %v", want, cfg.IgnorePatterns)
		}
	}

	engine := indexer.NewCacheEngine(cfg, nil)
	files := engine.ScanFiles()
	if _, ok := files["pkg/keep.py"]; !ok {
		t.Fatalf("expected pkg/keep.py to be scanned, got %v", files)
	}
	if _, ok := files["pkg/skip_me.py"]; ok {
		t.Fatalf("expected pkg/skip_me.py to be ignored, got %v", files)
	}
	if _, ok := files["generated/gen.py"]; ok {
		t.Fatalf("expected generated/gen.py to be ignored, got %v", files)
	}
}
