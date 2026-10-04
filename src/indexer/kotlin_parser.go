package indexer

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"meta-lattice/src/models"
)

var (
	kotlinPackageRegex = regexp.MustCompile(`(?m)^\s*package\s+([a-zA-Z0-9_.]+)`)
	kotlinImportRegex  = regexp.MustCompile(`(?m)^\s*import\s+([a-zA-Z0-9_.]+)`)
	kotlinTypeRegex    = regexp.MustCompile(`(?m)^\s*(?:(?:public|private|protected|internal|open|abstract|sealed|data|enum|annotation)\s+)*(data\s+class|sealed\s+class|enum\s+class|class|interface|object)\s+([a-zA-Z0-9_]+)`)
	kotlinFunRegex     = regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|open|override|abstract|suspend|inline|tailrec|operator|infix)\s+)*fun\s+(?:<[^>]+>\s+)?(?:[a-zA-Z0-9_<>?]+\.)?([a-zA-Z0-9_]+)\s*\(([^)]*)\)(?:\s*:\s*([^{\n=]+))?`)
	kotlinCallRegex    = regexp.MustCompile(`\b([a-zA-Z0-9_]+)\s*\(`)
)

var kotlinKeywordCalls = map[string]bool{
	"if": true, "for": true, "while": true, "when": true, "return": true,
	"fun": true, "class": true, "super": true, "this": true,
}

func ParseKotlinFile(relPath string, code string, sha string, domain string) *ParsedFileResult {
	lines := strings.Split(code, "\n")
	loc := len(lines)
	layer := DetectLayer(relPath)

	pkg := ""
	if m := kotlinPackageRegex.FindStringSubmatch(code); len(m) > 1 {
		pkg = m[1]
	}

	var imports []ImportSpec
	for _, m := range kotlinImportRegex.FindAllStringSubmatch(code, -1) {
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
		Language:  "kotlin",
		LOC:       loc,
		Domain:    domain,
		Layer:     layer,
		SHA:       sha,
		Level:     string(models.LevelL1),
		Labels:    []string{"L1Module", "Module", "File"},
		Docstring: fmt.Sprintf("Kotlin package %s in %s", pkg, relPath),
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

	for _, idx := range kotlinTypeRegex.FindAllStringSubmatchIndex(code, -1) {
		kind := strings.Join(strings.Fields(code[idx[2]:idx[3]]), " ")
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
			Labels:    []string{"L2Class", "Class", "Kotlin"},
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

		m := kotlinFunRegex.FindStringSubmatch(line)
		if len(m) < 3 {
			continue
		}
		fnName := strings.TrimSpace(m[1])
		params := strings.TrimSpace(m[2])
		retType := ""
		if len(m) > 3 {
			retType = strings.TrimSpace(m[3])
		}
		if kotlinKeywordCalls[fnName] {
			continue
		}

		isPublic := !strings.Contains(line, " private ") && !strings.Contains(line, "private ") &&
			!strings.Contains(line, " internal ")
		if isPublic {
			exports = append(exports, fnName)
		}

		sig := fmt.Sprintf("fun %s(%s)", fnName, params)
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

		bodyLines, foundCalls, complexity := scanBody(lines, lineNum, 150, kotlinCallRegex,
			kotlinKeywordCalls, fnName,
			[]string{"if ", "when ", "for ", "while ", "catch "},
			[]string{" && ", " || "}, kotlinFunRegex)
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
