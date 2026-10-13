//go:build linux

package memory

import (
	"errors"
	"strings"
	"testing"
)

func knowledgeFixtureInput() KnowledgeIngestInput {
	return KnowledgeIngestInput{Scope: Scope{WorkspaceID: "ws-a", ProjectID: "proj-a", UserID: "synthetic-user", AgentID: "synthetic-agent"}, SourceKey: "print", Filename: "print.md", MIMEType: "text/markdown", Content: "# Synthetic print specification\nA3 paper. Bleed 3 mm. Physical print quality is not validated.", Synthetic: true}
}
func TestKnowledgeIngestionVersionsAndIdempotency(t *testing.T) {
	f := handoffFixture(t)
	in := knowledgeFixtureInput()
	first, err := f.svc.IngestKnowledge(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Source.Status != "draft" || first.Source.Knowledge.TrustLevel != "user_supplied" || len(first.Chunks) != 1 || first.Document.Knowledge.ParserVersion == "" || first.Chunks[0].Knowledge.ChunkerVersion == "" {
		t.Fatalf("bad ingestion: %#v", first)
	}
	duplicate, err := f.svc.IngestKnowledge(testCtx, f.human, in)
	if err != nil || !duplicate.Duplicate || duplicate.Source.ID != first.Source.ID || duplicate.Document.ID != first.Document.ID {
		t.Fatalf("dedupe: %v", err)
	}
	reviewed, err := f.svc.SetKnowledgeState(testCtx, f.human, colKnowledgeSources, first.Source.ID, KnowledgeStateInput{Scope: in.Scope, ExpectedRevision: 1, Status: "active", TrustLevel: "reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	original := in.Content
	in.Content += "\nUpdated synthetic version."
	in.ExpectedRevision = reviewed.Revision
	second, err := f.svc.IngestKnowledge(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	if second.Source.ID != first.Source.ID || second.Source.SourceEventID != first.Source.SourceEventID || second.Document.ID == first.Document.ID || second.Source.Status != "draft" || second.Source.Knowledge.TrustLevel != "user_supplied" || len(second.Source.Knowledge.DocumentIDs) != 2 {
		t.Fatal("new version incorrectly replaces evidence or inherits review")
	}
	in.Content = original
	in.ExpectedRevision = second.Source.Revision
	replay, err := f.svc.IngestKnowledge(testCtx, f.human, in)
	if err != nil || !replay.Duplicate || replay.Source.Knowledge.SourceVersion != second.Source.Knowledge.SourceVersion {
		t.Fatalf("old version replay rolled back head: %v", err)
	}
	raw, err := f.svc.ReadArtifact(testCtx, f.human, in.Scope, first.Source.SourceEventID, first.Document.Knowledge.StorageRef)
	if err != nil || string(raw) != original {
		t.Fatalf("protected original: %v", err)
	}
	if _, err = f.svc.ReadArtifact(testCtx, f.agent, in.Scope, first.Source.SourceEventID, first.Document.Knowledge.StorageRef); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent obtained raw corpus: %v", err)
	}
	if _, err = f.svc.Get(testCtx, f.agent, colKnowledgeChunks, in.Scope, first.Chunks[0].ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("generic chunk bypass: %v", err)
	}
}
func TestKnowledgeIngestionValidationAndFailure(t *testing.T) {
	f := handoffFixture(t)
	base := knowledgeFixtureInput()
	for _, mutate := range []func(*KnowledgeIngestInput){func(i *KnowledgeIngestInput) { i.Filename = "../print.md" }, func(i *KnowledgeIngestInput) { i.Filename = "print.pdf"; i.MIMEType = "application/pdf" }, func(i *KnowledgeIngestInput) { i.MIMEType = "application/json" }, func(i *KnowledgeIngestInput) { i.Content = strings.Repeat("x", (64<<10)+1) }, func(i *KnowledgeIngestInput) { i.Content = "\xff" }, func(i *KnowledgeIngestInput) { i.Content = "x\x00" }} {
		in := base
		mutate(&in)
		if _, err := f.svc.IngestKnowledge(testCtx, f.human, in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid file accepted: %v", err)
		}
	}
	if _, err := f.svc.IngestKnowledge(testCtx, f.agent, base); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent ingest: %v", err)
	}
	in := base
	in.Filename = "broken.json"
	in.MIMEType = "application/json"
	in.Content = "{ broken synthetic fixture"
	got, err := f.svc.IngestKnowledge(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source.Status != "failed" || got.Document.Knowledge.ErrorCode != "extraction_failed" || len(got.Chunks) != 0 {
		t.Fatal("unsafe extraction failure")
	}
	if _, err = f.svc.SetKnowledgeState(testCtx, f.human, colKnowledgeSources, got.Source.ID, KnowledgeStateInput{Scope: in.Scope, ExpectedRevision: 1, Status: "active", TrustLevel: "reviewed"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("failed source promoted: %v", err)
	}
}
func TestKnowledgeBindingReviewAndSourceFence(t *testing.T) {
	f := handoffFixture(t)
	in := knowledgeFixtureInput()
	got, err := f.svc.IngestKnowledge(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	binding := KnowledgeBindingInput{Scope: in.Scope, BindingKey: "print", KnowledgeSourceID: got.Source.ID, SpecialistProfileID: "illustrator-expert", SpecialistProfileVersionID: "1.0.0", TrustRequirement: "reviewed", AccessMode: "synthetic_retrieval"}
	b, err := f.svc.CreateKnowledgeBinding(testCtx, f.human, binding)
	if err != nil {
		t.Fatal(err)
	}
	state := KnowledgeStateInput{Scope: in.Scope, ExpectedRevision: b.Revision, Status: "active_synthetic"}
	if _, err = f.svc.SetKnowledgeState(testCtx, f.human, colKnowledgeBindings, b.ID, state); !errors.Is(err, ErrDenied) {
		t.Fatalf("unreviewed binding enabled: %v", err)
	}
	if _, err = f.svc.SetKnowledgeState(testCtx, f.agent, colKnowledgeSources, got.Source.ID, KnowledgeStateInput{Scope: in.Scope, ExpectedRevision: 1, Status: "active", TrustLevel: "reviewed"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent review enabled: %v", err)
	}
	if _, err = f.svc.SetKnowledgeState(testCtx, f.human, colKnowledgeSources, got.Source.ID, KnowledgeStateInput{Scope: in.Scope, ExpectedRevision: 1, Status: "active", TrustLevel: "reviewed"}); err != nil {
		t.Fatal(err)
	}
	active, err := f.svc.SetKnowledgeState(testCtx, f.human, colKnowledgeBindings, b.ID, state)
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != "active" || active.Knowledge.LifecycleStatus != "active_synthetic" {
		t.Fatal("binding state mismatch")
	}
	for _, scope := range []Scope{{WorkspaceID: "different", ProjectID: in.Scope.ProjectID, UserID: in.Scope.UserID, AgentID: in.Scope.AgentID}, {WorkspaceID: in.Scope.WorkspaceID, ProjectID: "different", UserID: in.Scope.UserID, AgentID: in.Scope.AgentID}, {WorkspaceID: in.Scope.WorkspaceID, ProjectID: in.Scope.ProjectID, UserID: "different", AgentID: in.Scope.AgentID}, {WorkspaceID: in.Scope.WorkspaceID, ProjectID: in.Scope.ProjectID, UserID: in.Scope.UserID, AgentID: "different"}} {
		binding.Scope = scope
		if _, err = f.svc.CreateKnowledgeBinding(testCtx, f.human, binding); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross scope binding allowed: %v", err)
		}
	}
	anchor, err := f.repo.Get(testCtx, "sources", in.Scope, got.Source.SourceEventID)
	if err != nil {
		t.Fatal(err)
	}
	anchor.Status = "deleted"
	if err = f.repo.Replace(testCtx, "sources", anchor, anchor.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.GetKnowledgeSource(testCtx, f.human, got.Source.ID, in.Scope); err == nil {
		t.Fatal("deleted knowledge readable")
	}
	if _, err = f.svc.IngestKnowledge(testCtx, f.human, in); err == nil {
		t.Fatal("deleted knowledge resurrected by duplicate")
	}
}
