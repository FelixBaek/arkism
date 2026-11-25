package memory

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestWebhookDefenseAndConsolidationEvents(t *testing.T) {
	f, _ := wikiFixture(t)
	f.svc.WebhookKey = []byte(strings.Repeat("k", 32))
	f.hook("https://hooks.example.com/x", false, EventDefenseTriggered, EventConsolidationCompleted)
	putSettings(t, f, scopeA, SpaceConfigInput{DefenseRules: &[]SpaceDefenseRule{{Pattern: `falcon`, Action: "redact"}, {Pattern: `\bINC-\d+\b`, Action: "block"}}})
	text := "project falcon ships"
	if err := f.svc.defendSpace(testCtx, "", defenseGuard{}, scopeA, &text); err != nil || text == "project falcon ships" {
		t.Fatal("redact", err, text)
	}
	plain := "nothing to see"
	_ = f.svc.defendSpace(testCtx, "", defenseGuard{}, scopeA, &plain)
	blocked := "see INC-42"
	if err := f.svc.defendSpace(testCtx, "", defenseGuard{}, scopeA, &blocked); !errors.Is(err, ErrDefenseBlocked) {
		t.Fatal("block", err)
	}
	if ev := queuedEvents(f); len(ev) != 2 || ev[0] != EventDefenseTriggered || ev[1] != EventDefenseTriggered {
		t.Fatal("defense events", ev)
	}
	if _, err := f.consolidate(f.human, true); err != nil {
		t.Fatal(err)
	}
	if ev := queuedEvents(f); len(ev) != 2 {
		t.Fatal("dry run queued an event", ev)
	}
	if _, err := f.consolidate(f.human, false); err != nil {
		t.Fatal(err)
	}
	if ev := queuedEvents(f); len(ev) != 3 || !slices.Contains(ev, EventConsolidationCompleted) { // queued rows have no order
		t.Fatal("consolidation event", ev)
	}
}
