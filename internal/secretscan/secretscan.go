// Package secretscan is the one secret detector shared by the server and the
// host tools: named key rules, unnamed high-entropy tokens, and the personal
// data and raw transcript markers that also stop a transfer. It imports no
// other package of this module, so any package may use it.
package secretscan

import (
	"bytes"
	"encoding/base64"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// secretValue is the value after a secret keyword. A value that is a reference
// or placeholder says where a secret lives, not what it is, so it stays: a path
// with a folder (~/.config/x/token, ./local/token.txt, /run/secrets/key), an
// upper-case variable ($API_KEY, ${API_KEY}, %APP_KEY%), a lower-case dotfile
// (.env), <your-key> and a mask (***). Anything else after a reference mark is a
// value: "$Hunter2", "~hunter2", ".hunter2", "*hunter2", and a bare one-part
// path holding a digit ("/abc9fQ2"). A value opening a JSON object or list still
// counts as one ({"password":{}}). Case-sensitive even inside an (?i) rule.
const secretValue = `(?-i:"[^"~/.$%<*\r\n][^"\r\n]*"|'[^'~/.$%<*\r\n][^'\r\n]*'|(?:\\?["'])?(?:[^\s,;"'\\~/.$%<*]|` + badReference + `)[^\s,;]*)`

// badReference is a value that only looks like a reference (see secretValue).
const badReference = `~[^/\s,;"']|\.[a-z]*[^a-z./\s,;"']|(?:\.\.?|~)?/[^\s,;/"'.]*[0-9][^\s,;/"'.]*(?:[\s,;"']|$)|[$%]\{?[A-Z0-9_]*[^A-Z0-9_{}%/\\.\s,;"']|\*[^*\s,;"']`

// Deterministic local defence; policy authorization still has to inspect the
// sanitized complete payload. These patterns are not a universal DLP engine.
// Rules are the named secret shapes. Contains also runs the high-entropy scanner.
var Rules = []*regexp.Regexp{
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
	// Chat bot tokens: base64 bot id . timestamp . HMAC.
	regexp.MustCompile(`\b[MNO][A-Za-z0-9_-]{23,27}\.[A-Za-z0-9_-]{6}\.[A-Za-z0-9_-]{27,40}\b`),
	regexp.MustCompile(`\b(?:sk|al|rk)-(?:[a-zA-Z0-9_-]{12,})\b`),
	regexp.MustCompile(`(?i)\b(?:api[_ -]?key|access[_ -]?token|refresh[_ -]?token|token|password|passwd|secret|비밀번호)\\?["']?\s*[:=]\s*` + secretValue),
}

// Personal is e-mail addresses, mobile numbers and resident registration numbers.
var Personal = regexp.MustCompile(`(?i)([a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}|\b01[016789][- ]?\d{3,4}[- ]?\d{4}\b|\b\d{6}-[1-4]\d{6}\b)`)

// RawOutput marks raw tool output or a raw conversation pasted into a transfer.
var RawOutput = regexp.MustCompile(`(?i)\b(?:tool_output|raw_output|raw_conversation)\b`)

// Contains is the one secret detector: named rules, then unnamed high-entropy tokens.
func Contains(s string) bool {
	for _, r := range Rules {
		if r.MatchString(s) {
			return true
		}
	}
	var scan Scanner
	scan.Feed(s)
	return scan.Close()
}

// Redact replaces every rule match and every high-entropy token with mask.
func Redact(s, mask string) string {
	for _, rule := range Rules {
		s = rule.ReplaceAllLiteralString(s, mask)
	}
	var out strings.Builder
	last := 0
	scan := Scanner{OnToken: func(start, end int) {
		out.WriteString(s[last:start])
		out.WriteString(mask)
		last = end
	}}
	scan.Feed(s)
	scan.Close()
	out.WriteString(s[last:])
	return out.String()
}

// Transferable reports text that may leave the server or the host: no secret,
// no personal data, no raw output marker.
func Transferable(s string) bool {
	return !Contains(s) && !Personal.MatchString(s) && !RawOutput.MatchString(s)
}

// MaskTransfer masks everything Transferable refuses.
func MaskTransfer(s, mask string) string {
	s = Redact(s, mask)
	s = Personal.ReplaceAllLiteralString(s, mask)
	return RawOutput.ReplaceAllLiteralString(s, mask)
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

// Scanner finds high-entropy tokens in text fed in pieces. The zero value is ready.
type Scanner struct {
	tok        []byte
	n, at      int  // token length (may pass the cap) and absolute offset of the next byte
	lead, last byte // the byte before the current token, and the last byte fed
	afterComma bool // the previous token was ";base64" and a comma followed
	blob       bool // the current token is a data URI payload
	found      bool
	OnToken    func(start, end int) // optional: report each flagged token
}

func secretTokenByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '+' || c == '/'
}

// Feed accepts text in pieces; a token may span pieces.
func (s *Scanner) Feed(p string) {
	for i := 0; i < len(p); i++ {
		c := p[i]
		if secretTokenByte(c) {
			if s.n == 0 {
				s.blob, s.afterComma, s.lead = s.afterComma, false, s.last
			}
			if s.n <= entropyMaxLen {
				s.tok = append(s.tok, c)
			}
			s.n++
		} else {
			base64Mark := s.n == 6 && s.lead == ';' && string(s.tok) == "base64"
			s.endToken()
			s.afterComma = base64Mark && c == ','
		}
		s.last = c
		s.at++
	}
}

func (s *Scanner) endToken() {
	if s.n > 0 && !s.blob && highEntropyToken(s.tok[:min(s.n, len(s.tok))], s.n) {
		s.found = true
		if s.OnToken != nil {
			s.OnToken(s.at-s.n, s.at)
		}
	}
	s.tok, s.n, s.blob = s.tok[:0], 0, false
}

// Close ends the last token and reports whether any token was flagged.
func (s *Scanner) Close() bool {
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
