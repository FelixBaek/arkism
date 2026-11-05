//go:build linux

package memory

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func wikiMemoryAnswers(cited string) []string {
	return []string{
		`{"summary":"print edges","pages":[{"title":"Bleed","type":"concept","action":"create","reason":"term"},{"title":"Trim","type":"concept","action":"create","reason":"term"},{"title":"Edge","type":"concept","action":"create","reason":"term"}],"links":[]}`,
		wikiFile("wiki/concept/bleed.md", "Bleed", "concept", "Bleed is the 3 mm edge outside the trim [mem:"+cited+"].") +
			wikiFile("wiki/concept/trim.md", "Trim", "concept", "Trim is where the sheet is cut [mem:not-a-recalled-id].") +
			wikiFile("wiki/concept/edge.md", "Edge", "concept", "An edge with no citation."),
	}
}

// wikiMemoryFixture: the job runner recalls memories with its own search permission.
func wikiMemoryFixture(t *testing.T) (*fixture, *fakeWikiLLM) {
	f, llm := wikiFixture(t)
	f.auth.actors[f.work].verbs["search"] = true
	f.auth.actors[f.work].verbs["context"] = true
	return f, llm
}

func TestWikiFromMemoriesCitationsDraftsAndReview(t *testing.T) {
	f, llm := wikiMemoryFixture(t)
	m := f.activeMemory("Bleed is the 3 mm edge outside the trim", "bleed")
	llm.answers = append(wikiMemoryAnswers(m.ID), wikiMemoryAnswers(m.ID)...)
	scope := m.Scope

	// dry_run: drafts come back, nothing is stored, bad citations are dropped.
	dry, err := f.svc.BuildWikiFromMemories(testCtx, f.human, WikiMemoryBuildInput{Scope: scope, Query: "bleed", DryRun: true})
	if err != nil || !dry.DryRun || len(dry.Drafts) != 1 || dry.Drafts[0].Slug != "bleed" || !slices.Equal(dry.Drafts[0].Citations, []string{m.ID}) {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	reasons := []string{}
	for _, r := range dry.Result.Rejected {
		reasons = append(reasons, r.Reason)
	}
	if !slices.Contains(reasons, "unknown_citation") || !slices.Contains(reasons, "no_citation") {
		t.Fatalf("rejections: %v", reasons)
	}
	if !strings.Contains(llm.prompts[0], "[mem:"+m.ID+"]") || !strings.Contains(llm.prompts[1], wikiCitationRule) {
		t.Fatalf("memory prompt: %v", llm.prompts)
	}
	if list, _ := f.svc.ListWikiPages(testCtx, f.human, scope, "all"); len(list.Items) != 0 {
		t.Fatalf("dry run stored pages: %+v", list.Items)
	}

	// Queued job: the page is a draft no agent can read until a person approves.
	queued, err := f.svc.BuildWikiFromMemories(testCtx, f.human, WikiMemoryBuildInput{Scope: scope, Query: "bleed"})
	if err != nil || queued.Job == nil || queued.Job.Status != JobStatusPending {
		t.Fatalf("queue: %+v %v", queued, err)
	}
	ran, err := f.svc.RunWikiJob(testCtx, f.work, scope, queued.Job.ID)
	bleed := wikiPageID(scope, "bleed")
	if err != nil || ran.Status != JobStatusDone || !slices.Equal(ran.Job.Result.Created, []string{bleed}) || !slices.Equal(ran.Job.Memory.MemoryIDs, []string{m.ID}) {
		t.Fatalf("run: %+v %v", ran, err)
	}
	page, err := f.svc.GetWikiPage(testCtx, f.human, scope, bleed)
	if err != nil || page.Status != "draft" || !slices.Equal(page.MemoryIDs, []string{m.ID}) || len(page.SourceIDs) != 0 {
		t.Fatalf("draft: %+v %v", page, err)
	}
	if _, err = f.svc.GetWikiPage(testCtx, f.agent, scope, bleed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("agent read a draft: %v", err)
	}
	wikiApprove(t, f, scope, bleed)
	got, err := f.svc.ReadWiki(testCtx, f.agent, WikiSearchInput{Scope: scope, Query: "bleed"})
	if err != nil || len(got.Items) != 1 || got.Items[0].ID != bleed {
		t.Fatalf("reviewed page not served: %+v %v", got, err)
	}

	// The page stops being served once a cited memory is no longer live.
	f.transition(f.human, "memories", m.ID, ChangeInput{Scope: scope, ExpectedRevision: m.Revision, Status: "expired"})
	if _, err = f.svc.GetWikiPage(testCtx, f.agent, scope, bleed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("page with an expired citation served: %v", err)
	}
}

func TestWikiFromMemoriesRespectsLocksAndInput(t *testing.T) {
	f, llm := wikiMemoryFixture(t)
	m := f.activeMemory("Bleed is the 3 mm edge outside the trim", "bleed")
	scope := m.Scope
	llm.answers = wikiMemoryAnswers(m.ID)
	queued, err := f.svc.BuildWikiFromMemories(testCtx, f.human, WikiMemoryBuildInput{Scope: scope, Topic: "bleed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.RunWikiJob(testCtx, f.work, scope, queued.Job.ID); err != nil {
		t.Fatal(err)
	}
	bleed := wikiPageID(scope, "bleed")
	page := wikiApprove(t, f, scope, bleed)
	if _, err = f.svc.LockWikiPage(testCtx, f.human, bleed, WikiLockInput{Scope: scope, ExpectedRevision: page.Revision, Locked: true}); err != nil {
		t.Fatal(err)
	}
	locked, _ := f.svc.GetWikiPage(testCtx, f.human, scope, bleed)

	// A locked page is skipped: not sent in a write prompt, not changed.
	llm.answers = append(llm.answers, `{"summary":"x","pages":[{"title":"Bleed","type":"concept","action":"update","reason":"more"}],"links":[]}`)
	calls := len(llm.prompts)
	dry, err := f.svc.BuildWikiFromMemories(testCtx, f.human, WikiMemoryBuildInput{Scope: scope, EntityName: "Bleed", DryRun: true})
	if err != nil || !slices.Equal(dry.Result.SkippedLocked, []string{bleed}) || len(dry.Drafts) != 0 || len(llm.prompts) != calls+1 {
		t.Fatalf("locked page planned: %+v %v", dry, err)
	}
	if after, _ := f.svc.GetWikiPage(testCtx, f.human, scope, bleed); after.Revision != locked.Revision {
		t.Fatalf("locked page changed: %+v", after)
	}

	for name, in := range map[string]WikiMemoryBuildInput{
		"none": {Scope: scope},
		"two":  {Scope: scope, Query: "a", Topic: "b"},
		"long": {Scope: scope, Query: strings.Repeat("x", wikiMemoryMaxQuery+1)},
	} {
		if _, err = f.svc.BuildWikiFromMemories(testCtx, f.human, in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err = f.svc.BuildWikiFromMemories(testCtx, f.agent, WikiMemoryBuildInput{Scope: scope, Query: "bleed"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent build: %v", err)
	}
	if _, err = f.svc.BuildWikiFromMemories(testCtx, f.human, WikiMemoryBuildInput{Scope: scope, Query: "nothing matches this"}); err != nil {
		t.Fatalf("queue with no matches yet: %v", err)
	}
	f.svc.Wiki = nil
	if _, err = f.svc.BuildWikiFromMemories(testCtx, f.human, WikiMemoryBuildInput{Scope: scope, Query: "bleed", DryRun: true}); !errors.Is(err, ErrModelNotConfigured) {
		t.Fatalf("no model: %v", err)
	}
	if code, status := errorResponse(ErrModelNotConfigured); code != "model_not_configured" || status != 503 {
		t.Fatalf("error code: %s %d", code, status)
	}
}

// wikiPrintReviewed builds and approves the two print pages (bleed links to a3-paper).
func wikiPrintReviewed(t *testing.T) (*fixture, Scope) {
	f, _ := wikiFixture(t, wikiPrintAnswers()...)
	src := wikiSource(t, f, "print", wikiPrintSource)
	wikiBuild(t, f, src)
	wikiApprove(t, f, src.Scope, wikiPageID(src.Scope, "bleed"))
	wikiApprove(t, f, src.Scope, wikiPageID(src.Scope, "a3-paper"))
	return f, src.Scope
}

func TestWikiTreeFoldersMoveAndRename(t *testing.T) {
	f, scope := wikiPrintReviewed(t)
	bleed, a3 := wikiPageID(scope, "bleed"), wikiPageID(scope, "a3-paper")
	for _, bad := range []string{"", "../x", "a//b", "a/./b", "a/b?", strings.Repeat("a/", 9)} {
		if _, err := f.svc.CreateWikiFolder(testCtx, f.human, WikiFolderInput{Scope: scope, Path: bad}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("folder %q: %v", bad, err)
		}
	}
	if _, err := f.svc.CreateWikiFolder(testCtx, f.agent, WikiFolderInput{Scope: scope, Path: "docs"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent folder: %v", err)
	}
	for range 2 { // idempotent
		if got, err := f.svc.CreateWikiFolder(testCtx, f.human, WikiFolderInput{Scope: scope, Path: "/archive/old/"}); err != nil || got.Path != "archive/old" {
			t.Fatalf("folder: %+v %v", got, err)
		}
	}
	page, _ := f.svc.GetWikiPage(testCtx, f.human, scope, bleed)
	folder := "docs/print"
	moved, err := f.svc.PatchWikiPage(testCtx, f.human, bleed, WikiPagePatchInput{Scope: scope, ExpectedRevision: page.Revision, Folder: &folder})
	if err != nil || moved.Folder != folder || moved.Reviewed.Text != page.Reviewed.Text {
		t.Fatalf("move: %+v %v", moved, err)
	}
	if _, err = f.svc.PatchWikiPage(testCtx, f.human, bleed, WikiPagePatchInput{Scope: scope, ExpectedRevision: page.Revision, Folder: &folder}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale move: %v", err)
	}
	tree, err := f.svc.WikiTree(testCtx, f.agent, scope)
	paths := []string{}
	for _, fo := range tree.Folders {
		paths = append(paths, fo.Path)
	}
	if err != nil || !slices.Equal(paths, []string{"archive", "archive/old", "docs", "docs/print"}) || len(tree.Pages) != 2 || tree.Pages[1].Folder != folder || tree.Folders[3].Pages != 1 {
		t.Fatalf("tree: %+v %v", tree, err)
	}

	// a3-paper has an incoming reviewed link, so a slug change is refused.
	a3page, _ := f.svc.GetWikiPage(testCtx, f.human, scope, a3)
	if _, err = f.svc.PatchWikiPage(testCtx, f.human, a3, WikiPagePatchInput{Scope: scope, ExpectedRevision: a3page.Revision, Title: "Sheet"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("rename of a linked page: %v", err)
	}
	renamed, err := f.svc.PatchWikiPage(testCtx, f.human, bleed, WikiPagePatchInput{Scope: scope, ExpectedRevision: moved.Revision, Title: "Bleed Margin"})
	newID := wikiPageID(scope, "bleed-margin")
	if err != nil || renamed.ID != newID || renamed.Slug != "bleed-margin" || renamed.Reviewed.Title != "Bleed Margin" || renamed.Folder != folder {
		t.Fatalf("rename: %+v %v", renamed, err)
	}
	if _, err = f.svc.GetWikiPage(testCtx, f.human, scope, bleed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old id kept: %v", err)
	}
	links, err := f.svc.WikiGraph(testCtx, f.agent, scope)
	if err != nil || len(links) != 1 || links[0].FromID != newID || links[0].ToID != a3 {
		t.Fatalf("links after rename: %+v %v", links, err)
	}
}

func TestWikiExportArchives(t *testing.T) {
	f, scope := wikiPrintReviewed(t)
	bleed := wikiPageID(scope, "bleed")
	page, _ := f.svc.GetWikiPage(testCtx, f.human, scope, bleed)
	folder := "print"
	if _, err := f.svc.PatchWikiPage(testCtx, f.human, bleed, WikiPagePatchInput{Scope: scope, ExpectedRevision: page.Revision, Folder: &folder}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ExportWiki(testCtx, f.agent, scope, "rar"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad format: %v", err)
	}
	out, err := f.svc.ExportWiki(testCtx, f.agent, scope, "")
	if err != nil || out.ContentType != "application/x-tar" {
		t.Fatalf("tar: %+v %v", out.ContentType, err)
	}
	files := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(out.Data))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		files[h.Name] = string(body)
	}
	got := files["print/bleed.md"]
	if len(files) != 2 || files["a3-paper.md"] == "" || !strings.HasPrefix(got, "---\ntitle: \"Bleed\"\n") || !strings.Contains(got, "citations: []") || !strings.Contains(got, "updated_at: ") || !strings.Contains(got, "\n---\n\nBleed is a 3 mm edge") {
		t.Fatalf("tar files: %v", files)
	}
	out, err = f.svc.ExportWiki(testCtx, f.human, scope, "zip")
	if err != nil || out.ContentType != "application/zip" {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(out.Data), int64(len(out.Data)))
	if err != nil || len(zr.File) != 2 || zr.File[1].Name != "print/bleed.md" {
		t.Fatalf("zip: %v %v", zr, err)
	}
}

func TestWikiExportSkipsDrafts(t *testing.T) {
	f, _ := wikiFixture(t, wikiPrintAnswers()...)
	src := wikiSource(t, f, "print", wikiPrintSource)
	wikiBuild(t, f, src)
	out, err := f.svc.ExportWiki(testCtx, f.human, src.Scope, "tar")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tar.NewReader(bytes.NewReader(out.Data)).Next(); err != io.EOF {
		t.Fatalf("draft exported: %v", err)
	}
}

func TestWikiSearchVectorLaneAndFallback(t *testing.T) {
	f, scope := wikiPrintReviewed(t)
	f.emb.semantic = true
	query := WikiSearchInput{Scope: scope, Query: "bleed"}
	before, err := f.svc.SearchWiki(testCtx, f.agent, query)
	if err != nil || len(before.Items) == 0 || before.Items[0].Reason != "text_match" {
		t.Fatalf("not embedded yet: %+v %v", before, err)
	}
	// Approval queued the pages on the shared embedding queue; drain it.
	for range 20 {
		if err = f.svc.WorkOnce(testCtx, f.work); errors.Is(err, ErrNotFound) {
			break
		}
	}
	bleed, _ := f.repo.Get(testCtx, colWikiPages, scope, wikiPageID(scope, "bleed"))
	if bleed.Embedding == nil || bleed.Embedding.ContentHash != ContentHash(recordContent(bleed)) {
		t.Fatalf("page not embedded: %+v", bleed.Embedding)
	}
	hybrid, err := f.svc.SearchWiki(testCtx, f.agent, query)
	if err != nil || len(hybrid.Items) == 0 || hybrid.Items[0].Slug != "bleed" || !strings.Contains(hybrid.Items[0].Reason, "semantic_match") {
		t.Fatalf("hybrid: %+v %v", hybrid, err)
	}
	// Without an embedder the ranking is BM25 only, as before.
	f.svc.Embedder = nil
	plain, err := f.svc.SearchWiki(testCtx, f.agent, query)
	if err != nil || len(plain.Items) != len(before.Items) || plain.Items[0].ID != before.Items[0].ID || plain.Items[0].Score != before.Items[0].Score || plain.Items[0].Reason != "text_match" {
		t.Fatalf("fallback: %+v vs %+v %v", plain, before, err)
	}
}

// Each recalled memory is a separate external transfer: a restricted memory,
// one derived from a restricted source and an unclassified one never reach the
// model even though the fenced prompt as a whole looks safe.
func TestWikiFromMemoriesTransferIsCheckedPerMemory(t *testing.T) {
	f, llm := wikiMemoryFixture(t)
	ok := f.activeMemory("Bleed allowed fact", "bleed")
	restricted := f.activeMemory("Bleed restricted fact", "bleed")
	derived := f.activeMemory("Bleed derived fact", "bleed")
	unclassified := f.activeMemory("Bleed unclassified fact", "bleed")
	set := func(col string, r Record, sensitivity string) {
		r.Sensitivity = sensitivity
		f.repo.store[col][recKey(r.Scope, r.ID)] = r
	}
	set("memories", restricted, "restricted")
	set("memories", unclassified, "")
	// Same-text fixture sources share one id; give the derived memory its own.
	src := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "a restricted original"}))
	set("sources", src, "restricted")
	derived.Sources = refs(src)
	set("memories", derived, derived.Sensitivity)
	llm.answers = wikiMemoryAnswers(ok.ID)
	dry, err := f.svc.BuildWikiFromMemories(testCtx, f.human, WikiMemoryBuildInput{Scope: ok.Scope, Query: "bleed", DryRun: true})
	if err != nil || !slices.Equal(dry.MemoryIDs, []string{ok.ID}) {
		t.Fatalf("recalled: %+v %v", dry.MemoryIDs, err)
	}
	for _, p := range llm.prompts {
		for _, bad := range []string{"restricted fact", "derived fact", "unclassified fact"} {
			if strings.Contains(p, bad) {
				t.Fatalf("%q sent to the model", bad)
			}
		}
	}

	// A runner the policy does not let send content out recalls nothing.
	who := f.auth.actors[f.human]
	who.external = false
	f.auth.actors[f.human] = who
	if _, err = f.svc.BuildWikiFromMemories(testCtx, f.human, WikiMemoryBuildInput{Scope: ok.Scope, Query: "bleed", DryRun: true}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no transfer permission: %v", err)
	}
}

// Past the row window the tree says it is truncated, and a slug rename is
// refused because an inbound link could be outside the window.
func TestWikiTreeAndRenameAtTheRowCap(t *testing.T) {
	f, scope := wikiPrintReviewed(t)
	bleed := wikiPageID(scope, "bleed")
	tree, err := f.svc.WikiTree(testCtx, f.human, scope)
	if err != nil || tree.Truncated {
		t.Fatalf("small tree: %+v %v", tree.Truncated, err)
	}
	for i := range wikiListLimit + 1 {
		id := fmt.Sprintf("filler-%d", i)
		filler := Record{ID: id, Scope: scope, Kind: "wiki_link", Status: "active", Revision: 1,
			Wiki: &WikiRecord{Link: &WikiLink{FromID: id, ToID: id + "-to"}}}
		if err = f.repo.Insert(testCtx, colWikiLinks, filler); err != nil {
			t.Fatal(err)
		}
		filler.Kind, filler.Wiki = "wiki_folder", &WikiRecord{Folder: &WikiFolder{Path: id}}
		if err = f.repo.Insert(testCtx, colWikiPages, filler); err != nil {
			t.Fatal(err)
		}
	}
	if tree, err = f.svc.WikiTree(testCtx, f.human, scope); err != nil || !tree.Truncated {
		t.Fatalf("full tree not marked: %v", err)
	}
	page, _ := f.svc.GetWikiPage(testCtx, f.human, scope, bleed)
	if _, err = f.svc.PatchWikiPage(testCtx, f.human, bleed, WikiPagePatchInput{Scope: scope, ExpectedRevision: page.Revision, Title: "Bleed Margin"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("rename past the link window: %v", err)
	}
}

func TestWikiFolderMoveDeleteAndQueryPages(t *testing.T) {
	f, scope := wikiPrintReviewed(t)
	bleed, a3 := wikiPageID(scope, "bleed"), wikiPageID(scope, "a3-paper")
	for _, id := range []string{bleed, a3} {
		page, _ := f.svc.GetWikiPage(testCtx, f.human, scope, id)
		folder := "docs/print"
		if _, err := f.svc.PatchWikiPage(testCtx, f.human, id, WikiPagePatchInput{Scope: scope, ExpectedRevision: page.Revision, Folder: &folder}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.CreateWikiFolder(testCtx, f.human, WikiFolderInput{Scope: scope, Path: "docs/print/extra"}); err != nil {
		t.Fatal(err)
	}
	docs, root := "docs", ""
	mm, err := f.svc.CreateMentalModel(testCtx, f.human, MentalModelInput{Scope: scope, Title: "Print rules", Query: "print rules", Refresh: MentalModelRefresh{Mode: "manual"}, Folder: &docs})
	if err != nil || mm.Folder == nil || *mm.Folder != "docs" {
		t.Fatal("query page", err, mm)
	}
	loose, err := f.svc.CreateMentalModel(testCtx, f.human, MentalModelInput{Scope: scope, Title: "Loose", Query: "loose", Refresh: MentalModelRefresh{Mode: "manual"}, Folder: &root})
	if err != nil {
		t.Fatal(err)
	}
	tree, err := f.svc.WikiTree(testCtx, f.human, scope)
	if err != nil || len(tree.Pages) != 4 || !slices.ContainsFunc(tree.Pages, func(p WikiTreePage) bool { return p.ID == mm.ID && p.Type == "query" && p.Folder == "docs" }) {
		t.Fatalf("tree with query pages %+v %v", tree.Pages, err)
	}
	if agentTree, _ := f.svc.WikiTree(testCtx, f.agent, scope); slices.ContainsFunc(agentTree.Pages, func(p WikiTreePage) bool { return p.Type == "query" }) {
		t.Fatal("agent saw a query page with no approved text")
	}

	for _, bad := range []WikiFolderMoveInput{{From: "docs", To: "docs/inner"}, {From: "docs", To: "docs"}, {From: "", To: "x"}, {From: "docs", To: strings.Repeat("a/", 7) + "b"}} {
		bad.Scope = scope
		if _, err = f.svc.MoveWikiFolder(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("move %+v: %v", bad, err)
		}
	}
	if _, err = f.svc.MoveWikiFolder(testCtx, f.agent, WikiFolderMoveInput{Scope: scope, From: "docs", To: "manual"}); !errors.Is(err, ErrDenied) {
		t.Fatal("agent moved a folder", err)
	}
	moved, err := f.svc.MoveWikiFolder(testCtx, f.human, WikiFolderMoveInput{Scope: scope, From: "docs", To: "manual/docs"})
	if err != nil || len(moved.Moved) != 3 {
		t.Fatal("move", err, moved)
	}
	tree, _ = f.svc.WikiTree(testCtx, f.human, scope)
	paths := []string{}
	for _, fo := range tree.Folders {
		paths = append(paths, fo.Path)
	}
	if !slices.Equal(paths, []string{"manual", "manual/docs", "manual/docs/print", "manual/docs/print/extra"}) {
		t.Fatal("folders after move", paths)
	}
	if links, _ := f.svc.WikiGraph(testCtx, f.agent, scope); len(links) != 1 {
		t.Fatal("move broke links", links)
	}

	if _, err = f.svc.DeleteWikiFolder(testCtx, f.human, WikiFolderDeleteInput{Scope: scope, Path: "manual/docs"}); !errors.Is(err, ErrConflict) {
		t.Fatal("non-empty folder deleted without recursive", err)
	}
	if _, err = f.svc.DeleteWikiFolder(testCtx, f.human, WikiFolderDeleteInput{Scope: scope, Path: "manual/docs/print/extra"}); err != nil {
		t.Fatal("empty folder delete", err)
	}

	a3page, _ := f.svc.GetWikiPage(testCtx, f.human, scope, a3)
	if _, err = f.svc.DeleteWikiPage(testCtx, f.agent, a3, WikiPageDeleteInput{Scope: scope, ExpectedRevision: a3page.Revision}); !errors.Is(err, ErrDenied) {
		t.Fatal("agent deleted a page", err)
	}
	if _, err = f.svc.DeleteWikiPage(testCtx, f.human, a3, WikiPageDeleteInput{Scope: scope, ExpectedRevision: a3page.Revision - 1}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale delete", err)
	}
	gone, err := f.svc.DeleteWikiPage(testCtx, f.human, a3, WikiPageDeleteInput{Scope: scope, ExpectedRevision: a3page.Revision})
	if err != nil || !slices.Equal(gone.Deleted, []string{a3}) || !slices.Equal(gone.Unlinked, []string{bleed}) {
		t.Fatal("page delete", err, gone)
	}
	if links, _ := f.svc.WikiGraph(testCtx, f.agent, scope); len(links) != 0 {
		t.Fatal("link to deleted page kept", links)
	}
	if _, err = f.svc.DeleteWikiPage(testCtx, f.human, loose.ID, WikiPageDeleteInput{Scope: scope, ExpectedRevision: loose.Revision}); err != nil {
		t.Fatal("query page delete", err)
	}
	all, err := f.svc.DeleteWikiFolder(testCtx, f.human, WikiFolderDeleteInput{Scope: scope, Path: "manual", Recursive: true})
	if err != nil || len(all.Deleted) != 2 {
		t.Fatal("recursive delete", err, all)
	}
	if tree, _ = f.svc.WikiTree(testCtx, f.human, scope); len(tree.Pages) != 0 || len(tree.Folders) != 0 {
		t.Fatalf("tree after recursive delete %+v", tree)
	}
}

func TestWikiFolderAndPageDeleteHTTP(t *testing.T) {
	f, scope := wikiPrintReviewed(t)
	h := Handler(f.svc)
	do := func(method, path, token, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	q := "workspace_id=" + scope.WorkspaceID + "&project_id=" + scope.ProjectID + "&user_id=" + scope.UserID + "&agent_id=" + scope.AgentID
	bleed := wikiPageID(scope, "bleed")
	page, _ := f.svc.GetWikiPage(testCtx, f.human, scope, bleed)
	folder := "docs"
	if _, err := f.svc.PatchWikiPage(testCtx, f.human, bleed, WikiPagePatchInput{Scope: scope, ExpectedRevision: page.Revision, Folder: &folder}); err != nil {
		t.Fatal(err)
	}
	if code := do("PATCH", "/v1/wiki/folders", f.human, `{"scope":`+structuredText(scope)+`,"from":"docs","to":"guide"}`); code != 200 {
		t.Fatal("move folder", code)
	}
	if code := do("DELETE", "/v1/wiki/folders?"+q+"&path=guide&recursive=maybe", f.human, ""); code != 400 {
		t.Fatal("bad recursive flag", code)
	}
	if code := do("DELETE", "/v1/wiki/folders?"+q+"&path=guide", f.human, ""); code != 409 {
		t.Fatal("non-empty folder", code)
	}
	if code := do("DELETE", "/v1/wiki/pages/"+bleed+"?"+q+"&expected_revision=x", f.human, ""); code != 400 {
		t.Fatal("bad revision", code)
	}
	if code := do("DELETE", "/v1/wiki/folders?"+q+"&path=guide&recursive=true", f.agent, ""); code != 403 {
		t.Fatal("agent delete", code)
	}
	if code := do("DELETE", "/v1/wiki/folders?"+q+"&path=guide&recursive=true", f.human, ""); code != 200 {
		t.Fatal("recursive delete", code)
	}
}
