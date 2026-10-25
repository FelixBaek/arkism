package crew

// Execute pinned skill comparisons; publish measured evidence, never approval.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

type comparisonError struct{ code string }

func (e *comparisonError) Error() string { return e.code }

func cmpErr(code string) error { return &comparisonError{code} }

// activeAdapter is the running adapter's process group, so a terminated worker can stop it.
var activeAdapter atomic.Int64

func utcNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// finiteCost is a non-negative finite int or float (never a bool).
func finiteCost(v any) bool {
	if _, ok := v.(bool); ok {
		return false
	}
	f, ok := num(v)
	return ok && !math.IsInf(f, 0) && !math.IsNaN(f) && f >= 0
}

var (
	caseID   = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,128}$`)
	envKey   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	sha256Re = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

func validateInputs(datasetRaw, environmentRaw []byte, plan map[string]any, token string) (dataset, env map[string]any, err error) {
	defer catch(&err)
	if digest(datasetRaw) != at(plan, "dataset_sha256") || digest(environmentRaw) != at(plan, "environment_sha256") {
		return nil, nil, cmpErr("plan_material_changed")
	}
	d, err := strictJSON(datasetRaw)
	if err != nil {
		return nil, nil, err
	}
	e, err := strictJSON(environmentRaw)
	if err != nil {
		return nil, nil, err
	}
	dataset, env = obj(d), obj(e)
	if dataset == nil || env == nil {
		return nil, nil, errValue
	}
	if err := validateDataset(dataset); err != nil {
		return nil, nil, err
	}
	if err := validateArgv(env); err != nil {
		return nil, nil, err
	}
	if err := validateChildEnv(env, token); err != nil {
		return nil, nil, err
	}
	if err := verifyFiles(env); err != nil {
		return nil, nil, err
	}
	return dataset, env, nil
}

func validateDataset(dataset map[string]any) error {
	cases, ok := dataset["cases"].([]any)
	if !keysAre(dataset, "cases") || !ok || len(cases) < 3 || len(cases) > 64 {
		return cmpErr("invalid_dataset")
	}
	categories, heldOut, err := caseCoverage(cases)
	if err != nil {
		return err
	}
	if len(categories) != 3 || !categories[`"quality"`] || !categories[`"regression"`] || !categories[`"safety"`] || !heldOut {
		return cmpErr("missing_evaluation_coverage")
	}
	return nil
}

func caseCoverage(cases []any) (categories map[string]bool, heldOut bool, err error) {
	seen := map[string]bool{}
	categories = map[string]bool{}
	for _, c := range cases {
		cs := obj(c)
		id, idOK := cs["id"].(string)
		held, heldOK := cs["held_out"].(bool)
		if !keysAre(cs, "id", "category", "input", "expected", "held_out") || !idOK || !caseID.MatchString(id) || seen[id] || !heldOK {
			return nil, false, cmpErr("invalid_case")
		}
		seen[id] = true
		category := mustCanonical(cs["category"]) // categories may be any JSON value
		categories[category] = true
		heldOut = heldOut || category == `"quality"` && held
	}
	return categories, heldOut, nil
}

func validateArgv(env map[string]any) error {
	rest := map[string]any{}
	for k, v := range env {
		if k != "companions" {
			rest[k] = v
		}
	}
	argv, argvOK := env["argv"].([]any)
	if !keysAre(rest, "argv", "files", "env", "timeout_ms", "max_output_bytes") || !same(getOr(env, "companions", "skill_dir"), "skill_dir") || !argvOK || len(argv) == 0 {
		return cmpErr("invalid_environment")
	}
	for _, x := range argv {
		if s, ok := x.(string); !ok || s == "" || strings.Contains(s, "\x00") {
			return cmpErr("invalid_environment")
		}
	}
	files, filesOK := env["files"].(map[string]any)
	argv0 := argv[0].(string)
	if _, pinned := files[argv0]; !filepath.IsAbs(argv0) || !filesOK || !pinned || len(files) < 1 || len(files) > 64 {
		return cmpErr("unpinned_executable")
	}
	return pinnedArguments(argv, files)
}

func pinnedArguments(argv []any, files map[string]any) error {
	for _, x := range argv {
		arg := x.(string)
		if st, err := os.Stat(arg); filepath.IsAbs(arg) && err == nil && st.Mode().IsRegular() {
			if _, pinned := files[arg]; !pinned {
				return cmpErr("unpinned_argument_file")
			}
		}
	}
	return nil
}

func validateChildEnv(env map[string]any, token string) error {
	child, childOK := env["env"].(map[string]any)
	for k, v := range child {
		if s, ok := v.(string); !ok || !envKey.MatchString(k) || strings.Contains(s, "\x00") {
			childOK = false
		}
	}
	if !childOK {
		return cmpErr("invalid_child_environment")
	}
	if token != "" && strings.Contains(mustCanonical(env), token) {
		return cmpErr("evaluator_credential_in_adapter")
	}
	timeout, tOK := host.Int(env["timeout_ms"])
	limit, lOK := host.Int(env["max_output_bytes"])
	if !tOK || timeout < 1 || timeout > 3600000 || !lOK || limit < 128 || limit > 1048576 {
		return cmpErr("invalid_execution_limits")
	}
	return nil
}

func verifyFiles(env map[string]any) error {
	for path, expected := range obj(env["files"]) {
		want, ok := expected.(string)
		if !filepath.IsAbs(path) || !ok || !sha256Re.MatchString(want) {
			return cmpErr("invalid_pinned_file")
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		if hex.EncodeToString(h.Sum(nil)) != want {
			return cmpErr("environment_file_changed")
		}
	}
	return nil
}

// executeAdapter runs trusted local code in a fresh cwd; this is not an OS sandbox. files (relative
// path -> bytes) is the variant's pinned skill folder, written under cwd/skill and announced to the
// adapter as skill_dir.
func executeAdapter(env map[string]any, payload map[string]any, files map[string][]byte) (result map[string]any, elapsed int64, started, finished string, err error) {
	if err = verifyFiles(env); err != nil {
		return
	}
	started = utcNow()
	limit, _ := host.Int(env["max_output_bytes"])
	timeoutMS, _ := host.Int(env["timeout_ms"])
	cwd, err := os.MkdirTemp("", "arkism-compare-")
	if err != nil {
		return
	}
	defer os.RemoveAll(cwd)
	if files != nil {
		if payload, err = writeSkillDir(cwd, payload, files); err != nil {
			return
		}
	}
	stdout, err := os.CreateTemp("", "arkism-compare-out-")
	if err != nil {
		return
	}
	defer os.Remove(stdout.Name())
	defer stdout.Close()
	argv := list(env["argv"])
	cmd := exec.Command(argv[0].(string))
	for _, a := range argv[1:] {
		cmd.Args = append(cmd.Args, a.(string))
	}
	cmd.Env = []string{}
	for _, k := range sortedKeys(obj(env["env"])) {
		cmd.Env = append(cmd.Env, k+"="+obj(env["env"])[k].(string))
	}
	cmd.Dir, cmd.Stdin, cmd.Stdout = cwd, strings.NewReader(mustCanonical(payload)), stdout
	newSession(cmd)
	began := time.Now()
	if err = cmd.Start(); err != nil {
		return
	}
	activeAdapter.Store(int64(cmd.Process.Pid))
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waited := false
	func() {
		defer func() {
			activeAdapter.Store(0)
			killGroup(cmd.Process.Pid, sigKill)
			if !waited {
				<-done
			}
		}()
		for {
			if st, e := stdout.Stat(); e == nil && st.Size() > limit {
				err = cmpErr("adapter_output_limit")
				return
			}
			if time.Since(began) > time.Duration(timeoutMS)*time.Millisecond {
				err = cmpErr("adapter_timeout")
				return
			}
			select {
			case waitErr := <-done:
				waited = true
				if waitErr != nil {
					err = cmpErr("adapter_failed")
				}
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	if err != nil {
		return
	}
	elapsed = int64(math.Ceil(float64(time.Since(began).Microseconds()) / 1000))
	if _, err = stdout.Seek(0, 0); err != nil {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(stdout, limit+1))
	if err != nil {
		return
	}
	if err = verifyFiles(env); err != nil {
		return
	}
	if int64(len(raw)) > limit {
		err = cmpErr("adapter_output_limit")
		return
	}
	parsed, err := strictJSON(raw)
	if err != nil {
		return
	}
	result = obj(parsed)
	if result == nil || !keysAre(result, "output", "cost") || !finiteCost(result["cost"]) {
		return nil, 0, "", "", cmpErr("invalid_adapter_measurement")
	}
	return result, elapsed, started, utcNow(), nil
}

// writeSkillDir writes files under cwd/skill and returns payload with skill_dir set.
func writeSkillDir(cwd string, payload map[string]any, files map[string][]byte) (map[string]any, error) {
	root := filepath.Join(cwd, "skill")
	for relative, raw := range files {
		p := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(p), 0o777); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, raw, 0o666); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(root, 0o777); err != nil {
		return nil, err
	}
	withDir := map[string]any{"skill_dir": root}
	for k, v := range payload {
		withDir[k] = v
	}
	withDir["skill_dir"] = root
	return withDir, nil
}

// comparisonAPI is the Arkism API as the comparison runner sees it (a fake in tests).
type comparisonAPI interface {
	get(collection string, scope map[string]any, id string) (any, error)
	request(path string, payload any) (any, error)
}

type evalAPI struct {
	url, token string
	client     *http.Client
}

func newEvalAPI(raw, token string) (*evalAPI, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errValue
	}
	h := u.Hostname()
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		!(u.Scheme == "https" || u.Scheme == "http" && (h == "localhost" || h == "127.0.0.1" || h == "::1")) {
		return nil, cmpErr("invalid_api_url")
	}
	client := &http.Client{
		Transport:     &http.Transport{Proxy: http.ProxyFromEnvironment},
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") },
	}
	return &evalAPI{url: strings.TrimRight(raw, "/"), token: token, client: client}, nil
}

func (a *evalAPI) request(path string, payload any) (any, error) {
	method, body := http.MethodGet, io.Reader(nil)
	if payload != nil {
		method, body = http.MethodPost, strings.NewReader(mustCanonical(payload))
	}
	req, err := http.NewRequest(method, a.url+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &host.HTTPError{Code: resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2097153))
	if err != nil {
		return nil, err
	}
	if len(raw) > 2097152 {
		return nil, cmpErr("api_response_limit")
	}
	return strictJSON(raw)
}

func (a *evalAPI) get(collection string, scope map[string]any, id string) (any, error) {
	return a.request("/v1/"+collection+"/"+pyQuote(id, "")+"?"+urlencode(scope), nil)
}

// urlencode is urllib.parse.urlencode for a flat object (keys sorted: Go maps keep no order).
func urlencode(m map[string]any) string {
	parts := []string{}
	for _, k := range sortedKeys(m) {
		parts = append(parts, quotePlus(k)+"="+quotePlus(pyStr(m[k])))
	}
	return strings.Join(parts, "&")
}

func quotePlus(s string) string { return strings.ReplaceAll(pyQuote(s, " "), " ", "+") }

var sensitivityLevel = map[string]int{"public": 0, "internal": 1, "private": 2, "restricted": 3}

func level(v any) int {
	if s, ok := v.(string); ok {
		if n, ok := sensitivityLevel[s]; ok {
			return n
		}
	}
	return 3
}

func orDefault(v, def any) any {
	if truthy(v) {
		return v
	}
	return def
}

func prepare(api comparisonAPI, proposal map[string]any) (map[string]any, error) {
	return prepareTrial(api, proposal, "")
}

// prepareTrial pins a comparison; inside a skill trial the candidate is already approved.
func prepareTrial(api comparisonAPI, proposal map[string]any, trialID string) (out map[string]any, err error) {
	defer catch(&err)
	status := "candidate"
	if trialID != "" {
		status = "approved"
	}
	expected := obj(at(proposal, "record"))
	scope := obj(at(expected, "scope"))
	c, err := api.get("skill_versions", scope, mustStr(at(expected, "id")))
	if err != nil {
		return nil, err
	}
	candidate := obj(c)
	plan := getOr(candidate, "comparison_plan", nil)
	if !same(at(candidate, "revision"), at(expected, "revision")) || !same(at(candidate, "status"), status) ||
		!same(plan, getOr(expected, "comparison_plan", nil)) || !truthy(plan) ||
		!same(at(at(candidate, "analysis"), "content_hash"), at(at(expected, "analysis"), "content_hash")) {
		return nil, cmpErr("candidate_changed")
	}
	p, err := api.get("skills", scope, mustStr(at(candidate, "skill_id")))
	if err != nil {
		return nil, err
	}
	if !same(getOr(p, "status", nil), "active") || !same(getOr(p, "active_version", ""), at(plan, "baseline_version")) {
		return nil, cmpErr("baseline_changed")
	}
	s, err := api.get("sources", scope, mustStr(at(list(at(candidate, "sources"))[0], "source_id")))
	if err != nil {
		return nil, err
	}
	content := mustStr(at(s, "text"))
	if !same(digest([]byte(content)), at(at(candidate, "analysis"), "content_hash")) {
		return nil, cmpErr("candidate_content_changed")
	}
	cb, err := pinnedBundle(getOr(s, "skill_bundle", nil), getOr(candidate, "skill_bundle_sha256", ""))
	if err != nil {
		return nil, err
	}
	bundles := map[string]any{"baseline": nil, "candidate": cb}
	baseline, baselineID, baselineRevision := "", "", any(0)
	sensitivity := orDefault(getOr(candidate, "sensitivity", nil), "private")
	if truthy(at(plan, "baseline_version")) {
		if baseline, baselineID, baselineRevision, sensitivity, err = pinBaseline(api, scope, candidate, plan, sensitivity, bundles); err != nil {
			return nil, err
		}
	}
	planSHA, ok := at(proposal, "comparison_plan_sha256").(string)
	if !ok {
		return nil, errValue
	}
	if !sha256Re.MatchString(planSHA) {
		return nil, cmpErr("invalid_plan_hash")
	}
	out = map[string]any{"scope": scope, "candidate": map[string]any{"collection": "skill_versions", "id": at(candidate, "id"), "revision": at(candidate, "revision")},
		"sensitivity": sensitivity, "baseline_revision": baselineRevision, "baseline_id": baselineID, "plan": plan, "plan_sha256": planSHA,
		"skills": map[string]any{"baseline": baseline, "candidate": content}, "bundles": bundles}
	if trialID != "" {
		out["trial_id"] = trialID
	}
	return out, nil
}

// pinBaseline resolves the plan's baseline version, records its bundle in bundles, and
// raises sensitivity to the baseline's when it is stricter.
func pinBaseline(api comparisonAPI, scope, candidate map[string]any, plan, sensitivity any, bundles map[string]any) (baseline, baselineID string, baselineRevision, raised any, err error) {
	r, err := api.request("/v1/skills/resolve", map[string]any{"scope": scope, "refs": []any{mustStr(at(candidate, "skill_id")) + "@latest-approved"}})
	if err != nil {
		return "", "", nil, nil, err
	}
	resolved := list(at(r, "items"))[0]
	if truthy(getOr(resolved, "error", nil)) || !same(getOr(resolved, "version_id", nil), at(plan, "baseline_version")) {
		return "", "", nil, nil, cmpErr("baseline_unavailable")
	}
	baseline, baselineID = mustStr(at(resolved, "instructions")), mustStr(at(resolved, "version_id"))
	record, err := api.get("skill_versions", scope, baselineID)
	if err != nil {
		return "", "", nil, nil, err
	}
	baselineRevision = at(record, "revision")
	if other := orDefault(getOr(record, "sensitivity", nil), "private"); level(other) > level(sensitivity) {
		sensitivity = other
	}
	if !same(digest([]byte(baseline)), at(resolved, "content_hash")) {
		return "", "", nil, nil, cmpErr("baseline_content_changed")
	}
	if bundles["baseline"], err = pinnedBundle(getOr(resolved, "bundle", nil), getOr(record, "skill_bundle_sha256", "")); err != nil {
		return "", "", nil, nil, err
	}
	return baseline, baselineID, baselineRevision, sensitivity, nil
}

// pinnedBundle is the version's companion bundle, which must be exactly the one its record pins
// ("" = unpinned). Stored bytes and the manifest hash are rechecked here.
func pinnedBundle(bundle, expected any) (out any, err error) {
	if bundle == nil && !truthy(expected) {
		return nil, nil
	}
	ok := func() (ok bool) {
		defer func() {
			if recover() != nil {
				ok = false
			}
		}()
		files, isList := at(bundle, "files").([]any)
		ok = isList
		var manifest strings.Builder
		for _, f := range files {
			m := obj(f)
			if keysAre(m, "path", "sha256", "content") {
				ok = ok && digest([]byte(mustStr(m["content"]))) == m["sha256"]
			} else {
				hash, isStr := m["sha256"].(string)
				ok = ok && keysAre(m, "path", "sha256", "size", "omitted") && isStr && sha256Re.MatchString(hash)
			}
		}
		for _, f := range files {
			manifest.WriteString(mustStr(at(f, "path")) + "\n" + mustStr(at(f, "sha256")) + "\n")
		}
		got := at(bundle, "sha256")
		return ok && same(digest([]byte(manifest.String())), got) && same(got, expected)
	}()
	if !ok {
		return nil, cmpErr("bundle_changed")
	}
	return bundle, nil
}

func skillFolder(text string, bundle any) map[string][]byte {
	files := map[string][]byte{"SKILL.md": []byte(text)}
	if bundle != nil {
		for _, f := range list(at(bundle, "files")) {
			m := obj(f)
			if _, ok := m["content"]; ok && m["path"] != "SKILL.md" {
				files[mustStr(m["path"])] = []byte(mustStr(m["content"]))
			}
		}
	}
	return files
}

func evaluate(prepared, dataset, env map[string]any) (out map[string]any, err error) {
	defer catch(&err)
	bundles := obj(prepared["bundles"])
	if !truthy(bundles) {
		bundles = map[string]any{"baseline": nil, "candidate": nil}
	}
	materialize := same(env["companions"], "skill_dir")
	coverage := companionCoverage(materialize, bundles)
	skills := obj(at(prepared, "skills"))
	rows, err := runCases(dataset, env, skills, bundles, materialize)
	if err != nil {
		return nil, err
	}
	score := func(variant string) any {
		passed, total := 0, 0
		for _, r := range rows {
			m := r.(map[string]any)
			if m["variant"] == variant && same(m["category"], "quality") {
				total++
				if m["passed"] == true {
					passed++
				}
			}
		}
		if total == 0 {
			panic(errValue)
		}
		return pyFloat(float64(passed) / float64(total))
	}
	costs, duration, regression, safety := candidateTotals(rows)
	cost, err := pySum(costs)
	if err != nil {
		return nil, err
	}
	bundleSHA := map[string]any{}
	for _, v := range []string{"baseline", "candidate"} {
		bundleSHA[v] = ""
		if b := bundles[v]; truthy(b) {
			bundleSHA[v] = getOr(b, "sha256", "")
		}
	}
	report := map[string]any{"plan_sha256": at(prepared, "plan_sha256"), "baseline_score": score("baseline"), "candidate_score": score("candidate"),
		"candidate_cost": cost, "candidate_duration_ms": duration, "regression_passed": regression, "safety_passed": safety,
		"bundle_sha256": bundleSHA, "companion_coverage": coverage}
	if !finiteCost(cost) {
		return nil, cmpErr("invalid_total_cost")
	}
	plan := at(prepared, "plan")
	number := func(v any) float64 {
		f, ok := num(v)
		if !ok {
			panic(errValue)
		}
		return f
	}
	covers := same(bundleSHA["candidate"], bundleSHA["baseline"]) || coverage == "materialized"
	base, cand := number(report["baseline_score"]), number(report["candidate_score"])
	passed := covers && cand-base+1e-12 >= number(at(plan, "min_score_gain")) && number(cost) <= number(at(plan, "max_cost")) &&
		float64(duration) <= number(at(plan, "max_duration_ms")) && regression && safety
	contentSHA := map[string]any{}
	for k, v := range skills {
		contentSHA[k] = digest([]byte(mustStr(v)))
	}
	return map[string]any{"format_version": 1, "candidate": at(prepared, "candidate"), "baseline_id": at(prepared, "baseline_id"),
		"baseline_revision": getOr(prepared, "baseline_revision", 0), "content_sha256": contentSHA, "dataset_sha256": at(plan, "dataset_sha256"),
		"environment_sha256": at(plan, "environment_sha256"), "comparison_report": report, "passed": passed, "cases": rows}, nil
}

// Only an adapter that receives every pinned file can vouch for changed companions.
func companionCoverage(materialize bool, bundles map[string]any) string {
	if !materialize {
		return "body_only"
	}
	coverage := "materialized"
	for _, v := range []string{"baseline", "candidate"} {
		if b := bundles[v]; truthy(b) {
			for _, f := range list(at(b, "files")) {
				if _, ok := obj(f)["omitted"]; ok {
					coverage = "partial"
				}
			}
		}
	}
	return coverage
}

func runCases(dataset, env, skills, bundles map[string]any, materialize bool) ([]any, error) {
	var rows []any
	for index, c := range list(at(dataset, "cases")) {
		variants := []string{"baseline", "candidate"}
		if index%2 == 1 {
			variants = []string{"candidate", "baseline"}
		}
		for _, variant := range variants {
			skill := mustStr(at(skills, variant))
			var files map[string][]byte
			if materialize {
				files = skillFolder(skill, at(bundles, variant))
			}
			result, duration, started, finished, err := executeAdapter(env, map[string]any{"input": at(c, "input"), "skill": skill}, files)
			if err != nil {
				return nil, err
			}
			output := mustCanonical(result["output"])
			rows = append(rows, map[string]any{"case_sha256": digest([]byte(mustCanonical(c))), "category": at(c, "category"), "variant": variant,
				"passed": output == mustCanonical(at(c, "expected")), "output_sha256": digest([]byte(output)), "cost": result["cost"],
				"duration_ms": duration, "started_at": started, "finished_at": finished})
		}
	}
	return rows, nil
}

func candidateTotals(rows []any) (costs []any, duration int64, regression, safety bool) {
	regression, safety = true, true
	for _, r := range rows {
		m := r.(map[string]any)
		if m["variant"] != "candidate" {
			continue
		}
		costs = append(costs, m["cost"])
		duration += m["duration_ms"].(int64)
		if same(m["category"], "regression") && m["passed"] != true {
			regression = false
		}
		if same(m["category"], "safety") && m["passed"] != true {
			safety = false
		}
	}
	return costs, duration, regression, safety
}

func publish(api comparisonAPI, prepared, result map[string]any) (any, error) {
	payload := map[string]any{"scope": prepared["scope"], "event_id": digest([]byte(mustCanonical(result))), "result": result}
	if trialID, _ := prepared["trial_id"].(string); trialID != "" {
		payload["event_id"], payload["trial_id"] = digest([]byte(mustCanonical([]any{trialID, result}))), trialID
	}
	return api.request("/v1/skills/comparisons/record", payload)
}

// saveJSON publishes atomically through a hard link, so another writer's file is never overwritten
// (an existing path fails with fs.ErrExist).
func saveJSON(path string, value any) error {
	text, err := canonical(value)
	if err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(path), ".comparison-")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	_, err = out.WriteString(text + "\n")
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Link(out.Name(), path); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		// A directory handle cannot be flushed on Windows (FlushFileBuffers fails with Access is denied); NTFS journals
		// the new name itself, so the file sync above is all there is to do.
		return nil
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func saveReceipt(path string, receipt any) error {
	path += ".receipt"
	err := saveJSON(path, receipt)
	if errors.Is(err, fs.ErrExist) {
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		stored, jerr := strictJSON(raw)
		if jerr != nil {
			return jerr
		}
		if !same(stored, receipt) {
			return cmpErr("receipt_conflict")
		}
		return nil
	}
	return err
}

func readJSONArg(path string) (any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strictJSON(raw)
}

const evaluateUsage = "[-h] --proposal PROPOSAL --token-file TOKEN_FILE [--dataset DATASET] [--environment ENVIRONMENT] [--resume-result RESUME_RESULT] [--url URL] [--execute] [--publish] [--output OUTPUT]"

func evaluateMain(argv []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	const prog = "evaluate-skill-candidate"
	a, err := parseArgs(argv, []string{"proposal", "token-file", "dataset", "environment", "resume-result", "url", "output"}, []string{"execute", "publish"}, nil)
	if err == nil && len(a.pos) > 0 {
		err = fmt.Errorf("unrecognized arguments: %s", strings.Join(a.pos, " "))
	}
	if err == nil {
		err = checkEvaluateArgs(a)
	}
	if err != nil {
		return usageError(stderr, prog, evaluateUsage, err)
	}
	apiURL := a.vals["url"]
	if !a.has("url") {
		apiURL = host.DefaultURL
	}
	defer func() {
		if recover() != nil {
			fmt.Fprintln(stderr, "comparison_failed")
			code = 1
		}
	}()
	out, err := runEvaluate(a, apiURL)
	if err != nil {
		var ce *comparisonError
		if errors.As(err, &ce) {
			fmt.Fprintln(stderr, ce.code)
		} else {
			fmt.Fprintln(stderr, "comparison_failed")
		}
		return 1
	}
	fmt.Fprintln(stdout, mustCanonical(out))
	return 0
}

func checkEvaluateArgs(a *args) error {
	if !a.has("proposal") || !a.has("token-file") {
		return errors.New("the following arguments are required: --proposal, --token-file")
	}
	if a.has("resume-result") {
		if a.flags["execute"] || a.has("output") || !a.flags["publish"] {
			return errors.New("--resume-result requires --publish and excludes --execute/--output")
		}
	} else if !a.has("dataset") || !a.has("environment") || a.flags["publish"] && !a.flags["execute"] || a.flags["execute"] && !a.has("output") {
		return errors.New("dataset/environment required; execution requires --output; publication requires --execute")
	}
	return nil
}

func runEvaluate(a *args, apiURL string) (map[string]any, error) {
	p, err := readJSONArg(a.vals["proposal"])
	if err != nil {
		return nil, err
	}
	proposal := obj(p)
	if proposal == nil {
		return nil, errValue
	}
	token, err := os.ReadFile(a.vals["token-file"])
	if err != nil {
		return nil, err
	}
	api, err := newEvalAPI(apiURL, strings.TrimSpace(string(token)))
	if err != nil {
		return nil, err
	}
	if path, ok := a.vals["resume-result"]; ok {
		return resumeEvaluation(api, proposal, path)
	}
	prepared, err := prepare(api, proposal)
	if err != nil {
		return nil, err
	}
	datasetRaw, err := os.ReadFile(a.vals["dataset"])
	if err != nil {
		return nil, err
	}
	environmentRaw, err := os.ReadFile(a.vals["environment"])
	if err != nil {
		return nil, err
	}
	plan := obj(prepared["plan"])
	if plan == nil {
		return nil, errValue
	}
	dataset, env, err := validateInputs(datasetRaw, environmentRaw, plan, api.token)
	if err != nil {
		return nil, err
	}
	if !a.flags["execute"] {
		return map[string]any{"ready": true, "cases": len(list(dataset["cases"])), "candidate": prepared["candidate"]}, nil
	}
	return executeEvaluation(a, api, proposal, prepared, dataset, env)
}

func resumeEvaluation(api *evalAPI, proposal map[string]any, path string) (map[string]any, error) {
	r, err := readJSONArg(path)
	if err != nil {
		return nil, err
	}
	result := obj(r)
	if result == nil || !same(result["format_version"], 1) {
		return nil, cmpErr("legacy_result_requires_review")
	}
	expected := obj(at(proposal, "record"))
	target := map[string]any{"collection": "skill_versions", "id": at(expected, "id"), "revision": at(expected, "revision")}
	if !same(at(result, "candidate"), target) || !same(at(at(result, "comparison_report"), "plan_sha256"), at(proposal, "comparison_plan_sha256")) {
		return nil, cmpErr("resume_target_mismatch")
	}
	receipt, err := publish(api, map[string]any{"scope": at(expected, "scope")}, result)
	if err != nil {
		return nil, err
	}
	if err := saveReceipt(path, receipt); err != nil {
		return nil, err
	}
	return map[string]any{"published": true, "resumed": true, "evaluation": at(receipt, "evaluation")}, nil
}

func executeEvaluation(a *args, api *evalAPI, proposal, prepared, dataset, env map[string]any) (map[string]any, error) {
	output := a.vals["output"]
	if exists(output) || exists(output+".receipt") {
		return nil, cmpErr("output_already_exists")
	}
	result, err := evaluate(prepared, dataset, env)
	if err != nil {
		return nil, err
	}
	again, err := prepare(api, proposal)
	if err != nil {
		return nil, err
	}
	if !same(again, prepared) {
		return nil, cmpErr("comparison_material_changed")
	}
	if err := saveJSON(output, result); err != nil {
		return nil, err
	}
	if a.flags["publish"] {
		receipt, err := publish(api, prepared, result)
		if err != nil {
			return nil, err
		}
		if err := saveReceipt(output, receipt); err != nil {
			return nil, err
		}
	}
	return map[string]any{"passed": result["passed"], "published": a.flags["publish"], "candidate": prepared["candidate"]}, nil
}

func init() { host.Register("evaluate-skill-candidate", evaluateMain) }
