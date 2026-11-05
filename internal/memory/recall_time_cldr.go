package memory

import (
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

//go:generate go run ../../tools/cldrtime -src $CLDR_DATES_FULL -out recall_time_cldr_data.go

// Time words for the languages recall_time_lexicon.go does not cover, from Unicode CLDR
// (recall_time_cldr_data.go): month names, yesterday/last week-style words, "N units
// ago" and last/this weekday. They are only a fallback: parseTimeIntent reads them
// when no hand-tuned rule matched, so the 27 hand-tuned languages and their false-positive
// guards decide first. Phrases are matched as whole words, not regexps:
//   - a month name needs a day (1..31) right before or after it, or a 4-digit year next to it;
//   - a one-word phrase (month names too: "out", "here", "masa") needs at least cldrSingleMin
//     letters and must not be a hand-tuned stop word or month name ("mars", "jana") or a
//     known false friend (cldrFalseFriends); a hand-tuned month name is kept when it names
//     the same month, so "enero 15, 2026" reads in locales that borrow the Spanish names;
//   - there are no weekend words: CLDR dateFields has no weekend field (only day, week,
//     month, year, quarter, weekday and the seven day names), so weekends are hand-tuned only;
//   - a one-word Latin-script "this <weekday>" is the bare weekday name in some locales
//     ("monday", "måndag"), so it is skipped;
//   - phrases two tables (or two locales) read differently were dropped.
//
// shortcut: scripts written without spaces (Lao, Khmer, Burmese) are not split into words
// here, so their phrases only match when the query spaces them; add a segmenter if asked.

const (
	cldrSingleMin = 5
	cldrMaxWords  = 6
)

type cldrTimeEntry struct {
	kind  byte // m month, r relative, a ago, l last weekday, t this weekday
	key   int
	numAt int // ago: the word index of the number
}

var cldrTime = sync.OnceValue(buildCLDRTime)

func buildCLDRTime() map[string]cldrTimeEntry {
	guard := map[string]int{} // hand-tuned month names keep their month; stop words and clashing names are -1
	for i := range timeLexicons {
		for w, m := range timeLexicons[i].monthOf {
			if g, ok := guard[w]; ok && g != m {
				m = -1
			}
			guard[w] = m
		}
	}
	for i := range timeLexicons {
		for w := range timeLexicons[i].stopSet {
			guard[w] = -1
		}
	}
	out, bad := map[string]cldrTimeEntry{}, map[string]bool{}
	for _, line := range strings.Split(cldrTimeData, "\n") {
		one, e, ok := cldrLine(line, guard)
		if !ok {
			continue
		}
		if prev, ok := out[one]; ok && prev != e {
			bad[one] = true
		}
		out[one] = e
	}
	for k := range bad {
		delete(out, k)
	}
	return out
}

// cldrLine reads one "<code>\t<phrase>" data line into its folded phrase and entry; ok is
// false for lines the guards in the file comment leave out.
func cldrLine(line string, guard map[string]int) (string, cldrTimeEntry, bool) {
	code, phrase, _ := strings.Cut(line, "\t")
	words := cldrWords(foldTimeText(phrase))
	key, err := strconv.Atoi(code[min(len(code), 1):])
	if code == "" || err != nil || len(words) == 0 || len(words) > cldrMaxWords {
		return "", cldrTimeEntry{}, false
	}
	e := cldrTimeEntry{kind: code[0], key: key, numAt: -1}
	for i, w := range words {
		if w == "0" {
			e.numAt = i
		}
	}
	one := strings.Join(words, " ")
	single := len(words) == 1
	// A hand-tuned month name still reads as the same month here, for word orders its own
	// rules skip ("enero 15, 2026" in Filipino); a month name is never anything else.
	g, guarded := guard[one]
	switch {
	case e.kind == 'a' != (e.numAt >= 0), guarded && (single || e.kind == 'm') && !(e.kind == 'm' && g == e.key):
		return "", cldrTimeEntry{}, false
	case single && utf8.RuneCountInString(one) < cldrSingleMin, single && e.kind == 't' && isLatinWord(one), cldrFalseFriends[one]:
		return "", cldrTimeEntry{}, false
	}
	return one, e, true
}

// cldrWords splits folded text into words, writing numbers of up to 3 digits as "0"
// (the pattern slot); years keep their digits.
func cldrWords(s string) []string {
	words := strings.FieldsFunc(s, notTimeWordRune)
	for i, w := range words {
		if _, err := strconv.Atoi(w); err == nil && len(w) <= 3 {
			words[i] = "0"
		}
	}
	return words
}

// cldrTimeIntent finds the first CLDR phrase in the folded query, longest first at each word.
func cldrTimeIntent(q string, now time.Time) (timeWindow, bool) {
	table := cldrTime()
	raw := strings.FieldsFunc(q, notTimeWordRune)
	words := cldrWords(q)
	for i := range words {
		for n := min(cldrMaxWords, len(words)-i); n >= 1; n-- {
			e, ok := table[strings.Join(words[i:i+n], " ")]
			if !ok {
				continue
			}
			if from, to, ok := e.span(raw, i, n, now); ok {
				return timeWindow{From: from, To: to, Phrase: strings.Join(raw[i:i+n], " ")}, true
			}
		}
	}
	return timeWindow{}, false
}

func (e cldrTimeEntry) span(raw []string, at, n int, now time.Time) (time.Time, time.Time, bool) {
	switch e.kind {
	case 'm':
		return cldrMonthSpan(raw, at, n, e.key, now)
	case 'r':
		if e.key >= 0 && e.key < relKinds {
			return relSpans[e.key](now)
		}
	case 'a':
		if u := e.key; u >= 0 && u < 4 {
			return agoSpan(now, u, atoi(raw[at+e.numAt]))
		}
	case 'l', 't':
		offset := 0
		if e.kind == 'l' {
			offset = -1
		}
		if monday, _, _ := weekSpan(now, offset); e.key >= 0 && e.key < 7 {
			day := monday.AddDate(0, 0, e.key)
			return day, day.AddDate(0, 0, 1), true
		}
	}
	return time.Time{}, time.Time{}, false
}

// cldrMonthSpan reads a month name with its day ("15 gener", "15 de gener", "gener 15")
// and/or its year;
// a name with neither is not a time phrase.
func cldrMonthSpan(raw []string, at, n, month int, now time.Time) (time.Time, time.Time, bool) {
	day, year := 0, ""
	for _, i := range []int{at - 1, at + n, at - 2, at + n + 1} {
		if i < 0 || i >= len(raw) {
			continue
		}
		if v, err := strconv.Atoi(raw[i]); err == nil {
			switch {
			case len(raw[i]) == 4 && year == "":
				year = raw[i]
			case len(raw[i]) <= 2 && v >= 1 && v <= 31 && day == 0 && (i == at-1 || i == at+n || i == at-2 && utf8.RuneCountInString(raw[at-1]) <= 3):
				day = v
			}
		}
	}
	switch {
	case day > 0:
		return inYear(year, now, func(y int) (time.Time, time.Time, bool) { return dateSpan(y, month, day, now.Location()) })
	case year != "":
		return monthSpan(atoi(year), month, "", now.Location())
	}
	return time.Time{}, time.Time{}, false
}

// cldrFalseFriends are CLDR time words that are ordinary words in a common language
// ("hitte" is Dutch for heat, "danes" English, "kamari"/"wingi" Swahili).
// shortcut: hand-picked from a multilingual corpus test, extend it when a new collision is reported.
var cldrFalseFriends = map[string]bool{"hitte": true, "danes": true, "kamari": true, "wingi": true}

func isLatinWord(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) && !unicode.Is(unicode.Latin, r) {
			return false
		}
	}
	return true
}
