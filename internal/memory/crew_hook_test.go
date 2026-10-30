package memory

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestCrewHookTokenOwnerOnly(t *testing.T) {
	f := newFixture(t)
	t.Setenv("ARKISM_CREW_HOOKS", filepath.Join(t.TempDir(), "crew-hooks.json"))
	t.Setenv("ARKISM_CREW_PUBLIC_URL", "")
	t.Setenv("ARKISM_CREW_LISTEN", "")
	w := f.crew(crewWorkflow("아침 요약", "아침요약"))
	in := CrewHookInput{Scope: scopeA}
	if _, err := f.svc.CrewHookToken(testCtx, f.agent, w.ID, in); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent read a webhook token", err)
	}
	first, err := f.svc.CrewHookToken(testCtx, f.human, w.ID, in)
	if err != nil || len(first.Token) != 64 || first.URL != "http://127.0.0.1:8095/hooks/"+w.ID || !first.ThisPCOnly || first.Header != "X-Arkism-Hook-Token" {
		t.Fatal("first", first, err)
	}
	again, err := f.svc.CrewHookToken(testCtx, f.human, w.ID, in)
	if err != nil || again.Token != first.Token {
		t.Fatal("the token must stay the same once made", again, err)
	}
	if _, err := f.svc.CrewHookToken(testCtx, f.human, "missing", in); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown workflow", err)
	}
	t.Setenv("ARKISM_CREW_PUBLIC_URL", "https://flows.example.org/")
	if h, _ := f.svc.CrewHookToken(testCtx, f.human, w.ID, in); h.URL != "https://flows.example.org/hooks/"+w.ID || h.ThisPCOnly {
		t.Fatal("public address", h)
	}
}
