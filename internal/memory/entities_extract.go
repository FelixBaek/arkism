package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Named entities are the people, projects, repos, tools, files and other named
// things approved memories talk about. The deterministic pass below needs no
// model; an optional model pass (entities_model.go) only proposes.

const (
	entityTypeConcept   = "concept"
	entityNameMinRunes  = 2
	entityNameMaxRunes  = 80
	entityMaxPerMemory  = 32
	entityMaxAliases    = 32
	entityKoreanKeepLen = 2
)

var entityTypes = map[string]bool{"person": true, "project": true, "repo": true, "tool": true, "product": true,
	"file": true, "service": true, "place": true, "organization": true, entityTypeConcept: true}

type entityCandidate struct {
	Name string
	Type string
	// Weak marks a lone capitalized word that opens a sentence: it links to an
	// entity that already exists but never creates one.
	Weak bool
}

var (
	entityURL         = regexp.MustCompile(`(?i)\b(?:https?|ftp)://\S+|\S+@\S+\.[a-z]{2,}`)
	entityHandle      = regexp.MustCompile(`(?:^|[^\w@])@([A-Za-z0-9][A-Za-z0-9_-]{1,38})`)
	entityBacktick    = regexp.MustCompile("`([^`\n]{2,80})`")
	entityQuoted      = regexp.MustCompile(`["“「『]([^"”」』\n]{2,60})["”」』]`)
	entityCapitalized = regexp.MustCompile(`[A-Z][A-Za-z0-9]*(?:[ -][A-Z][A-Za-z0-9]*){0,3}`)
	entityKoreanTitle = regexp.MustCompile(`([가-힣]{2,4}) ?(?:선생님|교수님|대표님|팀장님|과장님|부장님|님|씨)`)
	entityHexLike     = regexp.MustCompile(`^[0-9a-fA-F]{7,}$`)
	entityFileExt     = regexp.MustCompile(`[\w-]{2,}\.[A-Za-z][A-Za-z0-9]{0,5}$`)
)

// Capitalized words that start sentences or carry no name of their own.
var entityStopWords = map[string]bool{"The": true, "A": true, "An": true, "This": true, "That": true, "These": true, "Those": true,
	"It": true, "I": true, "We": true, "You": true, "He": true, "She": true, "They": true, "If": true, "When": true, "Then": true,
	"And": true, "But": true, "Or": true, "So": true, "For": true, "In": true, "On": true, "At": true, "To": true, "Of": true,
	"With": true, "By": true, "From": true, "As": true, "Is": true, "Are": true, "Was": true, "Use": true, "Do": true, "Not": true,
	"No": true, "Yes": true, "All": true, "Any": true, "Each": true, "After": true, "Before": true, "Also": true, "Only": true,
	"Always": true, "Never": true, "Note": true, "Why": true, "What": true, "How": true, "OK": true, "TODO": true}

var entityRepoStop = map[string]bool{"and/or": true, "read/write": true, "input/output": true, "on/off": true, "yes/no": true,
	"true/false": true, "client/server": true, "i/o": true}

var entityCommonPeople = map[string]bool{"사용자": true, "고객": true, "여러분": true, "관리자": true, "작성자": true, "선생": true}

var entityOrgSuffixes = []string{"대학교", "대학", "주식회사", "연구소", "재단", "병원", "은행", "협회", "학회", "위원회"}

// normalizeEntityName is the display form: trimmed, inner whitespace collapsed.
func normalizeEntityName(name string) string {
	return strings.Join(strings.Fields(strings.Trim(name, keywordTrim+" \t")), " ")
}

// entityKey is the dedupe key of a name: case and a leading @ do not matter.
// Only a hash is kept, so a deleted entity's tombstone holds no readable name.
func entityKey(name string) string {
	norm := strings.ToLower(strings.TrimPrefix(normalizeEntityName(name), "@"))
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:16])
}

func validEntityName(name string) bool {
	n := utf8.RuneCountInString(name)
	if n < entityNameMinRunes || n > entityNameMaxRunes || strings.ContainsAny(name, "\n\r\t") || entityHexLike.MatchString(name) || containsSecret(name) {
		return false
	}
	return strings.IndexFunc(name, unicode.IsLetter) >= 0
}

// extractEntityCandidates is the deterministic pass. The first type found for
// a name wins, except that a specific type replaces "concept".
func extractEntityCandidates(text string) []entityCandidate {
	text = entityURL.ReplaceAllString(text, " ")
	var out []entityCandidate
	at := map[string]int{}
	add := func(name, typ string, weak bool) {
		name = normalizeEntityName(name)
		if !validEntityName(name) {
			return
		}
		k := entityKey(name)
		if i, ok := at[k]; ok {
			out[i].merge(typ, weak)
			return
		}
		if len(out) < entityMaxPerMemory {
			at[k] = len(out)
			out = append(out, entityCandidate{Name: name, Type: typ, Weak: weak})
		}
	}
	for _, m := range entityHandle.FindAllStringSubmatch(text, -1) {
		add("@"+m[1], "person", false)
	}
	for _, m := range entityBacktick.FindAllStringSubmatch(text, -1) {
		add(m[1], codeEntityType(m[1]), false)
	}
	for _, m := range entityQuoted.FindAllStringSubmatch(text, -1) {
		add(m[1], entityTypeConcept, false)
	}
	for _, m := range entityKoreanTitle.FindAllStringSubmatch(text, -1) {
		if !entityCommonPeople[m[1]] {
			add(m[1], "person", false)
		}
	}
	for _, raw := range strings.Fields(text) {
		if word := koreanOrganization(raw); word != "" {
			add(word, "organization", false)
		}
		// Identifiers, paths and file names: the same rule the keyword index uses.
		if kw := keywordFromToken(raw); len(kw) >= 4 && !strings.ContainsAny(kw, "'’") && !strings.ContainsFunc(kw, isHangul) && !entityRepoStop[strings.ToLower(kw)] {
			add(kw, codeEntityType(kw), false)
		}
	}
	for _, c := range capitalizedNames(text) {
		add(c.Name, entityTypeConcept, c.Weak)
	}
	return out
}

// merge folds a repeat of the name in: a specific type replaces "concept", and the name stays weak only if every find was.
func (c *entityCandidate) merge(typ string, weak bool) {
	if c.Type == entityTypeConcept && typ != entityTypeConcept {
		c.Type = typ
	}
	c.Weak = c.Weak && weak
}

// codeEntityType tells files and repos from other code identifiers.
func codeEntityType(s string) string {
	switch {
	case strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") || strings.HasPrefix(s, "~/"):
		return "file"
	case strings.Count(s, "/") >= 2 || strings.Count(s, "/") == 1 && entityFileExt.MatchString(s):
		return "file"
	case strings.Count(s, "/") == 1 && !strings.ContainsAny(s, " ()") && !entityRepoStop[strings.ToLower(s)]:
		return "repo"
	case !strings.Contains(s, " ") && entityFileExt.MatchString(s) && strings.IndexFunc(s, unicode.IsLetter) == 0:
		return "file"
	}
	return entityTypeConcept
}

// koreanOrganization returns a Hangul word that ends in an organization suffix,
// with its particles cut ("서울대학교에서" → "서울대학교").
func koreanOrganization(raw string) string {
	word := stripKoreanSuffix(strings.Trim(raw, keywordTrim), entityKoreanKeepLen)
	if word == "" || strings.IndexFunc(word, isHangul) < 0 {
		return ""
	}
	for _, suffix := range entityOrgSuffixes {
		if strings.HasSuffix(word, suffix) && word != suffix {
			return word
		}
	}
	return ""
}

// capitalizedNames finds runs of capitalized words outside paths and file
// names. A lone plain word that starts a sentence is weak; leading stop words
// are dropped from a run.
func capitalizedNames(text string) []entityCandidate {
	var out []entityCandidate
	for _, loc := range entityCapitalized.FindAllStringIndex(text, -1) {
		if loc[0] > 0 {
			if r, _ := utf8.DecodeLastRuneInString(text[:loc[0]]); unicode.IsLetter(r) && r < utf8.RuneSelf || unicode.IsDigit(r) || r == '_' {
				continue // inside a word
			}
		}
		if rest := text[loc[1]:]; strings.HasPrefix(rest, "/") || strings.HasPrefix(rest, "_") || len(rest) > 1 && rest[0] == '.' && isASCIIAlnum(rest[1]) {
			continue // part of a path or file name
		}
		words := strings.FieldsFunc(text[loc[0]:loc[1]], func(r rune) bool { return r == ' ' })
		for len(words) > 0 && entityStopWords[words[0]] {
			words = words[1:]
		}
		if len(words) == 0 {
			continue
		}
		weak := len(words) == 1 && sentenceStart(text, loc[0]) && !innerCapital(words[0])
		out = append(out, entityCandidate{Name: strings.Join(words, " "), Type: entityTypeConcept, Weak: weak})
	}
	return out
}

func sentenceStart(text string, at int) bool {
	before := strings.TrimRight(text[:at], " \t")
	return before == "" || strings.HasSuffix(before, "\n") || strings.ContainsAny(before[len(before)-1:], ".!?:")
}

func innerCapital(word string) bool {
	for i, r := range word {
		if i > 0 && (unicode.IsUpper(r) || unicode.IsDigit(r)) {
			return true
		}
	}
	return false
}

func isASCIIAlnum(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}
