package indexer

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"meta-lattice/src/models"
)

var (
	rustUseRegex    = regexp.MustCompile(`(?m)^\s*pub(?:\([^)]+\))?\s+use\s+([^;]+);|^\s*use\s+([^;]+);`)
	rustTypeRegex   = regexp.MustCompile(`(?m)^\s*(?:pub(?:\([^)]+\))?\s+)?(struct|enum|trait|union)\s+([a-zA-Z0-9_]+)`)
	rustImplRegex   = regexp.MustCompile(`(?m)^\s*impl(?:<[^>]+>)?\s+(?:([a-zA-Z0-9_:]+)\s+for\s+)?([a-zA-Z0-9_]+)`)
	rustFnRegex     = regexp.MustCompile(`(?m)^\s*(pub(?:\([^)]+\))?\s+)?(?:async\s+)?(?:const\s+)?(?:unsafe\s+)?fn\s+([a-zA-Z0-9_]+)(?:<[^>]+>)?\s*\(([^)]*)\)(?:\s*->\s*([^{;]+))?`)
	rustCallRegex   = regexp.MustCompile(`\b([a-zA-Z0-9_]+)\s*\(`)
)

func ParseRustFile(relPath string, code string, sha string, domain string) *ParsedFileResult {
	lines := strings.Split(code, "\n")
	loc := len(lines)
	layer := DetectLayer(relPath)

	var imports []ImportSpec
	for _, m := range rustUseRegex.FindAllStringSubmatch(code, -1) {
		rawUse := m[1]
		if rawUse == "" {
			rawUse = m[2]
		}
		rawUse = strings.TrimSpace(rawUse)

		isRel := strings.HasPrefix(rawUse, "crate::") || strings.HasPrefix(rawUse, "super::") || strings.HasPrefix(rawUse, "self::")
		cleanSpec := strings.ReplaceAll(rawUse, "::", "/")
		parts := strings.Split(rawUse, "::")
		lastName := parts[len(parts)-1]

		imports = append(imports, ImportSpec{
			SourceFile: relPath,
			ModuleSpec: cleanSpec,
			Names:      []string{lastName},
			IsRelative: isRel,
		})
	}

	fileNode := models.L1ModuleNode{
		ID:        "file:" + relPath,
		Path:      relPath,
		Filename:  path.Base(relPath),
		Language:  "rust",
		LOC:       loc,
		Domain:    domain,
		Layer:     layer,
		SHA:       sha,
		Level:     string(models.LevelL1),
		Labels:    []string{"L1Module", "Module", "File"},
		Docstring: fmt.Sprintf("Rust module %s in %s", path.Base(relPath), relPath),
	}

	var classNodes []models.L2ClassNode
	var symbolNodes []models.L3SymbolNode
	var calls []CallSpec
	var exports []string

	// L2 Structs, Enums, Traits
	for _, m := range rustTypeRegex.FindAllStringSubmatch(code, -1) {
		kind := m[1]
		name := m[2]
		exports = append(exports, name)

		clsID := fmt.Sprintf("class:%s:%s", relPath, name)
		classNodes = append(classNodes, models.L2ClassNode{
			ID:        clsID,
			Name:      name,
			FilePath:  relPath,
			Kind:      kind,
			IsPublic:  true,
			Docstring: fmt.Sprintf("%s %s in %s", kind, name, relPath),
			Level:     string(models.LevelL2),
			Labels:    []string{"L2Class", "Class", strings.Title(kind)},
		})
	}

	// L2 Impls
	for _, m := range rustImplRegex.FindAllStringSubmatch(code, -1) {
		targetType := m[2]
		clsID := fmt.Sprintf("class:%s:%s", relPath, targetType)
		exists := false
		for _, c := range classNodes {
			if c.ID == clsID {
				exists = true
				break
			}
		}
		if !exists {
			classNodes = append(classNodes, models.L2ClassNode{
				ID:        clsID,
				Name:      targetType,
				FilePath:  relPath,
				Kind:      "impl",
				IsPublic:  true,
				Docstring: fmt.Sprintf("impl %s in %s", targetType, relPath),
				Level:     string(models.LevelL2),
				Labels:    []string{"L2Class", "Class", "Impl"},
			})
		}
	}

	// L3 Functions
	for lineNum, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
			continue
		}

		if m := rustFnRegex.FindStringSubmatch(line); len(m) > 2 {
			isPub := strings.TrimSpace(m[1]) != ""
			fnName := strings.TrimSpace(m[2])
			params := strings.TrimSpace(m[3])
			retType := ""
			if len(m) > 4 {
				retType = strings.TrimSpace(m[4])
			}

			if isPub {
				exports = append(exports, fnName)
			}

			sig := fmt.Sprintf("fn %s(%s)", fnName, params)
			if retType != "" {
				sig += " -> " + retType
			}

			symID := fmt.Sprintf("symbol:%s:%s", relPath, fnName)

			// Complexity & calls
			complexity := 1
			bodyLines := []string{line}
			var fnCalls []string

			for i := lineNum + 1; i < len(lines) && i < lineNum+150; i++ {
				subLine := lines[i]
				bodyLines = append(bodyLines, subLine)
				subTrim := strings.TrimSpace(subLine)

				if strings.HasPrefix(subTrim, "if ") || strings.HasPrefix(subTrim, "match ") ||
					strings.HasPrefix(subTrim, "for ") || strings.HasPrefix(subTrim, "while ") ||
					strings.Contains(subTrim, " && ") || strings.Contains(subTrim, " || ") ||
					strings.HasSuffix(subTrim, "?") {
					complexity++
				}

				for _, cm := range rustCallRegex.FindAllStringSubmatch(subLine, -1) {
					cName := cm[1]
					if cName != "if" && cName != "match" && cName != "for" && cName != "while" && cName != fnName {
						fnCalls = append(fnCalls, cName)
						calls = append(calls, CallSpec{
							CallerID:   symID,
							CalledName: cName,
							LineNumber: i + 1,
						})
					}
				}

				if strings.Contains(subLine, "}") && !strings.Contains(subLine, "{") {
					break
				}
			}

			symbolNodes = append(symbolNodes, models.L3SymbolNode{
				ID:                   symID,
				Name:                 fnName,
				FilePath:             relPath,
				Kind:                 "function",
				LineStart:            lineNum + 1,
				LineEnd:              lineNum + len(bodyLines),
				Signature:            sig,
				Code:                 strings.Join(bodyLines, "\n"),
				CyclomaticComplexity: complexity,
				Calls:                fnCalls,
				ParentID:             fileNode.ID,
				IsPublic:             isPub,
				Level:                string(models.LevelL3),
				Labels:               []string{"L3Symbol", "Symbol", "Function"},
			})
		}
	}

	fileNode.Exports = exports

	return &ParsedFileResult{
		FileNode:    fileNode,
		ClassNodes:  classNodes,
		SymbolNodes: symbolNodes,
		Imports:     imports,
		Calls:       calls,
	}
}
