package memory

// The knowledge deterministic lane ranks with the same Postgres full-text
// search the memory search uses: search_tokens holds a tsvector built in Go
// from SearchTokens, and the query is an OR tsquery of the same normalized
// terms. Knowledge text is often written without spaces between Korean nouns
// ("배포절차"), so Hangul tokens of three or more runes also contribute their
// bigrams on both the document and the query side.
import (
	"context"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	knowledgeFTSAdapter      = "pg-fts-bigram-v1"
	knowledgeQueryMaxTerms   = 64
	knowledgeBigramMinRunes  = 3
	knowledgeTokensMaxPerDoc = searchTokenMaxPerDoc
)

// KnowledgeTextRank is one full-text match inside an already-authorized chunk
// allowlist: how many distinct query terms the chunk contains and the
// Postgres text rank used as the tie breaker.
type KnowledgeTextRank struct {
	ID      string  `json:"id"`
	Matched int     `json:"matched"`
	Score   float64 `json:"score"`
}

func knowledgeHangulToken(token string) bool {
	for _, r := range token {
		if !isHangul(r) {
			return false
		}
	}
	return token != ""
}

// knowledgeSearchTokens is SearchTokens followed by the bigrams of every
// Hangul token of knowledgeBigramMinRunes or more runes.
func knowledgeSearchTokens(text string) []string {
	tokens := SearchTokens(text)
	out := slices.Clone(tokens)
	for _, t := range tokens {
		if !knowledgeHangulToken(t) || utf8.RuneCountInString(t) < knowledgeBigramMinRunes {
			continue
		}
		runes := []rune(t)
		for i := 0; i+1 < len(runes); i++ {
			out = append(out, string(runes[i:i+2]))
		}
	}
	return out
}

// knowledgeQueryTerms are the distinct knowledge tokens of a query.
func knowledgeQueryTerms(query string) []string {
	terms := []string{}
	for _, t := range knowledgeSearchTokens(query) {
		if !slices.Contains(terms, t) {
			terms = append(terms, t)
			if len(terms) == knowledgeQueryMaxTerms {
				break
			}
		}
	}
	return terms
}

// knowledgeChunkSearchText is what a chunk is searchable by: its text and its
// heading path, so a heading word finds every chunk of that section.
func knowledgeChunkSearchText(r Record) string {
	text := r.Text
	if r.Knowledge != nil && r.Knowledge.SectionPath != "" && r.Knowledge.SectionPath != knowledgeSectionDocument {
		text = r.Knowledge.SectionPath + "\n" + text
	}
	return text
}

// knowledgeSearchVector renders a chunk's tokens as a tsvector literal.
func knowledgeSearchVector(r Record) string {
	tokens := knowledgeSearchTokens(knowledgeChunkSearchText(r))
	if len(tokens) > knowledgeTokensMaxPerDoc {
		tokens = tokens[:knowledgeTokensMaxPerDoc]
	}
	var b strings.Builder
	for i, t := range tokens {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(tsQuote(t))
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(min(i+1, searchTokenMaxPos)))
	}
	return b.String()
}

// RankKnowledgeText ranks an explicit chunk ID allowlist that scope, binding,
// lifecycle and trust filters already produced. Like RankVectors it never
// widens that set: the tenant fence stays in SQL and an ID outside the list is
// a conflict. Rows written before search_tokens covered knowledge chunks are
// matched through the plain 'simple' parser on their text.
func (p *Postgres) RankKnowledgeText(ctx context.Context, scope Scope, ids, terms []string) ([]KnowledgeTextRank, error) {
	if err := checkScope(scope); err != nil || len(ids) > knowledgeCandidateLimit || len(terms) > knowledgeQueryMaxTerms {
		return nil, ErrInvalid
	}
	if len(ids) == 0 || len(terms) == 0 {
		return []KnowledgeTextRank{}, nil
	}
	rows, err := p.q().Query(ctx, `WITH c AS (
SELECT id, COALESCE(search_tokens, to_tsvector('simple', COALESCE(doc->>'text',''))) AS v FROM memory_records
WHERE collection=$1 AND workspace_id=$2 AND project_id=$3 AND user_id=$4 AND agent_id=$5 AND id=ANY($6::text[]))
SELECT id, (SELECT count(*) FROM unnest(tsvector_to_array(v)) l WHERE l = ANY($7::text[]))::int, ts_rank(v, $8::tsquery, 1)::float8
FROM c WHERE v @@ $8::tsquery ORDER BY 2 DESC, 3 DESC, id ASC`,
		colKnowledgeChunks, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, ids, terms, searchTermsQuery(terms))
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	allowed := make(map[string]bool, len(ids))
	for _, id := range ids {
		allowed[id] = true
	}
	out := []KnowledgeTextRank{}
	for rows.Next() {
		var rank KnowledgeTextRank
		if err := rows.Scan(&rank.ID, &rank.Matched, &rank.Score); err != nil {
			return nil, ErrUnavailable
		}
		if !allowed[rank.ID] {
			return nil, ErrConflict
		}
		// Round so the context hash cannot depend on float formatting noise.
		rank.Score = math.Round(rank.Score*1e6) / 1e6
		out = append(out, rank)
	}
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	return out, nil
}

// knowledgeTextRankRecords is the in-process equivalent of RankKnowledgeText
// over already-loaded chunks: distinct matched terms first, then term
// frequency normalized by 1+log(length) (Postgres ts_rank normalization 1).
func knowledgeTextRankRecords(records []Record, terms []string) []KnowledgeTextRank {
	out := []KnowledgeTextRank{}
	if len(terms) == 0 {
		return out
	}
	want := map[string]bool{}
	for _, t := range terms {
		want[t] = true
	}
	for _, r := range records {
		tokens := knowledgeSearchTokens(knowledgeChunkSearchText(r))
		seen := map[string]bool{}
		frequency := 0
		for _, t := range tokens {
			if want[t] {
				seen[t] = true
				frequency++
			}
		}
		if len(seen) == 0 {
			continue
		}
		score := float64(frequency) / (1 + math.Log(float64(1+len(tokens))))
		out = append(out, KnowledgeTextRank{ID: r.ID, Matched: len(seen), Score: math.Round(score*1e6) / 1e6})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Matched != out[j].Matched {
			return out[i].Matched > out[j].Matched
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	return out
}
