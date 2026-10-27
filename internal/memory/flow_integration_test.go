package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/crewrun"
	"github.com/FelixBaek/arkism/internal/flowresult"
)

const flowHTTPQuery = "?workspace_id=ws-a&project_id=proj-a"

type flowHTTPHarness struct {
	t       *testing.T
	handler http.Handler
}

type flowHandlerTransport struct{ handler http.Handler }

func (r flowHandlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	r.handler.ServeHTTP(w, req)
	return w.Result(), nil
}

func (h flowHTTPHarness) call(method, path, token string, input any, want int, out any) []byte {
	h.t.Helper()
	var raw []byte
	if input != nil {
		var err error
		raw, err = json.Marshal(input)
		if err != nil {
			h.t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, req)
	if w.Code != want {
		h.t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, w.Code, want, w.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			h.t.Fatal(err)
		}
	}
	return w.Body.Bytes()
}

func flowHTTPFixture(t *testing.T) (*fixture, flowHTTPHarness, CrewRunInput) {
	t.Helper()
	f, _, in := flowFixture(t)
	in.Run.Nodes = map[string]CrewRunNode{} // Matches Discord's actual create payload.
	in.Run.Input = "```text\n리서치 요청\n```"
	in.Run.Origin.GuildID = "123456789012345681"
	in.Run.Origin.ChannelID = "123456789012345682"
	in.Run.Origin.UserID = "123456789012345683"
	return f, flowHTTPHarness{t, Handler(f.svc)}, in
}

func finishFlowHTTP(t *testing.T, h flowHTTPHarness, in CrewRunInput) (Record, string) {
	t.Helper()
	var r Record
	h.call("POST", "/v1/crew/runs", tokEngine, in, 200, &r)
	lease := "execution-lease-0123456789"
	h.call("POST", "/v1/crew/runs/"+r.ID+"/lease", tokEngine, FlowLeaseInput{Scope: scopeA, Action: "acquire", LeaseToken: lease}, 200, nil)
	run := *r.CrewRun
	run.Status = "succeeded"
	run.Summary = "short summary"
	research, _ := json.Marshal(flowresult.Research{Answer: strings.Repeat("확인한 조사 결과. ", 700), Sources: []flowresult.Source{{Title: "원문", URL: "https://example.com/original"}}, Limitations: []string{"미확인 수치"}})
	run.Nodes = map[string]CrewRunNode{"start": {Status: "done"}, "research": {Status: "done", Output: string(research)}, "end": {Status: "done", Output: string(research)}}
	h.call("POST", "/v1/crew/runs/"+r.ID+"/progress", tokEngine, CrewRunUpdate{Scope: scopeA, ExpectedRevision: r.Revision, Run: run, LeaseToken: lease}, 200, &r)
	return r, string(research)
}

func TestFlowHTTPInvocationThroughDurableDelivery(t *testing.T) {
	f, h, in := flowHTTPFixture(t)
	var r, again Record
	h.call("POST", "/v1/crew/runs", tokEngine, in, 200, &r)
	h.call("POST", "/v1/crew/runs", tokEngine, in, 200, &again)
	if r.ID != again.ID || r.CrewRun.Definition == nil {
		t.Fatal("missing idempotency or definition snapshot")
	}
	changed := in
	changed.Run.Input = "different event content"
	h.call("POST", "/v1/crew/runs", tokEngine, changed, 409, nil)
	var waiting FlowDeliveryPage
	listPath := "/v1/crew/deliveries" + flowHTTPQuery + "&connector=discord&bot_id=bot-1&limit=200"
	h.call("GET", listPath, tokEngine, nil, 200, &waiting)
	if len(waiting.Items) != 0 {
		t.Fatal("unfinished result exposed for delivery")
	}
	stored, err := f.repo.Get(testCtx, colJobs, scopeA, flowDeliveryID(r.ID))
	if err != nil || stored.FlowDelivery.Status != "waiting_run" {
		t.Fatal("create did not atomically persist delivery")
	}
	r, expectedOutput := finishFlowHTTP(t, h, in)
	var fetched Record
	raw := h.call("GET", "/v1/crew/runs/"+r.ID+flowHTTPQuery, tokEngine, nil, 200, &fetched)
	if strings.Contains(string(raw), "execution-lease-") || fetched.CrewRun.Nodes["end"].Output != expectedOutput {
		t.Fatal("full output lost or private lease leaked")
	}
	rawRun, _ := json.Marshal(fetched.CrewRun)
	if rendered := flowresult.Text(rawRun); len([]rune(rendered)) <= 4000 || !strings.Contains(rendered, "https://example.com/original") || !strings.Contains(rendered, "미확인 수치") {
		t.Fatal("full result is truncated or lacks provenance")
	}
	var page FlowDeliveryPage
	h.call("GET", listPath, tokEngine, nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].Delivery.ResultRevision != r.Revision {
		t.Fatalf("missing terminal outbox: %+v", page)
	}
	d := page.Items[0]
	path := "/v1/crew/deliveries/" + d.ID + "/change"
	change := FlowDeliveryChange{Scope: scopeA, ExpectedRevision: d.Revision, Action: "claim", LeaseToken: "delivery-lease-0123456789"}
	h.call("POST", path, tokEngine, change, 400, nil)
	change.ResultHash = strings.Repeat("a", 64)
	claimRaw := h.call("POST", path, tokEngine, change, 200, &d)
	if strings.Contains(string(claimRaw), change.LeaseToken) || d.Delivery.LeaseUntil.IsZero() {
		t.Fatal("public claim exposed token or omitted deadline")
	}
	change.Action = "ack_part"
	change.ExpectedRevision = d.Revision
	change.MessageID = "123456789012345684"
	h.call("POST", path, tokEngine, change, 200, &d)
	if d.Delivery.Part != 1 || d.Delivery.Status != "pending" {
		t.Fatal("part receipt did not open a recoverable checkpoint")
	}
	var duplicate FlowDeliveryView
	h.call("POST", path, tokEngine, change, 200, &duplicate)
	if duplicate.Revision != d.Revision {
		t.Fatal("duplicate receipt modified state")
	}
	change.MessageID = "123456789012345685"
	h.call("POST", path, tokEngine, change, 409, nil)
	change.Action = "claim"
	change.ExpectedRevision = d.Revision
	change.ResultHash = strings.Repeat("b", 64)
	h.call("POST", path, tokEngine, change, 409, nil)
	// A fresh HTTP handler and a new worker token resume from the saved receipt.
	h.handler = Handler(f.svc)
	change.ResultHash = strings.Repeat("a", 64)
	change.LeaseToken = "replacement-delivery-0123456789"
	h.call("POST", path, tokEngine, change, 200, &d)
	if d.Delivery.Part != 1 {
		t.Fatal("restart lost part index")
	}
	change.Action = "ack_part"
	change.ExpectedRevision = d.Revision
	change.Part = 1
	h.call("POST", path, tokEngine, change, 200, &d)
	change.Action = "complete"
	change.ExpectedRevision = d.Revision
	h.call("POST", path, tokEngine, change, 200, &d)
	h.call("POST", path, tokEngine, change, 200, &duplicate)
	if duplicate.Revision != d.Revision || d.Delivery.Status != "delivered" || len(d.Delivery.MessageIDs) != 2 {
		t.Fatal("completion or receipt replay failed")
	}
	h.call("GET", listPath, tokEngine, nil, 200, &page)
	if len(page.Items) != 0 {
		t.Fatal("completed delivery remained actionable")
	}
	h.call("POST", "/v1/crew/runs", tokEngine, in, 200, &again)
	if again.ID != r.ID || again.Revision != r.Revision || len(f.repo.rows(colCrewRuns)) != 1 {
		t.Fatal("retry after completion reran work")
	}
}

func TestFlowHTTPProgressRequiresCurrentExecutionLease(t *testing.T) {
	_, h, in := flowHTTPFixture(t)
	var r Record
	h.call("POST", "/v1/crew/runs", tokEngine, in, 200, &r)
	up := CrewRunUpdate{Scope: scopeA, ExpectedRevision: r.Revision, Run: *r.CrewRun}
	h.call("POST", "/v1/crew/runs/"+r.ID+"/progress", tokEngine, up, 409, nil)
	lease := FlowLeaseInput{Scope: scopeA, Action: "acquire", LeaseToken: "first-executor-0123456789"}
	h.call("POST", "/v1/crew/runs/"+r.ID+"/lease", tokEngine, lease, 200, nil)
	lease.LeaseToken = "second-executor-0123456789"
	h.call("POST", "/v1/crew/runs/"+r.ID+"/lease", tokEngine, lease, 409, nil)
	up.LeaseToken = lease.LeaseToken
	h.call("POST", "/v1/crew/runs/"+r.ID+"/progress", tokEngine, up, 409, nil)
	up.LeaseToken = "first-executor-0123456789"
	h.call("POST", "/v1/crew/runs/"+r.ID+"/progress", tokEngine, up, 200, &r)
	up.ExpectedRevision = r.Revision
	up.Run.Input = "changed request"
	h.call("POST", "/v1/crew/runs/"+r.ID+"/progress", tokEngine, up, 400, nil)
}

func TestFlowHTTPQuarantinedRunCannotBeReadOrLeased(t *testing.T) {
	f, h, in := flowHTTPFixture(t)
	var run Record
	h.call("POST", "/v1/crew/runs", tokEngine, in, 200, &run)
	run.PrivacyQuarantined = true
	if err := f.repo.Replace(testCtx, colCrewRuns, run, run.Revision); err != nil {
		t.Fatal(err)
	}
	h.call("GET", "/v1/crew/runs/"+run.ID+flowHTTPQuery, tokEngine, nil, 404, nil)
	h.call("POST", "/v1/crew/runs/"+run.ID+"/lease", tokEngine, FlowLeaseInput{Scope: scopeA, Action: "acquire", LeaseToken: "quarantined-executor-0123456789"}, 404, nil)
}

func TestFlowHTTPScopeRoleAndPrivacyBoundaries(t *testing.T) {
	f, h, in := flowHTTPFixture(t)
	h.call("POST", "/v1/crew/runs", tokPipeline, in, 403, nil)
	r, _ := finishFlowHTTP(t, h, in)
	id := flowDeliveryID(r.ID)
	path := "/v1/crew/deliveries/" + id + "/change"
	stored, _ := f.repo.Get(testCtx, colJobs, scopeA, id)
	change := FlowDeliveryChange{Scope: scopeA, ExpectedRevision: stored.Revision, Action: "claim", LeaseToken: "delivery-lease-0123456789", ResultHash: strings.Repeat("c", 64)}
	for _, token := range []string{f.agent, tokPipeline, f.work, "invalid-token"} {
		h.call("POST", path, token, change, 403, nil)
	}
	wrong := change
	wrong.Scope = scopeB
	h.call("POST", path, tokEngine, wrong, 404, nil)
	var page FlowDeliveryPage
	h.call("GET", "/v1/crew/deliveries?workspace_id=ws-a&project_id=proj-b&connector=discord&bot_id=bot-1", tokEngine, nil, 200, &page)
	if len(page.Items) != 0 {
		t.Fatal("cross-scope delivery leaked")
	}
	bad := actor("wrong-scope", "service", true, "read", "write")
	bad.wrongScope = true
	f.auth.actors["wrong-scope"] = bad
	h.call("POST", path, "wrong-scope", change, 403, nil)
	r.ContentErased = true
	if err := f.repo.Replace(testCtx, colCrewRuns, r, r.Revision); err != nil {
		t.Fatal(err)
	}
	h.call("POST", path, tokEngine, change, 409, nil)
}

func TestFlowHTTPExpiredDeliveryLeaseNeverReclaimsSend(t *testing.T) {
	f, h, in := flowHTTPFixture(t)
	r, _ := finishFlowHTTP(t, h, in)
	id := flowDeliveryID(r.ID)
	stored, _ := f.repo.Get(testCtx, colJobs, scopeA, id)
	change := FlowDeliveryChange{Scope: scopeA, ExpectedRevision: stored.Revision, Action: "claim", LeaseToken: "delivery-lease-0123456789", ResultHash: strings.Repeat("a", 64)}
	path := "/v1/crew/deliveries/" + id + "/change"
	var d FlowDeliveryView
	h.call("POST", path, tokEngine, change, 200, &d)
	stored, _ = f.repo.Get(testCtx, colJobs, scopeA, id)
	stored.LeaseUntil = time.Now().Add(-time.Minute)
	if err := f.repo.Replace(testCtx, colJobs, stored, stored.Revision); err != nil {
		t.Fatal(err)
	}
	change.ExpectedRevision = d.Revision
	change.LeaseToken = "replacement-lease-0123456789"
	h.call("POST", path, tokEngine, change, 200, &d)
	if d.Delivery.Status != "unknown" {
		t.Fatal("stale send lease became retryable")
	}
	change.ExpectedRevision = d.Revision
	h.call("POST", path, tokEngine, change, 409, nil)
}

func TestFlowHTTPDeliveryRechecksReadGrantAndKeepsLogsPrivate(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	f, h, in := flowHTTPFixture(t)
	in.Run.Input = "private-flow-request-marker"
	r, _ := finishFlowHTTP(t, h, in)
	stored, _ := f.repo.Get(testCtx, colJobs, scopeA, flowDeliveryID(r.ID))
	change := FlowDeliveryChange{Scope: scopeA, ExpectedRevision: stored.Revision, Action: "claim", LeaseToken: "private-delivery-lease-marker", ResultHash: strings.Repeat("a", 64)}
	path := "/v1/crew/deliveries/" + stored.ID + "/change"
	f.auth.actors[tokEngine] = actor("crew-engine", "service", true, "write")
	h.call("POST", path, tokEngine, change, 403, nil)
	f.auth.actors[tokEngine] = actor("crew-engine", "service", true, "read", "write")
	h.call("POST", path, tokEngine, change, 200, nil)
	for _, private := range []string{in.Run.Input, change.LeaseToken, "확인한 조사 결과", "https://example.com/original"} {
		if strings.Contains(logs.String(), private) {
			t.Fatal("request logger exposed private payload")
		}
	}
	for _, audit := range f.repo.rows("audit_events") {
		raw, _ := json.Marshal(audit)
		if strings.Contains(string(raw), in.Run.Input) || strings.Contains(string(raw), change.LeaseToken) || audit.CrewRun != nil || audit.FlowDelivery != nil {
			t.Fatal("audit event included execution/delivery body")
		}
	}
}

func TestFlowHTTPProgressUsesPinnedApprovalDefinition(t *testing.T) {
	f, h, in := flowHTTPFixture(t)
	var r Record
	h.call("POST", "/v1/crew/runs", tokEngine, in, 200, &r)
	current, _ := f.repo.Get(testCtx, colCrew, scopeA, in.Run.CrewID)
	// Simulate a separately approved edit while this read-only snapshot runs.
	raw, _ := json.Marshal(current.Crew)
	var edited Crew
	_ = json.Unmarshal(raw, &edited)
	edited.Graph.Nodes[1].Type = "approval"
	current.Crew = &edited
	previous := current.Revision
	current.Revision++
	if err := f.repo.Replace(testCtx, colCrew, current, previous); err != nil {
		t.Fatal(err)
	}
	lease := "pinned-executor-0123456789"
	h.call("POST", "/v1/crew/runs/"+r.ID+"/lease", tokEngine, FlowLeaseInput{Scope: scopeA, Action: "acquire", LeaseToken: lease}, 200, nil)
	run := *r.CrewRun
	run.Nodes = map[string]CrewRunNode{"start": {Status: "done"}, "research": {Status: "done", Output: `"read-only result"`}, "end": {Status: "pending"}}
	h.call("POST", "/v1/crew/runs/"+r.ID+"/progress", tokEngine, CrewRunUpdate{Scope: scopeA, ExpectedRevision: r.Revision, Run: run, LeaseToken: lease}, 200, nil)
}

func TestFlowHTTPRealEnginePersistsResultAndDoesNotRerun(t *testing.T) {
	f, h, in := flowHTTPFixture(t)
	definition := Crew{Type: "workflow", Name: "결과 전달 검증", Handle: "결과전달검증", Graph: &CrewGraph{
		Nodes: []CrewNode{{ID: "start", Type: "start"}, {ID: "end", Type: "end"}}, Edges: []CrewEdge{{From: "start", To: "end"}},
	}}
	item := f.crew(definition)
	in.Run.CrewID = item.ID
	in.Run.CrewRevision = item.Revision
	answer := strings.Repeat("검증된 결과 문장. ", 600)
	report, _ := json.Marshal(flowresult.Research{Answer: answer, Sources: []flowresult.Source{{Title: "검증 원문", URL: "https://example.com/source"}}, Limitations: []string{"실제 검색은 이 테스트 범위 밖"}})
	in.Run.Input = string(report)
	var created Record
	h.call("POST", "/v1/crew/runs", tokEngine, in, 200, &created)
	tokenFile := filepath.Join(t.TempDir(), "service.token")
	if err := os.WriteFile(tokenFile, []byte(tokEngine), 0600); err != nil {
		t.Fatal(err)
	}
	client := &crewrun.Client{BaseURL: "http://memoryd.test", TokenFile: tokenFile, Scope: crewrun.Scope{WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID}, HTTP: &http.Client{Transport: flowHandlerTransport{h.handler}}}
	engine := &crewrun.Engine{Store: client}
	// This executes the actual graph engine and HTTP client against the actual
	// API handler. Start/end are deterministic; no LLM, CLI, or socket is used.
	if err := engine.Drive(testCtx, created.ID); err != nil {
		t.Fatal(err)
	}
	var result Record
	h.call("GET", "/v1/crew/runs/"+created.ID+flowHTTPQuery, tokEngine, nil, 200, &result)
	if result.CrewRun.Status != "succeeded" || result.CrewRun.Nodes["end"].Status != "done" {
		t.Fatalf("engine did not complete: %+v", result.CrewRun)
	}
	raw, _ := json.Marshal(result.CrewRun)
	if !strings.Contains(flowresult.Text(raw), answer) {
		t.Fatal("engine or API truncated the complete result")
	}
	revision := result.Revision
	engine = &crewrun.Engine{Store: client}
	if err := engine.Drive(testCtx, created.ID); err != nil {
		t.Fatal(err)
	}
	h.call("GET", "/v1/crew/runs/"+created.ID+flowHTTPQuery, tokEngine, nil, 200, &result)
	if result.Revision != revision {
		t.Fatal("restarted engine reran a terminal workflow")
	}
	var delivery FlowDeliveryPage
	h.call("GET", "/v1/crew/deliveries"+flowHTTPQuery+"&connector=discord&bot_id=bot-1", tokEngine, nil, 200, &delivery)
	if len(delivery.Items) != 1 || delivery.Items[0].Delivery.Status != "pending" || delivery.Items[0].Delivery.ResultRevision != revision {
		t.Fatalf("engine completion did not publish result delivery: %+v", delivery)
	}
}

type flowRejectDeliveryRepo struct{ Repository }
type flowRejectDeliveryTx struct{ Tx }

func (r flowRejectDeliveryRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, flowRejectDeliveryTx{tx}) })
}
func (tx flowRejectDeliveryTx) Insert(ctx context.Context, col string, r Record) error {
	if col == colJobs && r.Kind == flowDeliveryKind {
		return ErrUnavailable
	}
	return tx.Tx.Insert(ctx, col, r)
}

func (tx flowRejectDeliveryTx) Replace(ctx context.Context, col string, r Record, revision int64) error {
	if col == colJobs && r.Kind == flowDeliveryKind {
		return ErrUnavailable
	}
	return tx.Tx.Replace(ctx, col, r, revision)
}

func postgresFlowFixture(t *testing.T) (*fixture, *Postgres, CrewRunInput) {
	t.Helper()
	p := isolatedPostgres(t)
	f, _, in := flowHTTPFixture(t)
	f.svc.Repo = p
	crew := f.crew(deepResearchTemplate())
	in.Run.CrewID, in.Run.CrewRevision = crew.ID, crew.Revision
	// The real repository/transactions are exercised with the existing fake
	// authorizer and an admin connection; this does not claim RLS validation.
	return f, p, in
}

func TestFlowPostgresConcurrentIdempotencyAtomicJobAndLease(t *testing.T) {
	f, p, in := postgresFlowFixture(t)
	type created struct {
		record Record
		err    error
	}
	start := make(chan struct{})
	results := make(chan created, 4)
	for i := 0; i < 4; i++ {
		go func() { <-start; r, err := f.svc.CreateCrewRun(testCtx, tokEngine, in); results <- created{r, err} }()
	}
	close(start)
	var run Record
	for i := 0; i < 4; i++ {
		got := <-results
		if got.err != nil {
			t.Fatal("concurrent create", got.err)
		}
		if run.ID != "" && got.record.ID != run.ID {
			t.Fatal("same request created distinct runs")
		}
		run = got.record
	}
	rows, err := p.ListRecords(testCtx, scopeA, colCrewRuns, 100)
	if err != nil || len(rows) != 1 {
		t.Fatalf("duplicate database rows %d %v", len(rows), err)
	}
	job, err := p.Get(testCtx, colJobs, scopeA, flowDeliveryID(run.ID))
	if err != nil || job.FlowDelivery.Status != "waiting_run" {
		t.Fatal("atomic job absent", err)
	}
	// Fail the second insert inside a real transaction; the run must roll back.
	f.svc.Repo = flowRejectDeliveryRepo{p}
	bad := in
	bad.RequestID = "outbox-insert-fails"
	if _, err = f.svc.CreateCrewRun(testCtx, tokEngine, bad); !errors.Is(err, ErrUnavailable) {
		t.Fatal("outbox failure was not returned", err)
	}
	f.svc.Repo = p
	if _, err = p.Get(testCtx, colCrewRuns, scopeA, flowRequestID(scopeA, "crew-engine", bad.RequestID)); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed outbox left an orphan run", err)
	}
	type leased struct {
		token string
		err   error
	}
	leases := make(chan leased, 4)
	start = make(chan struct{})
	for i := 0; i < 4; i++ {
		token := fmt.Sprintf("concurrent-executor-%016d", i)
		go func() {
			<-start
			_, err := f.svc.FlowLease(testCtx, tokEngine, run.ID, FlowLeaseInput{Scope: scopeA, Action: "acquire", LeaseToken: token})
			leases <- leased{token, err}
		}()
	}
	close(start)
	winner := ""
	won := 0
	for i := 0; i < 4; i++ {
		got := <-leases
		if got.err == nil {
			won++
			winner = got.token
		} else if !errors.Is(got.err, ErrConflict) {
			t.Fatal("lease contention", got.err)
		}
	}
	if won != 1 {
		t.Fatalf("%d concurrent execution owners", won)
	}
	state := *run.CrewRun
	state.Status = "succeeded"
	state.Summary = "complete"
	f.svc.Repo = flowRejectDeliveryRepo{p}
	if _, err = f.svc.UpdateCrewRun(testCtx, tokEngine, run.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: run.Revision, Run: state, LeaseToken: winner}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("terminal delivery update failure was not returned", err)
	}
	f.svc.Repo = p
	unchanged, err := p.Get(testCtx, colCrewRuns, scopeA, run.ID)
	if err != nil || unchanged.CrewRun.Status != "running" || unchanged.Revision != run.Revision {
		t.Fatal("terminal state committed without its outbox", err)
	}
	finished, err := f.svc.UpdateCrewRun(testCtx, tokEngine, run.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: run.Revision, Run: state, LeaseToken: winner})
	if err != nil {
		t.Fatal(err)
	}
	job, err = p.Get(testCtx, colJobs, scopeA, flowDeliveryID(run.ID))
	if err != nil || job.FlowDelivery.Status != "pending" || job.FlowDelivery.ResultRevision != finished.Revision {
		t.Fatal("terminal state and outbox disagree", err)
	}
}

func TestFlowPostgresDeliveryPaginationAndRestartReceipts(t *testing.T) {
	f, p, in := postgresFlowFixture(t)
	for i := 0; i < 5; i++ {
		request := in
		request.RequestID = fmt.Sprintf("page-event-%d", i)
		r, err := f.svc.CreateCrewRun(testCtx, tokEngine, request)
		if err != nil {
			t.Fatal(err)
		}
		token := fmt.Sprintf("page-executor-%016d", i)
		if _, err = f.svc.FlowLease(testCtx, tokEngine, r.ID, FlowLeaseInput{Scope: scopeA, Action: "acquire", LeaseToken: token}); err != nil {
			t.Fatal(err)
		}
		state := *r.CrewRun
		state.Status = "succeeded"
		state.Summary = "result"
		if _, err = f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: r.Revision, Run: state, LeaseToken: token}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	after := ""
	var first FlowDeliveryView
	for pageIndex := 0; pageIndex < 3; pageIndex++ {
		page, err := f.svc.ListFlowDeliveries(testCtx, tokEngine, scopeA, "discord", "bot-1", "", after, 2)
		if err != nil {
			t.Fatal(err)
		}
		want := 2
		if pageIndex == 2 {
			want = 1
		}
		if len(page.Items) != want {
			t.Fatalf("page %d has %d rows want %d", pageIndex, len(page.Items), want)
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatal("pagination repeated row")
			}
			seen[item.ID] = true
			if first.ID == "" {
				first = item
			}
		}
		after = page.NextCursor
		if pageIndex < 2 && after == "" {
			t.Fatal("missing continuation cursor")
		}
	}
	if after != "" || len(seen) != 5 {
		t.Fatal("pagination did not finish")
	}
	for _, scope := range []Scope{scopeB, scopeFar} {
		page, err := f.svc.ListFlowDeliveries(testCtx, tokEngine, scope, "discord", "bot-1", "", "", 2)
		if err != nil || len(page.Items) != 0 {
			t.Fatal("cross-scope SQL result", err)
		}
	}
	page, err := f.svc.ListFlowDeliveries(testCtx, tokEngine, scopeA, "discord", "different-bot", "", "", 2)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("cross-bot SQL result", err)
	}
	change := FlowDeliveryChange{Scope: scopeA, ExpectedRevision: first.Revision, Action: "claim", LeaseToken: "delivery-pg-worker-0123456789", ResultHash: strings.Repeat("a", 64)}
	claimed, err := f.svc.ChangeFlowDelivery(testCtx, tokEngine, first.ID, change)
	if err != nil {
		t.Fatal(err)
	}
	change.Action = "ack_part"
	change.ExpectedRevision = claimed.Revision
	change.MessageID = "123456789012345689"
	acked, err := f.svc.ChangeFlowDelivery(testCtx, tokEngine, first.ID, change)
	if err != nil {
		t.Fatal(err)
	}
	// A separate service instance recovers receipts from PostgreSQL, not RAM.
	restarted := &Service{Repo: p, Auth: f.svc.Auth}
	duplicate, err := restarted.ChangeFlowDelivery(testCtx, tokEngine, first.ID, change)
	if err != nil || duplicate.Revision != acked.Revision || duplicate.Delivery.Part != 1 || duplicate.Delivery.Status != "pending" {
		t.Fatal("receipt replay after service restart", err)
	}
	change.Action = "claim"
	change.ExpectedRevision = acked.Revision
	change.ResultHash = strings.Repeat("b", 64)
	if _, err = restarted.ChangeFlowDelivery(testCtx, tokEngine, first.ID, change); !errors.Is(err, ErrConflict) {
		t.Fatal("changed result hash was not fenced", err)
	}
	change.ResultHash = strings.Repeat("a", 64)
	change.LeaseToken = "delivery-pg-restart-0123456789"
	resumed, err := restarted.ChangeFlowDelivery(testCtx, tokEngine, first.ID, change)
	if err != nil || resumed.Delivery.Part != 1 || len(resumed.Delivery.MessageIDs) != 1 {
		t.Fatal("PG recovery lost checkpoint", err)
	}
}
