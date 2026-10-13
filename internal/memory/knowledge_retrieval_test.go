//go:build linux

package memory

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func knowledgeRetrievalFixture(t *testing.T) (*fixture, KnowledgeIngestInput, KnowledgeIngestResult, Record, KnowledgeRetrieveInput) {
	t.Helper()
	f := handoffFixture(t)
	for _, token := range []string{f.human, f.agent, f.work} {
		a := f.auth.actors[token]
		a.verbs["retrieve"] = true
		f.auth.actors[token] = a
	}
	in := knowledgeFixtureInput()
	in.Content = "Synthetic printable specification sample: 300 dpi. Not commercially validated."
	ing, e := f.svc.IngestKnowledge(testCtx, f.human, in)
	if e != nil {
		t.Fatal(e)
	}
	head, e := f.svc.SetKnowledgeState(testCtx, f.human, colKnowledgeSources, ing.Source.ID, KnowledgeStateInput{Scope: in.Scope, ExpectedRevision: ing.Source.Revision, Status: "active", TrustLevel: "reviewed"})
	if e != nil {
		t.Fatal(e)
	}
	ing.Source = head
	b, e := f.svc.CreateKnowledgeBinding(testCtx, f.human, KnowledgeBindingInput{Scope: in.Scope, BindingKey: "retrieval", KnowledgeSourceID: head.ID, SpecialistProfileID: "illustrator", SpecialistProfileVersionID: "v1", SkillID: "illustrator.v1", TrustRequirement: "reviewed", AccessMode: "synthetic_retrieval"})
	if e != nil {
		t.Fatal(e)
	}
	req := KnowledgeRetrieveInput{Scope: in.Scope, ProfileID: "illustrator", ProfileVersion: "v1", SkillID: "illustrator.v1", SkillVersion: "v1", BindingIDs: []string{b.ID}, Query: "printable specification", MaxTokens: 4096, Limit: 4}
	return f, in, ing, b, req
}
func knowledgeActivate(t *testing.T, f *fixture, b Record) Record {
	t.Helper()
	r, e := f.svc.SetKnowledgeState(testCtx, f.human, colKnowledgeBindings, b.ID, KnowledgeStateInput{Scope: b.Scope, ExpectedRevision: b.Revision, Status: "active_synthetic"})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func knowledgeValidateRequest(r KnowledgeReceipt) KnowledgeRevalidateInput {
	return KnowledgeRevalidateInput{Scope: r.Scope, ProfileID: r.ProfileID, ProfileVersion: r.ProfileVersion, SkillID: r.SkillID, SkillVersion: r.SkillVersion, RetrievalLogID: r.RetrievalLogID, ContextHash: r.ContextHash}
}
func TestKnowledgeRetrievalDefaultDenyAndDeterminism(t *testing.T) {
	f, _, _, b, req := knowledgeRetrievalFixture(t)
	empty, e := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if e != nil || len(empty.Context.Items) != 0 {
		t.Fatalf("candidate binding: %v", e)
	}
	knowledgeActivate(t, f, b)
	one, e := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if e != nil || len(one.Context.Items) != 1 {
		t.Fatalf("retrieval: %v %#v", e, one)
	}
	two, e := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if e != nil || one.ContextHash != two.ContextHash || !reflect.DeepEqual(one.Context, two.Context) {
		t.Fatalf("non deterministic retrieval: %v", e)
	}
	stable, e := f.svc.RevalidateKnowledge(testCtx, f.work, knowledgeValidateRequest(one))
	if e != nil || !reflect.DeepEqual(stable, one) {
		t.Fatalf("revalidation differs: %v", e)
	}
	log, e := f.svc.GetKnowledgeRetrievalLog(testCtx, f.work, one.RetrievalLogID, req.Scope)
	if e != nil {
		t.Fatal(e)
	}
	raw := structuredText(log)
	if strings.Contains(raw, req.Query) || strings.Contains(raw, "300 dpi") {
		t.Fatal("restricted log leaks raw query or document")
	}
	if log.KnowledgeRetrieval.QueryHash != ContentHash(req.Query) || len(log.KnowledgeRetrieval.Selected) != 1 {
		t.Fatal("missing retrieval evidence")
	}
	changed := knowledgeValidateRequest(one)
	changed.ProfileID = "listing_reviewer"
	if _, e = f.svc.RevalidateKnowledge(testCtx, f.work, changed); e == nil {
		t.Fatal("cross profile receipt accepted")
	}
}
func TestKnowledgeRetrievalVersionsTrustAndBindingDrift(t *testing.T) {
	f, in, ing, b, req := knowledgeRetrievalFixture(t)
	b = knowledgeActivate(t, f, b)
	old, e := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if e != nil {
		t.Fatal(e)
	}
	in.ExpectedRevision = ing.Source.Revision
	in.Content += " New revised printable specification."
	updated, e := f.svc.IngestKnowledge(testCtx, f.human, in)
	if e != nil {
		t.Fatal(e)
	}
	noReview, e := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if e != nil || len(noReview.Context.Items) != 0 {
		t.Fatalf("unreviewed source: %v", e)
	}
	if _, e = f.svc.RevalidateKnowledge(testCtx, f.work, knowledgeValidateRequest(old)); e == nil {
		t.Fatal("stale context accepted")
	}
	_, e = f.svc.SetKnowledgeState(testCtx, f.human, colKnowledgeSources, updated.Source.ID, KnowledgeStateInput{Scope: in.Scope, ExpectedRevision: updated.Source.Revision, Status: "active", TrustLevel: "reviewed"})
	if e != nil {
		t.Fatal(e)
	}
	fresh, e := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if e != nil || len(fresh.Context.Items) != 1 || fresh.Context.Items[0].DocumentID != updated.Document.ID {
		t.Fatalf("current version not selected: %v", e)
	}
	log, e := f.svc.GetKnowledgeRetrievalLog(testCtx, f.work, fresh.RetrievalLogID, in.Scope)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, x := range log.KnowledgeRetrieval.Exclusions {
		if x.Reason == "superseded_version" {
			found = true
		}
	}
	if !found {
		t.Fatal("old chunk exclusion not logged")
	}
	if fresh.ExclusionSummary["superseded_version"] == 0 || fresh.ExcludedCount != len(log.KnowledgeRetrieval.Exclusions) || fresh.SelectedCount != 1 || fresh.CandidateCount != len(log.KnowledgeRetrieval.CandidateChunkIDs) {
		t.Fatalf("receipt exclusion summary does not match the log: %+v", fresh)
	}
	if noReview.CandidateCount != 0 || noReview.ExclusionSummary["source_inactive"] != 1 {
		t.Fatalf("excluded binding must be distinguishable from no match: %+v", noReview)
	}
	_, e = f.svc.SetKnowledgeState(testCtx, f.human, colKnowledgeBindings, b.ID, KnowledgeStateInput{Scope: in.Scope, ExpectedRevision: b.Revision, Status: "suspended"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.RevalidateKnowledge(testCtx, f.work, knowledgeValidateRequest(fresh)); e == nil {
		t.Fatal("changed binding resumed")
	}
}
func TestKnowledgeRetrievalBudgetScopeAndLifecycle(t *testing.T) {
	f, in, ing, b, req := knowledgeRetrievalFixture(t)
	b = knowledgeActivate(t, f, b)
	req.MaxTokens = 256
	r, e := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if e != nil || len(r.Context.Items) != 0 {
		t.Fatalf("budget: %v", e)
	}
	req.MaxTokens = 4096
	for _, scope := range []Scope{{WorkspaceID: "other", ProjectID: in.Scope.ProjectID, UserID: in.Scope.UserID, AgentID: in.Scope.AgentID}, {WorkspaceID: in.Scope.WorkspaceID, ProjectID: "other", UserID: in.Scope.UserID, AgentID: in.Scope.AgentID}, {WorkspaceID: in.Scope.WorkspaceID, ProjectID: in.Scope.ProjectID, UserID: "other", AgentID: in.Scope.AgentID}, {WorkspaceID: in.Scope.WorkspaceID, ProjectID: in.Scope.ProjectID, UserID: in.Scope.UserID, AgentID: "other"}} {
		bad := req
		bad.Scope = scope
		if _, e = f.svc.RetrieveKnowledge(testCtx, f.work, bad); e == nil {
			t.Fatal("cross scope succeeded")
		}
	}
	noBinding := req
	noBinding.BindingIDs = nil
	if _, e = f.svc.RetrieveKnowledge(testCtx, f.work, noBinding); !errors.Is(e, ErrInvalid) {
		t.Fatalf("missing binding: %v", e)
	}
	bad := req
	bad.SkillVersion = "v2"
	if _, e = f.svc.RetrieveKnowledge(testCtx, f.work, bad); !errors.Is(e, ErrDenied) {
		t.Fatalf("unknown skill version: %v", e)
	}
	expiry := time.Now().Add(-time.Hour)
	b.Knowledge.ValidTo = &expiry
	if e = f.repo.Replace(testCtx, colKnowledgeBindings, b, b.Revision); e != nil {
		t.Fatal(e)
	}
	expired, e := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if e != nil || len(expired.Context.Items) != 0 {
		t.Fatalf("expired binding: %v", e)
	}
	anchor, e := f.repo.Get(testCtx, "sources", in.Scope, ing.Source.SourceEventID)
	if e != nil {
		t.Fatal(e)
	}
	anchor.PrivacyQuarantined = true
	if e = f.repo.Replace(testCtx, "sources", anchor, anchor.Revision); e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.RetrieveKnowledge(testCtx, f.work, req); e == nil {
		t.Fatal("quarantined source retrieved")
	}
}
