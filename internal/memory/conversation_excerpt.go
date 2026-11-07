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
	counts := make([]int, len(terms))
	matches := make([][]bool, len(lines))
	lengths := make([]int, len(lines))
	for i, line := range lines {
		lengths[i] = utf8.RuneCountInString(line)
		matches[i] = make([]bool, len(terms))
		for j, term := range terms {
			found, _ := conceptsFound([]string{term}, Hit{Text: line})
			if found > 0 {
				matches[i][j] = true
				counts[j]++
			}
		}
	}
	bestScore, bestStart, bestEnd := -1.0, 0, 0
	for start := range lines {
		used := 0
		seen := make([]bool, len(terms))
		score := 0.0
		for end := start; end < len(lines); end++ {
			if end > start {
				used++
			}
			used += lengths[end]
			if used > budget {
				break
			}
			for j, yes := range matches[end] {
				if yes && !seen[j] {
					seen[j] = true
					score += 1 + math.Log(float64(len(lines)+1)/float64(counts[j]+1))
				}
			}
			if score > bestScore || score == bestScore && end-start > bestEnd-bestStart {
				bestScore, bestStart, bestEnd = score, start, end
			}
		}
	}
	if bestScore <= 0 {
		return runeCutString(text, budget)
	}
	return strings.Join(lines[bestStart:bestEnd+1], "\n")
}
