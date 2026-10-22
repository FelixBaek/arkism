package crew

// Skill trials (ST-614): run explicitly configured trials once with canonical job leases. A pinned
// independent checker rates recorded executions; no outcome is inferred from a mention, usage count,
// missing trace or model confidence. The checker process never gets either Arkism credential, and
// neither source text, credentials nor exception bodies reach the output.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// checkerEnvironment reuses the fixed-runner environment validator. These validation-only cases are
// never submitted as a comparison or reported as measured held-out tests.
func checkerEnvironment(raw []byte, tokens ...string) (env map[string]any, err error) {
	cases := []any{}
	for _, kind := range []string{"quality", "regression", "safety"} {
		cases = append(cases, map[string]any{"id": kind, "category": kind, "input": nil, "expected": nil, "held_out": true})
	}
	dataset := []byte(mustCanonical(map[string]any{"cases": cases}))
	plan := map[string]any{"dataset_sha256": digest(dataset), "environment_sha256": digest(raw)}
	if len(tokens) == 0 {
		tokens = []string{""}
	}
	for _, token := range tokens {
		if _, env, err = validateInputs(dataset, raw, plan, token); err != nil {
			return nil, err
		}
	}
	return env, nil
}

// inactive: a record that is no longer usable evidence.
func inactive(row any) bool {
	return !same(getOr(row, "status", nil), "active") || truthy(getOr(row, "content_erased", nil)) || truthy(getOr(row, "privacy_quarantined", nil))
}

// processTrial rates the trial's unverified successful uses with the pinned checker.
func processTrial(manager, evaluator comparisonAPI, managerToken, evaluatorToken string, scope map[string]any, skillID, trialID string, environmentRaw []byte) (out map[string]any, err error) {
	defer catch(&err)
	if managerToken == "" || managerToken == evaluatorToken {
		return nil, cmpErr("independent_checker_credential_required")
	}
	env, err := checkerEnvironment(environmentRaw, managerToken, evaluatorToken)
	if err != nil {
		return nil, err
	}
	checkerHash := digest(environmentRaw)
	endpoint := "/v1/skill-trials/" + pyQuote(trialID, "") + "/observe"
	request := map[string]any{"scope": scope, "skill_id": skillID}
	o, err := manager.request(endpoint, request)
	if err != nil {
		return nil, err
	}
	observation := obj(o)
	if state := at(observation, "state"); !same(state, "awaiting_independent_evaluation") && !same(state, "awaiting_observation") {
		return observation, nil
	}
	candidate, err := evaluator.get("skill_versions", scope, mustStr(at(at(observation, "candidate"), "id")))
	if err != nil {
		return nil, err
	}
	if !same(at(candidate, "revision"), at(at(observation, "candidate"), "revision")) || !same(at(candidate, "status"), "approved") ||
		truthy(getOr(candidate, "content_erased", nil)) || truthy(getOr(candidate, "privacy_quarantined", nil)) {
		return nil, cmpErr("trial_candidate_changed")
	}
	for _, item := range list(at(observation, "items")) {
		if !same(at(item, "outcome"), "success") || truthy(at(item, "independently_verified")) {
			continue
		}
		if err := rateUse(evaluator, env, scope, trialID, checkerHash, item); err != nil {
			return nil, err
		}
	}
	again, err := manager.request(endpoint, request)
	return obj(again), err
}

// rateUse records the pinned checker's rating of one successful use.
func rateUse(evaluator comparisonAPI, env, scope map[string]any, trialID, checkerHash string, item any) error {
	sourceScope, sourceRef := obj(at(item, "scope")), obj(at(item, "source"))
	source, err := evaluator.get("sources", sourceScope, mustStr(at(sourceRef, "id")))
	if err != nil {
		return err
	}
	if !same(at(source, "revision"), at(sourceRef, "revision")) || inactive(source) {
		return cmpErr("trial_source_changed")
	}
	evidence, events, err := useEvents(evaluator, sourceScope, source)
	if err != nil {
		return err
	}
	usage, err := strictJSON([]byte(mustStr(at(source, "text"))))
	if err != nil {
		return err
	}
	result, _, _, _, err := executeAdapter(env, map[string]any{"input": map[string]any{"usage": usage, "events": events}, "skill": ""}, nil)
	if err != nil {
		return err
	}
	output := obj(result["output"])
	passed, isBool := output["passed"].(bool)
	if output == nil || !keysAre(output, "passed") || !isBool {
		return cmpErr("independent_checker_result_invalid")
	}
	_, err = evaluator.request("/v1/skill-trial-outcomes", map[string]any{
		"scope": sourceScope, "trial_scope": scope, "trial_id": trialID,
		"event_id": digest([]byte(mustCanonical([]any{scope, trialID, sourceRef, checkerHash}))), "source": sourceRef,
		"evidence": evidence, "checker_sha256": checkerHash, "passed": passed,
	})
	return err
}

// useEvents reads the events of the trace that recorded source.
func useEvents(evaluator comparisonAPI, sourceScope map[string]any, source any) (evidence, events []any, err error) {
	trace, err := evaluator.get("execution_traces", sourceScope, mustStr(at(source, "trace_id")))
	if err != nil {
		return nil, nil, err
	}
	if inactive(trace) {
		return nil, nil, cmpErr("trial_trace_changed")
	}
	refs := list(getOr(trace, "sources", []any{}))
	if len(refs) < 1 || len(refs) > 64 {
		return nil, nil, cmpErr("trial_evidence_unavailable")
	}
	evidence, events = []any{}, []any{}
	for _, ref := range refs {
		event, err := evaluator.get("sources", sourceScope, mustStr(at(ref, "source_id")))
		if err != nil {
			return nil, nil, err
		}
		if inactive(event) || !same(getOr(event, "trace_id", nil), at(source, "trace_id")) {
			return nil, nil, cmpErr("trial_evidence_changed")
		}
		payload, err := strictJSON([]byte(mustStr(at(event, "text"))))
		if err != nil {
			return nil, nil, err
		}
		pointer := map[string]any{"collection": "sources", "id": at(event, "id"), "revision": at(event, "revision")}
		evidence = append(evidence, pointer)
		events = append(events, map[string]any{"source": pointer, "payload": payload})
	}
	return evidence, events, nil
}

// compareAndPromote runs the independent fixed comparison after verified real trial outcomes.
func compareAndPromote(manager, evaluator comparisonAPI, managerToken, evaluatorToken string, scope map[string]any, skillID, trialID string,
	observation map[string]any, datasetRaw, environmentRaw []byte) (out map[string]any, err error) {
	defer catch(&err)
	if !same(at(observation, "state"), "ready_for_comparison") {
		return observation, nil
	}
	if managerToken == evaluatorToken {
		return nil, cmpErr("independent_checker_credential_required")
	}
	candidate, err := evaluator.get("skill_versions", scope, mustStr(at(at(observation, "candidate"), "id")))
	if err != nil {
		return nil, err
	}
	proposal := map[string]any{"record": candidate, "comparison_plan_sha256": at(candidate, "comparison_plan_sha256")}
	prepared, err := prepareTrial(evaluator, proposal, trialID)
	if err != nil {
		return nil, err
	}
	dataset, env, err := validateInputs(datasetRaw, environmentRaw, obj(prepared["plan"]), evaluatorToken)
	if err != nil {
		return nil, err
	}
	if strings.Contains(mustCanonical(env), managerToken) {
		return nil, cmpErr("manager_credential_in_adapter")
	}
	result, err := evaluate(prepared, dataset, env)
	if err != nil {
		return nil, err
	}
	again, err := prepareTrial(evaluator, proposal, trialID)
	if err != nil {
		return nil, err
	}
	if !same(again, prepared) {
		return nil, cmpErr("trial_comparison_material_changed")
	}
	receipt, err := publish(evaluator, prepared, result)
	if err != nil {
		return nil, err
	}
	if result["passed"] != true {
		_, err := manager.request("/v1/skill-trials/"+pyQuote(trialID, "")+"/stop", map[string]any{
			"scope": scope, "skill_id": skillID, "expected_revision": at(at(observation, "trial"), "revision")})
		return withKey(observation, "state", "stopped_comparison_regression"), err
	}
	parent, err := manager.get("skills", scope, skillID)
	if err != nil {
		return nil, err
	}
	evidence := []any{}
	for _, item := range list(at(observation, "items")) {
		evidence = append(evidence, map[string]any{"scope": at(item, "scope"), "source": at(item, "source"), "evaluation": at(item, "evaluation")})
	}
	if _, err := manager.request("/v1/skills/"+pyQuote(skillID, "")+"/apply", map[string]any{
		"scope": scope, "expected_revision": at(parent, "revision"), "version_id": at(candidate, "id"),
		"validation_id": at(at(receipt, "evaluation"), "id"), "trial_id": trialID, "scoped_trial_evidence": evidence,
	}); err != nil {
		return nil, err
	}
	return withKey(observation, "state", "promoted"), nil
}

// trialCheck is processTrial; tests replace it.
var trialCheck = processTrial

type trialRunner struct {
	manager, evaluator           comparisonAPI
	managerToken, evaluatorToken string
}

// run checks one due trial under its lease; the lease is always finished, and a failed finish
// request is recovered by the canonical lease expiry.
func (r *trialRunner) run(trial map[string]any) (out map[string]any, err error) {
	defer catch(&err)
	scope, skillID, trialID := obj(at(trial, "scope")), mustStr(at(trial, "skill_id")), mustStr(at(trial, "trial_id"))
	endpoint := "/v1/skill-trials/" + pyQuote(trialID, "")
	lease, err := r.manager.request(endpoint+"/claim-check", map[string]any{"scope": scope, "skill_id": skillID})
	if host.HTTPCode(err) == 404 {
		return map[string]any{"trial_id": trialID, "state": "not_due"}, nil
	} else if err != nil {
		return nil, err
	}
	outcome := "unavailable"
	defer func() {
		_, ferr := r.manager.request(endpoint+"/finish-check", map[string]any{"scope": scope, "skill_id": skillID,
			"expected_revision": at(lease, "revision"), "lease_token": at(lease, "lease_token"), "outcome": outcome})
		if err == nil && ferr != nil {
			out, err = nil, ferr
		}
	}()
	environmentRaw, err := os.ReadFile(mustStr(at(trial, "environment")))
	if err != nil {
		return nil, err
	}
	result, err := trialCheck(r.manager, r.evaluator, r.managerToken, r.evaluatorToken, scope, skillID, trialID, environmentRaw)
	if err != nil {
		return nil, err
	}
	if same(at(result, "state"), "promoted") || same(at(result, "state"), "recovery_pending") {
		monitored, err := r.manager.request(endpoint+"/monitor", map[string]any{"scope": scope, "skill_id": skillID})
		if err != nil {
			return nil, err
		}
		result = obj(monitored)
	}
	if truthy(getOr(trial, "comparison_dataset", nil)) && truthy(getOr(trial, "comparison_environment", nil)) {
		datasetRaw, err := os.ReadFile(mustStr(trial["comparison_dataset"]))
		if err != nil {
			return nil, err
		}
		comparisonRaw, err := os.ReadFile(mustStr(trial["comparison_environment"]))
		if err != nil {
			return nil, err
		}
		if result, err = compareAndPromote(r.manager, r.evaluator, r.managerToken, r.evaluatorToken, scope, skillID, trialID, result, datasetRaw, comparisonRaw); err != nil {
			return nil, err
		}
	}
	if !same(at(result, "state"), "recovery_unavailable") {
		outcome = "checked"
	}
	return map[string]any{"trial_id": trialID, "state": at(result, "state")}, nil
}

func (r *trialRunner) once(trials []any) []any {
	results := []any{}
	for _, trial := range trials {
		result, err := r.run(obj(trial))
		if err != nil {
			// Neither source text, credentials nor error bodies enter logs.
			result = map[string]any{"trial_id": str(obj(trial)["trial_id"]), "state": "unavailable"}
		}
		results = append(results, result)
	}
	return results
}

const skillTrialsUsage = "[-h] [--url URL] --manager-token-file MANAGER_TOKEN_FILE --evaluator-token-file EVALUATOR_TOKEN_FILE --config CONFIG [--once] [--interval INTERVAL]"

func readToken(path string) (string, error) {
	raw, err := os.ReadFile(path)
	return strings.TrimSpace(string(raw)), err
}

func skillTrialsMain(argv []string, _ io.Reader, stdout, stderr io.Writer) int {
	const prog = "skill-trials"
	a, err := parseArgs(argv, []string{"url", "manager-token-file", "evaluator-token-file", "config", "interval"}, []string{"once"}, nil)
	if err == nil && len(a.pos) > 0 {
		err = fmt.Errorf("unrecognized arguments: %s", strings.Join(a.pos, " "))
	}
	if err == nil && (!a.has("manager-token-file") || !a.has("evaluator-token-file") || !a.has("config")) {
		err = errors.New("the following arguments are required: --manager-token-file, --evaluator-token-file, --config")
	}
	interval := 60
	if err == nil && a.has("interval") {
		interval, err = trialInterval(a.vals["interval"])
	}
	var trials []any
	if err == nil {
		trials, err = trialConfig(a.vals["config"])
	}
	r := &trialRunner{}
	if err == nil {
		var read bool
		if read, err = r.readTokens(a); !read {
			fmt.Fprintln(stderr, "trial_check_failed")
			return 1
		}
	}
	if err != nil {
		return usageError(stderr, prog, skillTrialsUsage, err)
	}
	apiURL := "http://127.0.0.1:8091"
	if a.has("url") {
		apiURL = a.vals["url"]
	}
	if err := r.connect(apiURL); err != nil {
		fmt.Fprintln(stderr, "invalid_api_url")
		return 1
	}
	for {
		fmt.Fprintln(stdout, mustCanonical(r.once(trials)))
		if a.flags["once"] {
			return 0
		}
		time.Sleep(time.Duration(interval) * time.Second)
	}
}

func trialInterval(v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n < 30 || n > 3600 {
		return 0, errors.New("interval must be 30..3600 seconds")
	}
	return n, nil
}

// readTokens reads both credentials; read is false when a token file could not be read.
func (r *trialRunner) readTokens(a *args) (read bool, err error) {
	r.managerToken, err = readToken(a.vals["manager-token-file"])
	if err == nil {
		r.evaluatorToken, err = readToken(a.vals["evaluator-token-file"])
	}
	if err != nil {
		return false, err
	}
	if r.managerToken == "" || r.evaluatorToken == "" || r.managerToken == r.evaluatorToken {
		return true, errors.New("distinct nonempty manager and evaluator credentials required")
	}
	return true, nil
}

func (r *trialRunner) connect(apiURL string) error {
	manager, err := newEvalAPI(apiURL, r.managerToken)
	if err == nil {
		r.evaluator, err = newEvalAPI(apiURL, r.evaluatorToken)
	}
	if err != nil {
		return err
	}
	r.manager = manager
	return nil
}

func trialConfig(path string) (trials []any, err error) {
	raw, rerr := readJSONArg(path)
	trials = list(raw)
	if rerr != nil || len(trials) < 1 || len(trials) > 32 {
		err = errors.New("config must contain 1..32 trial objects")
	}
	for _, t := range trials {
		m := obj(t)
		if m == nil {
			return trials, errors.New("config must contain 1..32 trial objects")
		}
		if !truthy(m["scope"]) || !truthy(m["skill_id"]) || !truthy(m["trial_id"]) || !truthy(m["environment"]) ||
			truthy(m["comparison_dataset"]) != truthy(m["comparison_environment"]) {
			return trials, errors.New("each trial needs scope, skill_id, trial_id, environment, and both optional comparison paths")
		}
	}
	return trials, err
}

func init() { host.Register("skill-trials", skillTrialsMain) }
