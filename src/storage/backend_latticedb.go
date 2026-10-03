//go:build !nolattice && cgo

package storage

import (
	"fmt"
	"os"
	"path/filepath"

	latticedb "github.com/jeffhajewski/latticedb/bindings/go"
	"meta-lattice/src/models"
)

const HasNativeLatticeDB = true

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
		Create: true,
	})
	if err != nil {
		return fmt.Errorf("failed to create temporary LatticeDB: %w", err)
	}

	nodeIDMap := make(map[string]latticedb.NodeID, len(nodes))

	err = db.Update(func(tx *latticedb.Tx) error {
		// 1. Create all nodes and set properties
		for _, node := range nodes {
			labels := node.Labels
			if len(labels) == 0 {
				labels = []string{"Node"}
			}
			n, err := tx.CreateNode(latticedb.CreateNodeOptions{
				Labels: labels,
			})
			if err != nil {
				return fmt.Errorf("failed to create node %s: %w", node.ID, err)
			}
			nodeIDMap[node.ID] = n.ID

			if err := tx.SetProperty(n.ID, "id", node.ID); err != nil {
				return err
			}
			for k, v := range node.Properties {
				if k == "id" {
					continue
				}
				if lv := toLatticeValue(v); lv != nil {
					_ = tx.SetProperty(n.ID, k, lv)
				}
			}
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

				edgeProps := make(map[string]latticedb.Value)
				for k, v := range edge.Properties {
					if lv := toLatticeValue(v); lv != nil {
						edgeProps[k] = lv
					}
				}

				edgeType := edge.EdgeType
				if edgeType == "" {
					edgeType = "RELATES"
				}

				_, _ = tx.CreateEdge(srcNodeID, tgtNodeID, edgeType, latticedb.CreateEdgeOptions{
					Properties: edgeProps,
				})
			}
		}

		return nil
	})

	if err == nil {
		// Create B-Tree property indexes on the newly populated database for fast O(1) lookups
		_ = db.CreateNodePropertyIndex("Node", "id")
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

	db, err := latticedb.Open(latticePath, latticedb.OpenOptions{
		Create:   false,
		ReadOnly: true,
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

		props := make(map[string]any)
		if propMap, ok := row["props"].(map[string]any); ok {
			for k, v := range propMap {
				props[k] = v
			}
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

		props := make(map[string]any)
		if propMap, ok := row["props"].(map[string]any); ok {
			for k, v := range propMap {
				props[k] = v
			}
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
