package memory

import (
	"testing"
	"time"
)

// The CLDR fallback reads languages the hand-tuned lexicons do not cover.
func TestParseTimeIntentCLDRFallback(t *testing.T) {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, recallLocation("")) // a Thursday
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, now.Location()) }
	for q, want := range map[string][2]time.Time{
		"tulad ng sinabi ko kahapon":         {day(2026, 11, 4), day(2026, 11, 5)},   // Tagalog yesterday
		"tulikuwa na mkutano wiki iliyopita": {day(2026, 10, 26), day(2026, 11, 2)},  // Swahili last week
		"què vam decidir fa 3 dies":          {day(2026, 11, 2), day(2026, 11, 3)},   // Catalan 3 days ago
		"la reunió del 15 de gener":          {day(2026, 1, 15), day(2026, 1, 16)},   // Catalan month with day
		"cyfarfod ionawr 2025":               {day(2025, 1, 1), day(2025, 2, 1)},     // Welsh month with year
		"what did we ship last monday":       {day(2026, 10, 26), day(2026, 10, 27)}, // weekday phrases for every language
		"¿qué pasó el lunes pasado?":         {day(2026, 10, 26), day(2026, 10, 27)},
		"this friday's deploy":               {day(2026, 11, 6), day(2026, 11, 7)},
	} {
		w, ok := parseTimeIntent(q, now)
		if !ok || !w.From.Equal(want[0]) || !w.To.Equal(want[1]) {
			t.Errorf("%q: %v..%v %v (%q)", q, w.From, w.To, ok, w.Phrase)
		}
	}
	// Hand-tuned rules decide first: Spanish "ayer" keeps its own reading and phrase.
	if w, ok := parseTimeIntent("lo que hicimos ayer el lunes pasado", now); !ok || w.Phrase != "ayer" {
		t.Errorf("%v %v", w, ok)
	}
}

// Short or ambiguous CLDR words are not time words on their own.
func TestParseTimeIntentCLDRFalsePositives(t *testing.T) {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, recallLocation(""))
	for _, q := range []string{
		"jana wrote the report", // Swahili "yesterday", also a name
		"leo fixed it",          // Swahili "today"
		"gener alone",           // a month name without a day or year
		"ionawr plans",
		"monday standup notes", // a weekday without last/this
		"the 2026 roadmap",
	} {
		if w, ok := parseTimeIntent(q, now); ok {
			t.Errorf("%q: unexpected window %v (%q)", q, w, w.Phrase)
		}
	}
	if cldrTimeLocales < 200 || len(cldrTime()) < 3000 {
		t.Fatalf("locales %d, phrases %d", cldrTimeLocales, len(cldrTime()))
	}
}
