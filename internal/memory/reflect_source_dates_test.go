//go:build linux

package memory

import (
	"strings"
	"testing"
	"time"
)

func sourceDateRun(src Record) *reflectRun {
	return &reflectRun{in: ReflectInput{Scope: scopeA, Query: "When did the workshop happen?", MaxIterations: 1}, hits: []Hit{{ID: "dated-memory", Collection: colMemories, Text: "The workshop was yesterday", Sources: refs(src)}}}
}

func TestReflectSourceDateAdmission(t *testing.T) {
	for _, mode := range []string{"allowed", "file", "ambiguous", "missing", "restricted", "filter", "transfer", "span", "unbound", "read_denied", "same_date"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := reflectFixture(t, "answer")
			at := time.Date(2023, 8, 13, 0, 0, 0, 0, time.UTC)
			input := sourceInput(CreateInput{Scope: scopeA, Kind: "message", Text: "The workshop was yesterday"})
			input.CollectedAt = &at
			src := f.create(f.human, "sources", input)
			run := sourceDateRun(src)
			switch mode {
			case "read_denied":
				actor := f.auth.actors[f.human]
				actor.verbs["read"] = false
				f.auth.actors[f.human] = actor
			case "same_date":
				secondInput := sourceInput(CreateInput{Scope: scopeA, Kind: "message", Text: "another source same observation day"})
				secondInput.CollectedAt = &at
				second := f.create(f.human, "sources", secondInput)
				run.hits[0].Sources = append(run.hits[0].Sources, sourceReference(second, "original"))
			case "file":
				run.hits[0].Sources[0].Locator = "file:notes"
			case "missing":
				run.hits[0].Sources[0].SourceID = "missing"
			case "ambiguous":
				later := at.AddDate(0, 0, 1)
				otherInput := sourceInput(CreateInput{Scope: scopeA, Kind: "message", Text: "another workshop"})
				otherInput.CollectedAt = &later
				other := f.create(f.human, "sources", otherInput)
				run.hits[0].Sources = append(run.hits[0].Sources, sourceReference(other, "original"))
			case "restricted":
				src.Sensitivity = "restricted"
				if err := f.repo.Replace(testCtx, "sources", src, src.Revision); err != nil {
					t.Fatal(err)
				}
			case "filter":
				run.in.CreatedBy = []string{"different-recorder"}
			case "transfer":
				f.svc.Auth = pgPassageDenyTransfer{Authorizer: f.svc.Auth, id: src.ID}
			case "unbound":
				run.hits[0].Sources[0].Span = nil
			case "span":
				run.hits[0].Sources[0].Span = &SourceSpan{Start: 0, End: 1, ContentHash: ContentHash("different")}
			}
			f.svc.reflectSourceAnchors(testCtx, f.human, run)
			if mode != "allowed" && mode != "same_date" {
				if run.hits[0].CollectedAt != nil || len(run.sourceRecords) != 0 {
					t.Fatal("inadmissible date disclosed")
				}
				return
			}
			if run.hits[0].CollectedAt == nil || !run.hits[0].CollectedAt.Equal(at) {
				t.Fatal("missing source date")
			}
			_, prompt := reflectPrompt(run, true)
			if !strings.Contains(prompt, `"recorded_at":"2023-08-13T00:00:00Z"`) {
				t.Fatal(prompt)
			}
			if err := f.svc.verifyReflectSources(testCtx, f.human, run); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPostgresReflectSourceDateFence(t *testing.T) {
	for _, mode := range []string{"erased", "date_changed", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			f, p, _ := pgPassageFixture(t)
			src := pgPassageRow(t, p, scopeA, "date-source", "source text", "date-batch", 1, func(r *Record) { r.Kind = "message" })
			run := sourceDateRun(src)
			f.svc.reflectSourceAnchors(testCtx, f.human, run)
			if run.hits[0].CollectedAt == nil {
				t.Fatal("date not admitted")
			}
			if err := f.svc.verifyReflectSources(testCtx, f.human, run); err != nil {
				t.Fatal("initial source fence", err)
			}
			switch mode {
			case "erased":
				pgPassageErase(t, p, src.ID)
			case "date_changed":
				var err error
				src, err = p.Get(testCtx, "sources", scopeA, src.ID)
				if err != nil {
					t.Fatal(err)
				}
				changed := src.CollectedAt.AddDate(0, 0, 1)
				src.CollectedAt = &changed
				// Simulate out-of-band repair without revision change. Ordinary
				// repository replacement correctly forbids changing provenance.
				payload, doc, err := encodeRecord(src)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = p.q().Exec(testCtx, `UPDATE memory_records SET payload=$1,doc=$2::jsonb WHERE collection='sources' AND id=$3`, payload, string(doc), src.ID); err != nil {
					t.Fatal(err)
				}
			case "revoked":
				f.svc.Auth = pgPassageDenyTransfer{Authorizer: f.svc.Auth, id: src.ID}
			}
			if err := f.svc.verifyReflectSources(testCtx, f.human, run); err == nil {
				t.Fatal("changed source metadata delivered")
			}
		})
	}
}

func TestFileReadTimeIsNotOriginalObservation(t *testing.T) {
	at := time.Now().UTC()
	r := Record{MemoryContext: &MemoryContext{ObservedAt: &at}, Sources: []SourceRef{{SourceID: "file-source", Locator: "notes.md:8"}}}
	if HitFromRecord(colMemories, r).CollectedAt != nil {
		t.Fatal("file read time became original statement time")
	}
}

func TestPostgresReflectSourceDateModelDelivery(t *testing.T) {
	f, p, _ := pgPassageFixture(t)
	src := pgPassageRow(t, p, scopeA, "model-date-source", "original message", "model-date-batch", 1, func(r *Record) { r.Kind = "message" })
	model := &pgPassageChangingModel{fakeWikiLLM: fakeWikiLLM{answers: []string{"dated answer"}}, change: func() { pgPassageErase(t, p, src.ID) }}
	f.svc.Wiki = model
	run := sourceDateRun(src)
	out, err := f.svc.reflectLoop(testCtx, f.human, run, ReflectOutput{})
	if len(model.prompts) != 1 || !strings.Contains(model.prompts[0], `"recorded_at":"2026-10-01T00:00:00Z"`) {
		t.Fatal("model did not receive admitted source date", model.prompts, err)
	}
	if err == nil || out.Answer != "" {
		t.Fatal("answer based on erased source date delivered", out, err)
	}
}
