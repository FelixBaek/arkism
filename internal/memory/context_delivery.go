package memory

import (
	"context"
	"unicode/utf8"
)

func noteContextDrop(out *ContextBundle, reason string) {
	if out.Explain == nil {
		return
	}
	if out.Explain.Dropped == nil {
		out.Explain.Dropped = map[string]int{}
	}
	out.Explain.Dropped[reason]++
}

// Whole facts are optional: a long first result must not consume the budget of
// every later fact. Ranking stays intact among the entries that fit.
func boundWholeSearchHits(out *ContextBundle, maxChars int, history map[string]Record) {
	left := maxChars
	kept := []Hit{}
	for _, h := range out.Items {
		n := utf8.RuneCountInString(h.Text) + utf8.RuneCountInString(h.Summary)
		if n > left {
			noteContextDrop(out, "character_budget")
			continue
		}
		left -= n
		if r, ok := history[h.ID]; ok {
			out.History = append(out.History, HistoricalHit{Hit: h, ExpiresAt: r.ExpiresAt})
		} else {
			kept = append(kept, h)
		}
	}
	out.Items = kept
}

func boundConversationContext(out *ContextBundle, maxChars int) {
	left := maxChars
	for _, group := range [][]Hit{out.Items, out.Provisional} {
		for _, h := range group {
			left -= utf8.RuneCountInString(h.Text) + utf8.RuneCountInString(h.Summary)
		}
	}
	kept := []Hit{}
	for _, h := range out.Conversation {
		n := utf8.RuneCountInString(h.Text) + utf8.RuneCountInString(h.Summary)
		if n > left {
			noteContextDrop(out, "conversation_character_budget")
			continue
		}
		left -= n
		kept = append(kept, h)
	}
	out.Conversation = kept
}

// conversationSourcesUsed names the source records the excerpts show, neighbours included.
func conversationSourcesUsed(hits []Hit) map[string]bool {
	used := map[string]bool{}
	for _, h := range hits {
		used[h.ID] = true
		for _, source := range h.Sources {
			if source.Locator == "conversation_neighbor" {
				used[source.SourceID] = true
			}
		}
	}
	return used
}

// Source records are checked inside the search's final transaction, including
// neighbours appended to an excerpt. Never return stale text after revocation.
func verifyConversationContext(ctx context.Context, tx Tx, scope Scope, out ContextBundle) error {
	if len(out.Conversation) == 0 {
		return nil
	}
	used := conversationSourcesUsed(out.Conversation)
	for ref, original := range out.conversationRecords {
		if !used[ref.ID] {
			continue
		}
		current, err := tx.Get(ctx, "sources", scope, ref.ID)
		if err != nil {
			return err
		}
		current.MemoryReferenceFence, original.MemoryReferenceFence = 0, 0
		if current.Scope != scope || !live(current) || current.ContentErased || current.PrivacyQuarantined || current.Sensitivity == "restricted" || structuredText(current) != structuredText(original) {
			return ErrConflict
		}
		if out.evidenceSelectionFenced && len(current.Sources) > 0 {
			if err := checkSources(ctx, searchReadTx{Tx: tx}, current); err != nil {
				return err
			}
		}
		if err := tx.FenceSource(ctx, scope, ref.ID); err != nil {
			return err
		}
	}
	return nil
}
