package memory

import (
	"strings"
	"testing"
	"time"
)

// TestParseTimeIntentLexicons is one table per language of timeLexicons, asked on
// Thursday 2026-11-05 in Seoul.
func TestParseTimeIntentLexicons(t *testing.T) {
	seoul := recallLocation("")
	nov5 := time.Date(2026, 11, 5, 10, 0, 0, 0, seoul)
	day := func(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, seoul) }
	span := func(a, b time.Time) [2]time.Time { return [2]time.Time{a, b} }
	today, yesterday, before := span(day(2026, 11, 5), day(2026, 11, 6)), span(day(2026, 11, 4), day(2026, 11, 5)), span(day(2026, 11, 3), day(2026, 11, 4))
	threeDays, lastWeek, twoWeeks := span(day(2026, 11, 2), day(2026, 11, 3)), span(day(2026, 10, 26), day(2026, 11, 2)), span(day(2026, 10, 19), day(2026, 10, 26))
	october, threeMonths, lastYear := span(day(2026, 10, 1), day(2026, 11, 1)), span(day(2026, 8, 1), day(2026, 9, 1)), span(day(2025, 1, 1), day(2026, 1, 1))
	oct31, lateOct, midOct := span(day(2026, 10, 31), day(2026, 11, 1)), span(day(2026, 10, 21), day(2026, 11, 1)), span(day(2026, 10, 11), day(2026, 10, 21))
	cases := map[string]map[string][2]time.Time{
		"en":    {"two weeks ago": twoWeeks, "3 months ago": threeMonths, "a year ago": lastYear, "2026-W44": lastWeek, "31-10-2026": oct31, "mid-October": midOct, "Oct 31, 2026": oct31},
		"ko":    {"2주 전": twoWeeks, "두 주 전": twoWeeks, "3개월 전": threeMonths, "2025년": lastYear, "이틀 전": before},
		"ja":    {"2週間前": twoWeeks, "3ヶ月前": threeMonths, "1年前": lastYear},
		"zh":    {"两周前": twoWeeks, "三个月前": threeMonths, "一年前": lastYear},
		"es":    {"hace dos semanas": twoWeeks, "hace un año": lastYear},
		"de":    {"vor zwei Wochen": twoWeeks, "vor einem Jahr": lastYear, "Mitte Oktober": midOct},
		"pt":    {"ontem": yesterday, "hoje": today, "anteontem": before, "há 3 dias": threeDays, "3 dias atrás": threeDays, "semana passada": lastWeek, "ano passado": lastYear, "31 de outubro de 2026": oct31, "em outubro": october, "fim de outubro": lateOct},
		"it":    {"ieri": yesterday, "oggi": today, "l'altro ieri": before, "3 giorni fa": threeDays, "la settimana scorsa": lastWeek, "l'anno scorso": lastYear, "31 ottobre 2026": oct31, "a ottobre": october, "fine ottobre": lateOct},
		"nl":    {"gisteren": yesterday, "vandaag": today, "eergisteren": before, "3 dagen geleden": threeDays, "vorige week": lastWeek, "vorig jaar": lastYear, "31 oktober 2026": oct31, "in oktober": october, "eind oktober": lateOct},
		"ru":    {"вчера": yesterday, "Сегодня": today, "позавчера": before, "3 дня назад": threeDays, "две недели назад": twoWeeks, "на прошлой неделе": lastWeek, "в прошлом году": lastYear, "31 октября 2026": oct31, "в октябре": october, "в конце октября": lateOct},
		"uk":    {"вчора": yesterday, "сьогодні": today, "позавчора": before, "3 дні тому": threeDays, "минулого тижня": lastWeek, "минулого року": lastYear, "31 жовтня 2026": oct31, "у жовтні": october, "наприкінці жовтня": lateOct},
		"pl":    {"wczoraj": yesterday, "dzisiaj": today, "przedwczoraj": before, "3 dni temu": threeDays, "w zeszłym tygodniu": lastWeek, "w zeszłym roku": lastYear, "31 października 2026": oct31, "w październiku": october, "pod koniec października": lateOct},
		"tr":    {"dün": yesterday, "bugün": today, "3 gün önce": threeDays, "geçen hafta": lastWeek, "geçen yıl": lastYear, "31 Ekim 2026": oct31, "Ekim'de": october, "ekimde": october},
		"ar":    {"أمس": yesterday, "اليوم": today, "أول أمس": before, "منذ 3 أيام": threeDays, "منذ ٣ أيام": threeDays, "الأسبوع الماضي": lastWeek, "العام الماضي": lastYear, "31 أكتوبر 2026": oct31, "في أكتوبر": october, "أواخر أكتوبر": lateOct},
		"hi":    {"आज": today, "3 दिन पहले": threeDays, "३ दिन पहले": threeDays, "पिछले हफ्ते": lastWeek, "पिछले साल": lastYear, "31 अक्टूबर 2026": oct31, "अक्टूबर में": october},
		"th":    {"เมื่อวาน": yesterday, "วันนี้": today, "เมื่อวานซืน": before, "3 วันที่แล้ว": threeDays, "สัปดาห์ที่แล้ว": lastWeek, "ปีที่แล้ว": lastYear, "31 ตุลาคม 2026": oct31, "ตุลาคม": october},
		"vi":    {"hôm qua": yesterday, "hôm nay": today, "hôm kia": before, "3 ngày trước": threeDays, "tuần trước": lastWeek, "năm ngoái": lastYear, "ngày 31 tháng 10 năm 2026": oct31, "tháng 10": october, "cuối tháng 10": lateOct},
		"id":    {"kemarin": yesterday, "hari ini": today, "3 hari yang lalu": threeDays, "minggu lalu": lastWeek, "tahun lalu": lastYear, "31 Oktober 2026": oct31, "pada bulan Oktober": october, "akhir Oktober": lateOct},
		"ms":    {"semalam": yesterday, "3 hari lepas": threeDays, "minggu lepas": lastWeek, "tahun lepas": lastYear, "Ogos 2026": span(day(2026, 8, 1), day(2026, 9, 1))},
		"sv":    {"igår": yesterday, "i dag": today, "i förrgår": before, "för 3 dagar sedan": threeDays, "förra veckan": lastWeek, "förra året": lastYear, "31 oktober 2026": oct31, "i oktober": october, "i slutet av oktober": lateOct},
		"da/no": {"i går": yesterday, "i forgårs": before, "for 3 dage siden": threeDays, "sidste uge": lastWeek, "forrige uke": lastWeek, "i fjor": lastYear, "31. oktober 2026": oct31},
		"fi":    {"eilen": yesterday, "tänään": today, "toissapäivänä": before, "3 päivää sitten": threeDays, "viime viikolla": lastWeek, "viime vuonna": lastYear, "31. lokakuuta 2026": oct31, "lokakuussa": october},
		"cs":    {"včera": yesterday, "dnes": today, "předevčírem": before, "před 3 dny": threeDays, "minulý týden": lastWeek, "loni": lastYear, "31. října 2026": oct31, "v říjnu": october, "koncem října": lateOct},
		"el":    {"χθες": yesterday, "σήμερα": today, "προχθές": before, "πριν από 3 μέρες": threeDays, "την προηγούμενη εβδομάδα": lastWeek, "πέρσι": lastYear, "31 Οκτωβρίου 2026": oct31, "τον Οκτώβριο": october, "στα τέλη Οκτωβρίου": lateOct, "ΟΚΤΏΒΡΙΟΣ 2026": october},
		"he":    {"אתמול": yesterday, "היום": today, "שלשום": before, "לפני 3 ימים": threeDays, "בשבוע שעבר": lastWeek, "בשנה שעברה": lastYear, "31 אוקטובר 2026": oct31, "באוקטובר": october, "בסוף אוקטובר": lateOct},
		"ro":    {"ieri": yesterday, "azi": today, "alaltăieri": before, "acum 3 zile": threeDays, "săptămâna trecută": lastWeek, "anul trecut": lastYear, "31 octombrie 2026": oct31, "în octombrie": october, "la sfârşitul lui octombrie": lateOct},
		"hu":    {"tegnap": yesterday, "tegnapelőtt": before, "3 nappal ezelőtt": threeDays, "múlt héten": lastWeek, "tavaly": lastYear, "2026. október 31.": oct31, "októberben": october},
		"bn":    {"গতকাল": yesterday, "আজ": today, "৩ দিন আগে": threeDays, "গত সপ্তাহে": lastWeek, "গত বছর": lastYear, "31 অক্টোবর 2026": oct31, "অক্টোবরে": october},
		"fa":    {"دیروز": yesterday, "امروز": today, "پریروز": before, "۳ روز پیش": threeDays, "هفته گذشته": lastWeek, "پارسال": lastYear, "31 اکتبر 2026": oct31, "در اکتبر": october},
	}
	for lang, table := range cases {
		for query, want := range table {
			w, ok := parseTimeIntent(query, nov5)
			if !ok || !w.From.Equal(want[0]) || !w.To.Equal(want[1]) {
				t.Errorf("%s %q: got %v..%v ok=%v, want %v..%v", lang, query, w.From, w.To, ok, want[0], want[1])
			}
		}
	}
	if len(timeLexicons) < 29 {
		t.Fatalf("languages: %d", len(timeLexicons))
	}
}

// Ordinary words that look like time words in another language name no span.
func TestParseTimeIntentLanguageSafety(t *testing.T) {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, recallLocation(""))
	for _, q := range []string{"wat hebben we hier besloten", "die Notizen von hier", "ask ma about it", "에 대한 일 전부", "ekim planı", "in mars rover", "trong 3 tháng tới", "2026-W54", "1234년", "version 2.0.1"} {
		if w, ok := parseTimeIntent(q, now); ok {
			t.Errorf("%q: unexpected window %v (%q)", q, w, w.Phrase)
		}
	}
	// French "hier" still reads as yesterday when the sentence is French or has no other language.
	for _, q := range []string{"hier", "qu'avons-nous décidé hier"} {
		if w, ok := parseTimeIntent(q, now); !ok || w.Phrase != "hier" {
			t.Errorf("%q: %v %v", q, w, ok)
		}
	}
}

// Ordinary words and phrases of each language that must not read as a time.
func TestParseTimeIntentOrdinaryWords(t *testing.T) {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, recallLocation(""))
	cases := map[string][]string{
		"en":     {"how do we rotate the api keys", "may we merge this branch", "march the tests forward", "a dec of cards", "fine tuning the model", "5 to 10", "version 1.2.3"},
		"ko":     {"세일 전 가격", "한일전 결과 정리", "열일 전에 쉬자", "네 일 전부", "배포 전 체크리스트", "지난 번 회의록", "세 번 전에"},
		"ja/zh":  {"上旬の予定は未定", "前回の会議"},
		"es":     {"el mar está en calma", "mayo es un nombre", "cuando hicimos el despliegue", "mi casa"},
		"fr":     {"la fin du projet", "le mois de mars rover", "fin de la session"},
		"de":     {"die Mitte des Projekts", "Ende gut alles gut", "die Notizen von hier"},
		"pt/it":  {"o ano novo", "em casa", "a fine progetto", "il mese prossimo"},
		"nl":     {"het einde", "half werk", "wat hebben we hier besloten"},
		"ru/uk":  {"в начале проекта", "в конце концов", "конец файла", "на початку проекту"},
		"pl/cs":  {"na początku projektu", "koniec pliku", "na začátku projektu", "koncem dne"},
		"tr":     {"ocak başında", "bir ay sonra", "ekim planı"},
		"ar/fa":  {"في البداية", "نهاية الملف", "در می‌آید", "ماه عسل"},
		"vi/id":  {"tháng năm rực rỡ", "vào đầu", "cuối cùng", "pada awal proyek", "akhir kata", "trong 3 tháng tới"},
		"nordic": {"i början av projektet", "under mars rover", "i slutet av dagen", "alussa", "lopussa"},
		"el/he":  {"στις αρχές του έργου", "τέλος του αρχείου", "בסוף הקובץ"},
		"ro/hu":  {"la începutul proiectului", "mai mult", "a projekt elején", "a hét napjai", "ask ma about it"},
	}
	for lang, queries := range cases {
		for _, q := range queries {
			if w, ok := parseTimeIntent(q, now); ok {
				t.Errorf("%s %q: unexpected window %v..%v (%q)", lang, q, w.From, w.To, w.Phrase)
			}
		}
	}
	// Korean native numbers still count weeks and months; days and years need digits.
	for q, from := range map[string]time.Time{"두 달 전": time.Date(2026, 9, 1, 0, 0, 0, 0, now.Location()), "세 주 전": time.Date(2026, 10, 12, 0, 0, 0, 0, now.Location()), "세일 전 어제 가격": time.Date(2026, 11, 4, 0, 0, 0, 0, now.Location())} {
		if w, ok := parseTimeIntent(q, now); !ok || !w.From.Equal(from) {
			t.Errorf("%q: %v %v", q, w, ok)
		}
	}
}

// Numeric dates: dots and dashes are day first, slashes are not read at all.
func TestParseTimeIntentNumericOrder(t *testing.T) {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, recallLocation(""))
	for q, want := range map[string]string{"03-04-2026": "2026-04-03", "03.04.2026": "2026-04-03", "2026-04-03": "2026-04-03", "31-10-2026": "2026-10-31"} {
		if w, ok := parseTimeIntent(q, now); !ok || w.From.Format(time.DateOnly) != want {
			t.Errorf("%q: %v %v, want %s", q, w.From, ok, want)
		}
	}
	for _, q := range []string{"03/04/2026", "04/03/2026", "10-31-2026"} {
		if w, ok := parseTimeIntent(q, now); ok {
			t.Errorf("%q: unexpected %v", q, w)
		}
	}
}

// The parser is a fixed set of RE2 rules: linear time, and the longest query search
// accepts (1000 runes) stays well under a request's budget.
func TestParseTimeIntentLongQueryBounded(t *testing.T) {
	now := time.Now()
	for _, unit := range []string{"1 2 3 4 5 6 7 8 9 ", "세 일 전 ", "in mars under mai de ", "一二三四五六七八九十", "die hier wir "} {
		q := []rune(strings.Repeat(unit, 1000))[:1000]
		start := time.Now()
		parseTimeIntent(string(q), now)
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%q x1000: %v", unit, d)
		}
	}
}

func FuzzParseTimeIntent(f *testing.F) {
	for _, q := range []string{"어제", "3 months ago", "2026-W44", "31-10-2026", "fin octobre 2026", "세일 전", "ปีที่แล้ว"} {
		f.Add(q)
	}
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, recallLocation(""))
	f.Fuzz(func(t *testing.T, q string) {
		if w, ok := parseTimeIntent(q, now); ok && !w.From.Before(w.To) {
			t.Fatalf("%q: empty window %v..%v", q, w.From, w.To)
		}
	})
}

func BenchmarkParseTimeIntentMiss(b *testing.B) {
	q := strings.Repeat("the deploy pipeline failed again because of the cache key ", 4)
	now := time.Now()
	for b.Loop() {
		parseTimeIntent(q, now)
	}
}
