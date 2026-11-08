package community

import (
	"path/filepath"
	"testing"

	"meta-lattice/src/models"
	"meta-lattice/src/storage"
)

func newTestStorage(t *testing.T) *storage.GraphStorage {
	t.Helper()
	return storage.NewGraphStorage(filepath.Join(t.TempDir(), "knowledge.lattice"))
}

func TestLouvainCommunityDetection(t *testing.T) {
	db := newTestStorage(t)

	// Cluster 1: User domain (user_ctrl, user_svc, user_repo)
	db.CreateNode("file:src/user_ctrl.go", []string{"L1Module"}, map[string]any{"path": "src/user_ctrl.go", "level": string(models.LevelL1), "domain": "user"})
	db.CreateNode("file:src/user_svc.go", []string{"L1Module"}, map[string]any{"path": "src/user_svc.go", "level": string(models.LevelL1), "domain": "user"})
	db.CreateNode("file:src/user_repo.go", []string{"L1Module"}, map[string]any{"path": "src/user_repo.go", "level": string(models.LevelL1), "domain": "user"})

	db.CreateEdge("file:src/user_ctrl.go", "file:src/user_svc.go", string(models.EdgeImports), nil)
	db.CreateEdge("file:src/user_svc.go", "file:src/user_repo.go", string(models.EdgeImports), nil)

	// Cluster 2: Payment domain (pay_ctrl, pay_svc, pay_repo)
	db.CreateNode("file:src/pay_ctrl.go", []string{"L1Module"}, map[string]any{"path": "src/pay_ctrl.go", "level": string(models.LevelL1), "domain": "payment"})
	db.CreateNode("file:src/pay_svc.go", []string{"L1Module"}, map[string]any{"path": "src/pay_svc.go", "level": string(models.LevelL1), "domain": "payment"})
	db.CreateNode("file:src/pay_repo.go", []string{"L1Module"}, map[string]any{"path": "src/pay_repo.go", "level": string(models.LevelL1), "domain": "payment"})

	db.CreateEdge("file:src/pay_ctrl.go", "file:src/pay_svc.go", string(models.EdgeImports), nil)
	db.CreateEdge("file:src/pay_svc.go", "file:src/pay_repo.go", string(models.EdgeImports), nil)

	// 1 single cross-edge from pay_svc to user_svc
	db.CreateEdge("file:src/pay_svc.go", "file:src/user_svc.go", string(models.EdgeImports), nil)

	detector := NewCommunityDetector(db)
	res := detector.DetectCommunities(2)

	if res.CommunityCount != 2 {
		t.Fatalf("expected 2 distinct communities, got %d", res.CommunityCount)
	}

	if res.ModularityScore <= 0 {
		t.Errorf("expected positive modularity score, got %f", res.ModularityScore)
	}

	// Verify each community has 3 members
	for _, c := range res.Communities {
		if c.Size != 3 {
			t.Errorf("expected community of size 3, got %d: %+v", c.Size, c.Modules)
		}
	}
}
