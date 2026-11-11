//go:build linux

package memory

import (
	"testing"
	"time"
)

func TestReflectMemoryRejectsDeletionDuringAnswer(t *testing.T) {
	f, _ := reflectFixture(t, "answer")
	m := f.activeMemory("needle private event")
	at := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	m.CollectedAt = &at
	reflectMemoryRawPut(f, m)
	llm := &passageChangingLLM{fakeWikiLLM: fakeWikiLLM{answers: []string{"private event"}}, change: func() { f.editRecord(colMemories, m.ID, func(r *Record) { r.Status = "deleted" }) }}
	f.svc.Wiki = llm
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", MaxIterations: 1})
	if len(llm.prompts) != 1 {
		t.Fatalf("expected model call: %v", err)
	}
	if err == nil || out.Answer != "" {
		t.Fatalf("deleted memory delivered: err=%v answer=%q", err, out.Answer)
	}
}

func TestReflectMemoryMutationAndPermissionFences(t *testing.T) {
	for _, mode := range []string{"unchanged", "erased", "quarantined", "same_revision_text", "scope", "source_revoked", "transfer", "read", "revision"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := reflectFixture(t, "answer")
			m := f.activeMemory("needle private event")
			at := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			m.CollectedAt = &at
			reflectMemoryRawPut(f, m)
			llm := &passageChangingLLM{fakeWikiLLM: fakeWikiLLM{answers: []string{"private event"}}, change: func() {
				switch mode {
				case "erased":
					m.ContentErased = true
				case "quarantined":
					m.PrivacyQuarantined = true
				case "same_revision_text":
					m.Text = "changed text"
				case "scope":
					m.Scope.ProjectID = "other"
				case "revision":
					m.Revision++
				case "source_revoked":
					f.editRecord("sources", m.Sources[0].SourceID, func(r *Record) { r.Status = "deleted" })
					return
				case "transfer":
					f.svc.Auth = passageTransferDeny{Authorizer: f.svc.Auth, id: m.ID}
					return
				case "read":
					a := f.auth.actors[f.human]
					a.verbs["context"] = false
					f.auth.actors[f.human] = a
					return
				}
				reflectMemoryRawPut(f, m)
			}}
			f.svc.Wiki = llm
			out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", MaxIterations: 1})
			if len(llm.prompts) != 1 {
				t.Fatalf("expected call: %v", err)
			}
			if mode == "unchanged" {
				if err != nil || out.Answer == "" {
					t.Fatalf("unchanged rejected: %v", err)
				}
				return
			}
			if err == nil || out.Answer != "" {
				t.Fatalf("changed memory delivered: %v %q", err, out.Answer)
			}
		})
	}
}

func TestReflectMemoryAdmissionSurvivesTruncationAndDoesNotRebind(t *testing.T) {
	f, _ := reflectFixture(t, "answer")
	m := f.activeMemory("needle this text is deliberately much longer than the tool budget")
	run := &reflectRun{in: ReflectInput{Scope: scopeA, Query: "needle", PromptOnly: true}, seen: map[string]bool{}, graph: map[string]bool{}}
	run.add(packToolHits([]Hit{HitFromRecord(colMemories, m)}, 10, 3), false)
	if err := f.svc.verifyReflectMemories(testCtx, f.human, run); err != nil {
		t.Fatal("truncated original rejected", err)
	}
	m.Text = "needle changed"
	reflectMemoryRawPut(f, m)
	run.add([]Hit{HitFromRecord(colMemories, m)}, false)
	if err := f.svc.verifyReflectMemories(testCtx, f.human, run); err == nil {
		t.Fatal("first snapshot rebound")
	}
}

// Model concurrent low-level changes, including same-revision corruptions.
func reflectMemoryRawPut(f *fixture, r Record) {
	f.repo.mu.Lock()
	defer f.repo.mu.Unlock()
	f.repo.store[colMemories][recKey(scopeA, r.ID)] = r
}

func TestPostgresReflectMemoryDeliveryFence(t *testing.T) {
	for _, mode := range []string{"unchanged", "deleted", "erased", "transfer"} {
		t.Run(mode, func(t *testing.T) {
			f, p, _ := pgPassageFixture(t)
			src := pgPassageRow(t, p, scopeA, "memory-evidence", "needle private event", "memory-batch", 1)
			at := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			m := Record{ID: "fenced-memory", Scope: scopeA, Kind: "fact", Status: "active", Revision: 1, Text: "needle private event", Sensitivity: "internal", CreatedBy: "human-1", ApprovedBy: "human-1", CreatedAt: at, UpdatedAt: at, CollectedAt: &at, Sources: refs(src)}
			if err := p.Insert(testCtx, colMemories, m); err != nil {
				t.Fatal(err)
			}
			llm := &passageChangingLLM{fakeWikiLLM: fakeWikiLLM{answers: []string{"private event"}}, change: func() {
				mutatePostgresReflectMemory(t, f, p, m, mode)
			}}
			f.svc.Wiki = llm
			out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", MaxIterations: 1})
			if len(llm.prompts) != 1 {
				t.Fatalf("expected model call: %v", err)
			}
			if mode == "unchanged" {
				if err != nil || out.Answer == "" {
					t.Fatalf("unchanged rejected: %v", err)
				}
				return
			}
			if err == nil || out.Answer != "" {
				t.Fatalf("revoked PG memory delivered: %v %q", err, out.Answer)
			}
		})
	}
}

func TestReflectMemoryRejectsMissingAdmissionHash(t *testing.T) {
	f, _ := reflectFixture(t, "answer")
	m := f.activeMemory("needle")
	h := HitFromRecord(colMemories, m)
	h.reflectRecordHash = ""
	run := &reflectRun{in: ReflectInput{Scope: scopeA, Query: "needle", PromptOnly: true}, seen: map[string]bool{}, graph: map[string]bool{}}
	run.add([]Hit{h}, false)
	if err := f.svc.verifyReflectMemories(testCtx, f.human, run); err == nil {
		t.Fatal("unbound hit accepted")
	}
}

func mutatePostgresReflectMemory(t *testing.T, f *fixture, p *Postgres, m Record, mode string) {
	t.Helper()
	switch mode {
	case "deleted":
		m.Status = "deleted"
	case "erased":
		m.ContentErased = true
	case "transfer":
		f.svc.Auth = passageTransferDeny{Authorizer: f.svc.Auth, id: m.ID}
		return
	default:
		return
	}
	m.Revision++
	if err := p.Replace(testCtx, colMemories, m, m.Revision-1); err != nil {
		t.Fatal(err)
	}
}
