package indexer

import (
	"regexp"
	"strings"
)

// Helpers for Single-File Components (Vue SFC, Svelte): framework files wrap
// one or more <script> blocks and a <template> around plain TS/JS. The
// script contents are parsed with the regular TS/JS parser; template
// component tags become lightweight dependency hints.

var (
	sfcScriptOpenRegex  = regexp.MustCompile(`(?i)<script[^>]*>`)
	sfcScriptCloseRegex = regexp.MustCompile(`(?i)</script>`)
	sfcTemplateRegex    = regexp.MustCompile(`(?is)<template[^>]*>(.*?)</template>`)
	sfcStyleRegex       = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	sfcTagRegex         = regexp.MustCompile(`<([A-Za-z][A-Za-z0-9_-]*)`)
	sfcNameRegex        = regexp.MustCompile(`name\s*:\s*["']([a-zA-Z0-9_]+)["']`)
)

// extractSFCScripts returns the concatenated contents of all <script> blocks
// (plain, setup, and module-context alike) in document order.
func extractSFCScripts(code string) string {
	var b strings.Builder
	rest := code
	for {
		open := sfcScriptOpenRegex.FindStringIndex(rest)
		if open == nil {
			break
		}
		after := rest[open[1]:]
		closeIdx := sfcScriptCloseRegex.FindStringIndex(after)
		if closeIdx == nil {
			break
		}
		b.WriteString(after[:closeIdx[0]])
		b.WriteString("\n")
		rest = after[closeIdx[1]:]
	}
	return b.String()
}

// extractSFCTemplateRefs returns PascalCase/kebab component tags used inside
// <template> (e.g. UserCard in <UserCard />), excluding native HTML elements.
// Files without a <template> wrapper (Svelte) fall back to scanning the
// document outside <script>/<style> blocks. Kebab-case names (my-widget)
// are converted to PascalCase (MyWidget) to match imported symbol names.
func extractSFCTemplateRefs(code string) []string {
	markup := ""
	if m := sfcTemplateRegex.FindStringSubmatch(code); len(m) >= 2 {
		markup = m[1]
	} else {
		markup = sfcScriptOpenRegex.ReplaceAllString(code, "\x00")
		markup = sfcScriptCloseRegex.ReplaceAllString(markup, "\x00")
		parts := strings.Split(markup, "\x00")
		var kept []string
		for idx, part := range parts {
			if idx%2 == 0 {
				kept = append(kept, part)
			}
		}
		markup = sfcStyleRegex.ReplaceAllString(strings.Join(kept, "\n"), "")
	}
	seen := make(map[string]bool)
	var refs []string
	for _, tm := range sfcTagRegex.FindAllStringSubmatch(markup, -1) {
		tag := tm[1]
		if strings.HasPrefix(tag, "/") {
			continue
		}
		name := tag
		if strings.Contains(tag, "-") {
			var b strings.Builder
			for _, part := range strings.Split(tag, "-") {
				if part != "" {
					b.WriteString(strings.ToUpper(part[:1]) + part[1:])
				}
			}
			name = b.String()
		}
		if name == "" || name[0] < 'A' || name[0] > 'Z' {
			continue
		}
		if !seen[name] {
			seen[name] = true
			refs = append(refs, name)
		}
	}
	return refs
}

// sfcComponentName derives the component name from an explicit `name:`
// option in script, falling back to the file stem.
func sfcComponentName(script, fileStem string) string {
	if m := sfcNameRegex.FindStringSubmatch(script); len(m) > 1 {
		return m[1]
	}
	return fileStem
}
