package indexer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
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
// that stale caches are re-indexed automatically. 1.3: Multi-language support
// (Java, Rust, C/C++), tsconfig path aliases, multi-module monorepos, and delta edge updates.
const CacheVersion = "1.3"

var SupportedExtensions = map[string]string{
	".py":   "python",
	".ts":   "typescript",
	".tsx":  "typescript",
	".js":   "javascript",
	".jsx":  "javascript",
	".mjs":  "javascript",
	".cjs":  "javascript",
	".go":   "go",
	".java": "java",
	".rs":   "rust",
	".c":    "c",
	".cpp":  "cpp",
	".cc":   "cpp",
	".cxx":  "cpp",
	".h":    "c",
	".hpp":  "cpp",
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
}

type CacheEngine struct {
	config        *config.LatticeConfig
	db            *storage.GraphStorage
	workspaceRoot string
	state         CacheState
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
	return os.Rename(tmpFile, e.config.CacheStateFile)
}

func ComputeFileSHA(filePath string) string {
	f, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
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
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = e.workspaceRoot
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (e *CacheEngine) parseFile(relPath string, absPath string, sha string) *ParsedFileResult {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil
	}
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
	case ".java":
		return ParseJavaFile(relPath, sourceCode, sha, domain)
	case ".rs":
		return ParseRustFile(relPath, sourceCode, sha, domain)
	case ".c", ".cpp", ".cc", ".cxx", ".h", ".hpp":
		return ParseCCPPFile(relPath, sourceCode, sha, domain)
	default:
		return nil
	}
}

func (e *CacheEngine) Sync(force bool) SyncStats {
	startTime := time.Now()
	currentFiles := e.ScanFiles()
	cachedFiles := e.state.Files
	if cachedFiles == nil {
		cachedFiles = make(map[string]CacheFileInfo)
	}

	// If the graph database is empty (deleted, corrupted or failed to load) but
	// the cache state still claims files are indexed, every file would be
	// reported as "unchanged" and the graph would stay empty forever.
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

	// Step 1: Clean up deleted files
	for _, rel := range deleted {
		e.db.DeleteFileSubnodes(rel)
		delete(cachedFiles, rel)
	}

	// Step 2: Clean up modified files subnodes before parsing
	filesToParse := append(append([]string(nil), added...), modified...)
	for _, rel := range modified {
		e.db.DeleteFileSubnodes(rel)
	}
	if force {
		for _, rel := range added {
			e.db.DeleteFileSubnodes(rel)
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
				sha := ComputeFileSHA(job.absPath)
				fi, _ := os.Stat(job.absPath)
				var mtime int64
				var size int64
				if fi != nil {
					mtime = fi.ModTime().UnixNano()
					size = fi.Size()
				}
				parsed := e.parseFile(job.rel, job.absPath, sha)
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

	domainsSeen := make(map[string]bool)
	for pr := range parseResults {
		if pr.res != nil {
			res := pr.res
			domain := GetDomainFromPath(pr.rel)
			domainsSeen[domain] = true

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
	for rel, info := range cachedFiles {
		d := info.Domain
		if d == "" {
			d = GetDomainFromPath(rel)
		}
		domainsSeen[d] = true
	}

	domainCounts := make(map[string]int)
	for rel, info := range cachedFiles {
		d := info.Domain
		if d == "" {
			d = GetDomainFromPath(rel)
		}
		domainCounts[d]++
	}

	// Remove empty domains whose last file is gone
	for id, n := range e.db.AllNodes() {
		lvl, _ := n["level"].(string)
		name, _ := n["name"].(string)
		if lvl == string(models.LevelL0) {
			if _, exists := domainCounts[name]; !exists {
				e.db.DeleteNode(id)
			}
		}
	}

	// Create or update L0 domains
	for dom, count := range domainCounts {
		domID := "domain:" + dom
		l0 := models.L0DomainNode{
			ID:          domID,
			Name:        dom,
			Path:        dom,
			Description: "Domain " + dom + " containing modules",
			ModuleCount: count,
			Level:       string(models.LevelL0),
			Labels:      []string{"L0Domain", "Domain"},
		}
		e.db.CreateNode(domID, l0.Labels, l0.ToProperties())
	}

	// Connect L0 -> L1 edges for all current files
	for rel, info := range cachedFiles {
		dom := info.Domain
		if dom == "" {
			dom = GetDomainFromPath(rel)
		}
		domID := "domain:" + dom
		fileID := "file:" + rel
		e.db.CreateEdge(domID, fileID, string(models.EdgeContains), nil)
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

	// Save cache state & DB
	e.state.LastSync = time.Now().Unix()
	e.state.GitCommit = e.getGitCommit()
	e.state.Files = cachedFiles
	_ = e.saveCacheState()
	_ = e.db.Save()

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

	for id, props := range e.db.AllNodes() {
		lvl, _ := props["level"].(string)
		if lvl != string(models.LevelL3) {
			continue
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
	}

	if len(deltaCallSpecs) > 0 {
		e.resolveCallEdges(deltaCallSpecs)
	}
}

func (e *CacheEngine) resolveAllCallEdges() {
	var allCallSpecs []CallSpec
	for id, props := range e.db.AllNodes() {
		lvl, _ := props["level"].(string)
		if lvl == string(models.LevelL3) {
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
		}
	}
	e.resolveCallEdges(allCallSpecs)
}

func (e *CacheEngine) resolveImportEdges(imports []ImportSpec, currentFiles map[string]string) {
	exts := []string{
		"", ".py", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".go",
		".java", ".rs", ".c", ".cpp", ".cc", ".cxx", ".h", ".hpp",
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

	allGoModules := e.readAllGoModules()
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
			if isPy {
				mod = strings.ReplaceAll(spec, ".", "/")
			} else if isJava {
				mod = strings.ReplaceAll(spec, ".", "/")
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
	allNodes := e.db.AllNodes()

	// Group L3 symbols by their short name (e.g. AuthService.login -> login, or login -> login)
	byName := make(map[string][]string)
	for id, props := range allNodes {
		lvl, _ := props["level"].(string)
		if lvl == string(models.LevelL3) {
			fullName, _ := props["name"].(string)
			shortName := fullName
			if idx := strings.LastIndex(fullName, "."); idx != -1 {
				shortName = fullName[idx+1:]
			}
			byName[shortName] = append(byName[shortName], id)
		}
	}

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

		callerProps, exists := allNodes[call.CallerID]
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

		callerFile, _ := callerProps["file_path"].(string)
		callerFullName, _ := callerProps["name"].(string)
		callerClass := ""
		if idx := strings.LastIndex(callerFullName, "."); idx != -1 {
			callerClass = callerFullName[:idx]
		}

		// 1. Same file candidates
		var sameFile []string
		for _, c := range filteredCandidates {
			if cProps, ok := allNodes[c]; ok && cProps["file_path"] == callerFile {
				sameFile = append(sameFile, c)
			}
		}

		var target string
		if len(sameFile) > 0 {
			// Check same class
			var sameClass []string
			if callerClass != "" {
				for _, c := range sameFile {
					if cProps, ok := allNodes[c]; ok {
						cName, _ := cProps["name"].(string)
						if idx := strings.LastIndex(cName, "."); idx != -1 && cName[:idx] == callerClass {
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
				if cProps, ok := allNodes[c]; ok {
					cFile, _ := cProps["file_path"].(string)
					if importedFiles[cFile] {
						viaImport = append(viaImport, c)
					}
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
	data, err := os.ReadFile(filepath.Join(e.workspaceRoot, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module") {
			rest := strings.TrimSpace(strings.TrimPrefix(line, "module"))
			if i := strings.Index(rest, "//"); i >= 0 {
				rest = strings.TrimSpace(rest[:i])
			}
			return strings.Trim(rest, `"`)
		}
	}
	return ""
}

// readAllGoModules discovers all go.mod files across monorepos and maps modulePath -> relativeDir
func (e *CacheEngine) readAllGoModules() map[string]string {
	modules := make(map[string]string)
	_ = filepath.Walk(e.workspaceRoot, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			base := info.Name()
			if strings.HasPrefix(base, ".") || base == "node_modules" || base == "vendor" || base == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Name() == "go.mod" {
			data, err := os.ReadFile(p)
			if err == nil {
				for _, line := range strings.Split(string(data), "\n") {
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "module") {
						rest := strings.TrimSpace(strings.TrimPrefix(line, "module"))
						if i := strings.Index(rest, "//"); i >= 0 {
							rest = strings.TrimSpace(rest[:i])
						}
						modPath := strings.Trim(rest, `"`)
						relDir, _ := filepath.Rel(e.workspaceRoot, filepath.Dir(p))
						relDir = filepath.ToSlash(relDir)
						if relDir == "." {
							relDir = ""
						}
						modules[modPath] = relDir
						break
					}
				}
			}
		}
		return nil
	})
	return modules
}

// readTSConfigPaths loads compilerOptions.paths from tsconfig.json or jsconfig.json if present
func (e *CacheEngine) readTSConfigPaths() map[string]string {
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
	return paths
}

func containsString(list []string, item string) bool {
	for _, v := range list {
		if v == item {
			return true
		}
	}
	return false
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
