package crew

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// approvedTrialCandidate is a trial candidate record whose plan pins f's dataset and environment.
func approvedTrialCandidate(f *evalFixture, minGain float64) map[string]any {
	plan := map[string]any{"baseline_version": "", "min_score_gain": minGain}
	for k, v := range f.plan {
		if k != "min_score_gain" {
			plan[k] = v
		}
	}
	return map[string]any{"id": "candidate", "scope": map[string]any{"workspace_id": "w"}, "revision": 1, "status": "approved",
		"skill_id": "verify", "comparison_plan": plan, "comparison_plan_sha256": strings.Repeat("a", 64),
		"analysis": map[string]any{"content_hash": digest([]byte("verify"))}, "sources": []any{map[string]any{"source_id": "source"}}}
}

func readyObservation() map[string]any {
	return map[string]any{"state": "ready_for_comparison", "candidate": map[string]any{"id": "candidate"},
		"trial": map[string]any{"revision": 7},
		"items": []any{map[string]any{"scope": map[string]any{"workspace_id": "w"}, "source": map[string]any{"id": "s"}, "evaluation": map[string]any{"id": "e"}}}}
}

func lastCall(t *testing.T, api *fakeEvalAPI) (string, map[string]any) {
	t.Helper()
	if len(api.calls) == 0 {
		t.Fatal("no request made")
	}
	c := list(api.calls[len(api.calls)-1])
	return c[0].(string), obj(c[1])
}

func TestCompareAndPromoteAppliesOnlyAMeasuredWin(t *testing.T) {
	f := newEvalFixture(t, "adapter-echo")
	datasetRaw, envRaw := []byte(mustCanonical(f.dataset)), []byte(mustCanonical(f.env))
	for _, tc := range []struct {
		minGain   float64
		state     string
		lastPath  string
		published bool
	}{
		{0.1, "promoted", "/v1/skills/verify/apply", true},
		{2, "stopped_comparison_regression", "/v1/skill-trials/trial/stop", true},
	} {
		evaluator := &fakeEvalAPI{records: map[string]any{"skill_versions": approvedTrialCandidate(f, tc.minGain),
			"skills": map[string]any{"status": "active", "active_version": ""}, "sources": map[string]any{"text": "verify"}}}
		manager := &fakeEvalAPI{records: map[string]any{"skills": map[string]any{"revision": 3}}}
		got, err := compareAndPromote(manager, evaluator, "manager-credential", "evaluator-credential", map[string]any{"workspace_id": "w"},
			"verify", "trial", readyObservation(), datasetRaw, envRaw)
		if err != nil || got["state"] != tc.state {
			t.Fatal(tc.minGain, got, err)
		}
		if path, payload := lastCall(t, evaluator); path != "/v1/skills/comparisons/record" || payload["trial_id"] != "trial" {
			t.Fatal(path, payload)
		}
		path, payload := lastCall(t, manager)
		if path != tc.lastPath {
			t.Fatal(tc.minGain, path)
		}
		if tc.state == "promoted" {
			if payload["expected_revision"] != 3 || payload["version_id"] != "candidate" || payload["validation_id"] != "one" ||
				mustCanonical(payload["scoped_trial_evidence"]) != `[{"evaluation":{"id":"e"},"scope":{"workspace_id":"w"},"source":{"id":"s"}}]` {
				t.Fatal(payload)
			}
		} else if payload["expected_revision"] != 7 {
			t.Fatal(payload)
		}
	}
}

func TestCompareAndPromoteRefusesWhenNotReadyOrSameCredential(t *testing.T) {
	api := &fakeEvalAPI{}
	waiting := map[string]any{"state": "awaiting_observation"}
	if got, err := compareAndPromote(api, api, "a", "b", nil, "s", "t", waiting, nil, nil); err != nil || !same(got, waiting) || len(api.calls) != 0 {
		t.Fatal(got, err)
	}
	if _, err := compareAndPromote(api, api, "same", "same", nil, "s", "t", readyObservation(), nil, nil); code(err) != "independent_checker_credential_required" {
		t.Fatal(err)
	}
	// The manager credential pinned into the comparison adapter is refused before anything runs.
	f := newEvalFixture(t, "adapter-echo")
	f.env["env"] = map[string]any{"LEAK": "manager-credential"}
	f.repin()
	evaluator := &fakeEvalAPI{records: map[string]any{"skill_versions": approvedTrialCandidate(f, 0.1),
		"skills": map[string]any{"status": "active", "active_version": ""}, "sources": map[string]any{"text": "verify"}}}
	_, err := compareAndPromote(&fakeEvalAPI{}, evaluator, "manager-credential", "evaluator-credential", nil, "verify", "trial",
		readyObservation(), []byte(mustCanonical(f.dataset)), []byte(mustCanonical(f.env)))
	if code(err) != "manager_credential_in_adapter" || len(evaluator.calls) != 0 {
		t.Fatal(err, evaluator.calls)
	}
}

// scriptedTrialAPI answers gets by collection and observe requests with one fixed observation.
type scriptedTrialAPI struct {
	records     map[string]any
	observation map[string]any
	calls       []string
}

func (a *scriptedTrialAPI) get(collection string, _ map[string]any, id string) (any, error) {
	if v, ok := a.records[collection+"/"+id]; ok {
		return v, nil
	}
	return a.records[collection], nil
}

func (a *scriptedTrialAPI) request(path string, _ any) (any, error) {
	a.calls = append(a.calls, path)
	return a.observation, nil
}

func trialCheckerEnv(t *testing.T) []byte {
	bin := testBinary(t)
	return []byte(mustCanonical(map[string]any{"argv": []any{bin, "trial-checker"}, "files": map[string]any{bin: fileDigest(t, bin)},
		"env": map[string]any{}, "timeout_ms": 5000, "max_output_bytes": 1024}))
}

func TestProcessTrialRefusesChangedOrInactiveEvidence(t *testing.T) {
	env := trialCheckerEnv(t)
	observation := func() map[string]any {
		return map[string]any{"state": "awaiting_independent_evaluation", "candidate": map[string]any{"id": "v", "revision": 2},
			"items": []any{
				map[string]any{"outcome": "failure", "source": map[string]any{"id": "skip"}},
				map[string]any{"outcome": "success", "independently_verified": true, "source": map[string]any{"id": "skip"}},
				map[string]any{"scope": map[string]any{}, "source": map[string]any{"id": "s", "revision": 1}, "outcome": "success", "independently_verified": false}}}
	}
	records := func() map[string]any {
		return map[string]any{
			"skill_versions":   map[string]any{"revision": 2, "status": "approved"},
			"sources/s":        map[string]any{"id": "s", "revision": 1, "status": "active", "trace_id": "t", "text": `{"actual_test_exit":0}`},
			"execution_traces": map[string]any{"status": "active", "sources": []any{map[string]any{"source_id": "e"}}},
			"sources/e":        map[string]any{"id": "e", "revision": 1, "status": "active", "trace_id": "t", "text": `{"actual_test_exit":0}`},
		}
	}
	for want, change := range map[string]func(map[string]any){
		"trial_candidate_changed":    func(r map[string]any) { obj(r["skill_versions"])["status"] = "retired" },
		"trial_source_changed":       func(r map[string]any) { obj(r["sources/s"])["privacy_quarantined"] = true },
		"trial_trace_changed":        func(r map[string]any) { obj(r["execution_traces"])["content_erased"] = true },
		"trial_evidence_unavailable": func(r map[string]any) { obj(r["execution_traces"])["sources"] = []any{} },
		"trial_evidence_changed":     func(r map[string]any) { obj(r["sources/e"])["trace_id"] = "other" },
	} {
		r := records()
		change(r)
		manager, evaluator := &scriptedTrialAPI{observation: observation()}, &scriptedTrialAPI{records: r}
		if _, err := processTrial(manager, evaluator, "manager-credential", "evaluator-credential", map[string]any{}, "skill", "trial", env); code(err) != want {
			t.Errorf("%s: %v", want, err)
		}
		if len(evaluator.calls) != 0 {
			t.Errorf("%s: an outcome was posted: %v", want, evaluator.calls)
		}
	}
	// Skipped items never reach the checker: with only them, nothing is posted and the trial is observed again.
	only := observation()
	only["items"] = list(only["items"])[:2]
	manager, evaluator := &scriptedTrialAPI{observation: only}, &scriptedTrialAPI{records: records()}
	if _, err := processTrial(manager, evaluator, "manager-credential", "evaluator-credential", map[string]any{}, "skill", "trial", env); err != nil || len(evaluator.calls) != 0 || len(manager.calls) != 2 {
		t.Fatal(err, evaluator.calls, manager.calls)
	}
	// A trial that is not waiting for evaluation is returned as observed, without reading any evidence.
	done := map[string]any{"state": "ready_for_comparison"}
	manager = &scriptedTrialAPI{observation: done}
	if got, err := processTrial(manager, &scriptedTrialAPI{}, "manager-credential", "evaluator-credential", map[string]any{}, "skill", "trial", env); err != nil || !same(got, done) || len(manager.calls) != 1 {
		t.Fatal(got, err)
	}
}

func init() {
	// A checker that answers a score instead of the required {"passed": bool}.
	helperModes["trial-checker-score"] = func([]string) int {
		_ = readStdinJSON()
		os.Stdout.WriteString(`{"output":{"score":1},"cost":0}`)
		return 0
	}
}

func TestProcessTrialRejectsAMalformedCheckerAnswer(t *testing.T) {
	bin := testBinary(t)
	env := []byte(mustCanonical(map[string]any{"argv": []any{bin, "trial-checker-score"}, "files": map[string]any{bin: fileDigest(t, bin)},
		"env": map[string]any{}, "timeout_ms": 5000, "max_output_bytes": 1024}))
	manager := &trialAPI{}
	evaluator := &trialAPI{source: map[string]any{"id": "s", "revision": 1, "status": "active", "trace_id": "t", "text": `{"actual_test_exit":0}`}}
	if _, err := processTrial(manager, evaluator, "manager-credential", "evaluator-credential", map[string]any{}, "skill", "trial", env); code(err) != "independent_checker_result_invalid" || len(evaluator.calls) != 0 {
		t.Fatal(err, evaluator.calls)
	}
}

func TestTrialRunMonitorsPromotionAndReadsComparisonFiles(t *testing.T) {
	mon := &monitorAPI{}
	withTrialCheck(t, func() (map[string]any, error) { return map[string]any{"state": "promoted"}, nil })
	got, err := (&trialRunner{manager: mon}).run(testTrial(t))
	paths := []string{}
	for _, c := range mon.calls {
		paths = append(paths, c[0].(string))
	}
	if err != nil || got["state"] != "monitored" || strings.Join(paths, " ") != "/v1/skill-trials/trial/claim-check /v1/skill-trials/trial/monitor /v1/skill-trials/trial/finish-check" {
		t.Fatal(got, err, paths)
	}

	withTrialCheck(t, func() (map[string]any, error) { return map[string]any{"state": "awaiting_observation"}, nil })
	trial := testTrial(t)
	api := &leaseAPI{}
	dir := t.TempDir()
	trial["comparison_dataset"], trial["comparison_environment"] = filepath.Join(dir, "dataset.json"), filepath.Join(dir, "env.json")
	if got := (&trialRunner{manager: api}).once([]any{trial}); mustCanonical(got) != `[{"state":"unavailable","trial_id":"trial"}]` {
		t.Fatal(got)
	}
	_ = os.WriteFile(obj(trial)["comparison_dataset"].(string), []byte("{}"), 0o600)
	_ = os.WriteFile(obj(trial)["comparison_environment"].(string), []byte("{}"), 0o600)
	api = &leaseAPI{}
	if got, err := (&trialRunner{manager: api}).run(trial); err != nil || got["state"] != "awaiting_observation" {
		t.Fatal(got, err)
	}

	trial = testTrial(t)
	trial["environment"] = filepath.Join(dir, "missing.json")
	api = &leaseAPI{}
	if _, err := (&trialRunner{manager: api}).run(trial); err == nil || obj(api.calls[len(api.calls)-1][1])["outcome"] != "unavailable" {
		t.Fatal(err, api.calls)
	}
}

// monitorAPI grants the lease and answers the post-promotion monitor with a state.
type monitorAPI struct{ leaseAPI }

func (a *monitorAPI) request(path string, payload any) (any, error) {
	if strings.HasSuffix(path, "/monitor") {
		a.calls = append(a.calls, [2]any{path, payload})
		return map[string]any{"state": "monitored"}, nil
	}
	return a.leaseAPI.request(path, payload)
}

// finishFailAPI grants the lease but refuses the finish request.
type finishFailAPI struct{ leaseAPI }

func (a *finishFailAPI) request(path string, payload any) (any, error) {
	if strings.HasSuffix(path, "/finish-check") {
		return nil, errors.New("down")
	}
	return a.leaseAPI.request(path, payload)
}

func TestTrialRunReportsAFailedFinish(t *testing.T) {
	withTrialCheck(t, func() (map[string]any, error) { return map[string]any{"state": "awaiting_observation"}, nil })
	if got, err := (&trialRunner{manager: &finishFailAPI{}}).run(testTrial(t)); err == nil || got != nil {
		t.Fatal(got, err)
	}
	if _, err := (&trialRunner{manager: &leaseAPI{err: errors.New("down")}}).run(testTrial(t)); err == nil {
		t.Fatal("a failed claim was not reported")
	}
}

func TestSkillTrialsArgumentErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte(text), 0o600)
		return p
	}
	m, e := write("m", "manager\n"), write("e", "evaluator\n")
	good := write("good.json", mustCanonical([]any{testTrial(t)}))
	half := testTrial(t)
	half["comparison_dataset"] = "/x"
	base := func(config string, extra ...string) []string {
		return append([]string{"--manager-token-file", m, "--evaluator-token-file", e, "--config", config}, extra...)
	}
	for want, argv := range map[string][]string{
		"unrecognized arguments":          append(base(good), "extra"),
		"the following arguments":         {"--config", good},
		"interval must be":                base(good, "--interval", "5"),
		"config must contain":             base(write("empty.json", "[]")),
		"config must contain 1..32 trial": base(write("scalar.json", "[1]")),
		"each trial needs":                base(write("half.json", mustCanonical([]any{half}))),
		"distinct nonempty":               {"--manager-token-file", write("blank", "\n"), "--evaluator-token-file", e, "--config", good},
	} {
		var out, errOut strings.Builder
		if rc := skillTrialsMain(argv, nil, &out, &errOut); rc != 2 || !strings.Contains(errOut.String(), want) {
			t.Errorf("%s: %d %q", want, rc, errOut.String())
		}
	}
	var out, errOut strings.Builder
	if rc := skillTrialsMain([]string{"--manager-token-file", filepath.Join(dir, "none"), "--evaluator-token-file", e, "--config", good}, nil, &out, &errOut); rc != 1 ||
		errOut.String() != "trial_check_failed\n" {
		t.Fatal(rc, errOut.String())
	}
	errOut.Reset()
	if rc := skillTrialsMain(base(good, "--once", "--url", "http://example.com"), nil, &out, &errOut); rc != 1 || errOut.String() != "invalid_api_url\n" {
		t.Fatal(rc, errOut.String())
	}
}

func TestSkillTrialsOnceReportsEachTrialWithoutSecrets(t *testing.T) {
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	dir := t.TempDir()
	m, e, config := filepath.Join(dir, "m"), filepath.Join(dir, "e"), filepath.Join(dir, "trials.json")
	_ = os.WriteFile(m, []byte("manager-secret\n"), 0o600)
	_ = os.WriteFile(e, []byte("evaluator-secret\n"), 0o600)
	_ = os.WriteFile(config, []byte(mustCanonical([]any{testTrial(t)})), 0o600)
	var out, errOut strings.Builder
	rc := skillTrialsMain([]string{"--once", "--interval", "30", "--url", srv.URL, "--manager-token-file", m, "--evaluator-token-file", e, "--config", config}, nil, &out, &errOut)
	if rc != 0 || out.String() != `[{"state":"not_due","trial_id":"trial"}]`+"\n" || strings.Contains(out.String()+errOut.String(), "secret") {
		t.Fatal(rc, out.String(), errOut.String())
	}
	if len(auth) != 1 || auth[0] != "POST /v1/skill-trials/trial/claim-check Bearer manager-secret" {
		t.Fatal(auth)
	}
}

func TestEvalAPIRefusesUnsafeURLsAndOversizedAnswers(t *testing.T) {
	for _, bad := range []string{"http://example.com", "https://u:p@example.com", "https://example.com/?q=1", "https://example.com/#f", "ftp://localhost", "http://[::1"} {
		if _, err := newEvalAPI(bad, "t"); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			_, _ = w.Write([]byte(`"` + strings.Repeat("x", 2097152) + `"`))
		case "/denied":
			w.WriteHeader(http.StatusForbidden)
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()
	api, err := newEvalAPI(srv.URL+"/", "t")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.request("/big", nil); code(err) != "api_response_limit" {
		t.Fatal(err)
	}
	if _, err := api.request("/denied", nil); host.HTTPCode(err) != 403 {
		t.Fatal(err)
	}
	if got, err := api.request("/ok", map[string]any{}); err != nil || mustCanonical(got) != `{"ok":true}` {
		t.Fatal(got, err)
	}
}

func TestSensitivityLevelDefaultsToRestricted(t *testing.T) {
	for v, want := range map[any]int{"public": 0, "internal": 1, "private": 2, "restricted": 3, "secret": 3, 1: 3} {
		if got := level(v); got != want {
			t.Errorf("level(%v) = %d", v, got)
		}
	}
	if (&comparisonError{"x"}).Error() != "x" {
		t.Fatal("comparison error text")
	}
}

func TestEvaluateResumePublishesOnlyTheMatchingResult(t *testing.T) {
	record := map[string]any{"id": "cand", "scope": map[string]any{"workspace_id": "w"}, "revision": 1}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"evaluation":{"id":"one"}}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	write := func(name string, v any) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte(mustCanonical(v)), 0o600)
		return p
	}
	proposal := write("proposal.json", map[string]any{"record": record, "comparison_plan_sha256": strings.Repeat("a", 64)})
	token := write("token", "secret")
	target := map[string]any{"collection": "skill_versions", "id": "cand", "revision": 1}
	matching := map[string]any{"format_version": 1, "candidate": target, "comparison_report": map[string]any{"plan_sha256": strings.Repeat("a", 64)}}
	run := func(result string) (int, string, string) {
		var out, errOut strings.Builder
		rc := evaluateMain([]string{"--proposal", proposal, "--token-file", token, "--url", srv.URL, "--publish", "--resume-result", result}, nil, &out, &errOut)
		return rc, out.String(), errOut.String()
	}
	if rc, _, errOut := run(write("legacy.json", map[string]any{"candidate": target})); rc != 1 || errOut != "legacy_result_requires_review\n" {
		t.Fatal(rc, errOut)
	}
	other := map[string]any{"format_version": 1, "candidate": target, "comparison_report": map[string]any{"plan_sha256": strings.Repeat("b", 64)}}
	if rc, _, errOut := run(write("other.json", other)); rc != 1 || errOut != "resume_target_mismatch\n" {
		t.Fatal(rc, errOut)
	}
	path := write("result.json", matching)
	if rc, out, errOut := run(path); rc != 0 || out != `{"evaluation":{"id":"one"},"published":true,"resumed":true}`+"\n" || !exists(path+".receipt") {
		t.Fatal(rc, out, errOut)
	}
	// Replaying the same receipt is accepted; the stored receipt is not rewritten.
	if rc, _, errOut := run(path); rc != 0 {
		t.Fatal(rc, errOut)
	}
}

func TestEvaluateCommandArgumentAndInputErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte(text), 0o600)
		return p
	}
	proposal, token := write("p.json", "{}"), write("token", "t")
	for want, argv := range map[string][]string{
		"unrecognized arguments":   {"--proposal", proposal, "--token-file", token, "x"},
		"the following arguments":  {"--proposal", proposal},
		"--resume-result requires": {"--proposal", proposal, "--token-file", token, "--resume-result", "r", "--publish", "--execute"},
		"dataset/environment":      {"--proposal", proposal, "--token-file", token},
		"execution requires":       {"--proposal", proposal, "--token-file", token, "--dataset", "d", "--environment", "e", "--execute"},
	} {
		var out, errOut strings.Builder
		if rc := evaluateMain(argv, nil, &out, &errOut); rc != 2 || !strings.Contains(errOut.String(), want) {
			t.Errorf("%s: %d %q", want, rc, errOut.String())
		}
	}
	for name, argv := range map[string][]string{
		"array proposal": {"--proposal", write("a.json", "[]"), "--token-file", token, "--dataset", "d", "--environment", "e"},
		"missing token":  {"--proposal", proposal, "--token-file", filepath.Join(dir, "none"), "--dataset", "d", "--environment", "e"},
		"remote url":     {"--proposal", proposal, "--token-file", token, "--dataset", "d", "--environment", "e", "--url", "http://example.com"},
	} {
		var out, errOut strings.Builder
		if rc := evaluateMain(argv, nil, &out, &errOut); rc != 1 || errOut.Len() == 0 || strings.Contains(errOut.String(), "\"t\"") {
			t.Errorf("%s: %d %q", name, rc, errOut.String())
		}
	}
}
