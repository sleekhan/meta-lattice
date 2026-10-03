package indexer

import (
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"meta-lattice/src/models"
)

type ImportSpec struct {
	SourceFile   string   `json:"source_file"`
	ModuleSpec   string   `json:"module_spec"`
	Names        []string `json:"names"`
	IsRelative   bool     `json:"is_relative"`
	ResolvedPath string   `json:"resolved_path,omitempty"`
}

type CallSpec struct {
	CallerID   string `json:"caller_id"`
	CalledName string `json:"called_name"`
	LineNumber int    `json:"line_number"`
}

type ParsedFileResult struct {
	FileNode    models.L1ModuleNode
	ClassNodes  []models.L2ClassNode
	SymbolNodes []models.L3SymbolNode
	Imports     []ImportSpec
	Calls       []CallSpec
}

var layerKeywords = []struct {
	layer    string
	keywords map[string]bool
}{
	{
		layer: "controller",
		keywords: map[string]bool{
			"controller": true, "controllers": true, "api": true, "apis": true,
			"routes": true, "route": true, "router": true, "routers": true,
			"endpoint": true, "endpoints": true, "view": true, "views": true,
			"presentation": true,
		},
	},
	{
		layer: "service",
		keywords: map[string]bool{
			"service": true, "services": true, "usecase": true, "usecases": true,
			"interactor": true, "interactors": true, "handler": true, "handlers": true,
			"application": true,
		},
	},
	{
		layer: "repository",
		keywords: map[string]bool{
			"repo": true, "repos": true, "repository": true, "repositories": true,
			"dao": true, "daos": true, "store": true, "stores": true, "db": true,
		},
	},
	{
		layer: "model",
		keywords: map[string]bool{
			"model": true, "models": true, "entity": true, "entities": true,
			"domain": true, "schema": true, "schemas": true,
		},
	},
	{
		layer: "infrastructure",
		keywords: map[string]bool{
			"infra": true, "infrastructure": true, "client": true, "clients": true,
			"external": true,
		},
	},
}

var nonAlphanumericRegex = regexp.MustCompile(`[^a-z0-9]+`)

// DetectLayer determines architectural layer from whole path tokens
// (filename first, then nearest directory).
func DetectLayer(filePath string) string {
	cleanPath := filepath.ToSlash(filePath)
	ext := path.Ext(cleanPath)
	base := path.Base(cleanPath)
	stem := strings.TrimSuffix(base, ext)

	dir := path.Dir(cleanPath)
	var dirParts []string
	if dir != "." && dir != "/" && dir != "" {
		dirParts = strings.Split(dir, "/")
	}

	// Test parts: stem first, then reversed parent directories
	var partsToTest []string
	partsToTest = append(partsToTest, stem)
	for i := len(dirParts) - 1; i >= 0; i-- {
		partsToTest = append(partsToTest, dirParts[i])
	}

	for _, part := range partsToTest {
		tokens := nonAlphanumericRegex.Split(strings.ToLower(part), -1)
		tokenSet := make(map[string]bool, len(tokens))
		for _, tok := range tokens {
			if tok != "" {
				tokenSet[tok] = true
			}
		}

		for _, item := range layerKeywords {
			for tok := range tokenSet {
				if item.keywords[tok] {
					return item.layer
				}
			}
		}
	}

	return ""
}

// GetDomainFromPath determines domain/package cluster from file path.
func GetDomainFromPath(relPath string) string {
	clean := filepath.ToSlash(relPath)
	parts := strings.Split(clean, "/")
	if len(parts) > 1 {
		first := parts[0]
		if (first == "src" || first == "lib" || first == "app" || first == "packages" || first == "pkg") && len(parts) > 2 {
			return parts[1]
		}
		return first
	}
	return "root"
}
