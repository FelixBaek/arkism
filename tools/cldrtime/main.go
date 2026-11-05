// Command cldrtime writes internal/memory/recall_time_cldr_data.go: month names,
// relative day/week/month/year words, "N units ago" patterns and last/this weekday
// phrases from the Unicode CLDR dates data (the cldr-dates-full JSON package), for
// the recall time-word fallback in internal/memory/recall_time_cldr.go.
//
// Usage: go run ./tools/cldrtime -src <cldr-dates-full dir> -out internal/memory/recall_time_cldr_data.go
//
// Locales: every base locale and script variant (no region). The hand-tuned
// languages of recall_time_lexicon.go keep their own months, relative words and
// "ago" forms; only their weekday phrases (which they lack) come from here. Values
// a locale inherits untranslated from the root, future forms and phrases two
// locales read differently are left out.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// handTuned are the languages recall_time_lexicon.go covers itself.
var handTuned = []string{"en", "ko", "ja", "zh", "yue", "es", "fr", "de", "pt", "it", "nl", "ru", "uk", "pl", "tr", "ar", "hi", "th", "vi", "id", "ms",
	"sv", "da", "nb", "nn", "no", "fi", "cs", "el", "he", "ro", "hu", "bn", "fa"}

// rel lists the CLDR field and offset of each relative kind, in the order of the
// relBefore..relThisYear constants of recall_time.go.
var rel = []struct {
	field, offset string
}{{"day", "-2"}, {"day", "-1"}, {"day", "0"}, {"week", "-1"}, {"week", "0"}, {"month", "-1"}, {"month", "0"}, {"year", "-2"}, {"year", "-1"}, {"year", "0"}}

var units = []string{"day", "week", "month", "year"}
var weekdays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

type field map[string]json.RawMessage

func main() {
	src := flag.String("src", "", "cldr-dates-full package directory")
	out := flag.String("out", "", "Go file to write")
	flag.Parse()
	version, license := readMeta(*src)
	root := loadLocale(*src, "und")
	entries, conflicts := map[string]string{}, map[string]bool{}
	locales := 0
	dirs, err := os.ReadDir(filepath.Join(*src, "main"))
	must(err)
	for _, d := range dirs {
		loc := d.Name()
		base, script, _ := strings.Cut(loc, "-")
		if loc == "und" || loc == "root" || script != "" && len(script) != 4 {
			continue // region variants repeat their base locale
		}
		locales++
		for phrase, code := range localeEntries(loadLocale(*src, loc), root, slices.Contains(handTuned, base), base == "en") {
			if prev, ok := entries[phrase]; ok && prev != code {
				conflicts[phrase] = true
			}
			entries[phrase] = code
		}
	}
	lines := []string{}
	for phrase, code := range entries {
		if !conflicts[phrase] {
			lines = append(lines, code+"\t"+phrase)
		}
	}
	slices.Sort(lines)
	write(*out, version, license, locales, lines)
}

type locale struct {
	months [2][12]string // format and stand-alone wide names
	fields map[string]field
}

func loadLocale(src, loc string) locale {
	var l locale
	var cal struct {
		Main map[string]struct {
			Dates struct {
				Calendars struct {
					Gregorian struct {
						Months map[string]map[string]map[string]string `json:"months"`
					} `json:"gregorian"`
				} `json:"calendars"`
			} `json:"dates"`
		} `json:"main"`
	}
	readJSON(filepath.Join(src, "main", loc, "ca-gregorian.json"), &cal)
	months := cal.Main[loc].Dates.Calendars.Gregorian.Months
	for i, ctx := range []string{"format", "stand-alone"} {
		for m := range 12 {
			l.months[i][m] = months[ctx]["wide"][fmt.Sprint(m+1)]
		}
	}
	var df struct {
		Main map[string]struct {
			Dates struct {
				Fields map[string]field `json:"fields"`
			} `json:"dates"`
		} `json:"main"`
	}
	readJSON(filepath.Join(src, "main", loc, "dateFields.json"), &df)
	l.fields = df.Main[loc].Dates.Fields
	return l
}

func (f field) text(key string) string {
	var s string
	if json.Unmarshal(f[key], &s) != nil {
		return ""
	}
	return s
}

// localeEntries maps each lower-cased phrase to its code: m<month>, r<relative kind>,
// a<unit> (an "ago" pattern with {0} as 0), l<weekday> (last X) or t<weekday> (this X),
// weekdays numbered from Monday = 0.
func localeEntries(l, root locale, tuned, english bool) map[string]string {
	out := map[string]string{}
	add := func(code, value, rootValue string) {
		v := strings.ToLower(strings.TrimSpace(value))
		if v == "" || !english && v == strings.ToLower(rootValue) || strings.ContainsAny(v, "+-") && strings.Contains(v, "{0}") {
			return // untranslated root value or a bare offset like "-{0} d"
		}
		out[strings.ReplaceAll(v, "{0}", "0")] = code
	}
	for i, d := range weekdays {
		add(fmt.Sprintf("l%d", i), l.fields[d].text("relative-type--1"), root.fields[d].text("relative-type--1"))
		add(fmt.Sprintf("t%d", i), l.fields[d].text("relative-type-0"), root.fields[d].text("relative-type-0"))
	}
	if tuned {
		return out
	}
	for c := range 2 {
		for m := range 12 {
			add(fmt.Sprintf("m%d", m+1), l.months[c][m], root.months[c][m])
		}
	}
	for k, r := range rel {
		add(fmt.Sprintf("r%d", k), l.fields[r.field].text("relative-type-"+r.offset), root.fields[r.field].text("relative-type-"+r.offset))
	}
	for u, unit := range units {
		var past map[string]string
		if json.Unmarshal(l.fields[unit]["relativeTime-type-past"], &past) != nil {
			continue
		}
		for _, p := range past {
			if strings.Count(p, "{0}") == 1 {
				add(fmt.Sprintf("a%d", u), p, "")
			}
		}
	}
	return out
}

func readMeta(src string) (string, string) {
	var pkg struct {
		Version string `json:"version"`
	}
	readJSON(filepath.Join(src, "package.json"), &pkg)
	license, err := os.ReadFile(filepath.Join(src, "LICENSE"))
	must(err)
	return pkg.Version, strings.TrimSpace(string(license))
}

func write(path, version, license string, locales int, lines []string) {
	var b strings.Builder
	fmt.Fprintf(&b, "// Code generated by tools/cldrtime from cldr-dates-full %s; DO NOT EDIT.\n//\n", version)
	b.WriteString("// The phrases below are derived from Unicode CLDR data, used under this license:\n//\n")
	for _, line := range strings.Split(license, "\n") {
		b.WriteString(strings.TrimRight("// "+line, " ") + "\n")
	}
	fmt.Fprintf(&b, "\npackage memory\n\n// cldrTimeLocales is how many CLDR locales the phrases were read from.\nconst cldrTimeLocales = %d\n\n", locales)
	b.WriteString("// cldrTimeData is one \"<code>\\t<phrase>\" per line; codes as in tools/cldrtime.\nconst cldrTimeData = `")
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("`\n")
	must(os.WriteFile(path, []byte(b.String()), 0o644))
}

func readJSON(path string, v any) {
	raw, err := os.ReadFile(path)
	must(err)
	must(json.Unmarshal(raw, v))
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
