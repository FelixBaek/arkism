package memory

import (
	"context"
	"encoding/json"
	"unicode"
	"unicode/utf8"

	"github.com/tiktoken-go/tokenizer"
)

// packContext counts the exact data string, including provenance and authority.
// shortcut: greedy whole-item packing, at most 50 trials; no optimal knapsack.
func packContext(ctx context.Context, out *ContextBundle, budget int, encoding string) error {
	codec, err := tokenizer.Get(tokenizer.Encoding(encoding))
	if err != nil {
		return ErrInvalid
	}
	data := struct {
		Authority string `json:"authority"`
		Items     []Hit  `json:"items"`
	}{Authority: AuthorityDataOnly, Items: []Hit{}}
	workBytes := 0
	measure := func() (string, int, error) {
		if ctx.Err() != nil {
			return "", 0, ErrUnavailable
		}
		raw, err := json.Marshal(data)
		if err != nil {
			return "", 0, ErrUnavailable
		}
		// The upstream BPE merge is quadratic on long unbroken pieces.
		// shortcut: bound lexical runs and total work; use a faster tokenizer
		// if legitimate documents exceed these explicit service limits.
		workBytes += len(raw)
		if len(raw) > 1<<20 || workBytes > 8<<20 || !boundedTokenRuns(string(raw)) {
			return "", 0, ErrUnavailable
		}
		n, err := codec.Count(string(raw))
		if err != nil {
			return "", 0, ErrUnavailable
		}
		return string(raw), n, nil
	}
	text, count, err := measure()
	if err != nil {
		return err
	}
	if count > budget {
		return ErrInvalid
	}
	for _, h := range out.Items {
		data.Items = append(data.Items, h)
		candidate, n, err := measure()
		if err != nil {
			return err
		}
		if n > budget {
			data.Items = data.Items[:len(data.Items)-1]
			continue
		}
		text, count = candidate, n
	}
	out.Items = data.Items
	out.ContextData, out.TokensUsed, out.TokenEncoding = text, count, encoding
	return nil
}

// Both whitespace and non-whitespace runs can form a single BPE piece.
func boundedTokenRuns(text string) bool {
	run := 0
	space := false
	for _, r := range text {
		next := unicode.IsSpace(r)
		if next != space {
			run = 0
			space = next
		}
		run += utf8.RuneLen(r)
		if run > 4096 {
			return false
		}
	}
	return true
}
