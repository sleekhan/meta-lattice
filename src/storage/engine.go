package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	filePath    string // json backup path
	latticePath string // native .lattice path
	nodes       map[string]*NodeData          // nodeID -> NodeData
	outEdges    map[string][]models.GraphEdge // sourceID -> edges
	inEdges     map[string][]models.GraphEdge // targetID -> edges
	bm25        *BM25Index
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
		if _, statErr := os.Stat(s.latticePath); statErr == nil {
			nodes, outEdges, inEdges, err := loadFromLatticeDB(s.latticePath)
			if err != nil {
				return fmt.Errorf("corrupt LatticeDB file: %w", err)
			}
			s.nodes = nodes
			s.outEdges = outEdges
			s.inEdges = inEdges
			s.bm25.Clear()
			for _, n := range s.nodes {
				s.indexNodeBM25(n)
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

	s.nodes = make(map[string]*NodeData)
	s.outEdges = make(map[string][]models.GraphEdge)
	s.inEdges = make(map[string][]models.GraphEdge)
	s.bm25.Clear()

	for _, n := range state.Nodes {
		s.nodes[n.ID] = n
		s.indexNodeBM25(n)
	}

	for _, e := range state.Edges {
		s.outEdges[e.SourceID] = append(s.outEdges[e.SourceID], e)
		s.inEdges[e.TargetID] = append(s.inEdges[e.TargetID], e)
	}

	return nil
}

func (s *GraphStorage) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 1. If native LatticeDB is compiled in, persist exclusively to .lattice
	if HasNativeLatticeDB {
		return saveToLatticeDB(s.latticePath, s.nodes, s.outEdges)
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
	return os.Rename(tmpFile, s.filePath)
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

func (s *GraphStorage) CreateNode(id string, labels []string, properties map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()

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
	s.indexNodeBM25(node)
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

	delete(s.nodes, id)
	s.bm25.RemoveDocument(id)

	// Remove outgoing edges
	if outList, ok := s.outEdges[id]; ok {
		for _, e := range outList {
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

	propsCopy := make(map[string]any)
	for k, v := range properties {
		propsCopy[k] = v
	}

	edge := models.GraphEdge{
		SourceID:   sourceID,
		TargetID:   targetID,
		EdgeType:   edgeType,
		Properties: propsCopy,
	}

	// Replace existing edge of same source, target, edgeType if exists
	s.deleteEdgeLocked(sourceID, targetID, edgeType)

	s.outEdges[sourceID] = append(s.outEdges[sourceID], edge)
	s.inEdges[targetID] = append(s.inEdges[targetID], edge)
}

func (s *GraphStorage) deleteEdgeLocked(sourceID, targetID, edgeType string) {
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

func (s *GraphStorage) DeleteEdgesByType(edgeTypes ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	typesSet := make(map[string]bool)
	for _, t := range edgeTypes {
		typesSet[t] = true
	}

	for src, list := range s.outEdges {
		var updated []models.GraphEdge
		for _, e := range list {
			if !typesSet[e.EdgeType] {
				updated = append(updated, e)
			}
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

	typesSet := make(map[string]bool)
	for _, t := range edgeTypes {
		typesSet[t] = true
	}

	if outList, ok := s.outEdges[sourceID]; ok {
		var remaining []models.GraphEdge
		for _, e := range outList {
			if len(typesSet) == 0 || typesSet[e.EdgeType] {
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

	typesSet := make(map[string]bool)
	for _, t := range edgeTypes {
		typesSet[t] = true
	}

	if inList, ok := s.inEdges[targetID]; ok {
		var remaining []models.GraphEdge
		for _, e := range inList {
			if len(typesSet) == 0 || typesSet[e.EdgeType] {
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

func (s *GraphStorage) FTSSearch(queryText string, limit int) []map[string]any {
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
	s.mu.RLock()
	defer s.mu.RUnlock()

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

	return map[string]int{
		"total_nodes": len(s.nodes),
		"total_edges": totalEdges,
		"l0_domains":  l0,
		"l1_modules":  l1,
		"l2_classes":  l2,
		"l3_symbols":  l3,
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
