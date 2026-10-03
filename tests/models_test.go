package tests

import (
	"testing"

	"meta-lattice/src/models"
)

func TestModelsToProperties(t *testing.T) {
	l0 := models.L0DomainNode{
		ID:          "domain:auth",
		Name:        "auth",
		Path:        "src/auth",
		Description: "Auth subsystem",
		ModuleCount: 5,
	}
	p0 := l0.ToProperties()
	if p0["id"] != "domain:auth" || p0["level"] != "L0" {
		t.Fatalf("unexpected L0 properties: %+v", p0)
	}

	l1 := models.L1ModuleNode{
		ID:       "file:src/auth/service.py",
		Path:     "src/auth/service.py",
		Filename: "service.py",
		Language: "python",
		LOC:      120,
	}
	p1 := l1.ToProperties()
	if p1["id"] != "file:src/auth/service.py" || p1["level"] != "L1" {
		t.Fatalf("unexpected L1 properties: %+v", p1)
	}

	l2 := models.L2ClassNode{
		ID:               "class:src/auth/service.py:AuthService",
		Name:             "AuthService",
		FilePath:         "src/auth/service.py",
		MethodSignatures: []string{"def login(self, u: str) -> bool"},
	}
	p2 := l2.ToProperties()
	if p2["id"] != "class:src/auth/service.py:AuthService" || p2["level"] != "L2" {
		t.Fatalf("unexpected L2 properties: %+v", p2)
	}

	l3 := models.L3SymbolNode{
		ID:        "symbol:src/auth/service.py:AuthService.login",
		Name:      "AuthService.login",
		ParentID:  "class:src/auth/service.py:AuthService",
		Signature: "def login(self, u: str) -> bool",
		Code:      "def login(self, u: str) -> bool:\n    return True",
	}
	p3 := l3.ToProperties()
	if p3["id"] != "symbol:src/auth/service.py:AuthService.login" || p3["level"] != "L3" {
		t.Fatalf("unexpected L3 properties: %+v", p3)
	}
}
