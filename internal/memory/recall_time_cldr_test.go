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
		"ang pulong ng enero 15, 2026":       {day(2026, 1, 15), day(2026, 1, 16)},   // Tagalog, Spanish month name in month-first order
		"what did we ship last monday":       {day(2026, 10, 26), day(2026, 10, 27)}, // weekday phrases for every language
		"¿qué pasó el lunes pasado?":         {day(2026, 10, 26), day(2026, 10, 27)},
		"this friday's deploy":               {day(2026, 11, 6), day(2026, 11, 7)},
	} {
		w, ok := parseTimeIntent(q, "", now)
		if !ok || !w.From.Equal(want[0]) || !w.To.Equal(want[1]) {
			t.Errorf("%q: %v..%v %v (%q)", q, w.From, w.To, ok, w.Phrase)
		}
	}
	// Hand-tuned rules decide first: Spanish "ayer" keeps its own reading and phrase.
	if w, ok := parseTimeIntent("lo que hicimos ayer el lunes pasado", "", now); !ok || w.Phrase != "ayer" {
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
		if w, ok := parseTimeIntent(q, "", now); ok {
			t.Errorf("%q: unexpected window %v (%q)", q, w, w.Phrase)
		}
	}
	if cldrTimeLocales < 200 || len(cldrTime()) < 3000 {
		t.Fatalf("locales %d, phrases %d", cldrTimeLocales, len(cldrTime()))
	}
}

// Slash dates read by side size, then by the reader's locale; never guessed.
func TestParseTimeIntentSlashDates(t *testing.T) {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, recallLocation(""))
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, now.Location()) }
	for _, c := range []struct {
		q, locale string
		want      time.Time // zero: not read
	}{
		{"meeting 15/01/2026", "", day(1, 15)},              // only day first fits
		{"meeting 01/15/2026", "", day(1, 15)},              // only month first fits
		{"meeting 4/4/2026", "", day(4, 4)},                 // both readings agree
		{"meeting 03/04/2026", "", time.Time{}},             // ambiguous, no locale: not read
		{"what did we ship 03/04/2026", "", time.Time{}},    // English is written both ways
		{"meeting 03/04/2026", "en-US", day(3, 4)},          // en-US inherits en: month first
		{"meeting 03/04/2026", "en-GB,en;q=0.9", day(4, 3)}, // day first
		{"meeting 03/04/2026", "en", time.Time{}},           // bare English: locales disagree
		{"meeting 03/04/2026", "fr-FR", day(4, 3)},
		{"meeting 03/04/2026", "de_DE", day(4, 3)},
		{"meeting 03/04/2026", "en-PH", day(3, 4)},
		{"meeting 03/04/2026", "ja-JP", time.Time{}}, // year-first locale, English query
		{"meeting 03/04/2026", "es-US", day(4, 3)},
		{"la reunión del 03/04/2026", "", time.Time{}}, // Spanish: Panama and Puerto Rico write month first
		{"la reunión del 03/04/2026", "es-ES", day(4, 3)},
		{"wir haben am 03/04/2026 entschieden", "", day(4, 3)}, // German: day first everywhere
		{"会议 03/04/2026", "", time.Time{}},                     // Chinese is year first
		{"la réunion du 03/04/2026", "", day(4, 3)},            // French: day first (fr-CA is year first)
		{"la reunión del 03/04/2026", "en-US", day(3, 4)},      // the locale outranks the query language
		{"meeting 31/02/2026", "", time.Time{}},                // no such day
		{"meeting 13/13/2026", "", time.Time{}},
		{"meeting 15.01.2026", "", day(1, 15)}, // dots and dashes stay day first
		{"meeting 2026/01/15", "", day(1, 15)},
		{"meeting 2026.01.15", "", day(1, 15)},
		{"ratio 3/4 of 2026 budget", "", time.Time{}}, // not a date
	} {
		w, ok := parseTimeIntent(c.q, c.locale, now)
		if c.want.IsZero() {
			if ok {
				t.Errorf("%q %q: unexpected %v (%q)", c.q, c.locale, w.From, w.Phrase)
			}
			continue
		}
		if !ok || !w.From.Equal(c.want) || !w.To.Equal(c.want.AddDate(0, 0, 1)) {
			t.Errorf("%q %q: %v..%v %v, want %v", c.q, c.locale, w.From, w.To, ok, c.want)
		}
	}
}

func TestLocaleDateOrder(t *testing.T) {
	for tag, want := range map[string]byte{"": 0, "en": 0, "en-US": 'm', "en-gb": 'd', "en-CA": 0, "es": 0, "es-MX": 'd', "es-PR": 'm', "fr": 'd', "de": 'd', "pt-BR": 'd',
		"zh-Hant-HK": 'd', "zh": 0, "ko-KR": 0, "xx-YY": 0, "sw": 'd'} {
		if got := localeDateOrder(tag); got != want {
			t.Errorf("%q: %q, want %q", tag, got, want)
		}
	}
}

// Month names with case endings and numbers with suffixes, in CLDR-only languages.
func TestParseTimeIntentCLDRInflected(t *testing.T) {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, recallLocation(""))
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, now.Location()) }
	for q, want := range map[string]time.Time{
		"2026ko urtarrilaren 15a":   day(2026, 1, 15), // Basque
		"bilera urtarrilaren 15ean": day(2026, 1, 15),
		"2025eko martxoaren 3an":    day(2025, 3, 3),
		"2026. gada 15. janvārī":    day(2026, 1, 15), // Latvian locative
		"15. januára 2026":          day(2026, 1, 15), // Slovak genitive (a CLDR form)
		"15. siječnja 2026":         day(2026, 1, 15), // Croatian
		"2026 m. sausio 15 d.":      day(2026, 1, 15), // Lithuanian
		"15 януари 2026 г.":         day(2026, 1, 15), // Bulgarian
		"15 იანვარი, 2026":          day(2026, 1, 15), // Georgian
		"2026 թ. հունվարի 15":       day(2026, 1, 15), // Armenian
		"15 yanvar 2026":            day(2026, 1, 15), // Azerbaijani
		"2026 ж. 15 қаңтар":         day(2026, 1, 15), // Kazakh
		"15 ஜனவரி 2026":             day(2026, 1, 15), // Tamil
		"15 janar 2026":             day(2026, 1, 15), // Albanian
		"15. janúar 2026":           day(2026, 1, 15), // Icelandic
		"15. jaanuar 2026":          day(2026, 1, 15), // Estonian
		"cyfarfod 15 ionawr":        day(2026, 1, 15), // Welsh, year from now
	} {
		w, ok := parseTimeIntent(q, "", now)
		if !ok || !w.From.Equal(want) || !w.To.Equal(want.AddDate(0, 0, 1)) {
			t.Errorf("%q: %v..%v %v (%q), want %v", q, w.From, w.To, ok, w.Phrase, want)
		}
	}
	for _, q := range []string{
		"urtarrilaren bilera",        // inflected month without a day or year
		"janvārī plānojām",           // the same in Latvian
		"augustine 15 notes",         // an English name on a month stem: the stem needs a CLDR-only name
		"version 15abcde of the api", // a number with a long tail is not a day
		"urtarrilazzzzzz 15",         // too long an ending
	} {
		if w, ok := parseTimeIntent(q, "", now); ok {
			t.Errorf("%q: unexpected %v (%q)", q, w.From, w.Phrase)
		}
	}
}
