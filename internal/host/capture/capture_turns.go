package capture

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

// The conversation profile (capture_density conversation) reads long batches in windows and
// keeps the turns themselves as searchable conversation sources, so recall can still show
// what was said when no claim was extracted from it.

const (
	claimsPerCall     = 16 // claims kept from one extraction call
	captureClaimsMax  = 32 // the server's claims per capture request
	windowTurns       = 10 // turns one conversation-profile call reads
	windowOverlap     = 2  // turns a window shares with the next, so a fact on the border is seen whole
	turnChunkRunes    = 600
	turnSourcesMax    = 64 // conversation sources one batch stores
	turnSourceTimeout = 30 * time.Second
)

// extractionWindows splits turns into the windows one call each reads: the whole batch, or for the
// conversation profile windows of windowTurns overlapping by windowOverlap.
func extractionWindows(density string, turns []string) [][]string {
	if density != densityConversation || len(turns) <= windowTurns {
		return [][]string{turns}
	}
	var out [][]string
	for start := 0; ; start += windowTurns - windowOverlap {
		end := min(start+windowTurns, len(turns))
		out = append(out, turns[start:end])
		if end == len(turns) {
			return out
		}
	}
}

// extractWindows runs call over each window's text and returns the raw claims, at most
// claimsPerCall per call; a claim text an earlier (overlapping) window already returned is dropped.
func extractWindows(density string, turns []string, call func(text string) (any, error)) ([]any, error) {
	var raw []any
	seen := map[string]bool{}
	for _, w := range extractionWindows(density, turns) {
		out, err := call(strings.Join(w, "\n"))
		if err != nil {
			return nil, err
		}
		list, err := asList(out)
		if err != nil {
			return nil, err
		}
		var texts []string
		for _, v := range list[:min(len(list), claimsPerCall)] {
			row, _ := v.(map[string]any)
			text, _ := row["text"].(string)
			if text != "" && seen[text] {
				continue
			}
			texts = append(texts, text)
			raw = append(raw, v)
		}
		for _, t := range texts {
			seen[t] = true
		}
	}
	return raw, nil
}

// ConversationChunks packs dated, speaker-labelled turns ("Mina: ...") into source texts of at
// most turnChunkRunes runes each, every line prefixed with day ("[2023-05-08] Mina: ...").
// A longer turn is its own chunk, cut to the limit.
func ConversationChunks(day string, turns []string) []string {
	var out []string
	var cur strings.Builder
	for _, t := range turns {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		line := head("["+day+"] "+t, turnChunkRunes)
		if cur.Len() > 0 && utf8.RuneCountInString(cur.String())+1+utf8.RuneCountInString(line) > turnChunkRunes {
			out = append(out, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteByte('\n')
		}
		cur.WriteString(line)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// storeTurns keeps the batch's turns as conversation sources once (job["turns_stored"]), for
// the conversation profile only. The turns were redacted with the batch; the server inspects
// the text again and redacts a secret. A retry resends the same idempotency keys.
func storeTurns(job map[string]any, cued []any, origin string, checkpoint func() error) error {
	if captureDensity() != densityConversation || truthy(job["turns_stored"]) || len(cued) == 0 {
		return nil
	}
	at := cued[len(cued)-1].(map[string]any)["at"]
	turns := make([]string, len(cued))
	for i, u := range cued {
		turns[i] = "user: " + u.(map[string]any)["text"].(string)
	}
	chunks := ConversationChunks(messageDay(cued), turns)
	if privatePayload(chunks) {
		return sessionWait("capture_private_content")
	}
	event := str(or(job["event_id"], ""))
	for i, text := range chunks[:min(len(chunks), turnSourcesMax)] {
		body := host.Obj{{Key: "scope", Value: capScope()}, {Key: "kind", Value: "conversation"}, {Key: "text", Value: text},
			{Key: "origin_product", Value: origin}, {Key: "source_ref", Value: head("capture-turns:"+event, 500)}, {Key: "collected_at", Value: at},
			{Key: "on_secret", Value: "redact"}, {Key: "idempotency_key", Value: "turns-" + sha256Hex([]byte(fmt.Sprintf("%s\n%d", event, i)))[:58]}}
		if _, err := capPost("/v1/sources", body, turnSourceTimeout); err != nil {
			return err
		}
	}
	job["turns_stored"] = true
	return checkpoint()
}
