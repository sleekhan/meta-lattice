package tests

import (
	"os"
	"path/filepath"
	"testing"

	"meta-lattice/src/config"
	"meta-lattice/src/indexer"
	"meta-lattice/src/models"
	"meta-lattice/src/storage"
)

func TestVueSFCParser(t *testing.T) {
	code := `<template>
  <div>
    <UserCard :user="user" />
    <span>plain</span>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue';
import UserCard from './UserCard.vue';

const user = ref(null);
function reload() {
  user.value = fetchUser();
}
</script>

<style scoped>
div { color: red; }
</style>
`
	res := indexer.ParseVueFile("src/UserList.vue", code, "sha", "src")
	if res == nil {
		t.Fatal("expected non-nil parsed result")
	}
	if res.FileNode.Language != "vue" {
		t.Fatalf("expected vue language, got %q", res.FileNode.Language)
	}
	if len(res.Imports) != 2 {
		t.Fatalf("expected 2 imports, got %+v", res.Imports)
	}
	if len(res.ClassNodes) != 1 || res.ClassNodes[0].Name != "UserList" || res.ClassNodes[0].Kind != "component" {
		t.Fatalf("expected UserList component node, got %+v", res.ClassNodes)
	}
	names := map[string]bool{}
	for _, s := range res.SymbolNodes {
		names[s.Name] = true
	}
	if !names["reload"] {
		t.Fatalf("expected reload symbol, got %+v", res.SymbolNodes)
	}
	foundExports := map[string]bool{}
	for _, e := range res.FileNode.Exports {
		foundExports[e] = true
	}
	if !foundExports["UserCard"] {
		t.Fatalf("expected template ref UserCard in exports, got %v", res.FileNode.Exports)
	}
	if foundExports["span"] || foundExports["div"] {
		t.Fatalf("native elements must not be refs, got %v", res.FileNode.Exports)
	}
	found := false
	for _, c := range res.Calls {
		if c.CallerID == "symbol:src/UserList.vue:reload" && c.CalledName == "fetchUser" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected reload->[fetchUser], got %+v", res.Calls)
	}
}

func TestSvelteParser(t *testing.T) {
	code := `<script lang="ts">
  import Header from './Header.svelte';

  let count = 0;
  function bump() {
    count += 1;
    save(count);
  }
</script>

<main>
  <Header title="hi" />
  <button on:click={bump}>{count}</button>
</main>
`
	res := indexer.ParseSvelteFile("src/Counter.svelte", code, "sha", "src")
	if res == nil {
		t.Fatal("expected non-nil parsed result")
	}
	if res.FileNode.Language != "svelte" {
		t.Fatalf("expected svelte language, got %q", res.FileNode.Language)
	}
	if len(res.ClassNodes) != 1 || res.ClassNodes[0].Name != "Counter" || res.ClassNodes[0].Kind != "component" {
		t.Fatalf("expected Counter component node, got %+v", res.ClassNodes)
	}
	names := map[string]bool{}
	for _, s := range res.SymbolNodes {
		names[s.Name] = true
	}
	if !names["bump"] {
		t.Fatalf("expected bump symbol, got %+v", res.SymbolNodes)
	}
	foundExports := map[string]bool{}
	for _, e := range res.FileNode.Exports {
		foundExports[e] = true
	}
	if !foundExports["Header"] {
		t.Fatalf("expected template ref Header in exports, got %v", res.FileNode.Exports)
	}
}

func TestReactComponentKindAndJSXCalls(t *testing.T) {
	code := `import { useState } from 'react';
import UserCard from './UserCard';

export function helper() {
  return 1;
}

export default function UserList() {
  const [users, setUsers] = useState([]);
  return (
    <div>
      <UserCard user={users[0]} />
    </div>
  );
}
`
	res := indexer.ParseTSJSFile("src/UserList.tsx", code, "sha", "src")
	if res == nil {
		t.Fatal("expected non-nil parsed result")
	}
	kinds := map[string]string{}
	for _, s := range res.SymbolNodes {
		kinds[s.Name] = s.Kind
	}
	if kinds["UserList"] != "component" {
		t.Fatalf("expected UserList kind=component, got %+v", kinds)
	}
	if kinds["helper"] != "function" {
		t.Fatalf("expected helper kind=function, got %+v", kinds)
	}
	jsxCalls := map[string]int{}
	for _, c := range res.Calls {
		if c.CallerID == "symbol:src/UserList.tsx:UserList" {
			jsxCalls[c.CalledName] = c.LineNumber
		}
	}
	if line, ok := jsxCalls["UserCard"]; !ok || line != 12 {
		t.Fatalf("expected UserCard JSX call on line 12, got %+v", res.Calls)
	}
	if _, ok := jsxCalls["useState"]; !ok {
		t.Fatalf("expected useState hook call, got %+v", res.Calls)
	}
}

func TestAngularComponentDecorator(t *testing.T) {
	code := `import { Component } from '@angular/core';

@Component({
  selector: 'app-user',
  templateUrl: './user.component.html',
})
export class UserComponent {
  load() {
    this.service.get();
  }
}
`
	res := indexer.ParseTSJSFile("src/user.component.ts", code, "sha", "src")
	if res == nil {
		t.Fatal("expected non-nil parsed result")
	}
	if len(res.ClassNodes) != 1 || res.ClassNodes[0].Kind != "component" {
		t.Fatalf("expected component kind for @Component class, got %+v", res.ClassNodes)
	}
}

// Vue import edges must resolve end-to-end through a workspace sync.
func TestVueImportEdgesResolve(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/UserCard.vue", `<template><div>x</div></template>
<script setup>
const x = 1;
</script>
`)
	write("src/App.vue", `<template><UserCard /></template>
<script setup>
import UserCard from './UserCard.vue';
</script>
`)

	cfg := config.NewLatticeConfig(dir)
	db := storage.NewGraphStorage(cfg.DBPath)
	eng := indexer.NewCacheEngine(cfg, db)
	eng.Sync(false)

	edges := db.GetOutgoingEdges("file:src/App.vue", string(models.EdgeImports))
	if len(edges) != 1 || edges[0].TargetID != "file:src/UserCard.vue" {
		t.Fatalf("expected IMPORTS edge App.vue->UserCard.vue, got %+v", edges)
	}
}
