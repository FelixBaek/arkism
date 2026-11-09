//go:build linux

package memory

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// The fake store's half of spaceEraser: the same rules as the Postgres one over the in-memory rows.
func (x fakeTx) SpaceArtifacts(_ context.Context, space Scope, limit int) ([]Record, error) {
	out := []Record{}
	for _, r := range x.store[colArtifacts] {
		if spaceScope(r.Scope) == space && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}

func (x fakeTx) EraseSpace(_ context.Context, space Scope, only *spaceClear) (map[string]int64, error) {
	now := time.Now()
	if only == nil {
		for _, j := range x.store[colJobs] {
			held := j.PrivacyDeletion != nil && (j.PrivacyDeletion.State == privacyHoldHeld || j.PrivacyDeletion.LegalHoldStatus == privacyHoldHeld)
			if spaceScope(j.Scope) == space && (j.LeaseUntil.After(now) || held) {
				return nil, ErrConflict
			}
		}
	}
	counts := map[string]int64{}
	for col, rows := range x.store {
		for key, r := range rows {
			if spaceScope(r.Scope) != space || only != nil && (!slices.Contains(only.Collections, col) || only.Kind != "" && r.Kind != only.Kind) {
				continue
			}
			delete(rows, key)
			counts[col]++
		}
	}
	return counts, nil
}

func spaceRows(f *fixture, space Scope) map[string]int {
	f.repo.mu.Lock()
	defer f.repo.mu.Unlock()
	out := map[string]int{}
	for col, rows := range f.repo.store {
		for _, r := range rows {
			if spaceScope(r.Scope) == space {
				out[col]++
			}
		}
	}
	return out
}

func seedSpace(f *fixture, space Scope) {
	sub := space
	sub.UserID = "human-1"
	f.put(colMemories, Record{ID: "m1", Scope: space, Kind: "decision", Status: statusActive, Text: "use tabs"})
	f.put(colMemories, Record{ID: "m2", Scope: space, Kind: "lesson", Status: statusActive, Text: "retry once"})
	f.put("memory_candidates", Record{ID: "c1", Scope: space, Kind: "decision", Status: "candidate", Text: "maybe spaces"})
	f.put(colObservations, Record{ID: "o1", Scope: space, Kind: "observation", Status: statusActive, Text: "likes tabs"})
	f.put(colMentalModels, Record{ID: "mm1", Scope: space, Kind: "mental_model", Status: statusActive})
	f.put(colWikiPages, Record{ID: "w1", Scope: sub, Kind: "page", Status: statusActive})
	f.put(colSpaceConfigs, Record{ID: spaceConfigID, Scope: space, Kind: "space_config", Status: statusActive})
	f.put(colJobs, Record{ID: "j1", Scope: space, Kind: "embedding", Status: JobStatusPending})
}

func TestDeleteSpaceRemovesEverythingOfThatSpaceOnly(t *testing.T) {
	f := newFixture(t)
	seedSpace(f, scopeA)
	seedSpace(f, scopeB)
	for name, call := range map[string]func() error{
		"no confirm":    func() error { _, err := f.svc.DeleteSpace(testCtx, f.human, scopeA, ""); return err },
		"wrong confirm": func() error { _, err := f.svc.DeleteSpace(testCtx, f.human, scopeA, "proj-b"); return err },
		"alias": func() error {
			_, err := f.svc.DeleteSpace(testCtx, f.human, Scope{WorkspaceID: "ws-a", ProjectID: "@main"}, "@main")
			return err
		},
	} {
		if err := call(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := f.svc.DeleteSpace(testCtx, f.agent, scopeA, scopeA.ProjectID); !errors.Is(err, ErrDenied) {
		t.Fatalf("an agent deleted a space: %v", err)
	}
	report, err := f.svc.DeleteSpace(testCtx, f.human, scopeA, scopeA.ProjectID)
	if err != nil || report.Deleted[colMemories] != 2 || report.Deleted[colWikiPages] != 1 || report.Deleted[colJobs] != 1 {
		t.Fatalf("delete = %+v, %v", report, err)
	}
	if left := spaceRows(f, scopeA); len(left) != 1 || left["audit_events"] != 1 {
		t.Fatalf("left in the deleted space: %v", left)
	}
	if kept := spaceRows(f, scopeB); kept[colMemories] != 2 || kept[colWikiPages] != 1 || kept[colJobs] != 1 {
		t.Fatalf("the other space changed: %v", kept)
	}
}

func TestDeleteSpaceRefusesRunningJobsAndLegalHolds(t *testing.T) {
	f := newFixture(t)
	seedSpace(f, scopeA)
	f.put(colJobs, Record{ID: "running", Kind: "embedding", Status: JobStatusRunning, LeaseUntil: time.Now().Add(time.Minute)})
	if _, err := f.svc.DeleteSpace(testCtx, f.human, scopeA, scopeA.ProjectID); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleted under a running job: %v", err)
	}
	if spaceRows(f, scopeA)[colMemories] != 2 {
		t.Fatal("a refused delete removed rows")
	}
	g := newFixture(t)
	seedSpace(g, scopeA)
	g.put(colJobs, Record{ID: "hold", Kind: privacyDeletionKind, Status: JobStatusDone, PrivacyDeletion: &PrivacyDeletion{State: privacyCompleted, LegalHoldStatus: privacyHoldHeld}})
	if _, err := g.svc.DeleteSpace(testCtx, g.human, scopeA, scopeA.ProjectID); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleted under a legal hold: %v", err)
	}
}

func TestDeleteSpacesReportsEachSpace(t *testing.T) {
	f := newFixture(t)
	seedSpace(f, scopeA)
	seedSpace(f, scopeB)
	out, err := f.svc.DeleteSpaces(testCtx, f.human, "ws-a", SpaceDeleteBatchInput{Spaces: []SpaceDeleteItem{{ProjectID: "proj-a", Confirm: "proj-a"}, {ProjectID: "proj-b", Confirm: "typo"}}})
	if err != nil || len(out.Results) != 2 || out.Results[0].Report == nil || out.Results[1].Error != "invalid_request" {
		t.Fatalf("batch = %+v, %v", out, err)
	}
	if spaceRows(f, scopeA)[colMemories] != 0 || spaceRows(f, scopeB)[colMemories] != 2 {
		t.Fatal("the batch deleted the wrong spaces")
	}
	dup := SpaceDeleteBatchInput{Spaces: []SpaceDeleteItem{{ProjectID: "proj-b", Confirm: "proj-b"}, {ProjectID: "proj-b", Confirm: "proj-b"}}}
	if _, err := f.svc.DeleteSpaces(testCtx, f.human, "ws-a", dup); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a repeated space: %v", err)
	}
	if _, err := f.svc.DeleteSpaces(testCtx, f.human, "ws-a", SpaceDeleteBatchInput{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an empty batch: %v", err)
	}
}

func TestClearSpaceMemoriesKeepsTheRest(t *testing.T) {
	f := newFixture(t)
	seedSpace(f, scopeA)
	if _, err := f.svc.ClearSpaceMemories(testCtx, f.human, scopeA, "proj-a", "nonsense"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown kind: %v", err)
	}
	if _, err := f.svc.ClearSpaceMemories(testCtx, f.agent, scopeA, "proj-a", ""); !errors.Is(err, ErrDenied) {
		t.Fatalf("an agent cleared memories: %v", err)
	}
	one, err := f.svc.ClearSpaceMemories(testCtx, f.human, scopeA, "proj-a", "lesson")
	if err != nil || one.Deleted[colMemories] != 1 || one.Deleted["memory_candidates"] != 0 {
		t.Fatalf("clear lesson = %+v, %v", one, err)
	}
	all, err := f.svc.ClearSpaceMemories(testCtx, f.human, scopeA, "proj-a", "")
	if err != nil || all.Deleted[colMemories] != 1 || all.Deleted["memory_candidates"] != 1 || all.Deleted[colObservations] != 1 {
		t.Fatalf("clear all = %+v, %v", all, err)
	}
	if left := spaceRows(f, scopeA); left[colMentalModels] != 1 || left[colSpaceConfigs] != 1 || left[colWikiPages] != 1 || left[colMemories] != 0 {
		t.Fatalf("clear removed more than memories: %v", left)
	}
}

func TestAcceptingSpaceProposalsRunsTheDelete(t *testing.T) {
	f := newFixture(t)
	seedSpace(f, scopeA)
	proposal, err := f.svc.ProposeMaintenance(testCtx, f.agent, changeInput(ChangeRequest{Action: "memories_clear"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.DecideMaintenance(testCtx, f.human, proposal.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "accept"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted a clear without the typed confirm: %v", err)
	}
	if _, err = f.svc.DecideMaintenance(testCtx, f.human, proposal.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "dismiss", Confirm: "proj-a"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a confirm on dismiss: %v", err)
	}
	done, err := f.svc.DecideMaintenance(testCtx, f.human, proposal.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "accept", Confirm: "proj-a"})
	if err != nil || done.Status != "accepted" || spaceRows(f, scopeA)[colMemories] != 0 || spaceRows(f, scopeA)[colMentalModels] != 1 {
		t.Fatalf("accept clear = %+v, %v (%v)", done.Status, err, spaceRows(f, scopeA))
	}
	del, err := f.svc.ProposeMaintenance(testCtx, f.agent, changeInput(ChangeRequest{Action: "space_delete"}))
	if err != nil {
		t.Fatal(err)
	}
	gone, err := f.svc.DecideMaintenance(testCtx, f.human, del.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "accept", Confirm: "proj-a"})
	if err != nil || gone.Status != "accepted" || gone.Maintenance.Decision != "accept" {
		t.Fatalf("accept delete = %+v, %v", gone, err)
	}
	if left := spaceRows(f, scopeA); len(left) != 1 || left["audit_events"] != 1 {
		t.Fatalf("left after an accepted delete: %v", left)
	}
}

func TestSpaceDeleteRoutesAreHumanOnly(t *testing.T) {
	f := newFixture(t)
	seedSpace(f, scopeA)
	h := Handler(f.svc)
	call := func(token, method, path, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if code := call(f.agent, http.MethodDelete, "/v1/spaces/ws-a/proj-a?confirm=proj-a", ""); code != http.StatusForbidden {
		t.Fatalf("agent delete = %d", code)
	}
	if code := call(f.agent, http.MethodPost, "/v1/spaces/delete?workspace_id=ws-a&project_id=proj-a", `{"spaces":[{"project_id":"proj-a","confirm":"proj-a"}]}`); code != http.StatusForbidden {
		t.Fatalf("agent batch delete = %d", code)
	}
	if code := call(f.human, http.MethodDelete, "/v1/spaces/ws-a/proj-a/memories?confirm=proj-a&kind=lesson", ""); code != http.StatusOK {
		t.Fatalf("human clear = %d", code)
	}
	if code := call(f.human, http.MethodDelete, "/v1/spaces/ws-a/proj-a?confirm=proj-a", ""); code != http.StatusOK {
		t.Fatalf("human delete = %d", code)
	}
	// The fake store has no rename; the route answers that the store cannot do it.
	if code := call(f.human, http.MethodPost, "/v1/spaces/ws-a/proj-b/rename", `{"project_id":"proj-c"}`); code != http.StatusServiceUnavailable {
		t.Fatalf("rename without the database store = %d", code)
	}
	if code := call(f.agent, http.MethodPost, "/v1/spaces/ws-a/proj-b/rename", `{"project_id":"proj-c"}`); code != http.StatusForbidden {
		t.Fatalf("agent rename = %d", code)
	}
}
