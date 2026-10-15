//go:build unix

package crew

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func init() {
	// The pinned outcome checker: observed only when the output event carries an exit code.
	helperModes["outcome-checker"] = func([]string) int {
		x := readStdinJSON()
		v := obj(obj(list(obj(x["input"])["events"])[1])["payload"])["actual_exit"]
		var passed any
		if v != nil {
			passed = same(v, 0)
		}
		os.Stdout.WriteString(mustCanonical(map[string]any{"cost": 0, "output": map[string]any{"observed": v != nil, "passed": passed}}))
		return 0
	}
}

// recordAPI serves fresh copies of fixed records, as the API would.
type recordAPI struct {
	records map[string]any
	calls   []map[string]any
}

func (a *recordAPI) get(collection string, _ map[string]any, id string) (any, error) {
	return strictJSON([]byte(mustCanonical(a.records[collection+"/"+id])))
}

func (a *recordAPI) request(path string, payload any) (any, error) {
	a.calls = append(a.calls, obj(payload))
	return map[string]any{}, nil
}

func TestOutcomeCheckerObservesFailureSuccessAndUnknown(t *testing.T) {
	bin := testBinary(t)
	raw := []byte(mustCanonical(map[string]any{"argv": []any{bin, "outcome-checker"}, "files": map[string]any{bin: fileDigest(t, bin)},
		"env": map[string]any{}, "timeout_ms": 5000, "max_output_bytes": 1024}))
	target := map[string]any{"collection": "memories", "id": "m", "revision": 2}
	trace := map[string]any{"collection": "execution_traces", "id": "t", "revision": 1}
	observation := map[string]any{"scope": map[string]any{}, "target": target, "trace": trace, "dimension": "work_task"}
	for _, actual := range []any{0, 1, nil} {
		row := func(id string, extra map[string]any) map[string]any {
			m := map[string]any{"scope": map[string]any{}, "status": "active", "revision": 1, "id": id}
			for k, v := range extra {
				m[k] = v
			}
			return m
		}
		api := &recordAPI{records: map[string]any{
			"memories/m":         row("m", map[string]any{"revision": 2, "text": "Run tests"}),
			"execution_traces/t": row("t", map[string]any{"sources": []any{map[string]any{"source_id": "input"}, map[string]any{"source_id": "output"}}}),
			"sources/input":      row("input", map[string]any{"trace_id": "t", "source_event_id": "i", "text": mustCanonical(map[string]any{"retrieved": []any{target}})}),
			"sources/output":     row("output", map[string]any{"trace_id": "t", "source_event_id": "o", "text": mustCanonical(map[string]any{"actual_exit": actual, "model_claim": "success"})}),
		}}
		result, err := processOutcome(api, "independent", observation, raw, digest(raw), "producer")
		if err != nil {
			t.Fatal(actual, err)
		}
		if actual == nil {
			if result["state"] != "unobserved" || len(api.calls) != 0 {
				t.Fatal(result, api.calls)
			}
		} else {
			if api.calls[0]["passed"] != (actual == 0) {
				t.Fatal(actual, api.calls[0])
			}
			if _, err := processOutcome(api, "independent", observation, raw, digest(raw), "producer"); err != nil || api.calls[1]["event_id"] != api.calls[0]["event_id"] {
				t.Fatal("replay must reuse the event id", err)
			}
		}
		if _, err := processOutcome(api, "independent", observation, raw, strings.Repeat("0", 64), "producer"); code(err) != "outcome_checker_changed" {
			t.Fatal(err)
		}
		obj(api.records["memories/m"])["revision"] = 3
		if _, err := processOutcome(api, "independent", observation, raw, digest(raw), "producer"); code(err) != "outcome_source_changed" {
			t.Fatal(err)
		}
	}
}

func TestOutcomeSameCredentialRejectedBeforeRead(t *testing.T) {
	api := &recordAPI{}
	if _, err := processOutcome(api, "same", map[string]any{}, []byte("{}"), strings.Repeat("0", 64), "same"); code(err) != "independent_checker_credential_required" {
		t.Fatal(err)
	}
}

func TestReceiptDrainIsBoundedAndNeverPublishesWithoutChecks(t *testing.T) {
	root := t.TempDir()
	job := mustCanonical(map[string]any{"scope": map[string]any{}, "trace": map[string]any{"collection": "execution_traces", "id": "t", "revision": 1}})
	if err := os.WriteFile(filepath.Join(root, digest([]byte(job))+".json"), []byte(job), 0o600); err != nil {
		t.Fatal(err)
	}
	api := &recordAPI{records: map[string]any{"execution_traces/t": map[string]any{"id": "t", "revision": 1, "status": "active", "sources": []any{}}}}
	var rows []any
	for attempt := 0; attempt < 8; attempt++ {
		var err error
		if rows, err = drainReceipts(api, "checker", root, nil, "producer", float64(attempt*4000)); err != nil || len(rows) != 1 {
			t.Fatal(attempt, rows, err)
		}
	}
	if obj(rows[0])["state"] != "retry_exhausted" {
		t.Fatal(rows)
	}
	if rows, err := drainReceipts(api, "checker", root, nil, "producer", 100000); err != nil || len(rows) != 0 || len(api.calls) != 0 {
		t.Fatal(rows, err, api.calls)
	}
	var stored map[string]any
	raw, _ := os.ReadFile(filepath.Join(root, digest([]byte(job))+".json"))
	if json.Unmarshal(raw, &stored) != nil || stored["attempts"] != float64(8) {
		t.Fatal(string(raw))
	}
}
