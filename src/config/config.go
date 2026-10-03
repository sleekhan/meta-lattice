package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

var DefaultIgnorePatterns = []string{
	".git",
	".svn",
	".hg",
	"__pycache__",
	".pytest_cache",
	".mypy_cache",
	".venv",
	"venv",
	"node_modules",
	"dist",
	"build",
	"out",
	".lattice",
	"target",
	"bin",
	"obj",
	"ref",
	"*.min.js",
	"*.bundle.js",
	"*.map",
	"package-lock.json",
	"yarn.lock",
	"pnpm-lock.yaml",
}

type ArchRule struct {
	FromLayer   string   `json:"from_layer"`
	ForbiddenTo []string `json:"forbidden_to"`
	Reason      string   `json:"reason"`
}

type ArchRulesConfig struct {
	Preset         string     `json:"preset"`
	Layers         []string   `json:"layers"`
	Rules          []ArchRule `json:"rules"`
	AllowCycles    bool       `json:"allow_cycles"`
	MaxInstability float64    `json:"max_instability"`
}

func DefaultArchRules() ArchRulesConfig {
	return ArchRulesConfig{
		Preset: "layered",
		Layers: []string{"controller", "service", "repository", "model"},
		Rules: []ArchRule{
			{
				FromLayer:   "repository",
				ForbiddenTo: []string{"service", "controller"},
				Reason:      "Repository layer cannot depend on Service or Controller layer",
			},
			{
				FromLayer:   "service",
				ForbiddenTo: []string{"controller"},
				Reason:      "Service layer cannot depend on Controller layer",
			},
			{
				FromLayer:   "domain",
				ForbiddenTo: []string{"infrastructure", "presentation", "application"},
				Reason:      "Clean Architecture: Domain must be isolated from outer layers",
			},
		},
		AllowCycles:    false,
		MaxInstability: 0.8,
	}
}

type LatticeConfig struct {
	WorkspaceRoot  string
	CacheDir       string
	DBPath         string
	CacheStateFile string
	ArchConfigFile string
	IgnorePatterns []string
	ArchRules      ArchRulesConfig
}

func NewLatticeConfig(workspaceRoot string) *LatticeConfig {
	absRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		absRoot = workspaceRoot
	}
	cacheDir := filepath.Join(absRoot, ".lattice")
	cfg := &LatticeConfig{
		WorkspaceRoot:  absRoot,
		CacheDir:       cacheDir,
		DBPath:         filepath.Join(cacheDir, "knowledge.lattice"),
		CacheStateFile: filepath.Join(cacheDir, "cache_state.json"),
		ArchConfigFile: filepath.Join(absRoot, ".lattice-arch.json"),
		IgnorePatterns: append([]string(nil), DefaultIgnorePatterns...),
		ArchRules:      DefaultArchRules(),
	}
	cfg.loadArchConfig()
	return cfg
}

func (c *LatticeConfig) EnsureDirectories() error {
	return os.MkdirAll(c.CacheDir, 0755)
}

func (c *LatticeConfig) loadArchConfig() {
	if data, err := os.ReadFile(c.ArchConfigFile); err == nil {
		var custom ArchRulesConfig
		if err := json.Unmarshal(data, &custom); err == nil {
			if custom.Preset != "" {
				c.ArchRules.Preset = custom.Preset
			}
			if len(custom.Layers) > 0 {
				c.ArchRules.Layers = custom.Layers
			}
			if len(custom.Rules) > 0 {
				c.ArchRules.Rules = custom.Rules
			}
			c.ArchRules.AllowCycles = custom.AllowCycles
			if custom.MaxInstability > 0 {
				c.ArchRules.MaxInstability = custom.MaxInstability
			}
		}
	}
}

func GetConfig(workspaceRoot ...string) *LatticeConfig {
	if len(workspaceRoot) > 0 && workspaceRoot[0] != "" {
		return NewLatticeConfig(workspaceRoot[0])
	}
	if env := os.Getenv("CLAUDE_PROJECT_DIR"); env != "" {
		return NewLatticeConfig(env)
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	return NewLatticeConfig(cwd)
}
