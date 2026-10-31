package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"meta-lattice/src/models"
)

type NodeData struct {
	ID         string         `json:"id"`
	Labels     []string       `json:"labels"`
	Properties map[string]any `json:"properties"`
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
	bm25      *BM25Index
	// counts memoizes CountNodesAndEdges; any mutation invalidates it.
	counts      map[string]int
	countsValid bool
	bm25Loaded  bool
	dirty       bool
	loadedMtime int64
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
		nodes:       make(map[string]*NodeData),
		outEdges:    make(map[string][]models.GraphEdge),
		inEdges:     make(map[string][]models.GraphEdge),
		edgeIndex:   make(map[string]struct{}),
		bm25:        NewBM25Index(),
	}
	if err := gs.Load(); err != nil && !os.IsNotExist(err) {
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

func (s *GraphStorage) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Try loading from native LatticeDB file (.lattice) if it exists
	if HasNativeLatticeDB {
		if fi, statErr := os.Stat(s.latticePath); statErr == nil {
			nodes, outEdges, inEdges, err := loadFromLatticeDB(s.latticePath)
			if err != nil {
				return fmt.Errorf("corrupt LatticeDB file: %w", err)
			}
			s.nodes = nodes
			s.outEdges = outEdges
			s.inEdges = inEdges
			s.rebuildEdgeIndexLocked()
			s.bm25.Clear()
			s.bm25Loaded = false
			s.dirty = false
			s.countsValid = false
			s.loadedMtime = fi.ModTime().UnixNano()
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

	s.nodes = make(map[string]*NodeData)
	s.outEdges = make(map[string][]models.GraphEdge)
	s.inEdges = make(map[string][]models.GraphEdge)
	s.bm25.Clear()
	s.bm25Loaded = false
	s.dirty = false
	if fi, statErr := os.Stat(s.filePath); statErr == nil {
		s.loadedMtime = fi.ModTime().UnixNano()
	}

	for _, n := range state.Nodes {
		s.nodes[n.ID] = n
	}

	for _, e := range state.Edges {
		s.outEdges[e.SourceID] = append(s.outEdges[e.SourceID], e)
		s.inEdges[e.TargetID] = append(s.inEdges[e.TargetID], e)
	}
	s.rebuildEdgeIndexLocked()
	s.countsValid = false

	if HasNativeLatticeDB {
		// Loaded from fallback JSON but native LatticeDB is available:
		// mark dirty to auto-migrate to .lattice on next save.
		s.dirty = true
	}

	return nil
}

func (s *GraphStorage) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.dirty {
		return nil
	}

	// 1. If native LatticeDB is compiled in, persist exclusively to .lattice
	if HasNativeLatticeDB {
		err := saveToLatticeDB(s.latticePath, s.nodes, s.outEdges)
		if err == nil {
			s.dirty = false
			if fi, statErr := os.Stat(s.latticePath); statErr == nil {
				s.loadedMtime = fi.ModTime().UnixNano()
			}
			// Clean up legacy fallback JSON file once migrated to native LatticeDB
			if s.filePath != s.latticePath {
				_ = os.Remove(s.filePath)
			}
		}
		return err
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
	s.dirty = false
	if fi, statErr := os.Stat(s.filePath); statErr == nil {
		s.loadedMtime = fi.ModTime().UnixNano()
	}
	return nil
}

func (s *GraphStorage) IsDirty() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dirty
}

func (s *GraphStorage) ReloadIfModified() bool {
	activePath := s.filePath
	if HasNativeLatticeDB {
		if _, err := os.Stat(s.latticePath); err == nil {
			activePath = s.latticePath
		}
	}
	fi, err := os.Stat(activePath)
	if err != nil {
		return false
	}
	s.mu.RLock()
	curMtime := s.loadedMtime
	isDirty := s.dirty
	s.mu.RUnlock()

	if !isDirty && fi.ModTime().UnixNano() != curMtime {
		_ = s.Load()
		return true
	}
	return false
}

func (s *GraphStorage) ensureBM25Indexed() {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// edgeKey identifies a directed, typed edge between two nodes.
func edgeKey(sourceID, targetID, edgeType string) string {
	return sourceID + "\x00" + edgeType + "\x00" + targetID
}

// rebuildEdgeIndexLocked re-creates the edge lookup from the edge lists.
func (s *GraphStorage) rebuildEdgeIndexLocked() {
	s.edgeIndex = make(map[string]struct{}, len(s.outEdges))
	for src, list := range s.outEdges {
		for _, e := range list {
			s.edgeIndex[edgeKey(src, e.TargetID, e.EdgeType)] = struct{}{}
		}
	}
}

// markDirtyLocked records a graph mutation and invalidates derived caches.
func (s *GraphStorage) markDirtyLocked() {
	s.dirty = true
	s.countsValid = false
}

func (s *GraphStorage) CreateNode(id string, labels []string, properties map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.markDirtyLocked()
	propsCopy := make(map[string]any, len(properties))
	for k, v := range properties {
		propsCopy[k] = v
	}
	propsCopy["id"] = id
	propsCopy["labels"] = labels

	node := &NodeData{
		ID:         id,
		Labels:     labels,
		Properties: propsCopy,
	}
	s.nodes[id] = node
	if s.bm25Loaded {
		s.indexNodeBM25(node)
	}
}

// HasNode reports whether a node with the given ID exists.
func (s *GraphStorage) HasNode(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, exists := s.nodes[id]
	return exists
}

func (s *GraphStorage) GetNode(id string) (map[string]any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	node, exists := s.nodes[id]
	if !exists {
		return nil, false
	}
	res := make(map[string]any, len(node.Properties))
	for k, v := range node.Properties {
		res[k] = v
	}
	return res, true
}

func (s *GraphStorage) DeleteNode(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.markDirtyLocked()
	delete(s.nodes, id)
	if s.bm25Loaded {
		s.bm25.RemoveDocument(id)
	}

	// Remove outgoing edges
	if outList, ok := s.outEdges[id]; ok {
		for _, e := range outList {
			delete(s.edgeIndex, edgeKey(id, e.TargetID, e.EdgeType))
			// Remove from target's inEdges
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

	// Remove incoming edges
	if inList, ok := s.inEdges[id]; ok {
		for _, e := range inList {
			delete(s.edgeIndex, edgeKey(e.SourceID, id, e.EdgeType))
			// Remove from source's outEdges
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
	for _, id := range ids {
		s.DeleteNode(id)
	}
}

func (s *GraphStorage) DeleteFileSubnodes(filePath string) {
	s.mu.RLock()
	var toDelete []string
	for id, n := range s.nodes {
		fPath, _ := n.Properties["file_path"].(string)
		path, _ := n.Properties["path"].(string)
		if fPath == filePath || path == filePath {
			toDelete = append(toDelete, id)
		}
	}
	s.mu.RUnlock()

	for _, id := range toDelete {
		s.DeleteNode(id)
	}
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
			}
			return
		}
		delete(s.edgeIndex, key)
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
	s.edgeIndex[key] = struct{}{}
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
		if !ok || !reflect.DeepEqual(av, bv) {
			return false
		}
	}
	return true
}

func (s *GraphStorage) DeleteEdgesByType(edgeTypes ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.markDirtyLocked()
	typesSet := make(map[string]bool)
	for _, t := range edgeTypes {
		typesSet[t] = true
	}

	for src, list := range s.outEdges {
		var updated []models.GraphEdge
		for _, e := range list {
			if typesSet[e.EdgeType] {
				delete(s.edgeIndex, edgeKey(src, e.TargetID, e.EdgeType))
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
}

// DeleteEdgesForSource removes outgoing edges from sourceID matching the specified types (or all if empty).
func (s *GraphStorage) DeleteEdgesForSource(sourceID string, edgeTypes ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.markDirtyLocked()
	typesSet := make(map[string]bool)
	for _, t := range edgeTypes {
		typesSet[t] = true
	}

	if outList, ok := s.outEdges[sourceID]; ok {
		var remaining []models.GraphEdge
		for _, e := range outList {
			if len(typesSet) == 0 || typesSet[e.EdgeType] {
				delete(s.edgeIndex, edgeKey(sourceID, e.TargetID, e.EdgeType))
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
}

// DeleteEdgesForTarget removes incoming edges to targetID matching the specified types (or all if empty).
func (s *GraphStorage) DeleteEdgesForTarget(targetID string, edgeTypes ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.markDirtyLocked()
	typesSet := make(map[string]bool)
	for _, t := range edgeTypes {
		typesSet[t] = true
	}

	if inList, ok := s.inEdges[targetID]; ok {
		var remaining []models.GraphEdge
		for _, e := range inList {
			if len(typesSet) == 0 || typesSet[e.EdgeType] {
				delete(s.edgeIndex, edgeKey(e.SourceID, targetID, e.EdgeType))
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
}

func (s *GraphStorage) GetOutgoingEdges(nodeID string, edgeType string) []models.GraphEdge {
	s.mu.RLock()
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
	defer s.mu.RUnlock()

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
			res := make(map[string]any, len(n.Properties))
			for k, v := range n.Properties {
				res[k] = v
			}
			results = append(results, res)
			if limit > 0 && len(results) >= limit {
				break
			}
		}
	}
	return results
}

func (s *GraphStorage) QueryNodes(level string, domain string, limit int) []map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []map[string]any
	for _, n := range s.nodes {
		if level != "" {
			lvl, _ := n.Properties["level"].(string)
			if lvl != level {
				continue
			}
		}
		if domain != "" {
			dom, _ := n.Properties["domain"].(string)
			if dom != domain {
				continue
			}
		}
		res := make(map[string]any, len(n.Properties))
		for k, v := range n.Properties {
			res[k] = v
		}
		results = append(results, res)
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
		l0, l1, l2, l3 := 0, 0, 0, 0
		for _, n := range s.nodes {
			lvl, _ := n.Properties["level"].(string)
			switch lvl {
			case string(models.LevelL0):
				l0++
			case string(models.LevelL1):
				l1++
			case string(models.LevelL2):
				l2++
			case string(models.LevelL3):
				l3++
			}
		}

		totalEdges := 0
		for _, list := range s.outEdges {
			totalEdges += len(list)
		}

		s.counts = map[string]int{
			"total_nodes": len(s.nodes),
			"total_edges": totalEdges,
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
	defer s.mu.RUnlock()

	for id, n := range s.nodes {
		fn(id, n.Properties)
	}
}

func (s *GraphStorage) AllNodes() map[string]map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	all := make(map[string]map[string]any, len(s.nodes))
	for id, n := range s.nodes {
		cp := make(map[string]any, len(n.Properties))
		for k, v := range n.Properties {
			cp[k] = v
		}
		all[id] = cp
	}
	return all
}

func (s *GraphStorage) AllEdges() []models.GraphEdge {
	s.mu.RLock()
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
