package memory

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestMessengersHumanOnlyAndMasked(t *testing.T) {
	t.Setenv("ARKISM_CREW_NOTIFY", filepath.Join(t.TempDir(), "notify.json"))
	f := newFixture(t)
	save := MessengerSave{Scope: scopeA, Channel: "telegram", BotToken: "123456:AAH3abcdefghijklmnopqrstuvwxyz", ChatID: "-100123"}
	f.auth.actors["bot"] = actor("bot", "agent", true, "write")
	if _, err := f.svc.SaveMessenger(testCtx, "bot", save); !errors.Is(err, ErrDenied) {
		t.Fatal("agent saved a messenger", err)
	}
	if _, err := f.svc.SaveMessenger(testCtx, f.human, MessengerSave{Scope: scopeA, Channel: "telegram", BotToken: "bad", ChatID: "1"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad token accepted", err)
	}
	if _, err := f.svc.SaveMessenger(testCtx, f.human, save); err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.Messengers(testCtx, f.human, scopeA)
	raw, _ := json.Marshal(out)
	if err != nil || !strings.Contains(string(raw), `"last4":"wxyz"`) || strings.Contains(string(raw), "AAH3") {
		t.Fatalf("list %s %v", raw, err)
	}
	if _, err := f.svc.RemoveMessenger(testCtx, f.human, MessengerRef{Scope: scopeA, Channel: "telegram"}); err != nil {
		t.Fatal(err)
	}
	out, _ = f.svc.Messengers(testCtx, f.human, scopeA)
	if raw, _ = json.Marshal(out); !strings.Contains(string(raw), "[]") {
		t.Fatalf("not removed: %s", raw)
	}
}
