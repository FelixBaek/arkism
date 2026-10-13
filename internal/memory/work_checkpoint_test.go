package memory

// Offline work-checkpoint tests: the transactional fake repository
// stands in for Postgres, so CAS, receipt atomicity and rollback boundaries
// are exercised without sockets.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func workCheckpointPackage() WorkCheckpointPackage {
	return WorkCheckpointPackage{
		Title:      "atlas 인덱스 재구축",
		Goal:       "knowledge 청크 검색 복구",
		Repository: "https://example.test/org/repo",
		Branch:     "feat/reindex",
		Head:       "abc1234",
		Origin:     "claude-code",
		Decisions:  []string{"pgvector 유지"},
		Completed:  []string{"스키마 점검"},
		NextSteps:  []string{"색인 재실행"},
		Files:      []WorkCheckpointFile{{Path: "internal/memory/postgres.go", SHA256: strings.Repeat("ab", 32)}},
		Checks:     []WorkCheckpointCheck{{Command: "go test ./...", Outcome: "passed", Summary: "전부 통과"}},
	}
}

func workCheckpointSave(f *fixture, token, id, event string, revision int64, pkg WorkCheckpointPackage) (WorkCheckpointSaveOutput, error) {
	f.t.Helper()
	return f.svc.SaveWorkCheckpoint(testCtx, token, WorkCheckpointSaveInput{Scope: scopeA, ID: id, EventID: event, ExpectedRevision: revision, Package: pkg})
}

func TestWorkCheckpointSaveGetRoundtripAndIdempotency(t *testing.T) {
	f := newFixture(t)
	pkg := workCheckpointPackage()

	out, err := workCheckpointSave(f, f.agent, "task.alpha_1", "evt-1", 0, pkg)
	if err != nil {
		t.Fatal("save:", err)
	}
	if out.ID != "task.alpha_1" || out.Revision != 1 || out.Authority != AuthorityDataOnly {
		t.Fatalf("save output = %+v", out)
	}
	// Save answers metadata only; no package field may appear.
	raw, _ := json.Marshal(out)
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil || len(keys) != 3 || keys["id"] == nil || keys["revision"] == nil || keys["authority"] == nil {
		t.Fatalf("save response keys = %s", raw)
	}

	// A different program token (Codex) recovers the same package by ID.
	view, err := f.svc.GetWorkCheckpoint(testCtx, f.human, WorkCheckpointGetInput{Scope: scopeA, ID: "task.alpha_1"})
	if err != nil {
		t.Fatal("get:", err)
	}
	if view.Revision != 1 || view.Authority != AuthorityDataOnly || view.Package.Title != pkg.Title || view.Package.Goal != pkg.Goal ||
		view.Package.Repository != pkg.Repository || view.Package.Origin != pkg.Origin || len(view.Package.Files) != 1 ||
		view.Package.Files[0].Path != pkg.Files[0].Path || view.Package.Files[0].SHA256 != pkg.Files[0].SHA256 ||
		len(view.Package.Checks) != 1 || view.Package.Checks[0].Outcome != "passed" {
		t.Fatalf("recovered package = %+v", view.Package)
	}

	// Duplicate retry of the same request: same result, no new receipt.
	replay, err := workCheckpointSave(f, f.agent, "task.alpha_1", "evt-1", 0, pkg)
	if err != nil {
		t.Fatal("replay:", err)
	}
	if replay.Revision != 1 {
		t.Fatalf("replay revision = %d", replay.Revision)
	}
	if got := len(f.repo.rows(colWorkCheckpointReceipts)); got != 1 {
		t.Fatalf("receipts after replay = %d", got)
	}

	// Same event_id, different payload: conflict.
	changed := pkg
	changed.Status = "blocked"
	if _, err := workCheckpointSave(f, f.agent, "task.alpha_1", "evt-1", 1, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same event different payload = %v", err)
	}

	// Stale revision: conflict.
	if _, err := workCheckpointSave(f, f.human, "task.alpha_1", "evt-2", 0, pkg); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision = %v", err)
	}

	// Another program resumes the same ID.
	next := pkg
	next.Status = "completed"
	out2, err := workCheckpointSave(f, f.human, "task.alpha_1", "evt-2", 1, next)
	if err != nil || out2.Revision != 2 {
		t.Fatalf("resume save = %+v %v", out2, err)
	}

	// The old replay still answers with the revision recorded then, while a
	// fresh read returns the current content: never mixed.
	old, err := workCheckpointSave(f, f.agent, "task.alpha_1", "evt-1", 0, pkg)
	if err != nil || old.Revision != 1 {
		t.Fatalf("late replay = %+v %v", old, err)
	}
	view, err = f.svc.GetWorkCheckpoint(testCtx, f.human, WorkCheckpointGetInput{Scope: scopeA, ID: "task.alpha_1"})
	if err != nil || view.Revision != 2 || view.Package.Status != "completed" {
		t.Fatalf("get after resume = %+v %v", view, err)
	}

	// A completed status is caller-reported data, never a verified fact: the
	// stored row carries no verification marker.
	stored := f.repo.rows(colWorkCheckpoints)[0]
	if stored.VerificationStatus != "" || stored.ApprovedBy != "" {
		t.Fatalf("checkpoint implies verification: %+v", stored)
	}
}

func TestWorkCheckpointRestartAndForeignScope(t *testing.T) {
	f := newFixture(t)
	pkg := workCheckpointPackage()
	if _, err := workCheckpointSave(f, f.agent, "restart-task", "evt-a", 0, pkg); err != nil {
		t.Fatal("save:", err)
	}

	// Process restart: a fresh service over the same store recovers the work.
	restarted := &Service{Repo: f.repo, Auth: f.auth}
	view, err := restarted.GetWorkCheckpoint(testCtx, f.human, WorkCheckpointGetInput{Scope: scopeA, ID: "restart-task"})
	if err != nil || view.Package.Title != pkg.Title || len(view.Package.Decisions) != len(pkg.Decisions) {
		t.Fatalf("restart recovery = %+v %v", view, err)
	}

	// Foreign scope: another workspace neither sees nor can address the row.
	if _, err := f.svc.GetWorkCheckpoint(testCtx, f.human, WorkCheckpointGetInput{Scope: scopeB, ID: "restart-task"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign scope get = %v", err)
	}
	page, err := f.svc.ListWorkCheckpoints(testCtx, f.human, WorkCheckpointListInput{Scope: scopeB})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("foreign scope list = %+v %v", page, err)
	}
	if _, err := f.svc.GetWorkCheckpoint(testCtx, f.human, WorkCheckpointGetInput{Scope: scopeA, ID: "missing-task"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing get = %v", err)
	}
}

func TestWorkCheckpointGuards(t *testing.T) {
	f := newFixture(t)
	f.auth.actors["tok-pipe"] = actor("pipe-1", rolePipeline, true, allVerbs...)
	f.auth.actors["tok-nothing"] = actor("nothing-1", roleService, true)
	f.auth.actors["tok-writeonly"] = actor("writer-9", roleService, true, "write")
	pkg := workCheckpointPackage()

	cases := []struct {
		name  string
		token string
		id    string
		event string
		rev   int64
		pkg   func() WorkCheckpointPackage
		want  error
	}{
		{"pipeline role denied", "tok-pipe", "guard-1", "e1", 0, func() WorkCheckpointPackage { return pkg }, ErrDenied},
		{"no write permission", "tok-nothing", "guard-1", "e1", 0, func() WorkCheckpointPackage { return pkg }, ErrDenied},
		{"bad id charset", f.agent, "bad id!", "e1", 0, func() WorkCheckpointPackage { return pkg }, ErrInvalid},
		{"bad id length", f.agent, strings.Repeat("a", 129), "e1", 0, func() WorkCheckpointPackage { return pkg }, ErrInvalid},
		{"empty id", f.agent, "", "e1", 0, func() WorkCheckpointPackage { return pkg }, ErrInvalid},
		{"bad event id", f.agent, "guard-2", "e/1", 0, func() WorkCheckpointPackage { return pkg }, ErrInvalid},
		{"negative revision", f.agent, "guard-3", "e1", -1, func() WorkCheckpointPackage { return pkg }, ErrInvalid},
		{"missing title", f.agent, "guard-4", "e1", 0, func() WorkCheckpointPackage { p := pkg; p.Title = ""; return p }, ErrInvalid},
		{"missing goal", f.agent, "guard-5", "e1", 0, func() WorkCheckpointPackage { p := pkg; p.Goal = "  "; return p }, ErrInvalid},
		{"missing repository", f.agent, "guard-6", "e1", 0, func() WorkCheckpointPackage { p := pkg; p.Repository = ""; return p }, ErrInvalid},
		{"missing origin", f.agent, "guard-7", "e1", 0, func() WorkCheckpointPackage { p := pkg; p.Origin = ""; return p }, ErrInvalid},
		{"bad status", f.agent, "guard-8", "e1", 0, func() WorkCheckpointPackage { p := pkg; p.Status = "verified"; return p }, ErrInvalid},
		{"bad outcome", f.agent, "guard-9", "e1", 0, func() WorkCheckpointPackage {
			p := pkg
			p.Checks = []WorkCheckpointCheck{{Command: "x", Outcome: "ok"}}
			return p
		}, ErrInvalid},
		{"bad file hash", f.agent, "guard-10", "e1", 0, func() WorkCheckpointPackage {
			p := pkg
			p.Files = []WorkCheckpointFile{{Path: "a.go", SHA256: "nothex"}}
			return p
		}, ErrInvalid},
		{"array too long", f.agent, "guard-11", "e1", 0, func() WorkCheckpointPackage {
			p := pkg
			p.Decisions = make([]string, 101)
			for i := range p.Decisions {
				p.Decisions[i] = "d"
			}
			return p
		}, ErrInvalid},
		{"string too long", f.agent, "guard-12", "e1", 0, func() WorkCheckpointPackage { p := pkg; p.Goal = strings.Repeat("가", 8193); return p }, ErrInvalid},
		{"package over 64KiB", f.agent, "guard-13", "e1", 0, func() WorkCheckpointPackage {
			p := pkg
			p.Decisions = make([]string, 100)
			for i := range p.Decisions {
				p.Decisions[i] = strings.Repeat("a", 8192)
			}
			return p
		}, ErrInvalid},
		{"secret in package", f.agent, "guard-14", "e1", 0, func() WorkCheckpointPackage { p := pkg; p.Goal = "password: hunter2secret"; return p }, ErrSecretDetected},
		{"secret token in title", f.agent, "guard-15", "e1", 0, func() WorkCheckpointPackage {
			p := pkg
			p.Title = "leak -----BEGIN RSA PRIVATE KEY----- abc"
			return p
		}, ErrSecretDetected},
	}
	for _, tc := range cases {
		if _, err := workCheckpointSave(f, tc.token, tc.id, tc.event, tc.rev, tc.pkg()); !errors.Is(err, tc.want) {
			t.Fatalf("%s: got %v want %v", tc.name, err, tc.want)
		}
	}
	for _, col := range []string{colWorkCheckpoints, colWorkCheckpointReceipts} {
		if rows := f.repo.rows(col); len(rows) != 0 {
			t.Fatalf("%s stored rejected rows: %d", col, len(rows))
		}
	}

	// Generic CRUD never reaches the private collections.
	if _, err := f.svc.Create(testCtx, f.human, colWorkCheckpoints, CreateInput{Scope: scopeA, Kind: "work_checkpoint", Text: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("generic create = %v", err)
	}
	if _, err := f.svc.Get(testCtx, f.human, colWorkCheckpoints, scopeA, "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("generic get = %v", err)
	}
	if err := f.svc.Delete(testCtx, f.human, colWorkCheckpoints, "x", ChangeInput{Scope: scopeA, ExpectedRevision: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("generic delete = %v", err)
	}
	if _, err := f.svc.Transition(testCtx, f.human, colWorkCheckpoints, "x", ChangeInput{Scope: scopeA, ExpectedRevision: 1, Status: "approved"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("generic transition = %v", err)
	}
	if knownCollection(colWorkCheckpoints) || knownCollection(colWorkCheckpointReceipts) {
		t.Fatal("private collections must not be known collections")
	}

	// Happy save for the remaining assertions.
	pkg.Status = "in_progress"
	if _, err := workCheckpointSave(f, f.agent, "guard-live", "e-live", 0, pkg); err != nil {
		t.Fatal("save:", err)
	}

	// Read permission is separate from write.
	if _, err := f.svc.GetWorkCheckpoint(testCtx, "tok-writeonly", WorkCheckpointGetInput{Scope: scopeA, ID: "guard-live"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("read without permission = %v", err)
	}
	if _, err := f.svc.ListWorkCheckpoints(testCtx, "tok-writeonly", WorkCheckpointListInput{Scope: scopeA}); !errors.Is(err, ErrDenied) {
		t.Fatalf("list without permission = %v", err)
	}
	if _, err := f.svc.GetWorkCheckpoint(testCtx, f.human, WorkCheckpointGetInput{Scope: scopeA, ID: "../escape"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad id get = %v", err)
	}
	if _, err := f.svc.ListWorkCheckpoints(testCtx, f.human, WorkCheckpointListInput{Scope: scopeA, Limit: 101}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize limit = %v", err)
	}
	if _, err := f.svc.ListWorkCheckpoints(testCtx, f.human, WorkCheckpointListInput{Scope: scopeA, After: "bad id"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad cursor = %v", err)
	}

	// Privacy-quarantined checkpoints are invisible.
	stored := f.repo.rows(colWorkCheckpoints)[0]
	quarantined := stored
	quarantined.PrivacyQuarantined = true
	quarantined.Revision++
	f.repo.store[colWorkCheckpoints][recKey(scopeA, stored.ID)] = quarantined
	if _, err := f.svc.GetWorkCheckpoint(testCtx, f.human, WorkCheckpointGetInput{Scope: scopeA, ID: stored.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("quarantined get = %v", err)
	}
}

func TestWorkCheckpointSourceRefs(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	pkg := workCheckpointPackage()
	pkg.SourceRefs = []Ref{{Collection: "sources", ID: src.ID, Revision: src.Revision}}

	if _, err := workCheckpointSave(f, f.agent, "refs-task", "evt-r", 0, pkg); err != nil {
		t.Fatal("save with live ref:", err)
	}

	stale := pkg
	stale.SourceRefs = []Ref{{Collection: "sources", ID: src.ID, Revision: src.Revision + 5}}
	if _, err := workCheckpointSave(f, f.agent, "refs-task", "evt-r2", 1, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale ref revision = %v", err)
	}

	missing := pkg
	missing.SourceRefs = []Ref{{Collection: "sources", ID: "no-such-source", Revision: 1}}
	if _, err := workCheckpointSave(f, f.agent, "refs-task", "evt-r3", 1, missing); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing ref = %v", err)
	}

	foreign := pkg
	foreign.SourceRefs = []Ref{{Collection: "sources", ID: src.ID, Revision: src.Revision}}
	// The same source id never exists in another scope: the reference cannot cross.
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, WorkCheckpointSaveInput{Scope: scopeB, ID: "refs-task", EventID: "evt-r4", ExpectedRevision: 0, Package: foreign}); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign scope ref = %v", err)
	}

	// A caller that lost read permission on the referenced record is refused.
	f.auth.actors["tok-noread"] = actor("writer-2", roleAgent, true, "write")
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, "tok-noread", WorkCheckpointSaveInput{Scope: scopeA, ID: "refs-task2", EventID: "evt-r5", ExpectedRevision: 0, Package: pkg}); !errors.Is(err, ErrDenied) {
		t.Fatalf("ref permission recheck = %v", err)
	}

	// A content-erased record can no longer back a checkpoint.
	now := time.Now().UTC()
	erased := Record{ID: "erased-src", Scope: scopeA, Kind: "note", Status: statusActive, Revision: 3, ContentErased: true,
		CreatedAt: now, UpdatedAt: now, CreatedBy: "human-1", CreatedByType: roleHuman, Sources: []SourceRef{{SourceID: "erased-src", Locator: "original"}}}
	if err := f.repo.Insert(testCtx, "sources", erased); err != nil {
		t.Fatal(err)
	}
	erasedPkg := pkg
	erasedPkg.SourceRefs = []Ref{{Collection: "sources", ID: "erased-src", Revision: 3}}
	if _, err := workCheckpointSave(f, f.agent, "refs-task3", "evt-r6", 0, erasedPkg); !errors.Is(err, ErrDenied) {
		t.Fatalf("erased ref = %v", err)
	}

	// Refs never point at jobs, audit rows or the private collections.
	for _, col := range []string{colJobs, "audit_events", colWorkCheckpoints, colWorkCheckpointReceipts, "blobs", "session_archives"} {
		bad := pkg
		bad.SourceRefs = []Ref{{Collection: col, ID: "whatever", Revision: 1}}
		if _, err := workCheckpointSave(f, f.agent, "refs-task4", "evt-r7", 0, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ref to %s = %v", col, err)
		}
	}

	// The stored package keeps its references for the next program.
	view, err := f.svc.GetWorkCheckpoint(testCtx, f.human, WorkCheckpointGetInput{Scope: scopeA, ID: "refs-task"})
	if err != nil || len(view.Package.SourceRefs) != 1 || view.Package.SourceRefs[0].ID != src.ID || view.Package.SourceRefs[0].Revision != src.Revision {
		t.Fatalf("stored refs = %+v %v", view, err)
	}
}

func TestWorkCheckpointReceiptAuditNoBody(t *testing.T) {
	f := newFixture(t)
	pkg := workCheckpointPackage()
	marker := pkg.Title
	if _, err := workCheckpointSave(f, f.agent, "receipt-task", "evt-9", 0, pkg); err != nil {
		t.Fatal("save:", err)
	}
	if _, err := f.svc.GetWorkCheckpoint(testCtx, f.human, WorkCheckpointGetInput{Scope: scopeA, ID: "receipt-task"}); err != nil {
		t.Fatal("get:", err)
	}

	// Receipt rows carry request hash/id/revision only.
	receipts := f.repo.rows(colWorkCheckpointReceipts)
	if len(receipts) != 1 {
		t.Fatalf("receipts = %d", len(receipts))
	}
	r := receipts[0]
	if r.WorkCheckpoint == nil || r.WorkCheckpoint.Package != nil || r.WorkCheckpoint.RequestHash == "" ||
		r.WorkCheckpoint.ReceiptTargetID != "receipt-task" || r.WorkCheckpoint.ReceiptRevision != 1 {
		t.Fatalf("receipt shape = %+v", r.WorkCheckpoint)
	}
	if r.Text != "" || r.Summary != "" || r.Kind != kindWorkCheckpointReceipt || r.CreatedBy != "agent-1" {
		t.Fatalf("receipt body leak: %+v", r)
	}
	rawReceipt := structuredText(r.WorkCheckpoint)
	if strings.Contains(rawReceipt, marker) || strings.Contains(rawReceipt, "atlas") || strings.Contains(rawReceipt, "go test") {
		t.Fatalf("receipt carries body: %s", rawReceipt)
	}

	// Audit rows name the action and the id, never the package.
	audits := f.repo.rows("audit_events")
	if len(audits) < 2 {
		t.Fatalf("audit rows = %d", len(audits))
	}
	kinds := map[string]bool{}
	for _, a := range audits {
		kinds[a.Kind] = true
		if strings.Contains(structuredText(a), marker) || strings.Contains(structuredText(a), "go test ./...") {
			t.Fatalf("audit carries body: %s", structuredText(a))
		}
		if a.Target == nil || a.Target.ID != "receipt-task" {
			t.Fatalf("audit target = %+v", a.Target)
		}
	}
	if !kinds["create:"+colWorkCheckpoints] || !kinds["read:"+colWorkCheckpoints] {
		t.Fatalf("audit kinds = %v", kinds)
	}

	// Replay writes no new audit or receipt rows.
	before := len(f.repo.rows("audit_events"))
	if _, err := workCheckpointSave(f, f.agent, "receipt-task", "evt-9", 0, pkg); err != nil {
		t.Fatal("replay:", err)
	}
	if after := len(f.repo.rows("audit_events")); after != before {
		t.Fatalf("replay audited again: %d -> %d", before, after)
	}
}

func TestWorkCheckpointAuditFailureRollsBackEverything(t *testing.T) {
	f := newFixture(t)
	pkg := workCheckpointPackage()
	f.repo.failAudit = true
	if _, err := workCheckpointSave(f, f.agent, "rollback-task", "evt-b", 0, pkg); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("audit failure = %v", err)
	}
	for _, col := range []string{colWorkCheckpoints, colWorkCheckpointReceipts} {
		if rows := f.repo.rows(col); len(rows) != 0 {
			t.Fatalf("%s not rolled back: %d rows", col, len(rows))
		}
	}
	f.repo.failAudit = false
	if out, err := workCheckpointSave(f, f.agent, "rollback-task", "evt-b", 0, pkg); err != nil || out.Revision != 1 {
		t.Fatalf("save after rollback = %+v %v", out, err)
	}
}

func TestWorkCheckpointListPagination(t *testing.T) {
	f := newFixture(t)
	pkg := workCheckpointPackage()
	order := []string{"task-3", "task-1", "task-5", "task-2", "task-4"}
	for i, id := range order {
		p := pkg
		p.Title = "제목 " + id
		p.Status = "in_progress"
		if i%2 == 0 {
			p.Status = "blocked"
		}
		if _, err := workCheckpointSave(f, f.agent, id, "evt-"+id, 0, p); err != nil {
			t.Fatal("save:", err)
		}
	}

	page, err := f.svc.ListWorkCheckpoints(testCtx, f.human, WorkCheckpointListInput{Scope: scopeA, Limit: 2})
	if err != nil || page.Authority != AuthorityDataOnly || len(page.Items) != 2 {
		t.Fatalf("page1 = %+v %v", page, err)
	}
	if page.Items[0].ID != "task-1" || page.Items[1].ID != "task-2" || page.Next != "task-2" {
		t.Fatalf("page1 order = %+v next %q", page.Items, page.Next)
	}
	if page.Items[0].Title != "제목 task-1" || page.Items[0].Status != "in_progress" || page.Items[0].Origin != pkg.Origin || page.Items[0].Revision != 1 {
		t.Fatalf("page1 fields = %+v", page.Items)
	}

	page, err = f.svc.ListWorkCheckpoints(testCtx, f.human, WorkCheckpointListInput{Scope: scopeA, After: page.Next, Limit: 2})
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != "task-3" || page.Items[1].ID != "task-4" || page.Next != "task-4" {
		t.Fatalf("page2 = %+v %v", page, err)
	}
	if page.Items[0].Status != "blocked" || page.Items[1].Status != "blocked" {
		t.Fatalf("page2 statuses = %+v", page.Items)
	}
	page, err = f.svc.ListWorkCheckpoints(testCtx, f.human, WorkCheckpointListInput{Scope: scopeA, After: page.Next, Limit: 2})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "task-5" || page.Next != "" {
		t.Fatalf("page3 = %+v %v", page, err)
	}
	// Cursor at the exact end: empty page, no cursor.
	page, err = f.svc.ListWorkCheckpoints(testCtx, f.human, WorkCheckpointListInput{Scope: scopeA, After: "task-5", Limit: 2})
	if err != nil || len(page.Items) != 0 || page.Next != "" {
		t.Fatalf("tail page = %+v %v", page, err)
	}
	// Default limit of 20 returns everything in one page.
	page, err = f.svc.ListWorkCheckpoints(testCtx, f.human, WorkCheckpointListInput{Scope: scopeA})
	if err != nil || len(page.Items) != 5 || page.Next != "" {
		t.Fatalf("default page = %+v %v", page, err)
	}
	// Limits are exact: 100 is the maximum.
	if _, err = f.svc.ListWorkCheckpoints(testCtx, f.human, WorkCheckpointListInput{Scope: scopeA, Limit: 100}); err != nil {
		t.Fatal("limit 100:", err)
	}
}

// WorkCheckpointRows is the fake's optional store extension: one ID-ordered
// page of one scope, strictly after the cursor.
func (f *fakeRepo) WorkCheckpointRows(_ context.Context, scope Scope, after string, limit int) ([]Record, error) {
	if checkScope(scope) != nil || limit < 1 || limit > workCheckpointMaxLimit+1 || after != "" && !workCheckpointIDPattern.MatchString(after) {
		return nil, ErrInvalid
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Record
	for _, r := range f.rowsLocked(colWorkCheckpoints) {
		if !sameScope(r.Scope, scope) || after != "" && r.ID <= after {
			continue
		}
		out = append(out, r)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// workCheckpointForeignRows is a faulty store whose query crossed the scope
// fence; the service must refuse the page.
type workCheckpointForeignRows struct{ *fakeRepo }

func (r *workCheckpointForeignRows) WorkCheckpointRows(ctx context.Context, scope Scope, after string, limit int) ([]Record, error) {
	rows, err := r.fakeRepo.WorkCheckpointRows(ctx, scope, after, limit)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return append(rows, Record{ID: "task-z", Scope: scopeFar, Kind: kindWorkCheckpoint, Status: statusActive, Revision: 1,
		CreatedAt: now, UpdatedAt: now, WorkCheckpoint: &WorkCheckpoint{Package: &WorkCheckpointPackage{Title: "t", Goal: "g", Repository: "r", Origin: "o", Status: "in_progress"}}}), nil
}

// workCheckpointListerlessRepo hides the optional list extension, like a
// repository that never learned about work checkpoints.
type workCheckpointListerlessRepo struct{ Repository }

func TestWorkCheckpointListStoreContract(t *testing.T) {
	f := newFixture(t)
	pkg := workCheckpointPackage()
	if _, err := workCheckpointSave(f, f.agent, "task-1", "evt-1", 0, pkg); err != nil {
		t.Fatal("save:", err)
	}

	// A row from another scope inside the page fails the whole answer.
	foreigned := &Service{Repo: &workCheckpointForeignRows{fakeRepo: f.repo}, Auth: f.auth}
	if _, err := foreigned.ListWorkCheckpoints(testCtx, f.human, WorkCheckpointListInput{Scope: scopeA}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("foreign row in page = %v", err)
	}

	// Without the optional store extension the list is unavailable; save and
	// get still work over the base repository contract.
	basic := &Service{Repo: workCheckpointListerlessRepo{Repository: f.repo}, Auth: f.auth}
	if _, err := basic.ListWorkCheckpoints(testCtx, f.human, WorkCheckpointListInput{Scope: scopeA}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("list without store = %v", err)
	}
	if _, err := basic.GetWorkCheckpoint(testCtx, f.human, WorkCheckpointGetInput{Scope: scopeA, ID: "task-1"}); err != nil {
		t.Fatal("get without store:", err)
	}
}

func (x fakeTx) FenceWorkCheckpoint(ctx context.Context, scope Scope, id string, revision int64) error {
	r, err := x.Get(ctx, colWorkCheckpoints, scope, id)
	if err != nil {
		return err
	}
	if r.Revision != revision || !live(r) || r.ContentErased {
		return ErrConflict
	}
	return nil
}

func (x fakeTx) FenceWorkSubject(ctx context.Context, scope Scope) error {
	for _, row := range x.store[colJobs] {
		if row.Scope == scope && row.Kind == privacyDeletionKind && row.PrivacyDeletion != nil && row.PrivacyDeletion.TargetType == "subject" && row.PrivacyDeletion.State != privacyCancelled {
			return ErrDenied
		}
	}
	return nil
}
