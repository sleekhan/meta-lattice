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
	"meta-lattice/src/features/adr"
	"meta-lattice/src/features/auditor"
	"meta-lattice/src/features/blast"
	"meta-lattice/src/features/codegen"
	"meta-lattice/src/features/community"
	"meta-lattice/src/features/diff"
	"meta-lattice/src/features/query"
	"meta-lattice/src/features/routes"
	"meta-lattice/src/features/tracer"
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
	mu           sync.Mutex
	cfg          *config.LatticeConfig
	db           *storage.GraphStorage
	cacheEngine  *indexer.CacheEngine
	zoomer       *zoomer.HierarchicalZoomer
	auditor      *auditor.ArchitectureBoundaryAuditor
	blast        *blast.BlastRadiusEstimator
	codegen      *codegen.Codegen
	tracer       *tracer.CallTracer
	diff         *diff.ChangeDetector
	queryEng     *query.GraphQueryEngine
	adrMgr       *adr.ADRManager
	routesMgr    *routes.RouteManager
	commDetector *community.CommunityDetector
}

func NewMCPServer(workspaceRoot string) *MCPServer {
	cfg := config.GetConfig(workspaceRoot)
	db := storage.NewGraphStorage(cfg.DBPath)
	cacheEngine := indexer.NewCacheEngine(cfg, db)
	z := zoomer.NewHierarchicalZoomer(db)
	aud := auditor.NewArchitectureBoundaryAuditor(db, cfg)
	bl := blast.NewBlastRadiusEstimator(db)
	cg := codegen.NewCodegen(cfg.WorkspaceRoot)
	tr := tracer.NewCallTracer(db)
	cd := diff.NewChangeDetector(cfg, db)
	qe := query.NewGraphQueryEngine(db)
	am := adr.NewADRManager(cfg)
	rm := routes.NewRouteManager(db)
	comm := community.NewCommunityDetector(db)

	return &MCPServer{
		cfg:          cfg,
		db:           db,
		cacheEngine:  cacheEngine,
		zoomer:       z,
		auditor:      aud,
		blast:        bl,
		codegen:      cg,
		tracer:       tr,
		diff:         cd,
		queryEng:     qe,
		adrMgr:       am,
		routesMgr:    rm,
		commDetector: comm,
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
				"instructions": "Meta-Lattice provides Hierarchical Context Zooming (L0->L3), Architecture Boundary Auditing, Blast Radius Estimation, Incremental AST Caching, and Code Generation (scaffold_module, apply_plan with dry_run and rollback) for Claude Code using LatticeDB.",
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
		{
			"name":        "scaffold_module",
			"description": "Code Generation: Create a new source file from a per-language template (class, interface, struct, enum, module). Refuses to overwrite unless asked; paths are confined to the workspace.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file_path": map[string]any{"type": "string", "description": "New file path relative to workspace"},
					"kind":      map[string]any{"type": "string", "description": "class, interface, struct, enum, or module"},
					"name":      map[string]any{"type": "string", "description": "Type name (defaults to file stem)"},
					"namespace": map[string]any{"type": "string", "description": "Package/namespace declaration"},
					"imports":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Module specs to import"},
					"overwrite": map[string]any{"type": "boolean", "description": "Replace an existing file"},
				},
				"required": []string{"file_path"},
			},
		},
		{
			"name":        "apply_plan",
			"description": "Code Generation: Apply a batch of file edits (create_file, replace_text, insert_after, delete_file) with validation, dry_run support, and automatic rollback on failure. Always dry_run first for multi-file edits.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"operations": map[string]any{"type": "array", "description": "Ordered edit operations", "items": map[string]any{"type": "object"}},
					"dry_run":    map[string]any{"type": "boolean", "description": "Validate without writing (default: true for safety)"},
				},
				"required": []string{"operations"},
			},
		},
		{
			"name":        "trace_call_path",
			"description": "Call Chain Tracer: Traces multi-hop execution call chains between symbols or explores inbound/outbound call trees without loading source code bodies.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"source":    map[string]any{"type": "string", "description": "Source symbol name, node ID, or file path"},
					"target":    map[string]any{"type": "string", "description": "Optional destination symbol to find connecting call paths to"},
					"direction": map[string]any{"type": "string", "description": "outbound (callees), inbound (callers), or both (default: outbound)"},
					"max_depth": map[string]any{"type": "integer", "description": "Max hop depth (default: 3, max: 10)"},
				},
				"required": []string{"source"},
			},
		},
		{
			"name":        "detect_changes",
			"description": "Change Impact & Test Scoper: Analyzes uncommitted git changes or branch diffs against the AST graph, mapping touched line ranges to affected symbols, blast scores, and affected test suites.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"base_ref": map[string]any{"type": "string", "description": "Git base revision (default: HEAD for uncommitted diff, or e.g. 'main')"},
					"scope":    map[string]any{"type": "string", "description": "Scope: 'all' (symbols + tests) or 'tests' (test execution focus)"},
					"max_hops": map[string]any{"type": "integer", "description": "Max ripple traversal hops (default: 3)"},
				},
			},
		},
		{
			"name":        "query_graph",
			"description": "Structural Code Graph Query: Filter codebase elements by complexity, in/out degrees (dead code, entry points, hotspots), kinds, and path patterns.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"level":          map[string]any{"type": "string", "description": "Filter by level: L0, L1, L2, L3"},
					"kind":           map[string]any{"type": "string", "description": "Filter by kind: function, method, class, interface, struct"},
					"name_pattern":   map[string]any{"type": "string", "description": "Substring or regex on symbol name"},
					"file_pattern":   map[string]any{"type": "string", "description": "Substring or glob on file path"},
					"min_complexity": map[string]any{"type": "integer", "description": "Minimum cyclomatic complexity"},
					"max_complexity": map[string]any{"type": "integer", "description": "Maximum cyclomatic complexity"},
					"min_in_degree":  map[string]any{"type": "integer", "description": "Minimum incoming calls/imports (hotspots)"},
					"max_in_degree":  map[string]any{"type": "integer", "description": "Maximum incoming calls/imports (0 = dead code / entry points)"},
					"min_out_degree": map[string]any{"type": "integer", "description": "Minimum outgoing calls/dependencies"},
					"max_out_degree": map[string]any{"type": "integer", "description": "Maximum outgoing calls/dependencies"},
					"is_public":      map[string]any{"type": "boolean", "description": "Filter by export / public visibility"},
					"sort_by":        map[string]any{"type": "string", "description": "Sort order: complexity, hotspot, in_degree, out_degree, name"},
					"limit":          map[string]any{"type": "integer", "description": "Max results (default: 25, max: 100)"},
				},
			},
		},
		{
			"name":        "manage_adr",
			"description": "Architecture Decision Record (ADR) Manager: Create, read, list, update, and manage persistent architectural decisions, technical constraints, and rationale across agent sessions.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":        map[string]any{"type": "string", "description": "Action: list, get, create, update, delete"},
					"id":            map[string]any{"type": "string", "description": "ADR identifier (e.g. 'ADR-0001' or '1')"},
					"title":         map[string]any{"type": "string", "description": "ADR title (for create/update)"},
					"status":        map[string]any{"type": "string", "description": "ADR status: proposed, accepted, deprecated, superseded"},
					"context":       map[string]any{"type": "string", "description": "Context and problem statement"},
					"decision":      map[string]any{"type": "string", "description": "Chosen architecture decision"},
					"consequences":  map[string]any{"type": "string", "description": "Tradeoffs and consequences"},
					"tags":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Category tags"},
					"superseded_by": map[string]any{"type": "string", "description": "Identifier of superseding ADR"},
				},
				"required": []string{"action"},
			},
		},
		{
			"name":        "list_routes",
			"description": "API Route Discovery: Inspect and search indexed HTTP/API endpoints, their methods, frameworks, and backing implementation handler symbols.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path_pattern": map[string]any{"type": "string", "description": "Substring pattern to match on route path (e.g. '/api/users')"},
					"method":       map[string]any{"type": "string", "description": "HTTP method filter: GET, POST, PUT, DELETE, PATCH, or ALL"},
				},
			},
		},
		{
			"name":        "detect_communities",
			"description": "Louvain Community Detection: Discovers functional code clusters, cohesion scores, and architecture boundary drift using graph topology analysis.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"min_size": map[string]any{"type": "integer", "description": "Minimum community size in modules (default: 2)"},
				},
			},
		},
	}
}

// ensureIndexed lazily builds the index the first time a query tool is used on
// an empty graph, so tools never return misleading "not found" results just
// because sync_index has not been called yet. Callers must refresh the cache
// state first (see callTool) so a partial sync from the hook is visible.
func (s *MCPServer) ensureIndexed() {
	if !s.cacheEngine.IsFullyIndexed() {
		s.cacheEngine.Sync(false)
	}
}

func (s *MCPServer) callTool(name string, args map[string]any) (string, error) {
	// The hook and the CLI run in separate processes, so reload both the graph
	// and the cache state before serving a request.
	s.cacheEngine.RefreshState()
	s.db.ReloadIfModified()

	switch name {
	case "zoom_module", "zoom_symbol", "zoom_search", "check_layer_violation", "estimate_blast_radius", "trace_call_path", "detect_changes", "query_graph", "list_routes", "detect_communities":
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

	case "scaffold_module":
		filePath, _ := args["file_path"].(string)
		kind, _ := args["kind"].(string)
		name, _ := args["name"].(string)
		namespace, _ := args["namespace"].(string)
		overwrite, _ := args["overwrite"].(bool)
		var imports []string
		if raw, ok := args["imports"].([]any); ok {
			for _, it := range raw {
				if str, ok := it.(string); ok {
					imports = append(imports, str)
				}
			}
		}
		res, err := s.codegen.ScaffoldModule(codegen.ScaffoldOptions{
			FilePath:  filePath,
			Kind:      kind,
			Name:      name,
			Namespace: namespace,
			Imports:   imports,
			Overwrite: overwrite,
		})
		if err != nil {
			return "", err
		}
		stats := s.cacheEngine.Sync(false)
		res["sync"] = stats
		data, err := json.MarshalIndent(res, "", "  ")
		return string(data), err

	case "apply_plan":
		rawOps, _ := args["operations"].([]any)
		var ops []codegen.PlanOperation
		for _, raw := range rawOps {
			var op codegen.PlanOperation
			blob, err := json.Marshal(raw)
			if err != nil {
				return "", fmt.Errorf("invalid operation: %v", err)
			}
			if err := json.Unmarshal(blob, &op); err != nil {
				return "", fmt.Errorf("invalid operation: %v", err)
			}
			ops = append(ops, op)
		}
		// Safety default: validate only unless the caller explicitly opts in.
		dryRun := true
		if dr, ok := args["dry_run"].(bool); ok {
			dryRun = dr
		}
		res, err := s.codegen.ApplyPlan(ops, dryRun)
		if err != nil {
			blob, _ := json.MarshalIndent(res, "", "  ")
			if blob != nil {
				return string(blob), err
			}
			return "", err
		}
		if !dryRun {
			stats := s.cacheEngine.Sync(false)
			res["sync"] = stats
		}
		data, err := json.MarshalIndent(res, "", "  ")
		return string(data), err

	case "trace_call_path":
		src, _ := args["source"].(string)
		tgt, _ := args["target"].(string)
		dir, _ := args["direction"].(string)
		maxDepth := 3
		if md, ok := args["max_depth"].(float64); ok {
			maxDepth = int(md)
		}
		res := s.tracer.TraceCallPath(src, tgt, dir, maxDepth)
		data, err := json.MarshalIndent(res, "", "  ")
		return string(data), err

	case "detect_changes":
		baseRef, _ := args["base_ref"].(string)
		scope, _ := args["scope"].(string)
		maxHops := 3
		if mh, ok := args["max_hops"].(float64); ok {
			maxHops = int(mh)
		}
		res := s.diff.DetectChanges(baseRef, scope, maxHops, "")
		data, err := json.MarshalIndent(res, "", "  ")
		return string(data), err

	case "query_graph":
		opts := query.GraphQueryOptions{}
		if lvl, ok := args["level"].(string); ok {
			opts.Level = lvl
		}
		if kind, ok := args["kind"].(string); ok {
			opts.Kind = kind
		}
		if np, ok := args["name_pattern"].(string); ok {
			opts.NamePattern = np
		}
		if fp, ok := args["file_pattern"].(string); ok {
			opts.FilePattern = fp
		}
		if mc, ok := args["min_complexity"].(float64); ok {
			opts.MinComplexity = int(mc)
		}
		if mc, ok := args["max_complexity"].(float64); ok {
			opts.MaxComplexity = int(mc)
		}
		if minIn, ok := args["min_in_degree"].(float64); ok {
			v := int(minIn)
			opts.MinInDegree = &v
		}
		if maxIn, ok := args["max_in_degree"].(float64); ok {
			v := int(maxIn)
			opts.MaxInDegree = &v
		}
		if minOut, ok := args["min_out_degree"].(float64); ok {
			v := int(minOut)
			opts.MinOutDegree = &v
		}
		if maxOut, ok := args["max_out_degree"].(float64); ok {
			v := int(maxOut)
			opts.MaxOutDegree = &v
		}
		if ip, ok := args["is_public"].(bool); ok {
			opts.IsPublic = &ip
		}
		if sb, ok := args["sort_by"].(string); ok {
			opts.SortBy = sb
		}
		if lim, ok := args["limit"].(float64); ok {
			opts.Limit = int(lim)
		}
		res := s.queryEng.QueryGraph(opts)
		data, err := json.MarshalIndent(res, "", "  ")
		return string(data), err

	case "manage_adr":
		action, _ := args["action"].(string)
		switch strings.ToLower(action) {
		case "list":
			statusFilter, _ := args["status"].(string)
			tagFilter, _ := args["tag"].(string)
			res := s.adrMgr.List(statusFilter, tagFilter)
			data, err := json.MarshalIndent(res, "", "  ")
			return string(data), err

		case "get":
			id, _ := args["id"].(string)
			res, err := s.adrMgr.Get(id)
			if err != nil {
				return "", err
			}
			data, err := json.MarshalIndent(res, "", "  ")
			return string(data), err

		case "create":
			title, _ := args["title"].(string)
			status, _ := args["status"].(string)
			ctx, _ := args["context"].(string)
			dec, _ := args["decision"].(string)
			conseq, _ := args["consequences"].(string)
			var tags []string
			if rawTags, ok := args["tags"].([]any); ok {
				for _, t := range rawTags {
					if str, ok := t.(string); ok {
						tags = append(tags, str)
					}
				}
			}
			res, err := s.adrMgr.Create(adr.CreateADROptions{
				Title:        title,
				Status:       status,
				Context:      ctx,
				Decision:     dec,
				Consequences: conseq,
				Tags:         tags,
			})
			if err != nil {
				return "", err
			}
			data, err := json.MarshalIndent(res, "", "  ")
			return string(data), err

		case "update":
			id, _ := args["id"].(string)
			upOpts := adr.UpdateADROptions{ID: id}
			if t, ok := args["title"].(string); ok && t != "" {
				upOpts.Title = &t
			}
			if s, ok := args["status"].(string); ok && s != "" {
				upOpts.Status = &s
			}
			if c, ok := args["context"].(string); ok {
				upOpts.Context = &c
			}
			if d, ok := args["decision"].(string); ok {
				upOpts.Decision = &d
			}
			if cq, ok := args["consequences"].(string); ok {
				upOpts.Consequences = &cq
			}
			if sup, ok := args["superseded_by"].(string); ok {
				upOpts.SupersededBy = &sup
			}
			if rawTags, ok := args["tags"].([]any); ok {
				var tags []string
				for _, itm := range rawTags {
					if str, ok := itm.(string); ok {
						tags = append(tags, str)
					}
				}
				upOpts.Tags = &tags
			}
			res, err := s.adrMgr.Update(upOpts)
			if err != nil {
				return "", err
			}
			data, err := json.MarshalIndent(res, "", "  ")
			return string(data), err

		case "delete":
			id, _ := args["id"].(string)
			if err := s.adrMgr.Delete(id); err != nil {
				return "", err
			}
			return `{"deleted": true, "id": "` + id + `"}`, nil

		default:
			return "", fmt.Errorf("unknown manage_adr action: %s", action)
		}

	case "list_routes":
		pathPattern, _ := args["path_pattern"].(string)
		method, _ := args["method"].(string)
		res := s.routesMgr.FindRoutes(pathPattern, method)
		data, err := json.MarshalIndent(res, "", "  ")
		return string(data), err

	case "detect_communities":
		minSize := 2
		if ms, ok := args["min_size"].(float64); ok {
			minSize = int(ms)
		}
		res := s.commDetector.DetectCommunities(minSize)
		data, err := json.MarshalIndent(res, "", "  ")
		return string(data), err

	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}
