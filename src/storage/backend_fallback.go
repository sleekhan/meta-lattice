//go:build nolattice || !cgo

package storage

import (
	"os"

	"meta-lattice/src/models"
)

const HasNativeLatticeDB = false

func probeLatticeDB(latticePath string) (bool, error) {
	return false, os.ErrNotExist
}

func saveToLatticeDB(latticePath string, nodes map[string]*NodeData, outEdges map[string][]models.GraphEdge) error {
	// Fallback mode: no-op for LatticeDB file
	return nil
}

func loadFromLatticeDB(latticePath string) (map[string]*NodeData, map[string][]models.GraphEdge, map[string][]models.GraphEdge, error) {
	return nil, nil, nil, os.ErrNotExist
}

// applyJournalToLatticeDB has no native backend to talk to in this build; the
// caller falls back to rewriting the JSON snapshot.
func applyJournalToLatticeDB(latticePath string, journal *graphJournal, nodes map[string]*NodeData) error {
	return errIncrementalUnavailable
}

func hasNodeInLatticeDB(latticePath string, id string) bool {
	return false
}

func getNodeFromLatticeDB(latticePath string, id string) (*NodeData, bool) {
	return nil, false
}

func findNodesByLabelPropertyInLatticeDB(latticePath string, label string, key string, value any, limit int) ([]*NodeData, bool) {
	return nil, false
}

func findSymbolsByNameInLatticeDB(latticePath string, name string, filePath string) []*NodeData {
	return nil
}

func queryNodesFromLatticeDB(latticePath string, level string, domain string, limit int) []*NodeData {
	return nil
}

func getOutgoingEdgesFromLatticeDB(latticePath string, nodeID string, edgeType string) ([]models.GraphEdge, []*NodeData) {
	return nil, nil
}

func getIncomingEdgesFromLatticeDB(latticePath string, nodeID string, edgeType string) ([]models.GraphEdge, []*NodeData) {
	return nil, nil
}

func getEdgesByTypeFromLatticeDB(latticePath string, edgeType string) []models.GraphEdge {
	return nil
}

func getEdgePropsFromLatticeDB(latticePath string, sourceID, targetID, edgeType string) (map[string]any, bool) {
	return nil, false
}

func inspectNodesInLatticeDB(latticePath string, ids []string) map[string]nodeEdgesSnapshot {
	return nil
}

func countNodesAndEdgesInLatticeDB(latticePath string) (map[string]int, bool) {
	return nil, false
}

func ftsSearchInLatticeDB(latticePath string, queryText string, limit int) ([]map[string]any, bool) {
	return nil, false
}

func beginReadOnlySession() func() {
	return func() {}
}

func closeCachedReadOnlyDB() {}
