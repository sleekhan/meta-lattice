package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"meta-lattice/src/config"
	"meta-lattice/src/features/auditor"
	"meta-lattice/src/features/blast"
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
	case "audit":
		runAudit(os.Args[2:])
	case "blast":
		runBlast(os.Args[2:])
	case "mcp":
		runMCP(os.Args[2:])
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
                           Options: --force
  status                   Display current LatticeDB index status and graph statistics
  zoom <action> [target]   Hierarchical Context Zoom (actions: overview, module, symbol, search)
  audit                    Architecture Boundary Auditor: Check circular dependencies & layer violations
                           Options: --file <path>
  blast <target>           Blast Radius Estimator: Simulate ripple effects and breaking changes
                           Options: --type <signature|body|removal|rename>, --hops <n>
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

func runSync(args []string) {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	force := fs.Bool("force", false, "Force full re-indexing of all files")
	_ = fs.Parse(args)

	cfg, db, engine := openWorkspace(false)
	_ = cfg
	_ = db

	fmt.Println("Scanning and indexing workspace...")
	stats := engine.Sync(*force)

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
	_, db, engine := openWorkspace(false)
	counts := db.CountNodesAndEdges()
	cached := len(engine.ScanFiles())

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
	action := "overview"
	target := ""
	if len(args) > 0 {
		action = args[0]
	}
	if len(args) > 1 {
		target = args[1]
	}

	_, db, _ := openWorkspace(true)
	z := zoomer.NewHierarchicalZoomer(db)

	switch action {
	case "overview":
		res := z.ZoomOverview(target)
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
	_ = fs.Parse(args)

	cfg, db, _ := openWorkspace(true)
	aud := auditor.NewArchitectureBoundaryAuditor(db, cfg)
	res := aud.CheckLayerViolation(*targetFile, nil)

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

	_, db, _ := openWorkspace(true)
	bl := blast.NewBlastRadiusEstimator(db)
	res := bl.EstimateBlastRadius(target, changeType, hops)

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
		_ = inst.InstallClaudeCode()
	}
	if doCodex {
		_ = inst.InstallCodex()
	}
	if doAgy {
		_ = inst.InstallAntigravity()
	}

	fmt.Println("\n[✓] Meta-Lattice installation complete!")
	inst.CheckStatus()
}
