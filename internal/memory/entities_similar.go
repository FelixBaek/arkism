package memory

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"unicode"
)

// Similar names: a name extraction finds that matches no entity exactly is
// compared with the scope's entities by trigram similarity, the measure
// Postgres pg_trgm uses, computed in memory: the scope's entities are loaded
// here anyway (loadEntityGraph), so no database extension is needed.
// Depending on the space's entity_resolution, a similar name becomes a merge
// proposal a person decides (POST /v1/entities/{id}/merge or PATCH
// dismiss_similar), or, when only case, punctuation or word order differ, a
// mention of the existing entity.

const (
	entityResolveExact   = "exact"   // names match exactly (case and a leading @ aside), nothing else
	entityResolvePropose = "propose" // also propose merges of similar names to a person (server default)
	entityResolveSimilar = "similar" // also attach names whose trigram sets are identical
	// entitySimilarMin is the similarity a pair needs to be proposed; the word and
	// number checks below keep "John Smith"/"Jane Smith" and "Room 101"/"Room 102" apart.
	entitySimilarMin = 0.4
	// entityWordSimilarMin: two words are the same word when equal, one a prefix of the other, or this similar.
	entityWordSimilarMin = 0.5
	entitySimilarMax     = 3
	// entitySimilarBudget caps the entity comparisons of one extraction (about half a
	// second); later new names of a large batch become entities without proposals.
	// shortcut: a full scan per name, bounded here; index trigrams if spaces outgrow it.
	entitySimilarBudget = 1_000_000
)

var entityResolveModes = []string{"", entityResolveExact, entityResolvePropose, entityResolveSimilar}

// EntitySimilar is a merge proposal on an entity: an existing entity whose name is similar.
type EntitySimilar struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

func entityWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// trigramSet pads each word with two leading blanks and one trailing blank and
// takes every three-rune window, as pg_trgm does.
func trigramSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range entityWords(s) {
		r := []rune("  " + w + " ")
		for i := 0; i+3 <= len(r); i++ {
			out[string(r[i:i+3])] = true
		}
	}
	return out
}

// trigramSimilarity is pg_trgm similarity(): shared trigrams over all trigrams.
func trigramSimilarity(a, b map[string]bool) float64 {
	shared := 0
	for t := range a {
		if b[t] {
			shared++
		}
	}
	union := len(a) + len(b) - shared
	if union == 0 {
		return 0
	}
	return float64(shared) / float64(union)
}

func entityNumbers(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsDigit(r) }) {
		out[strings.TrimLeft(w, "0")+"#"] = true // "#" keeps "0" distinct from no number
	}
	return out
}

func missingFrom(a, b map[string]bool) bool {
	for k := range a {
		if !b[k] {
			return true
		}
	}
	return false
}

// entityNamesCompatible: when each name has a number the other lacks they are two
// things; otherwise every word of the shorter name must match a word of the longer.
func entityNamesCompatible(a, b string) bool {
	na, nb := entityNumbers(a), entityNumbers(b)
	if missingFrom(na, nb) && missingFrom(nb, na) {
		return false
	}
	short, long := entityWords(a), entityWords(b)
	if len(short) > len(long) {
		short, long = long, short
	}
	if len(long) < 2 {
		return true
	}
	for _, w := range short {
		if !entityWordInList(w, long) {
			return false
		}
	}
	return true
}

// entityWordInList: shortcut: trigram similarity stands in for an edit-distance ratio; swap it if typo pairs slip through.
func entityWordInList(w string, list []string) bool {
	for _, o := range list {
		if w == o || strings.HasPrefix(w, o) || strings.HasPrefix(o, w) || trigramSimilarity(trigramSet(w), trigramSet(o)) >= entityWordSimilarMin {
			return true
		}
	}
	return false
}

// similar returns live entities whose names are similar to name, most similar first.
// Labels never take part: they are a closed vocabulary, not names.
func (g *entityGraph) similar(name, typ string) []EntitySimilar {
	if typ == entityTypeLabel {
		return nil
	}
	want := trigramSet(name)
	if g.grams == nil {
		g.grams = map[string]map[string]bool{}
	}
	var out []EntitySimilar
	for _, e := range g.entities {
		if e.Deleted || e.Type == entityTypeLabel || e.Type != typ && typ != entityTypeConcept && e.Type != entityTypeConcept {
			continue
		}
		best := 0.0
		for _, n := range append([]string{e.Name}, e.Aliases...) {
			grams, ok := g.grams[n]
			if !ok {
				grams = trigramSet(n)
				g.grams[n] = grams
			}
			if score := trigramSimilarity(want, grams); score > best && score >= entitySimilarMin && entityNamesCompatible(name, n) {
				best = score
			}
		}
		if best > 0 {
			out = append(out, EntitySimilar{ID: e.ID, Name: e.Name, Score: math.Round(best*1000) / 1000})
		}
	}
	slices.SortFunc(out, func(a, b EntitySimilar) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), strings.Compare(a.ID, b.ID))
	})
	return out[:min(len(out), entitySimilarMax)]
}
