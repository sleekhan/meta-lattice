package storage

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
)

var wordRegex = regexp.MustCompile(`[a-zA-Z0-9_]+`)

// splitCamel splits an identifier on underscores and camelCase / acronym
// boundaries: "ZoomSymbol" -> [Zoom Symbol], "parseHTTPServer" -> [parse HTTP Server].
func splitCamel(word string) []string {
	var parts []string
	for _, chunk := range strings.Split(word, "_") {
		runes := []rune(chunk)
		start := 0
		for i := 1; i < len(runes); i++ {
			prev, cur := runes[i-1], runes[i]
			lowerToUpper := unicode.IsLower(prev) && unicode.IsUpper(cur)
			acronymEnd := unicode.IsUpper(prev) && unicode.IsUpper(cur) &&
				i+1 < len(runes) && unicode.IsLower(runes[i+1])
			letterDigit := unicode.IsLetter(prev) != unicode.IsLetter(cur) &&
				(unicode.IsDigit(prev) || unicode.IsDigit(cur))
			if lowerToUpper || acronymEnd || letterDigit {
				parts = append(parts, string(runes[start:i]))
				start = i
			}
		}
		if start < len(runes) {
			parts = append(parts, string(runes[start:]))
		}
	}
	return parts
}

func tokenize(text string) []string {
	raw := wordRegex.FindAllString(text, -1)
	var tokens []string
	for _, t := range raw {
		whole := strings.ToLower(t)
		tokens = append(tokens, whole)
		// Also index the individual sub-words (snake_case and camelCase).
		parts := splitCamel(t)
		if len(parts) > 1 {
			for _, p := range parts {
				if p = strings.ToLower(p); p != "" && p != whole {
					tokens = append(tokens, p)
				}
			}
		}
	}
	return tokens
}

type BM25Index struct {
	mu          sync.RWMutex
	docs        map[string]int            // docID -> token count
	inverted    map[string]map[string]int // term -> docID -> count
	totalDocLen int
	k1          float64
	b           float64
}

func NewBM25Index() *BM25Index {
	return &BM25Index{
		docs:     make(map[string]int),
		inverted: make(map[string]map[string]int),
		k1:       1.2,
		b:        0.75,
	}
}

func (idx *BM25Index) Clear() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.docs = make(map[string]int)
	idx.inverted = make(map[string]map[string]int)
	idx.totalDocLen = 0
}

func (idx *BM25Index) IndexDocument(docID string, text string) {
	tokens := tokenize(text)
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// If already exists, remove first
	if oldLen, exists := idx.docs[docID]; exists {
		idx.totalDocLen -= oldLen
		for _, post := range idx.inverted {
			delete(post, docID)
		}
	}

	docLen := len(tokens)
	if docLen == 0 {
		return
	}
	idx.docs[docID] = docLen
	idx.totalDocLen += docLen

	for _, token := range tokens {
		post, ok := idx.inverted[token]
		if !ok {
			post = make(map[string]int)
			idx.inverted[token] = post
		}
		post[docID]++
	}
}

func (idx *BM25Index) RemoveDocument(docID string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if oldLen, exists := idx.docs[docID]; exists {
		idx.totalDocLen -= oldLen
		delete(idx.docs, docID)
		for _, post := range idx.inverted {
			delete(post, docID)
		}
	}
}

type SearchMatch struct {
	DocID string
	Score float64
}

func (idx *BM25Index) Search(query string, limit int) []SearchMatch {
	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		return nil
	}

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	numDocs := float64(len(idx.docs))
	if numDocs == 0 {
		return nil
	}
	avgDocLen := float64(idx.totalDocLen) / numDocs

	scores := make(map[string]float64)

	for _, token := range queryTokens {
		post, exists := idx.inverted[token]
		if !exists {
			// Prefix matching fallback for typing
			for term, p := range idx.inverted {
				if strings.HasPrefix(term, token) {
					df := float64(len(p))
					idf := math.Log(1.0 + (numDocs-df+0.5)/(df+0.5))
					if idf < 0 {
						idf = 0.01
					}
					for docID, tf := range p {
						dLen := float64(idx.docs[docID])
						tfF := float64(tf)
						denom := tfF + idx.k1*(1.0-idx.b+idx.b*(dLen/avgDocLen))
						termScore := idf * (tfF * (idx.k1 + 1.0)) / denom
						scores[docID] += termScore * 0.7 // slightly downweight prefix match
					}
				}
			}
			continue
		}

		df := float64(len(post))
		idf := math.Log(1.0 + (numDocs-df+0.5)/(df+0.5))
		if idf < 0 {
			idf = 0.01
		}

		for docID, tf := range post {
			dLen := float64(idx.docs[docID])
			tfF := float64(tf)
			denom := tfF + idx.k1*(1.0-idx.b+idx.b*(dLen/avgDocLen))
			termScore := idf * (tfF * (idx.k1 + 1.0)) / denom
			scores[docID] += termScore
		}
	}

	var results []SearchMatch
	for docID, score := range scores {
		if score > 0 {
			results = append(results, SearchMatch{DocID: docID, Score: score})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results
}
