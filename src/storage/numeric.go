package storage

func normalizeNodeProperties(props map[string]any) {
	for key, value := range props {
		props[key] = normalizeNumeric(value)
	}
}

func normalizeNumeric(v any) any {
	switch n := v.(type) {
	case int64:
		return int(n)
	case []any:
		for i := range n {
			n[i] = normalizeNumeric(n[i])
		}
		return n
	case map[string]any:
		for key, value := range n {
			n[key] = normalizeNumeric(value)
		}
		return n
	default:
		return v
	}
}
