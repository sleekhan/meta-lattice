package tests

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meta-lattice/src/config"
	"meta-lattice/src/features/blast"
	"meta-lattice/src/indexer"
	"meta-lattice/src/installer"
	"meta-lattice/src/mcp"
	"meta-lattice/src/models"
	"meta-lattice/src/storage"
)

// A Go function without a body (e.g. assembly-backed) used to panic the parser.
func TestGoParserBodylessFunc(t *testing.T) {
	res := indexer.ParseGoFile("a.go", "package a\n\nfunc f(x int) int\n", "sha", "a")
	if res == nil || len(res.SymbolNodes) != 1 {
		t.Fatalf("expected 1 symbol for bodyless func, got %+v", res)
	}
}

// Go package imports must produce file-level IMPORTS edges via go.mod.
func TestGoImportEdgesResolveViaGoMod(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src", "util"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n\ngo 1.22\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "src", "util", "u.go"), []byte("package util\n\nfunc Do() {}\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main\n\nimport (\n\t\"fmt\"\n\t\"example.com/app/src/util\"\n)\n\nfunc main() { fmt.Println(); util.Do() }\n"), 0644)

	cfg := config.NewLatticeConfig(dir)
	db := storage.NewGraphStorage(cfg.DBPath)
	indexer.NewCacheEngine(cfg, db).Sync(false)

	edges := db.GetOutgoingEdges("file:src/main.go", string(models.EdgeImports))
	if len(edges) != 1 || edges[0].TargetID != "file:src/util/u.go" {
		t.Fatalf("expected single IMPORTS edge to src/util/u.go, got %+v", edges)
	}
}

// If knowledge.lattice disappears while cache_state.json remains, Sync must rebuild.
func TestSyncRebuildsWhenGraphDBIsLost(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.py"), []byte("def a():\n    return 1\n"), 0644)

	cfg := config.NewLatticeConfig(dir)
	db := storage.NewGraphStorage(cfg.DBPath)
	indexer.NewCacheEngine(cfg, db).Sync(false)

	_ = os.Remove(cfg.DBPath)
	db2 := storage.NewGraphStorage(cfg.DBPath)
	indexer.NewCacheEngine(cfg, db2).Sync(false)

	if db2.CountNodesAndEdges()["total_nodes"] == 0 {
		t.Fatal("expected graph to be rebuilt after DB loss")
	}
}

// Installing twice must not duplicate the block, and uninstall must remove all of it.
func TestCodexInstallIsIdempotentAndUninstallClean(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, ".codex", "config.toml")
	_ = os.MkdirAll(filepath.Dir(cfgPath), 0755)
	_ = os.WriteFile(cfgPath, []byte("[other]\nkey = 1\n"), 0644)

	inst := &installer.Installer{BinaryPath: "/x/meta-lattice", HomeDir: home}
	_ = inst.InstallCodex()
	_ = inst.InstallCodex()

	data, _ := os.ReadFile(cfgPath)
	if n := strings.Count(string(data), "[mcp_servers.meta-lattice]"); n != 1 {
		t.Fatalf("expected exactly 1 meta-lattice block, got %d:\n%s", n, data)
	}
	if !strings.Contains(string(data), "[other]") {
		t.Fatalf("existing config was lost:\n%s", data)
	}

	_ = inst.UninstallCodex()
	data, _ = os.ReadFile(cfgPath)
	s := string(data)
	if strings.Contains(s, "meta-lattice") || strings.Contains(s, `args = ["mcp"]`) || strings.Contains(s, "enabled = true") {
		t.Fatalf("uninstall left residue:\n%s", s)
	}
	if !strings.Contains(s, "[other]") {
		t.Fatalf("uninstall removed unrelated config:\n%s", s)
	}
}

// Installing Claude Code twice must be idempotent, deploy slash commands, and uninstall cleanly.
func TestClaudeCodeInstallIsIdempotentAndUninstallClean(t *testing.T) {
	home := t.TempDir()
	claudePath := filepath.Join(home, ".claude.json")
	_ = os.WriteFile(claudePath, []byte(`{"otherSetting": true, "mcpServers": {"existing": {"command": "echo"}}}`), 0644)

	inst := &installer.Installer{BinaryPath: "/x/meta-lattice", HomeDir: home}
	_ = inst.InstallClaudeCode()
	_ = inst.InstallClaudeCode()

	data, _ := os.ReadFile(claudePath)
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("corrupt json written: %v", err)
	}
	mcpServers := root["mcpServers"].(map[string]any)
	if _, ok := mcpServers["meta-lattice"]; !ok {
		t.Fatalf("meta-lattice missing from mcpServers")
	}
	if _, ok := mcpServers["existing"]; !ok {
		t.Fatalf("existing mcpServer was wiped")
	}
	if root["otherSetting"] != true {
		t.Fatalf("unrelated settings were wiped")
	}

	// Verify slash commands
	commandsDir := filepath.Join(home, ".claude", "commands")
	for _, cmd := range []string{"zoom", "audit", "blast", "sync"} {
		if _, err := os.Stat(filepath.Join(commandsDir, cmd+".md")); err != nil {
			t.Fatalf("missing slash command %s: %v", cmd, err)
		}
	}

	// Uninstall
	_ = inst.UninstallClaudeCode()
	data, _ = os.ReadFile(claudePath)
	var uninstalledRoot map[string]any
	_ = json.Unmarshal(data, &uninstalledRoot)
	uninstalledMCP := uninstalledRoot["mcpServers"].(map[string]any)
	if _, ok := uninstalledMCP["meta-lattice"]; ok {
		t.Fatalf("meta-lattice still in mcpServers after uninstall")
	}
	if _, ok := uninstalledMCP["existing"]; !ok {
		t.Fatalf("existing mcpServer lost after uninstall")
	}
	for _, cmd := range []string{"zoom", "audit", "blast", "sync"} {
		if _, err := os.Stat(filepath.Join(commandsDir, cmd+".md")); !os.IsNotExist(err) {
			t.Fatalf("command file %s was not removed after uninstall", cmd)
		}
	}
}

// Unknown notifications must be silently ignored (no error reply).
func TestMCPIgnoresUnknownNotifications(t *testing.T) {
	server := mcp.NewMCPServer(t.TempDir())
	in := strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{}}` + "\n")
	var out bytes.Buffer
	_ = server.Serve(in, &out)
	if out.Len() != 0 {
		t.Fatalf("expected no response to a notification, got %s", out.String())
	}
}

// Directory-style ignore names (out, bin, ref...) must not hide files of that name.
func TestIgnorePatternsOnlyPruneDirectories(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "out"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, "node_modules", "x"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "out.py"), []byte("def a(): pass\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "ref.go"), []byte("package r\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "out", "gen.py"), []byte("def g(): pass\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "node_modules", "x", "i.js"), []byte("function f(){}\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "app.min.js"), []byte("function m(){}\n"), 0644)

	cfg := config.NewLatticeConfig(dir)
	files := indexer.NewCacheEngine(cfg, storage.NewGraphStorage(cfg.DBPath)).ScanFiles()

	for _, want := range []string{"out.py", "ref.go"} {
		if _, ok := files[want]; !ok {
			t.Errorf("expected %s to be indexed, got %v", want, files)
		}
	}
	for _, unwanted := range []string{"out/gen.py", "node_modules/x/i.js", "app.min.js"} {
		if _, ok := files[unwanted]; ok {
			t.Errorf("expected %s to be ignored", unwanted)
		}
	}
}

// camelCase identifiers must be searchable by their sub-words.
func TestSearchSplitsCamelCase(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc ZoomSymbol() {}\n\nfunc parseHTTPServer() {}\n"), 0644)
	cfg := config.NewLatticeConfig(dir)
	db := storage.NewGraphStorage(cfg.DBPath)
	indexer.NewCacheEngine(cfg, db).Sync(false)

	for _, q := range []string{"symbol", "http", "server", "zoom"} {
		if len(db.FTSSearch(q, 5)) == 0 {
			t.Errorf("expected search %q to match a camelCase identifier", q)
		}
	}
}

// A corrupt knowledge.lattice must be set aside and rebuilt by the next sync.
func TestCorruptGraphDBIsRecovered(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.py"), []byte("def a():\n    return 1\n"), 0644)
	cfg := config.NewLatticeConfig(dir)
	indexer.NewCacheEngine(cfg, storage.NewGraphStorage(cfg.DBPath)).Sync(false)

	_ = os.WriteFile(cfg.DBPath, []byte("{not json"), 0644)
	db := storage.NewGraphStorage(cfg.DBPath)
	indexer.NewCacheEngine(cfg, db).Sync(false)

	if db.CountNodesAndEdges()["total_nodes"] == 0 {
		t.Fatal("expected graph rebuilt after corruption")
	}
	if _, err := os.Stat(cfg.DBPath + ".corrupt"); err != nil {
		t.Fatalf("expected corrupt file to be preserved: %v", err)
	}
}

// Query tools must lazily index instead of answering "not found" on an empty graph.
func TestMCPLazyIndexesForQueryTools(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "m.py"), []byte("def lazy_target():\n    return 1\n"), 0644)
	server := mcp.NewMCPServer(dir)
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"zoom_search","arguments":{"query":"lazy_target"}}}` + "\n")
	var out bytes.Buffer
	_ = server.Serve(in, &out)
	if !strings.Contains(out.String(), "lazy_target") || strings.Contains(out.String(), `\"total_matches\": 0`) {
		t.Fatalf("expected zoom_search to find lazy_target, got %s", out.String())
	}
}

// Blast radius must report the real domain of L3 callers (taken from their file)
// instead of always falling back to "root".
func TestBlastReportsCallerDomains(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src", "core"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, "src", "billing"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "src", "core", "util.py"), []byte("def shared():\n    return 1\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "src", "billing", "pay.py"), []byte("from src.core.util import shared\n\ndef charge():\n    return shared()\n"), 0644)

	cfg := config.NewLatticeConfig(dir)
	db := storage.NewGraphStorage(cfg.DBPath)
	indexer.NewCacheEngine(cfg, db).Sync(false)

	res := blast.NewBlastRadiusEstimator(db).EstimateBlastRadius("shared", "signature", 3)
	points, _ := res["top_10_breaking_points"].([]map[string]any)
	found := false
	for _, p := range points {
		if p["symbol_name"] == "charge" {
			found = true
			if p["domain"] != "billing" {
				t.Fatalf("expected caller domain billing, got %v", p["domain"])
			}
		}
	}
	if !found {
		t.Fatalf("expected charge among breaking points, got %+v", points)
	}
}
