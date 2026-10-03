package tests

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meta-lattice/src/mcp"
)

func TestMCPServerProtocolsAndTools(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "lattice-test-mcp-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	sampleFile := filepath.Join(tmpDir, "sample.py")
	_ = os.WriteFile(sampleFile, []byte(`
def sample_task(x: int) -> int:
    """Sample task calculation."""
    return x * 2
`), 0644)

	server := mcp.NewMCPServer(tmpDir)

	callServer := func(req mcp.JSONRPCRequest) mcp.JSONRPCResponse {
		reqBytes, _ := json.Marshal(req)
		in := bytes.NewReader(append(reqBytes, '\n'))
		var out bytes.Buffer

		_ = server.Serve(in, &out)

		var resp mcp.JSONRPCResponse
		_ = json.Unmarshal(out.Bytes(), &resp)
		return resp
	}

	// 1. Initialize
	initResp := callServer(mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
	})
	if initResp.Error != nil {
		t.Fatalf("unexpected init error: %+v", initResp.Error)
	}

	// 2. Tools list
	listResp := callServer(mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
	})
	listRes, ok := listResp.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected tools/list response: %+v", listResp)
	}
	toolsRaw, _ := listRes["tools"].([]any)
	expectedTools := []string{
		"zoom_overview",
		"zoom_module",
		"zoom_symbol",
		"zoom_search",
		"check_layer_violation",
		"estimate_blast_radius",
		"sync_index",
		"get_index_status",
	}
	toolSet := make(map[string]bool)
	for _, toolAny := range toolsRaw {
		if tool, ok := toolAny.(map[string]any); ok {
			name, _ := tool["name"].(string)
			toolSet[name] = true
		}
	}
	for _, exp := range expectedTools {
		if !toolSet[exp] {
			t.Fatalf("missing expected tool: %s", exp)
		}
	}

	// Helper to extract text from tool call
	getText := func(resp mcp.JSONRPCResponse) string {
		res, ok := resp.Result.(map[string]any)
		if !ok {
			return ""
		}
		content, _ := res["content"].([]any)
		if len(content) > 0 {
			cMap, _ := content[0].(map[string]any)
			txt, _ := cMap["text"].(string)
			return txt
		}
		return ""
	}

	// 3. Tool call: sync_index
	syncParams, _ := json.Marshal(map[string]any{
		"name":      "sync_index",
		"arguments": map[string]any{},
	})
	syncResp := callServer(mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      3,
		Method:  "tools/call",
		Params:  syncParams,
	})
	syncTxt := getText(syncResp)
	var syncData map[string]any
	if err := json.Unmarshal([]byte(syncTxt), &syncData); err != nil {
		t.Fatalf("failed to unmarshal sync output: %s", syncTxt)
	}
	if syncData["total_files"].(float64) < 1 {
		t.Fatalf("expected at least 1 total file, got %+v", syncData)
	}

	// 4. Tool call: zoom_overview
	zoomParams, _ := json.Marshal(map[string]any{
		"name":      "zoom_overview",
		"arguments": map[string]any{},
	})
	zoomResp := callServer(mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      4,
		Method:  "tools/call",
		Params:  zoomParams,
	})
	zoomTxt := getText(zoomResp)
	if !strings.Contains(zoomTxt, "L0_Overview") {
		t.Fatalf("expected L0_Overview in response: %s", zoomTxt)
	}

	// 5. Tool call: get_index_status
	statusParams, _ := json.Marshal(map[string]any{
		"name":      "get_index_status",
		"arguments": map[string]any{},
	})
	statusResp := callServer(mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      5,
		Method:  "tools/call",
		Params:  statusParams,
	})
	statusTxt := getText(statusResp)
	if !strings.Contains(statusTxt, "counts") {
		t.Fatalf("expected counts in index status: %s", statusTxt)
	}

	// 6. Ping
	pingResp := callServer(mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      6,
		Method:  "ping",
	})
	if pingResp.Error != nil {
		t.Fatalf("unexpected ping error: %+v", pingResp.Error)
	}
}
