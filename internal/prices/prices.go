// Package prices holds public list prices for the models Arkism runs, in
// dollars per million tokens. Subscription CLIs are priced at the same list
// prices, so every figure made from them is an estimate (추정), never a bill.
// Standard library only: memoryd, the host runner and the usage collector
// all read this one table.
package prices

import (
	"regexp"
	"strings"
	"unicode"
)

// Revision names this table in cost records priced from it.
const Revision = "list-2026-10"

// Price is $/1M tokens: input, output, cache write, cache read.
type Price struct {
	In, Out, CacheWrite, CacheRead float64
}

// Table is matched in order against a normalized model id (see Normalize);
// the first match wins, so more specific names come first. Anthropic list
// prices 2026-10; DeepSeek, OpenAI, Z.ai (GLM) and Google (Gemini) public list
// prices as published by each provider. Unknown models stay unpriced instead
// of guessed.
var Table = []struct {
	Re *regexp.Regexp
	P  Price
}{
	{regexp.MustCompile(`^claude-(fable|mythos)-5`), Price{10, 50, 12.5, .25}},
	{regexp.MustCompile(`^claude-opus-5-5`), Price{4, 20, 5, .2}},
	{regexp.MustCompile(`^claude-opus-(5|4)`), Price{5, 25, 6.25, .5}},
	{regexp.MustCompile(`^claude-sonnet-5`), Price{2, 10, 2.5, .2}},
	{regexp.MustCompile(`^claude-sonnet-4`), Price{3, 15, 3.75, .3}},
	{regexp.MustCompile(`^claude-haiku-5`), Price{.1, .5, .125, .01}},
	{regexp.MustCompile(`^claude-haiku-4`), Price{1, 5, 1.25, .1}},
	// DeepSeek
	{regexp.MustCompile(`^deepseek-.*(flash|lite)`), Price{.14, .28, 0, .014}},
	{regexp.MustCompile(`^deepseek-.*(reasoner|r\d)`), Price{.55, 2.19, 0, .14}},
	{regexp.MustCompile(`^deepseek`), Price{.28, .42, 0, .028}},
	// OpenAI GPT (and the Codex models, priced like the GPT they run on)
	{regexp.MustCompile(`^gpt-.*nano`), Price{.05, .4, 0, .005}},
	{regexp.MustCompile(`^gpt-.*mini`), Price{.25, 2, 0, .025}},
	{regexp.MustCompile(`^gpt-.*pro\b`), Price{15, 120, 0, 0}},
	{regexp.MustCompile(`^(gpt-|codex|o\d)`), Price{1.25, 10, 0, .125}},
	// Z.ai GLM
	{regexp.MustCompile(`^glm-.*(air|flash)`), Price{.2, 1.1, 0, .03}},
	{regexp.MustCompile(`^glm`), Price{.6, 2.2, 0, .11}},
	// Google Gemini
	{regexp.MustCompile(`^(antigravity-)?gemini-.*flash-lite`), Price{.1, .4, 0, .01}},
	{regexp.MustCompile(`^(antigravity-)?gemini-.*flash`), Price{.3, 2.5, 0, .03}},
	{regexp.MustCompile(`^(antigravity-)?gemini`), Price{2, 12, 0, .2}},
}

var spaceDots = regexp.MustCompile(`[\s.]+`)
var claudeShort = regexp.MustCompile(`^(opus|sonnet|haiku|fable|mythos)\b`)

// Normalize turns what people and routing write ("opus 5.5", "anthropic/claude-opus-5-5",
// "openai/gpt-6.1-sol") into the id the table matches.
func Normalize(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if _, rest, ok := strings.Cut(m, "/"); ok {
		m = rest
	}
	if claudeShort.MatchString(m) {
		m = "claude-" + spaceDots.ReplaceAllString(m, "-")
	} else if strings.HasPrefix(m, "claude") {
		m = spaceDots.ReplaceAllString(m, "-")
	}
	return m
}

// Lookup is the list price of a model id as given (no normalizing).
func Lookup(id string) (Price, bool) {
	for _, row := range Table {
		if row.Re.MatchString(id) {
			return row.P, true
		}
	}
	return Price{}, false
}

// Of is the list price of a model as people or routing write it.
func Of(model string) (Price, bool) { return Lookup(Normalize(model)) }

// Cost is the dollar cost of input and output tokens at list price.
func Cost(model string, in, out int64) (float64, bool) {
	p, ok := Of(model)
	if !ok {
		return 0, false
	}
	return (float64(in)*p.In + float64(out)*p.Out) / 1e6, true
}

// EstimateTokens guesses a token count from text when a CLI does not say:
// about four characters per token for Latin script, about one per character
// for Hangul and other wide scripts.
func EstimateTokens(text string) int64 {
	var ascii, wide int64
	for _, r := range text {
		if r < 128 {
			if !unicode.IsSpace(r) || r == ' ' {
				ascii++
			}
		} else {
			wide++
		}
	}
	n := (ascii+3)/4 + wide
	if n == 0 && text != "" {
		n = 1
	}
	return n
}
