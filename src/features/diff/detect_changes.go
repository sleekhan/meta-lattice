package diff

import (
	"bytes"
	"math"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"meta-lattice/src/config"
	"meta-lattice/src/models"
	"meta-lattice/src/storage"
)

type LineSpan struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type ChangedFile struct {
	FilePath string     `json:"file_path"`
	Spans    []LineSpan `json:"spans"`
}

type ChangedSymbol struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	FilePath  string `json:"file_path"`
	Kind      string `json:"kind"`
	Signature string `json:"signature,omitempty"`
	Lines     string `json:"lines"`
	IsPublic  bool   `json:"is_public"`
}

type AffectedTest struct {
	TestFile     string `json:"test_file"`
	TestSymbol   string `json:"test_symbol"`
	TargetSymbol string `json:"target_symbol"`
	Distance     int    `json:"distance"`
}

type ImpactedSymbol struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	FilePath  string `json:"file_path"`
	Signature string `json:"signature,omitempty"`
	Distance  int    `json:"distance"`
}

type ChangeDetectionResult struct {
	BaseRef         string           `json:"base_ref"`
	Scope           string           `json:"scope"`
	RiskTier        string           `json:"risk_tier"`
	BlastScore      int              `json:"blast_score"`
	ChangedFiles    []ChangedFile    `json:"changed_files"`
	ChangedSymbols  []ChangedSymbol  `json:"changed_symbols"`
	AffectedSymbols []ImpactedSymbol `json:"affected_symbols"`
	AffectedTests   []AffectedTest   `json:"affected_tests"`
	Summary         string           `json:"summary"`
	Error           string           `json:"error,omitempty"`
}

type ChangeDetector struct {
	cfg *config.LatticeConfig
	db  *storage.GraphStorage
}

func NewChangeDetector(cfg *config.LatticeConfig, db *storage.GraphStorage) *ChangeDetector {
	return &ChangeDetector{
		cfg: cfg,
		db:  db,
	}
}

var hunkRegex = regexp.MustCompile(`^@@\s+-[0-9]+(?:,[0-9]+)?\s+\+([0-9]+)(?:,([0-9]+))?\s+@@`)

func ParseGitDiff(diffText string) []ChangedFile {
	var files []ChangedFile
	lines := strings.Split(diffText, "\n")
	var currentFile string
	var currentSpans []LineSpan

	flushCurrent := func() {
		if currentFile != "" && len(currentSpans) > 0 {
			files = append(files, ChangedFile{
				FilePath: currentFile,
				Spans:    currentSpans,
			})
		}
		currentFile = ""
		currentSpans = nil
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			flushCurrent()
			continue
		}
		if strings.HasPrefix(line, "+++ b/") {
			currentFile = strings.TrimPrefix(line, "+++ b/")
			currentFile = strings.TrimSpace(strings.ReplaceAll(currentFile, "\\", "/"))
			continue
		}
		if strings.HasPrefix(line, "+++ /dev/null") {
			// Deleted file
			currentFile = ""
			continue
		}
		if strings.HasPrefix(line, "@@") && currentFile != "" {
			matches := hunkRegex.FindStringSubmatch(line)
			if len(matches) >= 2 {
				startLine, err := strconv.Atoi(matches[1])
				if err != nil {
					continue
				}
				count := 1
				if len(matches) >= 3 && matches[2] != "" {
					if c, err := strconv.Atoi(matches[2]); err == nil {
						count = c
					}
				}
				endLine := startLine + count - 1
				if count == 0 {
					endLine = startLine
				}
				currentSpans = append(currentSpans, LineSpan{
					Start: startLine,
					End:   endLine,
				})
			}
		}
	}
	flushCurrent()
	return files
}

func (d *ChangeDetector) getGitDiffOutput(baseRef string) (string, error) {
	wsRoot := d.cfg.WorkspaceRoot
	var cmd *exec.Cmd
	if baseRef != "" {
		cmd = exec.Command("git", "diff", "-U0", baseRef)
	} else {
		// Try diff against HEAD first
		cmd = exec.Command("git", "diff", "-U0", "HEAD")
	}
	cmd.Dir = wsRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil && baseRef == "" {
		// Fallback for empty repo / before initial commit
		fallbackCmd := exec.Command("git", "diff", "-U0")
		fallbackCmd.Dir = wsRoot
		var fbOut bytes.Buffer
		fallbackCmd.Stdout = &fbOut
		if fbErr := fallbackCmd.Run(); fbErr == nil {
			return fbOut.String(), nil
		}
	}
	if err != nil {
		return "", err
	}
	return stdout.String(), nil
}

func isTestFile(pathStr string) bool {
	p := strings.ToLower(strings.ReplaceAll(pathStr, "\\", "/"))
	base := filepath.Base(p)
	if strings.HasSuffix(p, "_test.go") || strings.HasSuffix(p, ".test.ts") || strings.HasSuffix(p, ".test.js") ||
		strings.HasSuffix(p, ".spec.ts") || strings.HasSuffix(p, ".spec.js") || strings.HasPrefix(base, "test_") ||
		strings.Contains(p, "/tests/") || strings.Contains(p, "/test/") || strings.Contains(p, "__tests__") {
		return true
	}
	return false
}

// DetectChanges analyzes git diff, maps modified lines to AST symbols, and calculates impact on symbols & tests.
func (d *ChangeDetector) DetectChanges(baseRef string, scope string, maxHops int, rawDiffOverride string) ChangeDetectionResult {
	defer d.db.BeginReadSession()()

	if maxHops <= 0 {
		maxHops = 3
	}
	if maxHops > 8 {
		maxHops = 8
	}
	scope = strings.ToLower(scope)
	if scope == "" {
		scope = "all"
	}

	res := ChangeDetectionResult{
		BaseRef: baseRef,
		Scope:   scope,
	}

	diffText := rawDiffOverride
	if diffText == "" {
		out, err := d.getGitDiffOutput(baseRef)
		if err != nil {
			res.Error = "Git diff failed: " + err.Error()
			return res
		}
		diffText = out
	}

	changedFiles := ParseGitDiff(diffText)
	res.ChangedFiles = changedFiles

	if len(changedFiles) == 0 {
		res.Summary = "No changes detected in git diff."
		res.RiskTier = "LOW"
		return res
	}

	// Match modified line spans with AST nodes (L3 symbols and L2 classes)
	changedSymMap := make(map[string]ChangedSymbol)

	for _, cf := range changedFiles {
		cleanPath := strings.TrimPrefix(cf.FilePath, "./")
		fileID := "file:" + cleanPath

		// Find children of this file (including L3 methods nested under L2 classes)
		outgoing := d.db.GetOutgoingEdges(fileID, string(models.EdgeContains))
		var candidates []map[string]any
		for _, e := range outgoing {
			child, ok := d.db.GetNode(e.TargetID)
			if !ok {
				continue
			}
			candidates = append(candidates, child)
			if lvl, _ := child["level"].(string); lvl == string(models.LevelL2) {
				for _, subE := range d.db.GetOutgoingEdges(e.TargetID, string(models.EdgeContains)) {
					if subChild, subOk := d.db.GetNode(subE.TargetID); subOk {
						candidates = append(candidates, subChild)
					}
				}
			}
		}

		for _, child := range candidates {
			if lvl, _ := child["level"].(string); lvl == "Route" {
				continue
			}
			start := toInt(child["line_start"])
			end := toInt(child["line_end"])
			if start == 0 && end == 0 {
				continue
			}

			// Check span overlap
			overlaps := false
			for _, span := range cf.Spans {
				if span.Start <= end && span.End >= start {
					overlaps = true
					break
				}
			}

			if overlaps {
				id, _ := child["id"].(string)
				name, _ := child["name"].(string)
				kind, _ := child["kind"].(string)
				sig, _ := child["signature"].(string)
				isPub, _ := child["is_public"].(bool)
				linesStr := strconv.Itoa(start) + "-" + strconv.Itoa(end)

				changedSymMap[id] = ChangedSymbol{
					ID:        id,
					Name:      name,
					FilePath:  cleanPath,
					Kind:      kind,
					Signature: sig,
					Lines:     linesStr,
					IsPublic:  isPub,
				}
			}
		}
	}

	for _, s := range changedSymMap {
		res.ChangedSymbols = append(res.ChangedSymbols, s)
	}
	sort.Slice(res.ChangedSymbols, func(i, j int) bool {
		return res.ChangedSymbols[i].Name < res.ChangedSymbols[j].Name
	})

	// BFS traversal for incoming callers (impact ripple)
	type bfsItem struct {
		nodeID       string
		targetOrigin string
		dist         int
	}

	var queue []bfsItem
	visited := make(map[string]bool)

	for _, s := range res.ChangedSymbols {
		queue = append(queue, bfsItem{
			nodeID:       s.ID,
			targetOrigin: s.Name,
			dist:         0,
		})
		visited[s.ID] = true
	}

	affectedSymMap := make(map[string]ImpactedSymbol)
	affectedTestMap := make(map[string]AffectedTest)

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		if curr.dist >= maxHops {
			continue
		}

		inEdges := d.db.GetIncomingEdges(curr.nodeID, "")
		for _, edge := range inEdges {
			if edge.EdgeType != string(models.EdgeCalls) && edge.EdgeType != string(models.EdgeImports) && edge.EdgeType != string(models.EdgeReferences) {
				continue
			}
			callerID := edge.SourceID
			if visited[callerID] {
				continue
			}
			visited[callerID] = true

			callerNode, exists := d.db.GetNode(callerID)
			if !exists {
				continue
			}

			cName, _ := callerNode["name"].(string)
			cFile, _ := callerNode["file_path"].(string)
			if cFile == "" {
				cFile, _ = callerNode["path"].(string)
			}
			cFile = strings.ReplaceAll(cFile, "\\", "/")
			cSig, _ := callerNode["signature"].(string)
			nextDist := curr.dist + 1

			if isTestFile(cFile) {
				key := cFile + ":" + cName
				if _, ok := affectedTestMap[key]; !ok {
					affectedTestMap[key] = AffectedTest{
						TestFile:     cFile,
						TestSymbol:   cName,
						TargetSymbol: curr.targetOrigin,
						Distance:     nextDist,
					}
				}
			} else {
				if _, ok := affectedSymMap[callerID]; !ok {
					affectedSymMap[callerID] = ImpactedSymbol{
						ID:        callerID,
						Name:      cName,
						FilePath:  cFile,
						Signature: cSig,
						Distance:  nextDist,
					}
				}
			}

			queue = append(queue, bfsItem{
				nodeID:       callerID,
				targetOrigin: curr.targetOrigin,
				dist:         nextDist,
			})
		}
	}

	if scope != "tests" {
		for _, a := range affectedSymMap {
			res.AffectedSymbols = append(res.AffectedSymbols, a)
		}
		sort.Slice(res.AffectedSymbols, func(i, j int) bool {
			if res.AffectedSymbols[i].Distance != res.AffectedSymbols[j].Distance {
				return res.AffectedSymbols[i].Distance < res.AffectedSymbols[j].Distance
			}
			return res.AffectedSymbols[i].Name < res.AffectedSymbols[j].Name
		})
	}

	if scope != "symbols" {
		for _, t := range affectedTestMap {
			res.AffectedTests = append(res.AffectedTests, t)
		}
		sort.Slice(res.AffectedTests, func(i, j int) bool {
			if res.AffectedTests[i].Distance != res.AffectedTests[j].Distance {
				return res.AffectedTests[i].Distance < res.AffectedTests[j].Distance
			}
			if res.AffectedTests[i].TestFile != res.AffectedTests[j].TestFile {
				return res.AffectedTests[i].TestFile < res.AffectedTests[j].TestFile
			}
			return res.AffectedTests[i].TestSymbol < res.AffectedTests[j].TestSymbol
		})
	}

	// Calculate Blast Score & Risk Tier
	hasPublicChange := false
	for _, s := range res.ChangedSymbols {
		if s.IsPublic {
			hasPublicChange = true
			break
		}
	}

	rawScore := float64(len(res.ChangedSymbols)*10 + len(affectedSymMap)*4)
	if hasPublicChange {
		rawScore *= 1.5
	}
	if rawScore > 100 {
		rawScore = 100
	}
	res.BlastScore = int(math.Round(rawScore))

	if res.BlastScore >= 75 {
		res.RiskTier = "CRITICAL"
	} else if res.BlastScore >= 50 {
		res.RiskTier = "HIGH"
	} else if res.BlastScore >= 20 {
		res.RiskTier = "MODERATE"
	} else {
		res.RiskTier = "LOW"
	}

	res.Summary = "Detected " + strconv.Itoa(len(res.ChangedFiles)) + " modified files, " +
		strconv.Itoa(len(res.ChangedSymbols)) + " directly touched symbols, " +
		strconv.Itoa(len(res.AffectedSymbols)) + " impacted downstream symbols, and " +
		strconv.Itoa(len(res.AffectedTests)) + " affected test targets."

	return res
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
