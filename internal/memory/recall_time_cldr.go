package memory

import (
	"regexp"
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
	cldrMaxSuffix = 4 // letters of a case ending after a number, or after a month stem's cut letters
)

type cldrTimeEntry struct {
	kind  byte // m month, i inflected month (cldrMonthStem), r relative, a ago, l last weekday, t this weekday
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
			if !ok && n == 1 {
				e, ok = cldrMonthStem(words[i])
			}
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
	case 'm', 'i':
		return cldrMonthSpan(raw, at, n, e.key, e.kind == 'i', now)
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

// cldrMonthStems maps the stem of each one-word CLDR month name (the name without its
// last one or two letters, at least cldrSingleMin letters) to the month, so inflected
// forms read: Basque "urtarrilaren" (of January), Latvian "janvārī" (in January). A stem
// two months share is dropped.
var cldrMonthStems = sync.OnceValue(func() map[string]int {
	out, bad := map[string]int{}, map[string]bool{}
	for w, e := range cldrTime() {
		r := []rune(w)
		for cut := 1; e.kind == 'm' && cut <= 2 && len(r)-cut >= cldrSingleMin && !strings.Contains(w, " "); cut++ {
			stem := string(r[:len(r)-cut])
			if m, ok := out[stem]; ok && m != e.key {
				bad[stem] = true
			}
			out[stem] = e.key
		}
	}
	for k := range bad {
		delete(out, k)
	}
	return out
})

// cldrMonthStem reads a word as a month name with a case ending of up to cldrMaxSuffix
// letters after its stem, longest stem first; like a plain name it still needs a day or
// year next to it.
func cldrMonthStem(word string) (cldrTimeEntry, bool) {
	r := []rune(word)
	for n := len(r) - 1; n >= cldrSingleMin && len(r)-n <= cldrMaxSuffix+2; n-- {
		if m, ok := cldrMonthStems()[string(r[:n])]; ok {
			return cldrTimeEntry{kind: 'i', key: m, numAt: -1}, true
		}
	}
	return cldrTimeEntry{}, false
}

// leadingNumber is the digits a word starts with when at most cldrMaxSuffix letters
// follow ("15a", "15ean", "2026ko" in Basque, "15th"), else the word itself.
func leadingNumber(w string) string {
	digits := strings.TrimRightFunc(w, unicode.IsLetter)
	if digits != w && digits != "" && utf8.RuneCountInString(w[len(digits):]) <= cldrMaxSuffix {
		return digits
	}
	return w
}

// cldrMonthSpan reads a month name with its day ("15 gener", "15 de gener", "gener 15")
// and/or its year;
// a name with neither is not a time phrase.
func cldrMonthSpan(raw []string, at, n, month int, inflected bool, now time.Time) (time.Time, time.Time, bool) {
	day, year, suffixed := cldrMonthNumbers(raw, at, n)
	switch {
	case inflected && year == "" && !suffixed:
		// an inflected name ("augustine 15" on the stem of "augusts") also needs a year or a
		// suffixed number ("15ean") to be read as a date
	case day > 0:
		return inYear(year, now, func(y int) (time.Time, time.Time, bool) { return dateSpan(y, month, day, now.Location()) })
	case year != "":
		return monthSpan(atoi(year), month, "", now.Location())
	}
	return time.Time{}, time.Time{}, false
}

// cldrMonthNumbers finds the day (1..31, right next to the name or one short word away)
// and the 4-digit year (up to three words before: Latvian "2026. gada 15. janvārī")
// around the month name at raw[at:at+n]; suffixed is whether either
// carried a case ending ("15ean", "2026ko").
func cldrMonthNumbers(raw []string, at, n int) (day int, year string, suffixed bool) {
	for _, i := range []int{at - 1, at + n, at - 2, at + n + 1, at - 3} {
		if i < 0 || i >= len(raw) {
			continue
		}
		num := leadingNumber(raw[i])
		v, err := strconv.Atoi(num)
		switch {
		case err != nil:
			continue
		case len(num) == 4 && year == "":
			year = num
		case len(num) <= 2 && v >= 1 && v <= 31 && day == 0 && (i == at-1 || i == at+n || i == at-2 && utf8.RuneCountInString(raw[at-1]) <= 3):
			day = v
		default:
			continue
		}
		suffixed = suffixed || num != raw[i]
	}
	return day, year, suffixed
}

// slashDateRe is a day/month/year or month/day/year date: 15/01/2026, 1/15/2026.
var slashDateRe = regexp.MustCompile(`\b(\d{1,2})/(\d{1,2})/(\d{4})\b`)

// slashDateSpan reads a slash date: a side over 12 can only be the day; otherwise
// order ('d' day first, 'm' month first) decides, and without one the date is not read.
func slashDateSpan(m []string, order func() byte, now time.Time) (time.Time, time.Time, bool) {
	a, b, y := atoi(m[1]), atoi(m[2]), atoi(m[3])
	if a > 12 || a == b || b <= 12 && order() == 'd' {
		return dateSpan(y, b, a, now.Location())
	}
	if b > 12 || order() == 'm' {
		return dateSpan(y, a, b, now.Location())
	}
	return time.Time{}, time.Time{}, false
}

// slashDateOrder is the reader's numeric date order: the Accept-Language locale's CLDR
// short date order, else the query language's when all its CLDR locales agree
// (German is day first in every German locale; English and Spanish are written both ways), else 0.
// shortcut: the timezone's country is not read; add it if readers without a locale ask.
func slashDateOrder(locale string, score []int) byte {
	if o := localeDateOrder(locale); o != 0 {
		return o
	}
	best, at := 0, -1
	for i, s := range score {
		switch {
		case s > best:
			best, at = s, i
		case s == best:
			at = -1
		}
	}
	if at < 0 {
		return 0
	}
	var order byte
	for _, code := range strings.Split(timeLexicons[at].lang, "/") {
		if o := cldrDateOrders()["*"+code]; o == 0 || order != 0 && o != order {
			return 0
		} else {
			order = o
		}
	}
	return order
}

// localeDateOrder is the order of a locale tag ("en-GB,en;q=0.9"): the nearest CLDR locale
// for a tag with a region or script (en-US inherits en), the agreed order for a bare language.
func localeDateOrder(tag string) byte {
	tag, _, _ = strings.Cut(tag, ",")
	tag, _, _ = strings.Cut(tag, ";")
	tag = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(tag), "_", "-"))
	orders := cldrDateOrders()
	if tag == "" || !strings.Contains(tag, "-") {
		return orders["*"+tag]
	}
	for t := tag; t != ""; t = t[:max(strings.LastIndexByte(t, '-'), 0)] {
		if o, ok := orders[t]; ok {
			return o
		}
	}
	return 0
}

// cldrDateOrders maps each lower-cased CLDR locale to 'd' or 'm' ('y' year-first locales
// read no slash date), and "*<language>" to the order all its locales agree on (0 when
// they disagree or the language itself is year first).
var cldrDateOrders = sync.OnceValue(func() map[string]byte {
	out := map[string]byte{}
	for _, line := range strings.Split(cldrDateOrderData, "\n") {
		loc, o, _ := strings.Cut(strings.ToLower(line), "\t")
		if len(o) != 1 {
			continue
		}
		out[loc] = 0
		if o != "y" {
			out[loc] = o[0]
		}
		base, _, _ := strings.Cut(loc, "-")
		switch prev, seen := out["*"+base]; {
		case o == "y" && loc != base: // a year-first region (fr-CA) says nothing about its language
		case o == "y" || seen && prev != o[0]:
			out["*"+base] = 0 // a year-first language (zh) or one written both ways (en, es)
		case !seen:
			out["*"+base] = o[0]
		}
	}
	return out
})

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
