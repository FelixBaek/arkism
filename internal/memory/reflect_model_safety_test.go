//go:build linux

package memory

import (
	"strings"
	"testing"
	"time"
)

func reflectModelSafetyRecord(m Record) Record {
	return Record{ID: "model-safety", Scope: m.Scope, Kind: mentalModelKind, Status: statusActive, Revision: 1, MentalModel: &MentalModelData{Title: "Private summary", Active: &DerivedRevision{Text: "private summary detail", At: time.Now().UTC(), Citations: []Ref{{Collection: colMemories, ID: m.ID, Revision: m.Revision}}}}}
}

func TestReflectMentalModelRejectsLateCitationDeletion(t *testing.T) {
	f, _ := reflectFixture(t, "unused")
	m := f.activeMemory("unrelated confidential detail")
	model := reflectModelSafetyRecord(m)
	f.put(colMentalModels, model)
	llm := &passageChangingLLM{fakeWikiLLM: fakeWikiLLM{answers: []string{toolCall("mental_models", map[string]any{}), "private summary detail"}}}
	llm.change = func() {
		if len(llm.prompts) == 2 {
			f.editRecord(colMemories, m.ID, func(r *Record) { r.Status = "deleted" })
		}
	}
	f.svc.Wiki = llm
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "lunch", MaxIterations: 2})
	if len(llm.prompts) != 2 || !strings.Contains(llm.prompts[1], "private summary detail") {
		t.Fatalf("mental model was not used: %v", err)
	}
	if err == nil || out.Answer != "" {
		t.Fatalf("late deleted citation escaped: %v %q", err, out.Answer)
	}
}

func TestReflectMentalModelCitationAndSummaryFences(t *testing.T) {
	for _, mode := range []string{"unchanged", "erased", "quarantined", "same_revision_text", "revision", "unapproved", "source_revoked", "transfer", "model_deleted", "model_changed", "model_read_revoked"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := reflectFixture(t, "unused")
			m := f.activeMemory("unrelated confidential detail")
			model := reflectModelSafetyRecord(m)
			f.put(colMentalModels, model)
			llm := &passageChangingLLM{fakeWikiLLM: fakeWikiLLM{answers: []string{toolCall("read_mental_models", map[string]any{"ids": []string{model.ID}}), "private summary detail"}}}
			llm.change = func() {
				if len(llm.prompts) != 2 {
					return
				}
				switch mode {
				case "erased":
					m.ContentErased = true
				case "quarantined":
					m.PrivacyQuarantined = true
				case "same_revision_text":
					m.Text = "changed detail"
				case "revision":
					m.Revision++
				case "unapproved":
					m.ApprovedBy = ""
				case "source_revoked":
					f.editRecord("sources", m.Sources[0].SourceID, func(r *Record) { r.Status = "deleted" })
					return
				case "transfer":
					f.svc.Auth = passageTransferDeny{Authorizer: f.svc.Auth, id: m.ID}
					return
				case "model_deleted":
					f.editRecord(colMentalModels, model.ID, func(r *Record) { r.Status = "deleted" })
					return
				case "model_changed":
					f.editRecord(colMentalModels, model.ID, func(r *Record) {
						copy := *r.MentalModel
						active := *copy.Active
						active.Text = "changed summary"
						copy.Active = &active
						r.MentalModel = &copy
					})
					return
				case "model_read_revoked":
					a := f.auth.actors[f.human]
					a.verbs["read"] = false
					f.auth.actors[f.human] = a
					return
				}
				reflectMemoryRawPut(f, m)
			}
			f.svc.Wiki = llm
			out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "lunch", MaxIterations: 2})
			if len(llm.prompts) != 2 || !strings.Contains(llm.prompts[1], "private summary detail") {
				t.Fatalf("model path missing: %v", err)
			}
			if mode == "unchanged" {
				if err != nil || out.Answer == "" {
					t.Fatalf("unchanged rejected: %v", err)
				}
				return
			}
			if err == nil || out.Answer != "" {
				t.Fatalf("changed citation/summary escaped: %v %q", err, out.Answer)
			}
		})
	}
}

func TestReflectMentalModelFirstCitationSnapshotCannotRebind(t *testing.T) {
	f, _ := reflectFixture(t, "unused")
	m := f.activeMemory("confidential detail")
	model := reflectModelSafetyRecord(m)
	f.put(colMentalModels, model)
	run := &reflectRun{in: ReflectInput{Scope: scopeA}}
	view := f.svc.mentalModelView(testCtx, model)
	if err := f.svc.keepReflectMentalModel(testCtx, f.human, run, view); err != nil {
		t.Fatal(err)
	}
	m.Text = "different same revision"
	reflectMemoryRawPut(f, m)
	if err := f.svc.keepReflectMentalModel(testCtx, f.human, run, view); err == nil {
		t.Fatal("citation snapshot rebound")
	}
}

func TestPostgresReflectMentalModelLateCitationFence(t *testing.T) {
	for _, mode := range []string{"unchanged", "deleted", "erased", "transfer"} {
		t.Run(mode, func(t *testing.T) {
			f, p, _ := pgPassageFixture(t)
			src := pgPassageRow(t, p, scopeA, "model-citation-source", "unrelated confidential detail", "model-batch", 1)
			at := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			m := Record{ID: "model-citation", Scope: scopeA, Kind: "fact", Status: statusActive, Revision: 1, Text: "unrelated confidential detail", Sensitivity: "internal", CreatedBy: "human-1", ApprovedBy: "human-1", CreatedAt: at, UpdatedAt: at, CollectedAt: &at, Sources: refs(src)}
			if err := p.Insert(testCtx, colMemories, m); err != nil {
				t.Fatal(err)
			}
			model := reflectModelSafetyRecord(m)
			model.CreatedAt = at
			model.UpdatedAt = at
			if err := p.Insert(testCtx, colMentalModels, model); err != nil {
				t.Fatal(err)
			}
			llm := &passageChangingLLM{fakeWikiLLM: fakeWikiLLM{answers: []string{toolCall("mental_models", map[string]any{}), "private summary detail"}}}
			llm.change = func() {
				if len(llm.prompts) != 2 {
					return
				}
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
			f.svc.Wiki = llm
			out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "lunch", MaxIterations: 2})
			if len(llm.prompts) != 2 || !strings.Contains(llm.prompts[1], "private summary detail") {
				t.Fatalf("model path missing: %v", err)
			}
			if mode == "unchanged" {
				if err != nil || out.Answer == "" {
					t.Fatalf("unchanged rejected: %v", err)
				}
				return
			}
			if err == nil || out.Answer != "" {
				t.Fatalf("PG changed citation escaped: %v %q", err, out.Answer)
			}
		})
	}
}
