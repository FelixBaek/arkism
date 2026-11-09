//go:build linux

package memory

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Agents may propose directives, mental models, invalidations and file candidates and
// manage their own jobs; nothing they do makes anything active.

func TestAgentDirectiveProposalAppliesOnlyAfterAPersonSaves(t *testing.T) {
	f := newFixture(t)
	p, err := f.svc.CreateDirective(testCtx, f.agent, directiveInput("Units", "Use metric units.", "both", 5))
	if err != nil || p.Active || !p.Proposed || p.CreatedBy != "agent-1" {
		t.Fatal(p, err)
	}
	for _, target := range []string{"reflect", "context"} {
		if rules, err := f.svc.directives(testCtx, scopeA, target); err != nil || len(rules) != 0 {
			t.Fatal("a proposal applied", target, rules, err)
		}
	}
	if why := spaceRecordSkip(colDirectives, Record{Status: obsProposed, Text: "Use metric units.", Directive: &Directive{Title: "Units"}}, p.CreatedAt); why != "inactive" {
		t.Fatal("a proposal would be exported as a definition:", why)
	}
	upd := directiveInput("Units", "Use metric units.", "both", 5)
	upd.ExpectedRevision = 1
	if _, err = f.svc.UpdateDirective(testCtx, f.agent, p.ID, upd); !errors.Is(err, ErrDenied) {
		t.Fatal("agent approved its own directive", err)
	}
	got, err := f.svc.UpdateDirective(testCtx, f.human, p.ID, upd)
	if err != nil || !got.Active || got.Proposed {
		t.Fatal("a person's save approves", got, err)
	}
	for i := 1; i < directiveProposedMax; i++ {
		if _, err = f.svc.CreateDirective(testCtx, f.agent, directiveInput("t", "rule", "both", 1)); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err = f.svc.CreateDirective(testCtx, f.agent, directiveInput("t", "rule", "both", 1)); err != nil {
		t.Fatal("cap counts only open proposals", err)
	}
	if _, err = f.svc.CreateDirective(testCtx, f.agent, directiveInput("t", "rule", "both", 1)); !errors.Is(err, ErrConflict) {
		t.Fatal("proposal cap", err)
	}
	if _, err = f.svc.CreateDirective(testCtx, f.human, directiveInput("t", "rule", "both", 1)); err != nil {
		t.Fatal("people are not held by the proposal cap", err)
	}
}

func TestAgentMentalModelProposalIsNeverRefreshedOrRead(t *testing.T) {
	f := newFixture(t)
	f.svc.Wiki = &fakeWikiLLM{}
	in := MentalModelInput{Scope: scopeA, Title: "Deploys", Query: "deploy order", Refresh: MentalModelRefresh{Mode: mmRefreshDaily}}
	p, err := f.svc.CreateMentalModel(testCtx, f.agent, in)
	if err != nil || p.Status != obsProposed || p.Active != nil {
		t.Fatal(p, err)
	}
	if page, err := f.svc.ListMentalModels(testCtx, f.agent, scopeA, "", "", 0); err != nil || len(page.Items) != 0 {
		t.Fatal("a proposal listed as active", page, err)
	}
	if page, err := f.svc.ListMentalModels(testCtx, f.human, scopeA, obsProposed, "", 0); err != nil || len(page.Items) != 1 {
		t.Fatal("proposals list", page, err)
	}
	if _, err = f.svc.ListMentalModels(testCtx, f.human, scopeA, "rejected", "", 0); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown status", err)
	}
	if _, err = f.svc.RefreshMentalModel(testCtx, f.human, p.ID, MentalModelRefreshInput{Scope: scopeA}); !errors.Is(err, ErrConflict) {
		t.Fatal("refresh of a proposal", err)
	}
	in.ExpectedRevision = 1
	if _, err = f.svc.UpdateMentalModel(testCtx, f.agent, p.ID, in); !errors.Is(err, ErrDenied) {
		t.Fatal("agent approved its own model", err)
	}
	got, err := f.svc.UpdateMentalModel(testCtx, f.human, p.ID, in)
	if err != nil || got.Status != statusActive {
		t.Fatal("a person's save approves", got, err)
	}
	if r, _ := f.repo.Get(testCtx, colMentalModels, scopeA, p.ID); r.ApprovedBy != "human-1" {
		t.Fatal("approver", r.ApprovedBy)
	}
}

func TestAgentInvalidationOnlyAPersonForgets(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("배포는 금요일에 한다")
	ref := Ref{Collection: "memories", ID: m.ID, Revision: m.Revision}
	in := MaintenanceInput{Scope: scopeA, Proposal: MaintenanceProposal{Type: invalidationType, Memory: &ref, Reason: "outdated"}}
	for _, bad := range []MaintenanceProposal{
		{Type: invalidationType, Memory: &ref, Reason: "low_pass_rate"},
		{Type: invalidationType, Memory: &ref, Reason: "outdated", Evidence: &MaintenanceEvidence{Samples: 1}},
		{Type: invalidationType, Reason: "outdated"},
	} {
		if _, err := f.svc.ProposeMaintenance(testCtx, f.agent, MaintenanceInput{Scope: scopeA, Proposal: bad}); !errors.Is(err, ErrInvalid) {
			t.Fatal("bad invalidation accepted", bad, err)
		}
	}
	p, err := f.svc.ProposeMaintenance(testCtx, f.agent, in)
	if err != nil || p.Status != maintenanceOpen {
		t.Fatal(p, err)
	}
	if f.get(f.human, "memories", scopeA, m.ID).Status != statusActive {
		t.Fatal("a proposal changed the memory")
	}
	forget := MaintenanceDecision{Scope: scopeA, ExpectedRevision: p.Revision, Decision: "forget", ReasonCode: "stale"}
	if _, err = f.svc.DecideMaintenance(testCtx, f.agent, p.ID, forget); !errors.Is(err, ErrDenied) {
		t.Fatal("agent decided", err)
	}
	if _, err = f.svc.DecideMaintenance(testCtx, f.human, p.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: p.Revision, Decision: "swap"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("swap on one memory", err)
	}
	if _, err = f.svc.DecideMaintenance(testCtx, f.human, p.ID, forget); err != nil {
		t.Fatal(err)
	}
	if got := f.get(f.human, "memories", scopeA, m.ID); got.Status != "rejected" {
		t.Fatal("forget", got.Status)
	}
}

func TestAgentSeesAndCancelsOnlyItsOwnJobs(t *testing.T) {
	f := newOpsFixture(t)
	f.put(colJobs, Record{ID: "mine", Kind: wikiJobKind, Status: JobStatusPending, CreatedBy: "agent-1"})
	f.put(colJobs, Record{ID: "theirs", Kind: wikiJobKind, Status: JobStatusPending, CreatedBy: "human-1"})
	page, err := f.svc.ListOperations(testCtx, f.agent, OpsListInput{Scope: scopeA, Actor: "human-1"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "mine" {
		t.Fatalf("agent list = %+v, %v", page, err)
	}
	if all, _ := f.svc.ListOperations(testCtx, f.human, OpsListInput{Scope: scopeA}); len(all.Items) != 2 {
		t.Fatal("a person sees every job", all)
	}
	if _, err = f.svc.GetOperation(testCtx, f.agent, scopeA, "theirs"); !errors.Is(err, ErrNotFound) {
		t.Fatal("agent read another's job", err)
	}
	if _, err = f.svc.CancelOperation(testCtx, f.agent, "theirs", OperationChangeInput{Scope: scopeA}); !errors.Is(err, ErrNotFound) {
		t.Fatal("agent cancelled another's job", err)
	}
	if v, err := f.svc.CancelOperation(testCtx, f.agent, "mine", OperationChangeInput{Scope: scopeA}); err != nil || v.Status != JobStatusCancelled {
		t.Fatal(v, err)
	}
	if _, err = f.svc.DeleteOperation(testCtx, f.agent, "mine", OperationChangeInput{Scope: scopeA}); !errors.Is(err, ErrDenied) {
		t.Fatal("agent delete", err)
	}
	if f.job("theirs").Status != JobStatusPending {
		t.Fatal("another's job changed")
	}
}

func TestAgentFileUploadNeedsTheSpaceToListTheTool(t *testing.T) {
	f, _ := wikiFixture(t, extractAnswer(extractRuleClaim))
	f.auth.actors[f.agent].verbs["generate"] = true
	in := FileMemoriesInput{Scope: scopeA, Filename: "rules.md", ContentBase64: base64.StdEncoding.EncodeToString([]byte(extractKoreanSource))}
	if _, err := f.svc.ExtractFileMemories(testCtx, f.agent, in); !errors.Is(err, ErrDenied) {
		t.Fatal("agent upload without the space opting in", err)
	}
	tools := []string{agentUploadTool, "recall"}
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{MCPTools: &tools}, false); err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.ExtractFileMemories(testCtx, f.agent, in)
	if err != nil || out.CandidatesCreated != 1 {
		t.Fatalf("agent upload %+v %v", out, err)
	}
	if active, _ := f.repo.ListRecordsByStatus(testCtx, scopeA, colMemories, statusActive, 10); len(active) != 0 {
		t.Fatal("an upload made a memory active", active)
	}
	if _, err = f.svc.ExtractPreview(testCtx, f.agent, ExtractPreviewInput{Scope: scopeA, Text: extractKoreanSource}); !errors.Is(err, ErrDenied) {
		t.Fatal("preview stays human-only", err)
	}
}

func TestAgentMemoryUpdateWaitsForAPersonsResolution(t *testing.T) {
	f := newFixture(t)
	old := f.captureAndReview(f.human, captureInput(scopeA, "evt-o", explicit("기본 테마는 다크")))[0]
	mem := f.get(f.human, colMemories, scopeA, old.MemoryID)
	fix := explicit("기본 테마는 paper 라이트")
	fix.Basis = "inferred"
	fix.Supersedes = &Ref{Collection: colMemories, ID: mem.ID, Revision: mem.Revision}
	got := f.capture(f.agent, captureInput(scopeA, "evt-c", fix))
	if got[0].Status != "candidate" || got[0].Reason != "conflict_review" {
		t.Fatal("an agent's correction is not held for review", got)
	}
	held := f.get(f.human, colMemories, scopeA, mem.ID)
	if held.Status != statusActive || held.ConflictID == "" || held.Text != mem.Text {
		t.Fatal("the old memory left use or changed", held.Status, held.ConflictID)
	}
	rel := f.get(f.human, "memory_relations", scopeA, held.ConflictID)
	cand := f.get(f.human, "memory_candidates", scopeA, got[0].CandidateID)
	review := CandidateReviewInput{Scope: scopeA, ExpectedRevision: cand.Revision, Action: "approve", OperationID: "agent-fix",
		Conflict: &CandidateReviewConflict{ID: rel.ID, ExpectedRevision: rel.Revision, Memory: Ref{colMemories, held.ID, held.Revision}, Resolution: "replace_old", Reason: "person agrees"}}
	if _, err := f.svc.ReviewCandidate(testCtx, f.agent, cand.ID, review); !errors.Is(err, ErrDenied) {
		t.Fatal("agent approved its own correction", err)
	}
	if _, err := f.svc.ReviewCandidate(testCtx, f.human, cand.ID, review); err != nil {
		t.Fatal(err)
	}
	if f.get(f.human, colMemories, scopeA, mem.ID).Status != "superseded" {
		t.Fatal("a person's approval did not replace the memory")
	}
}

func TestAgentProposalTextPassesDefenseRules(t *testing.T) {
	f := newFixture(t)
	rules := []SpaceDefenseRule{{Pattern: `(?i)falcon`, Action: "redact"}, {Pattern: `\bINC-\d+\b`, Action: "block"}}
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{DefenseRules: &rules}, false); err != nil {
		t.Fatal(err)
	}
	d, err := f.svc.CreateDirective(testCtx, f.agent, directiveInput("Falcon", "Ship falcon builds first.", "both", 5))
	if err != nil || strings.Contains(strings.ToLower(d.Title+d.Rule), "falcon") {
		t.Fatal("directive proposal not redacted", d, err)
	}
	if _, err = f.svc.CreateDirective(testCtx, f.agent, directiveInput("t", "See INC-42.", "both", 5)); !errors.Is(err, ErrDefenseBlocked) {
		t.Fatal("blocked directive proposal stored", err)
	}
	mm, err := f.svc.CreateMentalModel(testCtx, f.agent, MentalModelInput{Scope: scopeA, Title: "Falcon", Query: "falcon deploys", Refresh: MentalModelRefresh{Mode: mmRefreshManual}})
	if err != nil || strings.Contains(strings.ToLower(mm.Title+mm.Query), "falcon") {
		t.Fatal("mental model proposal not redacted", mm, err)
	}
	if h, err := f.svc.CreateDirective(testCtx, f.human, directiveInput("Falcon", "Ship falcon builds first.", "both", 5)); err != nil || h.Title != "Falcon" {
		t.Fatal("a person's directive changed", h, err)
	}
}

func TestAgentInvalidationKeepsItsOwnRecord(t *testing.T) {
	m := Ref{Collection: colMemories, ID: "m1", Revision: 1}
	inv := MaintenanceProposal{Type: invalidationType, Memory: &m, Reason: "outdated"}
	sig := MaintenanceProposal{Type: "rule_often_violated", Memory: &m, Reason: "low_pass_rate"}
	if proposalID(scopeA, inv) == proposalID(scopeA, sig) {
		t.Fatal("an invalidation shares the signal proposal's record")
	}
}

func TestAgentFileJobOfAnotherIsNotFound(t *testing.T) {
	f := newFixture(t)
	f.put(colJobs, Record{ID: "fj", Kind: fileExtractJobKind, Status: JobStatusFailed, CreatedBy: "human-1", FileExtract: &FileExtractJob{RequestedBy: "human-1"}})
	if _, err := f.svc.GetFileExtractJob(testCtx, f.agent, scopeA, "fj"); !errors.Is(err, ErrNotFound) {
		t.Fatal("agent read another's file job", err)
	}
	if _, err := f.svc.GetFileExtractJob(testCtx, f.human, scopeA, "fj"); err != nil {
		t.Fatal(err)
	}
	f.svc.Wiki = &fakeWikiLLM{}
	tools := []string{agentUploadTool}
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{MCPTools: &tools}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RunFileExtractJob(testCtx, f.agent, scopeA, "fj"); !errors.Is(err, ErrNotFound) {
		t.Fatal("agent ran another's file job", err)
	}
}

func TestKnowledgeSourceListPagesByCursor(t *testing.T) {
	f, ing, _, req := knowledgeRealFixture(t)
	for i := 0; i < 2; i++ {
		f.put(colKnowledgeSources, Record{ID: "ks" + strconv.Itoa(i), Scope: req.Scope, Kind: "file", Status: statusActive, CreatedAt: time.Unix(int64(i+1), 0)})
	}
	first, err := f.svc.ListKnowledgeSources(testCtx, f.human, req.Scope, "", 2)
	if err != nil || first.NextCursor != "2" || !first.Truncated || len(first.Items) != 1 || first.Items[0].ID != ing.Source.ID {
		t.Fatalf("first page %+v %v", first, err)
	}
	last, err := f.svc.ListKnowledgeSources(testCtx, f.human, req.Scope, first.NextCursor, 2)
	if err != nil || last.NextCursor != "" || last.Truncated {
		t.Fatalf("last page %+v %v", last, err)
	}
}
