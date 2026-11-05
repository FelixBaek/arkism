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

func BenchmarkParseTimeIntentMiss(b *testing.B) {
	q := strings.Repeat("the deploy pipeline failed again because of the cache key ", 4)
	now := time.Now()
	for b.Loop() {
		parseTimeIntent(q, now)
	}
}
