//go:build linux

package memory

import (
	"fmt"
	"strings"
	"testing"
)

func TestReflectRereadImportedSourceRefs(t *testing.T) {
	for _, via := range []string{"query", "session"} {
		t.Run(via, func(t *testing.T) {
			f, _ := reflectFixture(t)
			line := "Rowan: The harbor workshop opens on Thursday."
			reflectConversationBatch(f, line, "imported-workshop-session", 0, func(r *Record) { r.Keywords = []string{"workshop"} })
			run := &reflectRun{in: ReflectInput{Scope: scopeA, Query: "workshop"}}
			if via == "session" {
				run.in.Query = "unrelated"
				run.hits = []Hit{{Tags: []string{"session:imported-workshop-session"}}}
			}
			user, allowed := f.svc.rereadTranscripts(testCtx, f.human, run)
			if !allowed[line] || !strings.Contains(user, line) {
				t.Fatal("imported conversation batch missing")
			}
		})
	}
}

func TestReflectRereadImportedSourceGates(t *testing.T) {
	for _, mode := range []string{"read", "transfer", "filter", "scope", "erased", "exact_ref"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := reflectFixture(t)
			src := reflectConversationBatch(f, "Rowan: restricted workshop detail.", "imported-gate", 0, func(r *Record) { r.CreatedBy = "other" })
			run := &reflectRun{in: ReflectInput{Scope: scopeA, Query: "absent"}, hits: []Hit{{Tags: []string{"session:imported-gate"}}}}
			switch mode {
			case "read":
				f.svc.Auth = denyAction{f.auth, "read:sources"}
			case "transfer":
				f.svc.Auth = passageTransferDeny{Authorizer: f.auth, id: src.ID}
			case "filter":
				run.in.CreatedBy = []string{"wanted"}
			case "scope":
				run.in.Scope.ProjectID = "other-project"
			case "erased":
				f.editRecord("sources", src.ID, func(r *Record) { r.ContentErased = true })
			case "exact_ref":
				run.hits[0].Tags = []string{"session:imported"}
			}
			_, allowed := f.svc.rereadTranscripts(testCtx, f.human, run)
			if len(allowed) != 0 {
				t.Fatal("imported source bypassed", mode)
			}
		})
	}
}

func TestReflectRereadImportedWholeBatchAndFirstSnapshot(t *testing.T) {
	f, _ := reflectFixture(t)
	text := strings.Repeat("Rowan: Ordinary workshop context.\n", 100) + "Mira: The final answer is at the end."
	src := reflectConversationBatch(f, text, "imported-whole", 0)
	run := &reflectRun{in: ReflectInput{Scope: scopeA, Query: "absent"}, hits: []Hit{{Tags: []string{"session:imported-whole"}}}}
	user, allowed := f.svc.rereadTranscripts(testCtx, f.human, run)
	if !strings.Contains(user, text) || !allowed["Mira: The final answer is at the end."] {
		t.Fatal("whole source truncated")
	}
	f.editRecord("sources", src.ID, func(r *Record) { r.Text = "Rowan: replacement" })
	f.svc.rereadTranscripts(testCtx, f.human, run)
	if err := f.svc.verifyReflectSources(testCtx, f.human, run); err == nil {
		t.Fatal("first snapshot rebound")
	}
}

func TestReflectRereadImportedLateDeletionStopsAnswer(t *testing.T) {
	f, llm := reflectFixture(t)
	line := "Rowan: workshop marker."
	src := reflectConversationBatch(f, line, "imported-late", 0, func(r *Record) { r.Keywords = []string{"workshop"} })
	f.svc.ReflectReread = &passageChangingLLM{fakeWikiLLM: fakeWikiLLM{answers: []string{line}}, change: func() { f.editRecord("sources", src.ID, func(r *Record) { r.ContentErased = true }) }}
	llm.answers = []string{"must not be delivered"}
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "workshop", MaxIterations: 1})
	if err == nil || out.Answer != "" {
		t.Fatal("erased reread evidence delivered", err)
	}
}

func TestReflectRereadImportedOversizedOrAmbiguousBatchSkipped(t *testing.T) {
	for _, mode := range []string{"lookahead", "runes", "origin"} {
		t.Run(mode, func(t *testing.T) { checkImportedRereadBatchBound(t, mode) })
	}
}

func checkImportedRereadBatchBound(t *testing.T, mode string) {
	t.Helper()

	f, _ := reflectFixture(t)
	n := 1
	if mode == "lookahead" {
		n = 101
	}
	if mode == "origin" {
		n = 2
	}
	for i := range n {
		text := "Rowan: whole conversation."
		if mode == "runes" {
			text = strings.Repeat("a", reflectRereadRunes+1)
		}
		reflectConversationBatch(f, text, "imported-bound", i, func(r *Record) {
			if mode == "origin" && i == 1 {
				r.OriginProduct = "another-importer"
			}
		})
	}
	run := &reflectRun{in: ReflectInput{Scope: scopeA, Query: "absent"}, hits: []Hit{{Tags: []string{"session:imported-bound"}}}}
	_, allowed := f.svc.rereadTranscripts(testCtx, f.human, run)
	if len(allowed) != 0 || len(run.sourceRecords) != 0 {
		t.Fatal("incomplete or mixed-origin batch admitted")
	}
}

func TestReflectRereadImportedEmptyRefsNeverScanAllSources(t *testing.T) {
	f, _ := reflectFixture(t)
	reflectConversationBatch(f, "Rowan: workshop unrelated.", "unrelated-import", 0, func(r *Record) { r.Keywords = []string{"workshop"} })
	for _, ref := range []string{"", " ", "\t\n"} {
		run := &reflectRun{in: ReflectInput{Scope: scopeA, Query: "absent"}, hits: []Hit{{Tags: []string{"session:" + ref}}}}
		if refs := f.svc.rereadRefs(testCtx, run); len(refs) != 0 {
			t.Fatal("empty session produced lookup", refs)
		}
	}
}

func TestReflectRereadImportedSessionFeedsActualAnswer(t *testing.T) {
	f, llm, _ := rereadFixture(t)
	line := "Mira: The workshop badge says Pebble."
	reflectConversationBatch(f, line, "s1", 0)
	reader := &fakeWikiLLM{answers: []string{line}}
	f.svc.ReflectReread = reader
	llm.answers = []string{"The badge says Pebble."}
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle launch when?", MaxIterations: 1})
	if err != nil || len(reader.prompts) != 1 || len(llm.prompts) != 1 || !strings.Contains(llm.prompts[0], line) || out.Usage.Requests != 2 {
		t.Fatal("imported session did not reach answer", err)
	}
}

func TestReflectRereadQueryBeforeImportedSessionFallbacks(t *testing.T) {
	f, _ := reflectFixture(t)
	run := &reflectRun{in: ReflectInput{Scope: scopeA, Query: "rare-marker"}}
	for i := range reflectRereadBatches {
		ref := fmt.Sprintf("imported-routine-%d", i)
		reflectConversationBatch(f, fmt.Sprintf("Rowan: routine %d.", i), ref, 0)
		run.hits = append(run.hits, Hit{Tags: []string{"session:" + ref}})
	}
	line := "Mira: rare-marker is the needed direct evidence."
	reflectConversationBatch(f, line, "imported-direct", 0, func(r *Record) { r.Keywords = []string{"rare-marker"} })
	_, allowed := f.svc.rereadTranscripts(testCtx, f.human, run)
	if !allowed[line] {
		t.Fatal("query-matched evidence crowded out by imported session fallbacks")
	}
}
