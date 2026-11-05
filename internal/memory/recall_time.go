package memory

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// timeWindow is the half-open span [From, To) a query's time words point at.
type timeWindow struct {
	From, To time.Time
	Phrase   string
}

// timeRule turns one regexp match into a window. ok=false means the phrase names no
// real span (2026-02-30), so parsing stops, unless retry lets the next match try.
// lang is the timeLexicons entry whose words the rule reads, -1 for numeric forms.
type timeRule struct {
	re    *regexp.Regexp
	span  func(m []string, now time.Time) (time.Time, time.Time, bool)
	retry bool
	lang  int
}

// Relative time words, in the order a language's alternatives are tried at one position.
const (
	relBefore = iota // the day before yesterday
	relYesterday
	relToday
	relLastWeek
	relThisWeek
	relLastMonth
	relThisMonth
	relTwoYears // the year before last
	relLastYear
	relThisYear
	relKinds
)

var relSpans = [relKinds]func(now time.Time) (time.Time, time.Time, bool){
	func(n time.Time) (time.Time, time.Time, bool) { return daySpan(n, -2) },
	func(n time.Time) (time.Time, time.Time, bool) { return daySpan(n, -1) },
	func(n time.Time) (time.Time, time.Time, bool) { return daySpan(n, 0) },
	func(n time.Time) (time.Time, time.Time, bool) { return weekSpan(n, -1) },
	func(n time.Time) (time.Time, time.Time, bool) { return weekSpan(n, 0) },
	func(n time.Time) (time.Time, time.Time, bool) { return agoSpan(n, 2, 1) },
	func(n time.Time) (time.Time, time.Time, bool) { return agoSpan(n, 2, 0) },
	func(n time.Time) (time.Time, time.Time, bool) { return yearSpan(n, -2) },
	func(n time.Time) (time.Time, time.Time, bool) { return yearSpan(n, -1) },
	func(n time.Time) (time.Time, time.Time, bool) { return yearSpan(n, 0) },
}

// timeLexicon is one language's time words (data in recall_time_lexicon.go).
type timeLexicon struct {
	lang string
	// noLeft/noRight drop the word boundary on that side: scripts written without
	// spaces, and Korean particles glued after a word.
	noLeft, noRight bool
	months          [12]string // forms of each month name
	monthFirst      bool       // "October 31" as well as "31 October"
	yearFirst       bool       // "2026. október 31."
	// bare lets a month name stand alone (longer than 3 letters and not a stop word);
	// otherwise it needs a word from in/early/mid/late, an after ending or a year.
	bare                 bool
	in, early, mid, late string // words before a month name
	after                string // pattern right after a month name meaning "in" (postposition, case ending)
	nums                 [10]string
	numExtra             string    // pattern for other numerals (CJK)
	units                [4]string // patterns: day, week, month, year
	ago                  []string  // patterns with {n} and {u}; a space is optional
	rel                  [relKinds]string
	// stop holds frequent ordinary words, to tell languages apart (foreignWord).
	stop string

	monthOf, numOf map[string]int
	partOf         map[string]string
	stopSet        map[string]bool
}

// cjkParts are the month parts the Korean, Japanese and Chinese month rules read.
var cjkParts = map[string]string{"초": "early", "중순": "mid", "말": "late", "上旬": "early", "初": "early", "初め": "early", "中旬": "mid", "下旬": "late", "末": "late", "底": "late"}

var timeRules = buildTimeRules()

// buildTimeRules: numeric forms first, then for every language dates, month names,
// "N units ago" and relative words, each stage across all languages before the next.
func buildTimeRules() []timeRule {
	rules := numericTimeRules()
	var stages [4][]timeRule
	for i := range timeLexicons {
		timeLexicons[i].prepare()
		for st, rs := range timeLexicons[i].rules(i) {
			stages[st] = append(stages[st], rs...)
		}
	}
	for _, rs := range stages {
		rules = append(rules, rs...)
	}
	return rules
}

func numericTimeRules() []timeRule {
	cjkMonth := func(m []string, now time.Time) (time.Time, time.Time, bool) {
		return inYear(m[1], now, func(y int) (time.Time, time.Time, bool) {
			if m[3] != "" {
				return dateSpan(y, atoi(m[2]), atoi(m[3]), now.Location())
			}
			return monthSpan(y, atoi(m[2]), cjkParts[m[4]], now.Location())
		})
	}
	return []timeRule{
		{regexp.MustCompile(`(\d{4})[-./](\d{1,2})[-./](\d{1,2})`), func(m []string, now time.Time) (time.Time, time.Time, bool) {
			return dateSpan(atoi(m[1]), atoi(m[2]), atoi(m[3]), now.Location())
		}, false, -1},
		// ISO week: 2026-W44, 2026W44.
		{regexp.MustCompile(`\b(\d{4})-?w(\d{1,2})\b`), func(m []string, now time.Time) (time.Time, time.Time, bool) {
			return isoWeekSpan(atoi(m[1]), atoi(m[2]), now.Location())
		}, false, -1},
		{regexp.MustCompile(`(\d{4})[-./](\d{1,2})\b`), func(m []string, now time.Time) (time.Time, time.Time, bool) {
			return monthSpan(atoi(m[1]), atoi(m[2]), "", now.Location())
		}, false, -1},
		{regexp.MustCompile(`(?:(\d{4})\s*년\s*)?(\d{1,2})\s*월\s*(?:(\d{1,2})\s*일|(초|중순|말))?`), cjkMonth, false, -1},
		// Japanese and Chinese: 2026年10月31日, 10月31号, 10月下旬, 10月末, 10月底.
		{regexp.MustCompile(`(?:(\d{4})\s*年\s*)?(\d{1,2})\s*月\s*(?:(\d{1,2})\s*[日号號]|(上旬|中旬|下旬|初め?|末|底))?`), cjkMonth, false, -1},
		// Day first: 31.10.2026, 31-10-2026 (slashes stay out, they are ambiguous).
		{regexp.MustCompile(`\b(\d{1,2})[.-](\d{1,2})[.-](\d{4})\b`), func(m []string, now time.Time) (time.Time, time.Time, bool) {
			return dateSpan(atoi(m[3]), atoi(m[2]), atoi(m[1]), now.Location())
		}, false, -1},
		// A year alone in Korean, Japanese or Chinese: 2025년, 2025年.
		{regexp.MustCompile(`(\d{4})\s*[년年]`), func(m []string, now time.Time) (time.Time, time.Time, bool) {
			if y := atoi(m[1]); y < 1970 || y > 9999 {
				return time.Time{}, time.Time{}, false
			}
			return yearSpan(time.Date(atoi(m[1]), 1, 1, 0, 0, 0, 0, now.Location()), 0)
		}, false, -1},
	}
}

// prepare folds the word lists into lookup maps.
func (l *timeLexicon) prepare() {
	l.monthOf, l.numOf, l.partOf, l.stopSet = map[string]int{}, map[string]int{}, map[string]string{}, map[string]bool{}
	for i := range 12 {
		for _, w := range splitWords(l.months[i]) {
			l.monthOf[w] = i + 1
		}
	}
	for i, words := range l.nums {
		for _, w := range splitWords(words) {
			l.numOf[w] = i + 1
		}
	}
	for part, words := range map[string]string{"": l.in, "early": l.early, "mid": l.mid, "late": l.late} {
		for _, w := range splitWords(words) {
			l.partOf[w] = part
		}
	}
	for _, w := range strings.Fields(foldTimeText(l.stop)) {
		l.stopSet[w] = true
	}
}

const (
	timeEdge     = `[^\p{L}\p{M}\p{N}]`
	timeYearTail = `(?:[\s,]*(?:(?:de|del|of|năm)\s+)?(\d{4}))?`
)

// rules builds the language's rules for the four stages of buildTimeRules.
func (l *timeLexicon) rules(lang int) [4][]timeRule {
	var out [4][]timeRule
	add := func(stage int, pattern string, retry bool, span func(m []string, now time.Time) (time.Time, time.Time, bool)) {
		out[stage] = append(out[stage], timeRule{l.wrap(pattern), span, retry, lang})
	}
	if len(l.monthOf) > 0 {
		months := literalAlt(l.monthOf)
		add(0, `(\d{1,2})(?:\.|er|º|st|nd|rd|th)?\s*(?:(?:de|of|di)\s+)?(`+months+`)`+timeYearTail, false, func(m []string, now time.Time) (time.Time, time.Time, bool) {
			return inYear(m[3], now, func(y int) (time.Time, time.Time, bool) {
				return dateSpan(y, l.monthOf[m[2]], atoi(m[1]), now.Location())
			})
		})
		if l.monthFirst {
			add(0, `(`+months+`)\.?\s+(\d{1,2})(?:st|nd|rd|th)?(?:,?\s+(\d{4}))?`, false, func(m []string, now time.Time) (time.Time, time.Time, bool) {
				return inYear(m[3], now, func(y int) (time.Time, time.Time, bool) {
					return dateSpan(y, l.monthOf[m[1]], atoi(m[2]), now.Location())
				})
			})
		}
		if l.yearFirst {
			add(0, `(\d{4})\.?\s*(`+months+`)\s*(\d{1,2})\.?`, false, func(m []string, now time.Time) (time.Time, time.Time, bool) {
				return dateSpan(atoi(m[1]), l.monthOf[m[2]], atoi(m[3]), now.Location())
			})
		}
		add(1, `(?:(`+literalAlt(l.partOf)+`)\s*)?(`+months+`)(`+foldTimeText(l.after)+`)?`+timeYearTail, true, l.monthPhrase)
	}
	units := make([]string, 4)
	for i, u := range l.units {
		units[i] = "(" + foldTimeText(u) + ")"
	}
	num := `(\d{1,3}` + prefixAlt(literalAlt(l.numOf)) + prefixAlt(l.numExtra) + `)`
	for _, tpl := range l.ago {
		p := strings.ReplaceAll(foldTimeText(tpl), " ", " ?")
		p = strings.NewReplacer("{n}", num, "{u}", "(?:"+strings.Join(units, "|")+")").Replace(p)
		add(2, p, false, func(m []string, now time.Time) (time.Time, time.Time, bool) {
			unit := slices.IndexFunc(m[2:], func(s string) bool { return s != "" })
			return agoSpan(now, unit, l.number(m[1]))
		})
	}
	var kinds, alts []string
	for k, p := range l.rel {
		if p != "" {
			kinds, alts = append(kinds, strconv.Itoa(k)), append(alts, "("+foldTimeText(p)+")")
		}
	}
	if len(alts) > 0 {
		add(3, strings.Join(alts, "|"), false, func(m []string, now time.Time) (time.Time, time.Time, bool) {
			k := slices.IndexFunc(m[1:], func(s string) bool { return s != "" })
			return relSpans[atoi(kinds[k])](now)
		})
	}
	return out
}

// monthPhrase reads "in October", "fin octobre", "Ekim'de", "octubre de 2026".
func (l *timeLexicon) monthPhrase(m []string, now time.Time) (time.Time, time.Time, bool) {
	prep, name, after, year := m[1], m[2], m[3], m[4]
	if prep == "" && after == "" && year == "" && (!l.bare || utf8.RuneCountInString(name) <= 3 || l.stopSet[name]) {
		return time.Time{}, time.Time{}, false // "mars rover", "may I": a bare name is also a word
	}
	return inYear(year, now, func(y int) (time.Time, time.Time, bool) {
		return monthSpan(y, l.monthOf[name], l.partOf[prep], now.Location())
	})
}

// wrap adds the word boundaries (Unicode-aware; \b is ASCII-only in RE2).
func (l *timeLexicon) wrap(p string) *regexp.Regexp {
	if !l.noLeft {
		p = `(?:^|` + timeEdge + `)(?:` + p + `)`
	}
	if !l.noRight {
		p = `(?:` + p + `)(?:$|` + timeEdge + `)`
	}
	return regexp.MustCompile(p)
}

func (l *timeLexicon) number(s string) int {
	if n, ok := l.numOf[s]; ok {
		return n
	}
	return cjkInt(s)
}

func splitWords(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(foldTimeText(s), "|")
}

// literalAlt is the keys as a literal alternation, longest first so "juillet" beats "juli".
func literalAlt[V any](words map[string]V) string {
	keys := make([]string, 0, len(words))
	for w := range words {
		if w != "" {
			keys = append(keys, w)
		}
	}
	slices.SortFunc(keys, func(a, b string) int {
		if d := utf8.RuneCountInString(b) - utf8.RuneCountInString(a); d != 0 {
			return d
		}
		return strings.Compare(a, b)
	})
	for i, k := range keys {
		keys[i] = regexp.QuoteMeta(k)
	}
	return strings.Join(keys, "|")
}

func prefixAlt(p string) string {
	if p == "" {
		return ""
	}
	return "|" + p
}

var timeFold = strings.NewReplacer("’", "'", "ς", "σ", "i̇", "i", "ş", "ș", "ţ", "ț", "ي", "ی", "ك", "ک")

// foldTimeText lower-cases s, unifies letter variants (apostrophes, final sigma,
// Romanian comma letters, Arabic/Persian yeh and kaf) and turns any decimal digits
// (Arabic-Indic, Devanagari, Bengali, Thai, full-width) into ASCII.
func foldTimeText(s string) string {
	return strings.Map(asciiDigit, timeFold.Replace(strings.ToLower(s)))
}

func asciiDigit(r rune) rune {
	if r <= '9' || !unicode.IsDigit(r) {
		return r
	}
	zero := r
	for unicode.IsDigit(zero - 1) {
		zero--
	}
	return '0' + (r-zero)%10
}

func notTimeWordRune(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsMark(r) && !unicode.IsDigit(r)
}

// timeLanguageScores counts each language's stop words in q.
func timeLanguageScores(q string) []int {
	score := make([]int, len(timeLexicons))
	for _, w := range strings.FieldsFunc(q, notTimeWordRune) {
		for j := range timeLexicons {
			if timeLexicons[j].stopSet[w] {
				score[j]++
			}
		}
	}
	return score
}

// foreignWord: a one-word phrase that is an ordinary word of a language the query
// reads as more than the rule's own is not a time word ("hier" in a German sentence).
func (r timeRule) foreignWord(phrase string, score []int) bool {
	if r.lang < 0 || strings.Contains(phrase, " ") {
		return false
	}
	for j := range timeLexicons {
		if j != r.lang && timeLexicons[j].stopSet[phrase] && score[j]-1 > score[r.lang] {
			return true
		}
	}
	return false
}

// parseTimeIntent finds the first time phrase in query (numeric forms, then the
// languages of timeLexicons) and resolves it against now, whose location is the
// reader's calendar.
func parseTimeIntent(query string, now time.Time) (timeWindow, bool) {
	q := foldTimeText(strings.Join(strings.Fields(query), " "))
	score := timeLanguageScores(q)
	for _, rule := range timeRules {
		for _, m := range rule.re.FindAllStringSubmatch(q, -1) {
			phrase := strings.TrimFunc(m[0], notTimeWordRune)
			if rule.foreignWord(phrase, score) {
				continue
			}
			if from, to, ok := rule.span(m, now); ok {
				return timeWindow{From: from, To: to, Phrase: phrase}, true
			} else if !rule.retry {
				return timeWindow{}, false
			}
		}
	}
	return timeWindow{}, false
}

// agoSpan is the day, week, month or year (unit 0..3) n units before now's.
func agoSpan(now time.Time, unit, n int) (time.Time, time.Time, bool) {
	switch unit {
	case 0:
		return daySpan(now, -n)
	case 1:
		return weekSpan(now, -n)
	case 2:
		first := time.Date(now.Year(), now.Month()-time.Month(n), 1, 0, 0, 0, 0, now.Location())
		return monthSpan(first.Year(), int(first.Month()), "", now.Location())
	}
	return yearSpan(now, -n)
}

// isoWeekSpan is ISO week w of year y (Monday first; week 1 holds January 4).
func isoWeekSpan(y, w int, loc *time.Location) (time.Time, time.Time, bool) {
	jan4 := time.Date(y, 1, 4, 0, 0, 0, 0, loc)
	from := jan4.AddDate(0, 0, -(int(jan4.Weekday())+6)%7+7*(w-1))
	if gy, gw := from.ISOWeek(); y < 1970 || y > 9999 || gy != y || gw != w {
		return time.Time{}, time.Time{}, false
	}
	return from, from.AddDate(0, 0, 7), true
}

// recallLocation is the reader's calendar: the request's IANA zone, else Asia/Seoul.
// boundSearchInput already rejected names this host cannot load.
func recallLocation(name string) *time.Location {
	if name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return usageZone()
}

// validTimezone accepts empty (Asia/Seoul) or an IANA name this host can load.
func validTimezone(name string) bool {
	if name == "" {
		return true
	}
	_, err := time.LoadLocation(name)
	return len(name) <= 64 && err == nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// cjkInt reads Arabic digits or a Chinese-character number up to 99 (三, 十二, 二十一).
func cjkInt(s string) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	n, digit := 0, 0
	for _, r := range s {
		if r == '十' {
			n, digit = n+max(digit, 1)*10, 0
		} else {
			digit = strings.IndexRune("一二三四五六七八九", r)/len("一") + 1
			if r == '两' || r == '兩' {
				digit = 2
			}
		}
	}
	return n + digit
}

// inYear resolves a month phrase without a year to the latest such span not after now
// ("12월" in January means last December).
func inYear(year string, now time.Time, build func(int) (time.Time, time.Time, bool)) (time.Time, time.Time, bool) {
	if year != "" {
		return build(atoi(year))
	}
	from, to, ok := build(now.Year())
	if ok && from.After(now) {
		return build(now.Year() - 1)
	}
	return from, to, ok
}

func dateSpan(y, m, d int, loc *time.Location) (time.Time, time.Time, bool) {
	from := time.Date(y, time.Month(m), d, 0, 0, 0, 0, loc)
	if y < 1970 || y > 9999 || int(from.Month()) != m || from.Day() != d {
		return time.Time{}, time.Time{}, false
	}
	return from, from.AddDate(0, 0, 1), true
}

// monthSpan is a calendar month, or its early (1-10), mid (11-20) or late (21-end) part.
// Month 0 is December of the year before, so "last month" in January works.
func monthSpan(y, m int, part string, loc *time.Location) (time.Time, time.Time, bool) {
	if y < 1970 || y > 9999 || m < 0 || m > 12 {
		return time.Time{}, time.Time{}, false
	}
	from := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, loc)
	to := from.AddDate(0, 1, 0)
	switch part {
	case "early":
		to = from.AddDate(0, 0, 10)
	case "mid":
		from, to = from.AddDate(0, 0, 10), from.AddDate(0, 0, 20)
	case "late":
		from = from.AddDate(0, 0, 20)
	}
	return from, to, true
}

func daySpan(now time.Time, offset int) (time.Time, time.Time, bool) {
	from := startOfDay(now).AddDate(0, 0, offset)
	return from, from.AddDate(0, 0, 1), true
}

// weekSpan is a Monday-first week, offset weeks from now's.
func weekSpan(now time.Time, offset int) (time.Time, time.Time, bool) {
	monday := startOfDay(now).AddDate(0, 0, -(int(now.Weekday())+6)%7+7*offset)
	return monday, monday.AddDate(0, 0, 7), true
}

func yearSpan(now time.Time, offset int) (time.Time, time.Time, bool) {
	from := time.Date(now.Year()+offset, 1, 1, 0, 0, 0, 0, now.Location())
	return from, from.AddDate(1, 0, 0), true
}

// hitEventTime is when a memory happened: the start of the event span its content
// states (occurred), its observed_at, then a dated valid_from, then its content time (hitContentTime).
func hitEventTime(h Hit, loc *time.Location) time.Time {
	if h.Occurred != nil && !h.Occurred.From.IsZero() {
		return h.Occurred.From
	}
	if c := h.MemoryContext; c != nil {
		if c.ObservedAt != nil && !c.ObservedAt.IsZero() {
			return *c.ObservedAt
		}
		if t, err := time.Parse(time.RFC3339, c.ValidFrom); err == nil {
			return t
		}
		if len(c.ValidFrom) >= 10 {
			if t, err := time.ParseInLocation("2006-01-02", c.ValidFrom[:10], loc); err == nil {
				return t
			}
		}
	}
	return hitContentTime(h)
}

func inWindow(t time.Time, w timeWindow) bool { return !t.Before(w.From) && t.Before(w.To) }

// hitInWindow: a stated event span counts when it overlaps w ("last week" finds a
// three-day outage that began the Sunday before); other hits by their event time.
func hitInWindow(h Hit, w timeWindow, loc *time.Location) bool {
	if o := h.Occurred; o != nil && !o.From.IsZero() {
		return o.From.Before(w.To) && w.From.Before(o.To)
	}
	return inWindow(hitEventTime(h, loc), w)
}
