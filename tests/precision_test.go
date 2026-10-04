package tests

import (
	"testing"

	"meta-lattice/src/indexer"
)

// One-line bodies must contribute their calls without leaking the scan into
// following definitions.
func TestOneLineBodiesStayLocal(t *testing.T) {
	code := `public class Calc {
    public int get() { return helper(); }
    public int helper() { return 1; }
    public void use() { get(); }
}
`
	res := indexer.ParseJavaFile("Calc.java", code, "sha", "root")
	callsByCaller := map[string][]indexer.CallSpec{}
	for _, c := range res.Calls {
		callsByCaller[c.CallerID] = append(callsByCaller[c.CallerID], c)
	}

	getID := "symbol:Calc.java:get"
	useID := "symbol:Calc.java:use"
	if len(callsByCaller[getID]) != 1 || callsByCaller[getID][0].CalledName != "helper" {
		t.Fatalf("expected get->[helper], got %+v", res.Calls)
	}
	if callsByCaller[getID][0].LineNumber != 2 {
		t.Fatalf("expected call on line 2, got %+v", callsByCaller[getID])
	}
	if len(callsByCaller[useID]) != 1 || callsByCaller[useID][0].CalledName != "get" {
		t.Fatalf("expected use->[get], got %+v", res.Calls)
	}
}

// Bodyless declarations must not absorb the next function into their Code.
func TestBodylessDeclarationTerminates(t *testing.T) {
	code := `class Store {
public:
    int load();
    int save() {
        return persist();
    }
};
`
	res := indexer.ParseCCPPFile("store.cpp", code, "sha", "root")
	names := map[string]string{}
	for _, s := range res.SymbolNodes {
		names[s.Name] = s.Code
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 symbols, got %+v", res.SymbolNodes)
	}
	for _, c := range res.Calls {
		if c.CallerID == "symbol:store.cpp:load" {
			t.Fatalf("declaration load must have no calls, got %+v", res.Calls)
		}
	}
	found := false
	for _, c := range res.Calls {
		if c.CallerID == "symbol:store.cpp:save" && c.CalledName == "persist" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected save->[persist], got %+v", res.Calls)
	}
	if len(res.ClassNodes) != 1 || len(res.ClassNodes[0].MethodSignatures) == 0 {
		t.Fatalf("expected class with method signatures, got %+v", res.ClassNodes)
	}
}

// Nested definitions and comments must not pollute the enclosing calls,
// and call sites must carry true line numbers.
func TestTSNestedDefsAndLineNumbers(t *testing.T) {
	code := `export function outer() {
  // inner(1);
  function inner() { return 1; }
  const x = helper(2);
  return x;
}

function helper(v: number) { return v; }
`
	res := indexer.ParseTSJSFile("a.ts", code, "sha", "root")
	outerCalls := map[string]int{}
	for _, c := range res.Calls {
		if c.CallerID == "symbol:a.ts:outer" {
			outerCalls[c.CalledName] = c.LineNumber
		}
	}
	if _, ok := outerCalls["inner"]; ok {
		t.Fatalf("nested def name must not be a call, got %+v", res.Calls)
	}
	if line, ok := outerCalls["helper"]; !ok || line != 4 {
		t.Fatalf("expected helper call on line 4, got %+v", res.Calls)
	}
}

// Nested Python definitions belong to the inner symbol.
func TestPythonNestedDefNotLeaked(t *testing.T) {
	code := `def outer():
    def unused():
        return 1
    return base()

def base():
    return 2
`
	res := indexer.ParsePythonFile("m.py", code, "sha", "root")
	outerCalls := map[string]bool{}
	for _, c := range res.Calls {
		if c.CallerID == "symbol:m.py:outer" {
			outerCalls[c.CalledName] = true
		}
	}
	if outerCalls["unused"] {
		t.Fatalf("nested def name must not be a call, got %+v", res.Calls)
	}
	if !outerCalls["base"] {
		t.Fatalf("expected base call, got %+v", res.Calls)
	}
}

// Rust methods must parent to their impl target with visible signatures.
func TestRustImplParenting(t *testing.T) {
	code := `pub struct Engine {}

impl Engine {
    pub fn run(&self) {
        self.boot();
    }
}
`
	res := indexer.ParseRustFile("e.rs", code, "sha", "root")
	if len(res.ClassNodes) != 1 || res.ClassNodes[0].Name != "Engine" {
		t.Fatalf("expected Engine class, got %+v", res.ClassNodes)
	}
	if len(res.ClassNodes[0].MethodSignatures) == 0 {
		t.Fatal("expected method signatures on Engine")
	}
	found := false
	for _, c := range res.Calls {
		if c.CallerID == "symbol:e.rs:run" && c.CalledName == "boot" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected run->[boot], got %+v", res.Calls)
	}
}
