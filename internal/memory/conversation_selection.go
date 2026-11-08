package memory

import (
	"math"
	"strings"
)

// selectConversationCandidates only sees already authorized, redacted excerpts.
// The retrieval score remains the main signal. Lexical evidence in the returned
// excerpt can break close ranks, and substantially repeated passages receive a
// small penalty. Neither signal removes a vector-only match or fabricates text.
// This selector is used only by the opt-in local recall-quality path.
func selectConversationCandidates(query string, candidates []ScoredHit, memories, existing []Hit, limit int) []Hit {
	if limit <= 0 || len(candidates) == 0 {
		return nil
	}
	terms := searchQueryTerms(query)
	var concepts []string
	seen := map[string]bool{}
	for _, term := range terms {
		key := searchQueryConcepts([]string{term})[0]
		if !seen[key] {
			seen[key] = true
			concepts = append(concepts, term)
		}
	}
	weights := make([]float64, len(concepts))
	matches := make([][]bool, len(candidates))
	best := 0.0
	for i, row := range candidates {
		best = max(best, row.Score)
		matches[i] = make([]bool, len(concepts))
		// No batch summary, keywords or omitted turns may raise this score.
		h := Hit{Text: row.Text}
		for j, concept := range concepts {
			found, _ := conceptsFound([]string{concept}, h)
			matches[i][j] = found > 0
		}
	}
	total := 0.0
	for j := range concepts {
		n := 0
		for i := range candidates {
			if matches[i][j] {
				n++
			}
		}
		weights[j] = 1 + math.Log(float64(len(candidates)+1)/float64(n+1))
		total += weights[j]
	}
	base := make([]float64, len(candidates))
	passages := make([]conversationPassage, len(candidates))
	for i, row := range candidates {
		if best > 0 {
			base[i] = .75 * row.Score / best
		}
		// Use the existing relevance threshold on the excerpt alone. A name
		// or metadata match cannot earn the lexical relevance bonus.
		if total > 0 && lexicallyRelevant(terms, Hit{Text: row.Text}) {
			for j, yes := range matches[i] {
				if yes {
					base[i] += .25 * weights[j] / total
				}
			}
		}
		passages[i] = conversationPassageOf(row.Text)
	}
	covered := make([]conversationPassage, 0, len(memories)+len(existing)+limit)
	for _, hits := range [][]Hit{memories, existing} {
		for _, h := range hits {
			covered = append(covered, conversationPassageOf(h.Text))
		}
	}
	used := make([]bool, len(candidates))
	out := make([]Hit, 0, min(limit, len(candidates)))
	for len(out) < limit && len(out) < len(candidates) {
		at, score := -1, math.Inf(-1)
		for i := range candidates {
			if used[i] {
				continue
			}
			value := base[i]
			for _, previous := range covered {
				if conversationPassageRepeated(passages[i], previous) {
					value -= .15
					break
				}
			}
			if value > score { // stable original fusion order resolves ties
				at, score = i, value
			}
		}
		used[at] = true
		out = append(out, candidates[at].Hit)
		covered = append(covered, passages[at])
	}
	return out
}

type conversationPassage struct {
	text  string
	words int
}

func conversationPassageOf(text string) conversationPassage {
	// SearchTokens drops short tokens and normalizes suffixes for retrieval.
	// Duplicate evidence needs the stricter representation: keep quantities,
	// punctuation, negation and every word, normalizing only case/whitespace.
	words := strings.Fields(strings.ToLower(text))
	return conversationPassage{text: " " + strings.Join(words, " ") + " ", words: len(words)}
}

// Only an exact normalized passage of at least six tokens counts as repeated.
// Substring containment is insufficient: a surrounding qualification or added
// negation can change the meaning. Shared names and topics keep their score.
func conversationPassageRepeated(candidate, previous conversationPassage) bool {
	if candidate.words < 6 || previous.words < 6 {
		return false
	}
	return candidate.text == previous.text
}
