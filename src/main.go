package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

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
	"meta-lattice/src/installer"
	"meta-lattice/src/mcp"
	"meta-lattice/src/storage"
)

var (
	Version   = "1.0.0"
	GitCommit = "none"
	BuildDate = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}

	command := os.Args[1]

	switch command {
	case "version", "-v", "--version":
		fmt.Printf("Meta-Lattice %s (commit: %s, built: %s)\n", Version, GitCommit, BuildDate)
		return
	case "sync":
		runSync(os.Args[2:])
	case "status":
		runStatus(os.Args[2:])
	case "zoom":
		runZoom(os.Args[2:])
	case "trace":
		runTrace(os.Args[2:])
	case "detect-changes":
		runDetectChanges(os.Args[2:])
	case "query":
		runQuery(os.Args[2:])
	case "adr":
		runADR(os.Args[2:])
	case "routes":
		runRoutes(os.Args[2:])
	case "communities", "clusters":
		runCommunities(os.Args[2:])
	case "audit":
		runAudit(os.Args[2:])
	case "blast":
		runBlast(os.Args[2:])
	case "scaffold":
		runScaffold(os.Args[2:])
	case "apply-plan":
		runApplyPlan(os.Args[2:])
	case "mcp":
		runMCP(os.Args[2:])
	case "hook":
		runHook(os.Args[2:])
	case "install":
		runInstall(os.Args[2:])
	case "-h", "--help", "help":
		printUsage()
	default:
		fmt.Printf("Unknown command: %s\n\n", command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Meta-Lattice (Go Native): Hierarchical Context Zoomer, Boundary Auditor & Blast Estimator

Usage:
  meta-lattice <command> [arguments]

Commands:
  sync                     Synchronize AST cache incrementally using Git SHA / file hash caching
                           Options: --force, --quiet, --json
  hook [event]             High-performance Claude Code plugin hook (post-tool, session-start)
                           Options: --file <path>, --quiet, --json
  status                   Display current LatticeDB index status and graph statistics
                           Options: --json
  zoom <action> [target]   Hierarchical Context Zoom (actions: overview, module, symbol, search)
                           Options: --no-sync, --json
  trace <symbol>           Call Chain Tracer: Multi-hop call path tracking
                           Options: --target <sym>, --direction <outbound|inbound|both>, --depth <n>, --no-sync, --json
  detect-changes           Change Impact Scoper: Map git diff to affected symbols and tests
                           Options: --base <ref>, --scope <all|tests>, --hops <n>, --no-sync, --json
  query                    Structural Code Graph Query: Filter by complexity, degree, kind, etc.
                           Options: --level <L0..L3>, --kind <k>, --min-complexity <n>, --dead-code, --hotspots, --json
  routes                   API Route Discovery: Inspect and search indexed HTTP/API endpoints
                           Options: --path <pattern>, --method <GET|POST...>, --no-sync, --json
  communities              Louvain Community Detection: Find topological clusters & boundary drift
                           Options: --min-size <n>, --no-sync, --json
  adr <action>             Architecture Decision Records: Manage ADRs across agent sessions
                           Actions: list, get <id>, create --title "..." --decision "...", delete <id>
  audit                    Architecture Boundary Auditor: Check circular dependencies & layer violations
                           Options: --file <path>, --no-sync, --json
  blast <target>           Blast Radius Estimator: Simulate ripple effects and breaking changes
                           Options: --type <signature|body|removal|rename>, --hops <n>, --no-sync, --json
  scaffold                 Code Generation: Create a new module file from a language template
                           Options: --path <file>, --kind <class|interface|struct|enum|module>,
                                   --name <Type>, --namespace <pkg>, --import <a,b>, --overwrite, --json
  apply-plan               Code Generation: Apply a JSON edit plan (create/replace/insert/delete)
                           Options: --file <plan.json> ('-' reads stdin), --execute, --json
                           (default is --dry-run validation; pass --execute to write)
  mcp                      Start the Model Context Protocol (MCP) server over stdio
  install                  One-click installer for Claude Code, OpenAI Codex & Google Antigravity
                           Options: --claude, --codex, --antigravity, --all, --status, --uninstall
  version                  Show version, commit hash, and build date`)
}

func openWorkspace(doSync bool) (*config.LatticeConfig, *storage.GraphStorage, *indexer.CacheEngine) {
	cfg := config.GetConfig()
	db := storage.NewGraphStorage(cfg.DBPath)
	engine := indexer.NewCacheEngine(cfg, db)
	if doSync {
		engine.Sync(false)
	}
	return cfg, db, engine
}

func printJSON(v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}

// popBoolFlag extracts a `--name` switch from raw args and returns the
// remaining args plus whether the switch was present.
func popBoolFlag(args []string, name string) ([]string, bool) {
	want := "--" + name
	rest := make([]string, 0, len(args))
	found := false
	for _, a := range args {
		if a == want {
			found = true
			continue
		}
		rest = append(rest, a)
	}
	return rest, found
}

func runSync(args []string) {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	force := fs.Bool("force", false, "Force full re-indexing of all files")
	quiet := fs.Bool("quiet", false, "Quiet mode: suppress verbose banners")
	asJSON := fs.Bool("json", false, "Print machine-readable JSON")
	_ = fs.Parse(args)

	cfg, db, engine := openWorkspace(false)
	_ = cfg
	_ = db

	if !*asJSON && !*quiet {
		fmt.Println("Scanning and indexing workspace...")
	}
	stats := engine.Sync(*force)

	if stats.Error != "" {
		// A failed save must not be reported as a successful (zeroed) sync.
		fmt.Fprintf(os.Stderr, "Error: sync failed: %s\n", stats.Error)
		if *asJSON {
			printJSON(stats)
		}
		os.Exit(1)
	}

	if *asJSON {
		printJSON(stats)
		return
	}
	if *quiet {
		return
	}

	fmt.Println("\n=== LatticeDB Incremental Sync Complete ===")
	fmt.Printf("• Elapsed Time:          %d ms\n", stats.ElapsedMS)
	fmt.Printf("• Total Files Scanned:   %d\n", stats.TotalFiles)
	fmt.Printf("• Added Files:           %d\n", stats.AddedCount)
	fmt.Printf("• Modified Files:        %d\n", stats.ModifiedCount)
	fmt.Printf("• Deleted Files:         %d\n", stats.DeletedCount)
	fmt.Printf("• Cache Hits (Unchanged):%d\n", stats.CachedCount)
	fmt.Printf("• L0 Domains:            %d\n", stats.Nodes["l0_domains"])
	fmt.Printf("• L1 Modules:            %d\n", stats.Nodes["l1_modules"])
	fmt.Printf("• L2 Classes/Interfaces: %d\n", stats.Nodes["l2_classes"])
	fmt.Printf("• L3 Functions/Symbols:  %d\n", stats.Nodes["l3_symbols"])
	fmt.Printf("• Total Graph Edges:     %d\n", stats.Nodes["total_edges"])
	commit := stats.GitCommit
	if len(commit) > 8 {
		commit = commit[:8]
	}
	if commit == "" {
		commit = "(none)"
	}
	fmt.Printf("• Git Commit:            %s\n", commit)
}

func runStatus(args []string) {
	rest, asJSON := popBoolFlag(args, "json")
	_ = rest
	_, db, engine := openWorkspace(false)
	counts := db.CountNodesAndEdges()
	cached := len(engine.ScanFiles())

	res := map[string]any{
		"database_engine": db.DatabaseEngine(),
		"database_file":   db.DatabasePath(),
		"tracked_files":   cached,
		"counts":          counts,
	}
	if asJSON {
		printJSON(res)
		return
	}

	fmt.Println("\n=== LatticeDB Index Status ===")
	fmt.Printf("• Database Engine:       %s\n", db.DatabaseEngine())
	fmt.Printf("• Database File:         %s\n", db.DatabasePath())
	fmt.Printf("• Tracked Files:         %d\n", cached)
	fmt.Printf("• L0 Domain Nodes:       %d\n", counts["l0_domains"])
	fmt.Printf("• L1 Module Nodes:       %d\n", counts["l1_modules"])
	fmt.Printf("• L2 Class Nodes:        %d\n", counts["l2_classes"])
	fmt.Printf("• L3 Symbol Nodes:       %d\n", counts["l3_symbols"])
	fmt.Printf("• Total Graph Edges:     %d\n", counts["total_edges"])
}

func runZoom(args []string) {
	args, asJSON := popBoolFlag(args, "json")
	args, noSync := popBoolFlag(args, "no-sync")
	action := "overview"
	target := ""
	if len(args) > 0 {
		action = args[0]
	}
	if len(args) > 1 {
		target = args[1]
	}

	_, db, _ := openWorkspace(!noSync)
	z := zoomer.NewHierarchicalZoomer(db)

	switch action {
	case "overview":
		res := z.ZoomOverview(target)
		if asJSON {
			printJSON(res)
			return
		}
		domains, _ := res["domains"].([]map[string]any)
		fmt.Printf("\n=== L0/L1 Architectural Overview (%d domains) ===\n", len(domains))
		for _, d := range domains {
			modules, _ := d["modules"].([]map[string]any)
			fmt.Printf("\n[Domain: %s] (%v modules)\n", d["name"], d["total_modules"])
			for _, m := range modules {
				var exportsStr string
				if ex, ok := m["exports"].([]string); ok {
					exportsStr = strings.Join(ex, ", ")
				}
				if exportsStr == "" {
					exportsStr = "none"
				}
				fmt.Printf("  - %s (%v LOC) - exports: %s\n", m["path"], m["loc"], exportsStr)
			}
		}

	case "module":
		if target == "" {
			fmt.Println("Error: Please specify a file path for 'module' zoom.")
			return
		}
		res := z.ZoomModule(target)
		if asJSON {
			printJSON(res)
			return
		}
		if errStr, ok := res["error"].(string); ok {
			fmt.Printf("Error: %s\n", errStr)
			return
		}
		fileMeta, _ := res["file"].(map[string]any)
		fmt.Printf("\n=== L1 Module: %s (%v LOC, %s) ===\n", fileMeta["path"], fileMeta["loc"], fileMeta["language"])
		if classes, ok := res["classes"].([]map[string]any); ok && len(classes) > 0 {
			fmt.Println("\nClasses & Interfaces (Method signatures only):")
			for _, c := range classes {
				fmt.Printf("  • %s (Lines: %s)\n", c["name"], c["lines"])
				if sigs, ok := c["method_signatures"].([]string); ok {
					for _, sig := range sigs {
						fmt.Printf("    * %s\n", sig)
					}
				}
			}
		}
		if funcs, ok := res["top_level_functions"].([]map[string]any); ok && len(funcs) > 0 {
			fmt.Println("\nTop-Level Functions:")
			for _, fn := range funcs {
				fmt.Printf("  • %s (Lines: %s)\n", fn["signature"], fn["lines"])
			}
		}

	case "symbol":
		if target == "" {
			fmt.Println("Error: Please specify a symbol name for 'symbol' zoom.")
			return
		}
		res := z.ZoomSymbol(target, "")
		if asJSON {
			printJSON(res)
			return
		}
		if errStr, ok := res["error"].(string); ok {
			fmt.Printf("Error: %s\n", errStr)
			return
		}
		sym, _ := res["symbol"].(map[string]any)
		fmt.Printf("\n=== L3 Symbol Detail: %s ===\n", sym["name"])
		fmt.Printf("File: %s:%s | Cyclomatic Complexity: %v\n", sym["file_path"], sym["lines"], sym["cyclomatic_complexity"])
		fmt.Printf("\nSignature:\n  %s\n", sym["signature"])
		fmt.Printf("\nSource Code:\n%s\n", sym["code"])

	case "search":
		if target == "" {
			fmt.Println("Error: Please specify a search query.")
			return
		}
		res := z.ZoomSearch(target, "", 15)
		if asJSON {
			printJSON(res)
			return
		}
		results, _ := res["results"].([]map[string]any)
		fmt.Printf("\n=== BM25 Search Results for '%s' (%d matches) ===\n", target, len(results))
		for _, r := range results {
			desc := r["signature"]
			if desc == "" {
				desc = r["docstring"]
			}
			fmt.Printf("[%s] %-25s %-30s %s\n", r["level"], r["name"], r["file_path"], desc)
		}

	default:
		fmt.Printf("Unknown zoom action: %s\n", action)
	}
}

func runAudit(args []string) {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	targetFile := fs.String("file", "", "Specific file path to audit")
	noSync := fs.Bool("no-sync", false, "Skip automatic pre-audit sync")
	asJSON := fs.Bool("json", false, "Print machine-readable JSON")
	_ = fs.Parse(args)

	cfg, db, _ := openWorkspace(!*noSync)
	aud := auditor.NewArchitectureBoundaryAuditor(db, cfg)
	res := aud.CheckLayerViolation(*targetFile, nil)

	if *asJSON {
		printJSON(res)
		return
	}

	fmt.Printf("\n=== Architecture Boundary Audit Status: %s ===\n", res["status"])
	fmt.Printf("Target: %s\n", res["audited_target"])

	if violations, ok := res["layer_violations"].([]map[string]any); ok && len(violations) > 0 {
		fmt.Printf("\n[Layer Boundary Violations: %d]\n", len(violations))
		for _, v := range violations {
			fmt.Printf("  • %s -> %s\n", v["source_file"], v["target_file"])
			fmt.Printf("    Layer: %s -> %s\n", v["source_layer"], v["target_layer"])
			fmt.Printf("    Reason: %s\n", v["reason"])
			fmt.Printf("    Remediation: %s\n\n", v["remediation"])
		}
	}

	if cycles, ok := res["circular_dependencies"].([]map[string]any); ok && len(cycles) > 0 {
		fmt.Printf("\n[Circular Dependencies Detected: %d]\n", len(cycles))
		for _, c := range cycles {
			fmt.Printf("  • Cycle (len %v): %s\n", c["cycle_length"], c["display_chain"])
		}
	}

	if res["status"] == "PASSED" {
		fmt.Println("\n[OK] No layer violations or circular dependencies detected.")
	}
}

func runBlast(args []string) {
	args, asJSON := popBoolFlag(args, "json")
	args, noSync := popBoolFlag(args, "no-sync")
	if len(args) == 0 {
		fmt.Println("Error: Target symbol or path is required for 'blast'.")
		return
	}
	target := args[0]
	changeType := "signature"
	hops := 4

	for i := 1; i < len(args); i++ {
		if args[i] == "--type" && i+1 < len(args) {
			changeType = args[i+1]
			i++
		} else if args[i] == "--hops" && i+1 < len(args) {
			if h, err := strconv.Atoi(args[i+1]); err == nil {
				hops = h
			}
			i++
		}
	}

	_, db, _ := openWorkspace(!noSync)
	bl := blast.NewBlastRadiusEstimator(db)
	res := bl.EstimateBlastRadius(target, changeType, hops)

	if asJSON {
		printJSON(res)
		return
	}

	if errStr, ok := res["error"].(string); ok {
		fmt.Printf("Error: %s\n", errStr)
		return
	}

	fmt.Println("\n=== Blast Radius Estimation Report ===")
	fmt.Printf("Target:         %s\n", res["target"])
	fmt.Printf("Change Type:    %s\n", res["change_type"])
	fmt.Printf("Blast Score:    %v/100 [%s RISK]\n", res["blast_score"], res["risk_tier"])
	fmt.Printf("Recommendation: %s\n", res["recommendation"])

	if points, ok := res["top_10_breaking_points"].([]map[string]any); ok && len(points) > 0 {
		fmt.Printf("\nTop %d Breaking Change Points:\n", len(points))
		for _, p := range points {
			fmt.Printf("  [Hop %v] %-30s (%s:%v) - Score: %v\n", p["hop_distance"], p["symbol_name"], p["file_path"], p["line"], p["impact_score"])
			fmt.Printf("          Note: %s\n", p["risk_description"])
		}
	}

	fmt.Println("\nImpact Cascade Tree:")
	fmt.Println(res["blast_tree"])
}

func runTrace(args []string) {
	args, asJSON := popBoolFlag(args, "json")
	args, noSync := popBoolFlag(args, "no-sync")
	if len(args) == 0 {
		fmt.Println("Error: Target symbol or file is required for 'trace'.")
		return
	}
	source := args[0]
	target := ""
	direction := "outbound"
	depth := 3

	for i := 1; i < len(args); i++ {
		if args[i] == "--target" && i+1 < len(args) {
			target = args[i+1]
			i++
		} else if args[i] == "--direction" && i+1 < len(args) {
			direction = args[i+1]
			i++
		} else if (args[i] == "--depth" || args[i] == "--hops") && i+1 < len(args) {
			if d, err := strconv.Atoi(args[i+1]); err == nil {
				depth = d
			}
			i++
		}
	}

	_, db, _ := openWorkspace(!noSync)
	tc := tracer.NewCallTracer(db)
	res := tc.TraceCallPath(source, target, direction, depth)

	if asJSON {
		printJSON(res)
		return
	}

	if res.Error != "" {
		fmt.Printf("Error: %s\n", res.Error)
		return
	}

	fmt.Printf("\n=== Call Chain Trace: %s (Direction: %s, Max Depth: %d) ===\n", source, direction, depth)
	if target != "" {
		fmt.Printf("Target: %s | Paths Found: %d\n\n", target, res.PathsFound)
		for idx, p := range res.Paths {
			fmt.Printf("[Path #%d] (Length: %d hops)\n", idx+1, len(p))
			for _, hop := range p {
				fmt.Printf("  Step %d: %s (%s:%s) --[%s]--> %s (%s:%s)\n",
					hop.HopDepth, hop.FromName, hop.FromFile, hop.FromLines, hop.EdgeType, hop.ToName, hop.ToFile, hop.ToLines)
			}
		}
	} else {
		fmt.Printf("Total Traversal Hops: %d | Unique Symbols Reached: %d\n\n", res.TotalHops, res.UniqueSymbols)
		for _, step := range res.CallTree {
			depth := step["depth"].(int)
			indent := strings.Repeat("  ", depth)
			fmt.Printf("%s└─ [Hop %d] %s (%s:%s) -> %s\n",
				indent, depth, step["to_name"], step["to_file"], step["to_lines"], step["to_signature"])
		}
	}
}

func runDetectChanges(args []string) {
	args, asJSON := popBoolFlag(args, "json")
	args, noSync := popBoolFlag(args, "no-sync")
	baseRef := ""
	scope := "all"
	hops := 3

	for i := 0; i < len(args); i++ {
		if args[i] == "--base" && i+1 < len(args) {
			baseRef = args[i+1]
			i++
		} else if args[i] == "--scope" && i+1 < len(args) {
			scope = args[i+1]
			i++
		} else if args[i] == "--hops" && i+1 < len(args) {
			if h, err := strconv.Atoi(args[i+1]); err == nil {
				hops = h
			}
			i++
		}
	}

	cfg, db, _ := openWorkspace(!noSync)
	cd := diff.NewChangeDetector(cfg, db)
	res := cd.DetectChanges(baseRef, scope, hops, "")

	if asJSON {
		printJSON(res)
		return
	}

	if res.Error != "" {
		fmt.Printf("Error: %s\n", res.Error)
		return
	}

	fmt.Printf("\n=== Git Change Impact Analysis ===\n")
	fmt.Printf("Base Ref:   %s\n", func() string {
		if res.BaseRef != "" {
			return res.BaseRef
		}
		return "(uncommitted/HEAD)"
	}())
	fmt.Printf("Risk Level: %s (Blast Score: %d/100)\n", res.RiskTier, res.BlastScore)
	fmt.Printf("Summary:    %s\n", res.Summary)

	if len(res.ChangedSymbols) > 0 {
		fmt.Printf("\n[Directly Touched Symbols: %d]\n", len(res.ChangedSymbols))
		for _, s := range res.ChangedSymbols {
			fmt.Printf("  • %-30s (%s:%s) - %s\n", s.Name, s.FilePath, s.Lines, s.Signature)
		}
	}

	if len(res.AffectedTests) > 0 {
		fmt.Printf("\n[Affected Test Targets to Run: %d]\n", len(res.AffectedTests))
		for _, t := range res.AffectedTests {
			fmt.Printf("  • [Hop %d] %s (in %s) [triggered by %s]\n", t.Distance, t.TestSymbol, t.TestFile, t.TargetSymbol)
		}
	}

	if len(res.AffectedSymbols) > 0 && scope != "tests" {
		fmt.Printf("\n[Top Downstream Impacted Symbols: %d]\n", len(res.AffectedSymbols))
		limit := 10
		if len(res.AffectedSymbols) < limit {
			limit = len(res.AffectedSymbols)
		}
		for _, a := range res.AffectedSymbols[:limit] {
			fmt.Printf("  • [Hop %d] %-30s (%s)\n", a.Distance, a.Name, a.FilePath)
		}
	}
}

func runQuery(args []string) {
	args, asJSON := popBoolFlag(args, "json")
	args, noSync := popBoolFlag(args, "no-sync")
	args, deadCode := popBoolFlag(args, "dead-code")
	args, hotspots := popBoolFlag(args, "hotspots")

	opts := query.GraphQueryOptions{Limit: 25}
	if deadCode {
		zero := 0
		opts.MaxInDegree = &zero
		opts.MaxOutDegree = &zero
	}
	if hotspots {
		minIn := 5
		opts.MinInDegree = &minIn
		opts.SortBy = "hotspot"
	}

	for i := 0; i < len(args); i++ {
		if args[i] == "--level" && i+1 < len(args) {
			opts.Level = args[i+1]
			i++
		} else if args[i] == "--kind" && i+1 < len(args) {
			opts.Kind = args[i+1]
			i++
		} else if args[i] == "--name" && i+1 < len(args) {
			opts.NamePattern = args[i+1]
			i++
		} else if args[i] == "--file" && i+1 < len(args) {
			opts.FilePattern = args[i+1]
			i++
		} else if args[i] == "--min-complexity" && i+1 < len(args) {
			if c, err := strconv.Atoi(args[i+1]); err == nil {
				opts.MinComplexity = c
			}
			i++
		} else if args[i] == "--min-in-degree" && i+1 < len(args) {
			if d, err := strconv.Atoi(args[i+1]); err == nil {
				opts.MinInDegree = &d
			}
			i++
		} else if args[i] == "--sort" && i+1 < len(args) {
			opts.SortBy = args[i+1]
			i++
		} else if args[i] == "--limit" && i+1 < len(args) {
			if l, err := strconv.Atoi(args[i+1]); err == nil {
				opts.Limit = l
			}
			i++
		}
	}

	_, db, _ := openWorkspace(!noSync)
	qe := query.NewGraphQueryEngine(db)
	res := qe.QueryGraph(opts)

	if asJSON {
		printJSON(res)
		return
	}

	fmt.Printf("\n=== Graph Query Results: %d matched (scanned: %d) ===\n", res.TotalMatched, res.TotalScanned)
	for _, n := range res.Nodes {
		fmt.Printf("  • [%s] %-30s (%s:%s)\n", n.Level, n.Name, n.FilePath, n.Lines)
		fmt.Printf("    In-degree: %d | Out-degree: %d | Complexity: %d | Signature: %s\n",
			n.InDegree, n.OutDegree, n.CyclomaticComplexity, n.Signature)
	}
}

func runADR(args []string) {
	args, asJSON := popBoolFlag(args, "json")
	if len(args) == 0 {
		fmt.Println("Usage: meta-lattice adr <list|get|create|update|delete> [arguments]")
		return
	}
	action := args[0]
	cfg := config.GetConfig()
	am := adr.NewADRManager(cfg)

	switch strings.ToLower(action) {
	case "list":
		all := am.List("", "")
		if asJSON {
			printJSON(all)
			return
		}
		fmt.Printf("\n=== Architecture Decision Records (%d total) ===\n", len(all))
		for _, r := range all {
			fmt.Printf("  • %-10s [%s] %s (%s)\n", r.ID, strings.ToUpper(string(r.Status)), r.Title, r.Date)
		}

	case "get":
		if len(args) < 2 {
			fmt.Println("Error: ADR ID or number is required.")
			return
		}
		rec, err := am.Get(args[1])
		if err != nil {
			fmt.Printf("Error: %s\n", err)
			return
		}
		if asJSON {
			printJSON(rec)
			return
		}
		fmt.Printf("\n=== %s: %s ===\n", rec.ID, rec.Title)
		fmt.Printf("Status:       %s\n", rec.Status)
		fmt.Printf("Date:         %s\n", rec.Date)
		fmt.Printf("Tags:         %s\n", strings.Join(rec.Tags, ", "))
		if rec.SupersededBy != "" {
			fmt.Printf("Superseded By:%s\n", rec.SupersededBy)
		}
		fmt.Printf("\n[Context]\n%s\n", rec.Context)
		fmt.Printf("\n[Decision]\n%s\n", rec.Decision)
		fmt.Printf("\n[Consequences]\n%s\n", rec.Consequences)

	case "create":
		fs := flag.NewFlagSet("adr-create", flag.ExitOnError)
		title := fs.String("title", "", "ADR title")
		status := fs.String("status", "accepted", "ADR status")
		context := fs.String("context", "", "ADR context")
		decision := fs.String("decision", "", "ADR decision")
		consequences := fs.String("consequences", "", "ADR consequences")
		tagsCSV := fs.String("tags", "", "Comma-separated tags")
		_ = fs.Parse(args[1:])

		var tags []string
		for _, t := range strings.Split(*tagsCSV, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
		rec, err := am.Create(adr.CreateADROptions{
			Title:        *title,
			Status:       *status,
			Context:      *context,
			Decision:     *decision,
			Consequences: *consequences,
			Tags:         tags,
		})
		if err != nil {
			fmt.Printf("Error: %s\n", err)
			return
		}
		if asJSON {
			printJSON(rec)
			return
		}
		fmt.Printf("\n[✓] Created %s: %s\n", rec.ID, rec.Title)

	case "delete":
		if len(args) < 2 {
			fmt.Println("Error: ADR ID is required for delete.")
			return
		}
		if err := am.Delete(args[1]); err != nil {
			fmt.Printf("Error: %s\n", err)
			return
		}
		fmt.Printf("[✓] Deleted %s\n", args[1])

	default:
		fmt.Printf("Unknown adr action: %s\n", action)
	}
}

func runRoutes(args []string) {
	args, asJSON := popBoolFlag(args, "json")
	args, noSync := popBoolFlag(args, "no-sync")
	pathPattern := ""
	method := ""

	for i := 0; i < len(args); i++ {
		if args[i] == "--path" && i+1 < len(args) {
			pathPattern = args[i+1]
			i++
		} else if args[i] == "--method" && i+1 < len(args) {
			method = args[i+1]
			i++
		}
	}

	_, db, _ := openWorkspace(!noSync)
	rm := routes.NewRouteManager(db)
	res := rm.FindRoutes(pathPattern, method)

	if asJSON {
		printJSON(res)
		return
	}

	fmt.Printf("\n=== Discovered HTTP / API Endpoints (%d matched) ===\n", len(res))
	if len(res) == 0 {
		fmt.Println("No routes matched. Run `meta-lattice sync` to update the AST index.")
		return
	}
	for _, r := range res {
		fmt.Printf("  • %-7s %-35s -> %s (%s:%d) [%s]\n",
			r.Method, r.Path, r.HandlerName, r.FilePath, r.Line, r.Framework)
	}
}

func runCommunities(args []string) {
	args, asJSON := popBoolFlag(args, "json")
	args, noSync := popBoolFlag(args, "no-sync")
	minSize := 2

	for i := 0; i < len(args); i++ {
		if args[i] == "--min-size" && i+1 < len(args) {
			if s, err := strconv.Atoi(args[i+1]); err == nil {
				minSize = s
			}
			i++
		}
	}

	_, db, _ := openWorkspace(!noSync)
	detector := community.NewCommunityDetector(db)
	res := detector.DetectCommunities(minSize)

	if asJSON {
		printJSON(res)
		return
	}

	fmt.Printf("\n=== Louvain Topological Community Detection ===\n")
	fmt.Printf("Modularity Q:        %0.3f [%s]\n", res.ModularityScore, res.ArchitectureHealth)
	fmt.Printf("Discovered Clusters: %d communities across %d modules\n", res.CommunityCount, res.TotalModules)
	fmt.Printf("Summary:             %s\n\n", res.Summary)

	for _, c := range res.Communities {
		fmt.Printf("[Cluster %s: %s] (%d modules, Cohesion: %0.2f)\n", c.ID, strings.ToUpper(c.SuggestedName), c.Size, c.Cohesion)
		for _, m := range c.Modules {
			fmt.Printf("  └─ %s\n", m)
		}
		fmt.Println()
	}

	if len(res.DriftAlerts) > 0 {
		fmt.Printf("[Architecture Boundary Drift Alerts: %d]\n", len(res.DriftAlerts))
		for _, a := range res.DriftAlerts {
			fmt.Printf("  ⚠️ %s\n", a.Recommendation)
		}
	}
}

func runScaffold(args []string) {
	fs := flag.NewFlagSet("scaffold", flag.ExitOnError)
	filePath := fs.String("path", "", "New file path relative to workspace (required)")
	kind := fs.String("kind", "module", "class, interface, struct, enum, or module")
	name := fs.String("name", "", "Type name (defaults to file stem)")
	namespace := fs.String("namespace", "", "Package/namespace declaration")
	importsCSV := fs.String("import", "", "Comma-separated module specs to import")
	overwrite := fs.Bool("overwrite", false, "Replace an existing file")
	asJSON := fs.Bool("json", false, "Print machine-readable JSON")
	_ = fs.Parse(args)

	if *filePath == "" {
		fmt.Println("Error: --path is required for 'scaffold'.")
		os.Exit(1)
	}

	var imports []string
	for _, imp := range strings.Split(*importsCSV, ",") {
		if imp = strings.TrimSpace(imp); imp != "" {
			imports = append(imports, imp)
		}
	}

	cfg, _, engine := openWorkspace(false)
	cg := codegen.NewCodegen(cfg.WorkspaceRoot)
	res, err := cg.ScaffoldModule(codegen.ScaffoldOptions{
		FilePath:  *filePath,
		Kind:      *kind,
		Name:      *name,
		Namespace: *namespace,
		Imports:   imports,
		Overwrite: *overwrite,
	})
	if err != nil {
		if *asJSON {
			printJSON(map[string]any{"error": err.Error()})
		} else {
			fmt.Printf("Error: %s\n", err)
		}
		os.Exit(1)
	}

	stats := engine.Sync(false)
	res["sync"] = stats

	if *asJSON {
		printJSON(res)
		return
	}
	fmt.Printf("\n[✓] Created %s (%s %s, %v bytes)\n", res["created"], res["language"], res["kind"], res["bytes"])
	fmt.Println("Indexed. Verify with: ./meta-lattice zoom module", res["created"])
}

func runApplyPlan(args []string) {
	fs := flag.NewFlagSet("apply-plan", flag.ExitOnError)
	planFile := fs.String("file", "", "JSON plan file with {\"operations\": [...]} ('-' reads stdin)")
	execute := fs.Bool("execute", false, "Write changes (default is dry-run validation)")
	asJSON := fs.Bool("json", false, "Print machine-readable JSON")
	_ = fs.Parse(args)

	if *planFile == "" {
		fmt.Println("Error: --file is required for 'apply-plan'.")
		os.Exit(1)
	}

	var data []byte
	var err error
	if *planFile == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(*planFile)
	}
	if err != nil {
		fmt.Printf("Error: cannot read plan: %s\n", err)
		os.Exit(1)
	}

	var plan struct {
		Operations []codegen.PlanOperation `json:"operations"`
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		// Also accept a bare operations array.
		var bare []codegen.PlanOperation
		if err2 := json.Unmarshal(data, &bare); err2 != nil {
			fmt.Printf("Error: invalid plan JSON: %s\n", err)
			os.Exit(1)
		}
		plan.Operations = bare
	}

	cfg, _, engine := openWorkspace(false)
	cg := codegen.NewCodegen(cfg.WorkspaceRoot)
	res, err := cg.ApplyPlan(plan.Operations, !*execute)
	if err == nil && *execute {
		stats := engine.Sync(false)
		res["sync"] = stats
	}
	if err != nil {
		if res == nil {
			res = map[string]any{"error": err.Error(), "rollback": false}
		}
		if *asJSON {
			printJSON(res)
		} else if rolledBack, _ := res["rollback"].(bool); rolledBack {
			fmt.Printf("Error: %s (rolled back)\n", res["error"])
		} else {
			fmt.Printf("Error: %s (no changes were applied)\n", res["error"])
		}
		os.Exit(1)
	}

	if *asJSON {
		printJSON(res)
		return
	}
	if !*execute {
		fmt.Printf("\n[✓] Dry run OK: %v operation(s) validated, no files modified. Re-run with --execute to apply.\n", res["applied"])
		return
	}
	fmt.Printf("\n[✓] Applied %v operation(s) and re-indexed.\n", res["applied"])
}

func runMCP(args []string) {
	server := mcp.NewMCPServer("")
	if err := server.RunStdio(); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server exited with error: %v\n", err)
	}
}

func runInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	claude := fs.Bool("claude", false, "Install for Claude Code")
	codex := fs.Bool("codex", false, "Install for OpenAI Codex")
	antigravity := fs.Bool("antigravity", false, "Install for Google Antigravity")
	all := fs.Bool("all", false, "Install for all platforms (Claude Code, Codex, Antigravity)")
	status := fs.Bool("status", false, "Check installation status across all platforms")
	uninstall := fs.Bool("uninstall", false, "Uninstall configurations")
	_ = fs.Parse(args)

	inst := installer.NewInstaller("")

	if *status {
		inst.CheckStatus()
		return
	}

	if *uninstall {
		fmt.Println("Removing Meta-Lattice configurations...")
		specific := *claude || *codex || *antigravity
		if *claude || *all || !specific {
			_ = inst.UninstallClaudeCode()
		}
		if *codex || *all || !specific {
			_ = inst.UninstallCodex()
		}
		if *antigravity || *all || !specific {
			_ = inst.UninstallAntigravity()
		}
		fmt.Println("[✓] Uninstallation complete.")
		return
	}

	specific := *claude || *codex || *antigravity
	doClaude := *all || *claude || !specific
	doCodex := *all || *codex || !specific
	doAgy := *all || *antigravity || !specific

	if doClaude {
		if err := inst.InstallClaudeCode(); err != nil {
			fmt.Fprintf(os.Stderr, "Claude Code installation failed: %v\n", err)
			os.Exit(1)
		}
	}
	if doCodex {
		if err := inst.InstallCodex(); err != nil {
			fmt.Fprintf(os.Stderr, "Codex installation failed: %v\n", err)
			os.Exit(1)
		}
	}
	if doAgy {
		if err := inst.InstallAntigravity(); err != nil {
			fmt.Fprintf(os.Stderr, "Antigravity installation failed: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Println("\n[✓] Meta-Lattice installation complete!")
	inst.CheckStatus()
}

func runHook(args []string) {
	hookEvent := "post-tool"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		hookEvent = args[0]
		args = args[1:]
	}

	fs := flag.NewFlagSet("hook", flag.ExitOnError)
	targetFileFlag := fs.String("file", "", "Specific target file to sync and audit")
	asJSON := fs.Bool("json", false, "Print machine-readable JSON")
	quiet := fs.Bool("quiet", false, "Quiet mode: print only when violations/errors are found")
	_ = fs.Parse(args)

	switch hookEvent {
	case "session-start", "SessionStart":
		if *asJSON {
			printJSON(map[string]string{"status": "ready", "indexing": "deferred"})
		} else if !*quiet {
			fmt.Println("[✓] Meta-Lattice: Session ready (indexing deferred until first query)")
		}
		return

	case "post-tool", "post-edit", "PostToolUse":
		targetFile := *targetFileFlag

		// Check if Claude Code piped JSON payload via stdin
		if targetFile == "" {
			if stat, err := os.Stdin.Stat(); err == nil && (stat.Mode()&os.ModeCharDevice) == 0 {
				data, _ := io.ReadAll(io.LimitReader(os.Stdin, 512*1024))
				if len(data) > 0 {
					var payload struct {
						ToolName  string `json:"tool_name"`
						ToolInput struct {
							FilePath   string `json:"file_path"`
							Path       string `json:"path"`
							TargetFile string `json:"target_file"`
						} `json:"tool_input"`
					}
					if err := json.Unmarshal(data, &payload); err == nil {
						if payload.ToolInput.FilePath != "" {
							targetFile = payload.ToolInput.FilePath
						} else if payload.ToolInput.Path != "" {
							targetFile = payload.ToolInput.Path
						} else if payload.ToolInput.TargetFile != "" {
							targetFile = payload.ToolInput.TargetFile
						}
					}
				}
			}
		}

		cfg, db, engine := openWorkspace(false)
		startTime := time.Now()
		var syncStats indexer.SyncStats
		var syncErrText string
		if targetFile != "" {
			var syncErr error
			syncStats, syncErr = engine.SyncTargetFile(targetFile)
			if syncErr != nil {
				syncErrText = syncErr.Error()
			}
		} else {
			syncStats = engine.Sync(false)
			syncErrText = syncStats.Error
		}
		if syncErrText != "" {
			syncStats.Error = syncErrText
			fmt.Fprintln(os.Stderr, syncErrText)
		}

		aud := auditor.NewArchitectureBoundaryAuditor(db, cfg)
		auditRes := aud.CheckLayerViolation(targetFile, nil)
		elapsed := int(time.Since(startTime).Milliseconds())

		if *asJSON {
			printJSON(map[string]any{
				"sync":    syncStats,
				"audit":   auditRes,
				"elapsed": elapsed,
			})
			return
		}

		status, _ := auditRes["status"].(string)
		violations, _ := auditRes["layer_violations"].([]map[string]any)
		cycles, _ := auditRes["circular_dependencies"].([]map[string]any)

		if status == "VIOLATIONS_DETECTED" || len(violations) > 0 || len(cycles) > 0 {
			fmt.Printf("\n[!] Meta-Lattice Architectural Alert (%d ms):\n", elapsed)
			for _, v := range violations {
				fmt.Printf("  • Forbidden Import: %s -> %s (%s -> %s)\n", v["source_file"], v["target_file"], v["source_layer"], v["target_layer"])
				if r, ok := v["reason"].(string); ok && r != "" {
					fmt.Printf("    Reason: %s\n", r)
				}
			}
			for _, c := range cycles {
				fmt.Printf("  • Circular Dependency: %s\n", c["display_chain"])
			}
			return
		}

		if !*quiet {
			if targetFile != "" {
				fmt.Printf("[✓] Meta-Lattice: %s synced & architecture verified (%d ms)\n", targetFile, elapsed)
			} else {
				fmt.Printf("[✓] Meta-Lattice: Incremental AST synced & architecture verified (%d ms)\n", elapsed)
			}
		}

	default:
		fmt.Printf("Unknown hook event: %s\n", hookEvent)
	}
}
