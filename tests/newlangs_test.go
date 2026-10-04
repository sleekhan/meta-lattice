package tests

import (
	"testing"

	"meta-lattice/src/indexer"
)

func TestKotlinParser(t *testing.T) {
	code := `package com.example.svc

import com.example.repo.UserRepo
import com.example.model.User

class UserService(private val repo: UserRepo) {
    fun getUser(id: Int): User {
        return repo.find(id)
    }

    private fun log(msg: String) {
        println(msg)
    }
}
`
	res := indexer.ParseKotlinFile("svc/UserService.kt", code, "sha", "svc")
	if res == nil {
		t.Fatal("expected non-nil parsed result")
	}
	if len(res.Imports) != 2 || res.Imports[0].ModuleSpec != "com.example.repo.UserRepo" {
		t.Fatalf("unexpected imports: %+v", res.Imports)
	}
	if len(res.ClassNodes) != 1 || res.ClassNodes[0].Name != "UserService" {
		t.Fatalf("expected class UserService, got %+v", res.ClassNodes)
	}
	if len(res.ClassNodes[0].MethodSignatures) == 0 {
		t.Fatal("expected method signatures on L2 class node")
	}
	names := map[string]bool{}
	for _, s := range res.SymbolNodes {
		names[s.Name] = true
	}
	if !names["getUser"] || !names["log"] {
		t.Fatalf("expected getUser and log symbols, got %+v", res.SymbolNodes)
	}
	found := false
	for _, c := range res.Calls {
		if c.CalledName == "find" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected call to 'find', got %+v", res.Calls)
	}
}

func TestCSharpParser(t *testing.T) {
	code := `using System.Collections.Generic;
using App.Repositories;

namespace App.Services;

public class UserService
{
    private readonly IUserRepo _repo;

    public UserService(IUserRepo repo)
    {
        _repo = repo;
    }

    public async Task<User> GetAsync(int id)
    {
        if (id <= 0) return null;
        return await _repo.FindAsync(id);
    }
}
`
	res := indexer.ParseCSharpFile("Services/UserService.cs", code, "sha", "svc")
	if res == nil {
		t.Fatal("expected non-nil parsed result")
	}
	if len(res.Imports) != 2 || res.Imports[0].ModuleSpec != "System.Collections.Generic" {
		t.Fatalf("unexpected imports: %+v", res.Imports)
	}
	if len(res.ClassNodes) != 1 || res.ClassNodes[0].Name != "UserService" {
		t.Fatalf("expected class UserService, got %+v", res.ClassNodes)
	}
	names := map[string]bool{}
	for _, s := range res.SymbolNodes {
		names[s.Name] = true
	}
	if !names["GetAsync"] {
		t.Fatalf("expected GetAsync symbol, got %+v", res.SymbolNodes)
	}
	if len(res.ClassNodes[0].MethodSignatures) == 0 {
		t.Fatal("expected method signatures on L2 class node")
	}
}

func TestSwiftParser(t *testing.T) {
	code := `import Foundation

public struct UserService {
    let repo: UserRepo

    public func getUser(id: Int) -> User {
        if id <= 0 { return User.empty }
        return repo.find(id: id)
    }
}
`
	res := indexer.ParseSwiftFile("Sources/UserService.swift", code, "sha", "src")
	if res == nil {
		t.Fatal("expected non-nil parsed result")
	}
	if len(res.Imports) != 1 || res.Imports[0].ModuleSpec != "Foundation" {
		t.Fatalf("unexpected imports: %+v", res.Imports)
	}
	if len(res.ClassNodes) != 1 || res.ClassNodes[0].Name != "UserService" {
		t.Fatalf("expected struct UserService, got %+v", res.ClassNodes)
	}
	names := map[string]bool{}
	for _, s := range res.SymbolNodes {
		names[s.Name] = true
	}
	if !names["getUser"] {
		t.Fatalf("expected getUser symbol, got %+v", res.SymbolNodes)
	}
	found := false
	for _, c := range res.Calls {
		if c.CalledName == "find" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected call to 'find', got %+v", res.Calls)
	}
}

func TestPHPParser(t *testing.T) {
	code := `<?php

declare(strict_types=1);

namespace App\Service;

use App\Repository\UserRepository;
use App\Entity\User;

class UserService
{
    public function __construct(private UserRepository $repo) {}

    public function getUser(int $id): ?User
    {
        if ($id <= 0) {
            return null;
        }
        return $this->repo->find($id);
    }
}
`
	res := indexer.ParsePHPFile("src/Service/UserService.php", code, "sha", "src")
	if res == nil {
		t.Fatal("expected non-nil parsed result")
	}
	if len(res.Imports) != 2 || res.Imports[0].ModuleSpec != `App\Repository\UserRepository` {
		t.Fatalf("unexpected imports: %+v", res.Imports)
	}
	if len(res.ClassNodes) != 1 || res.ClassNodes[0].Name != "UserService" {
		t.Fatalf("expected class UserService, got %+v", res.ClassNodes)
	}
	names := map[string]bool{}
	for _, s := range res.SymbolNodes {
		names[s.Name] = true
	}
	if !names["getUser"] {
		t.Fatalf("expected getUser symbol, got %+v", res.SymbolNodes)
	}
	found := false
	for _, c := range res.Calls {
		if c.CalledName == "find" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected call to 'find', got %+v", res.Calls)
	}
}
