package memory

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// decisionServer answers every decision request with relation = choice.
func decisionServer(t *testing.T, choice *string, calls *atomic.Int32) *JevJudge {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "new_notes") || !strings.Contains(string(body), "existing_observations") {
			t.Errorf("decision request without the notes and observations: %s", body)
		}
		io.WriteString(w, `{"model":"jev-1.13.0","answers":{"relation":{"type":"choice","choice":"`+*choice+`","probabilities":{"`+*choice+`":0.9}}},"usage":{"input_tokens":40}}`)
	}))
	t.Cleanup(srv.Close)
	return &JevJudge{APIKey: "decision-key", Endpoint: srv.URL}
}

func TestConsolidationDecisionModelSettlesRestatements(t *testing.T) {
	f, llm, ms := obsFixture(t)
	// One observation already covers two of the three staging notes.
	llm.answers = []string{obsAnswer(t, obsDraft("Staging deploys need the staging flag.", ms[0].ID, ms[1].ID))}
	if out, err := f.consolidate(f.human, false); err != nil || len(out.Proposed) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	choice, calls := "restates", &atomic.Int32{}
	f.svc.Judges = map[string]SkillJudge{"jev": decisionServer(t, &choice, calls)}
	// Off by default: the group goes to the model, the engine is not asked.
	llm.answers = append(llm.answers, obsAnswer(t))
	if out, err := f.consolidate(f.human, false); err != nil || out.Calls != 1 || calls.Load() != 0 {
		t.Fatalf("off: %+v %v calls=%d", out, err, calls.Load())
	}
	bad := map[string]string{"consolidation": "fixed"}
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{ExpectedRevision: -1, DecisionModels: &bad}, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown engine is refused: %v", err)
	}
	on := map[string]string{"consolidation": "jev"}
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{ExpectedRevision: -1, DecisionModels: &on}, false); err != nil {
		t.Fatal(err)
	}
	prompts := len(llm.prompts)
	out, err := f.consolidate(f.human, false)
	if err != nil || out.Calls != 0 || len(llm.prompts) != prompts || calls.Load() != 1 || len(out.Rejected) != 1 || out.Rejected[0].Code != "decision_restates" || out.Rejected[0].Target != "jev" {
		t.Fatalf("restates: %+v %v calls=%d", out, err, calls.Load())
	}
	// Anything else still goes to the model.
	choice = "changes"
	llm.answers = append(llm.answers, obsAnswer(t))
	if out, err = f.consolidate(f.human, false); err != nil || out.Calls != 1 || calls.Load() != 2 {
		t.Fatalf("changes: %+v %v calls=%d", out, err, calls.Load())
	}
	// An engine the server cannot run leaves the model in charge.
	f.svc.Judges = nil
	llm.answers = append(llm.answers, obsAnswer(t))
	if out, err = f.consolidate(f.human, false); err != nil || out.Calls != 1 {
		t.Fatalf("no engine: %+v %v", out, err)
	}
	view, err := f.svc.EffectiveSpaceConfig(testCtx, f.human, scopeA)
	if err != nil || view.Config.DecisionModels["consolidation"] != "jev" || view.Sources["decision_models"] != spaceSourceSpace {
		t.Fatalf("effective: %+v %v", view, err)
	}
}

func TestPickedChoice(t *testing.T) {
	if got := pickedChoice(jevAnswer{Choice: "new", Probabilities: map[string]float64{"new": 0.3, "chatter": 0.7}}); got != "chatter" {
		t.Fatal(got)
	}
	if got := pickedChoice(jevAnswer{Choice: "new"}); got != "new" {
		t.Fatal(got)
	}
}
