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

// fillMethodSignatures populates each L2 class node's MethodSignatures from
// the signatures of L3 symbols parented to it, so zoom_module shows class
// interfaces without re-scanning source.
func fillMethodSignatures(classNodes *[]models.L2ClassNode, symbolNodes []models.L3SymbolNode) {
	byParent := make(map[string][]string)
	for _, sym := range symbolNodes {
		if sym.Signature != "" {
			byParent[sym.ParentID] = append(byParent[sym.ParentID], sym.Signature)
		}
	}
	for i, cls := range *classNodes {
		if sigs, ok := byParent[cls.ID]; ok {
			(*classNodes)[i].MethodSignatures = sigs
		}
	}
}

// BodyCall is a call site found while scanning a function body.
type BodyCall struct {
	Name string
	Line int
}

// scanBody walks forward from a single-line definition, collecting body
// text, call names and a brace-depth complexity estimate. It stops when the
// brace depth opened by the definition returns to zero (or after maxLines),
// so one-line `{}` bodies and bodyless `;` declarations don't leak the scan
// into following definitions. Lines that open a nested named definition are
// brace-counted but contribute no calls.
func scanBody(lines []string, start int, maxLines int, callRe *regexp.Regexp, exclude map[string]bool, selfName string, branchPrefixes []string, infix []string, defRe *regexp.Regexp) (body []string, bodyCalls []BodyCall, complexity int) {
	complexity = 1
	body = []string{lines[start]}
	depth := strings.Count(lines[start], "{") - strings.Count(lines[start], "}")
	trimmedDef := strings.TrimSpace(lines[start])
	if !strings.Contains(lines[start], "{") && strings.HasSuffix(trimmedDef, ";") {
		return body, nil, complexity
	}
	if depth <= 0 && strings.Contains(lines[start], "{") {
		// One-line body (e.g. `int get() { return helper(); }`): extract
		// calls from inside the braces instead of dropping them.
		inner := lines[start]
		if open := strings.Index(inner, "{"); open >= 0 {
			if close := strings.LastIndex(inner, "}"); close > open {
				inner = inner[open+1 : close]
			}
		}
		innerTrim := strings.TrimSpace(inner)
		for _, p := range branchPrefixes {
			if strings.Contains(innerTrim, p) {
				complexity++
				break
			}
		}
		for _, inf := range infix {
			if strings.Contains(innerTrim, inf) {
				complexity++
				break
			}
		}
		for _, cm := range callRe.FindAllStringSubmatch(inner, -1) {
			cName := cm[1]
			if cName != selfName && !exclude[cName] {
				bodyCalls = append(bodyCalls, BodyCall{Name: cName, Line: start + 1})
			}
		}
		return body, bodyCalls, complexity
	}

	for i := start + 1; i < len(lines) && i < start+maxLines; i++ {
		subLine := lines[i]
		body = append(body, subLine)
		subTrim := strings.TrimSpace(subLine)

		for _, p := range branchPrefixes {
			if strings.HasPrefix(subTrim, p) {
				complexity++
				break
			}
		}
		for _, inf := range infix {
			if strings.Contains(subTrim, inf) {
				complexity++
				break
			}
		}

		if defRe == nil || !defRe.MatchString(subLine) {
			for _, cm := range callRe.FindAllStringSubmatch(subLine, -1) {
				cName := cm[1]
				if cName != selfName && !exclude[cName] {
					bodyCalls = append(bodyCalls, BodyCall{Name: cName, Line: i + 1})
				}
			}
		}

		depth += strings.Count(subLine, "{") - strings.Count(subLine, "}")
		if depth <= 0 {
			break
		}
	}
	return body, bodyCalls, complexity
}

// extractBraceCalls extracts call sites from already-delimited body lines
// with true 1-based line numbers. Comment lines and nested named definitions
// are skipped so sibling symbols don't pollute the caller's edge list.
// When skipDefLine is true the definition line (index 0) is also skipped.
// First occurrence wins (ordered dedupe).
func extractBraceCalls(body []string, baseLine int, callerID, selfName string, callRe *regexp.Regexp, exclude map[string]bool, isNestedDef func(string) bool, skipDefLine bool) ([]CallSpec, []string) {
	var specs []CallSpec
	var names []string
	seen := make(map[string]bool)
	for i, l := range body {
		if i == 0 && skipDefLine {
			continue // definition line
		}
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		if isNestedDef != nil && isNestedDef(trimmed) {
			continue
		}
		for _, cm := range callRe.FindAllStringSubmatch(l, -1) {
			if len(cm) < 2 {
				continue
			}
			cName := cm[1]
			if cName == selfName || exclude[cName] || seen[cName] {
				continue
			}
			seen[cName] = true
			names = append(names, cName)
			specs = append(specs, CallSpec{CallerID: callerID, CalledName: cName, LineNumber: baseLine + i})
		}
	}
	return specs, names
}

// uniqueStrings deduplicates while preserving first-occurrence order.
func uniqueStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
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
