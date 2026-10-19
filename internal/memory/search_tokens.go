package memory

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The full-text lane matches on search_tokens, a tsvector built in Go from the
// same normalizer that turns a query into terms. Postgres' 'simple' parser keeps
// Korean particles attached ("서버를" never meets "서버"), and no dictionary
// for them ships with stock Postgres, so the stem is cut here on both sides.

// searchTokenCollections are the collections the fts lane reads; other rows
// keep search_tokens NULL and are never backfilled.
var searchTokenCollections = []string{colMemories, colSkillVersions, colReflections}

// searchTokensPending is literal so PostgreSQL can use the pending indexes
// for missing tokens and for pre-context indexes that need refreshing.
const searchContextPending = `NOT search_context_indexed AND (COALESCE(doc->'memory_context'->>'applicability','') <> '' OR COALESCE(doc->'memory_context'->>'uncertainty','') <> '' OR COALESCE(doc->'memory_context'->>'valid_from','') <> '')`
const searchTokensPending = `collection IN ('memories','skill_versions','reflections') AND (search_tokens IS NULL OR (` + searchContextPending + `))`

const (
	searchTokenMinRunes   = 2
	searchTokenMaxRunes   = 64    // longer runs are hashes or noise, not words
	searchTokenMaxPerDoc  = 8192  // keeps a tsvector far below its 1MB limit
	searchTokenMaxPos     = 16383 // tsvector position ceiling
	searchQueryMaxTokens  = 32
	searchKoreanStripPass = 3 // "서버에서는" = 서버 + 에서 + 는
)

// koreanSuffixes are particles and common 하다/되다 endings, longest first so
// "에서" wins over "서"-less "에" and "으로" over "로".
var koreanSuffixes = func() []string {
	s := []string{
		// particles
		"은", "는", "이", "가", "을", "를", "에", "에서", "으로", "로", "와", "과", "의", "도", "만",
		"까지", "부터", "에게", "한테", "하고", "이나", "나",
		// endings
		"했습니다", "합니다", "됩니다", "입니다", "했다", "한다", "하는", "하면", "해서", "하여", "하기", "했던",
		"된다", "되는", "되면", "됐다", "이다", "할", "한", "된", "됨", "함",
	}
	slices.SortStableFunc(s, func(a, b string) int { return utf8.RuneCountInString(b) - utf8.RuneCountInString(a) })
	return s
}()

func isHangul(r rune) bool { return unicode.Is(unicode.Hangul, r) }

// stripKoreanSuffix removes trailing particles/endings while at least keep runes
// remain. keep is 0 for a Hangul run glued to a Latin word ("PR을" → "PR").
func stripKoreanSuffix(word string, keep int) string {
	for range searchKoreanStripPass {
		n := utf8.RuneCountInString(word)
		cut := false
		for _, suffix := range koreanSuffixes {
			if strings.HasSuffix(word, suffix) && n-utf8.RuneCountInString(suffix) >= keep {
				word = strings.TrimSuffix(word, suffix)
				cut = true
				break
			}
		}
		if !cut || word == "" {
			break
		}
	}
	return word
}

// SearchTokens lowercases text, splits it on anything that is not a letter or
// digit, separates Hangul from other scripts inside a word, strips Korean
// particles and endings, and keeps tokens of two or more runes. Order and
// repeats are kept so that rank can use frequency and proximity.
func SearchTokens(text string) []string {
	out := []string{}
	word := []rune{}
	flush := func() {
		if len(word) == 0 {
			return
		}
		// Split the word into runs of one script: "Docker컨테이너를" → Docker, 컨테이너를.
		start := 0
		for i := 1; i <= len(word); i++ {
			if i < len(word) && isHangul(word[i]) == isHangul(word[start]) {
				continue
			}
			run := string(word[start:i])
			if isHangul(word[start]) {
				keep := searchTokenMinRunes
				if start > 0 {
					keep = 0
				}
				run = stripKoreanSuffix(run, keep)
			}
			if n := utf8.RuneCountInString(run); n >= searchTokenMinRunes && n <= searchTokenMaxRunes {
				out = append(out, run)
			}
			start = i
		}
		word = word[:0]
	}
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			word = append(word, r)
			continue
		}
		flush()
	}
	flush()
	return out
}

// Question scaffolding is not topic evidence. Keep negation, quantities and
// temporal operators; exact keyword search remains independent of this lane.
var searchQueryScaffolding = map[string]bool{
	"what": true, "when": true, "where": true, "who": true, "whose": true,
	"why": true, "how": true, "which": true,
	"do": true, "does": true, "did": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true,
	"has": true, "have": true, "had": true, "can": true, "could": true,
	"would": true, "should": true, "the": true, "an": true,
	"and": true, "or": true, "of": true, "in": true, "on": true,
	"at": true, "to": true, "for": true, "with": true, "by": true,
	"you": true, "your": true, "me": true, "my": true, "his": true,
	"her": true, "their": true, "they": true, "he": true, "she": true,
}

// searchQueryTerms are the distinct normalized topic tokens of a query, capped
// at searchQueryMaxTokens concepts so alias spellings do not consume the budget.
func searchQueryTerms(query string) []string {
	terms := []string{}
	for _, t := range SearchTokens(query) {
		if searchQueryScaffolding[t] {
			continue
		}
		if !slices.Contains(terms, t) {
			terms = append(terms, t)
			if len(searchQueryConcepts(terms)) == searchQueryMaxTokens {
				break
			}
		}
	}
	return terms
}

// Related backup terminology forms one lexical concept. Count the concept
// once even when the query or document contains both spellings, so expansion
// cannot manufacture the two independent matches a long query requires.
func searchQueryConcepts(terms []string) []string {
	groups := [][]string{{"사본", "백업"}, {"복원", "복구"}}
	concepts := []string{}
	for _, term := range terms {
		words := []string{term}
		for _, group := range groups {
			if slices.Contains(group, term) {
				words = group
				break
			}
		}
		parts := make([]string, len(words))
		for i, word := range words {
			parts[i] = tsQuote(word)
		}
		concept := "(" + strings.Join(parts, " | ") + ")"
		if !slices.Contains(concepts, concept) {
			concepts = append(concepts, concept)
		}
	}
	return concepts
}

func tsQuote(token string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(token) + "'"
}

// searchTokensVector renders tokens as a tsvector literal ('word':pos ...).
// Building the literal avoids running the text parser a second time, so the
// stored lexemes are exactly the Go tokens the query is compared against.
func searchTokensVector(text string) string {
	tokens := SearchTokens(text)
	if len(tokens) > searchTokenMaxPerDoc {
		tokens = tokens[:searchTokenMaxPerDoc]
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

// searchTermsQuery renders terms as an OR tsquery literal.
func searchTermsQuery(terms []string) string {
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = tsQuote(t)
	}
	return strings.Join(quoted, " | ")
}

// searchTokensText is the text the legacy expression indexed: text, summary
// and keywords.
func searchTokensText(text, summary string, keywords []string) string {
	return text + " " + summary + " " + strings.Join(keywords, " ")
}

// searchTokensRecordText also indexes the descriptive conditions under which a
// memory applies. Classification labels and observation timestamps are omitted:
// they describe provenance, not a subject a user is trying to recall.
func searchTokensRecordText(r Record) string {
	// Reserve the bounded context ahead of the body so a long record cannot
	// consume the document token budget before its conditions are indexed.
	var prefix strings.Builder
	if c := r.MemoryContext; c != nil {
		for _, qualifier := range []string{c.Applicability, c.Uncertainty, c.ValidFrom} {
			if qualifier != "" {
				prefix.WriteString(qualifier)
				prefix.WriteByte(' ')
			}
		}
	}
	return prefix.String() + searchTokensText(r.Text, r.Summary, r.Keywords)
}

// pgSearchTokens is the search_tokens value for a write: NULL for collections
// the fts lane never reads.
func pgSearchTokens(collection string, r Record) any {
	if collection == colKnowledgeChunks {
		return knowledgeSearchVector(r)
	}
	if !slices.Contains(searchTokenCollections, collection) {
		return nil
	}
	return searchTokensVector(searchTokensRecordText(r))
}

const searchTokensBackfillBatch = 500

// BackfillSearchTokens fills search_tokens for rows written before the column or context-aware indexing
// existed, in batches of searchTokensBackfillBatch, each its own transaction,
// until none remain or budget elapses. It is idempotent and resumable: a later
// call continues with the rows still pending. A row whose revision changed since
// it was read is left for the next call. Until a row is filled, the fts lane
// still finds it through the legacy expression index. It returns the number of
// rows filled and whether rows remain.
func (p *Postgres) BackfillSearchTokens(ctx context.Context, budget time.Duration) (int, bool, error) {
	deadline := time.Now().Add(budget)
	filled := 0
	lastCollection, lastID := "", ""
	for {
		if time.Now().After(deadline) || ctx.Err() != nil {
			return filled, true, nil
		}
		rows, err := p.q().Query(ctx, `SELECT collection, id, revision, COALESCE(doc->>'text',''), COALESCE(doc->>'summary',''),
COALESCE(ARRAY(SELECT jsonb_array_elements_text(CASE WHEN jsonb_typeof(doc->'keywords')='array' THEN doc->'keywords' ELSE '[]'::jsonb END)),'{}'),
COALESCE(doc->'memory_context'->>'applicability',''), COALESCE(doc->'memory_context'->>'uncertainty',''), COALESCE(doc->'memory_context'->>'valid_from','')
FROM memory_records WHERE `+searchTokensPending+` AND (collection,id) > ($1,$2)
ORDER BY collection, id LIMIT $3`, lastCollection, lastID, searchTokensBackfillBatch)
		if err != nil {
			return filled, true, pgErr(err)
		}
		var cols, ids, vecs []string
		var revs []int64
		for rows.Next() {
			var c, id, text, summary string
			var rev int64
			var keywords []string
			var applicability, uncertainty, validFrom string
			if err = rows.Scan(&c, &id, &rev, &text, &summary, &keywords, &applicability, &uncertainty, &validFrom); err != nil {
				rows.Close()
				return filled, true, pgErr(err)
			}
			cols, ids, revs = append(cols, c), append(ids, id), append(revs, rev)
			vecs = append(vecs, searchTokensVector(searchTokensRecordText(Record{Text: text, Summary: summary, Keywords: keywords, MemoryContext: &MemoryContext{Applicability: applicability, Uncertainty: uncertainty, ValidFrom: validFrom}})))
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return filled, true, pgErr(err)
		}
		if len(ids) == 0 {
			return filled, false, nil
		}
		tag, err := p.q().Exec(ctx, `UPDATE memory_records m SET search_tokens=v.t::tsvector,search_context_indexed=true
FROM unnest($1::text[],$2::text[],$3::bigint[],$4::text[]) AS v(c,i,r,t)
WHERE m.collection=v.c AND m.id=v.i AND m.revision=v.r AND (`+searchTokensPending+`)`, cols, ids, revs, vecs)
		if err != nil {
			return filled, true, pgErr(err)
		}
		filled += int(tag.RowsAffected())
		lastCollection, lastID = cols[len(cols)-1], ids[len(ids)-1]
	}
}
