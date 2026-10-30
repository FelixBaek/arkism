package memory

import (
	"errors"
	"testing"
	"time"
)

func TestSessionArchiveRetentionChoice(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	if got, err := f.svc.SessionArchiveSettings(testCtx, f.human, scopeA); err != nil || got.Days != 7 {
		t.Fatalf("default = %+v, %v; want 7 days", got, err)
	}
	if _, err := f.svc.SetSessionArchiveSettings(testCtx, f.human, SessionArchiveSettingsInput{Scope: scopeA, Days: 14}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("14 days accepted: %v", err)
	}
	f.auth.actors[f.agent] = actor("retention-agent", "agent", true, "read", "write")
	if _, err := f.svc.SetSessionArchiveSettings(testCtx, f.agent, SessionArchiveSettingsInput{Scope: scopeA, Days: 90}); !errors.Is(err, ErrDenied) {
		t.Fatalf("an agent changed retention: %v", err)
	}
	if _, err := f.svc.SetSessionArchiveSettings(testCtx, f.human, SessionArchiveSettingsInput{Scope: scopeA, Days: 30}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.svc.SessionArchiveSettings(testCtx, f.human, scopeA); got.Days != 30 {
		t.Fatalf("saved = %d days", got.Days)
	}
	out := archiveSearchUpload(t, f, *now, ContentHash("retention-choice"), []byte("{\"type\":\"session\"}\n"), "codex_jsonl")
	if out.ExpiresAt == nil || !out.ExpiresAt.Equal(now.Add(30*24*time.Hour)) {
		t.Fatalf("expires %v, want 30 days after %v", out.ExpiresAt, *now)
	}
}
