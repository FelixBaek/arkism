//go:build linux

package memory

import (
	"strings"
	"testing"
)

func TestReflectExpandedSourceDeliveryFence(t *testing.T) {
	for _, change := range []string{"unchanged", "erased", "revoked"} {
		t.Run(change, func(t *testing.T) {
			for _, depth := range []string{"chunk", "document"} {
				t.Run(depth, func(t *testing.T) {
					f, _ := reflectFixture(t)
					mem := f.activeMemory("alpha evidence summary", "alpha")
					src, err := f.repo.Get(testCtx, "sources", scopeA, mem.Sources[0].SourceID)
					if err != nil {
						t.Fatal(err)
					}
					model := &pgPassageChangingModel{fakeWikiLLM: fakeWikiLLM{answers: []string{toolCall("expand", map[string]any{"id": mem.ID, "depth": depth}), "answer using expanded source"}}}
					model.change = func() {
						if len(model.prompts) != 2 {
							return
						}
						if change == "unchanged" {
							return
						}
						if change == "revoked" {
							f.svc.Auth = passageTransferDeny{Authorizer: f.svc.Auth, id: src.ID}
							return
						}
						cur, e := f.repo.Get(testCtx, "sources", scopeA, src.ID)
						if e != nil {
							t.Fatal(e)
						}
						cur.ContentErased = true
						cur.Text = ""
						cur.Revision++
						if e = f.repo.Replace(testCtx, "sources", cur, cur.Revision-1); e != nil {
							t.Fatal(e)
						}
					}
					f.svc.Wiki = model
					out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "alpha", MaxIterations: 2})
					if len(model.prompts) != 2 || !strings.Contains(model.prompts[1], "src_"+src.ID) {
						t.Fatal("expanded source was not exercised", err, model.prompts)
					}
					if change == "unchanged" {
						if err != nil || out.Answer == "" {
							t.Fatal("unchanged source answer rejected", err)
						}
						return
					}
					if err == nil || out.Answer != "" {
						t.Fatal("erased expanded source answer delivered", out.Answer, err)
					}
				})
			}

		})
	}
}

func TestReflectExpandedSourceAdmission(t *testing.T) {
	for _, mode := range []string{"transfer_denied", "restricted", "changed_hash", "recorder_filter"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := reflectFixture(t)
			mem := f.activeMemory("alpha summary", "alpha")
			src, err := f.repo.Get(testCtx, "sources", scopeA, mem.Sources[0].SourceID)
			if err != nil {
				t.Fatal(err)
			}
			run := &reflectRun{in: ReflectInput{Scope: scopeA, Query: "alpha"}, hits: []Hit{HitFromRecord(colMemories, mem)}}
			switch mode {
			case "transfer_denied":
				f.svc.Auth = passageTransferDeny{Authorizer: f.svc.Auth, id: src.ID}
			case "recorder_filter":
				run.in.CreatedBy = []string{"different-recorder"}
			default:
				if mode == "restricted" {
					src.Sensitivity = "restricted"
				} else {
					src.Text = "different source text"
				}
				src.Revision++
				if err = f.repo.Replace(testCtx, "sources", src, src.Revision-1); err != nil {
					t.Fatal(err)
				}
			}
			for _, depth := range []string{"chunk", "document"} {
				hits, err := f.svc.reflectExpand(testCtx, f.human, run, reflectToolArgs{ID: mem.ID, Depth: depth})
				if err != nil || len(hits) != 0 {
					t.Fatal("ineligible expanded source admitted", depth, len(hits), err)
				}
			}
		})
	}
}

func TestPostgresReflectExpandedSourceDeliveryFence(t *testing.T) {
	for _, depth := range []string{"chunk", "document"} {
		t.Run(depth, func(t *testing.T) {
			f, p, _ := pgPassageFixture(t)
			src := pgPassageRow(t, p, scopeA, "expand-source", "A source detail absent from its summary.", "expand-batch", 1, func(r *Record) { r.Kind = "note" })
			mem := Record{ID: "expand-memory", Scope: scopeA, Kind: "semantic", Status: statusActive, Revision: 1, Text: "A brief summary", Sensitivity: "internal", Sources: refs(src), CreatedAt: src.CreatedAt, UpdatedAt: src.UpdatedAt}
			if err := p.Insert(testCtx, colMemories, mem); err != nil {
				t.Fatal(err)
			}
			model := &pgPassageChangingModel{fakeWikiLLM: fakeWikiLLM{answers: []string{toolCall("expand", map[string]any{"id": mem.ID, "depth": depth}), "answer using expanded source"}}}
			model.change = func() {
				if len(model.prompts) == 2 {
					pgPassageErase(t, p, src.ID)
				}
			}
			f.svc.Wiki = model
			run := &reflectRun{in: ReflectInput{Scope: scopeA, Query: "What detail?", MaxIterations: 2}, hits: []Hit{HitFromRecord(colMemories, mem)}, seen: map[string]bool{mem.ID: true}, graph: map[string]bool{}}
			out, err := f.svc.reflectLoop(testCtx, f.human, run, ReflectOutput{})
			if len(model.prompts) != 2 || !strings.Contains(model.prompts[1], "src_"+src.ID) {
				t.Fatal("source expansion not exercised", err, model.prompts)
			}
			if err == nil || out.Answer != "" {
				t.Fatal("erased expanded source delivered", out.Answer, err)
			}
		})
	}
}

func TestReflectExpandPreservesPreviouslyAdmittedSnapshot(t *testing.T) {
	f, _ := reflectFixture(t)
	// Imported sources need not contain a self-reference; use a standalone source.
	src := Record{ID: "snapshot-source", Scope: scopeA, Kind: "note", Status: statusActive, Revision: 1, Text: "earlier source version", Sensitivity: "internal"}
	if err := f.repo.Insert(testCtx, "sources", src); err != nil {
		t.Fatal(err)
	}
	run := &reflectRun{in: ReflectInput{Scope: scopeA, Query: "alpha"}, passages: []string{src.Text}}
	run.keepSources([]Record{src}, false)
	changed := src
	changed.Text = "new source version"
	changed.Revision++
	if err := f.repo.Replace(testCtx, "sources", changed, src.Revision); err != nil {
		t.Fatal(err)
	}
	candidate := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "new summary", Sources: refs(changed)}))
	approved := f.transition(f.human, "memory_candidates", candidate.ID, ChangeInput{Scope: scopeA, ExpectedRevision: candidate.Revision, Status: "approved"})
	mem := f.transition(f.human, colMemories, approved.ID, ChangeInput{Scope: scopeA, ExpectedRevision: approved.Revision, Status: "active"})
	run.hits = []Hit{HitFromRecord(colMemories, mem)}
	hits, err := f.svc.reflectExpand(testCtx, f.human, run, reflectToolArgs{ID: mem.ID, Depth: "document"})
	if err != nil || len(hits) != 1 || hits[0].Text != changed.Text {
		t.Fatal("new version expansion not exercised", hits, err)
	}
	snapshot := run.sourceRecords[src.ID]
	if snapshot.record.Text != src.Text || !snapshot.transfer || !snapshot.expanded || snapshot.dateOnly {
		t.Fatal("first source binding replaced or obligations lost")
	}
	if err = f.svc.verifyReflectSources(testCtx, f.human, run); err == nil {
		t.Fatal("old passage accepted under new source snapshot")
	}
}
