package models

import (
	"sort"
	"testing"
)

// TestToPropertiesKeySetsAreFixed pins the property keys each node kind writes.
// Incremental persistence overwrites properties instead of replacing the node,
// and LatticeDB has no way to remove a property, so a key that disappears from
// ToProperties would silently keep its stale value in the database file. If this
// test fails because a key was intentionally added or removed, bump
// indexer.CacheVersion so the graph is re-indexed from scratch.
func TestToPropertiesKeySetsAreFixed(t *testing.T) {
	cases := []struct {
		name string
		got  map[string]any
		want []string
	}{
		{
			name: "L0Domain",
			got:  (&L0DomainNode{}).ToProperties(),
			want: []string{"id", "name", "path", "description", "layer", "module_count", "level", "labels"},
		},
		{
			name: "L1Module",
			got:  (&L1ModuleNode{}).ToProperties(),
			want: []string{
				"id", "path", "filename", "language", "loc", "sha", "docstring",
				"domain", "layer", "exports", "import_paths", "level", "labels",
			},
		},
		{
			name: "L2Class",
			got:  (&L2ClassNode{}).ToProperties(),
			want: []string{
				"id", "name", "file_path", "kind", "bases", "line_start", "line_end",
				"docstring", "is_public", "method_signatures", "field_signatures",
				"level", "labels",
			},
		},
		{
			name: "L3Symbol",
			got:  (&L3SymbolNode{}).ToProperties(),
			want: []string{
				"id", "name", "parent_id", "file_path", "kind", "line_start",
				"line_end", "signature", "return_type", "params", "docstring",
				"code", "is_public", "cyclomatic_complexity", "calls", "references",
				"level", "labels",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := make([]string, 0, len(tc.got))
			for key := range tc.got {
				got = append(got, key)
			}
			sort.Strings(got)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)

			if len(got) != len(want) {
				t.Fatalf("%s writes %d keys, expected %d\n got: %v\nwant: %v", tc.name, len(got), len(want), got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("%s key set changed\n got: %v\nwant: %v", tc.name, got, want)
				}
			}
		})
	}
}
