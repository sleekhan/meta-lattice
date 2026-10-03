package indexer

import (
	"path"
	"regexp"
	"strings"

	"meta-lattice/src/models"
)

var (
	tsImportRegex = regexp.MustCompile(`(?m)^\s*import\s+(?:(?:\*\s+as\s+([a-zA-Z0-9_]+)|{([^}]+)}|([a-zA-Z0-9_]+))\s+from\s+)?['"]([^'"]+)['"]`)
	tsClassRegex  = regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:default\s+)?class\s+([a-zA-Z0-9_]+)(?:\s+extends\s+([a-zA-Z0-9_.]+))?(?:\s+implements\s+([^{]+))?\s*\{`)
	tsIfaceRegex  = regexp.MustCompile(`(?m)^\s*(?:export\s+)?interface\s+([a-zA-Z0-9_]+)(?:\s+extends\s+([^{]+))?\s*\{`)
	tsFuncRegex   = regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:async\s+)?function\s+([a-zA-Z0-9_]+)\s*\(`)
	tsArrowRegex  = regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:const|let|var)\s+([a-zA-Z0-9_]+)\s*=\s*(?:async\s+)?\([^)]*\)\s*(?::\s*[^=]+)?=>`)
	tsCallRegex   = regexp.MustCompile(`([a-zA-Z0-9_]+)\s*\(`)
)

func computeTSComplexity(lines []string) int {
	complexity := 1
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
			continue
		}
		words := strings.Fields(trimmed)
		for _, w := range words {
			clean := strings.Trim(w, "{};:,()")
			switch clean {
			case "if", "else if", "while", "for", "catch", "switch", "case":
				complexity++
			case "&&", "||", "?":
				complexity++
			}
		}
	}
	return complexity
}

// ParseTSJSFile parses TypeScript or JavaScript source file into L1, L2, L3 structures.
func ParseTSJSFile(filePath string, sourceCode string, sha string, domain string) *ParsedFileResult {
	lines := strings.Split(sourceCode, "\n")
	loc := len(lines)
	fileID := "file:" + filePath
	layer := DetectLayer(filePath)
	lang := "typescript"
	ext := path.Ext(filePath)
	if ext == ".js" || ext == ".jsx" || ext == ".mjs" || ext == ".cjs" {
		lang = "javascript"
	}

	var classNodes []models.L2ClassNode
	var symbolNodes []models.L3SymbolNode
	var imports []ImportSpec
	var allCalls []CallSpec
	var exports []string
	var importPaths []string

	// 1. Imports
	importMatches := tsImportRegex.FindAllStringSubmatch(sourceCode, -1)
	for _, m := range importMatches {
		moduleSpec := m[4]
		isRel := strings.HasPrefix(moduleSpec, ".")
		importPaths = append(importPaths, moduleSpec)

		var names []string
		if m[1] != "" { // * as namespace
			names = append(names, m[1])
		} else if m[2] != "" { // { a, b as c }
			for _, part := range strings.Split(m[2], ",") {
				part = strings.TrimSpace(part)
				if part != "" {
					flds := strings.Fields(part)
					if len(flds) >= 3 && flds[1] == "as" {
						names = append(names, flds[2])
					} else if len(flds) > 0 {
						names = append(names, flds[0])
					}
				}
			}
		} else if m[3] != "" { // default import
			names = append(names, m[3])
		}

		imports = append(imports, ImportSpec{
			SourceFile: filePath,
			ModuleSpec: moduleSpec,
			Names:      names,
			IsRelative: isRel,
		})
	}

	// 2. Scan lines for classes, interfaces, functions, and arrow consts
	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Class declaration
		if strings.Contains(trimmed, "class ") && strings.Contains(trimmed, "{") {
			m := tsClassRegex.FindStringSubmatch(trimmed)
			if len(m) > 1 {
				className := m[1]
				classID := "class:" + filePath + ":" + className
				isExported := strings.Contains(trimmed, "export")
				if isExported {
					exports = append(exports, className)
				}

				var bases []string
				if len(m) > 2 && m[2] != "" {
					bases = append(bases, strings.TrimSpace(m[2]))
				}
				if len(m) > 3 && m[3] != "" {
					for _, iface := range strings.Split(m[3], ",") {
						iface = strings.TrimSpace(iface)
						if iface != "" {
							bases = append(bases, iface)
						}
					}
				}

				classStartLine := i + 1
				// Find closing brace of class
				braceCount := 0
				j := i
				classEndLine := classStartLine
				var classLines []string
				var methodSigs []string
				var fieldSigs []string

				for j < len(lines) {
					l := lines[j]
					classLines = append(classLines, l)
					braceCount += strings.Count(l, "{") - strings.Count(l, "}")
					classEndLine = j + 1

					// Inside class body: check methods
					if j > i && braceCount >= 1 {
						t := strings.TrimSpace(l)
						// Check method e.g. "login(username: string): boolean {" or "async login(...) {"
						if strings.Contains(t, "(") && (strings.Contains(t, ")") || strings.Contains(t, "{")) && !strings.HasPrefix(t, "//") && !strings.HasPrefix(t, "*") {
							// Check if method
							parenIdx := strings.Index(t, "(")
							prefix := strings.TrimSpace(t[:parenIdx])
							fields := strings.Fields(prefix)
							if len(fields) > 0 {
								methodName := fields[len(fields)-1]
								if methodName != "if" && methodName != "for" && methodName != "while" && methodName != "switch" && methodName != "constructor" {
									// Extract signature up to '{'
									sig := t
									braceIdx := strings.Index(t, "{")
									if braceIdx != -1 {
										sig = strings.TrimSpace(t[:braceIdx])
									}
									methodSigs = append(methodSigs, sig)

									methodID := "symbol:" + filePath + ":" + className + "." + methodName
									methodStartLine := j + 1

									// Find method end
									mBraces := strings.Count(l, "{") - strings.Count(l, "}")
									k := j + 1
									methodEndLine := methodStartLine
									var methodLines []string
									methodLines = append(methodLines, l)
									for k < len(lines) && mBraces > 0 {
										mBraces += strings.Count(lines[k], "{") - strings.Count(lines[k], "}")
										methodLines = append(methodLines, lines[k])
										methodEndLine = k + 1
										k++
									}

									methodCode := strings.Join(methodLines, "\n")
									complexity := computeTSComplexity(methodLines)

									var calls []string
									callMatches := tsCallRegex.FindAllStringSubmatch(methodCode, -1)
									for _, cm := range callMatches {
										if len(cm) > 1 && cm[1] != methodName && cm[1] != "if" && cm[1] != "for" && cm[1] != "while" {
											calls = append(calls, cm[1])
											allCalls = append(allCalls, CallSpec{
												CallerID:   methodID,
												CalledName: cm[1],
												LineNumber: methodStartLine,
											})
										}
									}

									symbolNodes = append(symbolNodes, models.L3SymbolNode{
										ID:                   methodID,
										Name:                 className + "." + methodName,
										ParentID:             classID,
										FilePath:             filePath,
										Kind:                 "method",
										LineStart:            methodStartLine,
										LineEnd:              methodEndLine,
										Signature:            sig,
										Code:                 methodCode,
										IsPublic:             !strings.HasPrefix(methodName, "_") && !strings.HasPrefix(methodName, "#") && !strings.Contains(sig, "private"),
										CyclomaticComplexity: complexity,
										Calls:                uniqueStrings(calls),
										Level:                string(models.LevelL3),
										Labels:               []string{"L3Symbol", "Symbol", "Function"},
									})
								}
							}
						}
					}

					if j > i && braceCount <= 0 {
						break
					}
					j++
				}

				classNodes = append(classNodes, models.L2ClassNode{
					ID:               classID,
					Name:             className,
					FilePath:         filePath,
					Kind:             "class",
					Bases:            bases,
					LineStart:        classStartLine,
					LineEnd:          classEndLine,
					IsPublic:         isExported,
					MethodSignatures: methodSigs,
					FieldSignatures:  fieldSigs,
					Level:            string(models.LevelL2),
					Labels:           []string{"L2Class", "Class", "Interface"},
				})

				i = j + 1
				continue
			}
		}

		// Interface declaration
		if strings.Contains(trimmed, "interface ") && strings.Contains(trimmed, "{") {
			m := tsIfaceRegex.FindStringSubmatch(trimmed)
			if len(m) > 1 {
				ifaceName := m[1]
				ifaceID := "class:" + filePath + ":" + ifaceName
				isExported := strings.Contains(trimmed, "export")
				if isExported {
					exports = append(exports, ifaceName)
				}

				var bases []string
				if len(m) > 2 && m[2] != "" {
					for _, b := range strings.Split(m[2], ",") {
						b = strings.TrimSpace(b)
						if b != "" {
							bases = append(bases, b)
						}
					}
				}

				ifaceStartLine := i + 1
				braceCount := 0
				j := i
				ifaceEndLine := ifaceStartLine
				var methodSigs []string
				var fieldSigs []string

				for j < len(lines) {
					l := lines[j]
					braceCount += strings.Count(l, "{") - strings.Count(l, "}")
					ifaceEndLine = j + 1

					if j > i && braceCount >= 1 {
						t := strings.TrimSpace(l)
						if strings.Contains(t, "(") && !strings.HasPrefix(t, "//") && !strings.HasPrefix(t, "/*") {
							methodSigs = append(methodSigs, strings.TrimSuffix(t, ";"))
						} else if strings.Contains(t, ":") && !strings.HasPrefix(t, "//") {
							fieldSigs = append(fieldSigs, strings.TrimSuffix(t, ";"))
						}
					}

					if j > i && braceCount <= 0 {
						break
					}
					j++
				}

				classNodes = append(classNodes, models.L2ClassNode{
					ID:               ifaceID,
					Name:             ifaceName,
					FilePath:         filePath,
					Kind:             "interface",
					Bases:            bases,
					LineStart:        ifaceStartLine,
					LineEnd:          ifaceEndLine,
					IsPublic:         isExported,
					MethodSignatures: methodSigs,
					FieldSignatures:  fieldSigs,
					Level:            string(models.LevelL2),
					Labels:           []string{"L2Class", "Class", "Interface"},
				})

				i = j + 1
				continue
			}
		}

		// Function declaration
		if strings.Contains(trimmed, "function ") {
			m := tsFuncRegex.FindStringSubmatch(trimmed)
			if len(m) > 1 {
				funcName := m[1]
				isExported := strings.Contains(trimmed, "export")
				if isExported {
					exports = append(exports, funcName)
				}

				funcID := "symbol:" + filePath + ":" + funcName
				funcStartLine := i + 1

				braceCount := strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
				sig := trimmed
				braceIdx := strings.Index(trimmed, "{")
				if braceIdx != -1 {
					sig = strings.TrimSpace(trimmed[:braceIdx])
				}

				j := i + 1
				funcEndLine := funcStartLine
				var funcLines []string
				funcLines = append(funcLines, line)

				if strings.Contains(trimmed, "{") {
					for j < len(lines) && braceCount > 0 {
						l := lines[j]
						braceCount += strings.Count(l, "{") - strings.Count(l, "}")
						funcLines = append(funcLines, l)
						funcEndLine = j + 1
						j++
					}
				}

				codeSlice := strings.Join(funcLines, "\n")
				complexity := computeTSComplexity(funcLines)

				var calls []string
				callMatches := tsCallRegex.FindAllStringSubmatch(codeSlice, -1)
				for _, cm := range callMatches {
					if len(cm) > 1 && cm[1] != funcName && cm[1] != "if" && cm[1] != "for" && cm[1] != "while" {
						calls = append(calls, cm[1])
						allCalls = append(allCalls, CallSpec{
							CallerID:   funcID,
							CalledName: cm[1],
							LineNumber: funcStartLine,
						})
					}
				}

				symbolNodes = append(symbolNodes, models.L3SymbolNode{
					ID:                   funcID,
					Name:                 funcName,
					ParentID:             fileID,
					FilePath:             filePath,
					Kind:                 "function",
					LineStart:            funcStartLine,
					LineEnd:              funcEndLine,
					Signature:            sig,
					Code:                 codeSlice,
					IsPublic:             isExported || !strings.HasPrefix(funcName, "_"),
					CyclomaticComplexity: complexity,
					Calls:                uniqueStrings(calls),
					Level:                string(models.LevelL3),
					Labels:               []string{"L3Symbol", "Symbol", "Function"},
				})

				i = max(i+1, j)
				continue
			}
		}

		// Arrow function or const function e.g. "const local = (x: number) => x;"
		if (strings.Contains(trimmed, "const ") || strings.Contains(trimmed, "let ") || strings.Contains(trimmed, "var ")) &&
			(strings.Contains(trimmed, "=>") || strings.Contains(trimmed, "function")) {
			m := tsArrowRegex.FindStringSubmatch(trimmed)
			funcName := ""
			if len(m) > 1 {
				funcName = m[1]
			} else {
				// Alternative fallback for arrow functions
				eqIdx := strings.Index(trimmed, "=")
				if eqIdx != -1 {
					lhs := strings.TrimSpace(trimmed[:eqIdx])
					parts := strings.Fields(lhs)
					if len(parts) >= 2 {
						funcName = parts[len(parts)-1]
					}
				}
			}

			if funcName != "" {
				isExported := strings.Contains(trimmed, "export")
				if isExported {
					exports = append(exports, funcName)
				}

				funcID := "symbol:" + filePath + ":" + funcName
				funcStartLine := i + 1
				funcEndLine := funcStartLine
				codeSlice := line

				// If multi-line, collect until semicolon or balanced braces
				if strings.Contains(trimmed, "{") {
					braceCount := strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
					j := i + 1
					var fLines []string
					fLines = append(fLines, line)
					for j < len(lines) && braceCount > 0 {
						l := lines[j]
						braceCount += strings.Count(l, "{") - strings.Count(l, "}")
						fLines = append(fLines, l)
						funcEndLine = j + 1
						j++
					}
					codeSlice = strings.Join(fLines, "\n")
					i = max(i+1, j)
				} else {
					i++
				}

				complexity := 1
				var calls []string
				callMatches := tsCallRegex.FindAllStringSubmatch(codeSlice, -1)
				for _, cm := range callMatches {
					if len(cm) > 1 && cm[1] != funcName && cm[1] != "if" && cm[1] != "for" && cm[1] != "while" {
						calls = append(calls, cm[1])
						allCalls = append(allCalls, CallSpec{
							CallerID:   funcID,
							CalledName: cm[1],
							LineNumber: funcStartLine,
						})
					}
				}

				sig := trimmed
				arrowIdx := strings.Index(trimmed, "=>")
				if arrowIdx != -1 {
					sig = strings.TrimSpace(trimmed[:arrowIdx+2])
				}

				symbolNodes = append(symbolNodes, models.L3SymbolNode{
					ID:                   funcID,
					Name:                 funcName,
					ParentID:             fileID,
					FilePath:             filePath,
					Kind:                 "function",
					LineStart:            funcStartLine,
					LineEnd:              funcEndLine,
					Signature:            sig,
					Code:                 codeSlice,
					IsPublic:             isExported || !strings.HasPrefix(funcName, "_"),
					CyclomaticComplexity: complexity,
					Calls:                uniqueStrings(calls),
					Level:                string(models.LevelL3),
					Labels:               []string{"L3Symbol", "Symbol", "Function"},
				})

				continue
			}
		}

		i++
	}

	return &ParsedFileResult{
		FileNode: models.L1ModuleNode{
			ID:          fileID,
			Path:        filePath,
			Filename:    path.Base(filePath),
			Language:    lang,
			LOC:         loc,
			SHA:         sha,
			Docstring:   "",
			Domain:      domain,
			Layer:       layer,
			Exports:     uniqueStrings(exports),
			ImportPaths: uniqueStrings(importPaths),
			Level:       string(models.LevelL1),
			Labels:      []string{"L1Module", "Module", "File"},
		},
		ClassNodes:  classNodes,
		SymbolNodes: symbolNodes,
		Imports:     imports,
		Calls:       allCalls,
	}
}
