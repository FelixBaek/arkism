//go:build linux

package memory

import (
	"errors"
	"testing"
)

// A rename moves every row of a project, recomputes the ids derived from its
// scope, and leaves the old name empty; a preview counts the same and changes nothing.
func TestPostgresSpaceRenameMovesEveryRow(t *testing.T) {
	f, _ := wikiFixture(t, wikiPrintAnswers()...)
	p := isolatedPostgres(t)
	f.svc.Repo = p
	mem := f.activeMemory("prefer tabs over spaces", "style")
	src := wikiSource(t, f, "print", wikiPrintSource)
	wikiBuild(t, f, src)
	sub := src.Scope
	bleed := wikiApprove(t, f, sub, wikiPageID(sub, "bleed"))
	if _, err := f.svc.CreateWikiFolder(testCtx, f.human, WikiFolderInput{Scope: sub, Path: "archive"}); err != nil {
		t.Fatal(err)
	}
	settings := Record{ID: providersID(scopeA), Scope: scopeA, Kind: "providers", Status: statusActive, Revision: 1, Text: "settings of " + mem.ID}
	if err := p.Insert(testCtx, colProviders, settings); err != nil {
		t.Fatal(err)
	}
	// A graph relation whose evidence names a remapped id, and a privacy request whose id and hash carry the scope.
	if _, err := p.q().Exec(testCtx, `INSERT INTO entities (organization_id,project_id,kind,external_id) VALUES ($1,$2,'note','a'),($1,$2,'note','b')`, scopeA.WorkspaceID, scopeA.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.q().Exec(testCtx, `INSERT INTO relations (organization_id,project_id,source_kind,source_id,target_kind,target_id,relation_kind,metadata) VALUES ($1,$2,'note','a','note','b','cites',jsonb_build_object('evidence',jsonb_build_object('id',$3::text)))`, scopeA.WorkspaceID, scopeA.ProjectID, bleed.ID); err != nil {
		t.Fatal(err)
	}
	req := PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "k1", TargetType: "source_event", TargetIDs: []string{"src-1"}, Reason: "asked", RetentionPolicyRef: retentionExternal}
	privacy := Record{ID: privacyJobID(scopeA, "human-1", "k1"), Scope: scopeA, Kind: privacyDeletionKind, Status: JobStatusDone, Revision: 1, PrivacyDeletion: &PrivacyDeletion{State: privacyCompleted, RequesterType: "human", RequesterID: "human-1", IdempotencyKey: "k1", TargetType: req.TargetType, TargetIDs: req.TargetIDs, Reason: req.Reason, RetentionPolicyRef: req.RetentionPolicyRef, LegalHoldStatus: privacyHoldNone, RequestHash: ContentHash(structuredText(req))}}
	if err := p.Insert(testCtx, colJobs, privacy); err != nil {
		t.Fatal(err)
	}
	named := "before rename"
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{ExpectedRevision: -1, DisplayName: &named}, false); err != nil {
		t.Fatal(err)
	}
	to := Scope{WorkspaceID: scopeA.WorkspaceID, ProjectID: "renamed"}
	toSub := Scope{WorkspaceID: to.WorkspaceID, ProjectID: to.ProjectID, UserID: sub.UserID, AgentID: sub.AgentID}

	if _, err := p.RenameSpace(testCtx, scopeA, Scope{WorkspaceID: "ws-other", ProjectID: "proj-a"}, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-workspace rename: %v", err)
	}
	preview, err := p.RenameSpace(testCtx, scopeA, to, false)
	if err != nil || preview.Executed || preview.Records[colMemories] != 1 || preview.Records[colWikiPages] < 2 || preview.IDsRemapped < 3 {
		t.Fatalf("preview %+v %v", preview, err)
	}
	if _, err = p.Get(testCtx, colMemories, scopeA, mem.ID); err != nil {
		t.Fatalf("preview moved a row: %v", err)
	}
	done, err := p.RenameSpace(testCtx, scopeA, to, true)
	if err != nil || !done.Executed || done.Records[colMemories] != preview.Records[colMemories] || done.IDsRemapped != preview.IDsRemapped {
		t.Fatalf("execute %+v %v (preview %+v)", done, err, preview)
	}
	if rows, _ := p.ListRecords(testCtx, scopeA, colMemories, 10); len(rows) != 0 {
		t.Fatalf("old name still holds %d memories", len(rows))
	}
	if got, err := p.Get(testCtx, colMemories, to, mem.ID); err != nil || got.Scope.ProjectID != to.ProjectID {
		t.Fatalf("memory under new name: %+v %v", got.Scope, err)
	}
	assertRenamedLookups(t, f, p, to, toSub, bleed, settings, req)
	if _, err = p.RenameSpace(testCtx, scopeA, to, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("rename onto a used name: %v", err)
	}
}

// assertRenamedLookups checks lookups by the recomputed ids work under the new name.
func assertRenamedLookups(t *testing.T, f *fixture, p *Postgres, to, toSub Scope, bleed WikiPageView, settings Record, req PrivacyDeletionRequest) {
	t.Helper()
	if _, err := p.Get(testCtx, colSpaceConfigs, to, scopedSingletonID(spaceConfigID, to)); err != nil {
		t.Fatalf("space settings under new name: %v", err)
	}
	page, err := f.svc.GetWikiPage(testCtx, f.human, toSub, wikiPageID(toSub, "bleed"))
	if err != nil || page.Revision != bleed.Revision {
		t.Fatalf("page under new name: %+v %v", page, err)
	}
	if _, err = p.Get(testCtx, colWikiPages, toSub, wikiFolderID(toSub, "archive")); err != nil {
		t.Fatalf("folder under new name: %v", err)
	}
	if got, err := p.Get(testCtx, colProviders, to, providersID(to)); err != nil || got.Text != settings.Text {
		t.Fatalf("settings under new name: %+v %v", got, err)
	}
	var evidence string
	if err = p.q().QueryRow(testCtx, `SELECT metadata->'evidence'->>'id' FROM relations WHERE organization_id=$1 AND project_id=$2 AND relation_kind='cites'`, to.WorkspaceID, to.ProjectID).Scan(&evidence); err != nil || evidence != wikiPageID(toSub, "bleed") {
		t.Fatalf("relation evidence %q %v", evidence, err)
	}
	req.Scope = to
	if got, err := p.Get(testCtx, colJobs, to, privacyJobID(to, "human-1", "k1")); err != nil || got.PrivacyDeletion.RequestHash != ContentHash(structuredText(req)) {
		t.Fatalf("privacy request under new name: %+v %v", got.PrivacyDeletion, err)
	}
}
