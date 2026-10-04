package installer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var SkillTemplates = map[string]string{
	"hierarchical-zoom": `---
name: hierarchical-zoom
description: Hierarchical context zoomer (L0->L3) saving 80%+ tokens using Meta-Lattice
---

# Hierarchical Context Zoomer

Use Meta-Lattice MCP tools to drill down into the codebase:
1. 'zoom_overview': View L0 domain boundaries and L1 module list
2. 'zoom_module': Inspect L2 class/interface signatures without method bodies
3. 'zoom_symbol': View L3 full implementation code of specific functions
4. 'zoom_search': BM25 full-text search across all symbols
`,
	"architecture-auditor": `---
name: architecture-auditor
description: Audits layered architecture boundaries and circular dependencies using Meta-Lattice
---

# Architecture Boundary Auditor

Use 'check_layer_violation' before finishing multi-file refactoring:
- Detects circular dependencies (e.g. A -> B -> C -> A)
- Flags forbidden layer imports (e.g. repository importing controller)
- Reports Afferent (Ca), Efferent (Ce), and Instability (I) metrics
`,
	"blast-radius": `---
name: blast-radius
description: Simulates blast radius and ripple effects before changing symbols using Meta-Lattice
---

# Blast Radius Estimator

Use 'estimate_blast_radius' when modifying core interfaces or symbols:
- Identifies Top 10 breaking points
- Computes Blast Score (0-100) and risk tier (CRITICAL, HIGH, MODERATE, LOW)
- Visualizes impact cascade tree
`,
	"incremental-cache": `---
name: incremental-cache
description: Synchronizes Meta-Lattice AST cache incrementally in milliseconds
---

# Incremental AST Cache

Use 'sync_index' to refresh AST and graph edges after modifying source files:
- Ultra-fast incremental sync using SHA-256 and mtime
- Rebuilds cross-file IMPORTS and CALLS edges
`,
	"codegen": `---
name: codegen
description: Scaffolds new modules and applies multi-file edit plans with dry-run and rollback using Meta-Lattice
---

# Code Generation

Use 'scaffold_module' to create new source files from language templates,
and 'apply_plan' for batched edits:
- scaffold_module: kind class|interface|struct|enum|module, workspace-confined, no overwrite by default
- apply_plan: create_file, replace_text, insert_after, delete_file; dry_run first, rollback on failure
- After writing: 'sync_index', then 'check_layer_violation' on touched files
`,
}

var CommandTemplates = map[string]string{
	"zoom": `---
description: Hierarchically inspect and drill down code from L0 architecture to L3 symbol without wasting tokens.
usage: /zoom [overview | module <path> | symbol <name> | search <query>]
---

# Hierarchical Context Zoomer

Use Meta-Lattice MCP tools to navigate the codebase:
1. zoom_overview: View L0 domain boundaries and L1 module list
2. zoom_module: Inspect L2 class/interface signatures without method bodies
3. zoom_symbol: Inspect L3 full implementation code of specific functions
4. zoom_search: BM25 search across all symbols
`,
	"audit": `---
description: Audit architecture boundaries, detect circular dependencies, and compute coupling metrics.
usage: /audit [--file <path>]
---

# Architecture Boundary Auditor

Use Meta-Lattice MCP tool 'check_layer_violation':
- Enforces unidirectional layered architecture (controller -> service -> repository -> model)
- Detects circular dependencies (A -> B -> C -> A) using Tarjan's SCC algorithm
- Reports Afferent (Ca), Efferent (Ce), and Instability (I) metrics
`,
	"blast": `---
description: Simulate side-effects and estimate blast radius before modifying core functions or interfaces.
usage: /blast <symbol_or_path> [--type <signature|body|removal|rename>]
---

# Blast Radius Estimator

Use Meta-Lattice MCP tool 'estimate_blast_radius':
- Simulates ripple effects across reverse CALLS, IMPORTS, and REFERENCES edges
- Calculates Blast Score (0-100) and risk tier (CRITICAL, HIGH, MODERATE, LOW)
- Highlights Top 10 breaking points and impact cascade tree
`,
	"sync": `---
description: Incrementally synchronize AST index with workspace changes using file SHA-256 and mtime caching.
usage: /sync [--force]
---

# Incremental AST Cache Sync

Use Meta-Lattice MCP tool 'sync_index':
- Ultra-fast incremental sync using file SHA-256 and mtime
- Rebuilds cross-file IMPORTS and CALLS edges
`,
	"scaffold": `---
description: Generate a new source file from a per-language template (class, interface, struct, enum, module).
usage: /scaffold <file_path> [--kind <class|interface|struct|enum|module>] [--name <Type>]
---

# Code Generation - Scaffold Module

Use Meta-Lattice MCP tool 'scaffold_module':
- Creates Kotlin, C#, Swift, PHP, Go, Python, TS/JS, Java, Rust, C/C++ skeletons
- Refuses to overwrite unless 'overwrite' is true; paths stay inside the workspace
- Follow with 'sync_index', then 'zoom_module' to verify the new file
`,
	"apply": `---
description: Apply a batch of file edits with dry-run validation and automatic rollback.
usage: /apply <plan.json> [--execute]
---

# Code Generation - Apply Edit Plan

Use Meta-Lattice MCP tool 'apply_plan':
- Ops: create_file, replace_text, insert_after, delete_file
- ALWAYS dry_run first for multi-file edits; re-run with dry_run=false to write
- On failure all applied ops roll back; then run 'sync_index' and 'check_layer_violation'
`,
}

type Installer struct {
	BinaryPath string
	HomeDir    string
}

func NewInstaller(binaryPath string) *Installer {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	if binaryPath == "" {
		if exe, err := os.Executable(); err == nil {
			binaryPath = exe
		} else {
			if runtime.GOOS == "windows" {
				binaryPath = "meta-lattice.exe"
			} else {
				binaryPath = "meta-lattice"
			}
		}
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(binaryPath), ".exe") {
		if _, err := os.Stat(binaryPath + ".exe"); err == nil {
			binaryPath = binaryPath + ".exe"
		}
	}
	absBin, err := filepath.Abs(binaryPath)
	if err == nil {
		binaryPath = absBin
	}
	return &Installer{
		BinaryPath: filepath.ToSlash(binaryPath),
		HomeDir:    home,
	}
}

func (inst *Installer) InstallClaudeCode() error {
	// 1. Configure ~/.claude.json
	claudeConfigPath := filepath.Join(inst.HomeDir, ".claude.json")
	var root map[string]any
	if data, err := os.ReadFile(claudeConfigPath); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &root)
	}
	if root == nil {
		root = make(map[string]any)
	}

	mcpServers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		mcpServers = make(map[string]any)
		root["mcpServers"] = mcpServers
	}

	mcpServers["meta-lattice"] = map[string]any{
		"command": inst.BinaryPath,
		"args":    []string{"mcp"},
	}

	marshaled, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(claudeConfigPath, marshaled, 0644); err != nil {
		return err
	}
	fmt.Printf("[✓] Claude Code MCP config updated: %s\n", claudeConfigPath)

	// 2. Deploy custom slash commands to ~/.claude/commands
	claudeCommandsDir := filepath.Join(inst.HomeDir, ".claude", "commands")
	if err := os.MkdirAll(claudeCommandsDir, 0755); err != nil {
		return err
	}
	for cmdName, template := range CommandTemplates {
		cmdFile := filepath.Join(claudeCommandsDir, cmdName+".md")
		_ = os.WriteFile(cmdFile, []byte(template), 0644)
		fmt.Printf("[✓] Claude Code slash command deployed: /%s\n", cmdName)
	}

	return nil
}

func (inst *Installer) UninstallClaudeCode() error {
	claudeConfigPath := filepath.Join(inst.HomeDir, ".claude.json")
	if data, err := os.ReadFile(claudeConfigPath); err == nil {
		var root map[string]any
		if err := json.Unmarshal(data, &root); err == nil {
			if mcpServers, ok := root["mcpServers"].(map[string]any); ok {
				delete(mcpServers, "meta-lattice")
				marshaled, _ := json.MarshalIndent(root, "", "  ")
				_ = os.WriteFile(claudeConfigPath, marshaled, 0644)
				fmt.Printf("[✓] Removed meta-lattice from: %s\n", claudeConfigPath)
			}
		}
	}

	claudeCommandsDir := filepath.Join(inst.HomeDir, ".claude", "commands")
	for cmdName := range CommandTemplates {
		cmdFile := filepath.Join(claudeCommandsDir, cmdName+".md")
		_ = os.Remove(cmdFile)
	}
	fmt.Println("[✓] Removed Claude Code meta-lattice slash commands.")
	return nil
}

func (inst *Installer) InstallCodex() error {
	codexDir := filepath.Join(inst.HomeDir, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		return err
	}
	configPath := filepath.Join(codexDir, "config.toml")

	block := fmt.Sprintf("\n# Meta-Lattice MCP Server configuration\n[mcp_servers.meta-lattice]\nenabled = true\ncommand = %q\nargs = [\"mcp\"]\n", inst.BinaryPath)

	var content string
	if data, err := os.ReadFile(configPath); err == nil {
		content = string(data)
		// Backup
		_ = os.WriteFile(configPath+".bak", data, 0644)

		content = removeCodexBlock(content)
		if strings.TrimSpace(content) == "" {
			content = strings.TrimSpace(block) + "\n"
		} else {
			content = strings.TrimRight(content, "\n") + "\n" + block
		}
	} else {
		content = strings.TrimSpace(block) + "\n"
	}

	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		return err
	}
	fmt.Printf("[✓] OpenAI Codex configuration updated: %s\n", configPath)
	return nil
}

func (inst *Installer) UninstallCodex() error {
	configPath := filepath.Join(inst.HomeDir, ".codex", "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}
	original := string(data)
	updated := removeCodexBlock(original)
	if updated != original {
		newContent := strings.TrimSpace(updated)
		if newContent != "" {
			newContent += "\n"
		}
		if err := os.WriteFile(configPath, []byte(newContent), 0644); err != nil {
			return err
		}
		fmt.Printf("[✓] OpenAI Codex configuration removed: %s\n", configPath)
	}
	return nil
}

// removeCodexBlock strips the [mcp_servers.meta-lattice] table (and its leading
// "# Meta-Lattice ..." comment) from a TOML document. It is line based because
// the table body itself contains '[' characters (args = ["mcp"]), which broke
// the previous regex-based approach.
func removeCodexBlock(content string) string {
	const header = "[mcp_servers.meta-lattice]"
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))

	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != header {
			out = append(out, lines[i])
			continue
		}
		// Drop the preceding "# Meta-Lattice ..." comment, if any.
		if n := len(out); n > 0 && strings.HasPrefix(strings.TrimSpace(out[n-1]), "#") &&
			strings.Contains(strings.ToLower(out[n-1]), "meta-lattice") {
			out = out[:n-1]
		}
		// Skip the table body up to (not including) the next table header.
		for i+1 < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i+1]), "[") {
			i++
		}
	}
	return strings.Join(out, "\n")
}

func (inst *Installer) InstallAntigravity() error {
	geminiConfigDir := filepath.Join(inst.HomeDir, ".gemini", "config")
	geminiSkillsDir := filepath.Join(geminiConfigDir, "skills")
	if err := os.MkdirAll(geminiSkillsDir, 0755); err != nil {
		return err
	}

	// 1. mcp_config.json
	mcpConfigPath := filepath.Join(geminiConfigDir, "mcp_config.json")
	var root map[string]any
	if data, err := os.ReadFile(mcpConfigPath); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &root)
	}
	if root == nil {
		root = make(map[string]any)
	}

	mcpServers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		mcpServers = make(map[string]any)
		root["mcpServers"] = mcpServers
	}

	mcpServers["meta-lattice"] = map[string]any{
		"command": inst.BinaryPath,
		"args":    []string{"mcp"},
	}

	marshaled, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(mcpConfigPath, marshaled, 0644); err != nil {
		return err
	}
	fmt.Printf("[✓] Google Antigravity MCP config updated: %s\n", mcpConfigPath)

	// 2. Deploy skills
	for skillName, template := range SkillTemplates {
		targetDir := filepath.Join(geminiSkillsDir, skillName)
		_ = os.MkdirAll(targetDir, 0755)
		targetFile := filepath.Join(targetDir, "SKILL.md")
		_ = os.WriteFile(targetFile, []byte(template), 0644)
		fmt.Printf("[✓] Antigravity skill deployed: %s\n", skillName)
	}

	return nil
}

func (inst *Installer) UninstallAntigravity() error {
	geminiConfigDir := filepath.Join(inst.HomeDir, ".gemini", "config")
	mcpConfigPath := filepath.Join(geminiConfigDir, "mcp_config.json")

	if data, err := os.ReadFile(mcpConfigPath); err == nil {
		var root map[string]any
		if err := json.Unmarshal(data, &root); err == nil {
			if mcpServers, ok := root["mcpServers"].(map[string]any); ok {
				delete(mcpServers, "meta-lattice")
				marshaled, _ := json.MarshalIndent(root, "", "  ")
				_ = os.WriteFile(mcpConfigPath, marshaled, 0644)
				fmt.Printf("[✓] Removed meta-lattice from: %s\n", mcpConfigPath)
			}
		}
	}

	geminiSkillsDir := filepath.Join(geminiConfigDir, "skills")
	for skillName := range SkillTemplates {
		targetDir := filepath.Join(geminiSkillsDir, skillName)
		_ = os.RemoveAll(targetDir)
	}
	fmt.Println("[✓] Removed Antigravity meta-lattice skills.")
	return nil
}

func (inst *Installer) CheckStatus() {
	fmt.Println("\n=== Meta-Lattice Platform Integration Status ===")

	// Claude Code status
	claudeConfig := filepath.Join(inst.HomeDir, ".claude.json")
	claudeStatus := "Not Configured"
	if data, err := os.ReadFile(claudeConfig); err == nil {
		if strings.Contains(string(data), `"meta-lattice"`) {
			claudeStatus = "Configured [Active]"
		}
	}
	claudeCommandsDir := filepath.Join(inst.HomeDir, ".claude", "commands")
	cmdCount := 0
	for cmdName := range CommandTemplates {
		cmdFile := filepath.Join(claudeCommandsDir, cmdName+".md")
		if _, err := os.Stat(cmdFile); err == nil {
			cmdCount++
		}
	}
	fmt.Printf("• Claude Code MCP (~/.claude.json): %s\n", claudeStatus)
	fmt.Printf("• Claude Code Commands (~/.claude/commands): %d/%d Installed\n", cmdCount, len(CommandTemplates))

	// Codex status
	codexConfig := filepath.Join(inst.HomeDir, ".codex", "config.toml")
	codexStatus := "Not Configured"
	if data, err := os.ReadFile(codexConfig); err == nil {
		if strings.Contains(string(data), "[mcp_servers.meta-lattice]") {
			codexStatus = "Configured [Active]"
		}
	}
	fmt.Printf("• OpenAI Codex (~/.codex/config.toml): %s\n", codexStatus)

	// Antigravity MCP status
	geminiMCP := filepath.Join(inst.HomeDir, ".gemini", "config", "mcp_config.json")
	agyMCPStatus := "Not Configured"
	if data, err := os.ReadFile(geminiMCP); err == nil {
		if strings.Contains(string(data), `"meta-lattice"`) {
			agyMCPStatus = "Configured [Active]"
		}
	}
	fmt.Printf("• Google Antigravity MCP (~/.gemini/config/mcp_config.json): %s\n", agyMCPStatus)

	// Antigravity Skills status
	geminiSkills := filepath.Join(inst.HomeDir, ".gemini", "config", "skills")
	skillsCount := 0
	for skillName := range SkillTemplates {
		skillFile := filepath.Join(geminiSkills, skillName, "SKILL.md")
		if _, err := os.Stat(skillFile); err == nil {
			skillsCount++
		}
	}
	fmt.Printf("• Google Antigravity Skills: %d/%d Installed\n", skillsCount, len(SkillTemplates))
	fmt.Printf("• Executable Path: %s\n\n", inst.BinaryPath)
}
