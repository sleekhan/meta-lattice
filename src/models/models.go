package models

// NodeLevel defines hierarchical context level.
type NodeLevel string

const (
	LevelL0 NodeLevel = "L0" // Domain / Package level
	LevelL1 NodeLevel = "L1" // Module / File level
	LevelL2 NodeLevel = "L2" // Class / Interface level
	LevelL3 NodeLevel = "L3" // Function / Symbol level
)

// EdgeType defines graph edge relationships.
type EdgeType string

const (
	EdgeContains   EdgeType = "CONTAINS"   // Parent -> Child (L0->L1, L1->L2, L2->L3, L1->L3)
	EdgeImports    EdgeType = "IMPORTS"    // File -> File (module dependency)
	EdgeCalls      EdgeType = "CALLS"      // Symbol -> Symbol (call graph)
	EdgeExtends    EdgeType = "EXTENDS"    // Class -> Base Class
	EdgeImplements EdgeType = "IMPLEMENTS" // Class -> Interface
	EdgeReferences EdgeType = "REFERENCES" // Symbol -> Type or Symbol
)

// L0DomainNode represents an architectural domain/subsystem.
type L0DomainNode struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Description string   `json:"description"`
	Layer       string   `json:"layer,omitempty"`
	ModuleCount int      `json:"module_count"`
	Level       string   `json:"level"`
	Labels      []string `json:"labels"`
}

// ToProperties converts node to generic map representation.
func (n *L0DomainNode) ToProperties() map[string]any {
	return map[string]any{
		"id":           n.ID,
		"name":         n.Name,
		"path":         n.Path,
		"description":  n.Description,
		"layer":        n.Layer,
		"module_count": n.ModuleCount,
		"level":        string(LevelL0),
		"labels":       []string{"L0Domain", "Domain"},
	}
}

// L1ModuleNode represents a single source file.
type L1ModuleNode struct {
	ID          string   `json:"id"`
	Path        string   `json:"path"`
	Filename    string   `json:"filename"`
	Language    string   `json:"language"`
	LOC         int      `json:"loc"`
	SHA         string   `json:"sha"`
	Docstring   string   `json:"docstring"`
	Domain      string   `json:"domain"`
	Layer       string   `json:"layer,omitempty"`
	Exports     []string `json:"exports"`
	ImportPaths []string `json:"import_paths"`
	Level       string   `json:"level"`
	Labels      []string `json:"labels"`
}

// ToProperties converts node to generic map representation.
func (n *L1ModuleNode) ToProperties() map[string]any {
	return map[string]any{
		"id":           n.ID,
		"path":         n.Path,
		"filename":     n.Filename,
		"language":     n.Language,
		"loc":          n.LOC,
		"sha":          n.SHA,
		"docstring":    n.Docstring,
		"domain":       n.Domain,
		"layer":        n.Layer,
		"exports":      n.Exports,
		"import_paths": n.ImportPaths,
		"level":        string(LevelL1),
		"labels":       []string{"L1Module", "Module", "File"},
	}
}

// L2ClassNode represents a class, interface, struct, or type.
// Method bodies are omitted for token economy!
type L2ClassNode struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	FilePath         string   `json:"file_path"`
	Kind             string   `json:"kind"`
	Bases            []string `json:"bases"`
	LineStart        int      `json:"line_start"`
	LineEnd          int      `json:"line_end"`
	Docstring        string   `json:"docstring"`
	IsPublic         bool     `json:"is_public"`
	MethodSignatures []string `json:"method_signatures"`
	FieldSignatures  []string `json:"field_signatures"`
	Level            string   `json:"level"`
	Labels           []string `json:"labels"`
}

// ToProperties converts node to generic map representation.
func (n *L2ClassNode) ToProperties() map[string]any {
	return map[string]any{
		"id":                n.ID,
		"name":              n.Name,
		"file_path":         n.FilePath,
		"kind":              n.Kind,
		"bases":             n.Bases,
		"line_start":        n.LineStart,
		"line_end":          n.LineEnd,
		"docstring":         n.Docstring,
		"is_public":         n.IsPublic,
		"method_signatures": n.MethodSignatures,
		"field_signatures":  n.FieldSignatures,
		"level":             string(LevelL2),
		"labels":            []string{"L2Class", "Class", "Interface"},
	}
}

// L3SymbolNode represents a function or method with full implementation code.
type L3SymbolNode struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	ParentID             string   `json:"parent_id"`
	FilePath             string   `json:"file_path"`
	Kind                 string   `json:"kind"`
	LineStart            int      `json:"line_start"`
	LineEnd              int      `json:"line_end"`
	Signature            string   `json:"signature"`
	ReturnType           string   `json:"return_type,omitempty"`
	Params               []string `json:"params"`
	Docstring            string   `json:"docstring"`
	Code                 string   `json:"code"`
	IsPublic             bool     `json:"is_public"`
	CyclomaticComplexity int      `json:"cyclomatic_complexity"`
	Calls                []string `json:"calls"`
	References           []string `json:"references"`
	Level                string   `json:"level"`
	Labels               []string `json:"labels"`
}

// ToProperties converts node to generic map representation.
func (n *L3SymbolNode) ToProperties() map[string]any {
	return map[string]any{
		"id":                    n.ID,
		"name":                  n.Name,
		"parent_id":             n.ParentID,
		"file_path":             n.FilePath,
		"kind":                  n.Kind,
		"line_start":            n.LineStart,
		"line_end":              n.LineEnd,
		"signature":             n.Signature,
		"return_type":           n.ReturnType,
		"params":                n.Params,
		"docstring":             n.Docstring,
		"code":                  n.Code,
		"is_public":             n.IsPublic,
		"cyclomatic_complexity": n.CyclomaticComplexity,
		"calls":                 n.Calls,
		"references":            n.References,
		"level":                 string(LevelL3),
		"labels":                []string{"L3Symbol", "Symbol", "Function"},
	}
}

// GraphEdge represents a directed relationship in the property graph.
type GraphEdge struct {
	SourceID   string         `json:"source_id"`
	TargetID   string         `json:"target_id"`
	EdgeType   string         `json:"edge_type"`
	Properties map[string]any `json:"properties,omitempty"`
}
