package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/capture"
	"github.com/FelixBaek/arkism/internal/receiptchain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This fixture first proves that the older fixture is SET ROLE, then opens an
// actual restricted LOGIN connection. RESET ROLE cannot recover an admin session.
func activeViewLogin(t *testing.T, p *nativePGFixture) *Postgres {
	t.Helper()
	var current, session string
	var login, super, bypass bool
	if e := p.app.q().QueryRow(testCtx, `SELECT current_user,session_user,rolcanlogin,rolsuper,rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&current, &session, &login, &super, &bypass); e != nil {
		t.Fatal(e)
	}
	if current == session || login || super || bypass {
		t.Fatal("baseline is not expected NOLOGIN SET ROLE fixture")
	}
	role := "arkism_rls_test_" + ID()
	quoted := pgx.Identifier{role}.Sanitize()
	if _, e := p.admin.q().Exec(testCtx, "CREATE ROLE "+quoted+" LOGIN NOINHERIT NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION"); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if _, e := p.admin.q().Exec(context.Background(), "DROP OWNED BY "+quoted+"; DROP ROLE "+quoted); e != nil {
			t.Error(e)
		}
	})
	sql := "GRANT USAGE ON SCHEMA arkism,extensions TO " + quoted + `; GRANT SELECT ON memory_storage_metadata TO ` + quoted + `; GRANT SELECT,INSERT,UPDATE,DELETE ON memory_records,execution_traces,source_events,entities,relations TO ` + quoted + `; GRANT EXECUTE ON FUNCTION current_principal(),has_permission(text,text,text),has_any_permission(text),permitted_scopes(text),authorize_principal(text,text,text),reserve_candidate_quota(text,text,date,integer),artifact_gc_held(text),artifact_cleanup_pending(text,text,text,text,text,text) TO ` + quoted
	if _, e := p.admin.q().Exec(testCtx, sql); e != nil {
		t.Fatal(e)
	}
	cfg := p.admin.pool.Config().Copy()
	cfg.ConnConfig.User = role
	cfg.ConnConfig.Password = ""
	cfg.MaxConns = 1
	cfg.AfterConnect = nil
	pool, e := pgxpool.NewWithConfig(testCtx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	app := &Postgres{pool: pool, ownsPool: true}
	t.Cleanup(app.Close)
	activeViewAssertLogin(t, app, role)
	return app
}
func activeViewAssertLogin(t *testing.T, p *Postgres, role string) {
	t.Helper()
	var current, session string
	var login, super, bypass, inherit bool
	if e := p.q().QueryRow(testCtx, `SELECT current_user,session_user,rolcanlogin,rolsuper,rolbypassrls,rolinherit FROM pg_roles WHERE rolname=current_user`).Scan(&current, &session, &login, &super, &bypass, &inherit); e != nil {
		t.Fatal(e)
	}
	if current != role || session != role || !login || super || bypass || inherit {
		t.Fatal("not restricted LOGIN")
	}
	if _, e := p.q().Exec(testCtx, "RESET ROLE"); e != nil {
		t.Fatal(e)
	}
	if e := p.q().QueryRow(testCtx, "SELECT current_user,session_user").Scan(&current, &session); e != nil || current != role || session != role {
		t.Fatal("RESET ROLE escaped LOGIN", e)
	}
	if _, e := p.q().Exec(testCtx, "SET ROLE native_fixture_admin"); e == nil {
		t.Fatal("LOGIN escalated to own admin")
	}
	var forced int
	if e := p.q().QueryRow(testCtx, `SELECT count(*) FROM pg_class WHERE oid=ANY(ARRAY['arkism.memory_records'::regclass,'arkism.execution_traces'::regclass,'arkism.source_events'::regclass,'arkism.entities'::regclass,'arkism.relations'::regclass]) AND relrowsecurity AND relforcerowsecurity`).Scan(&forced); e != nil || forced != 5 {
		t.Fatal("FORCE RLS not active", e, forced)
	}
}

type activeViewFixture struct {
	f               *receiptChainPGFixture
	app             *Postgres
	svc             *Service
	shadow          *NativeSearchShadowRuntime
	spy             *productionViewHTTP
	states, consume string
}

func activeViewSetup(t *testing.T) *activeViewFixture {
	t.Helper()
	f := receiptChainPG(t)
	app := activeViewLogin(t, f.p)
	auth, e := NewPostgresAuthorizer(app)
	if e != nil {
		t.Fatal(e)
	}
	bound, e := app.WithToken(f.p.workerToken)
	if e != nil {
		t.Fatal(e)
	}
	svc := &Service{Repo: bound, Auth: auth, NativeDynamics: &NativeDynamicsRuntime{Enabled: true}}
	r := nativeSearchTestRuntime(t, "V3-P1-NOTIME")
	for _, target := range f.targets {
		if _, e := svc.ActivateNativeMemoryDynamics(testCtx, f.p.workerToken, NativeDynamicsActivationInput{Scope: f.p.in.Scope, Target: target, Dimension: r.config.Dimension, Config: nativeSearchMatchingConfig(t, r), RequestID: "active-" + target.ID}); e != nil {
			t.Fatal("activation", e)
		}
	}
	spy := &productionViewHTTP{}
	server := httptest.NewServer(spy.handler(Handler(svc)))
	t.Cleanup(server.Close)
	host.BaseURL = server.URL
	f.producer.BaseURL = server.URL
	t.Setenv("ARKISM_RECALL_BUDGET", "user=8:6000")
	return &activeViewFixture{f: f, app: app, svc: svc, shadow: r, spy: spy, states: nativeSearchHash("states", f.p.rows(t, "state")), consume: nativeSearchHash("consumption", f.p.rows(t, "consumption"))}
}
func (a *activeViewFixture) invoke(t *testing.T, sid string, view *capture.ContextViewRecall) (*receiptchain.Session, []Ref, []byte, error) {
	t.Helper()
	reader := capture.ReceiptReaderIdentity("receipt_fixture_host", sid)
	s, e := receiptchain.New(receiptchain.Scope(a.f.p.in.Scope), reader, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	out := &productionViewReader{receiptChainIndependentHost: receiptChainIndependentHost{reader: reader, baseURL: host.BaseURL, token: a.f.p.workerToken, client: &http.Client{Timeout: 3 * time.Second}, scope: a.f.p.in.Scope}}
	e = capture.HookWithReceiptChainView(map[string]any{"session_id": sid, "prompt": "receiptfixture viewcanary production invocation"}, "UserPromptSubmit", out, s, view)
	return s, out.refs, append([]byte{}, out.Bytes()...), e
}
func TestNativeSystemActiveViewTransportPG(t *testing.T) {
	a := activeViewSetup(t)
	scope := a.f.p.in.Scope
	target := a.f.targets[0]
	offID, onID := "off-"+ID(), "on-"+ID()
	for _, phase := range []string{"current", "next", "new_reader"} {
		t.Run(phase, func(t *testing.T) {
			offID, onID = activeViewPhase(t, a, phase, scope, target, offID, onID)
		})
	}
	activeViewPublished(t, a.shadow, true)
	if a.spy.search != 0 || a.spy.build != 6 {
		t.Fatal("extra Search or missing BuildContext", a.spy.search, a.spy.build)
	}
	activeViewUnchanged(t, a)
}
func activeViewSemanticUnknown(t *testing.T, s *receiptchain.Session) {
	t.Helper()
	for _, r := range s.Snapshot().Records {
		if r.ActualUse != nil || r.TaskSuccess != nil || r.CausalContribution != nil {
			t.Fatal("semantic verdict fabricated")
		}
	}
}
func activeViewPublished(t *testing.T, r *NativeSearchShadowRuntime, known bool) {
	t.Helper()
	obs := r.Observations()
	if len(obs) == 0 {
		t.Fatal("zero native observations")
	}
	for _, o := range obs {
		if known {
			if o.PublishedSHA == "" || o.SourceCount <= 0 || !o.BaselineReturned {
				t.Fatal("native publication unknown", o.Status, o.Reason, o.SourceCount)
			}
		} else if o.PublishedSHA != "" || o.SourceCount != 0 {
			t.Fatal("fenced native publication escaped")
		}
	}
}
func activeViewUnchanged(t *testing.T, a *activeViewFixture) {
	t.Helper()
	if a.states != nativeSearchHash("states", a.f.p.rows(t, "state")) || a.consume != nativeSearchHash("consumption", a.f.p.rows(t, "consumption")) {
		t.Fatal("receipt/shadow changed native learning")
	}
}

// The injector commits from an own admin connection BEFORE the selected native
// publisher transaction begins. Stack matching is test-only seam selection.
type activeViewBeforePublisher struct {
	Repository
	once   sync.Once
	before func()
	fired  atomic.Int64
}

func (r *activeViewBeforePublisher) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	pcs := make([]uintptr, 40)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		if strings.HasSuffix(frame.Function, ".(*Service).nativeSearchReadValues") {
			r.once.Do(func() { r.fired.Add(1); r.before() })
			break
		}
		if !more {
			break
		}
	}
	return r.Repository.Transaction(ctx, fn)
}
func TestNativeSystemActiveViewCommittedFencePG(t *testing.T) {
	for _, mode := range []string{"restore_marker", "native_pending", "pause"} {
		t.Run(mode, func(t *testing.T) { activeViewFence(t, mode) })
	}
}
func activeViewFence(t *testing.T, mode string) {
	t.Helper()
	a := activeViewSetup(t)
	_, _, referenceWire, referenceErr := a.invoke(t, "pre-fence-"+ID(), productionViewOption(a.f.p.in.Scope, a.f.targets[0]))
	if referenceErr != nil {
		t.Fatal("prefence baseline", referenceErr)
	}
	a.svc.NativeSearchShadow = a.shadow
	wrapper := &activeViewBeforePublisher{Repository: a.svc.Repo}
	key := ""
	wrapper.before = func() {
		switch mode {
		case "pause":
			sys04Pause(t, a.f.p.admin)
		case "restore_marker":
			key = "restore_review_pending"
		case "native_pending":
			key = nativeRecoveryPendingKey
		}
		if key != "" {
			if _, e := a.f.p.admin.q().Exec(testCtx, `INSERT INTO memory_storage_metadata(key,value)VALUES($1,$2)`, key, []byte(ContentHash("committed active view fence"))); e != nil {
				t.Error(e)
			}
		}
	}
	a.svc.Repo = wrapper
	s, refs, wire, e := a.invoke(t, "fenced-"+ID(), productionViewOption(a.f.p.in.Scope, a.f.targets[0]))
	if wrapper.fired.Load() != 1 {
		t.Fatal("publisher transaction seam not reached")
	}
	activeViewPublished(t, a.shadow, false)
	activeViewFencedBaseline(t, a, s, refs, wire, referenceWire, e)

	activeViewSemanticUnknown(t, s)
	activeViewUnchanged(t, a)
	if key != "" {
		var exists bool
		if e := a.f.p.admin.q().QueryRow(testCtx, `SELECT EXISTS(SELECT 1 FROM memory_storage_metadata WHERE key=$1)`, key).Scan(&exists); e != nil || !exists {
			t.Fatal("marker automatically cleared", e)
		}
	} else if gateStatus(t, a.f.p.admin).Gate.State != "paused" {
		t.Fatal("pause automatically cleared")
	}
	raw, _ := json.Marshal(s.Snapshot())
	if strings.Contains(string(raw), "BODY_CANARY") {
		t.Fatal("raw telemetry")
	}
}

func activeViewPhase(t *testing.T, a *activeViewFixture, phase string, scope Scope, target Ref, offID, onID string) (string, string) {
	t.Helper()
	view := productionViewOption(scope)
	if phase == "current" {
		view = productionViewOption(scope, target)
	}
	if phase == "new_reader" {
		offID, onID = "off-new-"+ID(), "on-new-"+ID()
	}
	a.svc.NativeSearchShadow = nil
	off, offRefs, offWire, e := a.invoke(t, offID, view)
	if e != nil {
		t.Fatal(e)
	}
	a.svc.NativeSearchShadow = a.shadow
	on, onRefs, onWire, e := a.invoke(t, onID, view)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(offWire, onWire) || structuredText(offRefs) != structuredText(onRefs) {
		t.Fatal("shadow changed actual returned baseline")
	}
	if phase == "current" {
		if productionHas(onRefs, target.ID) || len(onRefs) != 2 {
			t.Fatal("view excluded ref returned")
		}
		productionExcludedReceipt(t, on, target.ID)
	} else if !productionHas(onRefs, target.ID) {
		t.Fatal("one-shot view became persistent")
	}
	productionPublish(t, a.f, off)
	productionPublish(t, a.f, on)
	activeViewSemanticUnknown(t, on)
	return offID, onID
}

func activeViewFencedBaseline(t *testing.T, a *activeViewFixture, session *receiptchain.Session, refs []Ref, wire, referenceWire []byte, observedErr error) {
	t.Helper()
	a.svc.NativeSearchShadow = nil
	off, offRefs, offWire, offErr := a.invoke(t, "off-fenced-"+ID(), productionViewOption(a.f.p.in.Scope, a.f.targets[0]))
	if observedErr == nil && !bytes.Equal(wire, referenceWire) {
		t.Fatal("publisher fallback changed baseline bytes")
	}
	if observedErr != nil && len(wire) != 0 {
		t.Fatal("failed request emitted bytes")
	}
	if len(refs) == 0 {
		for _, r := range session.Snapshot().Records {
			if r.Stage == "source_read" || r.Stage == "reader_bytes_acknowledged" || r.Stage == "answer_observed" {
				t.Fatal("failed read became source-read success")
			}
		}
	}
	// This second request starts AFTER the marker; its entry check is a distinct
	// boundary from the first request whose retrieval preceded the commit.
	t.Logf("postcommit off entry: error=%t bytes=%d read_refs=%d", offErr != nil, len(offWire), len(offRefs))

	activeViewSemanticUnknown(t, off)
	// Ordinary source reads can remain legitimate under a native-only gate.
	// A rejected request/read remains unknown; no success is synthesized.
	t.Logf("fenced baseline: request_error=%t rendered_bytes=%d actual_read_refs=%d", observedErr != nil, len(wire), len(refs))
}
func (r *activeViewBeforePublisher) RequireRestoreReview(ctx context.Context, manifest string) error {
	return r.Repository.(restoreReviewStore).RequireRestoreReview(ctx, manifest)
}
func (r *activeViewBeforePublisher) CheckRestorePublication(ctx context.Context) error {
	return r.Repository.(restoreReviewStore).CheckRestorePublication(ctx)
}
