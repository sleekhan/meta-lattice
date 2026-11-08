package query

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"meta-lattice/src/models"
	"meta-lattice/src/storage"
)

type GraphQueryOptions struct {
	Level         string `json:"level,omitempty"`
	Kind          string `json:"kind,omitempty"`
	NamePattern   string `json:"name_pattern,omitempty"`
	FilePattern   string `json:"file_pattern,omitempty"`
	MinComplexity int    `json:"min_complexity,omitempty"`
	MaxComplexity int    `json:"max_complexity,omitempty"`
	MinInDegree   *int   `json:"min_in_degree,omitempty"`
	MaxInDegree   *int   `json:"max_in_degree,omitempty"`
	MinOutDegree  *int   `json:"min_out_degree,omitempty"`
	MaxOutDegree  *int   `json:"max_out_degree,omitempty"`
	IsPublic      *bool  `json:"is_public,omitempty"`
	SortBy        string `json:"sort_by,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

type QueryNodeResult struct {
	ID                   string `json:"id"`
	Level                string `json:"level"`
	Name                 string `json:"name"`
	Kind                 string `json:"kind,omitempty"`
	FilePath             string `json:"file_path,omitempty"`
	Signature            string `json:"signature,omitempty"`
	Lines                string `json:"lines,omitempty"`
	IsPublic             bool   `json:"is_public"`
	CyclomaticComplexity int    `json:"cyclomatic_complexity,omitempty"`
	InDegree             int    `json:"in_degree"`
	OutDegree            int    `json:"out_degree"`
	Docstring            string `json:"docstring,omitempty"`
}

type GraphQueryResult struct {
	TotalScanned int               `json:"total_scanned"`
	TotalMatched int               `json:"total_matched"`
	Returned     int               `json:"returned"`
	Query        GraphQueryOptions `json:"query"`
	Nodes        []QueryNodeResult `json:"nodes"`
}

type GraphQueryEngine struct {
	db *storage.GraphStorage
}

func NewGraphQueryEngine(db *storage.GraphStorage) *GraphQueryEngine {
	return &GraphQueryEngine{db: db}
}

func (q *GraphQueryEngine) QueryGraph(opts GraphQueryOptions) GraphQueryResult {
	defer q.db.BeginReadSession()()

	limit := opts.Limit
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}

	var nameReg *regexp.Regexp
	if opts.NamePattern != "" {
		pat := opts.NamePattern
		if !strings.Contains(pat, "^") && !strings.Contains(pat, "$") && !strings.Contains(pat, ".*") {
			pat = "(?i)" + regexp.QuoteMeta(pat)
		} else {
			pat = "(?i)" + pat
		}
		nameReg, _ = regexp.Compile(pat)
	}

	totalScanned := 0
	var candidates []QueryNodeResult

	checkProps := func(id string, props map[string]any) {
		totalScanned++

		lvl, _ := props["level"].(string)
		if opts.Level != "" && !strings.EqualFold(lvl, opts.Level) {
			return
		}

		kind, _ := props["kind"].(string)
		if opts.Kind != "" && !strings.EqualFold(kind, opts.Kind) {
			return
		}

		name, _ := props["name"].(string)
		if name == "" {
			name = id
		}
		if nameReg != nil && !nameReg.MatchString(name) {
			return
		}

		filePath, _ := props["file_path"].(string)
		if filePath == "" {
			filePath, _ = props["path"].(string)
		}
		filePath = strings.ReplaceAll(filePath, "\\", "/")
		if opts.FilePattern != "" {
			pattern := strings.ToLower(opts.FilePattern)
			cleanF := strings.ToLower(filePath)
			if !strings.Contains(cleanF, pattern) {
				matchedGlob, err := filepath.Match(pattern, filepath.Base(cleanF))
				if err != nil || !matchedGlob {
					return
				}
			}
		}

		complexity := toInt(props["cyclomatic_complexity"])
		if opts.MinComplexity > 0 && complexity < opts.MinComplexity {
			return
		}
		if opts.MaxComplexity > 0 && complexity > opts.MaxComplexity {
			return
		}

		isPub, _ := props["is_public"].(bool)
		if opts.IsPublic != nil && isPub != *opts.IsPublic {
			return
		}

		sig, _ := props["signature"].(string)
		doc, _ := props["docstring"].(string)
		if len(doc) > 100 {
			doc = doc[:100] + "..."
		}

		start := toInt(props["line_start"])
		end := toInt(props["line_end"])
		linesStr := ""
		if start > 0 && end > 0 {
			linesStr = strconv.Itoa(start) + "-" + strconv.Itoa(end)
		}

		candidates = append(candidates, QueryNodeResult{
			ID:                   id,
			Level:                lvl,
			Name:                 name,
			Kind:                 kind,
			FilePath:             filePath,
			Signature:            sig,
			Lines:                linesStr,
			IsPublic:             isPub,
			CyclomaticComplexity: complexity,
			Docstring:            doc,
		})
	}

	if normLvl := normalizeLevel(opts.Level); normLvl != "" {
		for _, props := range q.db.QueryNodes(normLvl, "", 0) {
			id, _ := props["id"].(string)
			checkProps(id, props)
		}
	} else {
		q.db.RangeNodes(checkProps)
	}

	var matched []QueryNodeResult
	for _, cand := range candidates {
		inEdges := q.db.GetIncomingEdges(cand.ID, "")
		outEdges := q.db.GetOutgoingEdges(cand.ID, "")

		inCalls := 0
		for _, e := range inEdges {
			if e.EdgeType != string(models.EdgeContains) {
				inCalls++
			}
		}
		outCalls := 0
		for _, e := range outEdges {
			if e.EdgeType != string(models.EdgeContains) {
				outCalls++
			}
		}

		if opts.MinInDegree != nil && inCalls < *opts.MinInDegree {
			continue
		}
		if opts.MaxInDegree != nil && inCalls > *opts.MaxInDegree {
			continue
		}
		if opts.MinOutDegree != nil && outCalls < *opts.MinOutDegree {
			continue
		}
		if opts.MaxOutDegree != nil && outCalls > *opts.MaxOutDegree {
			continue
		}

		cand.InDegree = inCalls
		cand.OutDegree = outCalls
		matched = append(matched, cand)
	}

	// Sorting
	sortBy := strings.ToLower(opts.SortBy)
	switch sortBy {
	case "complexity", "cyclomatic_complexity":
		sort.Slice(matched, func(i, j int) bool {
			if matched[i].CyclomaticComplexity != matched[j].CyclomaticComplexity {
				return matched[i].CyclomaticComplexity > matched[j].CyclomaticComplexity
			}
			return matched[i].ID < matched[j].ID
		})
	case "in_degree", "hotspot":
		sort.Slice(matched, func(i, j int) bool {
			if matched[i].InDegree != matched[j].InDegree {
				return matched[i].InDegree > matched[j].InDegree
			}
			return matched[i].ID < matched[j].ID
		})
	case "out_degree":
		sort.Slice(matched, func(i, j int) bool {
			if matched[i].OutDegree != matched[j].OutDegree {
				return matched[i].OutDegree > matched[j].OutDegree
			}
			return matched[i].ID < matched[j].ID
		})
	default:
		sort.Slice(matched, func(i, j int) bool {
			if matched[i].InDegree != matched[j].InDegree {
				return matched[i].InDegree > matched[j].InDegree
			}
			if matched[i].Name != matched[j].Name {
				return matched[i].Name < matched[j].Name
			}
			return matched[i].ID < matched[j].ID
		})
	}

	totalMatched := len(matched)
	if len(matched) > limit {
		matched = matched[:limit]
	}

	return GraphQueryResult{
		TotalScanned: totalScanned,
		TotalMatched: totalMatched,
		Returned:     len(matched),
		Query:        opts,
		Nodes:        matched,
	}
}

func normalizeLevel(lvl string) string {
	switch strings.ToUpper(strings.TrimSpace(lvl)) {
	case "L0":
		return string(models.LevelL0)
	case "L1":
		return string(models.LevelL1)
	case "L2":
		return string(models.LevelL2)
	case "L3":
		return string(models.LevelL3)
	case "ROUTE":
		return "Route"
	default:
		return ""
	}
}

func toInt(v any) int {
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	}
	return 0
}
