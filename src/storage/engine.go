package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"meta-lattice/src/models"
)

// errIncrementalUnavailable means the native file cannot be updated in place and
// has to be rebuilt from the in-memory graph.
var errIncrementalUnavailable = errors.New("incremental persistence unavailable")

// errDatabaseLocked means another process is holding the LatticeDB file lock.
// Rebuilding would clobber that writer, so the pending changes are kept and the
// caller retries later.
var errDatabaseLocked = errors.New("graph database is locked by another process")

type NodeData struct {
	ID         string         `json:"id"`
	Labels     []string       `json:"labels"`
	Properties map[string]any `json:"properties"`
}

type nodeEdgesSnapshot struct {
	labels   []string
	outEdges []models.GraphEdge
	inEdges  []models.GraphEdge
}

type GraphStorage struct {
	mu          sync.RWMutex
	filePath    string                        // json backup path
	latticePath string                        // native .lattice path
	nodes       map[string]*NodeData          // nodeID -> NodeData
	outEdges    map[string][]models.GraphEdge // sourceID -> edges
	inEdges     map[string][]models.GraphEdge // targetID -> edges
	// edgeIndex holds the (source, type, target) triples that exist, so
	// CreateEdge can decide whether an edge is new in O(1) instead of scanning
	// the source's edge list (which is quadratic for hubs such as L0 domains).
	edgeIndex map[string]struct{}
	// Secondary in-memory indexes for O(1) lookups when the graph is hydrated.
	byLevel      map[string]map[string]struct{} // level ("L0".."L3") -> set of nodeIDs
	bySymbolName map[string]map[string]struct{} // full or short symbol name -> set of nodeIDs
	byModulePath map[string]string              // module path -> nodeID
	edgesByType  map[string]map[string]struct{} // edgeType -> set of edgeKeys

	// lazyNative is true when the persisted .lattice file uses the v2 indexed
	// schema and has not been hydrated into memory. Point queries, B-Tree
	// lookups, FTS searches, and edge traversals execute directly against
	// LatticeDB, while lazyNodeCache memoizes nodes read during lazy queries.
	lazyNative    bool
	lazyNodeCache map[string]*NodeData

	bm25 *BM25Index
	// counts memoizes CountNodesAndEdges; any mutation invalidates it.
	counts      map[string]int
	countsValid bool
	bm25Loaded  bool
	dirty       bool
	// journal records the final intent for every node and edge touched since
	// the last successful save, so a native backend can persist just that delta
	// instead of rebuilding the whole file. It is nil when nothing changed.
	journal *graphJournal
	// lastSaveMode / lastSaveOps are diagnostics for tests and benchmarks.
	lastSaveMode string
	lastSaveOps  int
	loadedMtime  int64
}

// nodeMutation is the final intent for one node since the last save.
type nodeMutation struct {
	labels []string
	delete bool
}

// edgeMutation is the final intent for one edge since the last save. Properties
// are kept for upserts; a delete only needs the (source, type, target) key.
type edgeMutation struct {
	props  map[string]any
	delete bool
}

// graphJournal is the delta between the in-memory graph and the persisted one.
// Keys are our stable string IDs; later mutations overwrite earlier ones, so
// only the final state of each node and edge is applied.
type graphJournal struct {
	nodes map[string]nodeMutation
	edges map[string]edgeMutation
	// forceRebuild asks Save to rewrite the whole file. It is set when the
	// delta grows to a size where a rebuild is cheaper, and when the backend
	// cannot apply the journal incrementally.
	forceRebuild bool
}

func newGraphJournal() *graphJournal {
	return &graphJournal{
		nodes: make(map[string]nodeMutation),
		edges: make(map[string]edgeMutation),
	}
}

func (j *graphJournal) ops() int {
	if j == nil {
		return 0
	}
	return len(j.nodes) + len(j.edges)
}

type serializedState struct {
	Nodes []*NodeData        `json:"nodes"`
	Edges []models.GraphEdge `json:"edges"`
}

func NewGraphStorage(storagePath string) *GraphStorage {
	latticePath := storagePath
	jsonPath := storagePath
	if strings.HasSuffix(storagePath, ".lattice") {
		jsonPath = strings.TrimSuffix(storagePath, ".lattice") + ".json"
	} else if strings.HasSuffix(storagePath, ".json") {
		latticePath = strings.TrimSuffix(storagePath, ".json") + ".lattice"
	} else {
		latticePath = storagePath + ".lattice"
		jsonPath = storagePath + ".json"
	}

	gs := &GraphStorage{
		filePath:    jsonPath,
		latticePath: latticePath,
		bm25:        NewBM25Index(),
	}
	gs.clearInMemoryMapsLocked()

	if err := gs.Load(); err != nil && !os.IsNotExist(err) {
		if errors.Is(err, errDatabaseLocked) {
			// Another process is writing; the file is fine, so do not touch it.
			fmt.Fprintf(os.Stderr, "meta-lattice: %s is busy (%v); starting with an empty graph\n", storagePath, err)
			return gs
		}
		fmt.Fprintf(os.Stderr, "meta-lattice: failed to load %s (%v); starting with an empty graph\n", storagePath, err)
		if os.IsPermission(err) {
			return gs
		}
		// Preserve the file that actually failed to load. In native mode
		// that is the .lattice file when present, otherwise the JSON
		// fallback; in pure-Go (CGO-disabled) mode it is always the JSON
		// file. Renaming the wrong path silently skipped recovery there.
		corruptPath := storagePath
		if !HasNativeLatticeDB {
			corruptPath = jsonPath
		} else if _, statErr := os.Stat(latticePath); os.IsNotExist(statErr) {
			corruptPath = jsonPath
		}
		_ = os.Rename(corruptPath, corruptPath+".corrupt")
	}
	return gs
}

func (s *GraphStorage) clearInMemoryMapsLocked() {
	s.nodes = make(map[string]*NodeData)
	s.outEdges = make(map[string][]models.GraphEdge)
	s.inEdges = make(map[string][]models.GraphEdge)
	s.edgeIndex = make(map[string]struct{})
	s.byLevel = make(map[string]map[string]struct{})
	s.bySymbolName = make(map[string]map[string]struct{})
	s.byModulePath = make(map[string]string)
	s.edgesByType = make(map[string]map[string]struct{})
	s.lazyNodeCache = make(map[string]*NodeData)
}

func (s *GraphStorage) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Try loading from native LatticeDB file (.lattice) if it exists
	if HasNativeLatticeDB {
		if fi, statErr := os.Stat(s.latticePath); statErr == nil {
			isV2, probeErr := probeLatticeDB(s.latticePath)
			if probeErr != nil {
				if errors.Is(probeErr, errDatabaseLocked) {
					return probeErr
				}
				return fmt.Errorf("corrupt LatticeDB file: %w", probeErr)
			}
			if isV2 {
				// Fast path: v2 schema supports lazy on-demand native queries.
				s.clearInMemoryMapsLocked()
				s.lazyNative = true
				s.bm25.Clear()
				s.bm25Loaded = false
				s.dirty = false
				s.journal = nil
				s.lastSaveMode = ""
				s.lastSaveOps = 0
				s.countsValid = false
				if mtime, ok := storageMtime(s.latticePath); ok {
					s.loadedMtime = mtime
				} else {
					s.loadedMtime = fi.ModTime().UnixNano()
				}
				return nil
			}

			// Legacy v1 schema: hydrate into memory so it can be queried and migrated.
			nodes, outEdges, inEdges, err := loadFromLatticeDB(s.latticePath)
			if err != nil {
				if errors.Is(err, errDatabaseLocked) {
					return err
				}
				return fmt.Errorf("corrupt LatticeDB file: %w", err)
			}
			s.lazyNative = false
			s.nodes = nodes
			s.outEdges = outEdges
			s.inEdges = inEdges
			s.rebuildIndexesLocked()
			s.bm25.Clear()
			s.bm25Loaded = false
			s.dirty = false
			s.journal = nil
			s.lastSaveMode = ""
			s.lastSaveOps = 0
			s.countsValid = false
			if mtime, ok := storageMtime(s.latticePath); ok {
				s.loadedMtime = mtime
			} else {
				s.loadedMtime = fi.ModTime().UnixNano()
			}
			return nil
		}
	}

	// 2. Fallback to JSON file (.json)
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}

	var state serializedState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}

	s.lazyNative = false
	s.clearInMemoryMapsLocked()
	s.bm25.Clear()
	s.bm25Loaded = false
	s.dirty = false
	s.journal = nil
	s.lastSaveMode = ""
	s.lastSaveOps = 0
	if fi, statErr := os.Stat(s.filePath); statErr == nil {
		s.loadedMtime = fi.ModTime().UnixNano()
	}

	for _, n := range state.Nodes {
		normalizeNodeProperties(n.Properties)
		s.nodes[n.ID] = n
	}

	for _, e := range state.Edges {
		normalizeNodeProperties(e.Properties)
		s.outEdges[e.SourceID] = append(s.outEdges[e.SourceID], e)
		s.inEdges[e.TargetID] = append(s.inEdges[e.TargetID], e)
	}
	s.rebuildIndexesLocked()
	s.countsValid = false

	if HasNativeLatticeDB {
		// Loaded from fallback JSON but native LatticeDB is available:
		// mark dirty to auto-migrate to .lattice on next save.
		s.dirty = true
	}

	return nil
}

// ensureLoadedLocked hydrates the full graph from LatticeDB into memory when
// the storage is in lazyNative mode, replaying any uncommitted journal changes
// on top of the loaded graph. The caller must hold s.mu.Lock().
func (s *GraphStorage) ensureLoadedLocked() error {
	if !s.lazyNative {
		return nil
	}
	pendingJournal := s.journal
	pendingNodes := s.nodes
	wasDirty := s.dirty

	baseNodes, baseOut, baseIn, err := loadFromLatticeDB(s.latticePath)
	if err != nil {
		return err
	}

	s.lazyNative = false
	s.lazyNodeCache = make(map[string]*NodeData)
	s.nodes = baseNodes
	s.outEdges = baseOut
	s.inEdges = baseIn
	s.rebuildIndexesLocked()
	s.bm25.Clear()
	s.bm25Loaded = false
	s.countsValid = false

	if !wasDirty || pendingJournal == nil {
		s.dirty = false
		s.journal = nil
		return nil
	}

	// Replay pending journal mutations onto the newly hydrated base graph.
	s.journal = &graphJournal{
		nodes:        make(map[string]nodeMutation, len(pendingJournal.nodes)),
		edges:        make(map[string]edgeMutation, len(pendingJournal.edges)),
		forceRebuild: pendingJournal.forceRebuild,
	}

	for key, mut := range pendingJournal.edges {
		if !mut.delete {
			continue
		}
		parts := strings.SplitN(key, "\x00", 3)
		if len(parts) == 3 {
			s.removeSpecificEdgeLocked(parts[0], parts[2], parts[1])
		}
		s.journal.edges[key] = mut
	}
	for id, mut := range pendingJournal.nodes {
		if mut.delete {
			s.deleteNodeLocked(id)
			s.journal.nodes[id] = mut
		}
	}
	for id, mut := range pendingJournal.nodes {
		if mut.delete {
			continue
		}
		if n, ok := pendingNodes[id]; ok {
			if old, exists := s.nodes[id]; exists {
				s.unindexNodeLocked(old)
			}
			s.nodes[id] = n
			s.indexNodeSecondaryLocked(n)
		}
		s.journal.nodes[id] = mut
	}
	for key, mut := range pendingJournal.edges {
		if mut.delete {
			continue
		}
		parts := strings.SplitN(key, "\x00", 3)
		if len(parts) == 3 {
			src, typ, tgt := parts[0], parts[1], parts[2]
			if _, exists := s.edgeIndex[key]; exists {
				s.updateEdgeLocked(src, tgt, typ, mut.props)
			} else {
				edge := models.GraphEdge{
					SourceID:   src,
					TargetID:   tgt,
					EdgeType:   typ,
					Properties: copyProperties(mut.props),
				}
				s.outEdges[src] = append(s.outEdges[src], edge)
				s.inEdges[tgt] = append(s.inEdges[tgt], edge)
				s.indexEdgeLocked(key, typ)
			}
		}
		s.journal.edges[key] = mut
	}
	s.dirty = true
	s.countsValid = false
	return nil
}

func (s *GraphStorage) removeSpecificEdgeLocked(sourceID, targetID, edgeType string) {
	key := edgeKey(sourceID, targetID, edgeType)
	s.unindexEdgeLocked(key, edgeType)
	if outList, ok := s.outEdges[sourceID]; ok {
		var updated []models.GraphEdge
		for _, e := range outList {
			if !(e.TargetID == targetID && e.EdgeType == edgeType) {
				updated = append(updated, e)
			}
		}
		s.outEdges[sourceID] = updated
	}
	if inList, ok := s.inEdges[targetID]; ok {
		var updated []models.GraphEdge
		for _, e := range inList {
			if !(e.SourceID == sourceID && e.EdgeType == edgeType) {
				updated = append(updated, e)
			}
		}
		s.inEdges[targetID] = updated
	}
}

func (s *GraphStorage) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.dirty {
		s.lastSaveMode = "none"
		s.lastSaveOps = 0
		return nil
	}

	// 1. If native LatticeDB is compiled in, persist exclusively to .lattice
	if HasNativeLatticeDB {
		mode, ops, err := s.saveNativeLocked()
		if err != nil {
			return err
		}
		s.lastSaveMode, s.lastSaveOps = mode, ops
		s.dirty = false
		s.journal = nil
		s.countsValid = false
		if s.lazyNative {
			// Clear the in-memory delta overlay so future queries read from the
			// freshly updated native database.
			s.clearInMemoryMapsLocked()
		}
		if mtime, ok := storageMtime(s.latticePath); ok {
			s.loadedMtime = mtime
		}
		// Clean up legacy fallback JSON file once migrated to native LatticeDB
		if s.filePath != s.latticePath {
			_ = os.Remove(s.filePath)
		}
		return nil
	}

	// 2. Fallback: Persist to atomic JSON when LatticeDB is not available (e.g. CGO-disabled builds)
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	var state serializedState
	for _, n := range s.nodes {
		state.Nodes = append(state.Nodes, n)
	}
	for _, edges := range s.outEdges {
		state.Edges = append(state.Edges, edges...)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	tmpFile := s.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmpFile, s.filePath); err != nil {
		return err
	}
	s.lastSaveMode, s.lastSaveOps = "full", s.journal.ops()
	s.dirty = false
	s.journal = nil
	if fi, statErr := os.Stat(s.filePath); statErr == nil {
		s.loadedMtime = fi.ModTime().UnixNano()
	}
	return nil
}

// saveNativeLocked writes the graph to the native LatticeDB file, replaying the
// journal when it is safe to do so and rebuilding the file otherwise. The
// caller holds the write lock.
func (s *GraphStorage) saveNativeLocked() (mode string, ops int, err error) {
	ops = s.journal.ops()

	rebuild := func() (string, int, error) {
		if s.lazyNative {
			if err := s.ensureLoadedLocked(); err != nil {
				return "", ops, err
			}
		}
		if err := saveToLatticeDB(s.latticePath, s.nodes, s.outEdges); err != nil {
			return "", ops, err
		}
		return "full", ops, nil
	}

	if s.journal == nil || s.journal.forceRebuild || ops == 0 {
		return rebuild()
	}
	// A missing file has to be created from scratch.
	if _, statErr := os.Stat(s.latticePath); statErr != nil {
		return rebuild()
	}

	err = applyJournalToLatticeDB(s.latticePath, s.journal, s.nodes)
	if err == nil {
		return "incremental", ops, nil
	}
	if errors.Is(err, errIncrementalUnavailable) {
		// The file cannot be updated in place (old database without the id
		// indexes, duplicate node ids, ...). One rebuild puts it back in shape.
		return rebuild()
	}
	// A lock conflict or a failed transaction leaves the file untouched, so
	// keep the journal and let the caller retry rather than clobbering a
	// database that another process may be writing.
	return "", ops, err
}

func (s *GraphStorage) IsDirty() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dirty
}

func storageMtime(path string) (int64, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	mtime := fi.ModTime().UnixNano()
	if walFi, walErr := os.Stat(path + "-wal"); walErr == nil {
		if walMtime := walFi.ModTime().UnixNano(); walMtime > mtime {
			mtime = walMtime
		}
	}
	return mtime, true
}

func (s *GraphStorage) ReloadIfModified() bool {
	activePath := s.filePath
	if HasNativeLatticeDB {
		if _, err := os.Stat(s.latticePath); err == nil {
			activePath = s.latticePath
		}
	}
	mtime, ok := storageMtime(activePath)
	if !ok {
		return false
	}
	s.mu.RLock()
	curMtime := s.loadedMtime
	isDirty := s.dirty
	s.mu.RUnlock()

	if !isDirty && mtime != curMtime {
		_ = s.Load()
		return true
	}
	return false
}

func (s *GraphStorage) ensureBM25Indexed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.ensureLoadedLocked()
	if s.bm25Loaded {
		return
	}
	s.bm25.Clear()
	for _, n := range s.nodes {
		s.indexNodeBM25(n)
	}
	s.bm25Loaded = true
}

func (s *GraphStorage) IsNativeLatticeDB() bool {
	return HasNativeLatticeDB
}

func (s *GraphStorage) DatabasePath() string {
	if HasNativeLatticeDB {
		return s.latticePath
	}
	return s.filePath
}

func (s *GraphStorage) DatabaseEngine() string {
	if HasNativeLatticeDB {
		return "LatticeDB v0.15.0 (Single-File Property Graph)"
	}
	return "Embedded Go Property-Graph"
}

func shortSymbolName(name string) string {
	if idx := strings.LastIndex(name, "."); idx != -1 {
		return name[idx+1:]
	}
	return name
}

func buildNodeSearchText(properties map[string]any) string {
	name, _ := properties["name"].(string)
	sig, _ := properties["signature"].(string)
	doc, _ := properties["docstring"].(string)
	fpath, _ := properties["file_path"].(string)
	if fpath == "" {
		fpath, _ = properties["path"].(string)
	}
	raw := fmt.Sprintf("%s %s %s %s", name, sig, doc, fpath)
	tokens := tokenize(raw)
	if len(tokens) == 0 {
		return ""
	}
	return strings.Join(tokens, " ")
}

func (s *GraphStorage) indexNodeBM25(n *NodeData) {
	name, _ := n.Properties["name"].(string)
	sig, _ := n.Properties["signature"].(string)
	doc, _ := n.Properties["docstring"].(string)
	fpath, _ := n.Properties["file_path"].(string)
	if fpath == "" {
		fpath, _ = n.Properties["path"].(string)
	}
	text := fmt.Sprintf("%s %s %s %s", name, sig, doc, fpath)
	s.bm25.IndexDocument(n.ID, text)
}

func (s *GraphStorage) indexNodeSecondaryLocked(n *NodeData) {
	if n == nil {
		return
	}
	lvl, _ := n.Properties["level"].(string)
	if lvl != "" {
		set, ok := s.byLevel[lvl]
		if !ok {
			set = make(map[string]struct{})
			s.byLevel[lvl] = set
		}
		set[n.ID] = struct{}{}
	}
	if lvl == string(models.LevelL1) {
		if p, _ := n.Properties["path"].(string); p != "" {
			s.byModulePath[p] = n.ID
		}
	}
	if name, _ := n.Properties["name"].(string); name != "" {
		s.addSymbolNameIndexLocked(name, n.ID)
		if short := shortSymbolName(name); short != "" && short != name {
			s.addSymbolNameIndexLocked(short, n.ID)
		}
	}
}

func (s *GraphStorage) addSymbolNameIndexLocked(key, id string) {
	set, ok := s.bySymbolName[key]
	if !ok {
		set = make(map[string]struct{})
		s.bySymbolName[key] = set
	}
	set[id] = struct{}{}
}

func (s *GraphStorage) unindexNodeLocked(n *NodeData) {
	if n == nil {
		return
	}
	if lvl, _ := n.Properties["level"].(string); lvl != "" {
		if set, ok := s.byLevel[lvl]; ok {
			delete(set, n.ID)
		}
		if lvl == string(models.LevelL1) {
			if p, _ := n.Properties["path"].(string); p != "" && s.byModulePath[p] == n.ID {
				delete(s.byModulePath, p)
			}
		}
	}
	if name, _ := n.Properties["name"].(string); name != "" {
		if set, ok := s.bySymbolName[name]; ok {
			delete(set, n.ID)
		}
		if short := shortSymbolName(name); short != "" {
			if set, ok := s.bySymbolName[short]; ok {
				delete(set, n.ID)
			}
		}
	}
}

func (s *GraphStorage) indexEdgeLocked(key, edgeType string) {
	s.edgeIndex[key] = struct{}{}
	set, ok := s.edgesByType[edgeType]
	if !ok {
		set = make(map[string]struct{})
		s.edgesByType[edgeType] = set
	}
	set[key] = struct{}{}
}

func (s *GraphStorage) unindexEdgeLocked(key, edgeType string) {
	delete(s.edgeIndex, key)
	if set, ok := s.edgesByType[edgeType]; ok {
		delete(set, key)
	}
}

// edgeKey identifies a directed, typed edge between two nodes.
func edgeKey(sourceID, targetID, edgeType string) string {
	return sourceID + "\x00" + edgeType + "\x00" + targetID
}

// rebuildIndexesLocked re-creates the secondary node and edge lookups.
func (s *GraphStorage) rebuildIndexesLocked() {
	s.edgeIndex = make(map[string]struct{}, len(s.outEdges))
	s.edgesByType = make(map[string]map[string]struct{})
	s.byLevel = make(map[string]map[string]struct{})
	s.bySymbolName = make(map[string]map[string]struct{})
	s.byModulePath = make(map[string]string)

	for _, n := range s.nodes {
		s.indexNodeSecondaryLocked(n)
	}
	for src, list := range s.outEdges {
		for _, e := range list {
			s.indexEdgeLocked(edgeKey(src, e.TargetID, e.EdgeType), e.EdgeType)
		}
	}
}

// markDirtyLocked records a graph mutation and invalidates derived caches.
func (s *GraphStorage) markDirtyLocked() {
	s.dirty = true
	s.countsValid = false
}

// journalLocked returns the journal, allocating it on first use.
func (s *GraphStorage) journalLocked() *graphJournal {
	if s.journal == nil {
		s.journal = newGraphJournal()
	}
	return s.journal
}

// journalOverflowLocked switches to a full rewrite once the delta is large
// enough that replaying it is no cheaper than rebuilding the file. The floor
// keeps small graphs on the incremental path.
func (s *GraphStorage) journalOverflowLocked() {
	j := s.journal
	if j == nil || j.forceRebuild {
		return
	}
	totalItems := len(s.nodes) + len(s.edgeIndex)
	if s.countsValid {
		if c := s.counts["total_nodes"] + s.counts["total_edges"]; c > totalItems {
			totalItems = c
		}
	}
	limit := totalItems / 4
	if limit < 2000 {
		limit = 2000
	}
	if j.ops() > limit {
		j.forceRebuild = true
	}
}

func (s *GraphStorage) journalNodeUpsertLocked(id string, labels []string) {
	j := s.journalLocked()
	j.nodes[id] = nodeMutation{labels: labels}
	s.journalOverflowLocked()
}

func (s *GraphStorage) journalNodeDeleteLocked(id string, labels []string) {
	j := s.journalLocked()
	j.nodes[id] = nodeMutation{labels: labels, delete: true}
	s.journalOverflowLocked()
}

func (s *GraphStorage) journalEdgeUpsertLocked(sourceID, targetID, edgeType string, properties map[string]any) {
	j := s.journalLocked()
	j.edges[edgeKey(sourceID, targetID, edgeType)] = edgeMutation{props: properties}
	s.journalOverflowLocked()
}

func (s *GraphStorage) journalEdgeDeleteLocked(sourceID, targetID, edgeType string) {
	j := s.journalLocked()
	j.edges[edgeKey(sourceID, targetID, edgeType)] = edgeMutation{delete: true}
	s.journalOverflowLocked()
}

// RequestFullSave makes the next Save rewrite the whole database file instead
// of applying the journal. Callers use it when they already know the graph was
// rebuilt from scratch, which is cheaper as one batch.
func (s *GraphStorage) RequestFullSave() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.journalLocked().forceRebuild = true
}

// LastSaveMode reports how the previous Save persisted the graph: "none",
// "full" or "incremental".
func (s *GraphStorage) LastSaveMode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastSaveMode
}

// LastSaveOps reports how many journal entries the previous Save carried.
func (s *GraphStorage) LastSaveOps() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastSaveOps
}

func (s *GraphStorage) CreateNode(id string, labels []string, properties map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.markDirtyLocked()
	propsCopy := make(map[string]any, len(properties)+2)
	for k, v := range properties {
		propsCopy[k] = v
	}
	propsCopy["id"] = id
	propsCopy["labels"] = labels

	if old, exists := s.nodes[id]; exists {
		s.unindexNodeLocked(old)
	}
	node := &NodeData{
		ID:         id,
		Labels:     labels,
		Properties: propsCopy,
	}
	s.nodes[id] = node
	if s.lazyNodeCache != nil {
		s.lazyNodeCache[id] = node
	}
	s.indexNodeSecondaryLocked(node)
	s.journalNodeUpsertLocked(id, labels)
	if s.bm25Loaded {
		s.indexNodeBM25(node)
	}
}

// HasNode reports whether a node with the given ID exists.
func (s *GraphStorage) HasNode(id string) bool {
	s.mu.RLock()
	if _, exists := s.nodes[id]; exists {
		s.mu.RUnlock()
		return true
	}
	if !s.lazyNative {
		s.mu.RUnlock()
		return false
	}
	if s.journal != nil {
		if mut, ok := s.journal.nodes[id]; ok && mut.delete {
			s.mu.RUnlock()
			return false
		}
	}
	if s.lazyNodeCache != nil {
		if _, ok := s.lazyNodeCache[id]; ok {
			s.mu.RUnlock()
			return true
		}
	}
	latticePath := s.latticePath
	s.mu.RUnlock()

	return hasNodeInLatticeDB(latticePath, id)
}

func (s *GraphStorage) GetNode(id string) (map[string]any, bool) {
	s.mu.RLock()
	if node, exists := s.nodes[id]; exists {
		res := copyProperties(node.Properties)
		s.mu.RUnlock()
		return res, true
	}
	if !s.lazyNative {
		s.mu.RUnlock()
		return nil, false
	}
	if s.journal != nil {
		if mut, ok := s.journal.nodes[id]; ok && mut.delete {
			s.mu.RUnlock()
			return nil, false
		}
	}
	if s.lazyNodeCache != nil {
		if cached, ok := s.lazyNodeCache[id]; ok {
			res := copyProperties(cached.Properties)
			s.mu.RUnlock()
			return res, true
		}
	}
	latticePath := s.latticePath
	s.mu.RUnlock()

	node, ok := getNodeFromLatticeDB(latticePath, id)
	if !ok || node == nil {
		return nil, false
	}

	s.mu.Lock()
	if s.lazyNative && s.lazyNodeCache != nil {
		s.lazyNodeCache[id] = node
	}
	s.mu.Unlock()
	return copyProperties(node.Properties), true
}

func (s *GraphStorage) DeleteNode(id string) {
	s.DeleteNodes([]string{id})
}

func (s *GraphStorage) deleteNodeLocked(id string) {
	existing, exists := s.nodes[id]
	if !exists {
		return
	}

	s.markDirtyLocked()
	s.unindexNodeLocked(existing)
	delete(s.nodes, id)
	if s.lazyNodeCache != nil {
		delete(s.lazyNodeCache, id)
	}
	if s.bm25Loaded {
		s.bm25.RemoveDocument(id)
	}

	// LatticeDB does not cascade edge removal, so every incident edge is
	// journaled for deletion before the node itself goes away.
	s.journalNodeDeleteLocked(id, existing.Labels)
	s.removeIncidentEdgesInMemoryLocked(id)
}

func (s *GraphStorage) removeIncidentEdgesInMemoryLocked(id string) {
	if outList, ok := s.outEdges[id]; ok {
		for _, e := range outList {
			s.unindexEdgeLocked(edgeKey(id, e.TargetID, e.EdgeType), e.EdgeType)
			s.journalEdgeDeleteLocked(id, e.TargetID, e.EdgeType)
			inList := s.inEdges[e.TargetID]
			var updated []models.GraphEdge
			for _, inE := range inList {
				if inE.SourceID != id {
					updated = append(updated, inE)
				}
			}
			s.inEdges[e.TargetID] = updated
		}
		delete(s.outEdges, id)
	}

	if inList, ok := s.inEdges[id]; ok {
		for _, e := range inList {
			s.unindexEdgeLocked(edgeKey(e.SourceID, id, e.EdgeType), e.EdgeType)
			s.journalEdgeDeleteLocked(e.SourceID, id, e.EdgeType)
			outList := s.outEdges[e.SourceID]
			var updated []models.GraphEdge
			for _, outE := range outList {
				if outE.TargetID != id {
					updated = append(updated, outE)
				}
			}
			s.outEdges[e.SourceID] = updated
		}
		delete(s.inEdges, id)
	}
}

// DeleteNodes removes several nodes by ID. Callers that already know the node
// IDs (for example from the cache state) should prefer this over
// DeleteFileSubnodes, which has to scan every node in the graph.
func (s *GraphStorage) DeleteNodes(ids []string) {
	if len(ids) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.lazyNative {
		for _, id := range ids {
			s.deleteNodeLocked(id)
		}
		return
	}

	var checkDiskIDs []string
	for _, id := range ids {
		if s.journal != nil {
			if mut, ok := s.journal.nodes[id]; ok && mut.delete {
				continue
			}
		}
		checkDiskIDs = append(checkDiskIDs, id)
	}
	snaps := inspectNodesInLatticeDB(s.latticePath, checkDiskIDs)

	for _, id := range ids {
		existing, inMem := s.nodes[id]
		snap, onDisk := snaps[id]
		if s.journal != nil {
			if mut, ok := s.journal.nodes[id]; ok && mut.delete {
				onDisk = false
			}
		}
		if !inMem && !onDisk {
			continue
		}

		s.markDirtyLocked()
		var labels []string
		if inMem {
			labels = existing.Labels
			s.unindexNodeLocked(existing)
			delete(s.nodes, id)
		} else {
			labels = snap.labels
		}
		if s.lazyNodeCache != nil {
			delete(s.lazyNodeCache, id)
		}
		if s.bm25Loaded {
			s.bm25.RemoveDocument(id)
		}

		s.journalNodeDeleteLocked(id, labels)
		s.removeIncidentEdgesInMemoryLocked(id)

		if onDisk {
			for _, e := range snap.outEdges {
				s.journalEdgeDeleteLocked(id, e.TargetID, e.EdgeType)
			}
			for _, e := range snap.inEdges {
				s.journalEdgeDeleteLocked(e.SourceID, id, e.EdgeType)
			}
		}
	}
}

func (s *GraphStorage) DeleteFileSubnodes(filePath string) {
	s.mu.Lock()
	_ = s.ensureLoadedLocked()
	var toDelete []string
	for id, n := range s.nodes {
		fPath, _ := n.Properties["file_path"].(string)
		path, _ := n.Properties["path"].(string)
		if fPath == filePath || path == filePath {
			toDelete = append(toDelete, id)
		}
	}
	for _, id := range toDelete {
		s.deleteNodeLocked(id)
	}
	s.mu.Unlock()
}

func (s *GraphStorage) CreateEdge(sourceID, targetID, edgeType string, properties map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := edgeKey(sourceID, targetID, edgeType)
	if _, exists := s.edgeIndex[key]; exists {
		// The edge is already present: only rewrite it when its payload really
		// changed. Re-asserting identical edges (for example the L0 -> L1
		// domain links) must not mark the graph dirty, otherwise every sync
		// rewrites the whole database file.
		changed, found := s.updateEdgeLocked(sourceID, targetID, edgeType, properties)
		if found {
			if changed {
				s.markDirtyLocked()
				s.journalEdgeUpsertLocked(sourceID, targetID, edgeType, copyProperties(properties))
			}
			return
		}
		s.unindexEdgeLocked(key, edgeType)
	}

	if s.lazyNative {
		// If this edge has not been touched in the current session, check
		// whether it already exists on disk with identical properties.
		inJournal := false
		if s.journal != nil {
			if _, ok := s.journal.edges[key]; ok {
				inJournal = true
			}
		}
		if !inJournal {
			if diskProps, existsOnDisk := getEdgePropsFromLatticeDB(s.latticePath, sourceID, targetID, edgeType); existsOnDisk {
				propsCopy := copyProperties(properties)
				edge := models.GraphEdge{
					SourceID:   sourceID,
					TargetID:   targetID,
					EdgeType:   edgeType,
					Properties: propsCopy,
				}
				s.outEdges[sourceID] = append(s.outEdges[sourceID], edge)
				s.inEdges[targetID] = append(s.inEdges[targetID], edge)
				s.indexEdgeLocked(key, edgeType)
				if propertiesEqual(diskProps, propsCopy) {
					return
				}
				s.markDirtyLocked()
				s.journalEdgeUpsertLocked(sourceID, targetID, edgeType, propsCopy)
				return
			}
		}
	}

	s.markDirtyLocked()
	edge := models.GraphEdge{
		SourceID:   sourceID,
		TargetID:   targetID,
		EdgeType:   edgeType,
		Properties: copyProperties(properties),
	}
	s.outEdges[sourceID] = append(s.outEdges[sourceID], edge)
	s.inEdges[targetID] = append(s.inEdges[targetID], edge)
	s.indexEdgeLocked(key, edgeType)
	s.journalEdgeUpsertLocked(sourceID, targetID, edgeType, edge.Properties)
}

// updateEdgeLocked rewrites the properties of an existing edge. It reports
// whether the edge was found and whether its properties actually changed.
func (s *GraphStorage) updateEdgeLocked(sourceID, targetID, edgeType string, properties map[string]any) (changed, found bool) {
	propsCopy := copyProperties(properties)
	list, ok := s.outEdges[sourceID]
	if !ok {
		return false, false
	}
	for i := range list {
		if list[i].TargetID != targetID || list[i].EdgeType != edgeType {
			continue
		}
		if propertiesEqual(list[i].Properties, propsCopy) {
			return false, true
		}
		list[i].Properties = propsCopy
		if inList, ok := s.inEdges[targetID]; ok {
			for j := range inList {
				if inList[j].SourceID == sourceID && inList[j].EdgeType == edgeType {
					inList[j].Properties = propsCopy
					break
				}
			}
		}
		return true, true
	}
	return false, false
}

func copyProperties(properties map[string]any) map[string]any {
	cp := make(map[string]any, len(properties))
	for k, v := range properties {
		cp[k] = v
	}
	return cp
}

func propertiesEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || !propertyValueEqual(av, bv) {
			return false
		}
	}
	return true
}

// propertyValueEqual compares two property values while tolerating the type
// conversions introduced by persistence (e.g. []string loaded back as []any, or
// int loaded back as int64).
func propertyValueEqual(a, b any) bool {
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case []string:
		switch bv := b.(type) {
		case []string:
			if len(av) != len(bv) {
				return false
			}
			for i := range av {
				if av[i] != bv[i] {
					return false
				}
			}
			return true
		case []any:
			if len(av) != len(bv) {
				return false
			}
			for i := range av {
				s, ok := bv[i].(string)
				if !ok || av[i] != s {
					return false
				}
			}
			return true
		}
	case []any:
		switch bv := b.(type) {
		case []string:
			if len(av) != len(bv) {
				return false
			}
			for i := range av {
				s, ok := av[i].(string)
				if !ok || s != bv[i] {
					return false
				}
			}
			return true
		case []any:
			if len(av) != len(bv) {
				return false
			}
			for i := range av {
				if !propertyValueEqual(av[i], bv[i]) {
					return false
				}
			}
			return true
		}
	case int:
		if bv, ok := toInt64(b); ok {
			return int64(av) == bv
		}
	case int64:
		if bv, ok := toInt64(b); ok {
			return av == bv
		}
	case int32:
		if bv, ok := toInt64(b); ok {
			return int64(av) == bv
		}
	case map[string]any:
		if bv, ok := b.(map[string]any); ok {
			return propertiesEqual(av, bv)
		}
	}
	return reflect.DeepEqual(a, b)
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case int32:
		return int64(n), true
	default:
		return 0, false
	}
}

func (s *GraphStorage) DeleteEdgesByType(edgeTypes ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_ = s.ensureLoadedLocked()

	typesSet := make(map[string]bool)
	for _, t := range edgeTypes {
		typesSet[t] = true
	}

	removed := false
	for src, list := range s.outEdges {
		var updated []models.GraphEdge
		for _, e := range list {
			if typesSet[e.EdgeType] {
				s.unindexEdgeLocked(edgeKey(src, e.TargetID, e.EdgeType), e.EdgeType)
				s.journalEdgeDeleteLocked(src, e.TargetID, e.EdgeType)
				removed = true
				continue
			}
			updated = append(updated, e)
		}
		s.outEdges[src] = updated
	}

	for tgt, list := range s.inEdges {
		var updated []models.GraphEdge
		for _, e := range list {
			if !typesSet[e.EdgeType] {
				updated = append(updated, e)
			}
		}
		s.inEdges[tgt] = updated
	}

	if removed {
		s.markDirtyLocked()
	}
}

// DeleteEdgesForSource removes outgoing edges from sourceID matching the specified types (or all if empty).
func (s *GraphStorage) DeleteEdgesForSource(sourceID string, edgeTypes ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	typesSet := make(map[string]bool)
	for _, t := range edgeTypes {
		typesSet[t] = true
	}

	removed := false
	if outList, ok := s.outEdges[sourceID]; ok {
		var remaining []models.GraphEdge
		for _, e := range outList {
			if len(typesSet) == 0 || typesSet[e.EdgeType] {
				s.unindexEdgeLocked(edgeKey(sourceID, e.TargetID, e.EdgeType), e.EdgeType)
				s.journalEdgeDeleteLocked(sourceID, e.TargetID, e.EdgeType)
				removed = true
				inList := s.inEdges[e.TargetID]
				var updatedIn []models.GraphEdge
				for _, inE := range inList {
					if !(inE.SourceID == sourceID && inE.EdgeType == e.EdgeType) {
						updatedIn = append(updatedIn, inE)
					}
				}
				s.inEdges[e.TargetID] = updatedIn
			} else {
				remaining = append(remaining, e)
			}
		}
		s.outEdges[sourceID] = remaining
	}

	if s.lazyNative {
		diskEdges, _ := getOutgoingEdgesFromLatticeDB(s.latticePath, sourceID, "")
		for _, e := range diskEdges {
			if len(typesSet) == 0 || typesSet[e.EdgeType] {
				key := edgeKey(sourceID, e.TargetID, e.EdgeType)
				if s.journal != nil {
					if mut, ok := s.journal.edges[key]; ok && mut.delete {
						continue
					}
				}
				s.journalEdgeDeleteLocked(sourceID, e.TargetID, e.EdgeType)
				removed = true
			}
		}
	}

	if removed {
		s.markDirtyLocked()
	}
}

// DeleteEdgesForTarget removes incoming edges to targetID matching the specified types (or all if empty).
func (s *GraphStorage) DeleteEdgesForTarget(targetID string, edgeTypes ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	typesSet := make(map[string]bool)
	for _, t := range edgeTypes {
		typesSet[t] = true
	}

	removed := false
	if inList, ok := s.inEdges[targetID]; ok {
		var remaining []models.GraphEdge
		for _, e := range inList {
			if len(typesSet) == 0 || typesSet[e.EdgeType] {
				s.unindexEdgeLocked(edgeKey(e.SourceID, targetID, e.EdgeType), e.EdgeType)
				s.journalEdgeDeleteLocked(e.SourceID, targetID, e.EdgeType)
				removed = true
				outList := s.outEdges[e.SourceID]
				var updatedOut []models.GraphEdge
				for _, outE := range outList {
					if !(outE.TargetID == targetID && outE.EdgeType == e.EdgeType) {
						updatedOut = append(updatedOut, outE)
					}
				}
				s.outEdges[e.SourceID] = updatedOut
			} else {
				remaining = append(remaining, e)
			}
		}
		s.inEdges[targetID] = remaining
	}

	if s.lazyNative {
		diskEdges, _ := getIncomingEdgesFromLatticeDB(s.latticePath, targetID, "")
		for _, e := range diskEdges {
			if len(typesSet) == 0 || typesSet[e.EdgeType] {
				key := edgeKey(e.SourceID, targetID, e.EdgeType)
				if s.journal != nil {
					if mut, ok := s.journal.edges[key]; ok && mut.delete {
						continue
					}
				}
				s.journalEdgeDeleteLocked(e.SourceID, targetID, e.EdgeType)
				removed = true
			}
		}
	}

	if removed {
		s.markDirtyLocked()
	}
}

func (s *GraphStorage) cacheNeighborNodes(neighbors []*NodeData) {
	if len(neighbors) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.lazyNative || s.lazyNodeCache == nil {
		return
	}
	for _, n := range neighbors {
		if n != nil && n.ID != "" {
			s.lazyNodeCache[n.ID] = n
		}
	}
}

func (s *GraphStorage) GetOutgoingEdges(nodeID string, edgeType string) []models.GraphEdge {
	s.mu.RLock()
	if s.lazyNative && !s.dirty {
		latticePath := s.latticePath
		s.mu.RUnlock()
		edges, neighbors := getOutgoingEdgesFromLatticeDB(latticePath, nodeID, edgeType)
		s.cacheNeighborNodes(neighbors)
		return edges
	}
	if s.lazyNative && s.dirty {
		s.mu.RUnlock()
		s.mu.Lock()
		_ = s.ensureLoadedLocked()
		s.mu.Unlock()
		s.mu.RLock()
	}
	defer s.mu.RUnlock()

	var result []models.GraphEdge
	for _, e := range s.outEdges[nodeID] {
		if edgeType == "" || e.EdgeType == edgeType {
			result = append(result, e)
		}
	}
	return result
}

func (s *GraphStorage) GetIncomingEdges(nodeID string, edgeType string) []models.GraphEdge {
	s.mu.RLock()
	if s.lazyNative && !s.dirty {
		latticePath := s.latticePath
		s.mu.RUnlock()
		edges, neighbors := getIncomingEdgesFromLatticeDB(latticePath, nodeID, edgeType)
		s.cacheNeighborNodes(neighbors)
		return edges
	}
	if s.lazyNative && s.dirty {
		s.mu.RUnlock()
		s.mu.Lock()
		_ = s.ensureLoadedLocked()
		s.mu.Unlock()
		s.mu.RLock()
	}
	defer s.mu.RUnlock()

	var result []models.GraphEdge
	for _, e := range s.inEdges[nodeID] {
		if edgeType == "" || e.EdgeType == edgeType {
			result = append(result, e)
		}
	}
	return result
}

func (s *GraphStorage) GetEdgesByType(edgeType string) []models.GraphEdge {
	s.mu.RLock()
	if s.lazyNative && !s.dirty {
		latticePath := s.latticePath
		s.mu.RUnlock()
		return getEdgesByTypeFromLatticeDB(latticePath, edgeType)
	}
	if s.lazyNative && s.dirty {
		s.mu.RUnlock()
		s.mu.Lock()
		_ = s.ensureLoadedLocked()
		s.mu.Unlock()
		s.mu.RLock()
	}
	defer s.mu.RUnlock()

	var result []models.GraphEdge
	for _, edges := range s.outEdges {
		for _, e := range edges {
			if edgeType == "" || e.EdgeType == edgeType {
				result = append(result, e)
			}
		}
	}
	return result
}

func (s *GraphStorage) FTSSearch(queryText string, limit int) []map[string]any {
	s.mu.RLock()
	isLazy := s.lazyNative && !s.dirty
	latticePath := s.latticePath
	s.mu.RUnlock()

	if isLazy {
		if results, ok := ftsSearchInLatticeDB(latticePath, queryText, limit); ok {
			return results
		}
	}

	s.ensureBM25Indexed()
	matches := s.bm25.Search(queryText, limit)
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []map[string]any
	for _, m := range matches {
		if node, exists := s.nodes[m.DocID]; exists {
			res := make(map[string]any, len(node.Properties)+1)
			for k, v := range node.Properties {
				res[k] = v
			}
			res["_rank"] = m.Score
			results = append(results, res)
		}
	}
	return results
}

func (s *GraphStorage) FindNodesByLabelProperty(label string, key string, value any, limit int) []map[string]any {
	s.mu.RLock()
	if s.lazyNative && !s.dirty {
		latticePath := s.latticePath
		s.mu.RUnlock()
		if nodes, supported := findNodesByLabelPropertyInLatticeDB(latticePath, label, key, value, limit); supported {
			s.cacheNeighborNodes(nodes)
			results := make([]map[string]any, 0, len(nodes))
			for _, n := range nodes {
				results = append(results, copyProperties(n.Properties))
			}
			return results
		}
		s.mu.Lock()
		_ = s.ensureLoadedLocked()
		s.mu.Unlock()
		s.mu.RLock()
	} else if s.lazyNative && s.dirty {
		s.mu.RUnlock()
		s.mu.Lock()
		_ = s.ensureLoadedLocked()
		s.mu.Unlock()
		s.mu.RLock()
	}
	defer s.mu.RUnlock()

	if label == "L1Module" && key == "path" {
		if pathStr, ok := value.(string); ok {
			if id, exists := s.byModulePath[pathStr]; exists {
				if n, ok := s.nodes[id]; ok {
					return []map[string]any{copyProperties(n.Properties)}
				}
			}
			return nil
		}
	}

	var results []map[string]any
	for _, n := range s.nodes {
		hasLabel := false
		for _, l := range n.Labels {
			if l == label {
				hasLabel = true
				break
			}
		}
		if hasLabel && n.Properties[key] == value {
			results = append(results, copyProperties(n.Properties))
			if limit > 0 && len(results) >= limit {
				break
			}
		}
	}
	return results
}

// FindSymbolsByName looks up symbols (and classes) whose name equals name or
// ends with "."+name, optionally filtered by filePath. It uses B-Tree indexes
// in native mode and the in-memory symbol index when hydrated.
func (s *GraphStorage) FindSymbolsByName(name string, filePath string) []map[string]any {
	if name == "" {
		return nil
	}
	s.mu.RLock()
	if s.lazyNative && !s.dirty {
		latticePath := s.latticePath
		s.mu.RUnlock()
		nodes := findSymbolsByNameInLatticeDB(latticePath, name, filePath)
		s.cacheNeighborNodes(nodes)
		results := make([]map[string]any, 0, len(nodes))
		for _, n := range nodes {
			results = append(results, copyProperties(n.Properties))
		}
		return results
	}
	if s.lazyNative && s.dirty {
		s.mu.RUnlock()
		s.mu.Lock()
		_ = s.ensureLoadedLocked()
		s.mu.Unlock()
		s.mu.RLock()
	}
	defer s.mu.RUnlock()

	short := shortSymbolName(name)
	candidateIDs := s.bySymbolName[short]
	if len(candidateIDs) == 0 {
		candidateIDs = s.bySymbolName[name]
	}

	var results []map[string]any
	for id := range candidateIDs {
		n, ok := s.nodes[id]
		if !ok {
			continue
		}
		nName, _ := n.Properties["name"].(string)
		if nName != name && !strings.HasSuffix(nName, "."+name) {
			continue
		}
		if filePath != "" {
			fPath, _ := n.Properties["file_path"].(string)
			if fPath != filePath {
				continue
			}
		}
		results = append(results, copyProperties(n.Properties))
	}
	return results
}

func (s *GraphStorage) QueryNodes(level string, domain string, limit int) []map[string]any {
	s.mu.RLock()
	if s.lazyNative && !s.dirty {
		latticePath := s.latticePath
		s.mu.RUnlock()
		nodes := queryNodesFromLatticeDB(latticePath, level, domain, limit)
		s.cacheNeighborNodes(nodes)
		results := make([]map[string]any, 0, len(nodes))
		for _, n := range nodes {
			results = append(results, copyProperties(n.Properties))
		}
		return results
	}
	if s.lazyNative && s.dirty {
		s.mu.RUnlock()
		s.mu.Lock()
		_ = s.ensureLoadedLocked()
		s.mu.Unlock()
		s.mu.RLock()
	}
	defer s.mu.RUnlock()

	var results []map[string]any
	if level != "" {
		for id := range s.byLevel[level] {
			n, ok := s.nodes[id]
			if !ok {
				continue
			}
			if domain != "" {
				dom, _ := n.Properties["domain"].(string)
				if dom != domain {
					continue
				}
			}
			results = append(results, copyProperties(n.Properties))
			if limit > 0 && len(results) >= limit {
				break
			}
		}
		return results
	}

	for _, n := range s.nodes {
		if domain != "" {
			dom, _ := n.Properties["domain"].(string)
			if dom != domain {
				continue
			}
		}
		results = append(results, copyProperties(n.Properties))
		if limit > 0 && len(results) >= limit {
			break
		}
	}
	return results
}

func (s *GraphStorage) CountNodesAndEdges() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.countsValid {
		if s.lazyNative && !s.dirty {
			if counts, ok := countNodesAndEdgesInLatticeDB(s.latticePath); ok {
				s.counts = counts
				s.countsValid = true
			} else {
				_ = s.ensureLoadedLocked()
			}
		} else if s.lazyNative && s.dirty {
			_ = s.ensureLoadedLocked()
		}
	}

	if !s.countsValid {
		l0 := len(s.byLevel[string(models.LevelL0)])
		l1 := len(s.byLevel[string(models.LevelL1)])
		l2 := len(s.byLevel[string(models.LevelL2)])
		l3 := len(s.byLevel[string(models.LevelL3)])

		s.counts = map[string]int{
			"total_nodes": len(s.nodes),
			"total_edges": len(s.edgeIndex),
			"l0_domains":  l0,
			"l1_modules":  l1,
			"l2_classes":  l2,
			"l3_symbols":  l3,
		}
		s.countsValid = true
	}

	out := make(map[string]int, len(s.counts))
	for k, v := range s.counts {
		out[k] = v
	}
	return out
}

// RangeNodes visits every node without copying its properties. The map passed
// to fn is owned by the graph and must only be read, and fn must not call back
// into GraphStorage (the read lock is held for the duration of the walk).
func (s *GraphStorage) RangeNodes(fn func(id string, props map[string]any)) {
	s.mu.RLock()
	if s.lazyNative {
		s.mu.RUnlock()
		s.mu.Lock()
		_ = s.ensureLoadedLocked()
		s.mu.Unlock()
		s.mu.RLock()
	}
	defer s.mu.RUnlock()

	for id, n := range s.nodes {
		fn(id, n.Properties)
	}
}

func (s *GraphStorage) AllNodes() map[string]map[string]any {
	s.mu.RLock()
	if s.lazyNative {
		s.mu.RUnlock()
		s.mu.Lock()
		_ = s.ensureLoadedLocked()
		s.mu.Unlock()
		s.mu.RLock()
	}
	defer s.mu.RUnlock()

	all := make(map[string]map[string]any, len(s.nodes))
	for id, n := range s.nodes {
		all[id] = copyProperties(n.Properties)
	}
	return all
}

func (s *GraphStorage) AllEdges() []models.GraphEdge {
	s.mu.RLock()
	if s.lazyNative {
		s.mu.RUnlock()
		s.mu.Lock()
		_ = s.ensureLoadedLocked()
		s.mu.Unlock()
		s.mu.RLock()
	}
	defer s.mu.RUnlock()

	var edges []models.GraphEdge
	for _, list := range s.outEdges {
		edges = append(edges, list...)
	}
	return edges
}

func (s *GraphStorage) Close() error {
	return s.Save()
}
