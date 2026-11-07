package memory

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func durableMaintenanceFixture(t *testing.T) (*Postgres, *Service, Scope, string) {
	t.Helper()
	p, _, scope, _ := privacyTraceFixture(t)
	token := testPrincipalToken("durable-maintenance-human")
	if err := p.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: scope.UserID, Role: "human", Token: token, OrganizationID: scope.WorkspaceID, ProjectID: scope.ProjectID, Actions: []string{"*"}}); err != nil {
		t.Fatal(err)
	}
	auth, err := NewPostgresAuthorizer(p)
	if err != nil {
		t.Fatal(err)
	}
	return p, &Service{Repo: p, Auth: auth}, scope, token
}
func TestMaintenanceSubjectRestartHoldAndEquivalentImpact(t *testing.T) {
	p, svc, scope, token := durableMaintenanceFixture(t)
	for i := 0; i < 205; i++ {
		if err := p.Insert(testCtx, "sources", Record{ID: fmt.Sprintf("bulk-%04d", i), Scope: scope, Kind: "document", Status: statusActive, Revision: 1, Text: "synthetic"}); err != nil {
			t.Fatal(err)
		}
	}
	var want PrivacyImpact
	if err := p.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		var err error
		want, err = tx.(*Postgres).DiscoverPrivacyImpactComplete(ctx, scope, "subject", []string{scope.UserID})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	in := PrivacyDeletionRequest{Scope: scope, TargetType: "subject", TargetIDs: []string{scope.UserID}, IdempotencyKey: "maintenance-subject", Reason: "owner_request", RetentionPolicyRef: retentionSynthetic}
	st, err := svc.BeginPrivacyMaintenance(testCtx, token, in)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Active {
		t.Fatal("gate not closed")
	}
	if _, err = p.Get(testCtx, "sources", scope, "ctx-src"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("owner application read bypass", err)
	}
	if err = p.Insert(testCtx, "sources", Record{ID: "during-maintenance", Scope: scope, Kind: "document", Status: statusActive, Revision: 1}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("owner application write bypass", err)
	}
	same, err := p.MemoryMaintenanceStatus(testCtx)
	if err != nil || same.Revision != st.Revision {
		t.Fatal("status changed revision", same, err)
	}
	st, err = p.AdvanceMemoryMaintenance(testCtx, st.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if st.Scanned != 100 {
		t.Fatal("scan not bounded", st)
	}
	st, err = p.HoldMemoryMaintenance(testCtx, st.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.AdvanceMemoryMaintenance(testCtx, st.Revision); !errors.Is(err, ErrConflict) {
		t.Fatal("held advanced", err)
	}
	fresh := &Postgres{pool: p.pool}
	st, err = fresh.MemoryMaintenanceStatus(testCtx)
	if err != nil || !st.Held || st.Scanned != 100 {
		t.Fatal("restart lost checkpoint", st, err)
	}
	st, err = fresh.HoldMemoryMaintenance(testCtx, st.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(testCtx)
	cancel()
	if _, err = fresh.AdvanceMemoryMaintenance(cancelled, st.Revision); err == nil {
		t.Fatal("cancel accepted")
	}
	after, err := fresh.MemoryMaintenanceStatus(testCtx)
	if err != nil || after != st {
		t.Fatal("cancel changed progress", after, st, err)
	}
	for i := 0; i < 100 && st.Active; i++ {
		prior := st.Scanned
		st, err = fresh.AdvanceMemoryMaintenance(testCtx, st.Revision)
		if err != nil {
			t.Fatal(err)
		}
		if st.Active && st.Scanned-prior > 100 {
			t.Fatal("unbounded page", st)
		}
	}
	if st.Active {
		t.Fatal("maintenance did not finish", st)
	}
	job, err := p.Get(testCtx, colJobs, scope, privacyJobID(scope, scope.UserID, in.IdempotencyKey))
	if err != nil {
		t.Fatal(err)
	}
	got := job.PrivacyDeletion.Impact
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("impact differs\nwant=%+v\ngot=%+v", want, got)
	}
	if !job.PrivacyDeletion.Report.LiveAccessRemoved || job.PrivacyDeletion.State != privacyQuarantined {
		t.Fatal("premature publication")
	}
	for _, ref := range got.Records {
		r, err := p.Get(testCtx, ref.Collection, scope, ref.ID)
		if err != nil || !r.PrivacyQuarantined {
			t.Fatal("unquarantined", ref, err)
		}
	}
}

func TestMaintenanceBeginDrainsInflightAndFailedBeginRollsBack(t *testing.T) {
	p, svc, scope, token := durableMaintenanceFixture(t)
	admitted := make(chan struct{})
	release := make(chan struct{})
	readDone := make(chan error, 1)
	go func() {
		readDone <- p.ReadTransaction(testCtx, func(ctx context.Context, tx Tx) error {
			if _, err := tx.Get(ctx, "sources", scope, "ctx-src"); err != nil {
				return err
			}
			close(admitted)
			<-release
			return nil
		})
	}()
	<-admitted
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, err := svc.BeginPrivacyMaintenance(testCtx, token, PrivacyDeletionRequest{Scope: scope, TargetType: "subject", TargetIDs: []string{scope.UserID}, IdempotencyKey: "drain", Reason: "owner_request"})
		done <- err
	}()
	<-started
	select {
	case err := <-done:
		close(release)
		t.Fatal("did not drain existing transaction", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	st, err := p.MemoryMaintenanceStatus(testCtx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 80 && st.Active; i++ {
		st, err = p.AdvanceMemoryMaintenance(testCtx, st.Revision)
		if err != nil {
			t.Fatal(err)
		}
	}
	if st.Active {
		t.Fatal(st)
	}
	if _, err = svc.BeginPrivacyMaintenance(testCtx, "invalid-token", PrivacyDeletionRequest{Scope: scope, TargetType: "subject", TargetIDs: []string{scope.UserID}, IdempotencyKey: "denied", Reason: "owner_request"}); err == nil {
		t.Fatal("invalid token began")
	}
	st, err = p.MemoryMaintenanceStatus(testCtx)
	if err != nil || st.Active {
		t.Fatal("failed begin left gate", st, err)
	}
}

func TestMaintenanceSupersedeResumesWithoutServingUnvisitedDigests(t *testing.T) {
	p, svc, scope, token := durableMaintenanceFixture(t)
	src, err := p.Get(testCtx, "sources", scope, "ctx-src")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"old-memory", "next-memory"} {
		row := Record{ID: id, Scope: scope, Kind: "decision", Status: statusActive, Revision: 1, Text: "synthetic decision", Sources: refs(src), SourceEventID: src.ID, ApprovedBy: scope.UserID}
		if err = p.Insert(testCtx, colMemories, row); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 205; i++ {
		row := Record{ID: fmt.Sprintf("digest-%04d", i), Scope: scope, Kind: digestKindTopic, Status: digestActive, Revision: 1, Text: "outdated derived summary", Digest: &DigestData{Members: []Ref{{Collection: colMemories, ID: "old-memory", Revision: 1}}}}
		if err = p.Insert(testCtx, colTopicDigests, row); err != nil {
			t.Fatal(err)
		}
	}
	st, err := svc.BeginSupersedeMaintenance(testCtx, token, "old-memory", SupersedeInput{Scope: scope, ExpectedRevision: 1, Reason: "updated decision", ReplacementMemoryID: "next-memory", ReplacementExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	st, err = p.AdvanceMemoryMaintenance(testCtx, st.Revision)
	if err != nil || st.Scanned != 100 {
		t.Fatal(st, err)
	}
	if _, err = p.Get(testCtx, colTopicDigests, scope, "digest-0204"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unvisited digest served", err)
	}
	if _, err = p.AdvanceMemoryMaintenance(testCtx, st.Revision-1); !errors.Is(err, ErrConflict) {
		t.Fatal("stale operator revision accepted", err)
	}
	fresh := &Postgres{pool: p.pool}
	for i := 0; i < 25 && st.Active; i++ {
		st, err = fresh.AdvanceMemoryMaintenance(testCtx, st.Revision)
		if err != nil {
			t.Fatal(err)
		}
	}
	if st.Active {
		t.Fatal("cascade did not complete", st)
	}
	for _, id := range []string{"digest-0000", "digest-0100", "digest-0204"} {
		r, err := p.Get(testCtx, colTopicDigests, scope, id)
		if err != nil || r.Status != digestStale || r.Text != "" || r.StaleMark == nil || r.Revision != 2 {
			t.Fatal("missing/double invalidation", id, r, err)
		}
	}
	old, err := p.Get(testCtx, colMemories, scope, "old-memory")
	if err != nil || old.Status != "superseded" {
		t.Fatal(old, err)
	}
	bound, err := p.WithToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bound.MemoryMaintenanceStatus(testCtx); !errors.Is(err, ErrDenied) {
		t.Fatal("token-bound admin accepted", err)
	}
}

func TestMaintenanceSourceAndTraceParityDeepChainAndSafeCancellation(t *testing.T) {
	for _, target := range []string{"source_event", "trace"} {
		t.Run(target, func(t *testing.T) {
			p, svc, scope, token := durableMaintenanceFixture(t)
			ids := []string{"ctx-src"}
			if target == "trace" {
				ids = []string{"trace-1"}
			}
			if target == "source_event" {
				prior := "ctx-src"
				for i := 0; i < 70; i++ {
					id := fmt.Sprintf("dependency-%03d", 69-i)
					if err := p.Insert(testCtx, "sources", Record{ID: id, Scope: scope, Kind: "document", Status: statusActive, Revision: 1}); err != nil {
						t.Fatal(err)
					}
					if err := p.insertGraphRelation(testCtx, scope, "source_event", id, "source_event", prior, "context_uses"); err != nil {
						t.Fatal(err)
					}
					prior = id
				}
			}
			var want PrivacyImpact
			if err := p.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
				var err error
				want, err = tx.(*Postgres).DiscoverPrivacyImpactComplete(ctx, scope, target, ids)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			req := PrivacyDeletionRequest{Scope: scope, TargetType: target, TargetIDs: ids, IdempotencyKey: "targeted-maintenance", Reason: "owner_request", RetentionPolicyRef: retentionSynthetic}
			st, err := svc.BeginPrivacyMaintenance(testCtx, token, req)
			if err != nil {
				t.Fatal(err)
			}
			st, err = p.AdvanceMemoryMaintenance(testCtx, st.Revision)
			if err != nil {
				t.Fatal(err)
			}
			st, err = p.CancelMemoryMaintenance(testCtx, st.Revision)
			if err != nil || st.Active {
				t.Fatal("safe discovery cancellation failed", st, err)
			}
			if _, err = p.Get(testCtx, "sources", scope, "ctx-src"); err != nil {
				t.Fatal("cancel did not restore original access", err)
			}
			st, err = svc.BeginPrivacyMaintenance(testCtx, token, req)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 500 && st.Active; i++ {
				if st.Phase == "quarantine_records" {
					if _, err = p.CancelMemoryMaintenance(testCtx, st.Revision); !errors.Is(err, ErrConflict) {
						t.Fatal("unsafe cancel allowed", err)
					}
				}
				st, err = p.AdvanceMemoryMaintenance(testCtx, st.Revision)
				if err != nil {
					t.Fatal(st, err)
				}
			}
			if st.Active {
				t.Fatal(st)
			}
			job, err := p.Get(testCtx, colJobs, scope, privacyJobID(scope, scope.UserID, req.IdempotencyKey))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(want, job.PrivacyDeletion.Impact) {
				t.Fatalf("targeted impact differs\nwant=%+v\ngot=%+v", want, job.PrivacyDeletion.Impact)
			}
		})
	}
}

func TestMaintenanceRejectsStaleTargetsAndExistingLegalHoldBeforeFreeze(t *testing.T) {
	p, svc, scope, token := durableMaintenanceFixture(t)
	req := PrivacyDeletionRequest{Scope: scope, TargetType: "source_event", TargetIDs: []string{"ctx-src"}, IdempotencyKey: "preflight", Reason: "owner_request", ExpectedRevision: 99}
	if _, err := svc.BeginPrivacyMaintenance(testCtx, token, req); !errors.Is(err, ErrConflict) {
		t.Fatal("stale target accepted", err)
	}
	status, err := p.MemoryMaintenanceStatus(testCtx)
	if err != nil || status.Active {
		t.Fatal("stale request froze service", status, err)
	}
	req.ExpectedRevision = 0
	if err = p.Insert(testCtx, colJobs, Record{ID: "existing-legal-hold", Scope: scope, Kind: privacyDeletionKind, Status: JobStatusDone, Revision: 1, PrivacyDeletion: &PrivacyDeletion{State: privacyHeld, LegalHoldStatus: privacyHoldHeld, TargetType: "source_event", Impact: PrivacyImpact{SourceEventIDs: []string{"ctx-src"}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.BeginPrivacyMaintenance(testCtx, token, req); !errors.Is(err, ErrConflict) {
		t.Fatal("held overlap accepted", err)
	}
	status, err = p.MemoryMaintenanceStatus(testCtx)
	if err != nil || status.Active {
		t.Fatal("held request froze service", status, err)
	}
}

func TestMaintenanceActualProcessResume(t *testing.T) {
	p, svc, scope, token := durableMaintenanceFixture(t)
	req := PrivacyDeletionRequest{Scope: scope, TargetType: "trace", TargetIDs: []string{"trace-1"}, IdempotencyKey: "process-resume", Reason: "owner_request", RetentionPolicyRef: retentionSynthetic}
	st, err := svc.BeginPrivacyMaintenance(testCtx, token, req)
	if err != nil {
		t.Fatal(err)
	}
	st, err = p.AdvanceMemoryMaintenance(testCtx, st.Revision)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(testCtx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMaintenanceResumeChild$", "-test.count=1")
	cfg := p.pool.Config().ConnConfig
	childDSN := url.URL{Scheme: "postgres", User: url.User(cfg.User), Host: net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))), Path: "/" + cfg.Database, RawQuery: "sslmode=disable"}
	if cfg.Password != "" {
		childDSN.User = url.UserPassword(cfg.User, cfg.Password)
	}
	cmd.Env = append(os.Environ(), "ARKISM_MAINTENANCE_CHILD_DSN="+childDSN.String())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fresh process resume failed: %v %s", err, output)
	}
	st, err = p.MemoryMaintenanceStatus(testCtx)
	if err != nil || st.Active {
		t.Fatal("fresh process did not complete", st, err)
	}
	row, err := p.Get(testCtx, colJobs, scope, privacyJobID(scope, scope.UserID, req.IdempotencyKey))
	if err != nil || row.PrivacyDeletion == nil || row.PrivacyDeletion.State != privacyQuarantined {
		t.Fatal("no published privacy job", err)
	}
}
func TestMaintenanceResumeChild(t *testing.T) {
	dsn := os.Getenv("ARKISM_MAINTENANCE_CHILD_DSN")
	if dsn == "" {
		t.Skip("subprocess-only helper")
	}
	parsed, parseErr := url.Parse(dsn)
	if parseErr != nil || !net.ParseIP(parsed.Hostname()).IsLoopback() || !strings.HasPrefix(parsed.Path, "/arkism_test_") {
		t.Fatal("requires isolated loopback test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	p, err := NewPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	// Do not initialize or modify the schema: restart reads the durable gate.
	st, err := p.MemoryMaintenanceStatus(ctx)
	if err != nil || !st.Active {
		t.Fatal("missing persisted operation", st, err)
	}
	for i := 0; i < 100 && st.Active; i++ {
		st, err = p.AdvanceMemoryMaintenance(ctx, st.Revision)
		if err != nil {
			t.Fatal(err)
		}
	}
	if st.Active {
		t.Fatal("resume incomplete", st)
	}
}
