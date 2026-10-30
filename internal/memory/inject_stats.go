package memory

import (
	"context"
	"unicode/utf8"

	"github.com/tiktoken-go/tokenizer"
)

// TokenCountInput asks how many tokens a text is, for the desktop's injection preview:
// the text the SessionStart and UserPromptSubmit hooks would add to a new session.
type TokenCountInput struct {
	Scope         Scope  `json:"scope"`
	Text          string `json:"text"`
	TokenEncoding string `json:"token_encoding,omitempty"`
}

type TokenCount struct {
	Tokens        int    `json:"tokens"`
	Chars         int    `json:"chars"`
	TokenEncoding string `json:"token_encoding"`
}

const tokenCountMaxChars = 64000

// CountTokens counts text with the same tokenizer packContext uses. It reads no records,
// so it needs only the read:memories right in the scope.
func (s *Service) CountTokens(ctx context.Context, token string, in TokenCountInput) (TokenCount, error) {
	encoding := in.TokenEncoding
	if encoding == "" {
		encoding = "cl100k_base"
	}
	if encoding != "cl100k_base" && encoding != "o200k_base" || !utf8.ValidString(in.Text) || utf8.RuneCountInString(in.Text) > tokenCountMaxChars || !boundedTokenRuns(in.Text) {
		return TokenCount{}, ErrInvalid
	}
	if _, e := s.authorize(ctx, token, "read:memories", in.Scope, "", ""); e != nil {
		return TokenCount{}, e
	}
	codec, err := tokenizer.Get(tokenizer.Encoding(encoding))
	if err != nil {
		return TokenCount{}, ErrUnavailable
	}
	n, err := codec.Count(in.Text)
	if err != nil {
		return TokenCount{}, ErrUnavailable
	}
	return TokenCount{Tokens: n, Chars: utf8.RuneCountInString(in.Text), TokenEncoding: encoding}, nil
}

// validMemoryUse checks one window of the host's memory-use report (memory_use, memory_use_30).
// Used may exceed Injected: receipt-counting hosts also count memories the agent recalled itself.
func validMemoryUse(u *MemoryUseStats) bool {
	if u == nil {
		return true
	}
	if u.Days < 1 || u.Days > 90 || u.Sessions < 0 || u.Injections < 0 || u.Used < 0 || len(u.Items) > 5000 {
		return false
	}
	if u.InjectSessions < 0 || u.InjectChars < 0 || u.InjectChars > 0 && u.InjectSessions == 0 || u.Estimated < 0 || u.UnusedAfter < 0 {
		return false
	}
	for _, it := range u.Items {
		if !validRunLabel(it.ID, 256) || it.Injected < 0 || it.Used < 0 || it.Estimated < 0 || it.Unreceipted < 0 {
			return false
		}
	}
	return true
}
