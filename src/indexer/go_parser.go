package indexer

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path"
	"strings"
	"unicode"

	"meta-lattice/src/models"
)

func isExportedGo(name string) bool {
	if len(name) == 0 {
		return false
	}
	r := rune(name[0])
	return unicode.IsUpper(r)
}

func computeGoComplexity(n ast.Node) int {
	complexity := 1
	// Bodyless declarations (e.g. assembly-backed funcs) carry a nil *ast.BlockStmt,
	// which would panic inside ast.Inspect.
	if block, ok := n.(*ast.BlockStmt); ok && block == nil {
		return complexity
	}
	ast.Inspect(n, func(node ast.Node) bool {
		switch node.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.CaseClause, *ast.CommClause:
			complexity++
		case *ast.BinaryExpr:
			be := node.(*ast.BinaryExpr)
			if be.Op == token.LAND || be.Op == token.LOR {
				complexity++
			}
		}
		return true
	})
	return complexity
}

func nodeToString(fset *token.FileSet, node any) string {
	var buf bytes.Buffer
	_ = printer.Fprint(&buf, fset, node)
	return buf.String()
}

func ParseGoFile(filePath string, sourceCode string, sha string, domain string) *ParsedFileResult {
	lines := strings.Split(sourceCode, "\n")
	loc := len(lines)
	fileID := "file:" + filePath
	layer := DetectLayer(filePath)

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filePath, sourceCode, parser.ParseComments)
	if err != nil {
		// Return partial L1 node
		return &ParsedFileResult{
			FileNode: models.L1ModuleNode{
				ID:       fileID,
				Path:     filePath,
				Filename: path.Base(filePath),
				Language: "go",
				LOC:      loc,
				SHA:      sha,
				Domain:   domain,
				Layer:    layer,
				Level:    string(models.LevelL1),
				Labels:   []string{"L1Module", "Module", "File"},
			},
		}
	}

	docstring := ""
	if node.Doc != nil {
		docstring = node.Doc.Text()
	}

	var classNodes []models.L2ClassNode
	var symbolNodes []models.L3SymbolNode
	var imports []ImportSpec
	var allCalls []CallSpec
	var exports []string
	var importPaths []string

	// Imports
	for _, imp := range node.Imports {
		pkgPath := strings.Trim(imp.Path.Value, `"`)
		importPaths = append(importPaths, pkgPath)
		var names []string
		if imp.Name != nil {
			names = append(names, imp.Name.Name)
		} else {
			names = append(names, path.Base(pkgPath))
		}
		imports = append(imports, ImportSpec{
			SourceFile: filePath,
			ModuleSpec: pkgPath,
			Names:      names,
			IsRelative: strings.HasPrefix(pkgPath, "."),
		})
	}

	// Classes (Structs & Interfaces in Go)
	for _, decl := range node.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}

		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}

			name := typeSpec.Name.Name
			isPublic := isExportedGo(name)
			if isPublic {
				exports = append(exports, name)
			}

			classID := "class:" + filePath + ":" + name
			startPos := fset.Position(typeSpec.Pos()).Line
			endPos := fset.Position(typeSpec.End()).Line

			typeDoc := ""
			if typeSpec.Doc != nil {
				typeDoc = typeSpec.Doc.Text()
			} else if genDecl.Doc != nil {
				typeDoc = genDecl.Doc.Text()
			}

			kind := "type"
			var fieldSigs []string
			var methodSigs []string
			var bases []string

			switch t := typeSpec.Type.(type) {
			case *ast.StructType:
				kind = "struct"
				if t.Fields != nil {
					for _, field := range t.Fields.List {
						fTypeStr := nodeToString(fset, field.Type)
						if len(field.Names) == 0 {
							// Embedded field / base struct
							bases = append(bases, fTypeStr)
						} else {
							for _, fn := range field.Names {
								fieldSigs = append(fieldSigs, fn.Name+" "+fTypeStr)
							}
						}
					}
				}
			case *ast.InterfaceType:
				kind = "interface"
				if t.Methods != nil {
					for _, method := range t.Methods.List {
						if len(method.Names) == 0 {
							bases = append(bases, nodeToString(fset, method.Type))
						} else {
							for _, mn := range method.Names {
								methodSigs = append(methodSigs, mn.Name+nodeToString(fset, method.Type))
							}
						}
					}
				}
			}

			classNodes = append(classNodes, models.L2ClassNode{
				ID:               classID,
				Name:             name,
				FilePath:         filePath,
				Kind:             kind,
				Bases:            bases,
				LineStart:        startPos,
				LineEnd:          endPos,
				Docstring:        strings.TrimSpace(typeDoc),
				IsPublic:         isPublic,
				MethodSignatures: methodSigs,
				FieldSignatures:  fieldSigs,
				Level:            string(models.LevelL2),
				Labels:           []string{"L2Class", "Class", "Interface"},
			})
		}
	}

	// Functions & Methods
	for _, decl := range node.Decls {
		fnDecl, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}

		funcName := fnDecl.Name.Name
		isPublic := isExportedGo(funcName)

		startLine := fset.Position(fnDecl.Pos()).Line
		endLine := fset.Position(fnDecl.End()).Line

		// Slice code
		codeStart := max(0, startLine-1)
		codeEnd := min(len(lines), endLine)
		codeSlice := strings.Join(lines[codeStart:codeEnd], "\n")

		var params []string
		if fnDecl.Type.Params != nil {
			for _, p := range fnDecl.Type.Params.List {
				pType := nodeToString(fset, p.Type)
				if len(p.Names) == 0 {
					params = append(params, pType)
				} else {
					for _, pn := range p.Names {
						params = append(params, pn.Name+" "+pType)
					}
				}
			}
		}

		fnDoc := ""
		if fnDecl.Doc != nil {
			fnDoc = fnDecl.Doc.Text()
		}

		complexity := computeGoComplexity(fnDecl.Body)

		var receiverName string
		if fnDecl.Recv != nil && len(fnDecl.Recv.List) > 0 {
			recvType := nodeToString(fset, fnDecl.Recv.List[0].Type)
			receiverName = strings.TrimPrefix(recvType, "*")
		}

		var symName string
		var parentID string
		kind := "function"
		if receiverName != "" {
			symName = receiverName + "." + funcName
			parentID = "class:" + filePath + ":" + receiverName
			kind = "method"
		} else {
			symName = funcName
			parentID = fileID
			if isPublic {
				exports = append(exports, funcName)
			}
		}
		symID := "symbol:" + filePath + ":" + symName

		// Signature
		sig := "func "
		if receiverName != "" {
			sig += "(" + nodeToString(fset, fnDecl.Recv.List[0].Type) + ") "
		}
		sig += funcName + "(" + strings.Join(params, ", ") + ")"
		if fnDecl.Type.Results != nil {
			sig += " " + nodeToString(fset, fnDecl.Type.Results)
		}

		// Calls
		var calls []string
		if fnDecl.Body != nil {
			ast.Inspect(fnDecl.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				calledName := ""
				switch fun := call.Fun.(type) {
				case *ast.Ident:
					calledName = fun.Name
				case *ast.SelectorExpr:
					calledName = fun.Sel.Name
				}
				if calledName != "" {
					line := fset.Position(call.Pos()).Line
					allCalls = append(allCalls, CallSpec{
						CallerID:   symID,
						CalledName: calledName,
						LineNumber: line,
					})
					calls = append(calls, calledName)
				}
				return true
			})
		}

		symbolNodes = append(symbolNodes, models.L3SymbolNode{
			ID:                   symID,
			Name:                 symName,
			ParentID:             parentID,
			FilePath:             filePath,
			Kind:                 kind,
			LineStart:            startLine,
			LineEnd:              endLine,
			Signature:            sig,
			Params:               params,
			Docstring:            strings.TrimSpace(fnDoc),
			Code:                 codeSlice,
			IsPublic:             isPublic,
			CyclomaticComplexity: complexity,
			Calls:                uniqueStrings(calls),
			Level:                string(models.LevelL3),
			Labels:               []string{"L3Symbol", "Symbol", "Function"},
		})
	}

	return &ParsedFileResult{
		FileNode: models.L1ModuleNode{
			ID:          fileID,
			Path:        filePath,
			Filename:    path.Base(filePath),
			Language:    "go",
			LOC:         loc,
			SHA:         sha,
			Docstring:   strings.TrimSpace(docstring),
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

func uniqueStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	var out []string
	for _, s := range in {
		if !seen[s] && s != "" {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
