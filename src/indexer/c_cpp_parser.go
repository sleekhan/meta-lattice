package indexer

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"meta-lattice/src/models"
)

var (
	cIncludeRegex = regexp.MustCompile(`(?m)^\s*#include\s+["<]([^">]+)[">]`)
	cClassRegex   = regexp.MustCompile(`(?m)^\s*(?:template\s*<[^>]+>\s*)?(class|struct)\s+([a-zA-Z0-9_]+)(?:\s*:\s*[^{;]+)?`)
	cFnRegex      = regexp.MustCompile(`(?m)^\s*(?:(?:static|inline|virtual|explicit|constexpr|extern)\s+)*([a-zA-Z0-9_<>[\]:*&]+)\s+([a-zA-Z0-9_]+)\s*\(([^)]*)\)\s*(?:const)?\s*(?:override|noexcept|final)?\s*[{;]?`)
	cCallRegex    = regexp.MustCompile(`\b([a-zA-Z0-9_]+)\s*\(`)
)

func ParseCCPPFile(relPath string, code string, sha string, domain string) *ParsedFileResult {
	lines := strings.Split(code, "\n")
	loc := len(lines)
	layer := DetectLayer(relPath)
	ext := strings.ToLower(path.Ext(relPath))
	lang := "c"
	if ext == ".cpp" || ext == ".cc" || ext == ".cxx" || ext == ".hpp" {
		lang = "cpp"
	}

	var imports []ImportSpec
	for _, m := range cIncludeRegex.FindAllStringSubmatch(code, -1) {
		incPath := m[1]
		isRel := !strings.HasPrefix(incPath, "sys/") && !strings.Contains(incPath, "std")
		imports = append(imports, ImportSpec{
			SourceFile: relPath,
			ModuleSpec: incPath,
			Names:      []string{path.Base(incPath)},
			IsRelative: isRel,
		})
	}

	fileNode := models.L1ModuleNode{
		ID:        "file:" + relPath,
		Path:      relPath,
		Filename:  path.Base(relPath),
		Language:  lang,
		LOC:       loc,
		Domain:    domain,
		Layer:     layer,
		SHA:       sha,
		Level:     string(models.LevelL1),
		Labels:    []string{"L1Module", "Module", "File"},
		Docstring: fmt.Sprintf("%s file %s", strings.ToUpper(lang), relPath),
	}

	var classNodes []models.L2ClassNode
	var symbolNodes []models.L3SymbolNode
	var calls []CallSpec
	var exports []string

	// L2 Classes & Structs
	for _, m := range cClassRegex.FindAllStringSubmatch(code, -1) {
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

	// L3 Functions
	for lineNum, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if m := cFnRegex.FindStringSubmatch(line); len(m) > 3 {
			retType := strings.TrimSpace(m[1])
			fnName := strings.TrimSpace(m[2])
			params := strings.TrimSpace(m[3])

			// Skip keywords and control flow
			if fnName == "if" || fnName == "for" || fnName == "while" || fnName == "switch" ||
				fnName == "catch" || fnName == "return" || fnName == "sizeof" || fnName == "defined" {
				continue
			}

			sig := fmt.Sprintf("%s %s(%s)", retType, fnName, params)
			symID := fmt.Sprintf("symbol:%s:%s", relPath, fnName)
			exports = append(exports, fnName)

			// Complexity & calls
			complexity := 1
			bodyLines := []string{line}
			var fnCalls []string

			for i := lineNum + 1; i < len(lines) && i < lineNum+150; i++ {
				subLine := lines[i]
				bodyLines = append(bodyLines, subLine)
				subTrim := strings.TrimSpace(subLine)

				if strings.HasPrefix(subTrim, "if ") || strings.HasPrefix(subTrim, "for ") ||
					strings.HasPrefix(subTrim, "while ") || strings.HasPrefix(subTrim, "case ") ||
					strings.HasPrefix(subTrim, "catch ") || strings.Contains(subTrim, " && ") ||
					strings.Contains(subTrim, " || ") {
					complexity++
				}

				for _, cm := range cCallRegex.FindAllStringSubmatch(subLine, -1) {
					cName := cm[1]
					if cName != "if" && cName != "for" && cName != "while" && cName != "switch" && cName != fnName {
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
				IsPublic:             true,
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
