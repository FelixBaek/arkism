package memory

import (
	"math"
	"strings"
	"unicode/utf8"
)

// Choose a contiguous, verbatim group of complete turns instead of always
// cutting the start of a batch. Names repeated on every line carry less weight
// than topic words. No query answer or evaluation label participates.
func conversationQueryExcerpt(query, text string, budget int) string {
	text = redactSessionPrivate(text)
	if utf8.RuneCountInString(text) <= budget {
		return text
	}
	lines := strings.Split(text, "\n")
	terms := searchQueryTerms(query)
	if len(lines) < 2 || len(terms) == 0 {
		return runeCutString(text, budget)
	}
	// The later lexical gate counts concepts, not their alias spellings.
	// Keep one representative per concept while preserving the existing score.
	conceptAt := conceptRepresentatives(terms)
	need := 1
	if len(conceptAt) >= 3 {
		need = 2 // Same threshold as lexicallyRelevant.
	}
	matches, lengths, counts := excerptLineMatches(lines, terms)
	// All weights are nonnegative, so a sliding complete-line window finds
	// the optimum in O(lines * query terms), even for many short/empty lines.
	weights := make([]float64, len(terms))
	for j := range terms {
		weights[j] = 1 + math.Log(float64(len(lines)+1)/float64(counts[j]+1))
	}
	bestScore, bestStart, bestEnd := -1.0, 0, 0
	bestEligible := false
	w := excerptWindow{present: make([]int, len(terms)), lengths: lengths, matches: matches}
	for end := range lines {
		w.push(end)
		w.shrink(end, budget)
		if w.start > end {
			continue
		}
		score := w.score(weights)
		eligible := w.conceptsFound(conceptAt) >= need
		// Prefer a window that survives the downstream gate. If none does,
		// retain the existing highest-score fallback, including vector callers.
		if eligible && !bestEligible || eligible == bestEligible && (score > bestScore || score == bestScore && end-w.start > bestEnd-bestStart) {
			bestScore, bestStart, bestEnd = score, w.start, end
			bestEligible = eligible
		}
	}
	if bestScore <= 0 {
		return runeCutString(text, budget)
	}
	return strings.Join(lines[bestStart:bestEnd+1], "\n")
}

// conceptRepresentatives returns the index of the first term of each concept.
func conceptRepresentatives(terms []string) []int {
	concepts, conceptAt := map[string]bool{}, []int{}
	for j, term := range terms {
		concept := searchQueryConcepts([]string{term})[0]
		if !concepts[concept] {
			concepts[concept] = true
			conceptAt = append(conceptAt, j)
		}
	}
	return conceptAt
}

// excerptLineMatches marks which terms each line holds, with line lengths and per-term line counts.
func excerptLineMatches(lines, terms []string) ([][]bool, []int, []int) {
	counts := make([]int, len(terms))
	matches := make([][]bool, len(lines))
	lengths := make([]int, len(lines))
	for i, line := range lines {
		lengths[i] = utf8.RuneCountInString(line)
		matches[i] = make([]bool, len(terms))
		have := map[string]bool{}
		for _, token := range SearchTokens(line) {
			have[token] = true
		}
		for j, term := range terms {
			if lineHasTerm(have, term) {
				matches[i][j] = true
				counts[j]++
			}
		}
	}
	return matches, lengths, counts
}

func lineHasTerm(have map[string]bool, term string) bool {
	return termInTokens(term, func(word string) bool { return have[word] })
}

// excerptWindow is a sliding run of complete lines [start, end] and its term counts.
type excerptWindow struct {
	present     []int
	lengths     []int
	matches     [][]bool
	start, used int
}

func (w *excerptWindow) push(end int) {
	w.used += w.lengths[end]
	if end > w.start {
		w.used++
	}
	for j, yes := range w.matches[end] {
		if yes {
			w.present[j]++
		}
	}
}

func (w *excerptWindow) shrink(end, budget int) {
	for w.used > budget && w.start <= end {
		w.used -= w.lengths[w.start]
		if w.start < end {
			w.used--
		}
		for j, yes := range w.matches[w.start] {
			if yes {
				w.present[j]--
			}
		}
		w.start++
	}
}

func (w *excerptWindow) score(weights []float64) float64 {
	score := 0.0
	for j, n := range w.present {
		if n > 0 {
			score += weights[j]
		}
	}
	return score
}

func (w *excerptWindow) conceptsFound(conceptAt []int) int {
	found := 0
	for _, j := range conceptAt {
		if w.present[j] > 0 {
			found++
		}
	}
	return found
}
