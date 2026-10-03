package indexer

import (
	"path"
	"regexp"
	"strings"

	"meta-lattice/src/models"
)

var (
	pyImportRegex     = regexp.MustCompile(`(?m)^\s*import\s+([a-zA-Z0-9_.,\s]+)`)
	pyFromImportRegex = regexp.MustCompile(`(?m)^\s*from\s+([a-zA-Z0-9_.]+)\s+import\s+([a-zA-Z0-9_*,\s()]+)`)
	pyAllRegex        = regexp.MustCompile(`__all__\s*=\s*\[([^\]]+)\]`)
	pyClassRegex      = regexp.MustCompile(`(?m)^class\s+([a-zA-Z0-9_]+)(?:\(([^)]*)\))?\s*:`)
	pyCallRegex       = regexp.MustCompile(`([a-zA-Z0-9_]+)\s*\(`)
	pyAttrCallRegex   = regexp.MustCompile(`\.\s*([a-zA-Z0-9_]+)\s*\(`)
)

func computePyComplexity(lines []string) int {
	complexity := 1
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Count branch keywords
		words := strings.Fields(trimmed)
		for _, w := range words {
			clean := strings.Trim(w, ":,()")
			switch clean {
			case "if", "elif", "while", "for", "except", "with":
				complexity++
			case "and", "or":
				complexity++
			}
		}
	}
	return complexity
}

func extractDocstring(lines []string, startLine int) (string, int) {
	if startLine >= len(lines) {
		return "", startLine
	}
	firstLine := strings.TrimSpace(lines[startLine])
	var quote string
	if strings.HasPrefix(firstLine, `"""`) {
		quote = `"""`
	} else if strings.HasPrefix(firstLine, `'''`) {
		quote = `'''`
	} else {
		return "", startLine
	}

	trimmed := strings.TrimPrefix(firstLine, quote)
	if strings.HasSuffix(trimmed, quote) && len(trimmed) >= len(quote) {
		doc := strings.TrimSuffix(trimmed, quote)
		return strings.TrimSpace(doc), startLine + 1
	}

	var docLines []string
	if trimmed != "" {
		docLines = append(docLines, trimmed)
	}

	for i := startLine + 1; i < len(lines); i++ {
		l := lines[i]
		if strings.Contains(l, quote) {
			part := strings.Split(l, quote)[0]
			if part != "" {
				docLines = append(docLines, part)
			}
			return strings.TrimSpace(strings.Join(docLines, "\n")), i + 1
		}
		docLines = append(docLines, l)
	}

	return strings.TrimSpace(strings.Join(docLines, "\n")), len(lines)
}

// extractPythonSignature handles multi-line def statements, typed annotations, and default parameters.
// It searches for the closing parenthesis and colon at the def's nesting level.
func extractPythonSignature(lines []string, startIdx int) (string, int, string) {
	var sigParts []string
	endIdx := startIdx
	parenCount := 0
	foundOpen := false
	inlineBody := ""

	inSingleQuote := false
	inDoubleQuote := false

	for i := startIdx; i < len(lines); i++ {
		line := lines[i]
		endIdx = i

		for j := 0; j < len(line); j++ {
			ch := line[j]
			if ch == '\'' && !inDoubleQuote {
				if j > 0 && line[j-1] == '\\' {
					// escaped
				} else {
					inSingleQuote = !inSingleQuote
				}
			} else if ch == '"' && !inSingleQuote {
				if j > 0 && line[j-1] == '\\' {
					// escaped
				} else {
					inDoubleQuote = !inDoubleQuote
				}
			}

			if !inSingleQuote && !inDoubleQuote {
				if ch == '(' {
					parenCount++
					foundOpen = true
				} else if ch == ')' {
					parenCount--
				} else if foundOpen && parenCount <= 0 && ch == ':' {
					sigParts = append(sigParts, strings.TrimSpace(line[:j]))
					inlineBody = strings.TrimSpace(line[j+1:])
					goto done
				}
			}
		}
		sigParts = append(sigParts, strings.TrimSpace(line))
	}
done:
	rawSig := strings.Join(sigParts, " ")

	// Normalize spaces inside signature
	normalized := strings.Join(strings.Fields(rawSig), " ")
	// Ensure formatting like "def name(a: int, b: str='x:y') -> int"
	normalized = strings.ReplaceAll(normalized, "( ", "(")
	normalized = strings.ReplaceAll(normalized, " )", ")")
	normalized = strings.ReplaceAll(normalized, " ,", ",")
	normalized = strings.ReplaceAll(normalized, " :", ":")
	normalized = strings.ReplaceAll(normalized, "= ", "=")
	normalized = strings.ReplaceAll(normalized, " =", "=")
	normalized = strings.ReplaceAll(normalized, ",)", ")")
	normalized = strings.ReplaceAll(normalized, ", )", ")")

	return normalized, endIdx, inlineBody
}

func getIndentLevel(line string) int {
	indent := 0
	for _, ch := range line {
		if ch == ' ' {
			indent++
		} else if ch == '\t' {
			indent += 4
		} else {
			break
		}
	}
	return indent
}

func extractCallsFromLines(bodyLines []string, callerID string, baseLine int) []CallSpec {
	var calls []CallSpec
	seen := make(map[string]bool)

	for i, l := range bodyLines {
		lineNum := baseLine + i
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Direct calls
		matches := pyCallRegex.FindAllStringSubmatch(l, -1)
		for _, m := range matches {
			if len(m) > 1 {
				name := m[1]
				if isPythonKeyword(name) {
					continue
				}
				key := name
				if !seen[key] {
					seen[key] = true
					calls = append(calls, CallSpec{
						CallerID:   callerID,
						CalledName: name,
						LineNumber: lineNum,
					})
				}
			}
		}

		// Attribute calls obj.attr()
		attrMatches := pyAttrCallRegex.FindAllStringSubmatch(l, -1)
		for _, m := range attrMatches {
			if len(m) > 1 {
				name := m[1]
				if isPythonKeyword(name) {
					continue
				}
				key := name
				if !seen[key] {
					seen[key] = true
					calls = append(calls, CallSpec{
						CallerID:   callerID,
						CalledName: name,
						LineNumber: lineNum,
					})
				}
			}
		}
	}

	return calls
}

func isPythonKeyword(w string) bool {
	switch w {
	case "if", "elif", "else", "for", "while", "return", "yield", "class", "def",
		"import", "from", "as", "try", "except", "finally", "with", "assert",
		"raise", "pass", "break", "continue", "in", "is", "lambda", "not", "and", "or":
		return true
	}
	return false
}

// ParsePythonFile parses a Python source file into L1, L2, L3 structures.
func ParsePythonFile(filePath string, sourceCode string, sha string, domain string) *ParsedFileResult {
	lines := strings.Split(sourceCode, "\n")
	loc := len(lines)
	fileID := "file:" + filePath
	layer := DetectLayer(filePath)

	moduleDoc, _ := extractDocstring(lines, 0)

	var classNodes []models.L2ClassNode
	var symbolNodes []models.L3SymbolNode
	var imports []ImportSpec
	var allCalls []CallSpec
	var exports []string
	var importPaths []string

	// 1. Check __all__
	allMatches := pyAllRegex.FindStringSubmatch(sourceCode)
	if len(allMatches) > 1 {
		items := strings.Split(allMatches[1], ",")
		for _, it := range items {
			val := strings.Trim(strings.TrimSpace(it), `"'`)
			if val != "" {
				exports = append(exports, val)
			}
		}
	}

	// 2. Extract Imports
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		if m := pyFromImportRegex.FindStringSubmatch(trimmed); len(m) > 2 {
			modSpec := strings.TrimSpace(m[1])
			namesRaw := strings.TrimSpace(m[2])
			namesRaw = strings.Trim(namesRaw, "()")
			var names []string
			for _, n := range strings.Split(namesRaw, ",") {
				n = strings.TrimSpace(n)
				if n != "" {
					parts := strings.Fields(n)
					if len(parts) >= 3 && parts[1] == "as" {
						names = append(names, parts[2])
					} else if len(parts) > 0 {
						names = append(names, parts[0])
					}
				}
			}
			isRel := strings.HasPrefix(modSpec, ".")
			importPaths = append(importPaths, modSpec)
			imports = append(imports, ImportSpec{
				SourceFile: filePath,
				ModuleSpec: modSpec,
				Names:      names,
				IsRelative: isRel,
			})
		} else if m := pyImportRegex.FindStringSubmatch(trimmed); len(m) > 1 {
			mods := strings.Split(m[1], ",")
			for _, mod := range mods {
				mod = strings.TrimSpace(mod)
				if mod == "" {
					continue
				}
				parts := strings.Fields(mod)
				modName := parts[0]
				var alias string
				if len(parts) >= 3 && parts[1] == "as" {
					alias = parts[2]
				} else {
					alias = modName
				}
				importPaths = append(importPaths, modName)
				imports = append(imports, ImportSpec{
					SourceFile: filePath,
					ModuleSpec: modName,
					Names:      []string{alias},
					IsRelative: false,
				})
			}
		}
	}

	// 3. Line by line parsing for classes and functions
	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Top-level Class
		if strings.HasPrefix(trimmed, "class ") && getIndentLevel(line) == 0 {
			match := pyClassRegex.FindStringSubmatch(trimmed)
			if len(match) > 1 {
				className := match[1]
				classID := "class:" + filePath + ":" + className
				isPublic := !strings.HasPrefix(className, "_")
				if isPublic && len(exports) == 0 {
					exports = append(exports, className)
				}

				var bases []string
				if len(match) > 2 && match[2] != "" {
					for _, b := range strings.Split(match[2], ",") {
						b = strings.TrimSpace(b)
						if b != "" {
							bases = append(bases, b)
						}
					}
				}

				classStartLine := i + 1
				classDoc, nextIdx := extractDocstring(lines, i+1)
				if nextIdx > i+1 {
					i = nextIdx - 1
				}

				var methodSigs []string
				var fieldSigs []string

				// Find class body end & extract methods
				classEndLine := classStartLine
				j := i + 1
				for j < len(lines) {
					subLine := lines[j]
					subTrimmed := strings.TrimSpace(subLine)
					if subTrimmed == "" || strings.HasPrefix(subTrimmed, "#") {
						j++
						continue
					}
					if getIndentLevel(subLine) == 0 {
						break
					}
					classEndLine = j + 1

					// Method definition
					if strings.HasPrefix(subTrimmed, "def ") || strings.HasPrefix(subTrimmed, "async def ") {
						methodStartLine := j + 1
						sig, sigEndIdx, inlineBody := extractPythonSignature(lines, j)

						methodName := ""
						isAsync := strings.HasPrefix(subTrimmed, "async def ")
						fnPrefix := "def "
						if isAsync {
							fnPrefix = "async def "
						}
						afterDef := strings.TrimPrefix(sig, fnPrefix)
						parenPos := strings.Index(afterDef, "(")
						if parenPos != -1 {
							methodName = strings.TrimSpace(afterDef[:parenPos])
						}

						methodDoc, _ := extractDocstring(lines, sigEndIdx+1)
						methodID := "symbol:" + filePath + ":" + className + "." + methodName

						// Collect method body lines
						methodIndent := getIndentLevel(lines[j])
						k := sigEndIdx + 1
						methodEndLine := methodStartLine
						var methodBody []string
						if inlineBody != "" {
							methodBody = append(methodBody, inlineBody)
						}
						for k < len(lines) {
							mLine := lines[k]
							mTrimmed := strings.TrimSpace(mLine)
							if mTrimmed != "" && !strings.HasPrefix(mTrimmed, "#") {
								if getIndentLevel(mLine) <= methodIndent {
									break
								}
							}
							methodBody = append(methodBody, mLine)
							methodEndLine = k + 1
							k++
						}

						codeSlice := strings.Join(lines[j:methodEndLine], "\n")
						complexity := computePyComplexity(methodBody)
						methodCalls := extractCallsFromLines(methodBody, methodID, sigEndIdx+1)
						allCalls = append(allCalls, methodCalls...)

						var calledNames []string
						for _, c := range methodCalls {
							calledNames = append(calledNames, c.CalledName)
						}

						methodSigs = append(methodSigs, sig)

						kind := "method"
						if isAsync {
							kind = "async_method"
						}

						symbolNodes = append(symbolNodes, models.L3SymbolNode{
							ID:                   methodID,
							Name:                 className + "." + methodName,
							ParentID:             classID,
							FilePath:             filePath,
							Kind:                 kind,
							LineStart:            methodStartLine,
							LineEnd:              methodEndLine,
							Signature:            sig,
							Docstring:            methodDoc,
							Code:                 codeSlice,
							IsPublic:             !strings.HasPrefix(methodName, "_"),
							CyclomaticComplexity: complexity,
							Calls:                uniqueStrings(calledNames),
							Level:                string(models.LevelL3),
							Labels:               []string{"L3Symbol", "Symbol", "Function"},
						})

						j = k
						continue
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
					Docstring:        classDoc,
					IsPublic:         isPublic,
					MethodSignatures: methodSigs,
					FieldSignatures:  fieldSigs,
					Level:            string(models.LevelL2),
					Labels:           []string{"L2Class", "Class", "Interface"},
				})

				i = j
				continue
			}
		}

		// Top-level Function
		if (strings.HasPrefix(trimmed, "def ") || strings.HasPrefix(trimmed, "async def ")) && getIndentLevel(line) == 0 {
			funcStartLine := i + 1
			sig, sigEndIdx, inlineBody := extractPythonSignature(lines, i)

			funcName := ""
			isAsync := strings.HasPrefix(trimmed, "async def ")
			fnPrefix := "def "
			if isAsync {
				fnPrefix = "async def "
			}
			afterDef := strings.TrimPrefix(sig, fnPrefix)
			parenPos := strings.Index(afterDef, "(")
			if parenPos != -1 {
				funcName = strings.TrimSpace(afterDef[:parenPos])
			}

			isPublic := !strings.HasPrefix(funcName, "_")
			if isPublic && len(exports) == 0 {
				exports = append(exports, funcName)
			}

			funcDoc, _ := extractDocstring(lines, sigEndIdx+1)
			funcID := "symbol:" + filePath + ":" + funcName

			k := sigEndIdx + 1
			funcEndLine := funcStartLine
			var funcBody []string
			if inlineBody != "" {
				funcBody = append(funcBody, inlineBody)
			}
			for k < len(lines) {
				fLine := lines[k]
				fTrimmed := strings.TrimSpace(fLine)
				if fTrimmed != "" && !strings.HasPrefix(fTrimmed, "#") {
					if getIndentLevel(fLine) == 0 {
						break
					}
				}
				funcBody = append(funcBody, fLine)
				funcEndLine = k + 1
				k++
			}

			codeSlice := strings.Join(lines[i:funcEndLine], "\n")
			complexity := computePyComplexity(funcBody)
			fnCalls := extractCallsFromLines(funcBody, funcID, sigEndIdx+1)
			allCalls = append(allCalls, fnCalls...)

			var calledNames []string
			for _, c := range fnCalls {
				calledNames = append(calledNames, c.CalledName)
			}

			kind := "function"
			if isAsync {
				kind = "async_function"
			}

			symbolNodes = append(symbolNodes, models.L3SymbolNode{
				ID:                   funcID,
				Name:                 funcName,
				ParentID:             fileID,
				FilePath:             filePath,
				Kind:                 kind,
				LineStart:            funcStartLine,
				LineEnd:              funcEndLine,
				Signature:            sig,
				Docstring:            funcDoc,
				Code:                 codeSlice,
				IsPublic:             isPublic,
				CyclomaticComplexity: complexity,
				Calls:                uniqueStrings(calledNames),
				Level:                string(models.LevelL3),
				Labels:               []string{"L3Symbol", "Symbol", "Function"},
			})

			i = k
			continue
		}

		i++
	}

	return &ParsedFileResult{
		FileNode: models.L1ModuleNode{
			ID:          fileID,
			Path:        filePath,
			Filename:    path.Base(filePath),
			Language:    "python",
			LOC:         loc,
			SHA:         sha,
			Docstring:   moduleDoc,
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
