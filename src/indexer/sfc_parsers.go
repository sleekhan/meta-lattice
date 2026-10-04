package indexer

import (
	"fmt"
	"path"
	"strings"

	"meta-lattice/src/models"
)

// ParseVueFile parses a Vue Single-File Component (.vue): script blocks are
// analyzed with the TS/JS parser, an L2 component node is synthesized, and
// template component tags are recorded as file exports so template-driven
// dependencies participate in edge resolution.
func ParseVueFile(relPath string, code string, sha string, domain string) *ParsedFileResult {
	script := extractSFCScripts(code)
	res := ParseTSJSFile(relPath, script, sha, domain)
	if res == nil {
		return nil
	}

	stem := strings.TrimSuffix(path.Base(relPath), path.Ext(relPath))
	name := sfcComponentName(script, stem)

	res.FileNode.Language = "vue"
	res.FileNode.Docstring = fmt.Sprintf("Vue component %s in %s", name, relPath)

	compID := fmt.Sprintf("class:%s:%s", relPath, name)
	var sigs []string
	for _, sym := range res.SymbolNodes {
		if sym.ParentID == res.FileNode.ID && sym.Signature != "" {
			sigs = append(sigs, sym.Signature)
		}
	}
	res.ClassNodes = append([]models.L2ClassNode{{
		ID:               compID,
		Name:             name,
		FilePath:         relPath,
		Kind:             "component",
		IsPublic:         true,
		Docstring:        fmt.Sprintf("Vue component %s in %s", name, relPath),
		MethodSignatures: sigs,
		Level:            string(models.LevelL2),
		Labels:           []string{"L2Class", "Class", "Component"},
	}}, res.ClassNodes...)

	for _, ref := range extractSFCTemplateRefs(code) {
		res.FileNode.Exports = append(res.FileNode.Exports, ref)
	}
	res.FileNode.Exports = uniqueStrings(res.FileNode.Exports)

	return res
}

// ParseSvelteFile parses a Svelte component (.svelte) the same way:
// script blocks feed the TS/JS parser, an L2 component node is synthesized,
// and template component tags join the file exports.
func ParseSvelteFile(relPath string, code string, sha string, domain string) *ParsedFileResult {
	script := extractSFCScripts(code)
	res := ParseTSJSFile(relPath, script, sha, domain)
	if res == nil {
		return nil
	}

	stem := strings.TrimSuffix(path.Base(relPath), path.Ext(relPath))
	name := sfcComponentName(script, stem)

	res.FileNode.Language = "svelte"
	res.FileNode.Docstring = fmt.Sprintf("Svelte component %s in %s", name, relPath)

	compID := fmt.Sprintf("class:%s:%s", relPath, name)
	var sigs []string
	for _, sym := range res.SymbolNodes {
		if sym.ParentID == res.FileNode.ID && sym.Signature != "" {
			sigs = append(sigs, sym.Signature)
		}
	}
	res.ClassNodes = append([]models.L2ClassNode{{
		ID:               compID,
		Name:             name,
		FilePath:         relPath,
		Kind:             "component",
		IsPublic:         true,
		Docstring:        fmt.Sprintf("Svelte component %s in %s", name, relPath),
		MethodSignatures: sigs,
		Level:            string(models.LevelL2),
		Labels:           []string{"L2Class", "Class", "Component"},
	}}, res.ClassNodes...)

	for _, ref := range extractSFCTemplateRefs(code) {
		res.FileNode.Exports = append(res.FileNode.Exports, ref)
	}
	res.FileNode.Exports = uniqueStrings(res.FileNode.Exports)

	return res
}
