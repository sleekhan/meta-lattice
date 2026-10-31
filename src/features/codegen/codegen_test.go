package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePathRejectsSymlinkEscapeThroughMissingDir(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	cg := NewCodegen(root)

	// The intermediate directory does not exist yet: MkdirAll would follow the
	// symlink and create the file outside the workspace.
	if _, _, err := cg.resolvePath("link/nested/escape.py"); err == nil {
		t.Fatal("expected symlink escape through a missing directory to be rejected")
	}
	if _, err := cg.ScaffoldModule(ScaffoldOptions{FilePath: "link/nested/escape.py", Kind: "class"}); err == nil {
		t.Fatal("scaffold should refuse to write through an escaping symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "nested", "escape.py")); err == nil {
		t.Fatal("scaffold wrote a file outside the workspace")
	}

	// Brand-new directories inside the workspace must still be allowed.
	if _, _, err := cg.resolvePath("pkg/new/module.py"); err != nil {
		t.Fatalf("nested new directory inside the workspace should be allowed: %v", err)
	}
}

func TestResolvePathAllowsSymlinkInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	internal := filepath.Join(root, "real")
	if err := os.MkdirAll(internal, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(internal, filepath.Join(root, "alias")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	cg := NewCodegen(root)
	if _, _, err := cg.resolvePath("alias/module.py"); err != nil {
		t.Fatalf("symlink pointing inside the workspace should be allowed: %v", err)
	}
}
