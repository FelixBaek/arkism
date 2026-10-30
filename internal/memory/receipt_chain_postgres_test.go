package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/FelixBaek/arkism/internal/crewrun"
	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/capture"
	"github.com/FelixBaek/arkism/internal/receiptchain"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type receiptChainIndependentHost struct {
	bytes.Buffer
	reader           string
	baseURL, token   string
	client           *http.Client
	scope            Scope
	directCandidates []Ref
}

func (r *receiptChainIndependentHost) ReceiptAcknowledgement(cycle string) (capture.ReceiptAcknowledgement, error) {
	var envelope struct {
		Hook struct {
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if json.Unmarshal(r.Bytes(), &envelope) != nil {
		return capture.ReceiptAcknowledgement{}, receiptchain.ErrInvalid
	}
	refs := regexp.MustCompile(`\(id: ([^)]+)\)`).FindAllStringSubmatch(envelope.Hook.Context, -1)
	if len(refs) != 2 {
		return capture.ReceiptAcknowledgement{}, receiptchain.ErrIncomplete
	}
	reads := []capture.ReceiptSourceRead{}
	parts := []string{}
	seen := map[string]bool{}
	for _, ref := range refs {
		read, e := r.readMemory(ref[1], "prompt")
		if e != nil {
			return capture.ReceiptAcknowledgement{}, e
		}
		reads = append(reads, read)
		parts = append(parts, read.ResponseSHA256)
		seen[ref[1]] = true
	}
	for _, ref := range r.directCandidates {
		if !seen[ref.ID] {
			read, e := r.readMemory(ref.ID, "direct")
			if e != nil {
				return capture.ReceiptAcknowledgement{}, e
			}
			reads = append(reads, read)
			parts = append(parts, read.ResponseSHA256)
			break
		}
	}
	answer := []byte("independent-reader-result:" + receiptchain.Hash([]byte(strings.Join(parts, ":"))))
	return capture.ReceiptAcknowledgement{CycleID: cycle, ReaderSHA256: r.reader, InputSHA256: receiptchain.Hash(r.Bytes()), Reads: reads, Answer: answer}, nil
}
func (r *receiptChainIndependentHost) readMemory(id, path string) (capture.ReceiptSourceRead, error) {
	query := url.Values{"workspace_id": {r.scope.WorkspaceID}, "project_id": {r.scope.ProjectID}, "user_id": {r.scope.UserID}, "agent_id": {r.scope.AgentID}}
	req, e := http.NewRequest("GET", r.baseURL+"/v1/memories/"+url.PathEscape(id)+"?"+query.Encode(), nil)
	if e != nil {
		return capture.ReceiptSourceRead{}, e
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	response, e := r.client.Do(req)
	if e != nil {
		return capture.ReceiptSourceRead{}, e
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	closeErr := response.Body.Close()
	if e != nil {
		return capture.ReceiptSourceRead{}, e
	}
	if closeErr != nil {
		return capture.ReceiptSourceRead{}, closeErr
	}
	if response.StatusCode != 200 {
		return capture.ReceiptSourceRead{}, receiptchain.ErrIncomplete
	}
	var memory Record
	if json.Unmarshal(raw, &memory) != nil || memory.Scope != r.scope || memory.ID != id {
		return capture.ReceiptSourceRead{}, receiptchain.ErrIncomplete
	}
	hash := receiptchain.Hash([]byte(memory.Text))
	wire := receiptchain.Hash(raw)
	at := time.Now().UnixNano()
	return capture.ReceiptSourceRead{Path: path, Source: receiptchain.Source{Scope: receiptchain.Scope(memory.Scope), Ref: receiptchain.Ref{Collection: colMemories, ID: memory.ID, Revision: memory.Revision}, ContentSHA256: &hash, ProvenanceSHA256: &wire, Form: "full"}, ResponseSHA256: wire, ObservedAtNS: &at}, nil
}

type receiptChainFaultRepo struct {
	Repository
	fail atomic.Bool
}

func (r *receiptChainFaultRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if e := fn(ctx, tx); e != nil {
			return e
		}
		if r.fail.CompareAndSwap(true, false) {
			return ErrUnavailable
		}
		return nil
	})
}

type receiptChainTransport struct {
	next http.RoundTripper
	lose atomic.Bool
}

func (r *receiptChainTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, e := r.next.RoundTrip(req)
	if e == nil && req.Method == "POST" && strings.Contains(req.URL.Path, "execution_traces") && r.lose.CompareAndSwap(true, false) {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return nil, io.ErrUnexpectedEOF
	}
	return response, e
}

type receiptChainPGFixture struct {
	p        *nativePGFixture
	session  *receiptchain.Session
	producer *crewrun.Client
	repo     *receiptChainFaultRepo
	targets  []Ref
	spool    string
}

func receiptChainPG(t *testing.T) *receiptChainPGFixture {
	return receiptChainPGWithDirect(t, false)
}
func receiptChainPGWithDirect(t *testing.T, direct bool) *receiptChainPGFixture {
	t.Helper()
	root := os.Getenv("RECEIPT_CHAIN_OWN_ROOT")
	if root == "" {
		t.Skip("own host fixture root and disposable PG required")
	}
	if host.CaptureState != filepath.Join(root, "capture") {
		t.Fatal("host state is not explicit own fixture")
	}
	p := nativePGSetup(t)
	var appRole string
	if e := p.app.pool.QueryRow(testCtx, "SELECT current_user").Scan(&appRole); e != nil {
		t.Fatal(e)
	}
	if e := func() error {
		_, e := p.admin.q().Exec(testCtx, "GRANT SELECT,INSERT,UPDATE,DELETE ON execution_traces,source_events,entities,relations TO "+pgx.Identifier{appRole}.Sanitize())
		return e
	}(); e != nil {
		t.Fatal(e)
	}
	scope := p.in.Scope
	if e := p.admin.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "native-pg-worker", Role: roleService, Token: p.workerToken, OrganizationID: scope.WorkspaceID, ProjectID: scope.ProjectID, Actions: []string{"db:read", "db:write", "read:jobs", "write:jobs", "read:memories", "read:sources", "read:evaluations", "read:agent_runs", "read:execution_traces", "search:memories", "context:read"}}); e != nil {
		t.Fatal(e)
	}
	producerToken := testPrincipalToken("receipt-producer-" + ID())
	if e := p.admin.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "receipt-chain-producer", Role: roleService, Token: producerToken, OrganizationID: scope.WorkspaceID, ProjectID: scope.ProjectID, Actions: []string{"db:read", "db:write", "write:execution_traces", "write:agent_runs", "write:sources", "read:execution_traces", "read:agent_runs", "read:sources"}}); e != nil {
		t.Fatal(e)
	}
	targets := receiptChainMemories(t, p.admin)
	repo := &receiptChainFaultRepo{Repository: p.app}
	server := httptest.NewServer(Handler(&Service{Repo: repo, Auth: p.worker.Auth}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	workerFile, producerFile := filepath.Join(dir, "worker.token"), filepath.Join(dir, "producer.token")
	if e := os.WriteFile(workerFile, []byte(p.workerToken), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(producerFile, []byte(producerToken), 0600); e != nil {
		t.Fatal(e)
	}
	oldURL, oldToken := host.BaseURL, host.TokenFile
	t.Cleanup(func() { host.BaseURL, host.TokenFile = oldURL, oldToken })
	host.BaseURL, host.TokenFile = server.URL, workerFile
	for k, v := range map[string]string{"ARKISM_WORKSPACE": scope.WorkspaceID, "ARKISM_PROJECT": scope.ProjectID, "ARKISM_USER": scope.UserID, "ARKISM_AGENT": scope.AgentID, "ARKISM_RECALL_MIN_SCORE": "0", "ARKISM_RECALL_BUDGET": "user=2:1500", "ARKISM_WORK_ID": "", "ARKISM_REPO": "", "ARKISM_MEMORY_OUTCOME_SPOOL": ""} {
		t.Setenv(k, v)
	}
	sid := "receipt-reader-" + ID()
	reader := capture.ReceiptReaderIdentity("receipt_fixture_host", sid)
	session, e := receiptchain.New(receiptchain.Scope(scope), reader, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	consumer := &receiptChainIndependentHost{reader: reader, baseURL: server.URL, token: p.workerToken, client: &http.Client{Transport: server.Client().Transport, Timeout: 2 * time.Second}, scope: scope}
	if direct {
		consumer.directCandidates = targets
	}
	if e = capture.HookWithReceiptChain(map[string]any{"session_id": sid, "prompt": "receiptfixture context boundary"}, "UserPromptSubmit", consumer, session); e != nil {
		t.Fatal(e)
	}
	snap := session.Snapshot()
	if !snap.Complete || len(snap.Records) != map[bool]int{false: 7, true: 9}[direct] {
		t.Fatal("actual hook evidence incomplete", snap.Issues, len(snap.Records))
	}
	assertReceiptChainPGSnapshot(t, p, scope, snap)
	producer := &crewrun.Client{BaseURL: server.URL, TokenFile: producerFile, Scope: crewrun.Scope(scope), HTTP: server.Client()}
	return &receiptChainPGFixture{p, session, producer, repo, targets, filepath.Join(dir, "spool")}
}
func TestReceiptChainPostgresActualEndToEnd(t *testing.T) {
	f := receiptChainPG(t)
	before := len(pgCollection(t, f.p.admin, "execution_traces"))
	out, e := f.producer.PublishReceiptChain(testCtx, f.session, f.spool, receiptChainAgent(f.p.in.Scope))
	if e != nil {
		t.Fatal(e)
	}
	if out.Stored != "observed" || out.Readback != "observed" || out.Spooled != "observed" || len(out.Receipts) != 1 {
		t.Fatal(out)
	}
	if _, e = f.producer.PublishReceiptChain(testCtx, f.session, f.spool, receiptChainAgent(f.p.in.Scope)); e != nil {
		t.Fatal(e)
	}
	if len(pgCollection(t, f.p.admin, "execution_traces")) != before+1 {
		t.Fatal("duplicate trace")
	}
	files, e := filepath.Glob(filepath.Join(f.spool, "*.json"))
	if e != nil || len(files) != 1 {
		t.Fatal("duplicate or missing spool")
	}
	for _, ref := range out.Receipts[0].SourceEvents {
		source, e := f.p.admin.Get(testCtx, "sources", f.p.in.Scope, ref.ID)
		if e != nil || strings.Contains(source.Text, "BODY_CANARY") || strings.Contains(source.Text, "context boundary") || strings.Contains(source.Text, "independent local host answer") {
			t.Fatal("raw telemetry", e)
		}
	}
	receipt := out.Receipts[0]
	evidence := []Ref{}
	for _, ref := range receipt.SourceEvents {
		evidence = append(evidence, Ref{ref.Collection, ref.ID, ref.Revision})
	}
	_, e = f.p.checker.RecordMemoryOutcome(testCtx, f.p.checkerToken, MemoryOutcomeInput{Scope: f.p.in.Scope, Target: f.targets[0], Trace: Ref{receipt.Trace.Collection, receipt.Trace.ID, receipt.Trace.Revision}, Evidence: evidence, EventID: "receipt-does-not-qualify", CheckerSHA256: strings.Repeat("a", 64), Dimension: "work_task", Passed: true})
	if e == nil {
		t.Fatal("delivery chain qualified memory outcome")
	}
	if len(f.p.rows(t, "consumption")) != 0 || len(f.p.rows(t, "state")) != 0 {
		t.Fatal("receipt chain wrote native learning")
	}
}
func TestReceiptChainPostgresCommitAndLostReply(t *testing.T) {
	for _, fault := range []string{"before_commit", "lost_reply", "spool_failure"} {
		t.Run(fault, func(t *testing.T) {
			receiptChainCheckFault(t, fault)
		})
	}
}

func receiptChainAgent(scope Scope) string {
	if scope.AgentID != "" {
		return scope.AgentID
	}
	return "receipt_fixture_host"
}

func TestReceiptChainPostgresDurableCycleConflict(t *testing.T) {
	f := receiptChainPG(t)
	if _, e := f.producer.PublishReceiptChain(testCtx, f.session, "", receiptChainAgent(f.p.in.Scope)); e != nil {
		t.Fatal(e)
	}
	old := f.session.Snapshot().Records[0]
	other, e := receiptchain.NewCycle(f.session.Scope(), f.session.ReaderSHA256(), f.session.RunRef(), f.session.CycleID(), nil)
	if e != nil {
		t.Fatal(e)
	}
	changed := receiptchain.Hash([]byte("different transport bytes"))
	old.BytesSHA256 = &changed
	if _, e = other.Record(old); e != nil {
		t.Fatal(e)
	}
	before := len(pgCollection(t, f.p.admin, "execution_traces"))
	if _, e = f.producer.PublishReceiptChain(testCtx, other, "", receiptChainAgent(f.p.in.Scope)); e == nil {
		t.Fatal("same durable cycle different payload accepted")
	}
	if len(pgCollection(t, f.p.admin, "execution_traces")) != before {
		t.Fatal("conflict created new trace")
	}
}

func receiptChainMemories(t *testing.T, admin *Postgres) []Ref {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = admin
	targets := []Ref{}
	for _, label := range []string{"one", "two", "three"} {
		m := f.activeMemory("receiptfixture BODY_CANARY "+label+strings.Repeat(" payload unit", 20), "receiptfixture")
		m.Kind = "user"
		if e := admin.Replace(testCtx, colMemories, m, m.Revision); e != nil {
			t.Fatal(e)
		}
		targets = append(targets, Ref{colMemories, m.ID, m.Revision})
	}
	return targets
}

func assertReceiptChainPGSnapshot(t *testing.T, p *nativePGFixture, scope Scope, snap receiptchain.Snapshot) {
	t.Helper()
	for _, r := range snap.Records {
		if r.Path == "reader_direct" {
			if len(r.Sources) != 1 || r.ContextID != nil {
				t.Fatal("direct path not separate")
			}
			continue
		}
		if r.Stage == "server_return" {
			assertReceiptChainServerReturn(t, p, scope, r)
		} else if r.Stage == "source_read" {
			if len(r.Sources) != 1 || r.SourceRelation != "read" {
				t.Fatal("actual reader GET lost")
			}
		} else if len(r.Sources) != 2 {
			t.Fatal("actual packing not reflected", r.Stage, len(r.Sources))
		}
	}
}

func receiptChainCheckFault(t *testing.T, fault string) {
	t.Helper()

	f := receiptChainPG(t)
	before := len(pgCollection(t, f.p.admin, "execution_traces"))
	spool := f.spool
	switch fault {
	case "before_commit":
		f.repo.fail.Store(true)
	case "lost_reply":
		transport := &receiptChainTransport{next: f.producer.HTTP.Transport}
		if transport.next == nil {
			transport.next = http.DefaultTransport
		}
		transport.lose.Store(true)
		f.producer.HTTP = &http.Client{Transport: transport}
	case "spool_failure":
		if e := os.WriteFile(spool, []byte("owned fixture blocks directory"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	out, e := f.producer.PublishReceiptChain(testCtx, f.session, spool, receiptChainAgent(f.p.in.Scope))
	if e == nil {
		t.Fatal("failure not observed")
	}
	if fault == "before_commit" && len(pgCollection(t, f.p.admin, "execution_traces")) != before {
		t.Fatal("failed commit persisted")
	}
	if fault == "spool_failure" {
		if out.Stored != "observed" || out.Readback != "observed" || out.Spooled != "unknown" {
			t.Fatal(out)
		}
		if e = os.Remove(spool); e != nil {
			t.Fatal(e)
		}
	} else if out.Stored != "unknown" {
		t.Fatal("ambiguous store claimed success")
	}
	recovered, e := f.producer.PublishReceiptChain(testCtx, f.session, spool, receiptChainAgent(f.p.in.Scope))
	if e != nil || recovered.Spooled != "observed" || len(pgCollection(t, f.p.admin, "execution_traces")) != before+1 {
		t.Fatal("retry failed", e)
	}
}

type receiptChainReadbackDelete struct {
	next http.RoundTripper
	p    *nativePGFixture
	once atomic.Bool
}

func (r *receiptChainReadbackDelete) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == "GET" && strings.HasPrefix(req.URL.Path, "/v1/sources/") && r.once.CompareAndSwap(true, false) {
		id, e := url.PathUnescape(strings.TrimPrefix(req.URL.Path, "/v1/sources/"))
		if e != nil {
			return nil, e
		}
		source, e := r.p.admin.Get(testCtx, "sources", r.p.in.Scope, id)
		if e != nil {
			return nil, e
		}
		if e = r.p.admin.Delete(testCtx, "sources", r.p.in.Scope, id, source.Revision); e != nil {
			return nil, e
		}
	}
	return r.next.RoundTrip(req)
}
func TestReceiptChainPostgresDeletedReadbackWithholds(t *testing.T) {
	f := receiptChainPG(t)
	transport := &receiptChainReadbackDelete{next: f.producer.HTTP.Transport, p: f.p}
	if transport.next == nil {
		transport.next = http.DefaultTransport
	}
	transport.once.Store(true)
	f.producer.HTTP = &http.Client{Transport: transport}
	out, e := f.producer.PublishReceiptChain(testCtx, f.session, f.spool, receiptChainAgent(f.p.in.Scope))
	if e == nil || out.Stored != "observed" || out.Readback != "unknown" || out.Spooled != "unknown" {
		t.Fatal("deleted source completed receipt", e)
	}
	files, _ := filepath.Glob(filepath.Join(f.spool, "*.json"))
	if len(files) != 0 {
		t.Fatal("deleted source spooled")
	}
	before := len(pgCollection(t, f.p.admin, "sources"))
	if _, e = f.producer.PublishReceiptChain(testCtx, f.session, f.spool, receiptChainAgent(f.p.in.Scope)); e == nil || len(pgCollection(t, f.p.admin, "sources")) != before {
		t.Fatal("retry resurrected deleted source", e)
	}
}

func TestReceiptChainPostgresOtherPathSeparate(t *testing.T) {
	f := receiptChainPGWithDirect(t, true)
	included := map[receiptchain.Ref]bool{}
	direct := []receiptchain.Ref{}
	for _, r := range f.session.Snapshot().Records {
		if r.Path == "prompt" && r.Stage == "transport_accepted" {
			for _, source := range r.Sources {
				included[source.Ref] = true
			}
		}
		if r.Path == "reader_direct" && r.Stage == "source_read" {
			for _, source := range r.Sources {
				direct = append(direct, source.Ref)
			}
		}
	}
	if len(included) != 2 || len(direct) != 1 || included[direct[0]] {
		t.Fatal("other path became prompt inclusion")
	}
	if _, e := f.producer.PublishReceiptChain(testCtx, f.session, f.spool, receiptChainAgent(f.p.in.Scope)); e != nil {
		t.Fatal(e)
	}
}

// Unknown paging fixtures test storage admission, not additional host observations.
type receiptChainSecondPageFault struct {
	next  http.RoundTripper
	posts atomic.Int64
}

func (r *receiptChainSecondPageFault) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == "POST" && strings.HasSuffix(req.URL.Path, "/execution_traces/completed") && r.posts.Add(1) == 2 {
		return nil, io.ErrUnexpectedEOF
	}
	return r.next.RoundTrip(req)
}
func TestReceiptChainPostgresPartialPageRetry(t *testing.T) {
	f := receiptChainPG(t)
	before := len(pgCollection(t, f.p.admin, "execution_traces"))
	session, e := receiptchain.New(f.session.Scope(), f.session.ReaderSHA256(), nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 65; i++ {
		_, e = session.Record(receiptchain.Record{EventID: fmt.Sprintf("unknown-page-%d", i), Stage: "server_return", Path: "prompt", Status: "unknown", Reason: "unknown", SourceRelation: "unknown", Sources: []receiptchain.Source{}, Parents: []string{}})
		if e != nil {
			t.Fatal(e)
		}
	}
	transport := f.producer.HTTP.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	f.producer.HTTP = &http.Client{Transport: &receiptChainSecondPageFault{next: transport}}
	out, e := f.producer.PublishReceiptChain(testCtx, session, f.spool, receiptChainAgent(f.p.in.Scope))
	if e == nil || out.Stored != "unknown" || out.Readback != "unknown" || len(out.Receipts) != 1 {
		t.Fatal("partial pages reported complete", e)
	}
	if len(pgCollection(t, f.p.admin, "execution_traces")) != before+1 {
		t.Fatal("first durable page absent")
	}
	if files, _ := filepath.Glob(filepath.Join(f.spool, "*.json")); len(files) != 0 {
		t.Fatal("partial pages spooled")
	}
	out, e = f.producer.PublishReceiptChain(testCtx, session, f.spool, receiptChainAgent(f.p.in.Scope))
	if e != nil || out.Stored != "observed" || out.Readback != "observed" || out.Spooled != "observed" || len(out.Receipts) != 2 {
		t.Fatal("bounded retry failed", e)
	}
	if len(pgCollection(t, f.p.admin, "execution_traces")) != before+2 {
		t.Fatal("page retry duplicated durable rows")
	}
	if files, _ := filepath.Glob(filepath.Join(f.spool, "*.json")); len(files) != 2 {
		t.Fatal("missing page references")
	}
}

func assertReceiptChainServerReturn(t *testing.T, p *nativePGFixture, scope Scope, r receiptchain.Record) {
	t.Helper()
	if r.ContextID == nil || len(r.Sources) != 3 {
		t.Fatal("actual server return lost")
	}
	usage, e := p.admin.Get(testCtx, colJobs, scope, *r.ContextID)
	if e != nil || usage.Kind != "context_usage" || usage.Usage == nil || len(usage.Usage.Items) != 3 {
		t.Fatal("canonical usage link", e)
	}
}
