package storage

import "testing"

func TestNormalizeNodeProperties(t *testing.T) {
	props := map[string]any{
		"loc":   int64(12),
		"lines": []any{int64(3), int64(7)},
	}
	normalizeNodeProperties(props)
	if got, ok := props["loc"].(int); !ok || got != 12 {
		t.Fatalf("loc = %v (%T), want int(12)", props["loc"], props["loc"])
	}
	lines := props["lines"].([]any)
	if got, ok := lines[0].(int); !ok || got != 3 {
		t.Fatalf("line_start = %v (%T), want int(3)", lines[0], lines[0])
	}
}
