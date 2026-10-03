package tests

import (
	"testing"

	"meta-lattice/src/indexer"
)

func TestLayerDetectionUsesWholeTokens(t *testing.T) {
	if l := indexer.DetectLayer("src/rapid.py"); l != "" {
		t.Fatalf("expected empty layer for src/rapid.py, got %q", l)
	}
	if l := indexer.DetectLayer("src/handlers/user_repo.py"); l != "repository" {
		t.Fatalf("expected 'repository' for src/handlers/user_repo.py, got %q", l)
	}
	if l := indexer.DetectLayer("src/controllers/user.py"); l != "controller" {
		t.Fatalf("expected 'controller' for src/controllers/user.py, got %q", l)
	}
}

func TestPythonParserMultiLineSignatureAndImports(t *testing.T) {
	code := `"""Module doc."""
from .core import base

def use(
    a: int,
    b: str = 'x:y',
) -> int:
    return base()
`
	res := indexer.ParsePythonFile("pkg/user.py", code, "sha123", "pkg")
	if res == nil {
		t.Fatal("expected non-nil parsed result")
	}

	if len(res.Imports) != 1 {
		t.Fatalf("expected 1 import, got %d", len(res.Imports))
	}
	imp := res.Imports[0]
	if !imp.IsRelative || imp.ModuleSpec != ".core" || len(imp.Names) != 1 || imp.Names[0] != "base" {
		t.Fatalf("unexpected import: %+v", imp)
	}

	if len(res.SymbolNodes) != 1 {
		t.Fatalf("expected 1 symbol node, got %d", len(res.SymbolNodes))
	}
	sym := res.SymbolNodes[0]
	expectedSig := "def use(a: int, b: str='x:y') -> int"
	if sym.Signature != expectedSig {
		t.Fatalf("expected signature %q, got %q", expectedSig, sym.Signature)
	}

	if len(res.Calls) == 0 || res.Calls[0].CalledName != "base" {
		t.Fatalf("expected call to 'base', got %+v", res.Calls)
	}
}

func TestTSParserImportsAndArrowConsts(t *testing.T) {
	code := `import { Repo } from "./repo";
const local = (x: number) => x;
export function top() { return local(1); }
`
	res := indexer.ParseTSJSFile("src/u.ts", code, "sha", "root")
	if len(res.Imports) != 1 {
		t.Fatalf("expected 1 import, got %d", len(res.Imports))
	}
	if res.Imports[0].Names[0] != "Repo" || !res.Imports[0].IsRelative {
		t.Fatalf("unexpected import spec: %+v", res.Imports[0])
	}

	symNames := make(map[string]bool)
	for _, s := range res.SymbolNodes {
		symNames[s.Name] = true
	}
	if !symNames["local"] || !symNames["top"] {
		t.Fatalf("expected symbols 'local' and 'top', got %+v", symNames)
	}
}

func TestGoParser(t *testing.T) {
	code := `package billing

import "fmt"

type Invoice struct {
	ID string
}

func (i *Invoice) Print() {
	fmt.Println(i.ID)
}

func GenerateInvoice(id string) *Invoice {
	inv := &Invoice{ID: id}
	inv.Print()
	return inv
}
`
	res := indexer.ParseGoFile("src/billing/invoice.go", code, "sha", "billing")
	if res == nil {
		t.Fatal("expected non-nil result")
	}

	if len(res.ClassNodes) != 1 || res.ClassNodes[0].Name != "Invoice" {
		t.Fatalf("expected class Invoice, got %+v", res.ClassNodes)
	}

	symMap := make(map[string]indexer.CallSpec)
	for _, c := range res.Calls {
		symMap[c.CalledName] = c
	}
	if _, ok := symMap["Print"]; !ok {
		t.Fatalf("expected call to Print, got %+v", res.Calls)
	}
}

func TestJavaParser(t *testing.T) {
	code := `package com.example.service;

import com.example.repo.UserRepo;
import java.util.List;

public class UserService {
    private UserRepo repo;

    public User getUser(int id) {
        return this.repo.findById(id);
    }
}
`
	res := indexer.ParseJavaFile("src/main/java/com/example/service/UserService.java", code, "sha-java", "service")
	if res == nil {
		t.Fatal("expected non-nil parsed java result")
	}
	if len(res.Imports) != 2 {
		t.Fatalf("expected 2 imports, got %d", len(res.Imports))
	}
	if len(res.ClassNodes) != 1 || res.ClassNodes[0].Name != "UserService" {
		t.Fatalf("expected class UserService, got %+v", res.ClassNodes)
	}
	if len(res.SymbolNodes) != 1 || res.SymbolNodes[0].Name != "getUser" {
		t.Fatalf("expected method getUser, got %+v", res.SymbolNodes)
	}
	if len(res.Calls) == 0 || res.Calls[0].CalledName != "findById" {
		t.Fatalf("expected call to findById, got %+v", res.Calls)
	}
}

func TestRustParser(t *testing.T) {
	code := `use crate::storage::DB;
use std::sync::Arc;

pub struct Engine {
    db: Arc<DB>,
}

impl Engine {
    pub fn query_data(&self, id: u64) -> Result<String, Error> {
        let val = self.db.get(id)?;
        Ok(val)
    }
}
`
	res := indexer.ParseRustFile("src/engine.rs", code, "sha-rs", "engine")
	if res == nil {
		t.Fatal("expected non-nil parsed rust result")
	}
	if len(res.Imports) != 2 {
		t.Fatalf("expected 2 imports, got %d", len(res.Imports))
	}
	if len(res.ClassNodes) != 1 || res.ClassNodes[0].Name != "Engine" {
		t.Fatalf("expected 1 unified Engine class node, got %+v", res.ClassNodes)
	}
	if len(res.SymbolNodes) != 1 || res.SymbolNodes[0].Name != "query_data" {
		t.Fatalf("expected function query_data, got %+v", res.SymbolNodes)
	}
	if len(res.Calls) == 0 || res.Calls[0].CalledName != "get" {
		t.Fatalf("expected call to get, got %+v", res.Calls)
	}
}

func TestCCPPParser(t *testing.T) {
	code := `#include "header.h"
#include <vector>

class StorageEngine {
public:
    int SaveData(const char* data) {
        Validate(data);
        return 0;
    }
};
`
	res := indexer.ParseCCPPFile("src/storage.cpp", code, "sha-cpp", "storage")
	if res == nil {
		t.Fatal("expected non-nil parsed C++ result")
	}
	if len(res.Imports) != 2 {
		t.Fatalf("expected 2 includes, got %d", len(res.Imports))
	}
	if len(res.ClassNodes) != 1 || res.ClassNodes[0].Name != "StorageEngine" {
		t.Fatalf("expected class StorageEngine, got %+v", res.ClassNodes)
	}
	if len(res.SymbolNodes) != 1 || res.SymbolNodes[0].Name != "SaveData" {
		t.Fatalf("expected function SaveData, got %+v", res.SymbolNodes)
	}
	if len(res.Calls) == 0 || res.Calls[0].CalledName != "Validate" {
		t.Fatalf("expected call to Validate, got %+v", res.Calls)
	}
}
