package langnote

import "testing"

func TestPickAndText(t *testing.T) {
	cases := []struct{ setting, locale, sample, want string }{
		{"ja", "ko-KR", "deploy", "ja"},        // the space setting wins
		{"auto", "de-DE,de;q=0.9", "안녕", "de"}, // auto: the request locale
		{"auto", "", "지난 배포", "ko"},            // then the query's script
		{"", "", "先週の会議", "ja"},
		{"", "", "上周会议", "zh"},
		{"", "pt-BR", "release notes", "en"}, // no notices in pt: script, Latin = en
		{"es-419", "", "", "es"},
	}
	for _, c := range cases {
		if got := Pick(c.setting, c.locale, c.sample); got != c.want {
			t.Errorf("Pick(%q,%q,%q) = %q, want %q", c.setting, c.locale, c.sample, got, c.want)
		}
	}
	for key, byLang := range notices {
		if len(byLang) != len(notices[PendingCorrection]) || Text(key, "xx") != byLang["en"] {
			t.Errorf("%s: every notice needs the same languages and an English fallback", key)
		}
	}
}
