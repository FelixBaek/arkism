package memory

import (
	"errors"
	"testing"
)

func TestGeneratedRecordsRequireProvenance(t *testing.T) {
	f := newFixture(t)
	source := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "original input"}))
	good := Generator{Model: "model-v1", PromptVersion: "prompt-v1", PipelineVersion: "pipeline-v1"}
	for _, role := range []string{"agent", "pipeline", "service"} {
		f.auth.actors["generated"] = actor("generated-actor", role, true, allVerbs...)
		for _, col := range []string{"memory_candidates", "skill_versions", "reflections"} {
			for _, g := range []*Generator{nil, {}, {Model: good.Model}, {Model: good.Model, PromptVersion: good.PromptVersion}} {
				_, e := f.svc.Create(testCtx, "generated", col, CreateInput{Scope: scopeA, Text: "generated content", Sources: refs(source), Generator: g})
				if !errors.Is(e, ErrInvalid) {
					t.Fatal("missing generator accepted", role, col, e)
				}
			}
		}
	}
	if _, e := f.svc.Create(testCtx, f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "generated source", Generated: true})); !errors.Is(e, ErrInvalid) {
		t.Fatal("declared AI source without generator")
	}
	r := f.create("generated", "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "generated fact", Sources: refs(source), Generator: &good}))
	if r.CreatedByType != "service" || r.Generator == nil || *r.Generator != good {
		t.Fatal("generation provenance lost")
	}
	secret := good
	secret.Model = syntheticSecrets[0]
	if _, e := f.svc.Create(testCtx, f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "safe", Generator: &secret, OnSecret: "redact"})); !errors.Is(e, ErrSecretDetected) {
		t.Fatal("secret generator was rewritten or stored")
	}
}

func TestSkillAnalysisRecordsConfiguredGeneration(t *testing.T) {
	f := newFixture(t)
	r := analyzedSkill(t, f)
	if r.Generator == nil || r.Generator.Model != "test-model" || r.CreatedByType != "human" {
		t.Fatal("analysis provenance missing")
	}
	a := &ChatSkillAnalyzer{Config: LLMConfig{Model: "configured-model"}}
	g := a.Generator()
	if g.Model != "configured-model" || !validGenerator(&g) {
		t.Fatal("adapter generation metadata")
	}
}

func TestSourceCannotAcquireAnotherGeneratorOnDedup(t *testing.T) {
	f := newFixture(t)
	g := Generator{Model: "first-model", PromptVersion: "prompt-1", PipelineVersion: "pipeline-1"}
	in := CreateInput{Scope: scopeA, Text: "generated original", Generator: &g}
	first := f.create(f.human, "sources", sourceInput(in))
	other := g
	other.Model = "another-model"
	in.Generator = &other
	if _, e := f.svc.Create(testCtx, f.human, "sources", sourceInput(in)); !errors.Is(e, ErrConflict) {
		t.Fatal("source provenance changed via dedup")
	}
	if got := f.get(f.human, "sources", scopeA, first.ID); got.Generator.Model != "first-model" {
		t.Fatal("original generator overwritten")
	}
}

func TestMemoryProvenanceLifecycle(t *testing.T) {
	f := newFixture(t)
	source := f.source()
	candidate := f.create(f.agent, "memory_candidates", generatedCandidate(scopeA, refs(source)))
	if candidate.Status != "candidate" || candidate.VerificationStatus != VerificationCandidate || candidate.SourceEventID != source.ID {
		t.Fatal("candidate provenance missing", candidate)
	}
	approved := f.transition(f.human, "memory_candidates", candidate.ID, ChangeInput{Scope: scopeA, ExpectedRevision: candidate.Revision, Status: "approved"})
	if approved.VerificationStatus != VerificationVerified || approved.SourceEventID != candidate.SourceEventID {
		t.Fatal("approval did not verify immutable evidence", approved)
	}
	memory, err := f.repo.Get(testCtx, colMemories, scopeA, approved.ID)
	if err != nil || !verifiedMemory(memory) || memory.SourceEventID != source.ID {
		t.Fatal("verified memory missing evidence", memory, err)
	}

	bad := generatedCandidate(scopeA, refs(source))
	bad.SourceEventID = "unrelated-event"
	if _, err = f.svc.Create(testCtx, f.agent, "memory_candidates", bad); !errors.Is(err, ErrDenied) {
		t.Fatal("unrelated source event accepted", err)
	}
	run := f.create(f.agent, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "evidence run", Sources: refs(source)}))
	bad = generatedCandidate(scopeA, refs(source))
	bad.TraceID = run.ID
	if _, err = f.svc.Create(testCtx, f.agent, "memory_candidates", bad); !errors.Is(err, ErrDenied) {
		t.Fatal("unlinked trace accepted", err)
	}
}

func TestAgentRunContextOnlyInjectsVerifiedMemory(t *testing.T) {
	f := newFixture(t)
	memory := f.activeMemory("verified-context-needle", "verified-context-needle")
	source := f.source()
	run := f.create(f.agent, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "run", Sources: refs(source)}))

	pack, err := f.svc.BuildContext(testCtx, f.agent, ContextBuildInput{Scope: scopeA, Task: "verified-context-needle", RunID: run.ID, Modes: []string{"exact"}, TokenEncoding: "cl100k_base"})
	if err != nil || len(pack.Items) != 1 || pack.Items[0].ID != memory.ID {
		t.Fatal("verified memory not injected", pack, err)
	}

	tampered := memory
	tampered.VerificationStatus = VerificationCandidate
	f.repo.store[colMemories][recKey(scopeA, memory.ID)] = tampered
	pack, err = f.svc.BuildContext(testCtx, f.agent, ContextBuildInput{Scope: scopeA, Task: "verified-context-needle", RunID: run.ID, Modes: []string{"exact"}, TokenEncoding: "cl100k_base"})
	if err != nil || len(pack.Items) != 0 {
		t.Fatal("unverified memory reached agent context", pack, err)
	}
}

func TestLearningCandidateKeepsRunEvidence(t *testing.T) {
	f, run, evaluation := learningFixture(t, "success", true)
	result, err := f.svc.Learn(testCtx, f.agent, LearningInput{Scope: scopeA, Run: Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}, Evaluation: Ref{Collection: "evaluations", ID: evaluation.ID, Revision: evaluation.Revision}, TaskType: "repair", Lesson: "use trace evidence", VerificationMethod: "independent replay"})
	if err != nil {
		t.Fatal(err)
	}
	lesson, err := f.repo.Get(testCtx, "memory_candidates", scopeA, result.Lesson.ID)
	if err != nil || lesson.TraceID != run.ID || lesson.SourceEventID == "" || lesson.VerificationStatus != VerificationCandidate {
		t.Fatal("learning candidate lost evidence", lesson, err)
	}
}
