//go:build linux

package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// These tests use the real SQL repository and service pipeline. Authorization
// and generation are controlled test doubles, not claims about production RLS
// or semantic answer accuracy.
type pgPassageDenyTransfer struct {
	Authorizer
	id string
}

func (a pgPassageDenyTransfer) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	if in.ResourceID == a.id && strings.HasPrefix(in.Context.Destination, "external:") {
		return Decision{}, ErrDenied
	}
	return a.Authorizer.Authorize(ctx, token, in)
}

type pgPassageChangingModel struct {
	fakeWikiLLM
	change func()
}

func (m *pgPassageChangingModel) Complete(ctx context.Context, system, user string, n int) (string, ProviderUsage, error) {
	text, usage, err := m.fakeWikiLLM.Complete(ctx, system, user, n)
	if m.change != nil {
		m.change()
	}
	return text, usage, err
}
func pgPassageFixture(t *testing.T) (*fixture, *Postgres, *fakeWikiLLM) {
	t.Helper()
	f, llm := reflectFixture(t, "synthetic answer", "synthetic answer")
	p := isolatedPostgres(t)
	f.svc.Repo = p
	a := f.auth.actors[f.human]
	a.verbs["read"] = true
	f.auth.actors[f.human] = a
	return f, p, llm
}
func pgPassageRow(t *testing.T, p *Postgres, scope Scope, id, text, batch string, index int, change ...func(*Record)) Record {
	t.Helper()
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	r := Record{ID: id, Scope: scope, Kind: conversationSourceKind, Status: statusActive, Revision: 1, Text: text,
		Sensitivity: "internal", CreatedBy: "human-1", OriginProduct: "passage-fixture", SourceRef: batch,
		CollectedAt: &at, CreatedAt: at, UpdatedAt: at, ConversationOrder: &ConversationOrder{Epoch: "passage-epoch", Index: index, Total: 3}}
	for _, fn := range change {
		fn(&r)
	}
	if err := p.Insert(testCtx, "sources", r); err != nil {
		t.Fatal(err)
	}
	return r
}
func TestPostgresReflectPassagesNeighborScopeAndTransfer(t *testing.T) {
	f, p, llm := pgPassageFixture(t)
	pgPassageRow(t, p, scopeA, "pg-before", "[2026-10-01] Rowan: adjacent-before-detail.", "passage-main", 1)
	pgPassageRow(t, p, scopeA, "pg-match", "[2026-10-01] Rowan: copper pump inspection.", "passage-main", 2)
	pgPassageRow(t, p, scopeA, "pg-after", "[2026-10-01] Rowan: adjacent-after-detail.", "passage-main", 3)
	other := scopeA
	other.ProjectID += "-foreign"
	pgPassageRow(t, p, other, "pg-foreign", "[2026-10-01] Rowan: copper pump inspection foreign-canary.", "passage-main", 2)
	denied := pgPassageRow(t, p, scopeA, "pg-private", "[2026-10-01] Rowan: copper pump inspection private-canary.", "passage-private", 2, func(r *Record) { r.Sensitivity = "private" })
	f.svc.Auth = pgPassageDenyTransfer{Authorizer: f.svc.Auth, id: denied.ID}
	in := ReflectInput{Scope: scopeA, Query: "copper pump inspection", MaxIterations: 1}
	if _, err := f.svc.Reflect(testCtx, f.human, in); err != nil {
		t.Fatal("disabled", err)
	}
	if len(llm.prompts) != 1 || strings.Contains(llm.prompts[0], "Conversation passages (verbatim") {
		t.Fatal("disabled passages", llm.prompts)
	}
	f.svc.ReflectPassages = 10
	out, err := f.svc.Reflect(testCtx, f.human, in)
	if err != nil || out.Answer == "" || len(llm.prompts) != 2 {
		t.Fatal("enabled", err, out)
	}
	prompt := llm.prompts[1]
	for _, want := range []string{"copper pump inspection.", "adjacent-before-detail.", "adjacent-after-detail."} {
		if !strings.Contains(prompt, want) {
			t.Fatal("missing SQL passage", want, prompt)
		}
	}
	if strings.Contains(prompt, "foreign-canary") || strings.Contains(prompt, "private-canary") {
		t.Fatal("scope or transfer exclusion failed", prompt)
	}
}
func TestPostgresReflectPassagesFenceSourceErasure(t *testing.T) {
	for _, dependency := range []bool{false, true} {
		name := "selected"
		if dependency {
			name = "parent"
		}
		t.Run(name, func(t *testing.T) {
			f, p, _ := pgPassageFixture(t)
			f.svc.ReflectPassages = 10
			var parent Record
			if dependency {
				parent = Record{ID: "pg-parent", Scope: scopeA, Kind: "message", Status: statusActive, Revision: 1, Text: "original parent", Sensitivity: "internal", CreatedBy: "human-1"}
				if err := p.Insert(testCtx, "sources", parent); err != nil {
					t.Fatal(err)
				}
				var err error
				parent, err = p.Get(testCtx, "sources", scopeA, parent.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			selected := pgPassageRow(t, p, scopeA, "pg-selected", "[2026-10-01] Rowan: copper pump inspection erasure-canary.", "passage-erasure", 2, func(r *Record) {
				if dependency {
					r.Sources = refs(parent)
				}
			})
			target := selected
			if dependency {
				target = parent
			}
			changed := false
			model := &pgPassageChangingModel{fakeWikiLLM: fakeWikiLLM{answers: []string{"erasure-canary"}}, change: func() {
				current, err := p.Get(testCtx, "sources", scopeA, target.ID)
				if err != nil {
					t.Fatal(err)
				}
				current.ContentErased = true
				if err := p.Replace(testCtx, "sources", current, current.Revision); err != nil {
					t.Fatal(err)
				}
				changed = true
			}}
			f.svc.Wiki = model
			out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "copper pump inspection", MaxIterations: 1})
			if !changed || len(model.prompts) != 1 || !strings.Contains(model.prompts[0], "erasure-canary") {
				t.Fatal("erasure hook not exercised", changed, model.prompts, err)
			}
			if (!errors.Is(err, ErrDenied) && !errors.Is(err, ErrConflict)) || out.Answer != "" || out.Prompt != nil {
				t.Fatal("erased source delivered", err, out)
			}
			current, e := p.Get(testCtx, "sources", scopeA, target.ID)
			if e != nil || !current.ContentErased {
				t.Fatal("DB erasure not committed", e)
			}
		})
	}
}
