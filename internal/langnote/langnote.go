// Package langnote holds the fixed notices the server and the capture hook attach to
// memory data, in the language a space writes its memories in.
package langnote

import (
	"strings"
	"unicode"
)

// Notice keys.
const (
	PendingCorrection = "pending_correction"
	Provisional       = "provisional"
	NotUserWords      = "not_user_words"
)

var notices = map[string]map[string]string{
	PendingCorrection: {
		"ko": "정정 검토 중: 사람이 결정하기 전까지 이 기억은 틀렸을 수 있습니다.",
		"en": "Correction under review: this memory may be wrong until a person decides.",
		"ja": "訂正を確認中: 人が判断するまで、この記憶は誤っている可能性があります。",
		"zh": "更正审核中：在有人决定之前，这条记忆可能是错误的。",
		"es": "Corrección en revisión: este recuerdo puede ser incorrecto hasta que una persona decida.",
		"de": "Korrektur in Prüfung: Diese Erinnerung kann falsch sein, bis eine Person entscheidet.",
		"fr": "Correction en cours d'examen : ce souvenir peut être faux tant qu'une personne n'a pas décidé.",
	},
	Provisional: {
		"ko": "승인 전 후보입니다. 확정된 기억이 아니며 사람이 검토하기 전입니다.",
		"en": "Candidates awaiting approval: not confirmed memory and not yet reviewed by a person.",
		"ja": "承認前の候補です。確定した記憶ではなく、まだ人が確認していません。",
		"zh": "待批准的候选：不是已确认的记忆，尚未经人审核。",
		"es": "Candidatos pendientes de aprobación: no son recuerdos confirmados ni los ha revisado una persona.",
		"de": "Kandidaten vor der Freigabe: keine bestätigte Erinnerung, noch von keiner Person geprüft.",
		"fr": "Candidats en attente d'approbation : pas des souvenirs confirmés, pas encore examinés par une personne.",
	},
	NotUserWords: {
		"ko": "출처가 사용자 발화 아님",
		"en": "not from the user's own words",
		"ja": "ユーザー本人の発言ではない",
		"zh": "并非用户本人的话",
		"es": "no proviene de las palabras del usuario",
		"de": "nicht die eigenen Worte des Nutzers",
		"fr": "ne vient pas des propres mots de l'utilisateur",
	},
}

// Pick resolves the language a notice is written in: the first of setting (a space's
// memory_language), then locale (a request's Accept-Language), whose primary subtag has
// notices; with neither, the script of sample (Hangul ko, kana ja, Han zh, else en).
func Pick(setting, locale, sample string) string {
	for _, tag := range []string{setting, locale} {
		tag, _, _ = strings.Cut(tag, ",")
		tag, _, _ = strings.Cut(tag, ";")
		primary, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
		if _, ok := notices[PendingCorrection][primary]; ok {
			return primary
		}
	}
	switch {
	case strings.ContainsFunc(sample, func(r rune) bool { return unicode.Is(unicode.Hangul, r) }):
		return "ko"
	case strings.ContainsFunc(sample, func(r rune) bool { return unicode.In(r, unicode.Hiragana, unicode.Katakana) }):
		return "ja"
	case strings.ContainsFunc(sample, func(r rune) bool { return unicode.Is(unicode.Han, r) }):
		return "zh"
	}
	return "en"
}

// Text is the notice key in lang, English when lang has none.
func Text(key, lang string) string {
	if t, ok := notices[key][lang]; ok {
		return t
	}
	return notices[key]["en"]
}
