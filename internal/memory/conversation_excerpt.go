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
	concepts, conceptAt := map[string]bool{}, []int{}
	for j, term := range terms {
		concept := searchQueryConcepts([]string{term})[0]
		if !concepts[concept] {
			concepts[concept] = true
			conceptAt = append(conceptAt, j)
		}
	}
	need := 1
	if len(conceptAt) >= 3 {
		need = 2 // Same threshold as lexicallyRelevant.
	}
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
			found := false
			for _, word := range searchConceptWords(term) {
				found = found || have[word]
			}
			if found {
				matches[i][j] = true
				counts[j]++
			}
		}
	}
	// All weights are nonnegative, so a sliding complete-line window finds
	// the optimum in O(lines * query terms), even for many short/empty lines.
	weights := make([]float64, len(terms))
	for j := range terms {
		weights[j] = 1 + math.Log(float64(len(lines)+1)/float64(counts[j]+1))
	}
	bestScore, bestStart, bestEnd := -1.0, 0, 0
	bestEligible := false
	present := make([]int, len(terms))
	start, used := 0, 0
	for end := range lines {
		used += lengths[end]
		if end > start {
			used++
		}
		for j, yes := range matches[end] {
			if yes {
				present[j]++
			}
		}
		for used > budget && start <= end {
			used -= lengths[start]
			if start < end {
				used--
			}
			for j, yes := range matches[start] {
				if yes {
					present[j]--
				}
			}
			start++
		}
		if start > end {
			continue
		}
		score := 0.0
		for j, n := range present {
			if n > 0 {
				score += weights[j]
			}
		}
		found := 0
		for _, j := range conceptAt {
			if present[j] > 0 {
				found++
			}
		}
		eligible := found >= need
		// Prefer a window that survives the downstream gate. If none does,
		// retain the existing highest-score fallback, including vector callers.
		if eligible && !bestEligible || eligible == bestEligible && (score > bestScore || score == bestScore && end-start > bestEnd-bestStart) {
			bestScore, bestStart, bestEnd = score, start, end
			bestEligible = eligible
		}
	}
	if bestScore <= 0 {
		return runeCutString(text, budget)
	}
	return strings.Join(lines[bestStart:bestEnd+1], "\n")
}
