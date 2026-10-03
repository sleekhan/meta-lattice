package indexer

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"meta-lattice/src/models"
)

var (
	javaPackageRegex = regexp.MustCompile(`(?m)^\s*package\s+([a-zA-Z0-9_.]+)\s*;`)
	javaImportRegex  = regexp.MustCompile(`(?m)^\s*import\s+(?:static\s+)?([a-zA-Z0-9_.*]+)\s*;`)
	javaTypeDeclRegex = regexp.MustCompile(`(?m)(?:public|protected|private|abstract|final|static|\s)*\b(class|interface|enum|record)\s+([a-zA-Z0-9_]+)(?:<[^>]+>)?(?:\s+extends\s+[a-zA-Z0-9_<>.,\s]+)?(?:\s+implements\s+[a-zA-Z0-9_<>.,\s]+)?`)
	javaMethodRegex   = regexp.MustCompile(`(?m)(?:(?:public|protected|private|static|final|abstract|synchronized|native|default)\s+)+([a-zA-Z0-9_<>[\].,\s]+)\s+([a-zA-Z0-9_]+)\s*\(([^)]*)\)\s*(?:throws\s+[a-zA-Z0-9_,\s]+)?\s*\{?`)
	javaCallRegex     = regexp.MustCompile(`\b([a-zA-Z0-9_]+)\s*\(`)
)

func ParseJavaFile(relPath string, code string, sha string, domain string) *ParsedFileResult {
	lines := strings.Split(code, "\n")
	loc := len(lines)
	layer := DetectLayer(relPath)

	pkg := ""
	if m := javaPackageRegex.FindStringSubmatch(code); len(m) > 1 {
		pkg = m[1]
	}

	var imports []ImportSpec
	for _, m := range javaImportRegex.FindAllStringSubmatch(code, -1) {
		fullSpec := m[1]
		parts := strings.Split(fullSpec, ".")
		importedName := parts[len(parts)-1]
		imports = append(imports, ImportSpec{
			SourceFile: relPath,
			ModuleSpec: fullSpec,
			Names:      []string{importedName},
			IsRelative: false,
		})
	}

	fileNode := models.L1ModuleNode{
		ID:        "file:" + relPath,
		Path:      relPath,
		Filename:  path.Base(relPath),
		Language:  "java",
		LOC:       loc,
		Domain:    domain,
		Layer:     layer,
		SHA:       sha,
		Level:     string(models.LevelL1),
		Labels:    []string{"L1Module", "Module", "File"},
		Docstring: fmt.Sprintf("Java package %s in %s", pkg, relPath),
	}

	var classNodes []models.L2ClassNode
	var symbolNodes []models.L3SymbolNode
	var calls []CallSpec
	var exports []string

	// Find classes / interfaces
	typeMatches := javaTypeDeclRegex.FindAllStringSubmatchIndex(code, -1)
	currentClassID := ""

	for _, idx := range typeMatches {
		kind := code[idx[2]:idx[3]]
		name := code[idx[4]:idx[5]]
		exports = append(exports, name)

		clsID := fmt.Sprintf("class:%s:%s", relPath, name)
		currentClassID = clsID

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

	// Find methods
	for lineNum, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
			continue
		}

		if m := javaMethodRegex.FindStringSubmatch(line); len(m) > 3 {
			retType := strings.TrimSpace(m[1])
			methodName := strings.TrimSpace(m[2])
			params := strings.TrimSpace(m[3])

			// Skip keywords that look like method calls
			if methodName == "if" || methodName == "for" || methodName == "while" || methodName == "switch" || methodName == "catch" {
				continue
			}

			isPublic := strings.Contains(line, "public")
			if isPublic {
				exports = append(exports, methodName)
			}

			sig := fmt.Sprintf("%s %s(%s)", retType, methodName, params)
			symID := fmt.Sprintf("symbol:%s:%s", relPath, methodName)
			parentID := fileNode.ID
			if currentClassID != "" {
				parentID = currentClassID
			}

			// Estimate body & complexity
			complexity := 1
			bodyLines := []string{line}
			var methodCalls []string

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

				for _, cm := range javaCallRegex.FindAllStringSubmatch(subLine, -1) {
					cName := cm[1]
					if cName != "if" && cName != "for" && cName != "while" && cName != "switch" && cName != "catch" && cName != methodName {
						methodCalls = append(methodCalls, cName)
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
