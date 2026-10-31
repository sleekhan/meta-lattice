package storage

import (
	"bytes"
	"encoding/json"
)

func unmarshalPropsJSON(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var props map[string]any
	if err := dec.Decode(&props); err != nil {
		return nil, err
	}
	normalizeNodeProperties(props)
	return props, nil
}

func normalizeNodeProperties(props map[string]any) {
	for key, value := range props {
		props[key] = normalizeNumeric(value)
	}
}

func normalizeNumeric(v any) any {
	switch n := v.(type) {
	case int64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
		if f, err := n.Float64(); err == nil {
			return f
		}
		return n.String()
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
