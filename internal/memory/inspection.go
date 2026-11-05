package memory

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const inspectionVersion = "101-ingest-v2"

type Inspection struct {
	RulesVersion string `json:"rules_version" bson:"rules_version"`
	Redacted     bool   `json:"redacted" bson:"redacted"`
	PersonalData bool   `json:"personal_data" bson:"personal_data"`
}

// secretValue is the value after a secret keyword. A value that starts with a
// reference or placeholder mark (a path, $ENV, %ENV%, <your-key>, ***) says where
// a secret lives, not what it is, so "token: ~/.config/x/token" stays. A value
// opening a JSON object or list still counts as one ({"password":{}}).
const secretValue = `(?:"[^"~/.$%<*\r\n][^"\r\n]*"|'[^'~/.$%<*\r\n][^'\r\n]*'|(?:\\?["'])?[^\s,;"'\\~/.$%<*][^\s,;]*)`

// Deterministic local defence; policy authorization still has to inspect the
// sanitized complete payload. These patterns are not a universal DLP engine.
var secretRules = []*regexp.Regexp{
	regexp.MustCompile(`(?:비밀번호|비밀키|인증토큰)\s*[:=]\s*` + secretValue),
	regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----.*?-----END (?:[A-Z0-9]+ )*PRIVATE KEY-----`),
	regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----.*`),
	// user:password@host inside one value: never across JSON quotes ("http://h:8888","x":"a@b" is not one).
	regexp.MustCompile(`(?i)\b(?:mongodb(?:\+srv)?|postgres(?:ql)?|mysql|redis|https?)://(?:[^\s/@:"\\]|\\.)+:(?:[^\s@"\\]|\\.)+@[^\s"']+`),
	regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[a-z0-9._~+/=-]+`),
	regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]+\.eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
	regexp.MustCompile(`\bAIza[a-zA-Z0-9_-]{20,}\b`),
	regexp.MustCompile(`\b(?:gh[pousr]_[a-zA-Z0-9]{20,}|github_pat_[a-zA-Z0-9_]{20,})\b`),
	regexp.MustCompile(`\bxox[baprs]-[a-zA-Z0-9-]{10,}\b`),
	// Discord bot tokens: base64 bot id . timestamp . HMAC.
	regexp.MustCompile(`\b[MNO][A-Za-z0-9_-]{23,27}\.[A-Za-z0-9_-]{6}\.[A-Za-z0-9_-]{27,40}\b`),
	regexp.MustCompile(`\b(?:sk|al|rk)-(?:[a-zA-Z0-9_-]{12,})\b`),
	regexp.MustCompile(`(?i)\b(?:api[_ -]?key|access[_ -]?token|refresh[_ -]?token|token|password|passwd|secret|비밀번호)\\?["']?\s*[:=]\s*` + secretValue),
}
var personalPattern = regexp.MustCompile(`(?i)([a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}|\b01[016789][- ]?\d{3,4}[- ]?\d{4}\b|\b\d{6}-[1-4]\d{6}\b)`)

// containsSecret is the one secret detector: named rules, then unnamed high-entropy tokens.
func containsSecret(s string) bool {
	for _, r := range secretRules {
		if r.MatchString(s) {
			return true
		}
	}
	var scan entropyScanner
	scan.feed(s)
	return scan.close()
}

// redactSecrets replaces every rule match and every high-entropy token.
func redactSecrets(s string) string {
	for _, rule := range secretRules {
		s = rule.ReplaceAllString(s, "[REDACTED]")
	}
	var out strings.Builder
	last := 0
	scan := entropyScanner{onToken: func(start, end int) {
		out.WriteString(s[last:start])
		out.WriteString("[REDACTED]")
		last = end
	}}
	scan.feed(s)
	scan.close()
	out.WriteString(s[last:])
	return out.String()
}

// High-entropy tokens catch key formats no rule names. Bytes outside the base64
// alphabet split tokens ("." and ":" too, so hosts, file names and URLs split;
// a path's words are dropped as labels by keyMaterial). Pure hex
// (hashes, commit ids, uuids, record ids) is never flagged by itself; a keyword
// rule covers hex keys. Runs past entropyMaxLen and a "base64," data URI
// payload are encoded blobs (images, files), not keys.
const (
	entropyMinLen  = 32
	entropyMinKey  = 24 // what is left after labels and hex parts
	entropyMaxLen  = 512
	entropyMinBits = 3.7 // Shannon bits per byte of the judged part
)

type entropyScanner struct {
	tok        []byte
	n, at      int  // token length (may pass the cap) and absolute offset of the next byte
	afterComma bool // the previous token was "base64" and a comma followed
	blob       bool // the current token is a data URI payload
	found      bool
	onToken    func(start, end int) // optional: report each flagged token
}

func secretTokenByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '+' || c == '/'
}

// feed accepts text in pieces; a token may span pieces.
func (s *entropyScanner) feed(p string) {
	for i := 0; i < len(p); i++ {
		c := p[i]
		if secretTokenByte(c) {
			if s.n == 0 {
				s.blob, s.afterComma = s.afterComma, false
			}
			if s.n <= entropyMaxLen {
				s.tok = append(s.tok, c)
			}
			s.n++
		} else {
			base64Mark := s.n == 6 && string(s.tok) == "base64"
			s.endToken()
			s.afterComma = base64Mark && c == ','
		}
		s.at++
	}
}

func (s *entropyScanner) endToken() {
	if s.n > 0 && !s.blob && highEntropyToken(s.tok[:min(s.n, len(s.tok))], s.n) {
		s.found = true
		if s.onToken != nil {
			s.onToken(s.at-s.n, s.at)
		}
	}
	s.tok, s.n, s.blob = s.tok[:0], 0, false
}

// close ends the last token and reports whether any token was flagged.
func (s *entropyScanner) close() bool {
	s.endToken()
	return s.found
}

// highEntropyToken judges one token. tok holds at most entropyMaxLen+1 bytes; n is its full length.
func highEntropyToken(tok []byte, n int) bool {
	if n < entropyMinLen || n > entropyMaxLen || hashDigestPrefix.Match(tok) || encodedText(tok) {
		return false
	}
	key := keyMaterial(tok)
	classes, lower, upper, digit := charClasses(key)
	// Without a digit it is a word or an identifier ("TestABrokenScannerIsAnErrorNotAPass").
	if len(key) < entropyMinKey || digit == 0 || wordLike(key, classes, lower, upper) {
		return false
	}
	return shannonBits(key) >= entropyMinBits
}

// charClasses maps each byte to 'a', 'A', '0' or 0 (a separator).
func charClasses(key []byte) (classes []byte, lower, upper, digit int) {
	classes = make([]byte, len(key))
	for i, c := range key {
		switch {
		case c >= 'a' && c <= 'z':
			classes[i], lower = 'a', lower+1
		case c >= 'A' && c <= 'Z':
			classes[i], upper = 'A', upper+1
		case c >= '0' && c <= '9':
			classes[i], digit = '0', digit+1
		}
	}
	return classes, lower, upper, digit
}

// wordLike: words and identifiers (camelCase, slugs, version strings) are made
// of long same-class runs; random keys switch class often. Single-case text is
// told apart by vowels too: words have many, random text few. Measured on
// random keys these checks miss about 1 in 100 keys of 32 characters.
func wordLike(key, classes []byte, lower, upper int) bool {
	chars := len(key) - bytes.Count(classes, []byte{0})
	covered, runs := runCoverage(classes)
	if lower > 0 && upper > 0 {
		// "WindowRequested": a capital starting a lowercase run belongs to that word.
		merged := slices.Clone(classes)
		for i := 0; i+1 < len(merged); i++ {
			if merged[i] == 'A' && merged[i+1] == 'a' && (i == 0 || classes[i-1] != 'A') {
				merged[i] = 'a'
			}
		}
		words, _ := runCoverage(merged)
		return covered*10 >= chars*7 || words*10 >= chars*9
	}
	vowels := 0
	for _, c := range key {
		if strings.IndexByte("aeiouAEIOU", c) >= 0 {
			vowels++
		}
	}
	meanRun := float64(chars) / float64(max(runs, 1))
	return len(key) < 32 || float64(vowels) >= 0.35*float64(lower+upper) && meanRun >= 3
}

// runCoverage counts characters in same-class runs of 3 or more, and all runs.
func runCoverage(classes []byte) (covered, runs int) {
	for i := 0; i < len(classes); {
		j := i
		for j < len(classes) && classes[j] == classes[i] {
			j++
		}
		if classes[i] != 0 {
			runs++
			if j-i >= 3 {
				covered += j - i
			}
		}
		i = j
	}
	return covered, runs
}

func shannonBits(key []byte) float64 {
	var counts [256]int
	for _, c := range key {
		counts[c]++
	}
	bits := 0.0
	for _, k := range counts {
		if k > 0 {
			p := float64(k) / float64(len(key))
			bits -= p * math.Log2(p)
		}
	}
	return bits
}

var uuidPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// keyMaterial drops the parts of a token ("-", "_" or "/" separated) that are
// labels or hex: hex tails of 8+ ("chatcmpl_<hex>", "source-<hex>", "SHA<hex>",
// "test25d5ed6a", uuids), then parts left with 3 bytes or less or only lowercase
// letters ("key_live_", "diff-", "V3-P1", path and slug words). Only the rest is judged.
func keyMaterial(tok []byte) []byte {
	out := make([]byte, 0, len(tok))
	for _, part := range bytes.FieldsFunc(uuidPattern.ReplaceAll(tok, []byte("-")), func(r rune) bool { return r == '-' || r == '_' || r == '/' }) {
		keep := len(part)
		for keep > 0 && strings.IndexByte("0123456789abcdefABCDEF", part[keep-1]) >= 0 {
			keep--
		}
		if len(part)-keep < 8 {
			keep = len(part)
		}
		if keep <= 3 || bytes.IndexFunc(part[:keep], func(r rune) bool { return r < 'a' || r > 'z' }) < 0 {
			keep = 0
		}
		out = append(out, part[:keep]...)
	}
	return out
}

// Integrity digests ("sha512-<base64>") name their hash; they identify content.
var hashDigestPrefix = regexp.MustCompile(`^sha(?:1|224|256|384|512)-`)

// encodedText reports a base64 token that decodes to readable UTF-8 text (an
// encoded document or message) or to a public key blob; keys decode to random bytes.
func encodedText(tok []byte) bool {
	s := strings.TrimRight(string(tok), "=")
	// Also try past a short label ("diff-<base64>").
	if i := strings.IndexAny(s, "-_"); i > 0 && i <= 8 && encodedText([]byte(s[i+1:])) {
		return true
	}
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		raw, err := enc.DecodeString(s[:len(s)/4*4])
		if err == nil && len(raw) > 8 && raw[0] == 0 && (bytes.HasPrefix(raw[4:], []byte("ssh-")) || bytes.HasPrefix(raw[4:], []byte("ecdsa-"))) {
			return true // a public key line, not a private one
		}
		if err != nil || len(raw) < 12 || !utf8.Valid(raw) {
			continue
		}
		printable := 0
		for _, r := range string(raw) {
			if unicode.IsPrint(r) || unicode.IsSpace(r) {
				printable++
			}
		}
		if printable*10 >= utf8.RuneCount(raw)*9 {
			return true
		}
	}
	return false
}

func validSensitivity(s string) bool {
	return s == "" || s == "public" || s == "internal" || s == "private" || s == "restricted"
}

func inspectCreate(in CreateInput) (CreateInput, Inspection, error) {
	return inspectCreateInput(in, false)
}

func inspectCreateInput(in CreateInput, file bool) (CreateInput, Inspection, error) {
	report := Inspection{RulesVersion: inspectionVersion}
	if in.OnSecret != "" && in.OnSecret != "block" && in.OnSecret != "redact" || !validSensitivity(in.Sensitivity) {
		return CreateInput{}, report, ErrInvalid
	}
	raw, e := json.Marshal(in)
	if e != nil {
		return CreateInput{}, report, ErrInvalid
	}
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil {
		return CreateInput{}, report, ErrInvalid
	}
	var walk func(any, bool) (any, error)
	walk = func(v any, redactable bool) (any, error) {
		switch x := v.(type) {
		case string:
			clean, e := inspectString(x, in.OnSecret, redactable, &report)
			if e != nil {
				return nil, e
			}
			return clean, nil
		case []any:
			for i, value := range x {
				clean, e := walk(value, redactable)
				if e != nil {
					return nil, e
				}
				x[i] = clean
			}
			return x, nil
		case map[string]any:
			for k, value := range x {
				// References, identity and scope cannot be silently rewritten by redaction.
				allowed := redactable && k != "source_id" && k != "id" && k != "collection" && k != "run_id" && k != "evaluation_id" && k != "skill_id" && k != "skill_version"
				clean, e := walk(value, allowed)
				if e != nil {
					return nil, e
				}
				x[k] = clean
			}
			return x, nil
		default:
			return v, nil
		}
	}
	for k, v := range object {
		redactable := k == "text" || k == "summary" || k == "keywords" || k == "sources" || k == "reflection"
		clean, e := walk(v, redactable)
		if e != nil {
			return CreateInput{}, report, e
		}
		object[k] = clean
	}
	clean, e := json.Marshal(object)
	if e != nil {
		return CreateInput{}, report, ErrInvalid
	}
	var out CreateInput
	if json.Unmarshal(clean, &out) != nil {
		return CreateInput{}, report, ErrInvalid
	}
	if out.Sensitivity == "" {
		out.Sensitivity = "internal"
	}
	if report.PersonalData && out.Sensitivity != "restricted" {
		out.Sensitivity = "private"
	}
	if !inspectedCreateLimits(out, file) {
		return CreateInput{}, report, ErrInvalid
	}
	return out, report, nil
}

// inspectString records personal data in report and redacts secrets when allowed.
func inspectString(x, onSecret string, redactable bool, report *Inspection) (string, error) {
	if personalPattern.MatchString(x) {
		report.PersonalData = true
	}
	if containsSecret(x) {
		if onSecret != "redact" || !redactable {
			return "", ErrSecretDetected
		}
		x = redactSecrets(x)
		report.Redacted = true
		if containsSecret(x) {
			return "", ErrSecretDetected
		}
	}
	return x, nil
}
func inspectedCreateLimits(out CreateInput, file bool) bool {
	// Empty original data does not become meaningful merely by passing a scanner.
	if !file && strings.TrimSpace(out.Text) == "" || len(out.Text) > 64000 || len(out.Summary) > 8000 {
		return false
	}
	for _, keyword := range out.Keywords {
		if len(keyword) > 256 {
			return false
		}
	}
	return true
}
