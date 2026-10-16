package crew

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// leaseAPI grants a lease (or fails claim-check with err) and records every request.
type leaseAPI struct {
	err   error
	calls [][2]any
}

func (a *leaseAPI) get(string, map[string]any, string) (any, error) { return nil, errors.New("unused") }

func (a *leaseAPI) request(path string, payload any) (any, error) {
	a.calls = append(a.calls, [2]any{path, payload})
	if strings.HasSuffix(path, "/claim-check") {
		if a.err != nil {
			return nil, a.err
		}
		return map[string]any{"revision": 2, "lease_token": "lease"}, nil
	}
	return map[string]any{}, nil
}

func withTrialCheck(t *testing.T, check func() (map[string]any, error)) *int {
	calls := 0
	old := trialCheck
	trialCheck = func(comparisonAPI, comparisonAPI, string, string, map[string]any, string, string, []byte) (map[string]any, error) {
		calls++
		return check()
	}
	t.Cleanup(func() { trialCheck = old })
	return &calls
}

func testTrial(t *testing.T) map[string]any {
	env := filepath.Join(t.TempDir(), "environment.json")
	if err := os.WriteFile(env, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return map[string]any{"scope": map[string]any{"workspace_id": "w"}, "skill_id": "skill", "trial_id": "trial", "environment": env}
}

func TestTrialNotDueNeverExecutes(t *testing.T) {
	api := &leaseAPI{err: &host.HTTPError{Code: 404}}
	calls := withTrialCheck(t, func() (map[string]any, error) { return nil, nil })
	got, err := (&trialRunner{manager: api}).run(testTrial(t))
	if err != nil || got["state"] != "not_due" || *calls != 0 || len(api.calls) != 1 {
		t.Fatal(got, err, *calls, api.calls)
	}
}

func TestTrialCheckerFailureFinishesUnavailable(t *testing.T) {
	api := &leaseAPI{}
	withTrialCheck(t, func() (map[string]any, error) { return nil, errors.New("private payload") })
	got := (&trialRunner{manager: api}).once([]any{testTrial(t)})
	if mustCanonical(got) != `[{"state":"unavailable","trial_id":"trial"}]` {
		t.Fatal(got)
	}
	last := obj(api.calls[len(api.calls)-1][1])
	if last["outcome"] != "unavailable" || last["lease_token"] != "lease" {
		t.Fatal(last)
	}
}

func TestTrialWaitingObservationsAreCheckedNotFailures(t *testing.T) {
	api := &leaseAPI{}
	withTrialCheck(t, func() (map[string]any, error) { return map[string]any{"state": "awaiting_observation"}, nil })
	got, err := (&trialRunner{manager: api}).run(testTrial(t))
	if err != nil || got["state"] != "awaiting_observation" || obj(api.calls[len(api.calls)-1][1])["outcome"] != "checked" {
		t.Fatal(got, err, api.calls)
	}
}

func TestSkillTrialsRequiresDistinctCredentials(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	config := filepath.Join(dir, "trials.json")
	_ = os.WriteFile(token, []byte("same\n"), 0o600)
	_ = os.WriteFile(config, []byte(mustCanonical([]any{testTrial(t)})), 0o600)
	var out, errOut strings.Builder
	if code := skillTrialsMain([]string{"--once", "--manager-token-file", token, "--evaluator-token-file", token, "--config", config}, nil, &out, &errOut); code != 2 ||
		!strings.Contains(errOut.String(), "distinct") || strings.Contains(errOut.String(), "same") {
		t.Fatal(code, errOut.String())
	}
}

func init() {
	// The pinned trial checker: passes on an observed test exit 0, never on the word "success".
	helperModes["trial-checker"] = func([]string) int {
		x := readStdinJSON()
		passed := false
		for _, e := range list(obj(x["input"])["events"]) {
			passed = passed || same(obj(obj(e)["payload"])["actual_test_exit"], 0)
		}
		os.Stdout.WriteString(mustCanonical(map[string]any{"output": map[string]any{"passed": passed}, "cost": 0}))
		return 0
	}
}

// trialAPI answers the manager's observe and the evaluator's reads.
type trialAPI struct {
	source map[string]any
	calls  []map[string]any
}

func (a *trialAPI) get(collection string, _ map[string]any, _ string) (any, error) {
	switch collection {
	case "skill_versions":
		return map[string]any{"revision": 2, "status": "approved"}, nil
	case "execution_traces":
		return map[string]any{"status": "active", "sources": []any{map[string]any{"source_id": "s"}}}, nil
	}
	return a.source, nil
}

func (a *trialAPI) request(path string, payload any) (any, error) {
	a.calls = append(a.calls, obj(payload))
	return map[string]any{"state": "awaiting_independent_evaluation", "candidate": map[string]any{"id": "v", "revision": 2},
		"items": []any{map[string]any{"scope": map[string]any{}, "source": map[string]any{"collection": "sources", "id": "s", "revision": 1},
			"outcome": "success", "independently_verified": false}}}, nil
}

func TestTrialCheckerReportsBehaviorNotUsageWord(t *testing.T) {
	bin := testBinary(t)
	env := []byte(mustCanonical(map[string]any{"argv": []any{bin, "trial-checker"}, "files": map[string]any{bin: fileDigest(t, bin)},
		"env": map[string]any{}, "timeout_ms": 5000, "max_output_bytes": 1024}))
	for _, actual := range []int{0, 1} {
		manager := &trialAPI{}
		evaluator := &trialAPI{source: map[string]any{"id": "s", "revision": 1, "status": "active", "trace_id": "t",
			"text": mustCanonical(map[string]any{"status": "success", "actual_test_exit": actual})}}
		if _, err := processTrial(manager, evaluator, "manager-credential", "evaluator-credential", map[string]any{}, "skill", "trial", env); err != nil {
			t.Fatal(err)
		}
		posted := evaluator.calls[len(evaluator.calls)-1]
		if posted["passed"] != (actual == 0) || mustCanonical(posted["evidence"]) != `[{"collection":"sources","id":"s","revision":1}]` || len(manager.calls) != 2 {
			t.Fatal(actual, posted, manager.calls)
		}
	}
	manager := &trialAPI{}
	if _, err := processTrial(manager, manager, "same", "same", map[string]any{}, "s", "t", []byte("{}")); code(err) != "independent_checker_credential_required" || len(manager.calls) != 0 {
		t.Fatal(err)
	}
}
