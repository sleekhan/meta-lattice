package tests

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meta-lattice/src/config"
	"meta-lattice/src/mcp"
)

// TestMCPPicksUpExternalPartialSync verifies that a long-running MCP server
// notices a cache state rewritten by another process (the post-tool hook) and
// re-syncs instead of serving a stale index forever.
func TestMCPPicksUpExternalPartialSync(t *testing.T) {
	root := t.TempDir()
	sample := filepath.Join(root, "sample.py")
	writeSample := func(source string) {
		t.Helper()
		if err := os.WriteFile(sample, []byte(source), 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeSample("def alpha():\n    return 1\n")

	server := mcp.NewMCPServer(root)
	callTool := func(id int, name string, args map[string]any) string {
		params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
		req, _ := json.Marshal(mcp.JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      id,
			Method:  "tools/call",
			Params:  params,
		})
		var out bytes.Buffer
		if err := server.Serve(bytes.NewReader(append(req, '\n')), &out); err != nil {
			t.Fatalf("serve: %v", err)
		}
		var resp mcp.JSONRPCResponse
		if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v (raw %s)", err, out.String())
		}
		res, _ := resp.Result.(map[string]any)
		content, _ := res["content"].([]any)
		if len(content) == 0 {
			return ""
		}
		item, _ := content[0].(map[string]any)
		text, _ := item["text"].(string)
		return text
	}

	if out := callTool(1, "zoom_symbol", map[string]any{"symbol_name": "alpha"}); !strings.Contains(out, "L3_Symbol_Implementation") {
		t.Fatalf("initial index does not contain alpha: %s", out)
	}

	// Simulate the post-tool hook running in another process: the file changes
	// and the persisted cache state is marked as partially synced.
	writeSample("def beta():\n    return 2\n")
	cfg := config.NewLatticeConfig(root)
	state, err := os.ReadFile(cfg.CacheStateFile)
	if err != nil {
		t.Fatalf("read cache state: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(state, &raw); err != nil {
		t.Fatalf("decode cache state: %v", err)
	}
	raw["full_sync"] = false
	updated, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.CacheStateFile, updated, 0644); err != nil {
		t.Fatal(err)
	}

	if out := callTool(2, "zoom_symbol", map[string]any{"symbol_name": "beta"}); !strings.Contains(out, "L3_Symbol_Implementation") || !strings.Contains(out, `"name": "beta"`) {
		t.Fatalf("MCP server did not pick up the external change: %s", out)
	}
}
