package indexer

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"meta-lattice/src/models"
)

var (
	csharpUsingRegex     = regexp.MustCompile(`(?m)^\s*using\s+(?:static\s+)?([a-zA-Z0-9_.]+)\s*;`)
	csharpNamespaceRegex = regexp.MustCompile(`(?m)^\s*namespace\s+([a-zA-Z0-9_.]+)`)
	csharpTypeRegex      = regexp.MustCompile(`(?m)^\s*(?:(?:public|private|protected|internal|static|sealed|abstract|partial)\s+)*(class|interface|enum|struct|record)\s+([a-zA-Z0-9_]+)`)
	csharpMethodRegex    = regexp.MustCompile(`^\s*((?:(?:public|private|protected|internal|static|virtual|override|async|sealed|abstract|extern|new|partial|readonly)\s+)+)([a-zA-Z0-9_<>[\]?,.\s]+?)\s+([a-zA-Z0-9_]+)\s*\(([^)]*)\)`)
	csharpCallRegex      = regexp.MustCompile(`\b([a-zA-Z0-9_]+)\s*\(`)
)

var csharpKeywordCalls = map[string]bool{
	"if": true, "for": true, "foreach": true, "while": true, "switch": true,
	"catch": true, "using": true, "lock": true, "return": true, "new": true,
}

func ParseCSharpFile(relPath string, code string, sha string, domain string) *ParsedFileResult {
	lines := strings.Split(code, "\n")
	loc := len(lines)
	layer := DetectLayer(relPath)

	ns := ""
	if m := csharpNamespaceRegex.FindStringSubmatch(code); len(m) > 1 {
		ns = m[1]
	}

	var imports []ImportSpec
	for _, m := range csharpUsingRegex.FindAllStringSubmatch(code, -1) {
		fullSpec := strings.TrimSpace(m[1])
		parts := strings.Split(fullSpec, ".")
		imports = append(imports, ImportSpec{
			SourceFile: relPath,
			ModuleSpec: fullSpec,
			Names:      []string{parts[len(parts)-1]},
			IsRelative: false,
		})
	}

	fileNode := models.L1ModuleNode{
		ID:        "file:" + relPath,
		Path:      relPath,
		Filename:  path.Base(relPath),
		Language:  "csharp",
		LOC:       loc,
		Domain:    domain,
		Layer:     layer,
		SHA:       sha,
		Level:     string(models.LevelL1),
		Labels:    []string{"L1Module", "Module", "File"},
		Docstring: fmt.Sprintf("C# namespace %s in %s", ns, relPath),
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

	for _, idx := range csharpTypeRegex.FindAllStringSubmatchIndex(code, -1) {
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
			Labels:    []string{"L2Class", "Class", "CSharp"},
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

		m := csharpMethodRegex.FindStringSubmatch(line)
		if len(m) < 5 {
			continue
		}
		retType := strings.TrimSpace(m[2])
		methodName := strings.TrimSpace(m[3])
		params := strings.TrimSpace(m[4])
		if csharpKeywordCalls[methodName] {
			continue
		}

		isPublic := strings.Contains(m[1], "public")
		if isPublic {
			exports = append(exports, methodName)
		}

		sig := fmt.Sprintf("%s %s(%s)", retType, methodName, params)
		symID := fmt.Sprintf("symbol:%s:%s", relPath, methodName)

		parentID := fileNode.ID
		off := lineOffsets[lineNum]
		for _, tp := range typePositions {
			if tp.offset < off {
				parentID = tp.id
			} else {
				break
			}
		}

		bodyLines, foundCalls, complexity := scanBody(lines, lineNum, 150, csharpCallRegex,
			csharpKeywordCalls, methodName,
			[]string{"if ", "for ", "foreach ", "while ", "switch ", "catch "},
			[]string{" && ", " || ", " ?? "}, csharpMethodRegex)
		var methodCalls []string
		for _, bc := range foundCalls {
			methodCalls = append(methodCalls, bc.Name)
			calls = append(calls, CallSpec{
				CallerID:   symID,
				CalledName: bc.Name,
				LineNumber: bc.Line,
			})
		}

		symbolNodes = append(symbolNodes, models.L3SymbolNode{
			ID:                   symID,
			Name:                 methodName,
			FilePath:             relPath,
			Kind:                 "method",
			LineStart:            lineNum + 1,
			LineEnd:              lineNum + len(bodyLines),
			Signature:            sig,
			Code:                 strings.Join(bodyLines, "\n"),
			CyclomaticComplexity: complexity,
			Calls:                methodCalls,
			ParentID:             parentID,
			IsPublic:             isPublic,
			Level:                string(models.LevelL3),
			Labels:               []string{"L3Symbol", "Symbol", "Method"},
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
