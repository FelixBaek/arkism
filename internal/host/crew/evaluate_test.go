package crew

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func readGolden(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	v, err := strictJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v.(map[string]any)
}

func TestStrictJSONAndSumMatchPython(t *testing.T) {
	g := readGolden(t, "evaluate_numbers.json")
	for _, c := range list(g["strict_json"]) {
		m := obj(c)
		v, err := strictJSON([]byte(m["raw"].(string)))
		if want, isErr := m["error"].(string); isErr {
			var ce *comparisonError
			got := "invalid"
			if errors.As(err, &ce) {
				got = ce.code
			}
			if err == nil || got != want {
				t.Errorf("%s: got %v, want error %s", m["raw"], err, want)
			}
			continue
		}
		if err != nil || mustCanonical(v) != m["canonical"] {
			t.Errorf("%s: got %v %v, want %s", m["raw"], mustCanonical(v), err, m["canonical"])
		}
	}
	for _, c := range list(g["sum"]) {
		m := obj(c)
		v, _ := strictJSON([]byte(m["values"].(string)))
		got, err := pySum(list(v))
		if err != nil || string(got) != m["sum"] {
			t.Errorf("sum %s = %s, want %s", m["values"], got, m["sum"])
		}
	}
}

type evalFixture struct {
	dir                          string
	dataset, env, plan, prepared map[string]any
}

func newEvalFixture(t *testing.T, mode string) *evalFixture {
	bin := testBinary(t)
	f := &evalFixture{dir: t.TempDir()}
	cases := []any{}
	for _, c := range []string{"quality", "regression", "safety"} {
		cases = append(cases, map[string]any{"id": c, "category": c, "input": c, "expected": c, "held_out": true})
	}
	f.dataset = map[string]any{"cases": cases}
	f.env = map[string]any{"argv": []any{bin, mode}, "files": map[string]any{bin: fileDigest(t, bin)}, "env": map[string]any{},
		"timeout_ms": 5000, "max_output_bytes": 1024}
	f.plan = map[string]any{"min_score_gain": 0.1, "max_cost": 1, "max_duration_ms": 10000}
	f.repin()
	f.prepared = map[string]any{"scope": map[string]any{"workspace_id": "test", "project_id": "test"},
		"candidate": map[string]any{"collection": "skill_versions", "id": "candidate", "revision": 1}, "baseline_id": "", "plan": f.plan,
		"plan_sha256": strings.Repeat("a", 64), "skills": map[string]any{"baseline": "", "candidate": "verify"}}
	return f
}

func fileDigest(t *testing.T, p string) string {
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return digest(raw)
}

func (f *evalFixture) repin() {
	f.plan["dataset_sha256"] = digest([]byte(mustCanonical(f.dataset)))
	f.plan["environment_sha256"] = digest([]byte(mustCanonical(f.env)))
}

func (f *evalFixture) validate() (map[string]any, map[string]any, error) {
	return validateInputs([]byte(mustCanonical(f.dataset)), []byte(mustCanonical(f.env)), f.plan, "test-evaluator-credential")
}

func code(err error) string {
	var ce *comparisonError
	if errors.As(err, &ce) {
		return ce.code
	}
	if err != nil {
		return "other"
	}
	return ""
}

func TestRealProcessComparisonAndPrivateInputs(t *testing.T) {
	f := newEvalFixture(t, "adapter-echo")
	dataset, env, err := f.validate()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARKISM_API_TOKEN", "test-evaluator-credential")
	result, err := evaluate(f.prepared, dataset, env)
	if err != nil {
		t.Fatal(err)
	}
	report := obj(result["comparison_report"])
	if result["passed"] != true || mustCanonical(report["baseline_score"]) != "0.0" || mustCanonical(report["candidate_score"]) != "1.0" ||
		mustCanonical(report["candidate_cost"]) != "0.75" || report["candidate_duration_ms"].(int64) <= 0 || len(list(result["cases"])) != 6 {
		t.Fatalf("result %s", mustCanonical(result))
	}
	if text := mustCanonical(result); strings.Contains(text, `"expected"`) || strings.Contains(text, `"output"`) {
		t.Fatal("private case data leaked into the result")
	}
}

func TestRegressionAndSafetyDoNotFollowQualityScore(t *testing.T) {
	f := newEvalFixture(t, "adapter-echo")
	cases := list(f.dataset["cases"])
	obj(cases[1])["expected"] = "different"
	obj(cases[2])["expected"] = "different"
	result, err := evaluate(f.prepared, f.dataset, f.env)
	if err != nil {
		t.Fatal(err)
	}
	report := obj(result["comparison_report"])
	if mustCanonical(report["candidate_score"]) != "1.0" || report["regression_passed"] != false || report["safety_passed"] != false || result["passed"] != false {
		t.Fatal(mustCanonical(report))
	}
}

func TestEnvironmentAndDatasetPins(t *testing.T) {
	for _, change := range []string{"dataset", "environment", "file"} {
		f := newEvalFixture(t, "adapter-echo")
		pinned := filepath.Join(f.dir, "data.txt")
		_ = os.WriteFile(pinned, []byte("one"), 0o600)
		obj(f.env["files"])[pinned] = fileDigest(t, pinned)
		f.repin()
		switch change {
		case "dataset":
			obj(list(f.dataset["cases"])[0])["expected"] = "changed"
		case "environment":
			f.env["timeout_ms"] = 5001
		case "file":
			_ = os.WriteFile(pinned, []byte("two"), 0o600)
		}
		_, _, err := validateInputs([]byte(mustCanonical(f.dataset)), []byte(mustCanonical(f.env)), f.plan, "")
		if code(err) == "" || code(err) == "other" {
			t.Errorf("%s: %v", change, err)
		}
	}
}

func TestLimitsAndInvalidMeasurements(t *testing.T) {
	for _, mode := range [][]any{{"adapter-sleep"}, {"adapter-big"}, {"adapter-print", `{"output":1}`},
		{"adapter-print", `{"output":1,"cost":true}`}, {"adapter-print", `{"output":1,"cost":NaN}`}, {"adapter-fail"}} {
		f := newEvalFixture(t, "")
		f.env["argv"] = append([]any{list(f.env["argv"])[0]}, mode...)
		f.env["timeout_ms"] = 300
		if _, _, _, _, err := executeAdapter(f.env, map[string]any{"input": "private", "skill": "private"}, nil); code(err) == "" {
			t.Errorf("%v: no error", mode)
		}
	}
}

func TestCoverageAndEvaluatorTokenRejected(t *testing.T) {
	f := newEvalFixture(t, "adapter-echo")
	obj(list(f.dataset["cases"])[0])["held_out"] = false
	f.repin()
	if _, _, err := f.validate(); !strings.Contains(code(err), "coverage") {
		t.Fatal(err)
	}
	obj(list(f.dataset["cases"])[0])["held_out"] = true
	obj(f.env["env"])["OTHER"] = "test-evaluator-credential"
	f.repin()
	if _, _, err := f.validate(); !strings.Contains(code(err), "credential") {
		t.Fatal(err)
	}
}

type fakeEvalAPI struct {
	records map[string]any
	calls   []any
}

func (a *fakeEvalAPI) get(collection string, scope map[string]any, id string) (any, error) {
	return a.records[collection], nil
}

func (a *fakeEvalAPI) request(path string, payload any) (any, error) {
	a.calls = append(a.calls, []any{path, payload})
	return map[string]any{"evaluation": map[string]any{"id": "one"}}, nil
}

func TestPrepareRejectsChangedCandidateBaselineAndContent(t *testing.T) {
	f := newEvalFixture(t, "adapter-echo")
	plan := map[string]any{"baseline_version": ""}
	for k, v := range f.plan {
		plan[k] = v
	}
	candidate := func() map[string]any {
		return map[string]any{"id": "candidate", "scope": f.prepared["scope"], "revision": 1, "status": "candidate", "skill_id": "verify",
			"comparison_plan": plan, "analysis": map[string]any{"content_hash": digest([]byte("verify"))}, "sources": []any{map[string]any{"source_id": "source"}}}
	}
	proposal := map[string]any{"record": candidate(), "comparison_plan_sha256": strings.Repeat("a", 64)}
	for _, change := range []string{"unchanged", "revision", "baseline", "content"} {
		records := map[string]any{"skill_versions": candidate(), "skills": map[string]any{"status": "active", "active_version": ""}, "sources": map[string]any{"text": "verify"}}
		switch change {
		case "revision":
			obj(records["skill_versions"])["revision"] = 2
		case "baseline":
			obj(records["skills"])["active_version"] = "other"
		case "content":
			obj(records["sources"])["text"] = "changed"
		}
		got, err := prepare(&fakeEvalAPI{records: records}, proposal)
		if change == "unchanged" {
			if err != nil || obj(got["skills"])["candidate"] != "verify" {
				t.Fatal(got, err)
			}
		} else if code(err) == "" || code(err) == "other" {
			t.Errorf("%s: %v", change, err)
		}
	}
}

func bundleOf(files ...map[string]any) map[string]any {
	var manifest strings.Builder
	var items []any
	for _, f := range files {
		manifest.WriteString(f["path"].(string) + "\n" + f["sha256"].(string) + "\n")
		items = append(items, f)
	}
	return map[string]any{"sha256": digest([]byte(manifest.String())), "files": items}
}

func textFile(path, content string) map[string]any {
	return map[string]any{"path": path, "sha256": digest([]byte(content)), "content": content}
}

func TestCompanionFilesAreExecutedOrCannotPass(t *testing.T) {
	f := newEvalFixture(t, "adapter-companion")
	f.env["companions"] = "skill_dir"
	f.repin()
	dataset, env, err := f.validate()
	if err != nil {
		t.Fatal(err)
	}
	obj(f.prepared["skills"])["baseline"] = "verify"
	candidateBundle := bundleOf(textFile("scripts/answer.txt", "right"))
	f.prepared["bundles"] = map[string]any{"baseline": bundleOf(textFile("scripts/answer.txt", "wrong")), "candidate": candidateBundle}
	result, err := evaluate(f.prepared, dataset, env)
	if err != nil {
		t.Fatal(err)
	}
	report := obj(result["comparison_report"])
	if report["companion_coverage"] != "materialized" || mustCanonical(report["baseline_score"]) != "0.0" || mustCanonical(report["candidate_score"]) != "1.0" ||
		result["passed"] != true || obj(report["bundle_sha256"])["candidate"] != candidateBundle["sha256"] {
		t.Fatal(mustCanonical(report))
	}
	// A hash-only companion cannot be supplied, so changed companions cannot pass.
	obj(f.prepared["bundles"])["candidate"] = bundleOf(textFile("scripts/answer.txt", "right"),
		map[string]any{"path": "z.bin", "sha256": strings.Repeat("ab", 32), "size": 3, "omitted": "binary"})
	result, err = evaluate(f.prepared, dataset, env)
	if err != nil || obj(result["comparison_report"])["companion_coverage"] != "partial" || result["passed"] != false {
		t.Fatal(result, err)
	}
}

func TestBodyOnlyAdapterCannotVouchForChangedCompanions(t *testing.T) {
	f := newEvalFixture(t, "adapter-echo")
	dataset, env, err := f.validate()
	if err != nil {
		t.Fatal(err)
	}
	same := bundleOf(textFile("scripts/run.py", "print(1)"))
	f.prepared["bundles"] = map[string]any{"baseline": same, "candidate": same}
	if result, err := evaluate(f.prepared, dataset, env); err != nil || result["passed"] != true {
		t.Fatal(result, err)
	}
	obj(f.prepared["bundles"])["candidate"] = bundleOf(textFile("scripts/run.py", "print(2)"))
	result, err := evaluate(f.prepared, dataset, env)
	report := obj(result["comparison_report"])
	if err != nil || report["companion_coverage"] != "body_only" || mustCanonical(report["candidate_score"]) != "1.0" || result["passed"] != false {
		t.Fatal(result, err)
	}
}

func TestPrepareBindsEachVersionBundle(t *testing.T) {
	good := bundleOf(textFile("scripts/run.py", "print(1)"))
	if b, err := pinnedBundle(nil, ""); b != nil || err != nil {
		t.Fatal(b, err)
	}
	if b, err := pinnedBundle(good, good["sha256"]); err != nil || !same(b, good) {
		t.Fatal(b, err)
	}
	bad := map[string]any{"sha256": strings.Repeat("0", 64), "files": good["files"]}
	for _, c := range [][2]any{{nil, good["sha256"]}, {good, ""}, {good, strings.Repeat("0", 64)}, {bad, strings.Repeat("0", 64)}} {
		if _, err := pinnedBundle(c[0], c[1]); code(err) != "bundle_changed" {
			t.Errorf("%v: %v", c, err)
		}
	}
}

func TestDescendantsAreStoppedAfterAdapterReturns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no process groups: killGroup (proc_other.go) stops only the adapter itself; stopping its descendants would need a Job Object")
	}
	f := newEvalFixture(t, "adapter-spawn")
	marker := filepath.Join(f.dir, "descendant")
	f.env["argv"] = append(list(f.env["argv"]), marker)
	if _, _, _, _, err := executeAdapter(f.env, map[string]any{"input": "task", "skill": "skill"}, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if exists(marker) {
		t.Fatal("descendant outlived the adapter")
	}
}

func TestPublishIsAtomicAndUsesStableEventID(t *testing.T) {
	f := newEvalFixture(t, "adapter-echo")
	result, err := evaluate(f.prepared, f.dataset, f.env)
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeEvalAPI{}
	first, _ := publish(api, f.prepared, result)
	roundTrip, _ := strictJSON([]byte(mustCanonical(result)))
	again, _ := publish(api, f.prepared, obj(roundTrip))
	if !same(first, again) || mustCanonical(api.calls[0]) != mustCanonical(api.calls[1]) || list(api.calls[0])[0] != "/v1/skills/comparisons/record" {
		t.Fatal(mustCanonical(api.calls))
	}
}

func TestEvaluateCommandReadyAndPublish(t *testing.T) {
	f := newEvalFixture(t, "adapter-echo")
	plan := map[string]any{"baseline_version": ""}
	for k, v := range f.plan {
		plan[k] = v
	}
	record := map[string]any{"id": "cand/1", "scope": map[string]any{"workspace_id": "w s", "project_id": "p"}, "revision": 1, "status": "candidate",
		"skill_id": "verify", "comparison_plan": plan, "analysis": map[string]any{"content_hash": digest([]byte("verify"))}, "sources": []any{map[string]any{"source_id": "source"}}}
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.RequestURI())
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(403)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/skill_versions/"):
			_, _ = w.Write([]byte(mustCanonical(record)))
		case strings.HasPrefix(r.URL.Path, "/v1/skills/comparisons/record"):
			_, _ = w.Write([]byte(`{"evaluation":{"id":"one"}}`))
		case strings.HasPrefix(r.URL.Path, "/v1/skills/"):
			_, _ = w.Write([]byte(`{"status":"active","active_version":""}`))
		default:
			_, _ = w.Write([]byte(`{"text":"verify"}`))
		}
	}))
	defer srv.Close()
	write := func(name string, v any) string {
		p := filepath.Join(f.dir, name)
		_ = os.WriteFile(p, []byte(mustCanonical(v)), 0o600)
		return p
	}
	proposal := write("proposal.json", map[string]any{"record": record, "comparison_plan_sha256": strings.Repeat("a", 64)})
	token := filepath.Join(f.dir, "token")
	_ = os.WriteFile(token, []byte("secret\n"), 0o600)
	base := []string{"--proposal", proposal, "--token-file", token, "--url", srv.URL,
		"--dataset", write("dataset.json", f.dataset), "--environment", write("env.json", f.env)}
	var out, errOut strings.Builder
	if rc := evaluateMain(append(base, "--publish"), nil, &out, &errOut); rc != 2 {
		t.Fatal(rc, errOut.String())
	}
	if rc := evaluateMain(base, nil, &out, &errOut); rc != 0 || out.String() != `{"candidate":{"collection":"skill_versions","id":"cand/1","revision":1},"cases":3,"ready":true}`+"\n" {
		t.Fatal(rc, out.String(), errOut.String())
	}
	if paths[0] != "GET /v1/skill_versions/cand%2F1?project_id=p&workspace_id=w+s" {
		t.Fatal(paths)
	}
	out.Reset()
	output := filepath.Join(f.dir, "result.json")
	if rc := evaluateMain(append(base, "--execute", "--publish", "--output", output), nil, &out, &errOut); rc != 0 ||
		!strings.Contains(out.String(), `"passed":true,"published":true`) || !exists(output+".receipt") {
		t.Fatal(rc, out.String(), errOut.String())
	}
	errOut.Reset()
	if rc := evaluateMain(append(base, "--execute", "--output", output), nil, &out, &errOut); rc != 1 || errOut.String() != "output_already_exists\n" {
		t.Fatal(rc, errOut.String())
	}
}

func TestReceiptReplayDoesNotOverwriteConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report")
	if err := saveJSON(path, map[string]any{"passed": true}); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 { // Windows has no mode bits
		t.Fatal(st.Mode())
	}
	if err := saveJSON(path, map[string]any{"passed": false}); !errors.Is(err, fs.ErrExist) {
		t.Fatal(err)
	}
	first, second := saveReceipt(path, map[string]any{"evaluation": "one"}), saveReceipt(path, map[string]any{"evaluation": "one"})
	if first != nil || second != nil {
		t.Fatal("replay refused")
	}
	if err := saveReceipt(path, map[string]any{"evaluation": "two"}); code(err) != "receipt_conflict" {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path + ".receipt")
	if strings.TrimSpace(string(raw)) != `{"evaluation":"one"}` {
		t.Fatal(string(raw))
	}
}
