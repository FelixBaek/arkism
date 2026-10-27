package memory

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/crewrun"
)

type effectTestTransport func(*http.Request) (*http.Response, error)

func (f effectTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func effectHTTPFixture(t *testing.T) (*fixture, flowHTTPHarness, Record, *crewrun.Engine, string) {
	t.Helper()
	f, h, _ := flowHTTPFixture(t)
	item := f.crew(effectWorkflow())
	run := commonFlowStart(t, h, item, "notify-effect")
	dir := t.TempDir()
	token := filepath.Join(dir, "service.token")
	config := filepath.Join(dir, "notify.json")
	if e := os.WriteFile(token, []byte(tokEngine), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(config, []byte(`{"slack":{"ops":{"webhook_url":"https://hooks.slack.com/services/TTEST/BTEST/synthetic"}}}`), 0600); e != nil {
		t.Fatal(e)
	}
	client := &crewrun.Client{BaseURL: "http://memoryd.test", TokenFile: token, Scope: crewrun.Scope{WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID}, HTTP: &http.Client{Transport: flowHandlerTransport{h.handler}}}
	return f, h, run, &crewrun.Engine{Store: client, NotifyFile: config}, config
}

func effectHTTPApprove(t *testing.T, f *fixture, h flowHTTPHarness, run Record) Record {
	t.Helper()
	h.call("GET", "/v1/crew/runs/"+run.ID+flowHTTPQuery, f.human, nil, 200, &run)
	if run.CrewRun.Status != "waiting" || run.CrewRun.Waiting.EffectID == "" {
		t.Fatal("no exact effect approval", run.CrewRun)
	}
	h.call("POST", "/v1/crew/runs/"+run.ID+"/decision", f.human, CrewRunDecisionInput{Scope: scopeA, ExpectedRevision: run.Revision, NodeID: "notice", Approve: true}, 200, &run)
	return run
}

func TestFlowEffectHTTPActualEngineApprovalRestartAndSingleSend(t *testing.T) {
	f, h, run, engine, _ := effectHTTPFixture(t)
	sent := 0
	transport := effectTestTransport(func(r *http.Request) (*http.Response, error) {
		sent++
		b, _ := io.ReadAll(r.Body)
		if r.URL.String() != "https://hooks.slack.com/services/TTEST/BTEST/synthetic" || string(b) != `{"text":"Synthetic approved message"}` {
			t.Fatalf("wrong prepared arguments: %s %s", r.URL, string(b))
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: http.Header{}}, nil
	})
	engine.HTTP = &http.Client{Transport: transport}
	if e := engine.Drive(testCtx, run.ID); e != nil {
		t.Fatal(e)
	}
	if sent != 0 {
		t.Fatal("sent before approval")
	}
	run = effectHTTPApprove(t, f, h, run)
	// A different engine instance resumes the original effect/attempt after approval.
	engine = &crewrun.Engine{Store: engine.Store, NotifyFile: engine.NotifyFile, HTTP: engine.HTTP}
	if e := engine.Drive(testCtx, run.ID); e != nil {
		t.Fatal(e)
	}
	h.call("GET", "/v1/crew/runs/"+run.ID+flowHTTPQuery, f.human, nil, 200, &run)
	if sent != 1 || run.CrewRun.Status != "succeeded" || run.CrewRun.Nodes["notice"].Runs != 1 {
		t.Fatalf("sent=%d state=%+v", sent, run.CrewRun)
	}
	if e := engine.Drive(testCtx, run.ID); e != nil {
		t.Fatal(e)
	}
	if sent != 1 {
		t.Fatal("terminal replay sent again")
	}
}

func TestFlowEffectHTTPUnknownResponseNeverRetriesOrClaimsSuccess(t *testing.T) {
	f, h, run, engine, _ := effectHTTPFixture(t)
	sent := 0
	engine.HTTP = &http.Client{Transport: effectTestTransport(func(r *http.Request) (*http.Response, error) {
		sent++
		return nil, errors.New("synthetic response lost after external acceptance")
	})}
	if e := engine.Drive(testCtx, run.ID); e != nil {
		t.Fatal(e)
	}
	effectHTTPApprove(t, f, h, run)
	if e := engine.Drive(testCtx, run.ID); e != nil {
		t.Fatal(e)
	}
	h.call("GET", "/v1/crew/runs/"+run.ID+flowHTTPQuery, f.human, nil, 200, &run)
	if run.CrewRun.Status != "failed" || sent != 1 || !strings.Contains(run.CrewRun.Summary, "effect_outcome_unknown") {
		t.Fatal(sent, run.CrewRun)
	}
	id := run.CrewRun.Effects["notice:1"]
	receipt, e := f.repo.Get(testCtx, colJobs, scopeA, id)
	if e != nil || receipt.FlowEffect.Status != "unknown" {
		t.Fatal(receipt, e)
	}
	if e := engine.Drive(testCtx, run.ID); e != nil {
		t.Fatal(e)
	}
	if sent != 1 {
		t.Fatal("unknown effect repeated")
	}
}

func TestFlowEffectHTTPChangedPrivateDestinationCannotUsePriorApproval(t *testing.T) {
	f, h, run, engine, config := effectHTTPFixture(t)
	sent := 0
	engine.HTTP = &http.Client{Transport: effectTestTransport(func(r *http.Request) (*http.Response, error) { sent++; return nil, errors.New("must never dispatch") })}
	if e := engine.Drive(testCtx, run.ID); e != nil {
		t.Fatal(e)
	}
	effectHTTPApprove(t, f, h, run)
	if e := os.WriteFile(config, []byte(`{"slack":{"ops":{"webhook_url":"https://hooks.slack.com/services/TTEST/BTEST/changed"}}}`), 0600); e != nil {
		t.Fatal(e)
	}
	if e := engine.Drive(testCtx, run.ID); e != nil {
		t.Fatal(e)
	}
	h.call("GET", "/v1/crew/runs/"+run.ID+flowHTTPQuery, f.human, nil, 200, &run)
	if sent != 0 || run.CrewRun.Status != "failed" {
		t.Fatal("changed target dispatched", sent, run.CrewRun)
	}
}
