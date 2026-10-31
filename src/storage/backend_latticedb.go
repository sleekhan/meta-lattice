//go:build !nolattice && cgo

package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	latticedb "github.com/jeffhajewski/latticedb/bindings/go"
	"meta-lattice/src/models"
)

const HasNativeLatticeDB = true

// latticeSchemaVersion identifies the packed v2 property layout stored in the
// "__meta" stream offset table. Databases with version 2 store a node's full
// property map in "_p" (compact JSON bytes) alongside only the scalar keys
// backed by B-Tree or FTS indexes, cutting B-Tree page writes by ~6x and
// enabling sub-millisecond on-demand queries without full-graph hydration.
const (
	latticeMetaStream    = "__meta"
	latticeMetaSchemaKey = "schema_version"
	latticeMetaEdgesKey  = "total_edges"
	latticeSchemaVersion = uint64(2)
)

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
// such as "File" or "Function" are decorative and are preserved inside "_p".
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

func labelFromID(id string) string {
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

func candidateLabelsForID(id string) []string {
	if lbl := labelFromID(id); lbl != "" {
		return []string{lbl}
	}
	return identityIndexLabels
}

func labelForLevel(level string) string {
	switch level {
	case string(models.LevelL0), "L0Domain":
		return "L0Domain"
	case string(models.LevelL1), "L1Module":
		return "L1Module"
	case string(models.LevelL2), "L2Class":
		return "L2Class"
	case string(models.LevelL3), "L3Symbol":
		return "L3Symbol"
	default:
		return ""
	}
}

// nodePropertyMap converts a node to its packed LatticeDB v2 properties. Only
// the indexed scalar keys ("id", "name", "path", "short_name", "search_text")
// and the packed JSON payload ("_p") are stored as B-Tree properties.
func nodePropertyMap(id string, labels []string, properties map[string]any) map[string]latticedb.Value {
	fullProps := make(map[string]any, len(properties)+2)
	for k, v := range properties {
		fullProps[k] = v
	}
	fullProps["id"] = id
	if len(labels) > 0 {
		fullProps["labels"] = labels
	}

	packed, _ := json.Marshal(fullProps)
	props := make(map[string]latticedb.Value, 6)
	props["id"] = id
	props["_p"] = packed
	props["search_text"] = buildNodeSearchText(fullProps)

	primary := identityLabel(labels)
	if primary == "" {
		primary = labelFromID(id)
	}
	name, _ := fullProps["name"].(string)
	pathVal, _ := fullProps["path"].(string)

	switch primary {
	case "L0Domain":
		props["name"] = name
		if pathVal != "" {
			props["path"] = pathVal
		}
	case "L1Module":
		props["path"] = pathVal
	case "L2Class":
		props["name"] = name
	case "L3Symbol":
		props["name"] = name
		props["short_name"] = shortSymbolName(name)
	default:
		if name != "" {
			props["name"] = name
			props["short_name"] = shortSymbolName(name)
		}
		if pathVal != "" {
			props["path"] = pathVal
		}
	}
	return props
}

func edgePropertyMap(properties map[string]any) map[string]latticedb.Value {
	if len(properties) == 0 {
		return nil
	}
	packed, err := json.Marshal(properties)
	if err != nil {
		return nil
	}
	return map[string]latticedb.Value{"_p": packed}
}

func unpackPropsValue(val latticedb.Value) (map[string]any, bool) {
	var raw []byte
	switch v := val.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return nil, false
	}
	if len(raw) == 0 {
		return nil, false
	}
	props, err := unmarshalPropsJSON(raw)
	if err != nil || props == nil {
		return nil, false
	}
	return props, true
}

func extractLabels(raw any, primaryLabel string) []string {
	switch v := raw.(type) {
	case []string:
		if len(v) > 0 {
			return v
		}
	case []any:
		var out []string
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if primaryLabel != "" {
		return []string{primaryLabel}
	}
	return nil
}

func readNodeDataFromTx(tx *latticedb.Tx, nid latticedb.NodeID, primaryLabel string) (*NodeData, bool) {
	if val, ok, err := tx.GetProperty(nid, "_p"); err == nil && ok {
		if props, ok := unpackPropsValue(val); ok {
			idStr, _ := props["id"].(string)
			if idStr == "" {
				if idVal, okID, errID := tx.GetProperty(nid, "id"); errID == nil && okID {
					idStr, _ = idVal.(string)
				}
			}
			if idStr == "" {
				return nil, false
			}
			labels := extractLabels(props["labels"], primaryLabel)
			props["id"] = idStr
			if len(labels) > 0 {
				props["labels"] = labels
			}
			return &NodeData{
				ID:         idStr,
				Labels:     labels,
				Properties: props,
			}, true
		}
	}

	// Legacy v1 node fallback (no "_p" property).
	idVal, ok, err := tx.GetProperty(nid, "id")
	if err != nil || !ok {
		return nil, false
	}
	idStr, _ := idVal.(string)
	if idStr == "" {
		return nil, false
	}
	props := map[string]any{"id": idStr}
	for _, key := range []string{"level", "name", "path", "file_path", "loc", "module_count", "language", "layer", "docstring", "kind", "signature", "code", "is_public", "cyclomatic_complexity", "line_start", "line_end", "exports", "calls", "bases", "method_signatures", "field_signatures", "description", "domain"} {
		if v, exists, err := tx.GetProperty(nid, key); err == nil && exists {
			props[key] = normalizeNumeric(v)
		}
	}
	if primaryLabel == "" {
		primaryLabel = labelFromID(idStr)
	}
	labels := extractLabels(props["labels"], primaryLabel)
	if len(labels) > 0 {
		props["labels"] = labels
	}
	return &NodeData{
		ID:         idStr,
		Labels:     labels,
		Properties: props,
	}, true
}

func readEdgePropsFromTx(tx *latticedb.Tx, edgeID latticedb.EdgeID) map[string]any {
	if val, ok, err := tx.GetEdgeProperty(edgeID, "_p"); err == nil && ok {
		if props, ok := unpackPropsValue(val); ok {
			return props
		}
	}
	return make(map[string]any)
}

func resolveNodeIDInTx(tx *latticedb.Tx, id string) (latticedb.NodeID, string, bool) {
	for _, lbl := range candidateLabelsForID(id) {
		ids, err := tx.FindNodesByLabelProperty(lbl, "id", id, 1)
		if err == nil && len(ids) > 0 {
			return ids[0], lbl, true
		}
	}
	return 0, "", false
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

// probeLatticeDB verifies that the database file can be opened and checks
// whether it uses the packed v2 schema (which supports lazy on-demand queries).
func probeLatticeDB(latticePath string) (bool, error) {
	if _, err := os.Stat(latticePath); err != nil {
		return false, err
	}
	var isV2 bool
	err := withReadOnlyDB(latticePath, 8, func(db *latticedb.DB) error {
		ver, ok, err := db.GetStreamOffset(latticeMetaStream, latticeMetaSchemaKey)
		if err != nil {
			return fmt.Errorf("failed to read LatticeDB metadata: %w", err)
		}
		if _, err := db.GetNodesByLabel("L1Module"); err != nil {
			return fmt.Errorf("failed to read LatticeDB label index: %w", err)
		}
		isV2 = ok && ver == latticeSchemaVersion
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("failed to open LatticeDB %s: %w", latticePath, err)
	}
	if !isV2 {
		closeCachedReadOnlyDB()
	}
	return isV2, nil
}

func ignoreAlreadyExists(err error) error {
	if err == nil || errors.Is(err, &latticedb.Error{Code: latticedb.ErrorAlreadyExists}) {
		return nil
	}
	return err
}

// ensureIdentityIndexes makes sure the (label, "id"), secondary property, and
// FTS indexes exist. Indexes persist in the file, so this is a no-op after the
// first save; a database that cannot be indexed at all is rebuilt instead.
func ensureIdentityIndexes(db *latticedb.DB) error {
	for _, label := range identityIndexLabels {
		err := db.CreateNodePropertyIndex(label, "id")
		if err != nil && !errors.Is(err, &latticedb.Error{Code: latticedb.ErrorAlreadyExists}) {
			if errors.Is(err, &latticedb.Error{Code: latticedb.ErrorUnsupported}) {
				return fmt.Errorf("%w: identity indexes unsupported: %v", errIncrementalUnavailable, err)
			}
			return fmt.Errorf("%w: create %s index: %v", errIncrementalUnavailable, label, err)
		}
		_ = ignoreAlreadyExists(db.CreateNodeFTSIndex(label, "search_text"))
	}
	_ = ignoreAlreadyExists(db.CreateNodePropertyIndex("L0Domain", "name"))
	_ = ignoreAlreadyExists(db.CreateNodePropertyIndex("L1Module", "path"))
	_ = ignoreAlreadyExists(db.CreateNodePropertyIndex("L2Class", "name"))
	_ = ignoreAlreadyExists(db.CreateNodePropertyIndex("L3Symbol", "name"))
	_ = ignoreAlreadyExists(db.CreateNodePropertyIndex("L3Symbol", "short_name"))
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
	return labelFromID(id)
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
	closeCachedReadOnlyDB()

	db, err := openLatticeWithRetry(latticePath, latticedb.OpenOptions{
		Create:               false,
		CacheSizeMB:          16,
		EnableAdjacencyCache: true,
	})
	if err != nil {
		return err
	}
	defer db.Close()

	if err := ensureIdentityIndexes(db); err != nil {
		return err
	}

	ver, hasVer, _ := db.GetStreamOffset(latticeMetaStream, latticeMetaSchemaKey)
	baseEdges, hasEdges, _ := db.GetStreamOffset(latticeMetaStream, latticeMetaEdgesKey)
	trackEdges := hasVer && ver == latticeSchemaVersion && hasEdges

	err = db.Update(func(tx *latticedb.Tx) error {
		edgeDelta, txErr := applyJournalTx(tx, journal, nodes)
		if txErr != nil {
			return txErr
		}
		if trackEdges {
			updated := int64(baseEdges) + edgeDelta
			if updated < 0 {
				updated = 0
			}
			if err := tx.SetStreamOffset(latticeMetaStream, latticeMetaEdgesKey, uint64(updated)); err != nil {
				return err
			}
		}
		return nil
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
// edge upserts. It returns the net change in edge count.
func applyJournalTx(tx *latticedb.Tx, journal *graphJournal, nodes map[string]*NodeData) (int64, error) {
	resolver := newNodeResolver(tx, journal, nodes)
	var edgeDelta int64

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
			return 0, fmt.Errorf("%w: malformed edge key %q", errIncrementalUnavailable, key)
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
			return 0, err
		}
		if !ok {
			continue
		}
		targetID, ok, err := resolver.resolve(op.target)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		if err := tx.DeleteEdge(sourceID, targetID, op.typ); err != nil {
			if !isNotFound(err) {
				return 0, err
			}
		} else {
			edgeDelta--
		}
	}

	// 2. Node removals. LatticeDB does not cascade; also purge any remaining
	// incident edges on disk before removing the node itself.
	for id, mutation := range journal.nodes {
		if !mutation.delete {
			continue
		}
		nodeID, ok, err := resolver.resolve(id)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		if out, err := tx.GetOutgoingEdges(nodeID); err == nil {
			for _, e := range out {
				if err := tx.DeleteEdge(e.SourceID, e.TargetID, e.Type); err == nil {
					edgeDelta--
				}
			}
		}
		if inc, err := tx.GetIncomingEdges(nodeID); err == nil {
			for _, e := range inc {
				if err := tx.DeleteEdge(e.SourceID, e.TargetID, e.Type); err == nil {
					edgeDelta--
				}
			}
		}
		if err := tx.DeleteNode(nodeID); err != nil && !isNotFound(err) {
			return 0, err
		}
	}

	// 3. Node upserts.
	for id, mutation := range journal.nodes {
		if mutation.delete {
			continue
		}
		node, ok := nodes[id]
		if !ok {
			continue
		}
		labels := node.Labels
		if len(labels) == 0 {
			labels = mutation.labels
		}
		props := nodePropertyMap(id, labels, node.Properties)

		nodeID, exists, err := resolver.resolve(id)
		if err != nil {
			return 0, err
		}
		if !exists {
			primary := identityLabel(labels)
			createLabels := labels
			if primary != "" {
				createLabels = []string{primary}
			} else if len(createLabels) == 0 {
				createLabels = []string{"Node"}
			}
			created, err := tx.CreateNode(latticedb.CreateNodeOptions{
				Labels:     createLabels,
				Properties: props,
			})
			if err != nil {
				return 0, err
			}
			resolver.remember(id, created.ID)
			available[id] = true
			continue
		}
		for key, value := range props {
			if err := tx.SetProperty(nodeID, key, value); err != nil {
				return 0, err
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
			return 0, err
		}
		targetAvailable, err := endpointOK(op.target)
		if err != nil {
			return 0, err
		}
		if !sourceAvailable || !targetAvailable {
			continue
		}
		sourceID, ok, err := resolver.resolve(op.source)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		targetID, ok, err := resolver.resolve(op.target)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		if err := tx.DeleteEdge(sourceID, targetID, op.typ); err != nil {
			if !isNotFound(err) {
				return 0, err
			}
		} else {
			edgeDelta--
		}
		if _, err := tx.CreateEdge(sourceID, targetID, op.typ, latticedb.CreateEdgeOptions{
			Properties: edgePropertyMap(op.mut.props),
		}); err != nil {
			return 0, err
		}
		edgeDelta++
	}

	return edgeDelta, nil
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
		CacheSizeMB:          64,
		EnableAdjacencyCache: true,
	})
	if err != nil {
		return fmt.Errorf("failed to create temporary LatticeDB: %w", err)
	}

	nodeIDMap := make(map[string]latticedb.NodeID, len(nodes))
	var totalEdges uint64

	err = db.Update(func(tx *latticedb.Tx) error {
		// 1. Create all nodes with packed v2 properties and primary label.
		for _, node := range nodes {
			primary := identityLabel(node.Labels)
			createLabels := node.Labels
			if primary != "" {
				createLabels = []string{primary}
			} else if len(createLabels) == 0 {
				createLabels = []string{"Node"}
			}

			n, err := tx.CreateNode(latticedb.CreateNodeOptions{
				Labels:     createLabels,
				Properties: nodePropertyMap(node.ID, node.Labels, node.Properties),
			})
			if err != nil {
				return fmt.Errorf("failed to create node %s: %w", node.ID, err)
			}
			nodeIDMap[node.ID] = n.ID
		}

		// 2. Create all edges with packed properties.
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

				if _, err := tx.CreateEdge(srcNodeID, tgtNodeID, edgeType, latticedb.CreateEdgeOptions{
					Properties: edgePropertyMap(edge.Properties),
				}); err == nil {
					totalEdges++
				}
			}
		}

		if err := tx.SetStreamOffset(latticeMetaStream, latticeMetaSchemaKey, latticeSchemaVersion); err != nil {
			return err
		}
		if err := tx.SetStreamOffset(latticeMetaStream, latticeMetaEdgesKey, totalEdges); err != nil {
			return err
		}
		return nil
	})

	if err == nil {
		// Create B-Tree property and FTS indexes on the populated database.
		for _, label := range identityIndexLabels {
			_ = db.CreateNodePropertyIndex(label, "id")
			_ = db.CreateNodeFTSIndex(label, "search_text")
		}
		_ = db.CreateNodePropertyIndex("L0Domain", "name")
		_ = db.CreateNodePropertyIndex("L1Module", "path")
		_ = db.CreateNodePropertyIndex("L2Class", "name")
		_ = db.CreateNodePropertyIndex("L3Symbol", "name")
		_ = db.CreateNodePropertyIndex("L3Symbol", "short_name")
	}

	_ = db.Close()

	if err != nil {
		_ = os.Remove(tmpPath)
		_ = os.Remove(tmpPath + "-wal")
		_ = os.Remove(tmpPath + "-shm")
		return err
	}

	closeCachedReadOnlyDB()

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
	closeCachedReadOnlyDB()

	db, err := openLatticeWithRetry(latticePath, latticedb.OpenOptions{
		Create:               false,
		ReadOnly:             true,
		CacheSizeMB:          32,
		EnableAdjacencyCache: true,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to open LatticeDB %s: %w", latticePath, err)
	}
	defer db.Close()

	ver, ok, err := db.GetStreamOffset(latticeMetaStream, latticeMetaSchemaKey)
	if err == nil && ok && ver == latticeSchemaVersion {
		return loadNativeV2(db)
	}
	return loadLegacyCypher(db)
}

func loadNativeV2(db *latticedb.DB) (map[string]*NodeData, map[string][]models.GraphEdge, map[string][]models.GraphEdge, error) {
	nodes := make(map[string]*NodeData)
	outEdges := make(map[string][]models.GraphEdge)
	inEdges := make(map[string][]models.GraphEdge)
	nidToStr := make(map[latticedb.NodeID]string)

	err := db.View(func(tx *latticedb.Tx) error {
		for _, lbl := range identityIndexLabels {
			nids, err := db.GetNodesByLabel(lbl)
			if err != nil {
				return err
			}
			for _, nid := range nids {
				node, ok := readNodeDataFromTx(tx, nid, lbl)
				if !ok {
					continue
				}
				nodes[node.ID] = node
				nidToStr[nid] = node.ID
			}
		}

		for nid, srcID := range nidToStr {
			rawEdges, err := tx.GetOutgoingEdges(nid)
			if err != nil {
				return err
			}
			for _, e := range rawEdges {
				tgtID, ok := nidToStr[e.TargetID]
				if !ok || tgtID == "" {
					continue
				}
				edge := models.GraphEdge{
					SourceID:   srcID,
					TargetID:   tgtID,
					EdgeType:   e.Type,
					Properties: readEdgePropsFromTx(tx, e.ID),
				}
				outEdges[srcID] = append(outEdges[srcID], edge)
				inEdges[tgtID] = append(inEdges[tgtID], edge)
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("LatticeDB native v2 load failed: %w", err)
	}
	return nodes, outEdges, inEdges, nil
}

func loadLegacyCypher(db *latticedb.DB) (map[string]*NodeData, map[string][]models.GraphEdge, map[string][]models.GraphEdge, error) {
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
		} else if rawP, hasP := props["_p"]; hasP {
			if unpacked, okP := unpackPropsValue(rawP); okP {
				props = unpacked
				if unpackedLabels := extractLabels(props["labels"], ""); len(unpackedLabels) > 0 {
					labels = unpackedLabels
				}
			} else {
				delete(props, "_p")
				delete(props, "search_text")
				delete(props, "short_name")
				normalizeNodeProperties(props)
			}
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
		} else if rawP, hasP := props["_p"]; hasP {
			if unpacked, okP := unpackPropsValue(rawP); okP {
				props = unpacked
			} else {
				delete(props, "_p")
				normalizeNodeProperties(props)
			}
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

// -----------------------------------------------------------------------------
// Lazy On-Demand Native Operations
// -----------------------------------------------------------------------------

var (
	roCacheMu    sync.Mutex
	roCachePath  string
	roCacheMtime int64
	roCacheSize  int64
	roCacheDB    *latticedb.DB
	roCacheTimer *time.Timer
)

const roCacheIdleTTL = 8 * time.Millisecond

func closeCachedReadOnlyDB() {
	roCacheMu.Lock()
	defer roCacheMu.Unlock()
	closeCachedReadOnlyDBLocked()
}

func closeCachedReadOnlyDBLocked() {
	if roCacheTimer != nil {
		roCacheTimer.Stop()
		roCacheTimer = nil
	}
	if roCacheDB != nil {
		_ = roCacheDB.Close()
		roCacheDB = nil
	}
	roCachePath = ""
	roCacheMtime = 0
	roCacheSize = 0
}

func withReadOnlyDB(latticePath string, cacheMB uint32, fn func(db *latticedb.DB) error) error {
	if cacheMB < 8 {
		cacheMB = 8
	}
	stat, err := os.Stat(latticePath)
	if err != nil {
		return err
	}

	roCacheMu.Lock()
	defer roCacheMu.Unlock()

	if roCacheTimer != nil {
		roCacheTimer.Stop()
		roCacheTimer = nil
	}

	mtime := stat.ModTime().UnixNano()
	size := stat.Size()
	if roCacheDB != nil && (roCachePath != latticePath || roCacheMtime != mtime || roCacheSize != size) {
		closeCachedReadOnlyDBLocked()
	}

	if roCacheDB == nil {
		db, openErr := openLatticeWithRetry(latticePath, latticedb.OpenOptions{
			Create:               false,
			ReadOnly:             true,
			CacheSizeMB:          cacheMB,
			EnableAdjacencyCache: true,
		})
		if openErr != nil {
			return openErr
		}
		roCacheDB = db
		roCachePath = latticePath
		roCacheMtime = mtime
		roCacheSize = size
	}

	runErr := fn(roCacheDB)

	roCacheTimer = time.AfterFunc(roCacheIdleTTL, func() {
		closeCachedReadOnlyDB()
	})
	return runErr
}

func hasNodeInLatticeDB(latticePath string, id string) bool {
	var found bool
	_ = withReadOnlyDB(latticePath, 8, func(db *latticedb.DB) error {
		return db.View(func(tx *latticedb.Tx) error {
			_, _, found = resolveNodeIDInTx(tx, id)
			return nil
		})
	})
	return found
}

func getNodeFromLatticeDB(latticePath string, id string) (*NodeData, bool) {
	var node *NodeData
	var found bool
	_ = withReadOnlyDB(latticePath, 8, func(db *latticedb.DB) error {
		return db.View(func(tx *latticedb.Tx) error {
			nid, lbl, ok := resolveNodeIDInTx(tx, id)
			if !ok {
				return nil
			}
			node, found = readNodeDataFromTx(tx, nid, lbl)
			return nil
		})
	})
	return node, found
}

func findNodesByLabelPropertyInLatticeDB(latticePath string, label string, key string, value any, limit int) ([]*NodeData, bool) {
	queryLimit := uint(limit)
	if queryLimit == 0 {
		queryLimit = 1024
	}

	var results []*NodeData
	supported := true
	err := withReadOnlyDB(latticePath, 8, func(db *latticedb.DB) error {
		return db.View(func(tx *latticedb.Tx) error {
			ids, err := tx.FindNodesByLabelProperty(label, key, toLatticeValue(value), queryLimit)
			if err != nil {
				if errors.Is(err, &latticedb.Error{Code: latticedb.ErrorUnsupported}) {
					supported = false
				}
				return nil
			}
			for _, nid := range ids {
				if node, ok := readNodeDataFromTx(tx, nid, label); ok {
					results = append(results, node)
					if limit > 0 && len(results) >= limit {
						break
					}
				}
			}
			return nil
		})
	})
	if err != nil {
		return nil, false
	}
	return results, supported
}

func findSymbolsByNameInLatticeDB(latticePath string, name string, filePath string) []*NodeData {
	if name == "" {
		return nil
	}
	short := shortSymbolName(name)
	var results []*NodeData
	seen := make(map[latticedb.NodeID]bool)

	_ = withReadOnlyDB(latticePath, 8, func(db *latticedb.DB) error {
		return db.View(func(tx *latticedb.Tx) error {
			type lookupSpec struct {
				label string
				key   string
				val   string
			}
			specs := []lookupSpec{
				{label: "L3Symbol", key: "name", val: name},
				{label: "L3Symbol", key: "short_name", val: short},
				{label: "L2Class", key: "name", val: name},
			}
			for _, sp := range specs {
				ids, err := tx.FindNodesByLabelProperty(sp.label, sp.key, sp.val, 256)
				if err != nil {
					continue
				}
				for _, nid := range ids {
					if seen[nid] {
						continue
					}
					seen[nid] = true
					node, ok := readNodeDataFromTx(tx, nid, sp.label)
					if !ok {
						continue
					}
					nName, _ := node.Properties["name"].(string)
					if nName != name && !strings.HasSuffix(nName, "."+name) {
						continue
					}
					if filePath != "" {
						fPath, _ := node.Properties["file_path"].(string)
						if fPath != filePath {
							continue
						}
					}
					results = append(results, node)
				}
			}
			return nil
		})
	})
	return results
}

func queryNodesFromLatticeDB(latticePath string, level string, domain string, limit int) []*NodeData {
	var targetLabels []string
	if lbl := labelForLevel(level); lbl != "" {
		targetLabels = []string{lbl}
	} else {
		targetLabels = identityIndexLabels
	}

	var results []*NodeData
	_ = withReadOnlyDB(latticePath, 8, func(db *latticedb.DB) error {
		return db.View(func(tx *latticedb.Tx) error {
			for _, lbl := range targetLabels {
				nids, err := db.GetNodesByLabel(lbl)
				if err != nil {
					continue
				}
				for _, nid := range nids {
					node, ok := readNodeDataFromTx(tx, nid, lbl)
					if !ok {
						continue
					}
					if level != "" {
						if lvl, _ := node.Properties["level"].(string); lvl != level {
							continue
						}
					}
					if domain != "" {
						if dom, _ := node.Properties["domain"].(string); dom != domain {
							continue
						}
					}
					results = append(results, node)
					if limit > 0 && len(results) >= limit {
						return nil
					}
				}
			}
			return nil
		})
	})
	return results
}

func getOutgoingEdgesFromLatticeDB(latticePath string, nodeID string, edgeType string) ([]models.GraphEdge, []*NodeData) {
	var edges []models.GraphEdge
	var neighbors []*NodeData

	_ = withReadOnlyDB(latticePath, 8, func(db *latticedb.DB) error {
		return db.View(func(tx *latticedb.Tx) error {
			nid, _, ok := resolveNodeIDInTx(tx, nodeID)
			if !ok {
				return nil
			}
			var rawEdges []latticedb.Edge
			var err error
			if edgeType != "" {
				rawEdges, err = tx.GetOutgoingEdgesByType(nid, edgeType, 1000000)
			} else {
				rawEdges, err = tx.GetOutgoingEdges(nid)
			}
			if err != nil {
				return nil
			}
			for i, e := range rawEdges {
				var tgtID string
				if i < 64 {
					if tgtNode, ok := readNodeDataFromTx(tx, e.TargetID, ""); ok {
						tgtID = tgtNode.ID
						neighbors = append(neighbors, tgtNode)
					}
				}
				if tgtID == "" {
					if val, ok, err := tx.GetProperty(e.TargetID, "id"); err == nil && ok {
						tgtID, _ = val.(string)
					}
				}
				if tgtID == "" {
					continue
				}
				edges = append(edges, models.GraphEdge{
					SourceID:   nodeID,
					TargetID:   tgtID,
					EdgeType:   e.Type,
					Properties: readEdgePropsFromTx(tx, e.ID),
				})
			}
			return nil
		})
	})
	return edges, neighbors
}

func getIncomingEdgesFromLatticeDB(latticePath string, nodeID string, edgeType string) ([]models.GraphEdge, []*NodeData) {
	var edges []models.GraphEdge
	var neighbors []*NodeData

	_ = withReadOnlyDB(latticePath, 8, func(db *latticedb.DB) error {
		return db.View(func(tx *latticedb.Tx) error {
			nid, _, ok := resolveNodeIDInTx(tx, nodeID)
			if !ok {
				return nil
			}
			var rawEdges []latticedb.Edge
			var err error
			if edgeType != "" {
				rawEdges, err = tx.GetIncomingEdgesByType(nid, edgeType, 1000000)
			} else {
				rawEdges, err = tx.GetIncomingEdges(nid)
			}
			if err != nil {
				return nil
			}
			for i, e := range rawEdges {
				var srcID string
				if i < 64 {
					if srcNode, ok := readNodeDataFromTx(tx, e.SourceID, ""); ok {
						srcID = srcNode.ID
						neighbors = append(neighbors, srcNode)
					}
				}
				if srcID == "" {
					if val, ok, err := tx.GetProperty(e.SourceID, "id"); err == nil && ok {
						srcID, _ = val.(string)
					}
				}
				if srcID == "" {
					continue
				}
				edges = append(edges, models.GraphEdge{
					SourceID:   srcID,
					TargetID:   nodeID,
					EdgeType:   e.Type,
					Properties: readEdgePropsFromTx(tx, e.ID),
				})
			}
			return nil
		})
	})
	return edges, neighbors
}

func getEdgesByTypeFromLatticeDB(latticePath string, edgeType string) []models.GraphEdge {
	targetLabels := identityIndexLabels
	if edgeType == string(models.EdgeImports) {
		targetLabels = []string{"L1Module"}
	}

	var result []models.GraphEdge
	_ = withReadOnlyDB(latticePath, 8, func(db *latticedb.DB) error {
		return db.View(func(tx *latticedb.Tx) error {
			nidToStr := make(map[latticedb.NodeID]string)
			resolveStr := func(nid latticedb.NodeID) string {
				if s, ok := nidToStr[nid]; ok {
					return s
				}
				if val, ok, err := tx.GetProperty(nid, "id"); err == nil && ok {
					if s, _ := val.(string); s != "" {
						nidToStr[nid] = s
						return s
					}
				}
				return ""
			}

			for _, lbl := range targetLabels {
				nids, err := db.GetNodesByLabel(lbl)
				if err != nil {
					continue
				}
				for _, nid := range nids {
					var rawEdges []latticedb.Edge
					if edgeType != "" {
						rawEdges, err = tx.GetOutgoingEdgesByType(nid, edgeType, 1000000)
					} else {
						rawEdges, err = tx.GetOutgoingEdges(nid)
					}
					if err != nil || len(rawEdges) == 0 {
						continue
					}
					srcID := resolveStr(nid)
					if srcID == "" {
						continue
					}
					for _, e := range rawEdges {
						tgtID := resolveStr(e.TargetID)
						if tgtID == "" {
							continue
						}
						result = append(result, models.GraphEdge{
							SourceID:   srcID,
							TargetID:   tgtID,
							EdgeType:   e.Type,
							Properties: readEdgePropsFromTx(tx, e.ID),
						})
					}
				}
			}
			return nil
		})
	})
	return result
}

func getEdgePropsFromLatticeDB(latticePath string, sourceID, targetID, edgeType string) (map[string]any, bool) {
	var props map[string]any
	var found bool
	_ = withReadOnlyDB(latticePath, 8, func(db *latticedb.DB) error {
		return db.View(func(tx *latticedb.Tx) error {
			srcNID, _, ok1 := resolveNodeIDInTx(tx, sourceID)
			if !ok1 {
				return nil
			}
			tgtNID, _, ok2 := resolveNodeIDInTx(tx, targetID)
			if !ok2 {
				return nil
			}
			edges, err := tx.GetOutgoingEdgesByType(srcNID, edgeType, 1000000)
			if err != nil {
				return nil
			}
			for _, e := range edges {
				if e.TargetID == tgtNID {
					props = readEdgePropsFromTx(tx, e.ID)
					found = true
					return nil
				}
			}
			return nil
		})
	})
	return props, found
}

func inspectNodesInLatticeDB(latticePath string, ids []string) map[string]nodeEdgesSnapshot {
	if len(ids) == 0 {
		return nil
	}
	out := make(map[string]nodeEdgesSnapshot, len(ids))
	err := withReadOnlyDB(latticePath, 8, func(db *latticedb.DB) error {
		return db.View(func(tx *latticedb.Tx) error {
			nidToStr := make(map[latticedb.NodeID]string)
			resolveStr := func(nid latticedb.NodeID) string {
				if s, ok := nidToStr[nid]; ok {
					return s
				}
				if val, ok, err := tx.GetProperty(nid, "id"); err == nil && ok {
					if s, _ := val.(string); s != "" {
						nidToStr[nid] = s
						return s
					}
				}
				return ""
			}

			for _, id := range ids {
				nid, lbl, ok := resolveNodeIDInTx(tx, id)
				if !ok {
					continue
				}
				nidToStr[nid] = id
				snap := nodeEdgesSnapshot{labels: []string{lbl}}
				if rawOut, err := tx.GetOutgoingEdges(nid); err == nil {
					for _, e := range rawOut {
						if tgtID := resolveStr(e.TargetID); tgtID != "" {
							snap.outEdges = append(snap.outEdges, models.GraphEdge{
								SourceID: id,
								TargetID: tgtID,
								EdgeType: e.Type,
							})
						}
					}
				}
				if rawIn, err := tx.GetIncomingEdges(nid); err == nil {
					for _, e := range rawIn {
						if srcID := resolveStr(e.SourceID); srcID != "" {
							snap.inEdges = append(snap.inEdges, models.GraphEdge{
								SourceID: srcID,
								TargetID: id,
								EdgeType: e.Type,
							})
						}
					}
				}
				out[id] = snap
			}
			return nil
		})
	})
	if err != nil {
		return nil
	}
	return out
}

func countNodesAndEdgesInLatticeDB(latticePath string) (map[string]int, bool) {
	var counts map[string]int
	var okCounts bool
	_ = withReadOnlyDB(latticePath, 8, func(db *latticedb.DB) error {
		ver, ok, err := db.GetStreamOffset(latticeMetaStream, latticeMetaSchemaKey)
		if err != nil || !ok || ver != latticeSchemaVersion {
			return nil
		}
		totalEdges, ok, err := db.GetStreamOffset(latticeMetaStream, latticeMetaEdgesKey)
		if err != nil || !ok {
			return nil
		}

		n0, err0 := db.GetNodesByLabel("L0Domain")
		n1, err1 := db.GetNodesByLabel("L1Module")
		n2, err2 := db.GetNodesByLabel("L2Class")
		n3, err3 := db.GetNodesByLabel("L3Symbol")
		if err0 != nil || err1 != nil || err2 != nil || err3 != nil {
			return nil
		}

		l0, l1, l2, l3 := len(n0), len(n1), len(n2), len(n3)
		counts = map[string]int{
			"total_nodes": l0 + l1 + l2 + l3,
			"total_edges": int(totalEdges),
			"l0_domains":  l0,
			"l1_modules":  l1,
			"l2_classes":  l2,
			"l3_symbols":  l3,
		}
		okCounts = true
		return nil
	})
	return counts, okCounts
}

func ftsSearchInLatticeDB(latticePath string, queryText string, limit int) ([]map[string]any, bool) {
	queryTokens := tokenize(queryText)
	if len(queryTokens) == 0 {
		return nil, true
	}

	perLimit := uint32(limit * 4)
	if perLimit < 50 {
		perLimit = 50
	}

	var results []map[string]any
	var okFTS bool
	err := withReadOnlyDB(latticePath, 8, func(db *latticedb.DB) error {
		scores := make(map[latticedb.NodeID]float64)
		nodeLabels := make(map[latticedb.NodeID]string)

		for _, tok := range queryTokens {
			matchedTok := false
			for _, lbl := range identityIndexLabels {
				res, err := db.FTSSearch(lbl, "search_text", tok, latticedb.FTSSearchOptions{Limit: perLimit})
				if err != nil {
					if errors.Is(err, &latticedb.Error{Code: latticedb.ErrorUnsupported}) {
						okFTS = false
						return nil
					}
					continue
				}
				for _, r := range res {
					scores[r.NodeID] += float64(r.Score)
					nodeLabels[r.NodeID] = lbl
					matchedTok = true
				}
			}
			if !matchedTok && len(tok) >= 3 {
				for _, lbl := range identityIndexLabels {
					res, err := db.FTSSearch(lbl, "search_text", tok, latticedb.FTSSearchOptions{
						Limit:         perLimit,
						MaxDistance:   1,
						MinTermLength: 2,
					})
					if err != nil {
						continue
					}
					for _, r := range res {
						scores[r.NodeID] += float64(r.Score) * 0.7
						nodeLabels[r.NodeID] = lbl
					}
				}
			}
		}

		if len(scores) == 0 {
			// Fall back to in-memory BM25 in case a prefix match applies.
			okFTS = false
			return nil
		}

		type scoredNode struct {
			nid   latticedb.NodeID
			score float64
		}
		ranked := make([]scoredNode, 0, len(scores))
		for nid, sc := range scores {
			if sc > 0 {
				ranked = append(ranked, scoredNode{nid: nid, score: sc})
			}
		}
		sort.Slice(ranked, func(i, j int) bool {
			return ranked[i].score > ranked[j].score
		})
		if limit > 0 && len(ranked) > limit {
			ranked = ranked[:limit]
		}

		_ = db.View(func(tx *latticedb.Tx) error {
			for _, item := range ranked {
				node, ok := readNodeDataFromTx(tx, item.nid, nodeLabels[item.nid])
				if !ok {
					continue
				}
				res := make(map[string]any, len(node.Properties)+1)
				for k, v := range node.Properties {
					res[k] = v
				}
				res["_rank"] = item.score
				results = append(results, res)
			}
			return nil
		})
		okFTS = true
		return nil
	})
	if err != nil {
		return nil, false
	}
	return results, okFTS
}
