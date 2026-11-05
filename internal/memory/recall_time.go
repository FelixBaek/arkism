package memory

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// timeWindow is the half-open span [From, To) a query's time words point at.
type timeWindow struct {
	From, To time.Time
	Phrase   string
}

// timeRule turns one regexp match into a window. ok=false means the phrase names no
// real span (2026-02-30), so parsing stops, unless retry lets the next rule try.
type timeRule struct {
	re    *regexp.Regexp
	span  func(m []string, now time.Time) (time.Time, time.Time, bool)
	retry bool
}

const (
	monthNames = `jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?`
	monthFull  = `january|february|march|april|may|june|july|august|september|october|november|december`
)

var monthParts = map[string]string{"초": "early", "중순": "mid", "말": "late", "early": "early", "beginning of": "early", "start of": "early", "mid": "mid", "middle of": "mid", "late": "late", "end of": "late"}

// timeRules run in order; explicit dates win over month names, which win over relative words.
var timeRules = []timeRule{
	{regexp.MustCompile(`(\d{4})[-./](\d{1,2})[-./](\d{1,2})`), func(m []string, now time.Time) (time.Time, time.Time, bool) {
		return dateSpan(atoi(m[1]), atoi(m[2]), atoi(m[3]), now.Location())
	}, false},
	{regexp.MustCompile(`(\d{4})[-./](\d{1,2})\b`), func(m []string, now time.Time) (time.Time, time.Time, bool) {
		return monthSpan(atoi(m[1]), atoi(m[2]), "", now.Location())
	}, false},
	{regexp.MustCompile(`(?:(\d{4})\s*년\s*)?(\d{1,2})\s*월\s*(?:(\d{1,2})\s*일|(초|중순|말))?`), func(m []string, now time.Time) (time.Time, time.Time, bool) {
		return inYear(m[1], now, func(y int) (time.Time, time.Time, bool) {
			if m[3] != "" {
				return dateSpan(y, atoi(m[2]), atoi(m[3]), now.Location())
			}
			return monthSpan(y, atoi(m[2]), monthParts[m[4]], now.Location())
		})
	}, false},
	{regexp.MustCompile(`\b(` + monthNames + `)\s+(\d{1,2})(?:st|nd|rd|th)?\b(?:,?\s+(\d{4}))?`), func(m []string, now time.Time) (time.Time, time.Time, bool) {
		return inYear(m[3], now, func(y int) (time.Time, time.Time, bool) {
			return dateSpan(y, monthIndex(m[1]), atoi(m[2]), now.Location())
		})
	}, false},
	{regexp.MustCompile(`\b(\d{1,2})(?:st|nd|rd|th)?\s+(?:of\s+)?(` + monthNames + `)\b(?:,?\s+(\d{4}))?`), func(m []string, now time.Time) (time.Time, time.Time, bool) {
		return inYear(m[3], now, func(y int) (time.Time, time.Time, bool) {
			return dateSpan(y, monthIndex(m[2]), atoi(m[1]), now.Location())
		})
	}, false},
	{regexp.MustCompile(`\b(end of|late|early|beginning of|start of|mid|middle of)\s+(` + monthFull + `)\b(?:\s+(\d{4}))?`), func(m []string, now time.Time) (time.Time, time.Time, bool) {
		return inYear(m[3], now, func(y int) (time.Time, time.Time, bool) {
			return monthSpan(y, monthIndex(m[2]), monthParts[m[1]], now.Location())
		})
	}, false},
	{regexp.MustCompile(`\b(in\s+|during\s+)?(` + monthFull + `)\b(?:\s+(\d{4}))?`), func(m []string, now time.Time) (time.Time, time.Time, bool) {
		// "may" and "march" are also verbs: only "in May", "during March" or "May 2026" count.
		if (m[2] == "may" || m[2] == "march") && m[1] == "" && m[3] == "" {
			return time.Time{}, time.Time{}, false
		}
		return inYear(m[3], now, func(y int) (time.Time, time.Time, bool) { return monthSpan(y, monthIndex(m[2]), "", now.Location()) })
	}, true},
	{regexp.MustCompile(`(\d{1,3})\s*일\s*전|\b(\d{1,3}) days? ago\b`), func(m []string, now time.Time) (time.Time, time.Time, bool) {
		return daySpan(now, -atoi(m[1]+m[2]))
	}, false},
	{regexp.MustCompile(`그저께|그제|\bday before yesterday\b`), func(_ []string, now time.Time) (time.Time, time.Time, bool) { return daySpan(now, -2) }, false},
	{regexp.MustCompile(`어제|\byesterday\b`), func(_ []string, now time.Time) (time.Time, time.Time, bool) { return daySpan(now, -1) }, false},
	{regexp.MustCompile(`오늘|\btoday\b`), func(_ []string, now time.Time) (time.Time, time.Time, bool) { return daySpan(now, 0) }, false},
	{regexp.MustCompile(`(?:지난|저번)\s*주|\blast week\b`), func(_ []string, now time.Time) (time.Time, time.Time, bool) { return weekSpan(now, -1) }, false},
	{regexp.MustCompile(`이번\s*주|금주|\bthis week\b`), func(_ []string, now time.Time) (time.Time, time.Time, bool) { return weekSpan(now, 0) }, false},
	{regexp.MustCompile(`(?:지난|저번)\s*달|\blast month\b`), func(_ []string, now time.Time) (time.Time, time.Time, bool) {
		return monthSpan(now.Year(), int(now.Month())-1, "", now.Location())
	}, false},
	{regexp.MustCompile(`이번\s*달|\bthis month\b`), func(_ []string, now time.Time) (time.Time, time.Time, bool) {
		return monthSpan(now.Year(), int(now.Month()), "", now.Location())
	}, false},
	{regexp.MustCompile(`재작년`), func(_ []string, now time.Time) (time.Time, time.Time, bool) { return yearSpan(now, -2) }, false},
	{regexp.MustCompile(`작년|지난\s*해|\blast year\b`), func(_ []string, now time.Time) (time.Time, time.Time, bool) { return yearSpan(now, -1) }, false},
	{regexp.MustCompile(`올해|금년|\bthis year\b`), func(_ []string, now time.Time) (time.Time, time.Time, bool) { return yearSpan(now, 0) }, false},
}

// parseTimeIntent finds the first time phrase in query (Korean or English) and
// resolves it against now, whose location is the reader's calendar.
func parseTimeIntent(query string, now time.Time) (timeWindow, bool) {
	q := strings.ToLower(query)
	for _, rule := range timeRules {
		m := rule.re.FindStringSubmatch(q)
		if m == nil {
			continue
		}
		if from, to, ok := rule.span(m, now); ok {
			return timeWindow{From: from, To: to, Phrase: strings.TrimSpace(m[0])}, true
		} else if !rule.retry {
			return timeWindow{}, false
		}
	}
	return timeWindow{}, false
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

// monthIndex maps an English month name or its abbreviation to 1..12.
func monthIndex(name string) int {
	return strings.Index("janfebmaraprmayjunjulaugsepoctnovdec", name[:3])/3 + 1
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

// hitEventTime is when a memory happened: its observed_at, then a dated valid_from,
// then its content time (hitContentTime).
func hitEventTime(h Hit, loc *time.Location) time.Time {
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
