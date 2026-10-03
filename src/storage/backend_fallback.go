//go:build nolattice || !cgo

package storage

import (
	"os"

	"meta-lattice/src/models"
)

const HasNativeLatticeDB = false

func saveToLatticeDB(latticePath string, nodes map[string]*NodeData, outEdges map[string][]models.GraphEdge) error {
	// Fallback mode: no-op for LatticeDB file
	return nil
}

func loadFromLatticeDB(latticePath string) (map[string]*NodeData, map[string][]models.GraphEdge, map[string][]models.GraphEdge, error) {
	return nil, nil, nil, os.ErrNotExist
}
