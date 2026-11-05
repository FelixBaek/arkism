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
	// Lookups by the recomputed ids work under the new name.
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
	if _, err = p.RenameSpace(testCtx, scopeA, to, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("rename onto a used name: %v", err)
	}
}
