//go:build !nolattice && cgo

package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	latticedb "github.com/jeffhajewski/latticedb/bindings/go"
	"meta-lattice/src/models"
)

const HasNativeLatticeDB = true

// identityIndexLabels are the node labels that carry the stable string id.
// FindNodesByLabelProperty only answers for an explicitly indexed
// (label, property) pair, so every one of them needs an "id" index before the
// journal can be replayed.
var identityIndexLabels = []string{"L0Domain", "L1Module", "L2Class", "L3Symbol"}

const (
	latticeOpenRetries = 10
	latticeOpenBackoff = 20 * time.Millisecond
)

// identityLabel picks the level label (L0Domain..L3Symbol) of a node. Labels
// such as "File" or "Function" are decorative and are not indexed.
func identityLabel(labels []string) string {
	for _, label := range labels {
		for _, known := range identityIndexLabels {
			if label == known {
				return label
			}
		}
	}
	return ""
}

// nodePropertyMap converts our node to LatticeDB properties. "id" is always
// written from the stable string id so a node cannot be indexed under a stale
// value.
func nodePropertyMap(id string, properties map[string]any) map[string]latticedb.Value {
	props := make(map[string]latticedb.Value, len(properties)+1)
	props["id"] = id
	for k, v := range properties {
		if k == "id" {
			continue
		}
		if lv := toLatticeValue(v); lv != nil {
			props[k] = lv
		}
	}
	return props
}

func edgePropertyMap(properties map[string]any) map[string]latticedb.Value {
	props := make(map[string]latticedb.Value, len(properties))
	for k, v := range properties {
		if lv := toLatticeValue(v); lv != nil {
			props[k] = lv
		}
	}
	return props
}

// splitEdgeKey reverses edgeKey(source, type, target).
func splitEdgeKey(key string) (source, edgeType, target string, ok bool) {
	parts := strings.SplitN(key, "\x00", 3)
	if len(parts) != 3 {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// isLatticeLockError reports whether another process (or a read-only open while
// a writer is active) is holding the database lock.
func isLatticeLockError(err error) bool {
	return errors.Is(err, &latticedb.Error{Code: latticedb.ErrorDatabaseLocked}) ||
		errors.Is(err, &latticedb.Error{Code: latticedb.ErrorLockTimeout})
}

// openLatticeWithRetry opens the database, waiting briefly for a concurrent
// writer to release its lock. A lock conflict is reported as errDatabaseLocked
// so callers keep their pending changes instead of rebuilding the file.
func openLatticeWithRetry(path string, opts latticedb.OpenOptions) (*latticedb.DB, error) {
	var err error
	for attempt := 0; attempt < latticeOpenRetries; attempt++ {
		var db *latticedb.DB
		db, err = latticedb.Open(path, opts)
		if err == nil {
			return db, nil
		}
		if !isLatticeLockError(err) {
			return nil, err
		}
		time.Sleep(latticeOpenBackoff)
	}
	return nil, fmt.Errorf("%w: %v", errDatabaseLocked, err)
}

// ensureIdentityIndexes makes sure the (label, "id") indexes the journal replay
// relies on exist. Indexes persist in the file, so this is a no-op after the
// first save; a database that cannot be indexed at all is rebuilt instead.
func ensureIdentityIndexes(db *latticedb.DB) error {
	for _, label := range identityIndexLabels {
		err := db.CreateNodePropertyIndex(label, "id")
		if err == nil {
			continue
		}
		if errors.Is(err, &latticedb.Error{Code: latticedb.ErrorAlreadyExists}) {
			continue
		}
		if errors.Is(err, &latticedb.Error{Code: latticedb.ErrorUnsupported}) {
			return fmt.Errorf("%w: identity indexes unsupported: %v", errIncrementalUnavailable, err)
		}
		return fmt.Errorf("%w: create %s index: %v", errIncrementalUnavailable, label, err)
	}
	return nil
}

// nodeResolver maps stable string ids to LatticeDB node ids inside one
// transaction. It remembers resolved and missing ids so repeated lookups stay
// cheap without copying the whole in-memory node table up front.
type nodeResolver struct {
	tx       *latticedb.Tx
	journal  *graphJournal
	nodes    map[string]*NodeData
	resolved map[string]latticedb.NodeID
	missing  map[string]bool
}

func newNodeResolver(tx *latticedb.Tx, journal *graphJournal, nodes map[string]*NodeData) *nodeResolver {
	return &nodeResolver{
		tx:       tx,
		journal:  journal,
		nodes:    nodes,
		resolved: make(map[string]latticedb.NodeID),
		missing:  make(map[string]bool),
	}
}

func (r *nodeResolver) labelFor(id string) string {
	if node, ok := r.nodes[id]; ok {
		if label := identityLabel(node.Labels); label != "" {
			return label
		}
	}
	if r.journal != nil {
		if mutation, ok := r.journal.nodes[id]; ok {
			if label := identityLabel(mutation.labels); label != "" {
				return label
			}
		}
	}
	switch {
	case strings.HasPrefix(id, "domain:"):
		return "L0Domain"
	case strings.HasPrefix(id, "file:"):
		return "L1Module"
	case strings.HasPrefix(id, "class:"):
		return "L2Class"
	case strings.HasPrefix(id, "symbol:"):
		return "L3Symbol"
	default:
		return ""
	}
}

// resolve looks an id up by (label, id). An id that appears under more than one
// node means the file is inconsistent and a rebuild is the only safe fix.
func (r *nodeResolver) resolve(id string) (latticedb.NodeID, bool, error) {
	if nodeID, ok := r.resolved[id]; ok {
		return nodeID, true, nil
	}
	if r.missing[id] {
		return 0, false, nil
	}

	label := r.labelFor(id)
	if label == "" {
		r.missing[id] = true
		return 0, false, nil
	}

	ids, err := r.tx.FindNodesByLabelProperty(label, "id", id, 2)
	if err != nil {
		if errors.Is(err, &latticedb.Error{Code: latticedb.ErrorUnsupported}) {
			return 0, false, fmt.Errorf("%w: lookup %s: %v", errIncrementalUnavailable, id, err)
		}
		return 0, false, err
	}
	if len(ids) == 0 {
		r.missing[id] = true
		return 0, false, nil
	}
	if len(ids) > 1 {
		return 0, false, fmt.Errorf("%w: duplicate nodes for id %s", errIncrementalUnavailable, id)
	}
	r.resolved[id] = ids[0]
	return ids[0], true, nil
}

func (r *nodeResolver) remember(id string, nodeID latticedb.NodeID) {
	r.resolved[id] = nodeID
	delete(r.missing, id)
}

// applyJournalToLatticeDB replays the mutation journal against the existing
// database file in a single transaction. Nothing is written when the
// transaction fails, so a failed replay can safely fall back to a rebuild.
func applyJournalToLatticeDB(latticePath string, journal *graphJournal, nodes map[string]*NodeData) error {
	if journal == nil || journal.ops() == 0 {
		return nil
	}

	db, err := openLatticeWithRetry(latticePath, latticedb.OpenOptions{
		Create:               false,
		CacheSizeMB:          128,
		EnableAdjacencyCache: true,
	})
	if err != nil {
		return err
	}
	defer db.Close()

	if err := ensureIdentityIndexes(db); err != nil {
		return err
	}

	err = db.Update(func(tx *latticedb.Tx) error {
		return applyJournalTx(tx, journal, nodes)
	})
	if err == nil {
		return nil
	}

	if errors.Is(err, errIncrementalUnavailable) {
		return err
	}
	if isLatticeLockError(err) {
		return fmt.Errorf("%w: %v", errDatabaseLocked, err)
	}
	// Any other failure leaves the file as it was; rebuild it from memory.
	return fmt.Errorf("%w: %v", errIncrementalUnavailable, err)
}

// applyJournalTx applies the journal in dependency order: edge removals first
// (so no edge is left dangling), then node removals, node upserts and finally
// edge upserts.
func applyJournalTx(tx *latticedb.Tx, journal *graphJournal, nodes map[string]*NodeData) error {
	resolver := newNodeResolver(tx, journal, nodes)

	deleting := make(map[string]struct{})
	for id, mutation := range journal.nodes {
		if mutation.delete {
			deleting[id] = struct{}{}
		}
	}

	type edgeOp struct {
		source string
		target string
		typ    string
		mut    edgeMutation
	}
	edgeOps := make([]edgeOp, 0, len(journal.edges))
	for key, mutation := range journal.edges {
		source, typ, target, ok := splitEdgeKey(key)
		if !ok {
			return fmt.Errorf("%w: malformed edge key %q", errIncrementalUnavailable, key)
		}
		edgeOps = append(edgeOps, edgeOp{source: source, target: target, typ: typ, mut: mutation})
	}

	// LatticeDB silently accepts edges pointing at deleted nodes, so an edge is
	// only written when both endpoints exist after this transaction.
	available := make(map[string]bool, len(journal.nodes))
	endpointOK := func(id string) (bool, error) {
		if _, gone := deleting[id]; gone {
			return false, nil
		}
		if ok, seen := available[id]; seen {
			return ok, nil
		}
		if mutation, ok := journal.nodes[id]; ok && !mutation.delete {
			// Created by this transaction; resolve() cannot see it yet.
			available[id] = true
			return true, nil
		}
		_, ok, err := resolver.resolve(id)
		if err != nil {
			return false, err
		}
		available[id] = ok
		return ok, nil
	}

	// 1. Edge removals.
	for _, op := range edgeOps {
		if !op.mut.delete {
			continue
		}
		sourceID, ok, err := resolver.resolve(op.source)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		targetID, ok, err := resolver.resolve(op.target)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := tx.DeleteEdge(sourceID, targetID, op.typ); err != nil && !isNotFound(err) {
			return err
		}
	}

	// 2. Node removals. LatticeDB does not cascade, but every incident edge was
	// journaled by DeleteNode, so the graph stays consistent.
	for id, mutation := range journal.nodes {
		if !mutation.delete {
			continue
		}
		nodeID, ok, err := resolver.resolve(id)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := tx.DeleteNode(nodeID); err != nil && !isNotFound(err) {
			return err
		}
	}

	// 3. Node upserts. Every node kind writes a fixed property key set, so
	// overwriting each key is enough; LatticeDB has no property removal API.
	for id, mutation := range journal.nodes {
		if mutation.delete {
			continue
		}
		node, ok := nodes[id]
		if !ok {
			continue
		}
		props := nodePropertyMap(id, node.Properties)

		nodeID, exists, err := resolver.resolve(id)
		if err != nil {
			return err
		}
		if !exists {
			labels := node.Labels
			if len(labels) == 0 {
				labels = mutation.labels
			}
			if len(labels) == 0 {
				labels = []string{"Node"}
			}
			created, err := tx.CreateNode(latticedb.CreateNodeOptions{
				Labels:     labels,
				Properties: props,
			})
			if err != nil {
				return err
			}
			resolver.remember(id, created.ID)
			available[id] = true
			continue
		}
		for key, value := range props {
			if err := tx.SetProperty(nodeID, key, value); err != nil {
				return err
			}
		}
	}

	// 4. Edge upserts. LatticeDB allows duplicate (source, type, target) edges
	// and DeleteEdge only removes one, so every upsert is a delete-then-create.
	for _, op := range edgeOps {
		if op.mut.delete {
			continue
		}
		sourceAvailable, err := endpointOK(op.source)
		if err != nil {
			return err
		}
		targetAvailable, err := endpointOK(op.target)
		if err != nil {
			return err
		}
		if !sourceAvailable || !targetAvailable {
			continue
		}
		sourceID, ok, err := resolver.resolve(op.source)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		targetID, ok, err := resolver.resolve(op.target)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := tx.DeleteEdge(sourceID, targetID, op.typ); err != nil && !isNotFound(err) {
			return err
		}
		if _, err := tx.CreateEdge(sourceID, targetID, op.typ, latticedb.CreateEdgeOptions{
			Properties: edgePropertyMap(op.mut.props),
		}); err != nil {
			return err
		}
	}

	return nil
}

func isNotFound(err error) bool {
	return errors.Is(err, &latticedb.Error{Code: latticedb.ErrorNotFound})
}

func toLatticeValue(val any) latticedb.Value {
	if val == nil {
		return nil
	}
	switch v := val.(type) {
	case string:
		return v
	case bool:
		return v
	case int:
		return int64(v)
	case int64:
		return v
	case int32:
		return int64(v)
	case float64:
		return v
	case float32:
		return float64(v)
	case []byte:
		return v
	case []float32:
		return v
	case []string:
		items := make([]any, len(v))
		for i, s := range v {
			items[i] = s
		}
		return items
	case []any:
		items := make([]any, len(v))
		for i, item := range v {
			items[i] = toLatticeValue(item)
		}
		return items
	case map[string]any:
		m := make(map[string]any, len(v))
		for k, val := range v {
			m[k] = toLatticeValue(val)
		}
		return m
	default:
		return fmt.Sprintf("%v", v)
	}
}

func saveToLatticeDB(latticePath string, nodes map[string]*NodeData, outEdges map[string][]models.GraphEdge) error {
	dir := filepath.Dir(latticePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmpPath := latticePath + ".tmp"
	_ = os.Remove(tmpPath)
	_ = os.Remove(tmpPath + "-wal")
	_ = os.Remove(tmpPath + "-shm")

	db, err := latticedb.Open(tmpPath, latticedb.OpenOptions{
		Create:               true,
		CacheSizeMB:          128,
		EnableAdjacencyCache: true,
	})
	if err != nil {
		return fmt.Errorf("failed to create temporary LatticeDB: %w", err)
	}

	nodeIDMap := make(map[string]latticedb.NodeID, len(nodes))

	err = db.Update(func(tx *latticedb.Tx) error {
		// 1. Create all nodes and set properties in batch
		for _, node := range nodes {
			labels := node.Labels
			if len(labels) == 0 {
				labels = []string{"Node"}
			}

			n, err := tx.CreateNode(latticedb.CreateNodeOptions{
				Labels:     labels,
				Properties: nodePropertyMap(node.ID, node.Properties),
			})
			if err != nil {
				return fmt.Errorf("failed to create node %s: %w", node.ID, err)
			}
			nodeIDMap[node.ID] = n.ID
		}

		// 2. Create all edges
		for srcID, edges := range outEdges {
			srcNodeID, ok1 := nodeIDMap[srcID]
			if !ok1 {
				continue
			}
			for _, edge := range edges {
				tgtNodeID, ok2 := nodeIDMap[edge.TargetID]
				if !ok2 {
					continue
				}

				edgeType := edge.EdgeType
				if edgeType == "" {
					edgeType = "RELATES"
				}

				_, _ = tx.CreateEdge(srcNodeID, tgtNodeID, edgeType, latticedb.CreateEdgeOptions{
					Properties: edgePropertyMap(edge.Properties),
				})
			}
		}

		return nil
	})

	if err == nil {
		// Create B-Tree property indexes on the newly populated database for fast O(1) lookups.
		// Every node is indexed by its stable string id under its own label: a
		// single "Node" label index would never match, because nodes only carry
		// their level labels.
		for _, label := range identityIndexLabels {
			_ = db.CreateNodePropertyIndex(label, "id")
		}
		_ = db.CreateNodePropertyIndex("L0Domain", "name")
		_ = db.CreateNodePropertyIndex("L1Module", "path")
		_ = db.CreateNodePropertyIndex("L2Class", "name")
		_ = db.CreateNodePropertyIndex("L3Symbol", "name")
	}

	_ = db.Close()

	if err != nil {
		_ = os.Remove(tmpPath)
		_ = os.Remove(tmpPath + "-wal")
		_ = os.Remove(tmpPath + "-shm")
		return err
	}

	// Rename main file
	if err := os.Rename(tmpPath, latticePath); err != nil {
		return err
	}

	// Rename WAL and SHM if present to match the destination
	if _, statErr := os.Stat(tmpPath + "-wal"); statErr == nil {
		_ = os.Rename(tmpPath+"-wal", latticePath+"-wal")
	} else {
		_ = os.Remove(latticePath + "-wal")
	}
	if _, statErr := os.Stat(tmpPath + "-shm"); statErr == nil {
		_ = os.Rename(tmpPath+"-shm", latticePath+"-shm")
	} else {
		_ = os.Remove(latticePath + "-shm")
	}

	return nil
}

func loadFromLatticeDB(latticePath string) (map[string]*NodeData, map[string][]models.GraphEdge, map[string][]models.GraphEdge, error) {
	if _, err := os.Stat(latticePath); err != nil {
		return nil, nil, nil, err
	}

	db, err := openLatticeWithRetry(latticePath, latticedb.OpenOptions{
		Create:               false,
		ReadOnly:             true,
		CacheSizeMB:          128,
		EnableAdjacencyCache: true,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to open LatticeDB %s: %w", latticePath, err)
	}
	defer db.Close()

	// 1. Query nodes
	nodeRes, err := db.Query("MATCH (n) RETURN n.id AS id, labels(n) AS labels, properties(n) AS props", nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("LatticeDB node query failed: %w", err)
	}

	nodes := make(map[string]*NodeData, len(nodeRes.Rows))
	for _, row := range nodeRes.Rows {
		idStr, ok := row["id"].(string)
		if !ok || idStr == "" {
			continue
		}

		var labels []string
		if lblList, ok := row["labels"].([]any); ok {
			for _, l := range lblList {
				if s, ok := l.(string); ok {
					labels = append(labels, s)
				}
			}
		} else if lblListStr, ok := row["labels"].([]string); ok {
			labels = lblListStr
		}

		props, ok := row["props"].(map[string]any)
		if !ok || props == nil {
			props = make(map[string]any)
		} else {
			normalizeNodeProperties(props)
		}

		nodes[idStr] = &NodeData{
			ID:         idStr,
			Labels:     labels,
			Properties: props,
		}
	}

	// 2. Query edges
	edgeRes, err := db.Query("MATCH (a)-[r]->(b) RETURN a.id AS src, b.id AS tgt, type(r) AS t, properties(r) AS props", nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("LatticeDB edge query failed: %w", err)
	}

	outEdges := make(map[string][]models.GraphEdge)
	inEdges := make(map[string][]models.GraphEdge)

	for _, row := range edgeRes.Rows {
		src, _ := row["src"].(string)
		tgt, _ := row["tgt"].(string)
		eType, _ := row["t"].(string)
		if src == "" || tgt == "" {
			continue
		}

		props, ok := row["props"].(map[string]any)
		if !ok || props == nil {
			props = make(map[string]any)
		} else {
			normalizeNodeProperties(props)
		}

		edge := models.GraphEdge{
			SourceID:   src,
			TargetID:   tgt,
			EdgeType:   eType,
			Properties: props,
		}

		outEdges[src] = append(outEdges[src], edge)
		inEdges[tgt] = append(inEdges[tgt], edge)
	}

	return nodes, outEdges, inEdges, nil
}
