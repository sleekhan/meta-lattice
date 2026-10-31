package tests

import (
	"os"
	"path/filepath"
	"testing"

	"meta-lattice/src/installer"
)

func TestClaudeInstallerPreservesMalformedConfig(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, ".claude.json")
	original := []byte(`{not-json`)
	if err := os.WriteFile(configPath, original, 0644); err != nil {
		t.Fatal(err)
	}
	inst := installer.NewInstaller("meta-lattice")
	inst.HomeDir = home
	if err := inst.InstallClaudeCode(); err == nil {
		t.Fatal("expected malformed config to be rejected")
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("malformed config changed: %q", got)
	}
}
