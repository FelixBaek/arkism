//go:build linux

package memory

import (
	"fmt"
	"strings"
	"testing"
)

func TestPostgresImportedRereadFullAuthorizedSession(t *testing.T) {
	f, p, _ := pgPassageFixture(t)
	full := strings.Repeat("[2026-10-01] Rowan: ordinary context.\n", 30) + "[2026-10-01] Rowan: the destination is Cedar Bay."
	pgPassageRow(t, p, scopeA, "import-first", full, "import-session", 1)
	last := "[2026-10-01] Mira: the badge reads Lantern."
	pgPassageRow(t, p, scopeA, "import-last", last, "import-session", 2)
	denied := pgPassageRow(t, p, scopeA, "import-denied", "private-canary", "import-session", 3)
	f.svc.Auth = pgPassageDenyTransfer{Authorizer: f.svc.Auth, id: denied.ID}
	other := scopeA
	other.ProjectID += "-other"
	pgPassageRow(t, p, other, "import-foreign", "foreign-canary", "import-session", 1)
	run := &reflectRun{in: ReflectInput{Scope: scopeA, Query: "unmatched"}, hits: []Hit{{Tags: []string{"session:import-session"}}}}
	prompt, allowed := f.svc.rereadTranscripts(testCtx, f.human, run)
	if !strings.Contains(prompt, full) || !allowed[last] || len(run.sourceRecords) != 2 {
		t.Fatal("complete authorized imported session not recovered")
	}
	if strings.Contains(prompt, "private-canary") || strings.Contains(prompt, "foreign-canary") {
		t.Fatal("source gates bypassed")
	}
	if err := f.svc.verifyReflectSources(testCtx, f.human, run); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresImportedRereadLookaheadRejectsPartialBatch(t *testing.T) {
	f, p, _ := pgPassageFixture(t)
	for i := range 101 {
		pgPassageRow(t, p, scopeA, fmt.Sprintf("import-large-%03d", i), "[2026-10-01] Rowan: context.", "import-large", i)
	}
	run := &reflectRun{in: ReflectInput{Scope: scopeA, Query: "unmatched"}, hits: []Hit{{Tags: []string{"session:import-large"}}}}
	_, allowed := f.svc.rereadTranscripts(testCtx, f.human, run)
	if len(allowed) != 0 || len(run.sourceRecords) != 0 || len(run.sourceNotes) != 1 || run.sourceNotes[0].Error != "batch_limit" {
		t.Fatal("lookahead batch silently used as complete", run.sourceNotes)
	}
}

func TestPostgresImportedRereadLateErasureStopsGeneration(t *testing.T) {
	f, p, llm := pgPassageFixture(t)
	line := "[2026-10-01] Rowan: workshop marker."
	src := pgPassageRow(t, p, scopeA, "import-erased", line, "import-erasure", 1)
	reader := &pgPassageChangingModel{fakeWikiLLM: fakeWikiLLM{answers: []string{line}}, change: func() { pgPassageErase(t, p, src.ID) }}
	f.svc.ReflectReread = reader
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "workshop marker", MaxIterations: 1})
	if len(reader.prompts) != 1 || !strings.Contains(reader.prompts[0], line) {
		t.Fatal("real imported reread path not reached", err)
	}
	if err == nil || out.Answer != "" || len(llm.prompts) != 0 {
		t.Fatal("erased source reached answer model", err)
	}
}
