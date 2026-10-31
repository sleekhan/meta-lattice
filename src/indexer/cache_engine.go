package indexer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"meta-lattice/src/config"
	"meta-lattice/src/models"
	"meta-lattice/src/storage"
)

// CacheVersion is bumped whenever parsing/edge-resolution semantics change so
// that stale caches are re-indexed automatically. 1.4: Additional language
// support (Kotlin, C#, Swift, PHP) with dotted/backslash import resolution.
const CacheVersion = "1.4"

var SupportedExtensions = map[string]string{
	".py":     "python",
	".ts":     "typescript",
	".tsx":    "typescript",
	".js":     "javascript",
	".jsx":    "javascript",
	".mjs":    "javascript",
	".cjs":    "javascript",
	".vue":    "vue",
	".svelte": "svelte",
	".go":     "go",
	".java":   "java",
	".kt":     "kotlin",
	".kts":    "kotlin",
	".cs":     "csharp",
	".swift":  "swift",
	".php":    "php",
	".rs":     "rust",
	".c":      "c",
	".cpp":    "cpp",
	".cc":     "cpp",
	".cxx":    "cpp",
	".h":      "c",
	".hpp":    "cpp",
}

type CacheFileInfo struct {
	SHA         string       `json:"sha"`
	Mtime       int64        `json:"mtime"`
	Size        int64        `json:"size"`
	Domain      string       `json:"domain"`
	NodeIDs     []string     `json:"node_ids"`
	Exports     []string     `json:"exports"`
	Imports     []string     `json:"imports"`
	ImportSpecs []ImportSpec `json:"import_specs"`
}

type CacheState struct {
	Version   string                   `json:"version"`
	LastSync  int64                    `json:"last_sync"`
	GitCommit string                   `json:"git_commit"`
	FullSync  bool                     `json:"full_sync"`
	Files     map[string]CacheFileInfo `json:"files"`
}

type SyncStats struct {
	ElapsedMS     int            `json:"elapsed_ms"`
	TotalFiles    int            `json:"total_files"`
	AddedCount    int            `json:"added_count"`
	ModifiedCount int            `json:"modified_count"`
	DeletedCount  int            `json:"deleted_count"`
	CachedCount   int            `json:"cached_count"`
	Nodes         map[string]int `json:"nodes"`
	GitCommit     string         `json:"git_commit"`
	Error         string         `json:"error,omitempty"`
}

// stateSignature identifies the on-disk cache state file so a long-running
// process (e.g. the MCP server) can notice that another process such as the
// post-tool hook rewrote it.
type stateSignature struct {
	modTime time.Time
	size    int64
}

type CacheEngine struct {
	config        *config.LatticeConfig
	db            *storage.GraphStorage
	workspaceRoot string
	state         CacheState
	stateSig      stateSignature
	// tsPathsCache avoids re-reading the TypeScript config on every import
	// resolution; tsPathsStamp is the mtime/size fingerprint it was built from.
	tsPathsCache map[string]string
	tsPathsStamp string
	mu           sync.Mutex
}

func NewCacheEngine(cfg *config.LatticeConfig, db *storage.GraphStorage) *CacheEngine {
	engine := &CacheEngine{
		config:        cfg,
		db:            db,
		workspaceRoot: cfg.WorkspaceRoot,
		state: CacheState{
			Version: CacheVersion,
			Files:   make(map[string]CacheFileInfo),
		},
	}
	engine.loadCacheState()
	return engine
}

func (e *CacheEngine) IsFullyIndexed() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state.FullSync
}

// RefreshState reloads the persisted cache state when the state file changed
// since it was last read or written by this engine. It reports whether a
// reload happened. Long-running processes must call this before deciding
// whether to sync, otherwise a targeted update performed by another process
// (the post-tool hook) is invisible to them.
func (e *CacheEngine) RefreshState() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	fi, err := os.Stat(e.config.CacheStateFile)
	if err != nil {
		return false
	}
	if fi.ModTime().Equal(e.stateSig.modTime) && fi.Size() == e.stateSig.size {
		return false
	}
	e.loadCacheState()
	return true
}

func (e *CacheEngine) loadCacheState() {
	data, err := os.ReadFile(e.config.CacheStateFile)
	if err != nil {
		return
	}

	var loaded CacheState
	if err := json.Unmarshal(data, &loaded); err != nil {
		return
	}

	if loaded.Files == nil {
		loaded.Files = make(map[string]CacheFileInfo)
	}

	if loaded.Version != CacheVersion {
		// Stale cache version: force re-parse of all files by clearing hashes/mtimes,
		// but preserve file list so deletions remain detectable
		for rel, info := range loaded.Files {
			info.SHA = ""
			info.Mtime = 0
			info.Size = 0
			loaded.Files[rel] = info
		}
		loaded.Version = CacheVersion
	}

	e.state = loaded
	if fi, statErr := os.Stat(e.config.CacheStateFile); statErr == nil {
		e.stateSig = stateSignature{modTime: fi.ModTime(), size: fi.Size()}
	}
}

func (e *CacheEngine) saveCacheState() error {
	if err := e.config.EnsureDirectories(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(e.state, "", "  ")
	if err != nil {
		return err
	}
	tmpFile := e.config.CacheStateFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmpFile, e.config.CacheStateFile); err != nil {
		return err
	}
	// Remember our own write so RefreshState does not reload it needlessly.
	if fi, err := os.Stat(e.config.CacheStateFile); err == nil {
		e.stateSig = stateSignature{modTime: fi.ModTime(), size: fi.Size()}
	}
	return nil
}

// ComputeFileSHABytes returns the hex SHA-256 of the given content.
func ComputeFileSHABytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func ComputeFileSHA(filePath string) string {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return ""
	}
	return ComputeFileSHABytes(data)
}

func (e *CacheEngine) shouldIgnore(relPath string, isDir bool) bool {
	cleanRel := filepath.ToSlash(relPath)
	base := path.Base(cleanRel)

	for _, pattern := range e.config.IgnorePatterns {
		// Directory-style names ("out", "bin", "target", ".git", ...) must only
		// prune directories; otherwise legitimate files such as out.py or ref.go
		// would silently disappear from the index.
		if !isDir && !isFilePattern(pattern) {
			continue
		}
		// Pattern exact match or fnmatch
		if match, _ := filepath.Match(pattern, base); match {
			return true
		}
		if match, _ := filepath.Match(pattern, cleanRel); match {
			return true
		}
		if isDir && strings.Contains("/"+cleanRel+"/", "/"+pattern+"/") {
			return true
		}
	}
	return false
}

// isFilePattern reports whether an ignore pattern targets files (globs such as
// "*.map" or concrete file names such as "package-lock.json") rather than
// directory names (".git", "node_modules", "out").
func isFilePattern(pattern string) bool {
	if strings.ContainsAny(pattern, "*?[") {
		return true
	}
	return strings.Contains(pattern, ".") && !strings.HasPrefix(pattern, ".")
}

func (e *CacheEngine) ScanFiles() map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.scanFiles()
}

func (e *CacheEngine) scanFiles() map[string]string {
	result := make(map[string]string)

	_ = filepath.Walk(e.workspaceRoot, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(e.workspaceRoot, p)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		cleanRel := filepath.ToSlash(rel)

		if info.IsDir() {
			if e.shouldIgnore(cleanRel, true) {
				return filepath.SkipDir
			}
			return nil
		}

		if e.shouldIgnore(cleanRel, false) {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(cleanRel))
		if _, ok := SupportedExtensions[ext]; ok {
			result[cleanRel] = p
		}
		return nil
	})

	return result
}

func (e *CacheEngine) getGitCommit() string {
	if sha := readGitHead(e.workspaceRoot); sha != "" {
		return sha
	}
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = e.workspaceRoot
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// readGitHead resolves HEAD without spawning a subprocess, which the hook pays
// for on every edit. It understands a plain .git directory and the "gitdir:"
// indirection used by worktrees and submodules; anything unexpected falls back
// to the git CLI.
func readGitHead(root string) string {
	gitDir := filepath.Join(root, ".git")
	if fi, err := os.Stat(gitDir); err == nil && !fi.IsDir() {
		data, err := os.ReadFile(gitDir)
		if err != nil {
			return ""
		}
		line := strings.TrimSpace(string(data))
		if !strings.HasPrefix(line, "gitdir:") {
			return ""
		}
		gitDir = strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(root, gitDir)
		}
	}

	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(string(head))
	if !strings.HasPrefix(ref, "ref:") {
		if isHexSHA(ref) {
			return ref
		}
		return ""
	}

	refPath := strings.TrimSpace(strings.TrimPrefix(ref, "ref:"))
	if data, err := os.ReadFile(filepath.Join(gitDir, filepath.FromSlash(refPath))); err == nil {
		if sha := strings.TrimSpace(string(data)); isHexSHA(sha) {
			return sha
		}
	}

	// The ref may live in packed-refs (fresh clones, after gc).
	packed, err := os.ReadFile(filepath.Join(gitDir, "packed-refs"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(packed), "\n") {
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == refPath && isHexSHA(fields[0]) {
			return fields[0]
		}
	}
	return ""
}

func isHexSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// parseFileData parses already-read file content, so callers that also need the
// SHA-256 do not have to read the file twice.
func (e *CacheEngine) parseFileData(relPath string, absPath string, data []byte, sha string) *ParsedFileResult {
	sourceCode := string(data)
	ext := strings.ToLower(filepath.Ext(absPath))
	domain := GetDomainFromPath(relPath)

	switch ext {
	case ".go":
		return ParseGoFile(relPath, sourceCode, sha, domain)
	case ".py":
		return ParsePythonFile(relPath, sourceCode, sha, domain)
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		return ParseTSJSFile(relPath, sourceCode, sha, domain)
	case ".vue":
		return ParseVueFile(relPath, sourceCode, sha, domain)
	case ".svelte":
		return ParseSvelteFile(relPath, sourceCode, sha, domain)
	case ".java":
		return ParseJavaFile(relPath, sourceCode, sha, domain)
	case ".kt", ".kts":
		return ParseKotlinFile(relPath, sourceCode, sha, domain)
	case ".cs":
		return ParseCSharpFile(relPath, sourceCode, sha, domain)
	case ".swift":
		return ParseSwiftFile(relPath, sourceCode, sha, domain)
	case ".php":
		return ParsePHPFile(relPath, sourceCode, sha, domain)
	case ".rs":
		return ParseRustFile(relPath, sourceCode, sha, domain)
	case ".c", ".cpp", ".cc", ".cxx", ".h", ".hpp":
		return ParseCCPPFile(relPath, sourceCode, sha, domain)
	default:
		return nil
	}
}

// cacheDomain returns the domain recorded for a cached file, falling back to the
// path-derived domain for entries written by older cache versions.
func cacheDomain(rel string, info CacheFileInfo) string {
	if info.Domain != "" {
		return info.Domain
	}
	return GetDomainFromPath(rel)
}

// upsertDomain writes the L0 node for a domain with the given module count, or
// removes it once the domain no longer owns any file. Both Sync and
// SyncTargetFile go through here so the stored module_count cannot drift.
func (e *CacheEngine) upsertDomain(domain string, moduleCount int) {
	domID := "domain:" + domain
	if moduleCount == 0 {
		e.db.DeleteNode(domID)
		return
	}
	l0 := models.L0DomainNode{
		ID:          domID,
		Name:        domain,
		Path:        domain,
		Description: "Domain " + domain + " containing modules",
		ModuleCount: moduleCount,
		Level:       string(models.LevelL0),
		Labels:      []string{"L0Domain", "Domain"},
	}
	e.db.CreateNode(domID, l0.Labels, l0.ToProperties())
}

func (e *CacheEngine) Sync(force bool) SyncStats {
	e.mu.Lock()
	defer e.mu.Unlock()
	startTime := time.Now()
	currentFiles := e.scanFiles()
	cachedFiles := e.state.Files
	if cachedFiles == nil {
		cachedFiles = make(map[string]CacheFileInfo)
	}

	// Rebuild only when the persisted graph was lost while the cache still
	// claims indexed files. A targeted partial update (FullSync=false) must NOT
	// force a full re-parse: the delta scan below already detects workspace
	// drift, so re-parsing every file would only burn CPU.
	if !force && len(cachedFiles) > 0 && e.db.CountNodesAndEdges()["total_nodes"] == 0 {
		force = true
	}

	var added []string
	var modified []string
	var deleted []string
	var unchanged []string

	numWorkers := runtime.GOMAXPROCS(0)
	if numWorkers < 1 {
		numWorkers = 1
	}

	if force {
		for rel := range currentFiles {
			added = append(added, rel)
		}
		for rel := range cachedFiles {
			if _, exists := currentFiles[rel]; !exists {
				deleted = append(deleted, rel)
			}
		}
	} else {
		// Parallel change detection using worker pool
		type fileCheckResult struct {
			rel    string
			status string // "added", "modified", "unchanged"
		}

		checkJobs := make(chan string, len(currentFiles))
		checkResults := make(chan fileCheckResult, len(currentFiles))
		var checkWg sync.WaitGroup

		for w := 0; w < numWorkers; w++ {
			checkWg.Add(1)
			go func() {
				defer checkWg.Done()
				for rel := range checkJobs {
					absPath := currentFiles[rel]
					cacheItem, exists := cachedFiles[rel]
					if !exists {
						checkResults <- fileCheckResult{rel: rel, status: "added"}
						continue
					}
					fi, err := os.Stat(absPath)
					if err != nil {
						continue
					}
					mtime := fi.ModTime().UnixNano()
					size := fi.Size()
					if mtime != cacheItem.Mtime || size != cacheItem.Size {
						sha := ComputeFileSHA(absPath)
						if sha != cacheItem.SHA {
							checkResults <- fileCheckResult{rel: rel, status: "modified"}
						} else {
							checkResults <- fileCheckResult{rel: rel, status: "unchanged"}
						}
					} else {
						checkResults <- fileCheckResult{rel: rel, status: "unchanged"}
					}
				}
			}()
		}

		for rel := range currentFiles {
			checkJobs <- rel
		}
		close(checkJobs)
		checkWg.Wait()
		close(checkResults)

		for r := range checkResults {
			switch r.status {
			case "added":
				added = append(added, r.rel)
			case "modified":
				modified = append(modified, r.rel)
			case "unchanged":
				unchanged = append(unchanged, r.rel)
			}
		}

		for rel := range cachedFiles {
			if _, exists := currentFiles[rel]; !exists {
				deleted = append(deleted, rel)
			}
		}
	}

	// Step 1: Clean up deleted files. The cache state records the node IDs that
	// were created for each file, so stale nodes can be removed directly instead
	// of scanning the whole graph for every deleted file.
	deletedDomains := make(map[string]bool)
	for _, rel := range deleted {
		if info, ok := cachedFiles[rel]; ok {
			e.db.DeleteNodes(info.NodeIDs)
			deletedDomains[cacheDomain(rel, info)] = true
		} else {
			e.db.DeleteFileSubnodes(rel)
			deletedDomains[GetDomainFromPath(rel)] = true
		}
		delete(cachedFiles, rel)
	}

	// Step 2: Clean up modified files subnodes before parsing
	filesToParse := append(append([]string(nil), added...), modified...)
	for _, rel := range modified {
		if info, ok := cachedFiles[rel]; ok {
			e.db.DeleteNodes(info.NodeIDs)
		} else {
			e.db.DeleteFileSubnodes(rel)
		}
	}
	if force {
		for _, rel := range added {
			if info, ok := cachedFiles[rel]; ok {
				e.db.DeleteNodes(info.NodeIDs)
			}
		}
	}

	// Parallel parsing worker pool
	type parseJob struct {
		rel     string
		absPath string
	}
	type parseJobResult struct {
		rel   string
		sha   string
		mtime int64
		size  int64
		res   *ParsedFileResult
	}

	parseJobs := make(chan parseJob, len(filesToParse))
	parseResults := make(chan parseJobResult, len(filesToParse))
	var parseWg sync.WaitGroup

	for w := 0; w < numWorkers; w++ {
		parseWg.Add(1)
		go func() {
			defer parseWg.Done()
			for job := range parseJobs {
				data, err := os.ReadFile(job.absPath)
				if err != nil {
					continue
				}
				sha := ComputeFileSHABytes(data)
				fi, _ := os.Stat(job.absPath)
				var mtime int64
				var size int64
				if fi != nil {
					mtime = fi.ModTime().UnixNano()
					size = fi.Size()
				}
				parsed := e.parseFileData(job.rel, job.absPath, data, sha)
				parseResults <- parseJobResult{
					rel:   job.rel,
					sha:   sha,
					mtime: mtime,
					size:  size,
					res:   parsed,
				}
			}
		}()
	}

	for _, rel := range filesToParse {
		parseJobs <- parseJob{rel: rel, absPath: currentFiles[rel]}
	}
	close(parseJobs)
	parseWg.Wait()
	close(parseResults)

	for pr := range parseResults {
		if pr.res != nil {
			res := pr.res
			domain := GetDomainFromPath(pr.rel)

			var nodeIDs []string

			// L1 Module
			l1Props := res.FileNode.ToProperties()
			e.db.CreateNode(res.FileNode.ID, res.FileNode.Labels, l1Props)
			nodeIDs = append(nodeIDs, res.FileNode.ID)

			// L2 Classes
			for _, cls := range res.ClassNodes {
				cProps := cls.ToProperties()
				e.db.CreateNode(cls.ID, cls.Labels, cProps)
				nodeIDs = append(nodeIDs, cls.ID)
				e.db.CreateEdge(res.FileNode.ID, cls.ID, string(models.EdgeContains), nil)
			}

			// L3 Symbols
			for _, sym := range res.SymbolNodes {
				sProps := sym.ToProperties()
				e.db.CreateNode(sym.ID, sym.Labels, sProps)
				nodeIDs = append(nodeIDs, sym.ID)
				e.db.CreateEdge(sym.ParentID, sym.ID, string(models.EdgeContains), nil)
			}

			cachedFiles[pr.rel] = CacheFileInfo{
				SHA:         pr.sha,
				Mtime:       pr.mtime,
				Size:        pr.size,
				Domain:      domain,
				NodeIDs:     nodeIDs,
				Exports:     res.FileNode.Exports,
				Imports:     res.FileNode.ImportPaths,
				ImportSpecs: res.Imports,
			}
		}
	}

	// Step 3: Ensure L0 Domain nodes
	domainCounts := make(map[string]int)
	for rel, info := range cachedFiles {
		domainCounts[cacheDomain(rel, info)]++
	}

	// Only the domains touched by this delta need their node and links
	// refreshed. Rewriting every domain on every sync marked the graph dirty
	// even when nothing changed, which forced a full database rewrite.
	dirtyDomains := deletedDomains
	for _, rel := range append(added, modified...) {
		if info, ok := cachedFiles[rel]; ok {
			dirtyDomains[cacheDomain(rel, info)] = true
		}
	}

	// A forced re-index is the point where the graph and the cache state may
	// have drifted apart, so drop L0 nodes that no longer own any file.
	if force {
		var stale []string
		e.db.RangeNodes(func(id string, props map[string]any) {
			if lvl, _ := props["level"].(string); lvl != string(models.LevelL0) {
				return
			}
			if name, _ := props["name"].(string); domainCounts[name] == 0 {
				stale = append(stale, id)
			}
		})
		e.db.DeleteNodes(stale)
	}

	for dom := range dirtyDomains {
		// A zero count removes the domain node: the last file of that domain
		// is gone.
		e.upsertDomain(dom, domainCounts[dom])
	}

	// Connect L0 -> L1 edges for the files touched by this delta.
	for _, rel := range append(added, modified...) {
		info, ok := cachedFiles[rel]
		if !ok {
			continue
		}
		e.db.CreateEdge("domain:"+cacheDomain(rel, info), "file:"+rel, string(models.EdgeContains), nil)
	}

	// Step 4 & 5: Resolve cross-file IMPORTS and CALLS edges
	if len(added) > 0 || len(modified) > 0 || len(deleted) > 0 || force {
		if force || len(cachedFiles) == len(added) {
			// Full rebuild for initial or forced sync
			e.db.DeleteEdgesByType(string(models.EdgeImports), string(models.EdgeCalls))

			var allImportSpecs []ImportSpec
			for _, info := range cachedFiles {
				allImportSpecs = append(allImportSpecs, info.ImportSpecs...)
			}
			e.resolveImportEdges(allImportSpecs, currentFiles)
			e.resolveAllCallEdges()
		} else {
			// Localized Delta Rebuild: Only resolve affected files and callers
			e.resolveDeltaEdges(added, modified, deleted, currentFiles, cachedFiles)
		}
	}

	// Persist the graph only when it actually changed; the cache state may still
	// need to be written to restore the full-index marker after a targeted
	// update, without rewriting the whole graph file.
	graphChanged := len(added) > 0 || len(modified) > 0 || len(deleted) > 0 || force || e.db.IsDirty()
	if graphChanged || !e.state.FullSync {
		e.state.LastSync = time.Now().Unix()
		e.state.GitCommit = e.getGitCommit()
		e.state.Files = cachedFiles
		if graphChanged {
			if err := e.db.Save(); err != nil {
				e.state.FullSync = false
				return SyncStats{ElapsedMS: int(time.Since(startTime).Milliseconds()), Error: fmt.Sprintf("save graph database: %v", err)}
			}
		}
		e.state.FullSync = true
		if err := e.saveCacheState(); err != nil {
			e.state.FullSync = false
			return SyncStats{ElapsedMS: int(time.Since(startTime).Milliseconds()), Error: fmt.Sprintf("save cache state: %v", err)}
		}
	}

	elapsedMS := int(time.Since(startTime).Milliseconds())
	counts := e.db.CountNodesAndEdges()

	return SyncStats{
		ElapsedMS:     elapsedMS,
		TotalFiles:    len(currentFiles),
		AddedCount:    len(added),
		ModifiedCount: len(modified),
		DeletedCount:  len(deleted),
		CachedCount:   len(unchanged),
		Nodes:         counts,
		GitCommit:     e.state.GitCommit,
	}
}

// resolveWorkspacePath maps a relative or absolute path to a workspace-relative
// slash path and its absolute counterpart. It reports false when the path lies
// outside the workspace, so a post-tool hook cannot pull files from another
// project into this workspace's graph.
func (e *CacheEngine) resolveWorkspacePath(target string) (relPath string, absPath string, ok bool) {
	if target == "" {
		return "", "", false
	}
	candidate := target
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(e.workspaceRoot, filepath.FromSlash(candidate))
	}
	candidate = filepath.Clean(candidate)

	rel, err := filepath.Rel(e.workspaceRoot, candidate)
	if err != nil {
		return "", "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", "", false
	}
	return rel, candidate, true
}

// unchangedStats reports the current index state for a targeted sync that has
// nothing to do.
func (e *CacheEngine) unchangedStats(startTime time.Time) SyncStats {
	return SyncStats{
		ElapsedMS:   int(time.Since(startTime).Milliseconds()),
		TotalFiles:  len(e.state.Files),
		CachedCount: len(e.state.Files),
		Nodes:       e.db.CountNodesAndEdges(),
		GitCommit:   e.state.GitCommit,
	}
}

// SyncTargetFile performs ultra-fast targeted incremental sync for a single file
// (e.g. from Claude Code's post-tool hook), skipping full-directory scanning.
// Paths outside the workspace are ignored rather than indexed.
func (e *CacheEngine) SyncTargetFile(target string) (SyncStats, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	startTime := time.Now()

	relPath, absPath, insideWorkspace := e.resolveWorkspacePath(target)
	if !insideWorkspace || e.shouldIgnore(relPath, false) {
		return e.unchangedStats(startTime), nil
	}

	ext := strings.ToLower(filepath.Ext(relPath))
	if _, ok := SupportedExtensions[ext]; !ok {
		return e.unchangedStats(startTime), nil
	}

	cachedFiles := e.state.Files
	if cachedFiles == nil {
		cachedFiles = make(map[string]CacheFileInfo)
	}

	fi, statErr := os.Stat(absPath)
	isDeleted := os.IsNotExist(statErr)

	var added, modified, deleted []string
	// Domain the deleted file belonged to, so its module_count can be corrected.
	deletedDomain := ""

	if isDeleted {
		if info, exists := cachedFiles[relPath]; exists {
			deleted = append(deleted, relPath)
			deletedDomain = cacheDomain(relPath, info)
			e.db.DeleteNodes(info.NodeIDs)
			delete(cachedFiles, relPath)
		}
	} else if statErr == nil {
		cacheItem, exists := cachedFiles[relPath]
		mtime := fi.ModTime().UnixNano()
		size := fi.Size()

		if !exists {
			added = append(added, relPath)
		} else if mtime != cacheItem.Mtime || size != cacheItem.Size {
			if ComputeFileSHA(absPath) != cacheItem.SHA {
				modified = append(modified, relPath)
			}
		}
	}

	if len(added) == 0 && len(modified) == 0 && len(deleted) == 0 {
		return e.unchangedStats(startTime), nil
	}

	// Clean subnodes of modified file before re-parsing
	for _, rel := range modified {
		if info, ok := cachedFiles[rel]; ok {
			e.db.DeleteNodes(info.NodeIDs)
		} else {
			e.db.DeleteFileSubnodes(rel)
		}
	}

	for _, rel := range append(added, modified...) {
		data, readErr := os.ReadFile(absPath)
		if readErr != nil {
			continue
		}
		sha := ComputeFileSHABytes(data)
		parsed := e.parseFileData(rel, absPath, data, sha)
		if parsed != nil {
			domain := GetDomainFromPath(rel)
			var nodeIDs []string

			// L1 Module
			l1Props := parsed.FileNode.ToProperties()
			e.db.CreateNode(parsed.FileNode.ID, parsed.FileNode.Labels, l1Props)
			nodeIDs = append(nodeIDs, parsed.FileNode.ID)

			// L2 Classes
			for _, cls := range parsed.ClassNodes {
				e.db.CreateNode(cls.ID, cls.Labels, cls.ToProperties())
				nodeIDs = append(nodeIDs, cls.ID)
				e.db.CreateEdge(parsed.FileNode.ID, cls.ID, string(models.EdgeContains), nil)
			}

			// L3 Symbols
			for _, sym := range parsed.SymbolNodes {
				e.db.CreateNode(sym.ID, sym.Labels, sym.ToProperties())
				nodeIDs = append(nodeIDs, sym.ID)
				e.db.CreateEdge(sym.ParentID, sym.ID, string(models.EdgeContains), nil)
			}

			// L0 Domain nodes are refreshed after the loop: their module_count
			// depends on every file in the domain, not just this one.
			var mtime int64
			var size int64
			if fi != nil {
				mtime = fi.ModTime().UnixNano()
				size = fi.Size()
			}
			cachedFiles[rel] = CacheFileInfo{
				SHA:         sha,
				Mtime:       mtime,
				Size:        size,
				Domain:      domain,
				NodeIDs:     nodeIDs,
				Exports:     parsed.FileNode.Exports,
				Imports:     parsed.FileNode.ImportPaths,
				ImportSpecs: parsed.Imports,
			}
		}
	}

	// Refresh the domains touched by this delta. The count comes from the cache
	// state, so adding or deleting one file keeps module_count accurate.
	if len(added) > 0 || len(modified) > 0 || len(deleted) > 0 {
		domainCounts := make(map[string]int)
		for rel, info := range cachedFiles {
			domainCounts[cacheDomain(rel, info)]++
		}
		touched := map[string]bool{}
		for _, rel := range append(added, modified...) {
			if info, ok := cachedFiles[rel]; ok {
				touched[cacheDomain(rel, info)] = true
			}
		}
		if deletedDomain != "" {
			touched[deletedDomain] = true
		}
		for dom := range touched {
			e.upsertDomain(dom, domainCounts[dom])
		}

		for _, rel := range append(added, modified...) {
			info, ok := cachedFiles[rel]
			if !ok {
				continue
			}
			e.db.CreateEdge("domain:"+cacheDomain(rel, info), "file:"+rel, string(models.EdgeContains), nil)
		}
	}

	currentFiles := make(map[string]string, len(cachedFiles))
	for rel := range cachedFiles {
		currentFiles[rel] = filepath.Join(e.workspaceRoot, filepath.FromSlash(rel))
	}
	e.resolveDeltaEdges(added, modified, deleted, currentFiles, cachedFiles)

	e.state.LastSync = time.Now().Unix()
	e.state.GitCommit = e.getGitCommit()
	e.state.Files = cachedFiles
	e.state.FullSync = false

	stats := SyncStats{
		TotalFiles:    len(cachedFiles),
		AddedCount:    len(added),
		ModifiedCount: len(modified),
		DeletedCount:  len(deleted),
		CachedCount:   len(cachedFiles) - len(added) - len(modified),
		GitCommit:     e.state.GitCommit,
	}
	if err := e.db.Save(); err != nil {
		stats.ElapsedMS = int(time.Since(startTime).Milliseconds())
		stats.Error = fmt.Sprintf("save graph database: %v", err)
		return stats, fmt.Errorf("save graph database: %w", err)
	}
	if err := e.saveCacheState(); err != nil {
		stats.ElapsedMS = int(time.Since(startTime).Milliseconds())
		stats.Error = fmt.Sprintf("save cache state: %v", err)
		return stats, fmt.Errorf("save cache state: %w", err)
	}
	stats.Nodes = e.db.CountNodesAndEdges()
	// Measured after persistence: the hook reports this value, so it must cover
	// the save as well instead of only the in-memory work.
	stats.ElapsedMS = int(time.Since(startTime).Milliseconds())
	return stats, nil
}

func (e *CacheEngine) resolveDeltaEdges(added, modified, deleted []string, currentFiles map[string]string, cachedFiles map[string]CacheFileInfo) {
	dirtyFiles := make(map[string]bool)
	for _, f := range added {
		dirtyFiles[f] = true
	}
	for _, f := range modified {
		dirtyFiles[f] = true
	}
	for _, f := range deleted {
		dirtyFiles[f] = true
	}

	// 1. Invalidate IMPORTS connected to dirty files
	for rel := range dirtyFiles {
		fileID := "file:" + rel
		e.db.DeleteEdgesForSource(fileID, string(models.EdgeImports))
		e.db.DeleteEdgesForTarget(fileID, string(models.EdgeImports))
	}

	// Re-resolve IMPORTS across cachedFiles (very fast, string-based)
	var allImportSpecs []ImportSpec
	for _, info := range cachedFiles {
		allImportSpecs = append(allImportSpecs, info.ImportSpecs...)
	}
	e.resolveImportEdges(allImportSpecs, currentFiles)

	// 2. Identify all symbol names defined in added / modified files
	modifiedSymbolNames := make(map[string]bool)
	for _, rel := range append(added, modified...) {
		if info, ok := cachedFiles[rel]; ok {
			for _, symName := range info.Exports {
				modifiedSymbolNames[symName] = true
			}
			for _, nodeID := range info.NodeIDs {
				if strings.HasPrefix(nodeID, "symbol:") {
					if n, ok := e.db.GetNode(nodeID); ok {
						if name, _ := n["name"].(string); name != "" {
							if idx := strings.LastIndex(name, "."); idx != -1 {
								name = name[idx+1:]
							}
							modifiedSymbolNames[name] = true
						}
					}
				}
			}
		}
	}

	// 3. Collect call specs needing resolution:
	// a) All calls from symbols in added & modified files
	// b) Calls from any file whose CalledName matches modifiedSymbolNames
	var deltaCallSpecs []CallSpec
	dirtySet := make(map[string]bool)
	for _, rel := range append(added, modified...) {
		dirtySet[rel] = true
	}

	e.db.RangeNodes(func(id string, props map[string]any) {
		lvl, _ := props["level"].(string)
		if lvl != string(models.LevelL3) {
			return
		}
		fPath, _ := props["file_path"].(string)
		isDirtyFile := dirtySet[fPath]

		var callsList []string
		if callsRaw, ok := props["calls"].([]string); ok {
			callsList = callsRaw
		} else if callsAny, ok := props["calls"].([]any); ok {
			for _, c := range callsAny {
				if cStr, ok := c.(string); ok {
					callsList = append(callsList, cStr)
				}
			}
		}

		for _, c := range callsList {
			if isDirtyFile || modifiedSymbolNames[c] {
				deltaCallSpecs = append(deltaCallSpecs, CallSpec{
					CallerID:   id,
					CalledName: c,
				})
			}
		}
	})

	if len(deltaCallSpecs) > 0 {
		e.resolveCallEdges(deltaCallSpecs)
	}
}

func (e *CacheEngine) resolveAllCallEdges() {
	var allCallSpecs []CallSpec
	e.db.RangeNodes(func(id string, props map[string]any) {
		if lvl, _ := props["level"].(string); lvl != string(models.LevelL3) {
			return
		}
		if callsRaw, ok := props["calls"].([]string); ok {
			for _, c := range callsRaw {
				allCallSpecs = append(allCallSpecs, CallSpec{
					CallerID:   id,
					CalledName: c,
				})
			}
		} else if callsAny, ok := props["calls"].([]any); ok {
			for _, c := range callsAny {
				if cStr, ok := c.(string); ok {
					allCallSpecs = append(allCallSpecs, CallSpec{
						CallerID:   id,
						CalledName: cStr,
					})
				}
			}
		}
	})
	e.resolveCallEdges(allCallSpecs)
}

func (e *CacheEngine) resolveImportEdges(imports []ImportSpec, currentFiles map[string]string) {
	exts := []string{
		"", ".py", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".vue", ".svelte", ".go",
		".java", ".kt", ".kts", ".cs", ".swift", ".php",
		".rs", ".c", ".cpp", ".cc", ".cxx", ".h", ".hpp",
		"/__init__.py", "/index.ts", "/index.tsx", "/index.js", "/mod.rs",
	}

	findHit := func(base string) string {
		for _, ext := range exts {
			candidate := base + ext
			if _, exists := currentFiles[candidate]; exists {
				return candidate
			}
		}
		return ""
	}

	type edgeKey struct {
		src string
		tgt string
	}
	edges := make(map[edgeKey]map[string]any)

	allGoModules := e.readAllGoModules(currentFiles)
	if len(allGoModules) == 0 {
		rootMod := e.readGoModulePath()
		if rootMod != "" {
			allGoModules[rootMod] = ""
		}
	}

	goDirFiles := make(map[string][]string)
	for rel := range currentFiles {
		if strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go") {
			dir := path.Dir(rel)
			goDirFiles[dir] = append(goDirFiles[dir], rel)
		}
	}

	tsConfigPaths := e.readTSConfigPaths()

	for _, imp := range imports {
		spec := imp.ModuleSpec
		isPy := strings.HasSuffix(imp.SourceFile, ".py")
		isJava := strings.HasSuffix(imp.SourceFile, ".java")
		isDotLang := isJava || strings.HasSuffix(imp.SourceFile, ".kt") ||
			strings.HasSuffix(imp.SourceFile, ".kts") || strings.HasSuffix(imp.SourceFile, ".cs")
		isPHP := strings.HasSuffix(imp.SourceFile, ".php")
		isRust := strings.HasSuffix(imp.SourceFile, ".rs")
		isC := strings.HasSuffix(imp.SourceFile, ".c") || strings.HasSuffix(imp.SourceFile, ".cpp") ||
			strings.HasSuffix(imp.SourceFile, ".cc") || strings.HasSuffix(imp.SourceFile, ".h") || strings.HasSuffix(imp.SourceFile, ".hpp")

		if strings.HasSuffix(imp.SourceFile, ".go") {
			matchedDir := ""
			matched := false
			for modPath, modDir := range allGoModules {
				if spec == modPath || strings.HasPrefix(spec, modPath+"/") {
					sub := strings.TrimPrefix(strings.TrimPrefix(spec, modPath), "/")
					if modDir != "" && sub != "" {
						matchedDir = modDir + "/" + sub
					} else if modDir != "" {
						matchedDir = modDir
					} else if sub != "" {
						matchedDir = sub
					} else {
						matchedDir = "."
					}
					matched = true
					break
				}
			}
			if !matched {
				continue // stdlib or third-party package
			}
			for _, t := range goDirFiles[matchedDir] {
				if t == imp.SourceFile || path.Dir(imp.SourceFile) == matchedDir {
					continue
				}
				k := edgeKey{src: imp.SourceFile, tgt: t}
				props, ok := edges[k]
				if !ok {
					props = map[string]any{
						"imported_names": []string{},
						"spec":           imp.ModuleSpec,
					}
					edges[k] = props
				}
				existingNames, _ := props["imported_names"].([]string)
				props["imported_names"] = uniqueSortedStrings(append(existingNames, imp.Names...))
			}
			continue
		}

		var bases []string
		if imp.IsRelative {
			srcDir := path.Dir(imp.SourceFile)
			if srcDir == "." {
				srcDir = ""
			}

			if isPy {
				dotCount := 0
				for _, ch := range spec {
					if ch == '.' {
						dotCount++
					} else {
						break
					}
				}
				for d := 0; d < dotCount-1; d++ {
					if srcDir != "" {
						srcDir = path.Dir(srcDir)
						if srcDir == "." {
							srcDir = ""
						}
					}
				}
				cleanSpec := strings.TrimLeft(spec, ".")
				spec = strings.ReplaceAll(cleanSpec, ".", "/")
			}

			var base string
			if srcDir != "" && spec != "" {
				base = path.Clean(srcDir + "/" + spec)
			} else if srcDir != "" {
				base = srcDir
			} else {
				base = spec
			}
			bases = []string{base}
		} else {
			mod := spec
			if isPy || isDotLang {
				mod = strings.ReplaceAll(spec, ".", "/")
			} else if isPHP {
				mod = strings.ReplaceAll(strings.Trim(spec, "\\"), "\\", "/")
			} else if isRust {
				mod = strings.ReplaceAll(spec, "::", "/")
				mod = strings.TrimPrefix(strings.TrimPrefix(mod, "crate/"), "super/")
			} else if isC {
				mod = strings.TrimPrefix(spec, "include/")
			}

			// Check tsconfig path mappings
			for aliasPrefix, targetPrefix := range tsConfigPaths {
				if strings.HasPrefix(spec, aliasPrefix) {
					aliased := targetPrefix + strings.TrimPrefix(spec, aliasPrefix)
					bases = append(bases, aliased)
				}
			}

			for _, prefix := range []string{"", "src/", "app/", "lib/", "pkg/", "src/main/java/", "include/"} {
				bases = append(bases, prefix+mod)
			}
		}

		targets := make(map[string]bool)
		for _, b := range bases {
			if b != "" {
				hit := findHit(b)
				if hit != "" {
					targets[hit] = true
					break
				}
			}
		}

		if isPy {
			for _, b := range bases {
				for _, n := range imp.Names {
					hit := findHit(path.Clean(b + "/" + n))
					if hit != "" {
						targets[hit] = true
					}
				}
			}
		}

		for t := range targets {
			if t != imp.SourceFile {
				k := edgeKey{src: imp.SourceFile, tgt: t}
				props, ok := edges[k]
				if !ok {
					props = map[string]any{
						"imported_names": []string{},
						"spec":           imp.ModuleSpec,
					}
					edges[k] = props
				}
				existingNames, _ := props["imported_names"].([]string)
				merged := append(existingNames, imp.Names...)
				props["imported_names"] = uniqueSortedStrings(merged)
			}
		}
	}

	for k, props := range edges {
		e.db.CreateEdge("file:"+k.src, "file:"+k.tgt, string(models.EdgeImports), props)
	}
}

func (e *CacheEngine) resolveCallEdges(calls []CallSpec) {
	// Group L3 symbols by their short name (e.g. AuthService.login -> login, or
	// login -> login). Only the two properties the resolver needs are kept, so
	// the graph does not have to hand out a copy of every node's property map.
	type symbolMeta struct {
		filePath string
		name     string
	}
	byName := make(map[string][]string)
	meta := make(map[string]symbolMeta)
	e.db.RangeNodes(func(id string, props map[string]any) {
		if lvl, _ := props["level"].(string); lvl != string(models.LevelL3) {
			return
		}
		fullName, _ := props["name"].(string)
		shortName := fullName
		if idx := strings.LastIndex(fullName, "."); idx != -1 {
			shortName = fullName[idx+1:]
		}
		byName[shortName] = append(byName[shortName], id)
		fPath, _ := props["file_path"].(string)
		meta[id] = symbolMeta{filePath: fPath, name: fullName}
	})

	importedMap := make(map[string]map[string]bool)
	getImportedFiles := func(filePath string) map[string]bool {
		if files, ok := importedMap[filePath]; ok {
			return files
		}
		outgoing := e.db.GetOutgoingEdges("file:"+filePath, string(models.EdgeImports))
		files := make(map[string]bool)
		for _, edge := range outgoing {
			targetFile := strings.TrimPrefix(edge.TargetID, "file:")
			files[targetFile] = true
		}
		importedMap[filePath] = files
		return files
	}

	type edgeKey struct {
		src string
		tgt string
	}
	resolved := make(map[edgeKey]bool)

	for _, call := range calls {
		candidates := byName[call.CalledName]
		if len(candidates) == 0 {
			continue
		}

		caller, exists := meta[call.CallerID]
		if !exists {
			continue
		}

		var filteredCandidates []string
		for _, c := range candidates {
			if c != call.CallerID {
				filteredCandidates = append(filteredCandidates, c)
			}
		}
		if len(filteredCandidates) == 0 {
			continue
		}

		callerFile := caller.filePath
		callerFullName := caller.name
		callerClass := ""
		if idx := strings.LastIndex(callerFullName, "."); idx != -1 {
			callerClass = callerFullName[:idx]
		}

		// 1. Same file candidates
		var sameFile []string
		for _, c := range filteredCandidates {
			if cm, ok := meta[c]; ok && cm.filePath == callerFile {
				sameFile = append(sameFile, c)
			}
		}

		var target string
		if len(sameFile) > 0 {
			// Check same class
			var sameClass []string
			if callerClass != "" {
				for _, c := range sameFile {
					if cm, ok := meta[c]; ok {
						if idx := strings.LastIndex(cm.name, "."); idx != -1 && cm.name[:idx] == callerClass {
							sameClass = append(sameClass, c)
						}
					}
				}
			}
			if len(sameClass) == 1 {
				target = sameClass[0]
			} else if len(sameFile) == 1 {
				target = sameFile[0]
			}
		} else {
			// 2. Imported files
			importedFiles := getImportedFiles(callerFile)
			var viaImport []string
			for _, c := range filteredCandidates {
				if cm, ok := meta[c]; ok && importedFiles[cm.filePath] {
					viaImport = append(viaImport, c)
				}
			}
			if len(viaImport) == 1 {
				target = viaImport[0]
			} else if len(filteredCandidates) == 1 {
				target = filteredCandidates[0]
			}
		}

		if target != "" {
			resolved[edgeKey{src: call.CallerID, tgt: target}] = true
		}
	}

	for k := range resolved {
		e.db.CreateEdge(k.src, k.tgt, string(models.EdgeCalls), nil)
	}
}

// readGoModulePath returns the module path declared in <workspace>/go.mod, or "".
func (e *CacheEngine) readGoModulePath() string {
	return e.readGoModuleInDir(".")
}

// readAllGoModules maps modulePath -> relativeDir for every module that can be
// an import target. Instead of walking the whole workspace it only inspects the
// directories that hold Go files (and their ancestors): that is a much smaller
// set and exactly the set an import can resolve to.
func (e *CacheEngine) readAllGoModules(currentFiles map[string]string) map[string]string {
	dirs := make(map[string]bool)
	for rel := range currentFiles {
		if !strings.HasSuffix(rel, ".go") {
			continue
		}
		for dir := path.Dir(rel); ; dir = path.Dir(dir) {
			dirs[dir] = true
			if dir == "." || dir == "/" || dir == "" {
				break
			}
		}
	}

	modules := make(map[string]string)
	for dir := range dirs {
		modPath := e.readGoModuleInDir(dir)
		if modPath == "" {
			continue
		}
		relDir := dir
		if relDir == "." {
			relDir = ""
		}
		modules[modPath] = relDir
	}
	return modules
}

// readGoModuleInDir returns the module path declared in <dir>/go.mod, or "".
func (e *CacheEngine) readGoModuleInDir(dir string) string {
	data, err := os.ReadFile(filepath.Join(e.workspaceRoot, filepath.FromSlash(dir), "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "module") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "module"))
		if i := strings.Index(rest, "//"); i >= 0 {
			rest = strings.TrimSpace(rest[:i])
		}
		return strings.Trim(rest, `"`)
	}
	return ""
}

// readTSConfigPaths loads compilerOptions.paths from tsconfig.json or jsconfig.json if present
func (e *CacheEngine) readTSConfigPaths() map[string]string {
	fingerprint := ""
	for _, configFile := range []string{"tsconfig.json", "jsconfig.json"} {
		if fi, err := os.Stat(filepath.Join(e.workspaceRoot, configFile)); err == nil {
			fingerprint += fmt.Sprintf("%s:%d:%d;", configFile, fi.ModTime().UnixNano(), fi.Size())
		}
	}
	if e.tsPathsCache != nil && fingerprint == e.tsPathsStamp {
		return e.tsPathsCache
	}

	paths := make(map[string]string)
	for _, configFile := range []string{"tsconfig.json", "jsconfig.json"} {
		p := filepath.Join(e.workspaceRoot, configFile)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var parsed struct {
			CompilerOptions struct {
				Paths map[string][]string `json:"paths"`
			} `json:"compilerOptions"`
		}
		if err := json.Unmarshal(data, &parsed); err == nil && parsed.CompilerOptions.Paths != nil {
			for alias, targets := range parsed.CompilerOptions.Paths {
				if len(targets) > 0 {
					cleanAlias := strings.TrimSuffix(alias, "*")
					cleanTarget := strings.TrimPrefix(strings.TrimSuffix(targets[0], "*"), "./")
					paths[cleanAlias] = cleanTarget
				}
			}
			break
		}
	}
	e.tsPathsCache = paths
	e.tsPathsStamp = fingerprint
	return paths
}

func uniqueSortedStrings(in []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range in {
		if !seen[s] && s != "" {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
