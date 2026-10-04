package indexer

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"meta-lattice/src/models"
)

var (
	swiftImportRegex = regexp.MustCompile(`(?m)^\s*(?:@testable\s+)?import\s+([a-zA-Z0-9_]+)`)
	swiftTypeRegex   = regexp.MustCompile(`(?m)^\s*(?:(?:public|private|fileprivate|internal|open|final)\s+)*(class|struct|enum|protocol|extension)\s+([a-zA-Z0-9_]+)`)
	swiftFuncRegex   = regexp.MustCompile(`^\s*(?:(?:public|private|fileprivate|internal|open|override|static|class|final|mutating|required|convenience)\s+)*func\s+([a-zA-Z0-9_]+)\s*\(([^)]*)\)\s*(?:->\s*([^{\n]+))?`)
	swiftCallRegex   = regexp.MustCompile(`\b([a-zA-Z0-9_]+)\s*\(`)
)

var swiftKeywordCalls = map[string]bool{
	"if": true, "for": true, "while": true, "guard": true, "switch": true,
	"return": true, "func": true, "init": true, "self": true,
}

func ParseSwiftFile(relPath string, code string, sha string, domain string) *ParsedFileResult {
	lines := strings.Split(code, "\n")
	loc := len(lines)
	layer := DetectLayer(relPath)

	var imports []ImportSpec
	for _, m := range swiftImportRegex.FindAllStringSubmatch(code, -1) {
		mod := strings.TrimSpace(m[1])
		imports = append(imports, ImportSpec{
			SourceFile: relPath,
			ModuleSpec: mod,
			Names:      []string{mod},
			IsRelative: false,
		})
	}

	fileNode := models.L1ModuleNode{
		ID:        "file:" + relPath,
		Path:      relPath,
		Filename:  path.Base(relPath),
		Language:  "swift",
		LOC:       loc,
		Domain:    domain,
		Layer:     layer,
		SHA:       sha,
		Level:     string(models.LevelL1),
		Labels:    []string{"L1Module", "Module", "File"},
		Docstring: fmt.Sprintf("Swift module %s in %s", path.Base(relPath), relPath),
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

	for _, idx := range swiftTypeRegex.FindAllStringSubmatchIndex(code, -1) {
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
			Labels:    []string{"L2Class", "Class", "Swift"},
		})
	}

	lineOffsets := make([]int, len(lines)+1)
	for i, line := range lines {
		if i == 0 {
			lineOffsets[0] = 0
		}
		lineOffsets[i+1] = lineOffsets[i] + len(line) + 1
	}

	for lineNum, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
			continue
		}

		m := swiftFuncRegex.FindStringSubmatch(line)
		if len(m) < 3 {
			continue
		}
		fnName := strings.TrimSpace(m[1])
		params := strings.TrimSpace(m[2])
		retType := ""
		if len(m) > 3 {
			retType = strings.TrimSpace(m[3])
		}
		if swiftKeywordCalls[fnName] {
			continue
		}

		isPublic := !strings.Contains(line, "private") && !strings.Contains(line, "fileprivate")
		if isPublic {
			exports = append(exports, fnName)
		}

		sig := fmt.Sprintf("func %s(%s)", fnName, params)
		if retType != "" {
			sig += " -> " + retType
		}
		symID := fmt.Sprintf("symbol:%s:%s", relPath, fnName)

		parentID := fileNode.ID
		off := lineOffsets[lineNum]
		for _, tp := range typePositions {
			if tp.offset < off {
				parentID = tp.id
			} else {
				break
			}
		}

		bodyLines, foundCalls, complexity := scanBody(lines, lineNum, 150, swiftCallRegex,
			swiftKeywordCalls, fnName,
			[]string{"if ", "guard ", "for ", "while ", "switch ", "catch "},
			[]string{" && ", " || ", " ?? "}, swiftFuncRegex)
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
			IsPublic:             isPublic,
			Level:                string(models.LevelL3),
			Labels:               []string{"L3Symbol", "Symbol", "Function"},
		})
	}

	fillMethodSignatures(&classNodes, symbolNodes)
	fileNode.Exports = exports

	return &ParsedFileResult{
		FileNode:    fileNode,
		ClassNodes:  classNodes,
		SymbolNodes: symbolNodes,
		Imports:     imports,
		Calls:       calls,
	}
}
