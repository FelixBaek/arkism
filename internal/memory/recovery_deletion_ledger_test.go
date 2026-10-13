package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func (x fakeTx) RecoveryPrivacyRecords(_ context.Context, scope Scope) ([]Record, error) {
	rows := []Record{}
	for _, row := range x.store[colJobs] {
		if row.Scope == scope && row.Kind == privacyDeletionKind {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows[:min(len(rows), deletionLedgerLimit+1)], nil
}

func recoveryLedgerRequest(scope Scope, key string) Record {
	now := time.Now().Add(-time.Second)
	pd := &PrivacyDeletion{State: privacyQuarantined, RequesterType: "human", RequesterID: "human-1", TargetType: "work_checkpoint", TargetIDs: []string{"work"}, IdempotencyKey: key, Reason: "private-reason-sentinel", LegalHoldStatus: privacyHoldNone, RetentionPolicyRef: retentionExternal, RequestedAt: now}
	return Record{ID: privacyJobID(scope, pd.RequesterID, key), Scope: scope, Kind: privacyDeletionKind, Revision: 1, Status: JobStatusPending, CreatedAt: now, PrivacyDeletion: pd}
}

func TestRecoveryDeletionLedgerValidation(t *testing.T) {
	for _, mode := range []string{"empty", "pending", "service", "failed", "held", "completed", "foreign", "bad-id", "bad-state", "bad-target", "bad-hold", "bad-time", "denied", "overflow", "independent-limits"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			f.auth.actors[f.human].verbs["export"] = true
			r := recoveryLedgerRequest(scopeA, "request")
			wantError := false
			switch mode {
			case "service":
				r.PrivacyDeletion.RequesterType = "service"
			case "failed":
				r.PrivacyDeletion.State = privacyFailed
			case "held":
				r.PrivacyDeletion.State, r.PrivacyDeletion.LegalHoldStatus = privacyHeld, privacyHoldHeld
			case "completed":
				r.PrivacyDeletion.State = privacyCompleted
			case "foreign":
				r = recoveryLedgerRequest(scopeB, "foreign")
			case "bad-id":
				r.ID = "wrong"
				wantError = true
			case "bad-state":
				r.PrivacyDeletion.State = "unknown"
				wantError = true
			case "bad-target":
				r.PrivacyDeletion.TargetIDs = []string{"work", "work"}
				wantError = true
			case "bad-hold":
				r.PrivacyDeletion.LegalHoldStatus = "unknown"
				wantError = true
			case "bad-time":
				r.PrivacyDeletion.RequestedAt = time.Now().Add(time.Hour)
				wantError = true
			case "denied":
				f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: r.ID}
				wantError = true
			case "overflow", "independent-limits":
				count := deletionLedgerLimit
				if mode == "overflow" {
					count++
					wantError = true
				}
				for i := 0; i < count; i++ {
					if err := f.repo.Insert(testCtx, colJobs, recoveryLedgerRequest(scopeA, fmt.Sprint(i))); err != nil {
						t.Fatal(err)
					}
					if mode == "independent-limits" {
						id := fmt.Sprint(i)
						if err := f.repo.Insert(testCtx, colJobs, Record{ID: sourceDeletionID(scopeA, id), Scope: scopeA, Kind: "source_deletion", Status: "done", Revision: 1, CreatedAt: time.Now().Add(-time.Second), Target: &Ref{Collection: "sources", ID: id, Revision: 1}}); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if mode != "empty" && mode != "overflow" && mode != "independent-limits" {
				if err := f.repo.Insert(testCtx, colJobs, r); err != nil {
					t.Fatal(err)
				}
			}
			out, err := f.svc.ExportRecoveryDeletions(testCtx, f.human, scopeA)
			if wantError {
				if err == nil || out.PrivacyRequests != nil || out.Targets != nil {
					t.Fatalf("partial/invalid export: %+v %v", out, err)
				}
				return
			}
			if err != nil || out.Version != 2 || out.PrivacyRequests == nil {
				t.Fatalf("export: %+v %v", out, err)
			}
			want := 1
			if mode == "empty" || mode == "foreign" {
				want = 0
			}
			if mode == "independent-limits" {
				want = deletionLedgerLimit
				if len(out.Targets) != want {
					t.Fatal("receipt budget reduced")
				}
			}
			if len(*out.PrivacyRequests) != want {
				t.Fatalf("requests=%d want %d", len(*out.PrivacyRequests), want)
			}
			raw, _ := json.Marshal(out)
			if strings.Contains(string(raw), "private-reason-sentinel") || strings.Contains(string(raw), "requester_id") {
				t.Fatal("private metadata leaked")
			}
		})
	}
}

func TestRecoveryDeletionLedgerVersionSelection(t *testing.T) {
	f := newFixture(t)
	f.auth.actors[f.human].verbs["export"] = true
	for _, version := range []int{0, 1, 2, 3, -1} {
		body, _ := json.Marshal(map[string]any{"scope": scopeA, "version": version})
		req := httptest.NewRequest("POST", "/v1/deletions/export", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+f.human)
		response := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(response, req)
		want := 400
		if version == 1 || version == 2 {
			want = 200
		}
		if response.Code != want {
			t.Fatalf("version %d: %d want %d", version, response.Code, want)
		}
	}
}

func TestPostgresRecoveryDeletionLedgerPendingAndHeld(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	f.auth.actors[f.human] = actor("human-1", "human", true, append(allVerbs, "export", "hold")...)
	input := workCheckpointPGInput(scopeA, "pending-work", "event", 0, workCheckpointPackage())
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, input); err != nil {
		t.Fatal(err)
	}
	deletion, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "pending-delete", TargetType: "work_checkpoint", TargetIDs: []string{input.ID}, Reason: "private-reason-sentinel"})
	if err != nil {
		t.Fatal(err)
	}
	old, err := f.svc.ExportDeletions(testCtx, f.human, scopeA)
	if err != nil || len(old.Targets) != 0 || old.PrivacyRequests != nil {
		t.Fatal("v1 contract changed", err)
	}
	for _, state := range []string{privacyQuarantined, privacyHeld} {
		if state == privacyHeld {
			if _, err := f.svc.HoldPrivacyDeletion(testCtx, f.human, deletion.ID, PrivacyHoldInput{Scope: scopeA, ExpectedRevision: deletion.Revision, Reason: "review", PolicyRef: "owner_hold"}); err != nil {
				t.Fatal(err)
			}
		}
		body, _ := json.Marshal(map[string]any{"scope": scopeA, "version": 2})
		req := httptest.NewRequest("POST", "/v1/deletions/export", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+f.human)
		response := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(response, req)
		var out DeletionLedger
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &out) != nil || out.PrivacyRequests == nil || len(*out.PrivacyRequests) != 1 {
			t.Fatalf("v2 HTTP: %d %s", response.Code, response.Body.String())
		}
		got := (*out.PrivacyRequests)[0]
		if got.ID != deletion.ID || got.State != state || len(got.TargetIDs) != 1 || got.TargetIDs[0] != input.ID {
			t.Fatalf("missing pending/held request: %+v", got)
		}
		if state == privacyHeld && got.LegalHoldStatus != privacyHoldHeld {
			t.Fatal("hold lost")
		}
	}
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: deletion.ID}
	if out, err := f.svc.ExportRecoveryDeletions(testCtx, f.human, scopeA); !errors.Is(err, ErrDenied) || out.PrivacyRequests != nil {
		t.Fatalf("denied partial export: %+v %v", out, err)
	}
}

// Insert a new request between the receipt and privacy-request reads. Both
// lists must describe one snapshot, not two separately committed moments.
type recoveryLedgerInterleavedRepo struct {
	Repository
	afterReceipts func()
}

type recoveryLedgerInterleavedTx struct {
	Tx
	afterReceipts func()
}

func (r recoveryLedgerInterleavedRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return fn(ctx, recoveryLedgerInterleavedTx{Tx: tx, afterReceipts: r.afterReceipts})
	})
}

func (tx recoveryLedgerInterleavedTx) DeletionRecords(ctx context.Context, scope Scope) ([]Record, error) {
	rows, err := tx.Tx.DeletionRecords(ctx, scope)
	if err == nil {
		tx.afterReceipts()
	}
	return rows, err
}

func (tx recoveryLedgerInterleavedTx) RecoveryPrivacyRecords(ctx context.Context, scope Scope) ([]Record, error) {
	return tx.Tx.(recoveryDeletionReader).RecoveryPrivacyRecords(ctx, scope)
}

func TestPostgresRecoveryDeletionLedgerSnapshot(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.auth.actors[f.human].verbs["export"] = true
	var once sync.Once
	f.svc.Repo = recoveryLedgerInterleavedRepo{Repository: p, afterReceipts: func() {
		once.Do(func() {
			if err := p.Insert(testCtx, colJobs, recoveryLedgerRequest(scopeA, "concurrent-request")); err != nil {
				t.Fatal(err)
			}
		})
	}}
	out, err := f.svc.ExportRecoveryDeletions(testCtx, f.human, scopeA)
	if err != nil || out.PrivacyRequests == nil || len(*out.PrivacyRequests) != 0 {
		t.Fatalf("mixed snapshots: %+v %v", out, err)
	}
	f.svc.Repo = p
	out, err = f.svc.ExportRecoveryDeletions(testCtx, f.human, scopeA)
	if err != nil || out.PrivacyRequests == nil || len(*out.PrivacyRequests) != 1 {
		t.Fatalf("next snapshot lost request: %+v %v", out, err)
	}
}
