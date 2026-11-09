package memory

import (
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// The full-text lane matches on search_tokens, a tsvector built in Go from the
// same normalizer that turns a query into terms. Postgres' 'simple' parser keeps
// Korean particles attached ("서버를" never meets "서버"), and no dictionary
// for them ships with stock Postgres, so the stem is cut here on both sides.

// searchTokenCollections share the ordinary FTS document normalizer. Knowledge
// chunks use their own heading/bigram projection through pgSearchTokens. The
// versioned backfill refreshes both projections without changing source text.
var searchTokenCollections = []string{colMemories, colSkillVersions, colReflections, colObservations, "memory_candidates", "sources"}

// searchTokensPending is literal so PostgreSQL can use the pending indexes
// for missing tokens and for pre-context indexes that need refreshing.
const searchContextPending = `NOT search_context_indexed AND (COALESCE(doc->'memory_context'->>'applicability','') <> '' OR COALESCE(doc->'memory_context'->>'uncertainty','') <> '' OR COALESCE(doc->'memory_context'->>'valid_from','') <> '')`
const searchTokensPending = `collection IN ('memories','skill_versions','reflections','observations','memory_candidates','sources','knowledge_chunks') AND (search_tokens IS NULL OR search_tokens_version < 2 OR (` + searchContextPending + `))`

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

// SearchTokens applies canonical NFC and lowercase, keeps letters, digits and
// attached combining marks, separates Hangul from other scripts, strips Korean
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
	for _, r := range strings.ToLower(norm.NFC.String(text)) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) && len(word) > 0 {
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
	"언제": true, "어디": true, "어디서": true, "누가": true, "누구": true,
	"무엇": true, "뭐": true, "왜": true, "어떻게": true, "어느": true, "무슨": true,
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
// cannot manufacture the two independent matches a long query requires. A term
// that is the leading part of an earlier term (a stem after its inflected word)
// is another spelling of that word and joins its concept for the same reason.
func searchQueryConcepts(terms []string) []string {
	concepts, _ := searchConceptGroups(terms)
	return concepts
}

// searchConceptGroups also returns each concept's first term, the word as the query spelled it.
func searchConceptGroups(terms []string) (concepts, heads []string) {
	concepts, heads = []string{}, []string{}
	for _, term := range terms {
		if i := slices.IndexFunc(heads, func(head string) bool { return strings.HasPrefix(head, term) }); i >= 0 {
			if alt := " | " + tsQuote(term); heads[i] != term && !strings.Contains(concepts[i], alt) {
				concepts[i] = strings.TrimSuffix(concepts[i], ")") + alt + ")"
			}
			continue
		}
		words := searchConceptWords(term)
		parts := make([]string, len(words))
		for i, word := range words {
			parts[i] = tsQuote(word)
		}
		concept := "(" + strings.Join(parts, " | ") + ")"
		if !slices.Contains(concepts, concept) {
			concepts, heads = append(concepts, concept), append(heads, term)
		}
	}
	return concepts, heads
}

// searchConceptWords are the spellings that count as term's one lexical concept.
func searchConceptWords(term string) []string {
	for _, group := range [][]string{{"사본", "백업"}, {"복원", "복구"}} {
		if slices.Contains(group, term) {
			return group
		}
	}
	return []string{term}
}

// A pair of generic procedure words is not evidence about the subject of a
// longer question. Keep these words in the query and its rank, but require a
// substantive concept too when one is present. Short and entirely generic
// lookups remain permissive. This applies only to the lexical lane.
var searchQueryFraming = map[string]bool{
	"규칙": true, "체계": true, "분류": true, "분류체계": true,
	"경로": true, "이동": true, "방법": true, "방식": true,
	"기준": true, "절차": true, "정책": true,
	"관련": true, "대한": true, "관한": true, "대해": true,
	"무엇": true, "어떤": true, "어떻게": true,
	"알려줘": true, "알려주세요": true, "설명": true,
	"설명해줘": true, "설명해주세요": true, "정리": true, "정리해줘": true,
	"rule": true, "rules": true, "system": true, "systems": true,
	"classification": true, "classify": true, "path": true, "paths": true,
	"method": true, "methods": true, "procedure": true, "procedures": true,
	"criteria": true, "policy": true, "policies": true,
	"about": true, "regarding": true, "please": true, "tell": true,
	"explain": true, "describe": true, "use": true, "used": true, "using": true,
	"from": true,
}

func searchQueryTopics(terms []string) []string {
	concepts, heads := searchConceptGroups(terms)
	if len(concepts) < 3 {
		return nil
	}
	// Apply the additional subject gate to procedural framing, where generic
	// overlaps cause unrelated matches. Ordinary questions can need several
	// complementary partial facts (for example current age and graduation age).
	procedural := false
	for _, term := range terms {
		switch term {
		case "규칙", "체계", "분류", "분류체계", "경로", "방법", "방식", "기준", "절차", "정책",
			"rule", "rules", "system", "systems", "classification", "classify", "path", "paths",
			"method", "methods", "procedure", "procedures", "criteria", "policy", "policies":
			procedural = true
		}
	}
	if !procedural {
		return nil
	}
	topics := []string{}
	for i, head := range heads {
		// A shared date or quantity cannot supply the missing subject. Numeric
		// terms still participate in the distinct-match threshold and rank.
		if !searchQueryFraming[head] && strings.IndexFunc(head, unicode.IsLetter) >= 0 {
			topics = append(topics, concepts[i])
		}
	}
	return topics
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
