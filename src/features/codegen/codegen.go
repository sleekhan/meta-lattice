package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"meta-lattice/src/indexer"
)

// MaxFileBytes caps generated file content (1 MiB) to prevent runaway writes.
const MaxFileBytes = 1 << 20

// Codegen creates new modules and applies multi-file edit plans inside a
// workspace. Every path is confined to the workspace root: absolute paths
// and ".." escapes are rejected, and only indexed source extensions are
// writable.
type Codegen struct {
	WorkspaceRoot string
	MaxBytes      int
}

func NewCodegen(workspaceRoot string) *Codegen {
	abs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		abs = workspaceRoot
	}
	return &Codegen{WorkspaceRoot: abs, MaxBytes: MaxFileBytes}
}

func (c *Codegen) resolvePath(relPath string) (string, string, error) {
	raw := strings.TrimSpace(relPath)
	if raw == "" {
		return "", "", fmt.Errorf("empty path")
	}
	// Reject ".." segments explicitly instead of silently neutralizing them,
	// so traversal attempts are visible to the caller rather than rewritten.
	if filepath.IsAbs(raw) {
		return "", "", fmt.Errorf("path %q escapes workspace", relPath)
	}
	for _, seg := range strings.Split(filepath.ToSlash(raw), "/") {
		if seg == ".." {
			return "", "", fmt.Errorf("path %q escapes workspace", relPath)
		}
	}
	clean := filepath.ToSlash(filepath.Clean("/" + raw))
	rel := strings.TrimPrefix(clean, "/")
	if rel == "" || rel == "." {
		return "", "", fmt.Errorf("empty path")
	}
	abs := filepath.Join(c.WorkspaceRoot, filepath.FromSlash(rel))
	root := c.WorkspaceRoot + string(os.PathSeparator)
	if abs != c.WorkspaceRoot && !strings.HasPrefix(abs, root) {
		return "", "", fmt.Errorf("path %q escapes workspace", relPath)
	}
	ext := strings.ToLower(filepath.Ext(rel))
	if _, ok := indexer.SupportedExtensions[ext]; !ok {
		return "", "", fmt.Errorf("extension %q is not an indexed source file", ext)
	}
	return abs, rel, nil
}

func languageOf(rel string) string {
	ext := strings.ToLower(filepath.Ext(rel))
	if lang, ok := indexer.SupportedExtensions[ext]; ok {
		return lang
	}
	return ""
}

// ScaffoldOptions describes a new module file to generate.
type ScaffoldOptions struct {
	FilePath  string
	Kind      string // class, interface, struct, enum, module
	Name      string
	Namespace string   // package / namespace declaration (optional)
	Imports   []string // module specs, one per line in file syntax
	Overwrite bool
}

// ScaffoldModule writes a new source file from a per-language template.
// It never overwrites an existing file unless Overwrite is true, and the
// caller is expected to run sync_index afterwards (the MCP server does).
func (c *Codegen) ScaffoldModule(opts ScaffoldOptions) (map[string]any, error) {
	abs, rel, err := c.resolvePath(opts.FilePath)
	if err != nil {
		return nil, err
	}
	if _, statErr := os.Stat(abs); statErr == nil && !opts.Overwrite {
		return nil, fmt.Errorf("file %q already exists (pass overwrite to replace)", rel)
	}

	lang := languageOf(rel)
	kind := strings.ToLower(strings.TrimSpace(opts.Kind))
	if kind == "" {
		kind = "module"
	}
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
	}

	content := renderTemplate(lang, kind, name, strings.TrimSpace(opts.Namespace), opts.Imports)
	if len(content) > c.MaxBytes {
		return nil, fmt.Errorf("generated content exceeds %d bytes", c.MaxBytes)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
		return nil, err
	}

	return map[string]any{
		"created":   rel,
		"language":  lang,
		"kind":      kind,
		"name":      name,
		"bytes":     len(content),
		"guidance":  "Run `sync_index` to index the new module, then `zoom_module` to verify it.",
	}, nil
}

// PlanOperation is a single file mutation inside an apply_plan batch.
type PlanOperation struct {
	Op         string `json:"op"` // create_file | replace_text | insert_after | delete_file
	Path       string `json:"path"`
	Content    string `json:"content,omitempty"`
	OldText    string `json:"old_text,omitempty"`
	NewText    string `json:"new_text,omitempty"`
	Anchor     string `json:"anchor,omitempty"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
	Overwrite  bool   `json:"overwrite,omitempty"`
}

type opResult struct {
	Op     string `json:"op"`
	Path   string `json:"path"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type undoEntry struct {
	abs     string
	existed bool
	content []byte
}

// ApplyPlan validates and executes a batch of file operations. With dryRun
// nothing is written. On the first failure all previously applied operations
// are rolled back in reverse order, so the workspace is never left
// half-edited.
func (c *Codegen) ApplyPlan(ops []PlanOperation, dryRun bool) (map[string]any, error) {
	if len(ops) == 0 {
		return nil, fmt.Errorf("plan has no operations")
	}

	type prepared struct {
		op  PlanOperation
		abs string
		rel string
	}
	var plan []prepared
	for i, op := range ops {
		if op.Op == "" || op.Path == "" {
			return nil, fmt.Errorf("operation %d: missing op or path", i)
		}
		abs, rel, err := c.resolvePath(op.Path)
		if err != nil {
			return nil, fmt.Errorf("operation %d: %w", i, err)
		}
		if op.Op == "create_file" && len(op.Content) > c.MaxBytes {
			return nil, fmt.Errorf("operation %d: content exceeds %d bytes", i, c.MaxBytes)
		}
		plan = append(plan, prepared{op: op, abs: abs, rel: rel})
	}

	results := make([]opResult, 0, len(plan))
	var undos []undoEntry

	rollback := func() {
		for i := len(undos) - 1; i >= 0; i-- {
			u := undos[i]
			if !u.existed {
				_ = os.Remove(u.abs)
				continue
			}
			_ = os.WriteFile(u.abs, u.content, 0644)
		}
	}

	fail := func(idx int, format string, args ...any) (map[string]any, error) {
		rollback()
		for j := range results {
			results[j].OK = false
		}
		return map[string]any{
			"applied":  0,
			"dry_run":  dryRun,
			"failed":   idx,
			"error":    fmt.Sprintf(format, args...),
			"results":  results,
			"rollback": true,
		}, fmt.Errorf(format, args...)
	}

	for i, p := range plan {
		switch p.op.Op {
		case "create_file":
			if _, err := os.Stat(p.abs); err == nil && !p.op.Overwrite {
				return fail(i, "operation %d: %q already exists", i, p.rel)
			}
			detail := fmt.Sprintf("would create %d bytes", len(p.op.Content))
			if !dryRun {
				orig, statErr := os.ReadFile(p.abs)
				existed := statErr == nil
				if err := os.MkdirAll(filepath.Dir(p.abs), 0755); err != nil {
					return fail(i, "operation %d: %v", i, err)
				}
				if err := os.WriteFile(p.abs, []byte(p.op.Content), 0644); err != nil {
					return fail(i, "operation %d: %v", i, err)
				}
				undos = append(undos, undoEntry{abs: p.abs, existed: existed, content: orig})
				detail = fmt.Sprintf("created %d bytes", len(p.op.Content))
			}
			results = append(results, opResult{Op: p.op.Op, Path: p.rel, OK: true, Detail: detail})

		case "replace_text":
			orig, err := os.ReadFile(p.abs)
			if err != nil {
				return fail(i, "operation %d: cannot read %q", i, p.rel)
			}
			count := strings.Count(string(orig), p.op.OldText)
			if p.op.OldText == "" || count == 0 {
				return fail(i, "operation %d: old_text not found in %q", i, p.rel)
			}
			if !p.op.ReplaceAll && count > 1 {
				return fail(i, "operation %d: old_text matches %d times in %q (set replace_all or narrow it)", i, count, p.rel)
			}
			updated := strings.Replace(string(orig), p.op.OldText, p.op.NewText, -1)
			if !p.op.ReplaceAll && count > 1 {
				updated = strings.Replace(string(orig), p.op.OldText, p.op.NewText, 1)
			}
			detail := fmt.Sprintf("would replace %d occurrence(s)", count)
			if !dryRun {
				if err := os.WriteFile(p.abs, []byte(updated), 0644); err != nil {
					return fail(i, "operation %d: %v", i, err)
				}
				undos = append(undos, undoEntry{abs: p.abs, existed: true, content: orig})
				detail = fmt.Sprintf("replaced %d occurrence(s)", count)
			}
			results = append(results, opResult{Op: p.op.Op, Path: p.rel, OK: true, Detail: detail})

		case "insert_after":
			orig, err := os.ReadFile(p.abs)
			if err != nil {
				return fail(i, "operation %d: cannot read %q", i, p.rel)
			}
			idx := strings.Index(string(orig), p.op.Anchor)
			if p.op.Anchor == "" || idx < 0 {
				return fail(i, "operation %d: anchor not found in %q", i, p.rel)
			}
			updated := string(orig)[:idx+len(p.op.Anchor)] + p.op.Content + string(orig)[idx+len(p.op.Anchor):]
			detail := "would insert content after anchor"
			if !dryRun {
				if err := os.WriteFile(p.abs, []byte(updated), 0644); err != nil {
					return fail(i, "operation %d: %v", i, err)
				}
				undos = append(undos, undoEntry{abs: p.abs, existed: true, content: orig})
				detail = "inserted content after anchor"
			}
			results = append(results, opResult{Op: p.op.Op, Path: p.rel, OK: true, Detail: detail})

		case "delete_file":
			orig, err := os.ReadFile(p.abs)
			if err != nil {
				return fail(i, "operation %d: cannot read %q", i, p.rel)
			}
			detail := "would delete file"
			if !dryRun {
				if err := os.Remove(p.abs); err != nil {
					return fail(i, "operation %d: %v", i, err)
				}
				undos = append(undos, undoEntry{abs: p.abs, existed: true, content: orig})
				detail = "deleted file"
			}
			results = append(results, opResult{Op: p.op.Op, Path: p.rel, OK: true, Detail: detail})

		default:
			return fail(i, "operation %d: unknown op %q", i, p.op.Op)
		}
	}

	return map[string]any{
		"applied": len(results),
		"dry_run": dryRun,
		"results": results,
		"guidance": func() string {
			if dryRun {
				return "Dry run: no files were modified. Re-run with dry_run=false to apply."
			}
			return "Run `sync_index` to refresh the graph, then `check_layer_violation` on touched files."
		}(),
	}, nil
}

