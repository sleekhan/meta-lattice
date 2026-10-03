package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"meta-lattice/src/config"
	"meta-lattice/src/features/auditor"
	"meta-lattice/src/features/blast"
	"meta-lattice/src/features/zoomer"
	"meta-lattice/src/indexer"
	"meta-lattice/src/storage"
)

type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      any           `json:"id,omitempty"`
	Result  any           `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type MCPServer struct {
	mu          sync.Mutex
	cfg         *config.LatticeConfig
	db          *storage.GraphStorage
	cacheEngine *indexer.CacheEngine
	zoomer      *zoomer.HierarchicalZoomer
	auditor     *auditor.ArchitectureBoundaryAuditor
	blast       *blast.BlastRadiusEstimator
}

func NewMCPServer(workspaceRoot string) *MCPServer {
	cfg := config.GetConfig(workspaceRoot)
	db := storage.NewGraphStorage(cfg.DBPath)
	cacheEngine := indexer.NewCacheEngine(cfg, db)
	z := zoomer.NewHierarchicalZoomer(db)
	aud := auditor.NewArchitectureBoundaryAuditor(db, cfg)
	bl := blast.NewBlastRadiusEstimator(db)

	return &MCPServer{
		cfg:         cfg,
		db:          db,
		cacheEngine: cacheEngine,
		zoomer:      z,
		auditor:     aud,
		blast:       bl,
	}
}

func (s *MCPServer) RunStdio() error {
	return s.Serve(os.Stdin, os.Stdout)
}

func (s *MCPServer) Serve(r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	// Buffer large lines if needed
	const maxCapacity = 10 * 1024 * 1024
	scanner.Buffer(make([]byte, 0, 64*1024), maxCapacity)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req JSONRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			resp := JSONRPCResponse{
				JSONRPC: "2.0",
				Error: &JSONRPCError{
					Code:    -32700,
					Message: "Parse error",
				},
			}
			s.sendResponse(w, resp)
			continue
		}

		resp := s.handleRequest(&req)
		if resp != nil {
			s.sendResponse(w, resp)
		}
	}

	return scanner.Err()
}

func (s *MCPServer) sendResponse(w io.Writer, resp any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, _ := json.Marshal(resp)
	_, _ = w.Write(append(data, '\n'))
}

func (s *MCPServer) handleRequest(req *JSONRPCRequest) *JSONRPCResponse {
	// JSON-RPC notifications (no id) must never receive a reply, including
	// unknown ones such as notifications/cancelled or notifications/roots/list_changed.
	if req.ID == nil && strings.HasPrefix(req.Method, "notifications/") {
		return nil
	}

	switch req.Method {
	case "initialize":
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "meta-lattice",
					"version": "1.0.0",
				},
				"instructions": "Meta-Lattice provides Hierarchical Context Zooming (L0->L3), Architecture Boundary Auditing, Blast Radius Estimation, and Incremental AST Caching for Claude Code using LatticeDB.",
			},
		}

	case "notifications/initialized":
		return nil // Notification, no reply

	case "ping":
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{},
		}

	case "tools/list":
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": s.listTools(),
			},
		}

	case "tools/call":
		var callParams struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &callParams); err != nil {
			return &JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &JSONRPCError{
					Code:    -32602,
					Message: "Invalid params",
				},
			}
		}

		resultText, err := s.callTool(callParams.Name, callParams.Arguments)
		if err != nil {
			return &JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]any{
					"content": []map[string]any{
						{
							"type": "text",
							"text": fmt.Sprintf(`{"error": %q}`, err.Error()),
						},
					},
					"isError": true,
				},
			}
		}

		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"content": []map[string]any{
					{
						"type": "text",
						"text": resultText,
					},
				},
			},
		}

	default:
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &JSONRPCError{
				Code:    -32601,
				Message: "Method not found: " + req.Method,
			},
		}
	}
}

func (s *MCPServer) listTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "zoom_overview",
			"description": "L0/L1 Architectural Overview: Returns high-level domains, module catalogs, and export summaries without reading raw code. Consumes 80%+ fewer tokens.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"domain": map[string]any{"type": "string", "description": "Optional domain filter"},
				},
			},
		},
		{
			"name":        "zoom_module",
			"description": "L1->L2 Context Zoom: Inspect a file or module. Returns class definitions, interface contracts, and method signatures WITHOUT method bodies for token economy.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file_path": map[string]any{"type": "string", "description": "Relative file path"},
				},
				"required": []string{"file_path"},
			},
		},
		{
			"name":        "zoom_symbol",
			"description": "L2->L3 Context Zoom: Retrieve the exact source code, docstring, parameters, and incoming/outgoing calls for a specific function or method.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"symbol_name": map[string]any{"type": "string", "description": "Name or ID of symbol"},
					"file_path":   map[string]any{"type": "string", "description": "Optional file path"},
				},
				"required": []string{"symbol_name"},
			},
		},
		{
			"name":        "zoom_search",
			"description": "BM25 Full-Text Search in LatticeDB: Search for symbols, signatures, and docstrings across the codebase in milliseconds.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "Search query"},
					"level": map[string]any{"type": "string", "description": "Optional level filter (L0, L1, L2, L3)"},
					"limit": map[string]any{"type": "integer", "description": "Max results (default: 15)"},
				},
				"required": []string{"query"},
			},
		},
		{
			"name":        "check_layer_violation",
			"description": "Architecture Boundary Auditor: Detects circular dependencies, architectural layer violations (e.g. Repository -> Service -> Controller), and coupling instability metrics.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file_path": map[string]any{"type": "string", "description": "Optional file path to audit"},
				},
			},
		},
		{
			"name":        "estimate_blast_radius",
			"description": "Blast Radius Estimator: Simulate ripple effects and breaking change risks before editing core interfaces or utilities. Returns Blast Score (0-100) and Top 10 Breaking Points.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"symbol_or_path": map[string]any{"type": "string", "description": "Symbol name or file path"},
					"change_type":    map[string]any{"type": "string", "description": "Change type: signature, body, removal, rename"},
					"max_hops":       map[string]any{"type": "integer", "description": "Max traversal hops"},
				},
				"required": []string{"symbol_or_path"},
			},
		},
		{
			"name":        "sync_index",
			"description": "Incremental AST Cache Sync: Synchronizes LatticeDB with workspace changes using Git SHA/mtime caching in milliseconds.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"force": map[string]any{"type": "boolean", "description": "Force full re-indexing"},
				},
			},
		},
		{
			"name":        "get_index_status",
			"description": "Check current LatticeDB indexing status, node/edge counts (L0, L1, L2, L3), and cache hit statistics.",
			"inputSchema": map[string]any{
				"type": "object",
			},
		},
	}
}

// ensureIndexed lazily builds the index the first time a query tool is used on
// an empty graph, so tools never return misleading "not found" results just
// because sync_index has not been called yet.
func (s *MCPServer) ensureIndexed() {
	if s.db.CountNodesAndEdges()["total_nodes"] == 0 {
		s.cacheEngine.Sync(false)
	}
}

func (s *MCPServer) callTool(name string, args map[string]any) (string, error) {
	switch name {
	case "zoom_module", "zoom_symbol", "zoom_search", "check_layer_violation", "estimate_blast_radius":
		s.ensureIndexed()
	}

	switch name {
	case "zoom_overview":
		domain, _ := args["domain"].(string)
		s.ensureIndexed()
		res := s.zoomer.ZoomOverview(domain)
		data, err := json.MarshalIndent(res, "", "  ")
		return string(data), err

	case "zoom_module":
		filePath, _ := args["file_path"].(string)
		res := s.zoomer.ZoomModule(filePath)
		data, err := json.MarshalIndent(res, "", "  ")
		return string(data), err

	case "zoom_symbol":
		symName, _ := args["symbol_name"].(string)
		filePath, _ := args["file_path"].(string)
		res := s.zoomer.ZoomSymbol(symName, filePath)
		data, err := json.MarshalIndent(res, "", "  ")
		return string(data), err

	case "zoom_search":
		query, _ := args["query"].(string)
		level, _ := args["level"].(string)
		limit := 15
		if lFloat, ok := args["limit"].(float64); ok {
			limit = int(lFloat)
		}
		res := s.zoomer.ZoomSearch(query, level, limit)
		data, err := json.MarshalIndent(res, "", "  ")
		return string(data), err

	case "check_layer_violation":
		filePath, _ := args["file_path"].(string)
		res := s.auditor.CheckLayerViolation(filePath, nil)
		data, err := json.MarshalIndent(res, "", "  ")
		return string(data), err

	case "estimate_blast_radius":
		target, _ := args["symbol_or_path"].(string)
		changeType := "signature"
		if ct, ok := args["change_type"].(string); ok && ct != "" {
			changeType = ct
		}
		maxHops := 4
		if mh, ok := args["max_hops"].(float64); ok {
			maxHops = int(mh)
		}
		res := s.blast.EstimateBlastRadius(target, changeType, maxHops)
		data, err := json.MarshalIndent(res, "", "  ")
		return string(data), err

	case "sync_index":
		force := false
		if f, ok := args["force"].(bool); ok {
			force = f
		}
		stats := s.cacheEngine.Sync(force)
		data, err := json.MarshalIndent(stats, "", "  ")
		return string(data), err

	case "get_index_status":
		counts := s.db.CountNodesAndEdges()
		cachedCount := len(s.cacheEngine.ScanFiles())
		res := map[string]any{
			"is_native_latticedb": s.db.IsNativeLatticeDB(),
			"database_engine":     s.db.DatabaseEngine(),
			"db_path":             s.db.DatabasePath(),
			"counts":              counts,
			"cached_files":        cachedCount,
		}
		data, err := json.MarshalIndent(res, "", "  ")
		return string(data), err

	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}
