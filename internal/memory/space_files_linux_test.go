//go:build linux

package memory

import (
	"strings"
	"testing"
)

func assertBundleCarriesOnlyOwnFiles(t *testing.T, bundle string) {
	t.Helper()
	for _, want := range []string{"Bleed 3 mm", `"user_id":"synthetic-user"`, `"section":"attachments"`} {
		if !strings.Contains(bundle, want) {
			t.Fatalf("bundle misses %q:\n%s", want, bundle)
		}
	}
	for _, leak := range []string{"Someone else", "aHVtYW4yIHByaXZhdGUgZmlsZQ"} {
		if strings.Contains(bundle, leak) {
			t.Fatalf("bundle carries another person's private file %q", leak)
		}
	}
}

func TestSpaceBundleCarriesKnowledgeAndAttachments(t *testing.T) {
	f := handoffFixture(t)
	mine := knowledgeFixtureInput()
	if _, err := f.svc.IngestKnowledge(testCtx, f.human, mine); err != nil {
		t.Fatal(err)
	}
	theirs := knowledgeFixtureInput()
	theirs.SourceKey, theirs.Content = "other", "# Someone else's private notes"
	if _, err := f.svc.IngestKnowledge(testCtx, f.human2, theirs); err != nil {
		t.Fatal(err)
	}
	body := []byte("attachment body kept byte for byte\n")
	if _, err := f.svc.IngestFile(testCtx, f.human, FileIngestInput{Source: sourceInput(CreateInput{Scope: scopeA}), Artifact: FileArtifactSpec{Kind: "code", MIME: "text/plain"}}, body); err != nil {
		t.Fatal(err)
	}
	private := sourceInput(CreateInput{Scope: scopeA, Sensitivity: "private"})
	if _, err := f.svc.IngestFile(testCtx, f.human2, FileIngestInput{Source: private, Artifact: FileArtifactSpec{Kind: "code", MIME: "text/plain"}}, []byte("human2 private file")); err != nil {
		t.Fatal(err)
	}

	parts := SpaceExportInput{Parts: []string{spaceSectionKnowledge, spaceSectionAttachments}}
	bundle := exportBundle(t, f, scopeA, parts)
	assertBundleCarriesOnlyOwnFiles(t, bundle)

	// An agent's export carries no files at all.
	job, err := f.svc.ExportSpace(testCtx, f.agent, scopeA, parts)
	if err != nil {
		t.Fatal(err)
	}
	waitSpaceJob(t, f, scopeA, job.ID)
	agentBundle, err := downloadBundle(f, f.agent, scopeA, job.ID)
	if err != nil || strings.Contains(agentBundle, `"file"`) || !strings.Contains(agentBundle, `"reason":"not_human"`) {
		t.Fatalf("agent export %v:\n%s", err, agentBundle)
	}

	// Import restores both into the same sub-scopes; knowledge arrives as a draft; a second import adds no rows
	// (the knowledge file reports merged; a replayed attachment returns its first copy).
	report := importBundle(t, f, scopeB, bundle, "")
	if report.Imported != 2 {
		t.Fatalf("import report %+v", report)
	}
	sub := Scope{WorkspaceID: scopeB.WorkspaceID, ProjectID: scopeB.ProjectID, UserID: mine.Scope.UserID, AgentID: mine.Scope.AgentID}
	heads := scopeRecords(f, colKnowledgeSources, sub)
	if len(heads) != 1 || heads[0].Status != "draft" || len(scopeRecords(f, colKnowledgeChunks, sub)) == 0 {
		t.Fatalf("knowledge not restored as draft: %+v", heads)
	}
	if arts := scopeRecords(f, colArtifacts, scopeB); len(arts) != 1 || arts[0].Artifact.Digest != ContentHash(string(body)) {
		t.Fatalf("attachment not restored byte for byte: %+v", arts)
	}
	if again := importBundle(t, f, scopeB, bundle, ""); again.Merged != 1 || len(scopeRecords(f, colKnowledgeSources, sub)) != 1 || len(scopeRecords(f, colArtifacts, scopeB)) != 1 {
		t.Fatalf("second import duplicated files %+v", again)
	}

	// Clone goes through the same path.
	scopeC := Scope{WorkspaceID: scopeA.WorkspaceID, ProjectID: "proj-c"}
	clone, err := f.svc.CloneSpace(testCtx, f.human, scopeA, SpaceCloneInput{Target: scopeC, Parts: parts.Parts})
	if err != nil {
		t.Fatal(err)
	}
	waitSpaceJob(t, f, scopeA, clone.ID)
	subC := Scope{WorkspaceID: scopeC.WorkspaceID, ProjectID: scopeC.ProjectID, UserID: sub.UserID, AgentID: sub.AgentID}
	if len(scopeRecords(f, colKnowledgeSources, subC)) != 1 || len(scopeRecords(f, colArtifacts, scopeC)) != 1 {
		t.Fatal("clone did not carry knowledge and attachments")
	}
}
