package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type dynamicsFunctionalLedger struct {
	RequestID   string `json:"request_id"`
	ReceivedNS  int64  `json:"received_ns"`
	CompletedNS int64  `json:"completed_ns"`
	ElapsedNS   int64  `json:"elapsed_ns"`
	Outcome     string `json:"outcome"`
}

func dynamicsUseLogin(t *testing.T, p *nativePGFixture) {
	t.Helper()
	login := activeViewLogin(t, p)
	var err error
	p.checkerRepo, err = login.WithToken(p.checkerToken)
	if err != nil {
		t.Fatal(err)
	}
	p.workerRepo, err = login.WithToken(p.workerToken)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewPostgresAuthorizer(login)
	if err != nil {
		t.Fatal(err)
	}
	p.app = login
	p.checker.Repo = p.checkerRepo
	p.worker.Repo = p.workerRepo
	p.checker.Auth = auth
	p.worker.Auth = auth
}
func dynamicsFunctionalSetup(t *testing.T) (*nativePGFixture, *NativeSearchShadowRuntime, SearchInput) {
	t.Helper()
	p, shadow, in := nativeSearchPGFixture(t)
	dynamicsUseLogin(t, p)
	runtime := dynamicsFixtureRuntime(t, dynamicsFixtureConfig())
	p.checker.DynamicsLoad = runtime
	p.worker.DynamicsLoad = runtime
	p.worker.NativeSearchShadow = shadow
	return p, shadow, in
}
func dynamicsAssertSelected(t *testing.T, p *nativePGFixture, shadow *NativeSearchShadowRuntime, in SearchInput) {
	t.Helper()
	bundle := nativeSearchHTTP(t, p.worker, p.workerToken, in)
	observations := shadow.Observations()
	if len(bundle.Items) != 1 || len(observations) < 1 {
		t.Fatal("actual HTTP search missing memory or observation")
	}
	last := observations[len(observations)-1]
	if last.Status != "selected" || last.SourceCount != 1 {
		t.Fatalf("immutable snapshot not actually consumed: %+v", last)
	}
}
func TestDynamicsLoadFunctionalHTTPMemoryFlowPG(t *testing.T) {
	p, shadow, in := dynamicsFunctionalSetup(t)
	var ledger []dynamicsFunctionalLedger
	for i := 0; i < 5; i++ {
		p.in.EventID = "functional-" + string(rune('a'+i))
		start := time.Now()
		received := start.UnixNano()
		_, err := p.checker.RecordMemoryOutcome(testCtx, p.checkerToken, p.in)
		outcome := "accepted_durable"
		if i == 4 {
			if !errors.Is(err, ErrDynamicsQueueFull) {
				t.Fatalf("expected explicit overload: %v", err)
			}
			outcome = "queue_full"
		} else if err != nil {
			t.Fatal(err)
		}
		ledger = append(ledger, dynamicsFunctionalLedger{RequestID: p.in.EventID, ReceivedNS: received, CompletedNS: time.Now().UnixNano(), ElapsedNS: time.Since(start).Nanoseconds(), Outcome: outcome})
	}
	for i := 0; i < 2; i++ {
		if err := p.worker.WorkDynamicsOnce(testCtx, p.workerToken); err != nil {
			t.Fatal(err)
		}
	}
	dynamicsAssertSelected(t, p, shadow, in)
	dynamicsAssertSelected(t, p, shadow, in)
	observation, err := p.worker.ObserveDynamicsLoad(testCtx, p.workerToken, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	if observation.QueueCount != 0 || observation.Head != 4 {
		t.Fatal("external accepted ledger not reconciled", observation)
	}
	recovery, err := p.worker.ReadDynamicsLoadRecovery(testCtx, p.workerToken, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.RepairStartNS != nil {
		t.Fatal("unknown historical repair origin manufactured")
	}
	if err = p.worker.VerifyDynamicsLoadRecovery(testCtx, p.workerToken, recovery); err != nil {
		t.Fatal(err)
	}
	altered := recovery
	altered.QueueDigest = ContentHash("wrong")
	if err = p.worker.VerifyDynamicsLoadRecovery(testCtx, p.workerToken, altered); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong recovery digest accepted", err)
	}
	dynamicsWriteFunctionalEvidence(t, p, ledger, observation)
	dynamicsFunctionalRestart(t, p, shadow, in)
}
func dynamicsFunctionalRestart(t *testing.T, p *nativePGFixture, shadow *NativeSearchShadowRuntime, in SearchInput) {
	t.Helper()
	var err error
	// Drop only the process cache on restart: canonical source/queue survives,
	// serving falls back until a fresh authoritative rebuild is complete.
	p.worker.DynamicsLoad = dynamicsFixtureRuntime(t, dynamicsFixtureConfig())
	nativeSearchHTTP(t, p.worker, p.workerToken, in)
	latest := shadow.Observations()
	if latest[len(latest)-1].Status == "selected" {
		t.Fatal("cold restart served stale process snapshot")
	}
	// Publishing may be rate limited by the configured interval; wait via a
	// bounded actual-clock deadline, rather than advance a synthetic clock.
	deadline := time.Now().Add(time.Second)
	for {
		if err = p.worker.PublishDynamicsSnapshot(testCtx, p.workerToken, scopeA); err != nil {
			t.Fatal(err)
		}
		if _, err = p.worker.DynamicsLoad.DynamicsSnapshot(scopeA, dynamicsNowNS()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cold rebuild not published")
		}
		time.Sleep(time.Millisecond)
	}
	dynamicsAssertSelected(t, p, shadow, in)
}

func dynamicsWriteFunctionalEvidence(t *testing.T, p *nativePGFixture, ledger []dynamicsFunctionalLedger, observation DynamicsLoadObservation) {
	t.Helper()
	var categories []struct {
		Kind  string `json:"kind"`
		Bytes int64  `json:"bytes"`
	}
	rows, err := p.admin.q().Query(testCtx, `SELECT CASE WHEN collection='jobs' THEN kind ELSE collection END,COALESCE(SUM(octet_length(payload)),0) FROM memory_records GROUP BY 1 ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var category struct {
			Kind  string `json:"kind"`
			Bytes int64  `json:"bytes"`
		}
		if err = rows.Scan(&category.Kind, &category.Bytes); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		categories = append(categories, category)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	report := struct {
		Scope    string                     `json:"scope"`
		Requests []dynamicsFunctionalLedger `json:"requests"`
		Queue    DynamicsLoadObservation    `json:"queue"`
		Storage  any                        `json:"storage_payload_bytes_by_kind"`
		Limits   string                     `json:"limits"`
	}{"actual_restricted_LOGIN_PG_in_process_HTTP_synthetic_functional_not_performance_comparison", ledger, observation, categories, "received5=accepted_durable4+rejected1; canonical bytes retained; client visibility means HTTP observed source eligibility, not downstream task success; no model calls; RSS/DBphysical/WAL/replay cost not measured here"}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(raw)) > p.worker.DynamicsLoad.config.Limits.TelemetryMaxBytes {
		t.Fatal("bodyfree telemetry budget exceeded")
	}
	root := os.Getenv("SYS04_TEST_EVIDENCE_DIR")
	if root == "" {
		root = t.TempDir()
	}
	if err = os.WriteFile(filepath.Join(root, "sys05-functional-http-ledger.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestDynamicsLoadFunctionalCorrectionAndDeletePG(t *testing.T) {
	for _, operation := range []string{"correction", "delete"} {
		t.Run(operation, func(t *testing.T) {
			p, shadow, in := dynamicsFunctionalSetup(t)
			p.accept(t)
			if err := p.worker.WorkDynamicsOnce(testCtx, p.workerToken); err != nil {
				t.Fatal(err)
			}
			dynamicsAssertSelected(t, p, shadow, in)
			fixture := newFixture(t)
			fixture.svc.Repo = p.admin
			if operation == "delete" {
				if err := fixture.svc.Delete(testCtx, fixture.human, colMemories, p.in.Target.ID, ChangeInput{Scope: scopeA, ExpectedRevision: p.in.Target.Revision}); err != nil {
					t.Fatal("actual human delete", err)
				}
			} else {
				dynamicsHumanCorrection(t, p, fixture)
			}
			nativeSearchHTTP(t, p.worker, p.workerToken, in)
			observations := shadow.Observations()
			last := observations[len(observations)-1]
			if last.SourceCount > 0 {
				t.Fatalf("old cached memory survived %s: %+v", operation, last)
			}
			if err := p.worker.PublishDynamicsSnapshot(testCtx, p.workerToken, scopeA); !errors.Is(err, ErrDynamicsSnapshotUnavailable) {
				t.Fatalf("published through pending repair: %v", err)
			}
		})
	}
}
func dynamicsHumanCorrection(t *testing.T, p *nativePGFixture, fixture *fixture) {
	t.Helper()
	candidate := fixture.create(fixture.agent, "memory_candidates", generatedCandidate(scopeA, refs(fixture.source())))
	marked, err := fixture.svc.Conflict(testCtx, fixture.human, ConflictInput{Scope: scopeA, Memories: []Ref{p.in.Target, {Collection: "memory_candidates", ID: candidate.ID, Revision: candidate.Revision}}, Action: "mark", Reason: "synthetic load correction", Hold: "second"})
	if err != nil {
		t.Fatal(err)
	}
	old := p.in.Target
	for _, row := range marked.Memories {
		if row.ID == old.ID {
			old.Revision = row.Revision
		} else {
			candidate = row
		}
	}
	_, err = fixture.svc.ReviewCandidate(testCtx, fixture.human, candidate.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: candidate.Revision, Action: "approve", OperationID: "load-confirm-correction", Conflict: &CandidateReviewConflict{ID: marked.Relation.ID, ExpectedRevision: marked.Relation.Revision, Memory: old, Resolution: "replace_old", Reason: "synthetic human approved"}})
	if err != nil {
		t.Fatal("actual human correction", err)
	}
}
func TestDynamicsLoadFunctionalWorkOnceFairnessPG(t *testing.T) {
	p, svc, fixture, token := dynamicsFairnessSetup(t)
	if err := svc.WorkOnce(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	privacy, err := p.admin.Get(testCtx, colJobs, scopeA, fixture.nextID)
	if err != nil {
		t.Fatal(err)
	}
	if privacy.PrivacyDeletion.State != privacyVerifying {
		t.Fatal("native work starved privacy")
	}
	done := 0
	for _, row := range pgCollection(t, p.admin, colJobs) {
		if row.Kind == "embedding" && row.Status == JobStatusDone {
			done++
		}
	}
	if done < 1 {
		t.Fatal("native work starved embedding")
	}
	observation, err := p.worker.ObserveDynamicsLoad(testCtx, p.workerToken, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	if observation.QueueCount != 0 {
		t.Fatal("privacy/embedding starved native", observation)
	}
}

// dynamicsFairnessSetup shares the real WorkOnce between existing privacy and
// embedding work and one queued native update.
func dynamicsFairnessSetup(t *testing.T) (*nativePGFixture, *Service, *privacyAdvanceRepo, string) {
	t.Helper()
	p := dynamicsPG(t)
	// Keep the unrelated initial live-memory embedding outside this round. The
	// two due stale-target jobs below exercise real lease/finish without changing
	// the native source proof or calling a provider.
	for _, row := range pgCollection(t, p.admin, colJobs) {
		if row.Kind != "embedding" {
			continue
		}
		due := time.Now().Add(time.Hour)
		row.RunAt = &due
		row.Revision++
		if err := p.admin.Replace(testCtx, colJobs, row, row.Revision-1); err != nil {
			t.Fatal(err)
		}
	}
	p.accept(t)
	svc, fixture, token := privacyWorkerFixture(t)
	for _, row := range fixture.rows(colJobs) {
		if err := p.admin.Insert(testCtx, colJobs, row); err != nil {
			t.Fatal(err)
		}
	}
	// Existing privacy/embedding and native work share the real WorkOnce. The
	// synthetic authorizer grants both maintenance roles; no provider is called.
	svc.Repo = p.admin
	svc.Embedder = newFixture(t).emb
	svc.DynamicsLoad = p.worker.DynamicsLoad
	svc.NativeDynamics = p.worker.NativeDynamics
	fake := svc.Auth.(*fakeAuth)
	fake.actors[token] = actor("load-worker", roleService, true, append(append([]string{}, allVerbs...), "purge")...)
	return p, svc, fixture, token
}

func TestDynamicsLoadFunctionalQueueRepairWithholdsPG(t *testing.T) {
	p, shadow, in := dynamicsFunctionalSetup(t)
	p.accept(t)
	if err := p.worker.WorkDynamicsOnce(testCtx, p.workerToken); err != nil {
		t.Fatal(err)
	}
	dynamicsAssertSelected(t, p, shadow, in)
	if err := p.admin.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		row, e := dynamicsLockCoordinator(ctx, tx, scopeA)
		if e != nil {
			return e
		}
		row.DynamicsLoad.Withheld = true
		// No historical repair origin is known. It stays nil, not ChangedAt/now.
		return dynamicsReplace(ctx, tx, row)
	}); err != nil {
		t.Fatal(err)
	}
	nativeSearchHTTP(t, p.worker, p.workerToken, in)
	observations := shadow.Observations()
	last := observations[len(observations)-1]
	if last.SourceCount != 0 {
		t.Fatal("queue repair marker served old native values", last)
	}
	observation, err := p.worker.ObserveDynamicsLoad(testCtx, p.workerToken, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Withheld || observation.RepairStartNS != nil {
		t.Fatal("repair marker/origin changed", observation)
	}
	if err = p.worker.PublishDynamicsSnapshot(testCtx, p.workerToken, scopeA); !errors.Is(err, ErrDynamicsSnapshotUnavailable) {
		t.Fatal("repair republish bypass", err)
	}
}

// The explicit repair is refused to read-only authority, during a live lease
// and when nothing is withheld. It clears an expired lease without reviving its
// claim, queued work resumes after it, and an unknown origin closes with no interval.
func TestDynamicsLoadRepairRepublishPG(t *testing.T) {
	p, shadow, in := dynamicsFunctionalSetup(t)
	p.accept(t)
	claim, err := p.worker.claimDynamics(testCtx, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	dynamicsEditCoordinator(t, p, func(d *DynamicsLoadPayload) { d.Withheld = true })
	if err = p.worker.RepairDynamicsPublication(testCtx, p.workerToken, scopeA); !errors.Is(err, ErrDynamicsSnapshotUnavailable) {
		t.Fatal("repair during a live lease", err)
	}
	dynamicsEditCoordinator(t, p, func(d *DynamicsLoadPayload) { d.LeaseUntilNS = dynamicsNowNS() - 1 })
	if err = p.worker.WorkDynamicsOnce(testCtx, p.workerToken); !errors.Is(err, ErrDynamicsRuntimeBinding) {
		t.Fatal("worker claimed while withheld", err)
	}
	if c := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad; !c.Withheld || c.Count != 1 {
		t.Fatal("queued work lost while withheld", c)
	}
	before := dynamicsRebindCoordinator(t, p.admin)
	dynamicsAssertRepairDenied(t, p, before)
	if err = p.worker.RepairDynamicsPublication(testCtx, p.workerToken, scopeA); err != nil {
		t.Fatal("repair with queued work", err)
	}
	observation, err := p.worker.ObserveDynamicsLoad(testCtx, p.workerToken, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Withheld || observation.RepairStartNS != nil || observation.LastRepairStartNS != nil || observation.LastRepairEndNS != nil || observation.QueueCount != 1 || observation.PublicationGeneration != before.DynamicsLoad.PublicationGeneration {
		t.Fatal("repair before any apply invented an interval, dropped work or published", observation)
	}
	if c := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad; c.LeaseToken != "" || c.LeaseUntilNS != 0 {
		t.Fatal("expired lease kept after repair", c)
	}
	if err = p.worker.applyDynamicsClaim(testCtx, p.workerToken, scopeA, claim); !errors.Is(err, ErrConflict) {
		t.Fatal("expired claim applied after repair", err)
	}
	if err = p.worker.WorkDynamicsOnce(testCtx, p.workerToken); err != nil {
		t.Fatal("queued work did not resume", err)
	}
	if c := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad; c.Count != 0 || c.Withheld {
		t.Fatal("resumed work not applied", c)
	}
	dynamicsAssertSelected(t, p, shadow, in)
	if err = p.worker.RepairDynamicsPublication(testCtx, p.workerToken, scopeA); !errors.Is(err, ErrConflict) {
		t.Fatal("repair without a hold", err)
	}
}

func dynamicsEditCoordinator(t *testing.T, p *nativePGFixture, edit func(*DynamicsLoadPayload)) {
	t.Helper()
	if err := p.admin.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		row, e := dynamicsLockCoordinator(ctx, tx, scopeA)
		if e != nil {
			return e
		}
		edit(row.DynamicsLoad)
		return dynamicsReplace(ctx, tx, row)
	}); err != nil {
		t.Fatal(err)
	}
}

// A read-only principal on the same bound runtime is denied and writes nothing.
func dynamicsAssertRepairDenied(t *testing.T, p *nativePGFixture, before Record) {
	t.Helper()
	reader, token := dynamicsRefreshReader(t, p, dynamicsFixtureConfig(), false)
	reader.DynamicsLoad = p.worker.DynamicsLoad
	if err := reader.RepairDynamicsPublication(testCtx, token, scopeA); !errors.Is(err, ErrDenied) {
		t.Fatal("read-only repair not denied", err)
	}
	if after := dynamicsRebindCoordinator(t, p.admin); after.Revision != before.Revision || !after.DynamicsLoad.Withheld {
		t.Fatal("denied repair wrote", after.DynamicsLoad)
	}
}
