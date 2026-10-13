//go:build linux

package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func handoffFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.svc.SyntheticHandoff = true
	f.svc.BlobDirectory = blobTestDir(t)
	for _, token := range []string{f.human, f.human2, f.agent, f.work} {
		old := f.auth.actors[token]
		old.verbs["event"] = true
		old.verbs["write"] = true
		f.auth.actors[token] = old
	}
	return f
}
func handoffCreate(t *testing.T, f *fixture, key string) Consultation {
	t.Helper()
	c, e := f.svc.CreateConsultation(testCtx, f.human, CreateConsultationInput{Scope: scopeA, IdempotencyKey: key, Goal: "합성 Etsy 포스터 연습", Synthetic: true})
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func handoffEvent(t *testing.T, f *fixture, c Consultation, kind string, p ConsultationEventPayload) Consultation {
	t.Helper()
	token := f.human
	if kind == "clarification_requested" || kind == "artifacts_generated" || kind == "failed" || kind == "timed_out" {
		token = f.work
	}
	out, e := f.svc.ConsultationEvent(testCtx, token, c.ID, ConsultationEventInput{Scope: c.Scope, EventID: fmt.Sprintf("event-%d-%s", c.Revision, kind), ExpectedRevision: c.Revision, Type: kind, Payload: p})
	if e != nil {
		t.Fatalf("%s: %v", kind, e)
	}
	return out
}
func handoffApproved(t *testing.T, f *fixture, key string) Consultation {
	c := handoffCreate(t, f, key)
	c = handoffEvent(t, f, c, "product_selected", ConsultationEventPayload{Product: "Printable Poster"})
	c = handoffEvent(t, f, c, "hire_proposed", ConsultationEventPayload{})
	if c.Child != nil {
		t.Fatal("unapproved child")
	}
	return handoffEvent(t, f, c, "hire_approved", ConsultationEventPayload{})
}
func handoffArtifacts(t *testing.T, f *fixture, c Consultation) Consultation {
	t.Helper()
	r := consultationRecord(c.Child.TraceID, c.Scope, "runtime")
	r.Kind = "execution_trace"
	r.Relations = []Ref{{Collection: "agent_runs", ID: c.Child.AgentRunID, Revision: 1}}
	r.Sources = []SourceRef{{SourceID: c.SourceEventID, Locator: "package"}}
	if err := f.svc.Repo.Insert(testCtx, "execution_traces", r); err != nil {
		t.Fatal(err)
	}
	return handoffEvent(t, f, c, "artifacts_generated", ConsultationEventPayload{TraceID: c.Child.TraceID, Artifacts: []ConsultationArtifact{{Title: "포스터 콘셉트", Kind: "poster_concepts", Text: "Synthetic poster concept: quiet blue hills."}, {Title: "이미지 프롬프트", Kind: "image_prompts", Text: "Synthetic image prompt: blue rolling hills, no image generated."}, {Title: "인쇄 명세", Kind: "print_spec", Text: "Synthetic print spec: A3, 300 DPI; no physical proof."}}})
}
func TestSyntheticHandoffLifecycleRecoveryAndFiles(t *testing.T) { exerciseSyntheticHandoff(t, nil) }
func TestSyntheticHandoffDirectProductCompletion(t *testing.T) {
	f := handoffFixture(t)
	c := handoffCreate(t, f, "direct-planner")
	c = handoffEvent(t, f, c, "product_selected", ConsultationEventPayload{Product: "Digital Planner"})
	if c.Child != nil || c.Parent.Status != "direct_mode" {
		t.Fatal("direct product selection cannot finish without hiring")
	}
	oldParent := c.Parent
	c = handoffEvent(t, f, c, "product_selected", ConsultationEventPayload{Product: "Wedding Template"})
	if c.Parent.AgentRunID == oldParent.AgentRunID || c.Parent.TraceID == oldParent.TraceID {
		t.Fatal("changed brief must not reuse an immutable synthesis receipt")
	}
	trace := consultationRecord(c.Parent.TraceID, c.Scope, "runtime")
	trace.Relations = []Ref{{Collection: "agent_runs", ID: c.Parent.AgentRunID, Revision: 1}}
	trace.Sources = []SourceRef{{SourceID: c.SourceEventID, Locator: "package"}}
	if err := f.svc.Repo.Insert(testCtx, "execution_traces", trace); err != nil {
		t.Fatal(err)
	}
	c = handoffEvent(t, f, c, "direct_completed", ConsultationEventPayload{TraceID: c.Parent.TraceID})
	if c.Status != "completed" || c.Child != nil {
		t.Fatal("direct completion must not create a specialist")
	}
}
func TestPostgresSyntheticHandoffLifecycleRecoveryAndFiles(t *testing.T) {
	exerciseSyntheticHandoff(t, isolatedPostgres(t))
}
func exerciseSyntheticHandoff(t *testing.T, repo Repository) {
	f := handoffFixture(t)
	if repo != nil {
		f.svc.Repo = repo
	}
	c := handoffApproved(t, f, "happy")
	child := *c.Child
	c = handoffEvent(t, f, c, "clarification_requested", ConsultationEventPayload{Question: "어떤 색상을 원하나요?"})
	if c.Child.Status != "waiting_for_user" {
		t.Fatal(c.Child)
	}
	c = handoffEvent(t, f, c, "clarification_answered", ConsultationEventPayload{Answer: "파란색"})
	if c.Child.ThreadID != child.ThreadID {
		t.Fatal("new thread on resume")
	}
	c = handoffArtifacts(t, f, c)
	if len(c.Artifacts) != 3 {
		t.Fatal(c.Artifacts)
	}
	for _, a := range c.Artifacts {
		body, e := f.svc.ReadArtifact(testCtx, f.human, c.Scope, c.SourceEventID, a.ID)
		if e != nil || len(body) == 0 {
			t.Fatalf("file %s: %v", a.ID, e)
		}
		c = handoffEvent(t, f, c, "artifact_selected", ConsultationEventPayload{ArtifactID: a.ID})
		c = handoffEvent(t, f, c, "artifact_approved", ConsultationEventPayload{ArtifactID: a.ID})
	}
	input := ConsultationEventInput{Scope: c.Scope, EventID: "return-stable", ExpectedRevision: c.Revision, Type: "handoff_returned"}
	c, e := f.svc.ConsultationEvent(testCtx, f.human, c.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	again, e := f.svc.ConsultationEvent(testCtx, f.human, c.ID, input)
	if e != nil || again.Revision != c.Revision {
		t.Fatalf("duplicate: %v", e)
	}
	parentTrace := consultationRecord(c.Parent.TraceID, c.Scope, "runtime")
	parentTrace.Relations = []Ref{{Collection: "agent_runs", ID: c.Parent.AgentRunID, Revision: 1}}
	parentTrace.Sources = []SourceRef{{SourceID: c.SourceEventID, Locator: "package"}}
	if e = f.svc.Repo.Insert(testCtx, "execution_traces", parentTrace); e != nil {
		t.Fatal(e)
	}
	c = handoffEvent(t, f, c, "integrated", ConsultationEventPayload{TraceID: c.Parent.TraceID})
	if c.Status != "completed" || c.Handoff.ReturnPackage.Authority != "data_only_untrusted" {
		t.Fatal(c)
	}
	// Approval control rows and event history carry no copied dialogue/artifact bodies.
	approval, e := f.svc.Repo.Get(testCtx, "jobs", c.Scope, c.Handoff.ApprovalRequestID)
	if e != nil || approval.Text != "" || approval.Status != "approved" {
		t.Fatalf("approval: %v", e)
	}
	restored, e := f.svc.GetConsultation(testCtx, f.human, c.ID, c.Scope)
	if e != nil || restored.Child.AgentRunID != child.AgentRunID || restored.Revision != c.Revision {
		t.Fatalf("restore: %v", e)
	}
}
func TestSyntheticHandoffRejectScopeRolesBudgetAndQuarantine(t *testing.T) {
	f := handoffFixture(t)
	c := handoffCreate(t, f, "reject")
	again, e := f.svc.CreateConsultation(testCtx, f.human, CreateConsultationInput{Scope: scopeA, IdempotencyKey: "reject", Goal: c.Goal, Synthetic: true})
	if e != nil || again.ID != c.ID {
		t.Fatal(e)
	}
	_, e = f.svc.CreateConsultation(testCtx, f.human, CreateConsultationInput{Scope: scopeA, IdempotencyKey: "reject", Goal: "changed", Synthetic: true})
	if !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	c = handoffEvent(t, f, c, "product_selected", ConsultationEventPayload{Product: "Printable Poster"})
	for _, p := range []ConsultationEventPayload{{MaxHandoffDepth: 3}, {MaxActiveSpecialists: 2}, {EstimatedBudget: 101}} {
		_, e = f.svc.ConsultationEvent(testCtx, f.human, c.ID, ConsultationEventInput{Scope: c.Scope, ExpectedRevision: c.Revision, EventID: "bad-budget", Type: "hire_proposed", Payload: p})
		if !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
	c = handoffEvent(t, f, c, "hire_proposed", ConsultationEventPayload{})
	_, e = f.svc.ConsultationEvent(testCtx, f.agent, c.ID, ConsultationEventInput{Scope: c.Scope, ExpectedRevision: c.Revision, EventID: "agent-approve", Type: "hire_approved"})
	if !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	c = handoffEvent(t, f, c, "hire_rejected", ConsultationEventPayload{})
	if c.Child != nil || c.Parent.Status != "direct_mode" {
		t.Fatal(c)
	}
	_, e = f.svc.GetConsultation(testCtx, f.human, c.ID, scopeB)
	if !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	source, e := f.repo.Get(testCtx, "sources", c.Scope, c.SourceEventID)
	if e != nil {
		t.Fatal(e)
	}
	source.PrivacyQuarantined = true
	source.Revision++
	if e = f.repo.Replace(testCtx, "sources", source, source.Revision-1); e != nil {
		t.Fatal(e)
	}
	_, e = f.svc.GetConsultation(testCtx, f.human, c.ID, c.Scope)
	if !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	_, e = f.svc.ConsultationEvent(testCtx, f.human, c.ID, ConsultationEventInput{Scope: c.Scope, ExpectedRevision: c.Revision, EventID: "late-proposal", Type: "hire_proposed"})
	if !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
}
func TestSyntheticHandoffCancellationRetryAndAtomicity(t *testing.T) {
	f := handoffFixture(t)
	c := handoffApproved(t, f, "cancel")
	child := *c.Child
	c = handoffArtifacts(t, f, c)
	c = handoffEvent(t, f, c, "cancelled", ConsultationEventPayload{})
	for _, a := range c.Artifacts {
		if !a.Partial {
			t.Fatal("partial missing")
		}
	}
	c = handoffEvent(t, f, c, "retried", ConsultationEventPayload{})
	if c.Child.AgentRunID != child.AgentRunID || c.Child.ThreadID != child.ThreadID {
		t.Fatal("duplicate child")
	}
	for _, a := range c.Artifacts {
		if a.Status != "superseded" {
			t.Fatal(a)
		}
	}
	c = handoffEvent(t, f, c, "timed_out", ConsultationEventPayload{})
	c = handoffEvent(t, f, c, "direct_continued", ConsultationEventPayload{})
	if c.Parent.Status != "direct_mode" || c.Child.Status != "failed" {
		t.Fatal(c)
	}
	f.repo.failAudit = true
	_, e := f.svc.ConsultationEvent(testCtx, f.human, c.ID, ConsultationEventInput{Scope: c.Scope, ExpectedRevision: c.Revision, EventID: "failed-tx", Type: "cancelled"})
	if e == nil {
		t.Fatal("audit failure accepted")
	}
	f.repo.failAudit = false
	restored, e := f.svc.GetConsultation(testCtx, f.human, c.ID, c.Scope)
	if e != nil || restored.Revision != c.Revision || restored.Status != c.Status {
		t.Fatal("failed transaction changed snapshot")
	}
}
func TestSyntheticHandoffExplicitSourceLineage(t *testing.T) {
	ids, e := tracePayloadSourceIDs(json.RawMessage(`{"source_dependencies":[{"kind":"source_event","id":"root-source"}]}`))
	if e != nil || len(ids) != 1 || ids[0] != "root-source" {
		t.Fatalf("%v %v", ids, e)
	}
	for _, raw := range []string{`{"source_dependencies":[{"kind":"trace","id":"root-source"}]}`, `{"source_dependencies":[{"kind":"source_event","id":""}]}`} {
		if _, e = tracePayloadSourceIDs(json.RawMessage(raw)); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
}
