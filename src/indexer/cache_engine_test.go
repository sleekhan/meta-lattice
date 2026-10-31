package indexer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meta-lattice/src/config"
	"meta-lattice/src/storage"
)

func TestSyncCompletesTargetedUpdateAfterWorkspaceDrift(t *testing.T) {
	root := t.TempDir()
	write := func(name, source string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.py", "def first():\n    return 1\n")
	write("b.py", "def second():\n    return 2\n")

	cfg := config.NewLatticeConfig(root)
	db := storage.NewGraphStorage(cfg.DBPath)
	engine := NewCacheEngine(cfg, db)
	if stats := engine.Sync(false); stats.Error != "" {
		t.Fatalf("initial full sync failed: %s", stats.Error)
	}
	if !engine.IsFullyIndexed() {
		t.Fatal("initial sync should mark the index complete")
	}

	write("a.py", "def first():\n    return 10\n")
	write("c.py", "def third():\n    return 3\n")
	if _, err := engine.SyncTargetFile("a.py"); err != nil {
		t.Fatal(err)
	}
	if engine.IsFullyIndexed() {
		t.Fatal("targeted sync should invalidate the full-index marker")
	}

	stats := engine.Sync(false)
	if stats.Error != "" {
		t.Fatalf("follow-up sync failed: %s", stats.Error)
	}
	if !engine.IsFullyIndexed() {
		t.Fatal("follow-up sync should restore the full-index marker")
	}
	if _, ok := db.GetNode("file:c.py"); !ok {
		t.Fatal("follow-up sync did not index the unedited file")
	}
	// The follow-up must stay incremental: only the new file is parsed, the
	// file already refreshed by the targeted sync is a cache hit.
	if stats.AddedCount != 1 || stats.ModifiedCount != 0 || stats.CachedCount != 2 {
		t.Fatalf("follow-up sync was not incremental: added=%d modified=%d cached=%d",
			stats.AddedCount, stats.ModifiedCount, stats.CachedCount)
	}
}

func TestRefreshStateSeesExternalCacheUpdate(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.py"), []byte("def first():\n    return 1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := config.NewLatticeConfig(root)
	engine := NewCacheEngine(cfg, storage.NewGraphStorage(cfg.DBPath))
	if stats := engine.Sync(false); stats.Error != "" {
		t.Fatalf("initial sync failed: %s", stats.Error)
	}
	if engine.RefreshState() {
		t.Fatal("no external write happened yet, RefreshState should be a no-op")
	}

	// Simulate the post-tool hook running in another process: it rewrites the
	// cache state with FullSync=false and a new mtime/size.
	data, err := os.ReadFile(cfg.CacheStateFile)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["full_sync"] = false
	raw["last_sync"] = float64(1)
	updated, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.CacheStateFile, updated, 0644); err != nil {
		t.Fatal(err)
	}

	if !engine.RefreshState() {
		t.Fatal("RefreshState did not detect the externally written state")
	}
	if engine.IsFullyIndexed() {
		t.Fatal("RefreshState kept the stale full-index marker")
	}
}

func TestSyncTargetFileRejectsPathsOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.py"), []byte("def first():\n    return 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.py")
	if err := os.WriteFile(secret, []byte("def secret_fn():\n    return 42\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := config.NewLatticeConfig(root)
	db := storage.NewGraphStorage(cfg.DBPath)
	engine := NewCacheEngine(cfg, db)
	if stats := engine.Sync(false); stats.Error != "" {
		t.Fatalf("initial sync failed: %s", stats.Error)
	}

	for _, target := range []string{
		secret,
		filepath.Join(root, "..", filepath.Base(outside), "secret.py"),
		"../" + filepath.Base(outside) + "/secret.py",
	} {
		stats, err := engine.SyncTargetFile(target)
		if err != nil {
			t.Fatalf("SyncTargetFile(%q) failed: %v", target, err)
		}
		if stats.AddedCount != 0 || stats.ModifiedCount != 0 {
			t.Fatalf("SyncTargetFile(%q) indexed an outside file: %+v", target, stats)
		}
	}

	for id := range db.AllNodes() {
		if strings.Contains(id, "secret") || strings.HasPrefix(id, "domain:..") {
			t.Fatalf("outside file leaked into the graph: %s", id)
		}
	}
	if !engine.IsFullyIndexed() {
		t.Fatal("a rejected targeted sync must not change the index state")
	}
}

func TestSyncTargetFileAcceptsAbsolutePathInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	cfg := config.NewLatticeConfig(root)
	db := storage.NewGraphStorage(cfg.DBPath)
	engine := NewCacheEngine(cfg, db)
	engine.Sync(false)

	// Created after the initial sync so the targeted sync has real work to do.
	target := filepath.Join(root, "pkg", "a.py")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("def first():\n    return 1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	stats, err := engine.SyncTargetFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if stats.AddedCount != 1 {
		t.Fatalf("expected the absolute path inside the workspace to be indexed, got %+v", stats)
	}
	if _, ok := db.GetNode("file:pkg/a.py"); !ok {
		t.Fatal("expected file:pkg/a.py in the graph")
	}
}

func TestSyncWithoutChangesDoesNotRewriteGraph(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pkg/a.py", "pkg/b.py", "pkg/c.py"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("def fn():\n    return 1\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.NewLatticeConfig(root)
	db := storage.NewGraphStorage(cfg.DBPath)
	engine := NewCacheEngine(cfg, db)
	if stats := engine.Sync(false); stats.Error != "" {
		t.Fatalf("initial sync failed: %s", stats.Error)
	}

	before, err := os.Stat(db.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}

	stats := engine.Sync(false)
	if stats.AddedCount != 0 || stats.ModifiedCount != 0 || stats.DeletedCount != 0 {
		t.Fatalf("expected a clean sync, got %+v", stats)
	}
	if db.IsDirty() {
		t.Fatal("a clean sync must not leave the graph dirty")
	}
	after, err := os.Stat(db.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("a clean sync rewrote the graph database")
	}
}

func TestSyncRebuildsWhenGraphCountsDrift(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.py", "b.py"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("def fn():\n    return 1\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.NewLatticeConfig(root)
	db := storage.NewGraphStorage(cfg.DBPath)
	engine := NewCacheEngine(cfg, db)
	if stats := engine.Sync(false); stats.Error != "" {
		t.Fatalf("initial sync failed: %s", stats.Error)
	}
	if engine.state.GraphNodes == 0 || engine.state.GraphEdges == 0 {
		t.Fatalf("sync did not record the graph size: %+v", engine.state)
	}

	// Simulate a graph that no longer matches the cache state (for example a
	// save that was interrupted): a delta would be applied to the wrong base.
	db.DeleteNode("file:b.py")

	stats := engine.Sync(false)
	if stats.Error != "" {
		t.Fatalf("drift sync failed: %s", stats.Error)
	}
	if stats.AddedCount != 2 {
		t.Fatalf("drift should force a full re-parse, got %+v", stats)
	}
	if _, ok := db.GetNode("file:b.py"); !ok {
		t.Fatal("the drifted graph was not rebuilt")
	}

	// Once rebuilt the counts match again, so the next sync stays cheap.
	again := engine.Sync(false)
	if again.AddedCount != 0 || again.ModifiedCount != 0 {
		t.Fatalf("sync after recovery was not incremental: %+v", again)
	}
}

func TestSyncRecoversAfterGraphFileLoss(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.py", "b.py"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("def fn():\n    return 1\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.NewLatticeConfig(root)
	db := storage.NewGraphStorage(cfg.DBPath)
	engine := NewCacheEngine(cfg, db)
	if stats := engine.Sync(false); stats.Error != "" {
		t.Fatalf("initial sync failed: %s", stats.Error)
	}

	// The graph file disappears while the cache state survives (deleted
	// .lattice directory, failed rename, ...). A targeted sync then rebuilds a
	// partial graph from one file; the next full sync must notice that the
	// graph is missing a module instead of trusting the recorded counts.
	if err := os.Remove(db.DatabasePath()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.py"), []byte("def fn():\n    return 2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	partial := NewCacheEngine(cfg, storage.NewGraphStorage(cfg.DBPath))
	if _, err := partial.SyncTargetFile("a.py"); err != nil {
		t.Fatal(err)
	}
	if counts := partial.db.CountNodesAndEdges(); counts["l1_modules"] != 1 {
		t.Fatalf("expected the targeted sync to persist a partial graph, got %v", counts)
	}

	stats := partial.Sync(false)
	if stats.Error != "" {
		t.Fatalf("recovery sync failed: %s", stats.Error)
	}
	if stats.AddedCount != 2 {
		t.Fatalf("expected a full re-parse after the graph was lost, got %+v", stats)
	}
	counts := partial.db.CountNodesAndEdges()
	if counts["l1_modules"] != 2 {
		t.Fatalf("graph was not rebuilt: %v", counts)
	}
}

func TestSyncUpdatesDomainModuleCount(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte("def fn():\n    return 1\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("pkg/a.py")
	writeFile("pkg/b.py")

	cfg := config.NewLatticeConfig(root)
	db := storage.NewGraphStorage(cfg.DBPath)
	engine := NewCacheEngine(cfg, db)
	engine.Sync(false)

	if node, ok := db.GetNode("domain:pkg"); !ok {
		t.Fatal("expected domain:pkg")
	} else if count, _ := node["module_count"].(int); count != 2 {
		t.Fatalf("expected module_count=2, got %v", node["module_count"])
	}

	writeFile("pkg/c.py")
	engine.Sync(false)
	if node, ok := db.GetNode("domain:pkg"); !ok {
		t.Fatal("expected domain:pkg")
	} else if count, _ := node["module_count"].(int); count != 3 {
		t.Fatalf("expected module_count=3 after adding a file, got %v", node["module_count"])
	}

	if err := os.Remove(filepath.Join(root, "pkg", "a.py")); err != nil {
		t.Fatal(err)
	}
	engine.Sync(false)
	if node, ok := db.GetNode("domain:pkg"); !ok {
		t.Fatal("expected domain:pkg")
	} else if count, _ := node["module_count"].(int); count != 2 {
		t.Fatalf("expected module_count=2 after deleting a file, got %v", node["module_count"])
	}

	// The post-tool hook path must keep the same counter accurate.
	writeFile("pkg/d.py")
	if _, err := engine.SyncTargetFile(filepath.Join(root, "pkg", "d.py")); err != nil {
		t.Fatal(err)
	}
	if node, ok := db.GetNode("domain:pkg"); !ok {
		t.Fatal("expected domain:pkg")
	} else if count, _ := node["module_count"].(int); count != 3 {
		t.Fatalf("expected module_count=3 after a hook-added file, got %v", node["module_count"])
	}

	if err := os.Remove(filepath.Join(root, "pkg", "d.py")); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.SyncTargetFile(filepath.Join(root, "pkg", "d.py")); err != nil {
		t.Fatal(err)
	}
	if node, ok := db.GetNode("domain:pkg"); !ok {
		t.Fatal("expected domain:pkg")
	} else if count, _ := node["module_count"].(int); count != 2 {
		t.Fatalf("expected module_count=2 after a hook-deleted file, got %v", node["module_count"])
	}

	// Deleting the remaining files must drop the domain entirely.
	if err := os.RemoveAll(filepath.Join(root, "pkg")); err != nil {
		t.Fatal(err)
	}
	engine.Sync(false)
	if _, ok := db.GetNode("domain:pkg"); ok {
		t.Fatal("expected domain:pkg to be removed with its last file")
	}
}

func TestReadGitHeadResolvesPackedAndLooseRefs(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	other := "89abcdef0123456789abcdef0123456789abcdef"

	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "refs", "heads", "main"), []byte(sha+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := readGitHead(root); got != sha {
		t.Fatalf("loose ref: expected %s, got %q", sha, got)
	}

	// After a gc the ref only exists in packed-refs.
	if err := os.Remove(filepath.Join(gitDir, "refs", "heads", "main")); err != nil {
		t.Fatal(err)
	}
	packed := "# pack-refs with: peeled fully-peeled sorted \n" + other + " refs/heads/main\n"
	if err := os.WriteFile(filepath.Join(gitDir, "packed-refs"), []byte(packed), 0644); err != nil {
		t.Fatal(err)
	}
	if got := readGitHead(root); got != other {
		t.Fatalf("packed ref: expected %s, got %q", other, got)
	}

	// A detached HEAD carries the commit directly.
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(sha+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := readGitHead(root); got != sha {
		t.Fatalf("detached head: expected %s, got %q", sha, got)
	}
}

func TestReadAllGoModulesFindsNestedModules(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod":              "module example.com/root\n",
		"main.go":             "package main\n",
		"services/api/go.mod": "module example.com/api\n",
		"services/api/a.go":   "package api\n",
	}

	cfg := config.NewLatticeConfig(root)
	engine := NewCacheEngine(cfg, storage.NewGraphStorage(cfg.DBPath))
	currentFiles := make(map[string]string, len(files))
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		currentFiles[rel] = abs
	}

	modules := engine.readAllGoModules(currentFiles)
	if modules["example.com/root"] != "" {
		t.Fatalf("expected the root module to map to \"\", got %q", modules["example.com/root"])
	}
	if modules["example.com/api"] != "services/api" {
		t.Fatalf("expected the nested module to map to services/api, got %q", modules["example.com/api"])
	}
}
