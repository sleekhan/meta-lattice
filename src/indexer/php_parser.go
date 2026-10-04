package indexer

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"meta-lattice/src/models"
)

var (
	phpNamespaceRegex = regexp.MustCompile(`(?m)^\s*namespace\s+([a-zA-Z0-9_\\]+)\s*;`)
	phpUseRegex       = regexp.MustCompile(`(?m)^\s*use\s+(?:function\s+|const\s+)?([a-zA-Z0-9_\\]+)(?:\s+as\s+[a-zA-Z0-9_]+)?\s*;`)
	phpTypeRegex      = regexp.MustCompile(`(?m)^\s*(?:(?:abstract|final|readonly)\s+)?(class|interface|trait|enum)\s+([a-zA-Z0-9_]+)`)
	phpFuncRegex      = regexp.MustCompile(`^\s*(?:(?:public|protected|private|static|final|abstract)\s+)*function\s+&?\s*([a-zA-Z0-9_]+)\s*\(([^)]*)\)\s*(?::\s*([^{\n]+))?`)
	phpCallRegex      = regexp.MustCompile(`\b([a-zA-Z0-9_]+)\s*\(`)
)

var phpKeywordCalls = map[string]bool{
	"if": true, "for": true, "foreach": true, "while": true, "switch": true,
	"return": true, "function": true, "echo": true, "isset": true, "empty": true,
}

func ParsePHPFile(relPath string, code string, sha string, domain string) *ParsedFileResult {
	lines := strings.Split(code, "\n")
	loc := len(lines)
	layer := DetectLayer(relPath)

	ns := ""
	if m := phpNamespaceRegex.FindStringSubmatch(code); len(m) > 1 {
		ns = m[1]
	}

	var imports []ImportSpec
	for _, m := range phpUseRegex.FindAllStringSubmatch(code, -1) {
		fullSpec := strings.Trim(strings.TrimSpace(m[1]), "\\")
		parts := strings.Split(fullSpec, "\\")
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
		Language:  "php",
		LOC:       loc,
		Domain:    domain,
		Layer:     layer,
		SHA:       sha,
		Level:     string(models.LevelL1),
		Labels:    []string{"L1Module", "Module", "File"},
		Docstring: fmt.Sprintf("PHP namespace %s in %s", ns, relPath),
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

	for _, idx := range phpTypeRegex.FindAllStringSubmatchIndex(code, -1) {
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
			Labels:    []string{"L2Class", "Class", "PHP"},
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
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
			continue
		}

		m := phpFuncRegex.FindStringSubmatch(line)
		if len(m) < 3 {
			continue
		}
		fnName := strings.TrimSpace(m[1])
		params := strings.TrimSpace(m[2])
		retType := ""
		if len(m) > 3 {
			retType = strings.TrimSpace(m[3])
		}
		if phpKeywordCalls[fnName] {
			continue
		}

		isPublic := !strings.Contains(line, "private") && !strings.Contains(line, "protected")
		if isPublic {
			exports = append(exports, fnName)
		}

		sig := fmt.Sprintf("function %s(%s)", fnName, params)
		if retType != "" {
			sig += ": " + retType
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

		bodyLines, foundCalls, complexity := scanBody(lines, lineNum, 150, phpCallRegex,
			phpKeywordCalls, fnName,
			[]string{"if ", "for ", "foreach ", "while ", "switch ", "catch "},
			[]string{" && ", " || ", " ?? "}, phpFuncRegex)
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
