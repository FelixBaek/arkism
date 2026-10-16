//go:build unix

package crew

// Memory outcomes (ST-614): an independent producer over canonical execution receipts. The
// configured local checker is trusted executable code, not an OS sandbox or an LLM verdict. Missing
// observations stay unrated. Personal compliance and work task outcomes stay separate; neither
// claims causal improvement over a memory-free control. The spool holds references only, never
// source bodies; publication is retry-safe through the canonical outcome API.

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/capture"
)

var outcomeCollections = map[string]bool{"memories": true, "execution_traces": true, "sources": true}

// liveRef is the exact, still active record ref names, in this scope.
func liveRef(api comparisonAPI, scope, ref map[string]any) (row map[string]any, err error) {
	defer catch(&err)
	collection, _ := ref["collection"].(string)
	if revision, isInt := host.Int(ref["revision"]); !outcomeCollections[collection] || !isInt || revision < 1 {
		return nil, cmpErr("outcome_reference_invalid")
	}
	r, err := api.get(collection, scope, mustStr(at(ref, "id")))
	if err != nil {
		return nil, err
	}
	row = obj(r)
	changed := row == nil || !same(row["id"], ref["id"]) || !same(row["revision"], ref["revision"]) || inactive(row)
	for _, k := range execScope {
		changed = changed || !same(getOr(getOr(row, "scope", map[string]any{}), k, ""), getOr(scope, k, ""))
	}
	if changed {
		return nil, cmpErr("outcome_source_changed")
	}
	return row, nil
}

// processOutcome observes one exact target in one trace; a separate evaluator credential is required.
func processOutcome(evaluator comparisonAPI, evaluatorToken string, observation map[string]any, environmentRaw []byte, checkerSHA, producerToken string) (out map[string]any, err error) {
	defer catch(&err)
	if evaluatorToken == "" || producerToken == "" || evaluatorToken == producerToken {
		return nil, cmpErr("independent_checker_credential_required")
	}
	if !sha256Re.MatchString(checkerSHA) || digest(environmentRaw) != checkerSHA {
		return nil, cmpErr("outcome_checker_changed")
	}
	env, err := checkerEnvironment(environmentRaw, evaluatorToken, producerToken)
	if err != nil {
		return nil, err
	}
	scope, target, traceRef := obj(at(observation, "scope")), obj(at(observation, "target")), obj(at(observation, "trace"))
	dimension := at(observation, "dimension")
	if !same(dimension, "personal_compliance") && !same(dimension, "work_task") || !same(target["collection"], "memories") || !same(traceRef["collection"], "execution_traces") {
		return nil, cmpErr("outcome_contract_invalid")
	}
	memory, err := liveRef(evaluator, scope, target)
	if err != nil {
		return nil, err
	}
	trace, err := liveRef(evaluator, scope, traceRef)
	if err != nil {
		return nil, err
	}
	pointers := list(getOr(trace, "sources", []any{}))
	if len(pointers) < 1 || len(pointers) > 64 {
		return nil, cmpErr("outcome_evidence_unavailable")
	}
	var events, evidence []any
	var snapshots [][2]map[string]any
	exposed := false
	for _, pointer := range pointers {
		r, err := evaluator.get("sources", scope, mustStr(at(pointer, "source_id")))
		if err != nil {
			return nil, err
		}
		ref := map[string]any{"collection": "sources", "id": at(r, "id"), "revision": at(r, "revision")}
		row, err := liveRef(evaluator, scope, ref)
		if err != nil {
			return nil, err
		}
		if !same(row["trace_id"], traceRef["id"]) || !truthy(row["source_event_id"]) {
			return nil, cmpErr("outcome_trace_lineage_invalid")
		}
		p, err := strictJSON([]byte(mustStr(at(row, "text"))))
		if err != nil {
			return nil, err
		}
		payload := obj(p)
		if payload == nil {
			return nil, cmpErr("outcome_event_invalid")
		}
		// Context delivery is necessary attribution, never itself a success.
		for _, retrieved := range list(payload["retrieved"]) {
			exposed = exposed || same(retrieved, target)
		}
		evidence = append(evidence, ref)
		snapshots = append(snapshots, [2]map[string]any{ref, row})
		events = append(events, map[string]any{"source": ref, "kind": row["kind"], "payload": payload})
	}
	if !exposed {
		return map[string]any{"state": "unobserved", "reason": "target_not_delivered"}, nil
	}
	result, _, _, _, err := executeAdapter(env, map[string]any{"input": map[string]any{"dimension": dimension, "target": target,
		"memory": at(memory, "text"), "memory_context": memory["memory_context"], "events": events}, "skill": ""}, nil)
	if err != nil {
		return nil, err
	}
	output := obj(result["output"])
	observed, isBool := output["observed"].(bool)
	passed, passedBool := output["passed"].(bool)
	if output == nil || !keysAre(output, "observed", "passed") || !isBool || output["passed"] != nil && !passedBool || observed != (output["passed"] != nil) {
		return nil, cmpErr("independent_checker_result_invalid")
	}
	if !observed {
		return map[string]any{"state": "unobserved", "reason": "checker_missing_observation"}, nil
	}
	// Reject changes during checker execution, including deletion and revocation.
	for _, snap := range append([][2]map[string]any{{target, memory}, {traceRef, trace}}, snapshots...) {
		now, err := liveRef(evaluator, scope, snap[0])
		if err != nil {
			return nil, err
		}
		if !same(now, snap[1]) {
			return nil, cmpErr("outcome_material_changed")
		}
	}
	operation := digest([]byte(mustCanonical([]any{scope, target, traceRef, evidence, checkerSHA, dimension})))
	receipt, err := evaluator.request("/v1/memory-outcomes", map[string]any{"scope": scope, "target": target, "trace": traceRef,
		"evidence": evidence, "checker_sha256": checkerSHA, "event_id": operation, "passed": passed, "dimension": dimension})
	if err != nil {
		return nil, err
	}
	return map[string]any{"state": "observed", "event_id": operation, "passed": passed, "receipt": receipt}, nil
}

// drainReceipts is a bounded per-run consumer of the receipt spool; a failure keeps only
// metadata retry state.
func drainReceipts(evaluator comparisonAPI, evaluatorToken, dir string, configurations []any, producerToken string, now float64) ([]any, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	lock, _, err := lockFile(filepath.Join(dir, ".drain.lock"), true)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(paths)
	if len(paths) > 32 {
		paths = paths[:32]
	}
	results := []any{}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		j, err := strictJSON(raw)
		if err != nil {
			return nil, err
		}
		job := obj(j)
		if job == nil {
			return nil, errValue
		}
		retryAt, _ := num(job["retry_at"])
		if state := str(job["state"]); retryAt > now || state == "done" || state == "rejected" || state == "retry_exhausted" {
			continue
		}
		state, err := drainReceipt(evaluator, evaluatorToken, job, configurations, producerToken)
		var ce *comparisonError
		switch code := host.HTTPCode(err); {
		case err == nil:
		case code == 403 || code == 404 || code == 409 || code == 410:
			state = "rejected"
		case errors.As(err, &ce) && (ce.code == "outcome_source_changed" || ce.code == "outcome_material_changed"):
			state = "rejected"
		default:
			state = "pending"
		}
		attempts, _ := host.Int(job["attempts"])
		attempts++
		job["state"], job["attempts"] = state, attempts
		if state != "done" && state != "rejected" {
			if attempts >= 8 {
				job["state"] = "retry_exhausted"
			} else {
				job["retry_at"] = pyFloat(now + math.Min(3600, 60*math.Pow(2, float64(attempts-1))))
			}
		}
		if err := host.AtomicWrite(path, mustCanonical(job)); err != nil {
			return nil, err
		}
		results = append(results, map[string]any{"receipt_id": strings.TrimSuffix(filepath.Base(path), ".json"), "state": job["state"], "attempts": attempts})
	}
	return results, nil
}

// drainReceipt checks every memory the receipt's trace delivered against every configured checker.
func drainReceipt(evaluator comparisonAPI, evaluatorToken string, job map[string]any, configurations []any, producerToken string) (state string, err error) {
	defer catch(&err)
	scope, traceRef := obj(at(job, "scope")), obj(at(job, "trace"))
	trace, err := liveRef(evaluator, scope, traceRef)
	if err != nil {
		return "", err
	}
	var targets []any
	for _, ref := range list(getOr(trace, "sources", []any{})) {
		source, err := evaluator.get("sources", scope, mustStr(at(ref, "source_id")))
		if err != nil {
			return "", err
		}
		if !same(getOr(source, "trace_id", nil), traceRef["id"]) {
			continue
		}
		payload, err := strictJSON([]byte(mustStr(at(source, "text"))))
		if err != nil {
			return "", err
		}
		for _, target := range list(getOr(payload, "retrieved", []any{})) {
			known := false
			for _, t := range targets {
				known = known || same(t, target)
			}
			if same(getOr(target, "collection", nil), "memories") && !known {
				targets = append(targets, target)
			}
		}
	}
	if len(targets) > 32 {
		return "", cmpErr("outcome_target_limit")
	}
	pending := false
	for _, target := range targets {
		for _, config := range configurations {
			environmentRaw, err := os.ReadFile(mustStr(at(config, "environment")))
			if err != nil {
				return "", err
			}
			observation := map[string]any{"scope": scope, "trace": traceRef, "target": target, "dimension": at(config, "dimension")}
			result, err := processOutcome(evaluator, evaluatorToken, observation, environmentRaw, mustStr(at(config, "checker_sha256")), producerToken)
			if err != nil {
				return "", err
			}
			pending = pending || result["state"] == "unobserved"
		}
	}
	if len(configurations) == 0 || len(targets) == 0 || pending {
		return "awaiting_observation", nil
	}
	return "done", nil
}

const outcomesUsage = "[-h] [--url URL] --evaluator-token-file EVALUATOR_TOKEN_FILE --producer-token-file PRODUCER_TOKEN_FILE --spool SPOOL --config CONFIG"

// outcomesMain is `arkism capture outcomes`: drain the canonical receipt spool once.
func outcomesMain(argv []string, _ io.Reader, stdout, stderr io.Writer) int {
	const prog = "capture outcomes"
	a, err := parseArgs(argv, []string{"url", "evaluator-token-file", "producer-token-file", "spool", "config"}, nil, nil)
	if err == nil && len(a.pos) > 0 {
		err = fmt.Errorf("unrecognized arguments: %s", strings.Join(a.pos, " "))
	}
	if err == nil && (!a.has("evaluator-token-file") || !a.has("producer-token-file") || !a.has("spool") || !a.has("config")) {
		err = errors.New("the following arguments are required: --evaluator-token-file, --producer-token-file, --spool, --config")
	}
	if err != nil {
		return usageError(stderr, prog, outcomesUsage, err)
	}
	apiURL := "http://127.0.0.1:8091"
	if a.has("url") {
		apiURL = a.vals["url"]
	}
	results, err := func() ([]any, error) {
		configurations, err := readJSONArg(a.vals["config"])
		if err != nil {
			return nil, err
		}
		evaluatorToken, err := readToken(a.vals["evaluator-token-file"])
		if err != nil {
			return nil, err
		}
		producerToken, err := readToken(a.vals["producer-token-file"])
		if err != nil {
			return nil, err
		}
		api, err := newEvalAPI(apiURL, evaluatorToken)
		if err != nil {
			return nil, err
		}
		return drainReceipts(api, evaluatorToken, a.vals["spool"], list(configurations), producerToken, float64(time.Now().UnixNano())/1e9)
	}()
	if err != nil {
		var ce *comparisonError
		if errors.As(err, &ce) {
			fmt.Fprintln(stderr, ce.code)
		} else {
			fmt.Fprintln(stderr, "memory_outcome_failed")
		}
		return 1
	}
	fmt.Fprintln(stdout, mustCanonical(results))
	return 0
}

func init() { capture.Outcomes = outcomesMain }
