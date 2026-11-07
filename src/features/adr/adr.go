package adr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"meta-lattice/src/config"
)

type ADRStatus string

const (
	StatusProposed   ADRStatus = "proposed"
	StatusAccepted   ADRStatus = "accepted"
	StatusDeprecated ADRStatus = "deprecated"
	StatusSuperseded ADRStatus = "superseded"
)

type ADR struct {
	ID           string    `json:"id"`
	Number       int       `json:"number"`
	Title        string    `json:"title"`
	Status       ADRStatus `json:"status"`
	Date         string    `json:"date"`
	Context      string    `json:"context"`
	Decision     string    `json:"decision"`
	Consequences string    `json:"consequences"`
	Tags         []string  `json:"tags,omitempty"`
	SupersededBy string    `json:"superseded_by,omitempty"`
	UpdatedAt    string    `json:"updated_at,omitempty"`
}

type ADRManager struct {
	mu      sync.Mutex
	cfg     *config.LatticeConfig
	adrDir  string
	jsonDoc string
	records map[string]*ADR
}

func NewADRManager(cfg *config.LatticeConfig) *ADRManager {
	adrDir := filepath.Join(cfg.CacheDir, "adr")
	jsonDoc := filepath.Join(cfg.CacheDir, "adrs.json")
	mgr := &ADRManager{
		cfg:     cfg,
		adrDir:  adrDir,
		jsonDoc: jsonDoc,
		records: make(map[string]*ADR),
	}
	mgr.load()
	return mgr
}

func (m *ADRManager) load() {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.jsonDoc)
	if err != nil {
		return
	}
	var list []*ADR
	if err := json.Unmarshal(data, &list); err != nil {
		return
	}
	for _, rec := range list {
		if rec.ID != "" {
			m.records[rec.ID] = rec
		}
	}
}

func (m *ADRManager) saveLocked() error {
	_ = os.MkdirAll(m.adrDir, 0755)

	var list []*ADR
	for _, r := range m.records {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Number < list[j].Number
	})

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(m.jsonDoc, data, 0644); err != nil {
		return err
	}

	// Write markdown files for human readability and remove stale/renamed markdown files
	expectedFiles := make(map[string]bool, len(list))
	for _, r := range list {
		slug := slugify(r.Title)
		filename := fmt.Sprintf("%04d-%s.md", r.Number, slug)
		expectedFiles[filename] = true
		filePath := filepath.Join(m.adrDir, filename)

		md := fmt.Sprintf("# %s: %s\n\n- **Status**: %s\n- **Date**: %s\n- **Tags**: %s\n",
			r.ID, r.Title, r.Status, r.Date, strings.Join(r.Tags, ", "))
		if r.SupersededBy != "" {
			md += fmt.Sprintf("- **Superseded by**: %s\n", r.SupersededBy)
		}
		md += fmt.Sprintf("\n## Context\n\n%s\n\n## Decision\n\n%s\n\n## Consequences\n\n%s\n",
			r.Context, r.Decision, r.Consequences)

		_ = os.WriteFile(filePath, []byte(md), 0644)
	}

	if entries, err := os.ReadDir(m.adrDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") && !expectedFiles[entry.Name()] {
				_ = os.Remove(filepath.Join(m.adrDir, entry.Name()))
			}
		}
	}

	return nil
}

var nonAlphaRegex = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func slugify(s string) string {
	lower := strings.ToLower(s)
	clean := nonAlphaRegex.ReplaceAllString(lower, "-")
	return strings.Trim(clean, "-")
}

func (m *ADRManager) List(statusFilter string, tagFilter string) []ADR {
	m.mu.Lock()
	defer m.mu.Unlock()

	var res []ADR
	for _, r := range m.records {
		if statusFilter != "" && !strings.EqualFold(string(r.Status), statusFilter) {
			continue
		}
		if tagFilter != "" {
			hasTag := false
			for _, t := range r.Tags {
				if strings.EqualFold(t, tagFilter) {
					hasTag = true
					break
				}
			}
			if !hasTag {
				continue
			}
		}
		res = append(res, *r)
	}

	sort.Slice(res, func(i, j int) bool {
		return res[i].Number < res[j].Number
	})
	return res
}

func (m *ADRManager) Get(idOrNumber string) (*ADR, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := strings.ToUpper(strings.TrimSpace(idOrNumber))
	if !strings.HasPrefix(key, "ADR-") {
		if n, err := strconv.Atoi(key); err == nil {
			key = fmt.Sprintf("ADR-%04d", n)
		}
	}

	rec, ok := m.records[key]
	if !ok {
		return nil, fmt.Errorf("ADR '%s' not found", idOrNumber)
	}
	copyRec := *rec
	return &copyRec, nil
}

type CreateADROptions struct {
	Title        string   `json:"title"`
	Status       string   `json:"status,omitempty"`
	Context      string   `json:"context"`
	Decision     string   `json:"decision"`
	Consequences string   `json:"consequences,omitempty"`
	Tags         []string `json:"tags,omitempty"`
}

func (m *ADRManager) Create(opts CreateADROptions) (*ADR, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if strings.TrimSpace(opts.Title) == "" {
		return nil, fmt.Errorf("title cannot be empty")
	}

	// Calculate next number
	maxNum := 0
	for _, r := range m.records {
		if r.Number > maxNum {
			maxNum = r.Number
		}
	}
	nextNum := maxNum + 1
	id := fmt.Sprintf("ADR-%04d", nextNum)

	st := StatusAccepted
	if opts.Status != "" {
		st = ADRStatus(strings.ToLower(opts.Status))
	}

	today := time.Now().Format("2006-01-02")
	rec := &ADR{
		ID:           id,
		Number:       nextNum,
		Title:        opts.Title,
		Status:       st,
		Date:         today,
		Context:      opts.Context,
		Decision:     opts.Decision,
		Consequences: opts.Consequences,
		Tags:         opts.Tags,
		UpdatedAt:    today,
	}

	m.records[id] = rec
	if err := m.saveLocked(); err != nil {
		return nil, err
	}
	copyRec := *rec
	return &copyRec, nil
}

type UpdateADROptions struct {
	ID           string    `json:"id"`
	Title        *string   `json:"title,omitempty"`
	Status       *string   `json:"status,omitempty"`
	Context      *string   `json:"context,omitempty"`
	Decision     *string   `json:"decision,omitempty"`
	Consequences *string   `json:"consequences,omitempty"`
	Tags         *[]string `json:"tags,omitempty"`
	SupersededBy *string   `json:"superseded_by,omitempty"`
}

func (m *ADRManager) Update(opts UpdateADROptions) (*ADR, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := strings.ToUpper(strings.TrimSpace(opts.ID))
	if !strings.HasPrefix(key, "ADR-") {
		if n, err := strconv.Atoi(key); err == nil {
			key = fmt.Sprintf("ADR-%04d", n)
		}
	}

	rec, ok := m.records[key]
	if !ok {
		return nil, fmt.Errorf("ADR '%s' not found", opts.ID)
	}

	if opts.Title != nil && *opts.Title != "" {
		rec.Title = *opts.Title
	}
	if opts.Status != nil && *opts.Status != "" {
		rec.Status = ADRStatus(strings.ToLower(*opts.Status))
	}
	if opts.Context != nil {
		rec.Context = *opts.Context
	}
	if opts.Decision != nil {
		rec.Decision = *opts.Decision
	}
	if opts.Consequences != nil {
		rec.Consequences = *opts.Consequences
	}
	if opts.Tags != nil {
		rec.Tags = *opts.Tags
	}
	if opts.SupersededBy != nil {
		rec.SupersededBy = *opts.SupersededBy
		if rec.Status == StatusAccepted {
			rec.Status = StatusSuperseded
		}
	}
	rec.UpdatedAt = time.Now().Format("2006-01-02")

	if err := m.saveLocked(); err != nil {
		return nil, err
	}
	copyRec := *rec
	return &copyRec, nil
}

func (m *ADRManager) Delete(idOrNumber string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := strings.ToUpper(strings.TrimSpace(idOrNumber))
	if !strings.HasPrefix(key, "ADR-") {
		if n, err := strconv.Atoi(key); err == nil {
			key = fmt.Sprintf("ADR-%04d", n)
		}
	}

	if _, ok := m.records[key]; !ok {
		return fmt.Errorf("ADR '%s' not found", idOrNumber)
	}

	delete(m.records, key)
	return m.saveLocked()
}

