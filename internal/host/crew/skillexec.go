//go:build unix

package crew

// Owner-SSH comparison jobs using registered profiles, never caller commands.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/FelixBaek/arkism/internal/host"
)

var (
	execKey   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	execScope = []string{"workspace_id", "project_id", "user_id", "agent_id"}
	errorCode = regexp.MustCompile(`^[a-z_]{1,80}$`)
)

const execLimit = 8192

// launchJob starts the background worker for a job (a fake in tests).
var launchJob = launch

func execConfigPath() string {
	return filepath.Join(host.Home, ".config/arkism-skill-execution/config.json")
}
func execStatePath() string { return filepath.Join(host.Home, ".local/state/arkism-skill-execution") }

func requestInvalid() error { return cmpErr("execution_request_invalid") }

func isKey(v any) bool {
	s, ok := v.(string)
	return ok && execKey.MatchString(s) && s != "." && s != ".."
}

func onlyKeys(m map[string]any, allowed ...string) bool {
	for k := range m {
		found := false
		for _, a := range allowed {
			found = found || a == k
		}
		if !found {
			return false
		}
	}
	return true
}

type execConfig struct {
	scope, profiles map[string]any
	order           []string // profile ids in file order
	clientURL       string
	api             *evalAPI
	fingerprint     string
}

func loadExecConfig(path string) (*execConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v, err := strictJSON(raw)
	if err != nil {
		return nil, err
	}
	config := obj(v)
	clientURL, _ := config["client_url"].(string)
	scope, scopeOK := config["scope"].(map[string]any)
	profiles, profilesOK := config["profiles"].(map[string]any)
	if config == nil || !keysAre(config, "scope", "url", "client_url", "token_file", "profiles") ||
		!(strings.HasPrefix(clientURL, "https://") || strings.HasPrefix(clientURL, "http://")) || !scopeOK || !onlyKeys(scope, execScope...) ||
		!profilesOK || len(profiles) > 64 || !validExecScope(scope) || !validExecProfiles(profiles) {
		return nil, requestInvalid()
	}
	api, err := execConfigAPI(config)
	if err != nil {
		return nil, err
	}
	return &execConfig{scope: scope, profiles: profiles, order: memberKeys(raw, "profiles"), clientURL: clientURL, api: api, fingerprint: digest(raw)}, nil
}

func validExecScope(scope map[string]any) bool {
	for _, k := range execScope {
		if _, ok := getOr(scope, k, "").(string); !ok {
			return false
		}
	}
	return truthy(scope["workspace_id"]) && truthy(scope["project_id"])
}

func validExecProfiles(profiles map[string]any) bool {
	for id, p := range profiles {
		profile := obj(p)
		name, nameOK := profile["name"].(string)
		if !isKey(id) || !keysAre(profile, "name", "dataset", "environment") || !nameOK || host.Len(name) < 1 || host.Len(name) > 128 {
			return false
		}
		for _, k := range []string{"dataset", "environment"} {
			if s, ok := profile[k].(string); !ok || !filepath.IsAbs(s) {
				return false
			}
		}
	}
	return true
}

func execConfigAPI(config map[string]any) (*evalAPI, error) {
	tokenFile, ok := config["token_file"].(string)
	if !ok || !filepath.IsAbs(tokenFile) {
		return nil, requestInvalid()
	}
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(token)) == "" {
		return nil, requestInvalid()
	}
	apiURL, ok := config["url"].(string)
	if !ok {
		return nil, errValue
	}
	return newEvalAPI(apiURL, strings.TrimSpace(string(token)))
}

func execCandidate(api comparisonAPI, scope map[string]any, target any) (map[string]any, error) {
	t := obj(target)
	revision, isInt := host.Int(t["revision"])
	if t == nil || !keysAre(t, "id", "revision") || !isKey(t["id"]) || !isInt || revision < 1 {
		return nil, requestInvalid()
	}
	r, err := api.get("skill_versions", scope, t["id"].(string))
	if err != nil {
		return nil, err
	}
	row, ok := r.(map[string]any)
	if !ok {
		return nil, errValue
	}
	changed := !same(row["id"], t["id"]) || !same(row["revision"], t["revision"]) || truthy(row["content_erased"]) || truthy(row["privacy_quarantined"])
	rowScope := getOr(row, "scope", map[string]any{})
	for _, k := range execScope {
		changed = changed || !same(getOr(rowScope, k, ""), getOr(scope, k, ""))
	}
	if changed {
		return nil, cmpErr("execution_candidate_changed")
	}
	return row, nil
}

func material(profile map[string]any, token string) (plan, dataset, env map[string]any, err error) {
	datasetRaw, err := os.ReadFile(profile["dataset"].(string))
	if err != nil {
		return
	}
	environmentRaw, err := os.ReadFile(profile["environment"].(string))
	if err != nil {
		return
	}
	plan = map[string]any{"dataset_sha256": digest(datasetRaw), "environment_sha256": digest(environmentRaw)}
	dataset, env, err = validateInputs(datasetRaw, environmentRaw, plan, token)
	return
}

// lockFile opens path and takes its flock; held=false when another holder has it (non-blocking).
func lockFile(path string, blocking bool) (f *os.File, held bool, err error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, false, err
	}
	f = os.NewFile(uintptr(fd), path)
	how := syscall.LOCK_EX
	if !blocking {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(fd, how); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return f, false, nil
		}
		f.Close()
		return nil, false, err
	}
	return f, true, nil
}

// launch takes the worker lock before spawning, closing the queued/running race: the child inherits
// this exact open-file lock, so another start/retry cannot launch a second one.
func launch(configPath, state, job string) error {
	f, held, err := lockFile(filepath.Join(state, job, "worker.lock"), false)
	if err != nil {
		return err
	}
	defer f.Close()
	if !held {
		return nil
	}
	cmd := exec.Command(host.Executable(), "skill-execution", "--config", configPath, "--state", state, "--worker", job, "--lock-fd", "3")
	cmd.ExtraFiles = []*os.File{f}
	newSession(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func jobStatus(folder, job string) (map[string]any, error) {
	out := map[string]any{"authority": "data_only_untrusted", "job_id": job}
	f, held, err := lockFile(filepath.Join(folder, "worker.lock"), false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if !held {
		out["status"] = "running"
		return out, nil
	}
	resultPath := filepath.Join(folder, "result.json")
	if exists(resultPath + ".receipt") {
		receipt, err := readJSONArg(resultPath + ".receipt")
		if err != nil {
			return nil, err
		}
		result, err := readJSONArg(resultPath)
		if err != nil {
			return nil, err
		}
		out["status"], out["passed"], out["report"], out["evaluation"] = "published", at(result, "passed"), at(result, "comparison_report"), at(receipt, "evaluation")
		return out, nil
	}
	if exists(resultPath) {
		out["status"] = "awaiting_publication"
		return out, nil
	}
	// No durable result: never silently run the paid model again.
	out["status"] = "interrupted_or_failed"
	return out, nil
}

func handleExecution(value any, configPath, state string) (out map[string]any, err error) {
	defer catch(&err)
	config, err := loadExecConfig(configPath)
	if err != nil {
		return nil, err
	}
	v := obj(value)
	if !validExecRequest(v, config) {
		return nil, requestInvalid()
	}
	action := v["action"]
	switch action {
	case "profiles":
		return execProfiles(v, config)
	case "history":
		return execHistory(v, config, state)
	case "start", "status", "publish":
	default:
		return nil, requestInvalid()
	}
	request, job, err := execJob(v, action, config)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		return nil, err
	}
	folder := filepath.Join(state, job)
	dispatch, _, err := lockFile(filepath.Join(state, "dispatch.lock"), true)
	if err != nil {
		return nil, err
	}
	defer dispatch.Close()
	if action == "start" && !exists(folder) {
		if err := startJob(config, request, configPath, state, job); err != nil {
			return nil, err
		}
	}
	if err := checkSavedRequest(config, request, folder); err != nil {
		return nil, err
	}
	if action == "publish" {
		resultPath := filepath.Join(folder, "result.json")
		if st, err := os.Stat(resultPath); err != nil || !st.Mode().IsRegular() {
			return nil, cmpErr("execution_result_unavailable")
		}
		if !exists(resultPath + ".receipt") {
			if err := launchJob(configPath, state, job); err != nil {
				return nil, err
			}
		}
	}
	return jobStatus(folder, job)
}

func validExecRequest(v map[string]any, config *execConfig) bool {
	vscope, scopeOK := v["scope"].(map[string]any)
	if v == nil || !onlyKeys(v, "action", "scope", "server", "profile_id", "candidate", "event_id", "job_id") ||
		!same(v["server"], config.clientURL) || !scopeOK || !onlyKeys(vscope, execScope...) {
		return false
	}
	for _, k := range execScope {
		if !same(getOr(vscope, k, ""), getOr(config.scope, k, "")) {
			return false
		}
	}
	return true
}

func execProfiles(v map[string]any, config *execConfig) (map[string]any, error) {
	if !keysAre(v, "action", "scope", "server") {
		return nil, requestInvalid()
	}
	items := []any{}
	for _, id := range config.order {
		profile := obj(config.profiles[id])
		pins, dataset, env, err := material(profile, config.api.token)
		if err != nil {
			return nil, err
		}
		cases := len(list(dataset["cases"]))
		timeout, _ := host.Int(env["timeout_ms"])
		items = append(items, map[string]any{"id": id, "name": profile["name"], "cases": cases,
			"dataset_sha256": pins["dataset_sha256"], "environment_sha256": pins["environment_sha256"],
			"default_plan": defaultPlan(cases, timeout)})
	}
	return map[string]any{"authority": "data_only_untrusted", "items": items}, nil
}

// defaultPlan is the pass rule the desktop proposes so the owner never types one: +5 points, the
// candidate within the sum of its per-case time limits, and no cost cap (cost units are the adapter's own).
func defaultPlan(cases int, timeoutMS int64) map[string]any {
	return map[string]any{"min_score_gain": 0.05, "max_cost": defaultMaxCost, "max_duration_ms": int64(max(cases, 1)) * timeoutMS}
}

// shortcut: "no cap" is a large finite number because the plan needs one; add a per-profile cap if adapters report real money.
const defaultMaxCost = 1e6

func execHistory(v map[string]any, config *execConfig, state string) (map[string]any, error) {
	scope := config.scope
	if !keysAre(v, "action", "scope", "server", "candidate") {
		return nil, requestInvalid()
	}
	if _, err := execCandidate(config.api, scope, v["candidate"]); err != nil {
		return nil, err
	}
	rows := []map[string]any{}
	entries, _ := os.ReadDir(state)
	for _, e := range entries {
		folder := filepath.Join(state, e.Name())
		if !hex64.MatchString(e.Name()) || !isDir(folder) {
			continue
		}
		saved, err := readJSONArg(filepath.Join(folder, "request.json"))
		if err != nil {
			return nil, err
		}
		request := at(saved, "request")
		if same(at(request, "scope"), scope) && same(at(request, "candidate"), v["candidate"]) && same(at(saved, "config_sha256"), config.fingerprint) {
			row, err := jobStatus(folder, e.Name())
			if err != nil {
				return nil, err
			}
			row["created_at"], row["profile_id"], row["event_id"] = at(saved, "created_at"), at(request, "profile_id"), at(request, "event_id")
			rows = append(rows, row)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := mustStr(rows[i]["created_at"]), mustStr(rows[j]["created_at"])
		if a != b {
			return a > b
		}
		return rows[i]["job_id"].(string) > rows[j]["job_id"].(string)
	})
	items := make([]any, len(rows))
	for i, r := range rows {
		items[i] = r
	}
	return map[string]any{"authority": "data_only_untrusted", "items": items}, nil
}

// execJob returns the new request for start (nil otherwise) and the job id.
func execJob(v map[string]any, action any, config *execConfig) (map[string]any, string, error) {
	scope := config.scope
	if action == "start" {
		pid, _ := v["profile_id"].(string)
		if _, known := config.profiles[pid]; !keysAre(v, "action", "scope", "server", "profile_id", "candidate", "event_id") || !isKey(v["event_id"]) || !known {
			return nil, "", requestInvalid()
		}
		request := map[string]any{"scope": scope, "profile_id": v["profile_id"], "candidate": v["candidate"], "event_id": v["event_id"]}
		return request, digest([]byte(mustCanonical([]any{scope, v["event_id"]}))), nil
	}
	id, ok := v["job_id"].(string)
	if !keysAre(v, "action", "scope", "server", "job_id") || !ok || !hex64.MatchString(id) {
		return nil, "", requestInvalid()
	}
	return nil, id, nil
}

func startJob(config *execConfig, request map[string]any, configPath, state, job string) error {
	api, folder := config.api, filepath.Join(state, job)
	row, err := execCandidate(api, config.scope, request["candidate"])
	if err != nil {
		return err
	}
	pins, _, _, err := material(obj(config.profiles[request["profile_id"].(string)]), api.token)
	if err != nil {
		return err
	}
	mismatch := !same(row["status"], "candidate")
	for k, want := range pins {
		mismatch = mismatch || !same(getOr(getOr(row, "comparison_plan", map[string]any{}), k, nil), want)
	}
	if mismatch {
		return cmpErr("execution_plan_mismatch")
	}
	// Readiness validates current baseline and source before accepting a job.
	if _, err := prepare(api, map[string]any{"record": row, "comparison_plan_sha256": row["comparison_plan_sha256"]}); err != nil {
		return err
	}
	if err := os.Mkdir(folder, 0o700); err != nil {
		return err
	}
	if err := saveJSON(filepath.Join(folder, "request.json"), map[string]any{"request": request, "config_sha256": config.fingerprint, "created_at": utcNow()}); err != nil {
		return err
	}
	return launchJob(configPath, state, job)
}

func checkSavedRequest(config *execConfig, request map[string]any, folder string) error {
	scope := config.scope
	saved, err := readJSONArg(filepath.Join(folder, "request.json"))
	if err != nil {
		return err
	}
	if !same(at(saved, "config_sha256"), config.fingerprint) || request != nil && !same(at(saved, "request"), request) {
		return cmpErr("execution_request_changed")
	}
	request = obj(at(saved, "request"))
	for _, k := range execScope {
		if !same(getOr(at(request, "scope"), k, ""), getOr(scope, k, "")) {
			return requestInvalid()
		}
	}
	_, err = execCandidate(config.api, scope, at(request, "candidate")) // Recheck read access on each status/retry.
	return err
}

func runJob(configPath, state, job string) (err error) {
	defer catch(&err)
	folder := filepath.Join(state, job)
	config, err := loadExecConfig(configPath)
	if err != nil {
		return err
	}
	saved, err := readJSONArg(filepath.Join(folder, "request.json"))
	if err != nil {
		return err
	}
	if !same(config.fingerprint, at(saved, "config_sha256")) {
		return cmpErr("execution_config_changed")
	}
	request := obj(at(saved, "request"))
	scope := obj(at(request, "scope"))
	row, err := execCandidate(config.api, scope, at(request, "candidate"))
	if err != nil {
		return err
	}
	proposal := map[string]any{"record": row, "comparison_plan_sha256": row["comparison_plan_sha256"]}
	resultPath := filepath.Join(folder, "result.json")
	if exists(resultPath + ".receipt") {
		return nil
	}
	var result map[string]any
	if exists(resultPath) {
		r, err := readJSONArg(resultPath)
		if err != nil {
			return err
		}
		result = obj(r)
		target := map[string]any{"collection": "skill_versions"}
		for k, v := range obj(at(request, "candidate")) {
			target[k] = v
		}
		if !same(at(result, "candidate"), target) || !same(at(at(result, "comparison_report"), "plan_sha256"), proposal["comparison_plan_sha256"]) {
			return cmpErr("execution_result_changed")
		}
	} else {
		prepared, err := prepare(config.api, proposal)
		if err != nil {
			return err
		}
		pins, dataset, env, err := material(obj(config.profiles[mustStr(at(request, "profile_id"))]), config.api.token)
		if err != nil {
			return err
		}
		for k, want := range pins {
			if !same(getOr(prepared["plan"], k, nil), want) {
				return cmpErr("execution_plan_mismatch")
			}
		}
		if result, err = evaluate(prepared, dataset, env); err != nil {
			return err
		}
		again, err := prepare(config.api, proposal)
		if err != nil {
			return err
		}
		if !same(again, prepared) {
			return cmpErr("execution_material_changed")
		}
		if err := saveJSON(resultPath, result); err != nil {
			return err
		}
	}
	receipt, err := publish(config.api, map[string]any{"scope": scope}, result)
	if err != nil {
		return err
	}
	return saveReceipt(resultPath, receipt)
}

func skillExecutionMain(argv []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	a, err := parseArgs(argv, []string{"config", "state", "worker", "lock-fd"}, nil, nil)
	if err == nil && len(a.pos) > 0 {
		err = fmt.Errorf("unrecognized arguments: %s", strings.Join(a.pos, " "))
	}
	lockFD := -1
	if err == nil && a.has("lock-fd") {
		if lockFD, err = strconv.Atoi(a.vals["lock-fd"]); err != nil {
			err = fmt.Errorf("argument --lock-fd: invalid int value: '%s'", a.vals["lock-fd"])
		}
	}
	if err != nil {
		return usageError(stderr, "skill-execution", "[-h] [--config CONFIG] [--state STATE] [--worker WORKER] [--lock-fd LOCK_FD]", err)
	}
	configPath, state := execConfigPath(), execStatePath()
	if a.has("config") {
		configPath = a.vals["config"]
	}
	if a.has("state") {
		state = a.vals["state"]
	}
	worker, isWorker := a.vals["worker"]
	fail := func(err error) int {
		msg := "execution_unavailable"
		var ce *comparisonError
		if errors.As(err, &ce) && errorCode.MatchString(ce.code) {
			msg = ce.code
		}
		if !isWorker {
			fmt.Fprintln(stdout, host.MustDumps(map[string]any{"error": msg}, host.DumpOpts{}))
		}
		return 1
	}
	defer func() {
		if recover() != nil {
			code = fail(nil)
		}
	}()
	if isWorker {
		return runWorker(configPath, state, worker, lockFD, fail)
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, execLimit+1))
	if err != nil {
		return fail(err)
	}
	if len(raw) > execLimit {
		return fail(requestInvalid())
	}
	value, err := strictJSON(raw)
	if err != nil {
		return fail(err)
	}
	result, err := handleExecution(value, configPath, state)
	if err != nil {
		return fail(err)
	}
	output := mustCanonical(result)
	if len(output) > 256*1024 {
		return fail(requestInvalid())
	}
	fmt.Fprintln(stdout, output)
	return 0
}

func runWorker(configPath, state, worker string, lockFD int, fail func(error) int) int {
	if !hex64.MatchString(worker) || lockFD < 0 {
		return fail(requestInvalid())
	}
	var expected, actual syscall.Stat_t
	if err := syscall.Lstat(filepath.Join(state, worker, "worker.lock"), &expected); err != nil {
		return fail(err)
	}
	if err := syscall.Fstat(lockFD, &actual); err != nil {
		return fail(err)
	}
	if expected.Dev != actual.Dev || expected.Ino != actual.Ino {
		return fail(requestInvalid())
	}
	defer syscall.Close(lockFD)
	terminated := make(chan os.Signal, 1)
	signal.Notify(terminated, syscall.SIGTERM)
	defer signal.Stop(terminated)
	go func() {
		<-terminated
		if pid := activeAdapter.Load(); pid > 0 {
			killGroup(int(pid), sigKill)
		}
		os.Exit(1) // execution_interrupted
	}()
	if err := runJob(configPath, state, worker); err != nil {
		return fail(err)
	}
	return 0
}

func init() { host.Register("skill-execution", skillExecutionMain) }
