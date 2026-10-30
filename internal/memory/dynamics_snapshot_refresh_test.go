package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func dynamicsRefreshFixture(t *testing.T) (*nativePGFixture, *NativeSearchShadowRuntime, SearchInput, DynamicsLoadConfig) {
	t.Helper()
	p, shadow, in := nativeSearchPGFixture(t)
	dynamicsUseLogin(t, p)
	cfg := dynamicsFixtureConfig()
	// Explicit process-startup fixture allowance, not a product or scientific default.
	cfg.Profile = "readonly-process-fixture-v1"
	cfg.Limits.AllowedFreshnessNS = int64(30 * time.Second)
	runtime := dynamicsFixtureRuntime(t, cfg)
	p.checker.DynamicsLoad = runtime
	p.worker.DynamicsLoad = runtime
	p.worker.NativeSearchShadow = shadow
	p.accept(t)
	return p, shadow, in, cfg
}
func dynamicsRefreshReader(t *testing.T, p *nativePGFixture, cfg DynamicsLoadConfig, search bool) (*Service, string) {
	t.Helper()
	token := testPrincipalToken("refresh-reader-" + ID())
	actions := []string{"db:read", "read:jobs", "read:memories", "read:evaluations", "read:sources", "read:execution_traces", "read:agent_runs"}
	if search {
		actions = append(actions, "db:write", "search:memories", "context:read")
	}
	if err := p.admin.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "refresh-reader-" + ID(), Role: roleService, Token: token, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: actions}); err != nil {
		t.Fatal(err)
	}
	repo, err := p.app.WithToken(token)
	if err != nil {
		t.Fatal(err)
	}
	return &Service{Repo: repo, Auth: p.worker.Auth, NativeDynamics: p.worker.NativeDynamics, DynamicsLoad: dynamicsFixtureRuntime(t, cfg)}, token
}

// Include every user column/row in every physical arkism base table, including
// ACL/principal/metadata/source tables outside memory_records. Never print rows.
func dynamicsRefreshDatabase(t *testing.T, p *Postgres) []string {
	t.Helper()
	tables, err := p.q().Query(testCtx, `SELECT table_name FROM information_schema.tables WHERE table_schema='arkism' AND table_type='BASE TABLE' ORDER BY table_name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var name string
		if err = tables.Scan(&name); err != nil {
			tables.Close()
			t.Fatal(err)
		}
		names = append(names, name)
	}
	tables.Close()
	if err = tables.Err(); err != nil {
		t.Fatal(err)
	}
	var result []string
	for _, name := range names {
		rows, e := p.q().Query(testCtx, "SELECT to_jsonb(r)::text FROM "+pgx.Identifier{"arkism", name}.Sanitize()+" r ORDER BY to_jsonb(r)::text")
		if e != nil {
			t.Fatal(e)
		}
		result = append(result, "table:"+name)
		for rows.Next() {
			var row string
			if e = rows.Scan(&row); e != nil {
				rows.Close()
				t.Fatal(e)
			}
			result = append(result, row)
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
	}
	return result
}
func dynamicsAssertRefreshUnchanged(t *testing.T, p *nativePGFixture, reader *Service, token string, wantSuccess bool) error {
	t.Helper()
	before := dynamicsRefreshDatabase(t, p.admin)
	err := reader.RefreshDynamicsSnapshot(testCtx, token, scopeA)
	if !reflect.DeepEqual(before, dynamicsRefreshDatabase(t, p.admin)) {
		t.Fatal("refresh changed a physical user row in canonical/metadata/ACL tables")
	}
	if wantSuccess && err != nil {
		t.Fatal("readonly refresh", err)
	}
	if !wantSuccess && err == nil {
		t.Fatal("ineligible publication refreshed")
	}
	return err
}
func TestDynamicsSnapshotRefreshWorkerProcess(t *testing.T) {
	raw := os.Getenv("SYS05_REFRESH_CHILD_CONFIG")
	if raw == "" {
		t.Log("subprocess helper; functional parent invokes this exact binary explicitly")
		return
	}
	dsn := os.Getenv("SYS05_REFRESH_CHILD_DSN")
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.ConnConfig.Database, "arkism_test_") || net.ParseIP(config.ConnConfig.Host) == nil || !net.ParseIP(config.ConnConfig.Host).IsLoopback() {
		t.Fatal("worker helper refused non-owned DB")
	}
	repo, err := NewPostgres(testCtx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	activeViewAssertLogin(t, repo, config.ConnConfig.User)
	token := os.Getenv("SYS05_REFRESH_CHILD_TOKEN")
	bound, err := repo.WithToken(token)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewPostgresAuthorizer(repo)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewDynamicsLoadRuntime([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Repo: bound, Auth: auth, NativeDynamics: &NativeDynamicsRuntime{Enabled: true}, DynamicsLoad: runtime}
	if os.Getenv("SYS05_CRASH_CHILD_ROLE") != "" {
		dynamicsCrashChild(t, svc, token, runtime.config.Scopes[0])
		return
	}
	if err = svc.WorkDynamicsOnce(testCtx, token); err != nil {
		t.Fatal(err)
	}
}
func dynamicsRefreshRunWorker(t *testing.T, p *nativePGFixture, cfg DynamicsLoadConfig) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "-test.run=^TestDynamicsSnapshotRefreshWorkerProcess$", "-test.count=1", "-test.timeout=20s")
	command.Env = dynamicsRefreshChildEnv(p, raw)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err = command.Run(); err != nil {
		t.Fatalf("owned worker test subprocess: %v %s", err, output.String())
	}
}
func dynamicsRefreshChildEnv(p *nativePGFixture, raw []byte) []string {
	return []string{"PATH=/usr/bin:/bin", "TZ=UTC", "SYS05_REFRESH_CHILD_CONFIG=" + string(raw), "SYS05_REFRESH_CHILD_DSN=" + sys04DatabaseDSN(p.app), "SYS05_REFRESH_CHILD_TOKEN=" + p.workerToken}
}
func TestDynamicsSnapshotRefreshSeparateWorkerHTTPPG(t *testing.T) {
	p, shadow, in, cfg := dynamicsRefreshFixture(t)
	dynamicsRefreshRunWorker(t, p, cfg)
	if _, err := p.worker.DynamicsLoad.DynamicsSnapshot(scopeA, dynamicsNowNS()); !errors.Is(err, ErrDynamicsSnapshotUnavailable) {
		t.Fatal("subprocess memory cache leaked into parent")
	}
	readonly, token := dynamicsRefreshReader(t, p, cfg, false)
	dynamicsAssertRefreshUnchanged(t, p, readonly, token, true)
	coordinator, err := p.admin.Get(testCtx, colJobs, scopeA, dynamicsCoordinatorID(scopeA))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := readonly.DynamicsLoad.DynamicsSnapshot(scopeA, dynamicsNowNS())
	if err != nil {
		t.Fatal(err)
	}
	cp := coordinator.DynamicsLoad
	if snapshot.Generation != cp.PublicationGeneration || snapshot.AsOfNS != cp.AsOfNS || snapshot.PublishedNS != cp.PublishedNS || snapshot.Head != cp.Head || snapshot.Tail != cp.Tail {
		t.Fatal("refresh advanced publication metadata")
	}
	// API caller can write its normal Search receipts, but has no write:jobs.
	api, apiToken := dynamicsRefreshReader(t, p, cfg, true)
	api.DynamicsLoad = readonly.DynamicsLoad
	api.NativeSearchShadow = shadow
	bundle := nativeSearchHTTP(t, api, apiToken, in)
	observations := shadow.Observations()
	if len(bundle.Items) != 1 || len(observations) != 1 || observations[0].SourceCount != 1 || observations[0].Status != "selected" {
		t.Fatal("actual HTTP Search did not consume separately-published refreshed cache", observations)
	}
	before, err := p.admin.Get(testCtx, colJobs, scopeA, coordinator.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = api.PublishDynamicsSnapshot(testCtx, apiToken, scopeA); !errors.Is(err, ErrDenied) {
		t.Fatal("readonly refresh authority became publication authority", err)
	}
	after, err := p.admin.Get(testCtx, colJobs, scopeA, coordinator.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("denied publisher changed coordinator", err)
	}
}
func TestDynamicsSnapshotRefreshFaultsPG(t *testing.T) {
	for _, fault := range []string{"no_publication", "missing_witness", "forged_witness", "head_tail", "state_revision", "withheld", "lease", "stale", "future", "size", "purge", "source_change", "delete", "restore", "pause"} {
		t.Run(fault, func(t *testing.T) {
			p, _, _, cfg := dynamicsRefreshFixture(t)
			if err := p.worker.WorkDynamicsOnce(testCtx, p.workerToken); err != nil {
				t.Fatal(err)
			}
			reader, token := dynamicsRefreshReader(t, p, cfg, false)
			dynamicsRefreshFault(t, p, fault)
			dynamicsAssertRefreshUnchanged(t, p, reader, token, false)
			if _, err := reader.DynamicsLoad.DynamicsSnapshot(scopeA, dynamicsNowNS()); !errors.Is(err, ErrDynamicsSnapshotUnavailable) {
				t.Fatal("invalid refresh populated local cache")
			}
		})
	}
}
func dynamicsRefreshFault(t *testing.T, p *nativePGFixture, fault string) {
	t.Helper()
	switch fault {
	case "state_revision", "size":
		dynamicsRefreshStateFault(t, p, fault)
	case "purge":
		if err := p.admin.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
			return invalidateNativeDynamicsDependencyTx(ctx, tx, scopeA, p.in.Target, "refresh_fault")
		}); err != nil {
			t.Fatal(err)
		}
	case "source_change":
		row, err := p.admin.Get(testCtx, "sources", scopeA, p.in.Evidence[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		row.Text += " altered"
		row.Revision++
		if err = p.admin.Replace(testCtx, "sources", row, row.Revision-1); err != nil {
			t.Fatal(err)
		}
	case "delete":
		fixture := newFixture(t)
		fixture.svc.Repo = p.admin
		if err := fixture.svc.Delete(testCtx, fixture.human, colMemories, p.in.Target.ID, ChangeInput{Scope: scopeA, ExpectedRevision: p.in.Target.Revision}); err != nil {
			t.Fatal(err)
		}
	case "restore":
		if err := p.admin.RequireRestoreReview(testCtx, ContentHash("refresh restore")); err != nil {
			t.Fatal(err)
		}
	case "pause":
		gate, err := p.admin.recoveryWriteGate(testCtx, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = p.admin.ManageRecoveryWrites(testCtx, "pause", gate.StorageIdentity, gate.Gate.Generation); err != nil {
			t.Fatal(err)
		}
	default:
		dynamicsRefreshCoordinatorFault(t, p, fault)
	}
}
func dynamicsRefreshCoordinatorFault(t *testing.T, p *nativePGFixture, fault string) {
	t.Helper()
	err := p.admin.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		row, e := dynamicsLockCoordinator(ctx, tx, scopeA)
		if e != nil {
			return e
		}
		cp := row.DynamicsLoad
		switch fault {
		case "no_publication":
			cp.PublicationGeneration = 0
		case "missing_witness":
			row.RequestHash = ""
		case "forged_witness":
			row.RequestHash = ContentHash("forged witness")
		case "head_tail":
			cp.Head++
			cp.Tail++
		case "withheld":
			cp.Withheld = true
		case "lease":
			cp.LeaseToken = strings.Repeat("f", 64)
			cp.LeaseUntilNS = dynamicsNowNS() + int64(time.Second)
		case "stale":
			cp.AsOfNS = dynamicsNowNS() - p.worker.DynamicsLoad.config.Limits.AllowedFreshnessNS - 1
		case "future":
			cp.PublishedNS = dynamicsNowNS() + int64(time.Hour)
		default:
			return ErrInvalid
		}
		return dynamicsReplace(ctx, tx, row)
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestDynamicsSnapshotRefreshWitnessAndLocalOrder(t *testing.T) {
	cfg := dynamicsFixtureConfig()
	runtime := dynamicsFixtureRuntime(t, cfg)
	now := dynamicsNowNS()
	states1, states2 := map[string]Record{}, map[string]Record{}
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("state-%d", i)
		states1[id] = Record{ID: id, Revision: int64(i + 1)}
	}
	for i := 2; i >= 0; i-- {
		id := fmt.Sprintf("state-%d", i)
		states2[id] = Record{ID: id, Revision: int64(i + 1)}
	}
	first := &DynamicsLoadSnapshot{Scope: scopeA, ConfigSHA256: runtime.hash, Generation: 2, AsOfNS: now, PublishedNS: now, States: states1}
	second := *first
	second.States = states2
	a, err := dynamicsSnapshotWitness(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := dynamicsSnapshotWitness(&second)
	if err != nil || a != b {
		t.Fatal("witness depends on map insertion order", err)
	}
	if !runtime.installRefreshedDynamicsSnapshot(first) || !runtime.installRefreshedDynamicsSnapshot(&second) {
		t.Fatal("equal immutable publication failed idempotent refresh")
	}
	stale := *first
	stale.Generation = 1
	if runtime.installRefreshedDynamicsSnapshot(&stale) {
		t.Fatal("old local writer replaced newer generation")
	}
	changed := *first
	changed.PublishedNS++
	if runtime.installRefreshedDynamicsSnapshot(&changed) {
		t.Fatal("different same-generation snapshot replaced immutable publication")
	}
}

func dynamicsRefreshStateFault(t *testing.T, p *nativePGFixture, fault string) {
	t.Helper()
	row, err := p.admin.Get(testCtx, colJobs, scopeA, nativeStateID(p.rows(t, "state")[0].NativeDynamics.Key))
	if err != nil {
		t.Fatal(err)
	}
	if fault == "size" {
		row.Summary = strings.Repeat("x", 70000)
	}
	row.Revision++
	if err = p.admin.Replace(testCtx, colJobs, row, row.Revision-1); err != nil {
		t.Fatal(err)
	}
}

func TestDynamicsSnapshotRefreshAuthorityPG(t *testing.T) {
	for _, fault := range []string{"missing_jobs", "missing_sources", "revoked"} {
		t.Run(fault, func(t *testing.T) { dynamicsRefreshAuthorityCase(t, fault) })
	}
}
func dynamicsRefreshAuthorityCase(t *testing.T, fault string) {
	t.Helper()
	p, _, _, cfg := dynamicsRefreshFixture(t)
	if err := p.worker.WorkDynamicsOnce(testCtx, p.workerToken); err != nil {
		t.Fatal(err)
	}
	token := testPrincipalToken("refresh-denied-" + ID())
	principal := "refresh-denied-" + ID()
	actions := []string{"db:read", "read:jobs"}
	if fault == "missing_jobs" {
		actions = []string{"db:read", "read:memories", "read:evaluations", "read:sources", "read:execution_traces", "read:agent_runs"}
	}
	if err := p.admin.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: principal, Role: roleService, Token: token, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: actions}); err != nil {
		t.Fatal(err)
	}
	if fault == "revoked" {
		if err := p.admin.SetPrincipalActive(testCtx, principal, false); err != nil {
			t.Fatal(err)
		}
	}
	repo, err := p.app.WithToken(token)
	if err != nil {
		t.Fatal(err)
	}
	reader := &Service{Repo: repo, Auth: p.worker.Auth, NativeDynamics: p.worker.NativeDynamics, DynamicsLoad: dynamicsFixtureRuntime(t, cfg)}
	err = dynamicsAssertRefreshUnchanged(t, p, reader, token, false)
	if !errors.Is(err, ErrDenied) {
		t.Fatal("missing read authority not denied", err)
	}
}

func TestDynamicsSnapshotRefreshCloneIdentityPG(t *testing.T) {
	p, _, _, cfg := dynamicsRefreshFixture(t)
	if err := p.worker.WorkDynamicsOnce(testCtx, p.workerToken); err != nil {
		t.Fatal(err)
	}
	_, token := dynamicsRefreshReader(t, p, cfg, false)
	target := isolatedPostgresSchema(t, false)
	sys04DumpAndRestore(t, p.admin, target)
	sqlConfig := p.app.pool.Config().Copy()
	sqlConfig.ConnConfig.Database = target.pool.Config().ConnConfig.Database
	pool, err := pgxpool.NewWithConfig(testCtx, sqlConfig)
	if err != nil {
		t.Fatal(err)
	}
	app := &Postgres{pool: pool, ownsPool: true}
	t.Cleanup(app.Close)
	activeViewAssertLogin(t, app, sqlConfig.ConnConfig.User)
	repo, err := app.WithToken(token)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	reader := &Service{Repo: repo, Auth: auth, NativeDynamics: p.worker.NativeDynamics, DynamicsLoad: dynamicsFixtureRuntime(t, cfg)}
	before := dynamicsRefreshDatabase(t, target)
	err = reader.RefreshDynamicsSnapshot(testCtx, token, scopeA)
	if !errors.Is(err, ErrDynamicsRuntimeBinding) {
		t.Fatal("actual clone identity mismatch not rejected as binding", err)
	}
	if !reflect.DeepEqual(before, dynamicsRefreshDatabase(t, target)) {
		t.Fatal("clone refresh mutated storage/binding")
	}
}
func TestDynamicsSnapshotRefreshLegacyRepublishAndRecoveryWitnessPG(t *testing.T) {
	p, _, _, cfg := dynamicsRefreshFixture(t)
	if err := p.worker.WorkDynamicsOnce(testCtx, p.workerToken); err != nil {
		t.Fatal(err)
	}
	reader, token := dynamicsRefreshReader(t, p, cfg, false)
	dynamicsRefreshCoordinatorFault(t, p, "missing_witness")
	dynamicsAssertRefreshUnchanged(t, p, reader, token, false)
	if err := reader.PublishDynamicsSnapshot(testCtx, token, scopeA); !errors.Is(err, ErrDenied) {
		t.Fatal("legacy refresh silently became write publisher", err)
	}
	time.Sleep(6 * time.Millisecond)
	if err := p.worker.PublishDynamicsSnapshot(testCtx, p.workerToken, scopeA); err != nil {
		t.Fatal(err)
	}
	dynamicsAssertRefreshUnchanged(t, p, reader, token, true)
	if err := p.admin.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		row, e := dynamicsLockCoordinator(ctx, tx, scopeA)
		if e != nil {
			return e
		}
		raw, e := storeMarshal(row)
		if e != nil {
			return e
		}
		first, e := dynamicsRecoveryQueueDigestTx(ctx, tx, scopeA, row.DynamicsLoad, string(raw))
		if e != nil {
			return e
		}
		row.RequestHash = ContentHash("different witness only; same revision and metadata")
		other, e := storeMarshal(row)
		if e != nil {
			return e
		}
		second, e := dynamicsRecoveryQueueDigestTx(ctx, tx, scopeA, row.DynamicsLoad, string(other))
		if e != nil {
			return e
		}
		if first == second {
			return ErrConflict
		}
		return nil
	}); err != nil {
		t.Fatal("recovery commitment failed to include exact witness field", err)
	}
}

type dynamicsRefreshBeforeFinal struct {
	*Postgres
	calls  int
	before func()
}

func (r *dynamicsRefreshBeforeFinal) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	r.calls++
	if r.calls == 2 {
		r.before()
	}
	return r.Postgres.Transaction(ctx, fn)
}
func TestDynamicsSnapshotRefreshConcurrentPublicationPG(t *testing.T) {
	p, _, _, cfg := dynamicsRefreshFixture(t)
	if err := p.worker.WorkDynamicsOnce(testCtx, p.workerToken); err != nil {
		t.Fatal(err)
	}
	reader, token := dynamicsRefreshReader(t, p, cfg, false)
	var afterExternal []string
	base := reader.Repo.(*Postgres)
	reader.Repo = &dynamicsRefreshBeforeFinal{Postgres: base, before: func() {
		time.Sleep(6 * time.Millisecond)
		if err := p.worker.PublishDynamicsSnapshot(testCtx, p.workerToken, scopeA); err != nil {
			t.Fatal(err)
		}
		afterExternal = dynamicsRefreshDatabase(t, p.admin)
	}}
	if err := reader.RefreshDynamicsSnapshot(testCtx, token, scopeA); !errors.Is(err, ErrDynamicsSnapshotUnavailable) {
		t.Fatal("stale preview accepted after another publication", err)
	}
	if !reflect.DeepEqual(afterExternal, dynamicsRefreshDatabase(t, p.admin)) {
		t.Fatal("stale refresh wrote after actual external publication")
	}
	if _, err := reader.DynamicsLoad.DynamicsSnapshot(scopeA, dynamicsNowNS()); !errors.Is(err, ErrDynamicsSnapshotUnavailable) {
		t.Fatal("stale writer installed a cache")
	}
	reader.Repo = base
	dynamicsAssertRefreshUnchanged(t, p, reader, token, true)
}

type dynamicsRefreshTokenRepo struct {
	*Postgres
	expected string
	observed int
}

func (r *dynamicsRefreshTokenRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	digest, bound := r.effectiveTokenHash(ctx)
	if !bound || digest != r.expected {
		return ErrDenied
	}
	r.observed++
	return r.Postgres.Transaction(ctx, fn)
}
func TestDynamicsSnapshotRefreshBindsReaderRLSIdentityPG(t *testing.T) {
	p, _, _, cfg := dynamicsRefreshFixture(t)
	if err := p.worker.WorkDynamicsOnce(testCtx, p.workerToken); err != nil {
		t.Fatal(err)
	}
	reader, token := dynamicsRefreshReader(t, p, cfg, false)
	// Deliberately carry the stronger worker repository binding: the refresh
	// must override SQL identity with the explicit read-only caller token.
	observed := &dynamicsRefreshTokenRepo{Postgres: p.workerRepo, expected: ContentHash(token)}
	reader.Repo = observed
	dynamicsAssertRefreshUnchanged(t, p, reader, token, true)
	if observed.observed != 2 {
		t.Fatal("both authoritative read transactions did not use reader identity", observed.observed)
	}
}
