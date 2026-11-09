package memory

import (
	"context"
	"encoding/json"
	"unicode"
	"unicode/utf8"

	"github.com/tiktoken-go/tokenizer"
)

// packContext counts the exact data string, including provenance and authority.
// Greedy whole-item packing over the bounded memory and side lanes; no optimal knapsack.
func packContext(ctx context.Context, out *ContextBundle, budget int, encoding string, conversation ...bool) error {
	codec, err := tokenizer.Get(tokenizer.Encoding(encoding))
	if err != nil {
		return ErrInvalid
	}
	data := struct {
		Conversation      []Hit  `json:"conversation,omitempty"`
		Authority         string `json:"authority"`
		Items             []Hit  `json:"items"`
		Provisional       []Hit  `json:"provisional,omitempty"`
		ProvisionalNotice string `json:"provisional_notice,omitempty"`
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
	p := contextPacker{measure: measure, budget: budget, text: text, count: count}
	if err := p.add(out, out.Items, &data.Items, "token_budget"); err != nil {
		return err
	}
	// Provisional candidates come after approved items, only within what is left.
	if len(out.Provisional) > 0 {
		data.ProvisionalNotice = out.ProvisionalNotice
	}
	if err := p.add(out, out.Provisional, &data.Provisional, "provisional_token_budget"); err != nil {
		return err
	}
	if len(data.Provisional) == 0 {
		data.ProvisionalNotice = ""
	}
	if len(conversation) > 0 && conversation[0] {
		if err := p.add(out, out.Conversation, &data.Conversation, "conversation_token_budget"); err != nil {
			return err
		}
		out.Conversation = data.Conversation
	}
	out.Items, out.Provisional, out.ProvisionalNotice = data.Items, data.Provisional, data.ProvisionalNotice
	out.ContextData, out.TokensUsed, out.TokenEncoding = p.text, p.count, encoding
	return nil
}

// contextPacker holds the last measured context that fit the budget.
type contextPacker struct {
	measure func() (string, int, error)
	budget  int
	text    string
	count   int
}

// add appends hits to lane one at a time, keeping each only while the whole context fits.
func (p *contextPacker) add(out *ContextBundle, hits []Hit, lane *[]Hit, reason string) error {
	for _, h := range hits {
		*lane = append(*lane, h)
		candidate, n, err := p.measure()
		if err != nil {
			return err
		}
		if n > p.budget {
			*lane = (*lane)[:len(*lane)-1]
			noteContextDrop(out, reason)
			continue
		}
		p.text, p.count = candidate, n
	}
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

// tokenPacker keeps adding items (counted as their JSON) while the cl100k token count
// stays under the cap; used starts at the caller's fixed overhead.
type tokenPacker struct {
	codec tokenizer.Codec
	cap   int
	used  int
}

func newTokenPacker(capacity, overhead int) (*tokenPacker, error) {
	codec, err := tokenizer.Get(tokenizer.Cl100kBase)
	if err != nil {
		return nil, ErrUnavailable
	}
	return &tokenPacker{codec: codec, cap: capacity, used: overhead}, nil
}

func (p *tokenPacker) fits(v any) bool {
	n := p.count(v)
	if p.used+n > p.cap {
		return false
	}
	p.used += n
	return true
}

func (p *tokenPacker) count(v any) int {
	raw, _ := json.Marshal(v)
	n, err := p.codec.Count(string(raw))
	if err != nil {
		n = len(raw) / 3
	}
	return n
}
