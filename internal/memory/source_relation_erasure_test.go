package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSourceRelationErasure(t *testing.T) { testSourceRelationErasure(t, nil) }
func testSourceRelationErasure(t *testing.T, repo Repository) {
	t.Helper()
	f := newFixture(t)
	if repo != nil {
		f.svc.Repo = repo
	}
	repo = f.svc.Repo
	src := f.source()
	other := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "independent survivor origin"}))
	for i, s := range []Record{src, other} {
		r := Record{ID: fmt.Sprintf("member-%d", i), Scope: scopeA, Revision: 1, Status: "active", ApprovedBy: "human-1", Sources: refs(s), Text: "approved rule"}
		if e := repo.Insert(testCtx, "memories", r); e != nil {
			t.Fatal(e)
		}
	}
	marked, e := f.svc.Conflict(testCtx, f.human, ConflictInput{Scope: scopeA, Memories: []Ref{{Collection: "memories", ID: "member-0", Revision: 1}, {Collection: "memories", ID: "member-1", Revision: 1}}, Action: "mark", Reason: "erased original conflict prose"})
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 102; i++ {
		r := Record{ID: fmt.Sprintf("relation-%03d", i), Scope: scopeA, Kind: "supports", Status: "expired", Revision: 1, Text: "erased relation body", Sources: refs(src)}
		if e := repo.Insert(testCtx, "memory_relations", r); e != nil {
			t.Fatal(e)
		}
	}
	if e := repo.Insert(testCtx, "memory_relations", Record{ID: "resolved", Scope: scopeA, Kind: "conflicts_with", Status: "resolved", Revision: 1, Sources: refs(src), Text: "erased resolved prose", Conflict: &ConflictDetails{Hold: "both", ResolutionReason: "erased resolution prose"}}); e != nil {
		t.Fatal(e)
	}
	if e := repo.Insert(testCtx, "memory_relations", Record{ID: "no-survivors", Scope: scopeA, Kind: "conflicts_with", Status: "active", Revision: 1, Sources: refs(src), Text: "erased orphaned conflict", Conflict: &ConflictDetails{Hold: "both"}, Relations: []Ref{{Collection: "memories", ID: "absent-first", Revision: 1}, {Collection: "memories", ID: "absent-second", Revision: 1}}}); e != nil {
		t.Fatal(e)
	}
	scopes := []Scope{{WorkspaceID: "other", ProjectID: scopeA.ProjectID}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, AgentID: "other"}}
	for i, scope := range scopes {
		if e := repo.Insert(testCtx, "memory_relations", Record{ID: fmt.Sprintf("foreign-%d", i), Scope: scope, Revision: 1, Sources: refs(src), Text: "foreign body"}); e != nil {
			t.Fatal(e)
		}
	}
	if e = f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
		t.Fatal(e)
	}
	// Keep this historical relation-only source phase as a compatibility test.
	legacy, e := repo.Get(testCtx, "jobs", scopeA, sourceMemoryErasureID(scopeA, src.ID))
	if e != nil {
		t.Fatal(e)
	}
	legacy.Kind = sourceRelationErasureKind
	if e = repo.Replace(testCtx, "jobs", legacy, legacy.Revision); e != nil {
		t.Fatal(e)
	}
	initial, e := f.svc.SourceDeletionStatus(testCtx, f.human, scopeA, src.ID)
	if e != nil {
		t.Fatal(e)
	}
	initialJSON, e := json.Marshal(initial)
	if e != nil || !strings.Contains(string(initialJSON), `"redacted_conflicts":0`) {
		t.Fatalf("zero counter absent: %s (%v)", initialJSON, e)
	}
	token := erasureWorker(f)
	f.svc.Auth = strictRelationErasureAuth{f.svc.Auth}
	job, e := repo.ClaimJob(testCtx, time.Now(), time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	auditReached := false
	f.svc.Repo = relationAuditFailureRepo{Repository: repo, action: "erase:source_relations_batch", reached: &auditReached}
	if e = f.svc.eraseSourceMemories(testCtx, token, job); !errors.Is(e, ErrUnavailable) {
		t.Fatal("erasure audit did not fail", e)
	}
	if !auditReached {
		t.Fatal("erasure stopped before the injected audit failure")
	}
	kept, e := repo.Get(testCtx, "memory_relations", scopeA, marked.Relation.ID)
	if e != nil || kept.Text != marked.Relation.Text || len(kept.Sources) != 2 {
		t.Fatal("failed erasure lost conflict body", kept, e)
	}
	if _, e = repo.Get(testCtx, "memories", scopeA, "member-0"); e != nil {
		t.Fatal("failed erasure lost member", e)
	}
	saved, e := repo.Get(testCtx, "jobs", scopeA, job.ID)
	if e != nil || saved.SourceMemoryErasure.RedactedConflicts != 0 || len(saved.SourceMemoryErasure.DeletedRecords) != 0 {
		t.Fatal("failed erasure advanced counts", e)
	}
	f.svc.Repo = repo
	if e = f.svc.eraseSourceMemories(testCtx, token, job); e != nil {
		t.Fatal(e)
	}
	done := false
	for i := 0; i < 6; i++ {
		if e = f.svc.WorkOnce(testCtx, token); e != nil {
			t.Fatal(e)
		}
		status, e := f.svc.SourceDeletionStatus(testCtx, f.human, scopeA, src.ID)
		if e != nil {
			t.Fatal(e)
		}
		if status.Status == JobStatusDone {
			raw, _ := json.Marshal(status)
			if status.QueuedMemoryJobs != 0 || status.DeletedRecords["memory_relations"] != 104 || !strings.Contains(string(raw), `"redacted_conflicts":1`) || status.FullErasureVerified {
				t.Fatalf("bad counts: %s", raw)
			}
			done = true
			break
		}
	}
	if !done {
		t.Fatal("relations did not complete")
	}
	control, e := f.svc.Get(testCtx, f.human, "memory_relations", scopeA, marked.Relation.ID)
	if e != nil {
		t.Fatal("control unreadable", e)
	}
	raw, _ := json.Marshal(control)
	if control.Text != "" || len(control.Sources) != 0 || len(control.Relations) != 2 || control.Conflict.Hold != "both" || !strings.Contains(string(raw), `"content_erased":true`) || strings.Contains(string(raw), "erased original") {
		t.Fatalf("unsafe control: %s", raw)
	}
	survivor, e := repo.Get(testCtx, "memories", scopeA, "member-1")
	if e != nil || survivor.Status != "conflicted" || survivor.ConflictID != control.ID || survivor.Revision != marked.Memories[1].Revision {
		t.Fatal("survivor auto-resolved", survivor, e)
	}
	if _, e = repo.Get(testCtx, "memories", scopeA, "member-0"); !errors.Is(e, ErrNotFound) {
		t.Fatal("deleted memory survived", e)
	}
	for i, scope := range scopes {
		if _, e = repo.Get(testCtx, "memory_relations", scope, fmt.Sprintf("foreign-%d", i)); e != nil {
			t.Fatal("foreign relation lost", e)
		}
	}
	if _, e = repo.Get(testCtx, "sources", scopeA, other.ID); e != nil {
		t.Fatal("independent source lost", e)
	}
	resolve := ConflictInput{Scope: scopeA, Memories: control.Relations, Action: "resolve", Reason: "human selects surviving rule", ConflictID: control.ID, ExpectedRevision: control.Revision, Resolution: "keep_second"}
	f.svc.Repo = relationAuditFailureRepo{Repository: repo, action: "conflict:resolve"}
	if _, e = f.svc.Conflict(testCtx, f.human, resolve); !errors.Is(e, ErrUnavailable) {
		t.Fatal("resolution audit did not fail", e)
	}
	unchanged, e := repo.Get(testCtx, "memories", scopeA, "member-1")
	if e != nil || unchanged.Status != "conflicted" || unchanged.Revision != survivor.Revision {
		t.Fatal("failed resolution changed survivor", e)
	}
	if _, e = repo.Get(testCtx, "memory_relations", scopeA, control.ID); e != nil {
		t.Fatal("failed resolution lost control", e)
	}
	f.svc.Repo = repo
	out, e := f.svc.Conflict(testCtx, f.human, resolve)
	if e != nil {
		t.Fatal("cannot resolve erased conflict", e)
	}
	if len(out.Memories) != 1 || out.Memories[0].Status != "active" || out.Memories[0].ConflictID != "" {
		t.Fatal("wrong resolution", out)
	}
	if _, e = repo.Get(testCtx, "memory_relations", scopeA, control.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("resolved control retained", e)
	}
	if len(f.emb.calls) != 0 {
		t.Fatal("erasure called provider")
	}
}

type relationAuditFailureRepo struct {
	Repository
	action  string
	reached *bool
}
type relationAuditFailureTx struct {
	Tx
	action  string
	reached *bool
}

func (tx relationAuditFailureTx) CheckPrivacyOverlap(ctx context.Context, scope Scope, id, targetType string, impact PrivacyImpact, holding bool) error {
	return checkPrivacyOverlap(ctx, tx.Tx, scope, id, targetType, impact, holding)
}

func (r relationAuditFailureRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return fn(ctx, relationAuditFailureTx{Tx: tx, action: r.action, reached: r.reached})
	})
}
func (tx relationAuditFailureTx) Insert(ctx context.Context, col string, r Record) error {
	if col == "audit_events" && r.Kind == tx.action {
		if tx.reached != nil {
			*tx.reached = true
		}
		return ErrUnavailable
	}
	return tx.Tx.Insert(ctx, col, r)
}

func TestErasedConflictReadIsControlOnly(t *testing.T) {
	f := newFixture(t)
	r := Record{ID: "control", Scope: scopeA, Kind: "conflicts_with", Status: "active", Revision: 1, ContentErased: true, Text: "removed payload", Summary: "removed payload", Keywords: []string{"removed payload"}, Sources: []SourceRef{{SourceID: "removed", Locator: "removed payload"}}, Conflict: &ConflictDetails{Hold: "both", ResolutionReason: "removed payload"}, Relations: []Ref{{Collection: "memories", ID: "a", Revision: 1}, {Collection: "memory_candidates", ID: "b", Revision: 2}}}
	if e := f.repo.Insert(testCtx, "memory_relations", r); e != nil {
		t.Fatal(e)
	}
	got, e := f.svc.Get(testCtx, f.human, "memory_relations", scopeA, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "removed") || len(got.Relations) != 2 {
		t.Fatalf("payload leaked: %s", raw)
	}
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: r.ID}
	if _, e = f.svc.Get(testCtx, f.human, "memory_relations", scopeA, r.ID); !errors.Is(e, ErrDenied) {
		t.Fatal("read policy bypassed", e)
	}
	for _, mutate := range []func(*Record){func(r *Record) { r.Kind = "supports" }, func(r *Record) { r.Status = "resolved" }, func(r *Record) { r.Conflict = nil }, func(r *Record) { r.Relations = nil }, func(r *Record) {
		r.Relations = []Ref{{Collection: "sources", ID: "a", Revision: 1}, {Collection: "memories", ID: "b", Revision: 1}}
	}, func(r *Record) { r.Conflict = &ConflictDetails{Hold: "invalid"} }} {
		bad := r
		mutate(&bad)
		if _, e = conflictControlRecord(bad); !errors.Is(e, ErrUnavailable) {
			t.Fatal("malformed control accepted", e)
		}
	}
}

type strictRelationErasureAuth struct{ Authorizer }

func (a strictRelationErasureAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	if strings.HasPrefix(in.Action, "erase:") && in.Action != "erase:memory_records" && in.Action != "erase:source_relations" && in.Action != "erase:memory_relations" && in.Action != "erase:memories" {
		return Decision{}, nil
	}
	return a.Authorizer.Authorize(ctx, token, in)
}

func TestErasedConflictKeepsCandidateUnapproved(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprintf("expired=%t", expired), func(t *testing.T) {
			f := newFixture(t)
			src := f.source()
			c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "pending proposal", Sources: refs(src)}))
			if expired {
				past := time.Now().Add(-time.Hour)
				c.ExpiresAt = &past
			}
			c.Status = "conflicted"
			c.ConflictID = "candidate-control"
			if e := f.repo.Replace(testCtx, "memory_candidates", c, c.Revision); e != nil {
				t.Fatal(e)
			}
			c, e := f.repo.Get(testCtx, "memory_candidates", scopeA, c.ID)
			if e != nil {
				t.Fatal(e)
			}
			control := Record{ID: c.ConflictID, Scope: scopeA, Kind: "conflicts_with", Status: "active", Revision: 1, ContentErased: true, Sources: []SourceRef{}, Conflict: &ConflictDetails{Hold: "both"}, Relations: []Ref{{Collection: "memories", ID: "deleted-member", Revision: 1}, {Collection: "memory_candidates", ID: c.ID, Revision: c.Revision}}}
			if e = f.repo.Insert(testCtx, "memory_relations", control); e != nil {
				t.Fatal(e)
			}
			resolve := ConflictInput{Scope: scopeA, Memories: control.Relations, Action: "resolve", Reason: "retain for separate review", ConflictID: control.ID, ExpectedRevision: control.Revision, Resolution: "keep_second"}
			wantStatus := "candidate"
			if expired {
				if _, e = f.svc.Conflict(testCtx, f.human, resolve); !errors.Is(e, ErrConflict) {
					t.Fatal("expired candidate kept", e)
				}
				unchanged, err := f.repo.Get(testCtx, "memory_candidates", scopeA, c.ID)
				if err != nil || unchanged.Status != "conflicted" || unchanged.Revision != c.Revision {
					t.Fatal("failed keep changed candidate", unchanged, err)
				}
				resolve.Resolution = "reject_both"
				wantStatus = "rejected"
			}
			out, e := f.svc.Conflict(testCtx, f.human, resolve)
			if e != nil || len(out.Memories) != 1 || out.Memories[0].Status != wantStatus || out.Memories[0].ApprovedBy != "" || out.Memories[0].ConflictID != "" {
				t.Fatal("candidate implicitly approved", out, e)
			}
			if _, e = f.repo.Get(testCtx, "memories", scopeA, c.ID); !errors.Is(e, ErrNotFound) {
				t.Fatal("candidate copied into memory", e)
			}
			if len(f.embeddingJobs()) != 0 {
				t.Fatal("candidate embedded")
			}
			if _, e = f.repo.Get(testCtx, "memory_relations", scopeA, control.ID); !errors.Is(e, ErrNotFound) {
				t.Fatal("control retained", e)
			}
		})
	}
}

func TestRelationMemberFence(t *testing.T) {
	for _, mode := range []string{"success", "missing", "foreign", "audit"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			src := f.source()
			member := Record{ID: "member", Scope: scopeA, Status: "superseded", Revision: 3, UpdatedAt: time.Now().UTC()}
			if mode == "foreign" {
				member.Scope.ProjectID = "other"
			}
			if mode != "missing" {
				if e := f.repo.Insert(testCtx, "memories", member); e != nil {
					t.Fatal(e)
				}
			}
			relation := Record{ID: "relation", Scope: scopeA, Sources: refs(src), Relations: []Ref{{Collection: "memories", ID: member.ID, Revision: 4}, {Collection: "memories", ID: member.ID, Revision: 5}}}
			f.repo.failAudit = mode == "audit"
			e := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
				if e := insertSourceRecord(ctx, tx, "memory_relations", relation); e != nil {
					return e
				}
				return audit(ctx, tx, scopeA, "human-1", "create:memory_relations", relation.ID)
			})
			want := error(nil)
			if mode == "missing" || mode == "foreign" {
				want = ErrConflict
			}
			if mode == "audit" {
				want = ErrUnavailable
			}
			if !errors.Is(e, want) {
				t.Fatalf("got %v want %v", e, want)
			}
			if mode != "missing" {
				got, e := f.repo.Get(testCtx, "memories", member.Scope, member.ID)
				count := int64(0)
				if mode == "success" {
					count = 1
				}
				if e != nil || got.MemoryReferenceFence != count || got.Revision != member.Revision || !got.UpdatedAt.Equal(member.UpdatedAt) || got.Status != member.Status {
					t.Fatal("semantic change or wrong fence", got, e)
				}
				data, _ := json.Marshal(got)
				if strings.Contains(string(data), "fence") {
					t.Fatalf("internal fence leaked: %s", data)
				}
			}
			if want != nil {
				if _, e := f.repo.Get(testCtx, "memory_relations", scopeA, relation.ID); !errors.Is(e, ErrNotFound) {
					t.Fatal("failed write left relation", e)
				}
			}
		})
	}
}
