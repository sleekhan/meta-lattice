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
	cKeywordCalls = map[string]bool{
		"if": true, "for": true, "while": true, "switch": true, "catch": true,
		"return": true, "sizeof": true, "defined": true, "new": true, "delete": true,
	}
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

	type typePos struct {
		id     string
		offset int
	}
	var typePositions []typePos

	// L2 Classes & Structs
	for _, idx := range cClassRegex.FindAllStringSubmatchIndex(code, -1) {
		kind := code[idx[2]:idx[3]]
		name := code[idx[4]:idx[5]]
		exports = append(exports, name)

		clsID := fmt.Sprintf("class:%s:%s", relPath, name)
		typePositions = append(typePositions, typePos{id: clsID, offset: idx[0]})
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

	// L3 Functions (parented to the nearest preceding class/struct)
	lineOffsets := make([]int, len(lines)+1)
	for i, line := range lines {
		if i == 0 {
			lineOffsets[0] = 0
		}
		lineOffsets[i+1] = lineOffsets[i] + len(line) + 1
	}
	for lineNum, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if m := cFnRegex.FindStringSubmatch(line); len(m) > 3 {
			retType := strings.TrimSpace(m[1])
			fnName := strings.TrimSpace(m[2])
			params := strings.TrimSpace(m[3])

			// Skip keywords, control flow, and statement lines misread as
			// declarations (e.g. `return persist();` where retType=return).
			if cKeywordCalls[fnName] || cKeywordCalls[retType] {
				continue
			}

			sig := fmt.Sprintf("%s %s(%s)", retType, fnName, params)
			symID := fmt.Sprintf("symbol:%s:%s", relPath, fnName)
			exports = append(exports, fnName)

			parentID := fileNode.ID
			off := lineOffsets[lineNum]
			for _, tp := range typePositions {
				if tp.offset < off {
					parentID = tp.id
				} else {
					break
				}
			}

			// Complexity & calls with brace-depth termination so declarations
			// and one-line bodies can't leak into following functions.
			// NOTE: defRe is nil here on purpose — cFnRegex also matches
			// statement lines like `return persist();`, which must contribute
			// calls instead of being skipped as nested definitions. (The
			// definition loop above already rejects keyword return types.)
			bodyLines, foundCalls, complexity := scanBody(lines, lineNum, 150, cCallRegex,
				cKeywordCalls, fnName,
				[]string{"if ", "for ", "while ", "case ", "catch "},
				[]string{" && ", " || "}, nil)
			var fnCalls []string
			for _, bc := range foundCalls {
				fnCalls = append(fnCalls, bc.Name)
				calls = append(calls, CallSpec{
					CallerID:   symID,
					CalledName: bc.Name,
					LineNumber: bc.Line,
				})
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
				ParentID:             parentID,
				IsPublic:             true,
				Level:                string(models.LevelL3),
				Labels:               []string{"L3Symbol", "Symbol", "Function"},
			})
		}
	}

	fileNode.Exports = exports
	fillMethodSignatures(&classNodes, symbolNodes)

	return &ParsedFileResult{
		FileNode:    fileNode,
		ClassNodes:  classNodes,
		SymbolNodes: symbolNodes,
		Imports:     imports,
		Calls:       calls,
	}
}
