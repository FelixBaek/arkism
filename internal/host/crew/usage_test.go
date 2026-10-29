package crew

import (
	"encoding/json"
	"math"
	"reflect"

	"bytes"
	"fmt"
	"github.com/FelixBaek/arkism/internal/crewrun"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func captureReports(t *testing.T) *[]map[string]any {
	got := &[]map[string]any{}
	patch(t, &host.ReportCall, func(c map[string]any) { *got = append(*got, c) })
	return got
}

func TestRunNodeReportsUsageWithItsOrigin(t *testing.T) {
	t.Cleanup(func() { stopOnTerm.Store(false) })
	reports := captureReports(t)
	fakeSpawn(t, "검토 끝: 좋아요", "", 0)
	var buf bytes.Buffer
	stdin := `{"model": "opus 5.5", "task": "이 PR 봐 줘", "once": true, "access": "read", "origin": {"kind": "meeting", "meeting_id": "m1", "agent_id": "a1", "team_id": "t1"}}`
	if code := crewMain([]string{"run-node", "--json"}, strings.NewReader(stdin), &buf, &buf); code != 0 {
		t.Fatal(code, buf.String())
	}
	out := loads(t, buf.String())
	u := obj(out["usage"])
	if u["estimated"] != true || tnum(t, u["input_tokens"]) <= 0 || tnum(t, u["output_tokens"]) <= 0 || tnum(t, u["cost_usd"]) <= 0 {
		t.Fatal(out)
	}
	if len(*reports) != 1 {
		t.Fatal(*reports)
	}
	r := (*reports)[0]
	if r["model"] != "anthropic/opus 5.5" || r["decision"] != "fixed" || r["ok"] != true || obj(r["origin"])["meeting_id"] != "m1" || r["estimated"] != true {
		t.Fatal(r)
	}
	// without an origin a workflow node is not counted twice (the engine's own calls stay as they were)
	*reports = nil
	buf.Reset()
	_ = crewMain([]string{"run-node", "--json"}, strings.NewReader(`{"model": "opus", "task": "x", "cwd": "/tmp"}`), &buf, &buf)
	if len(*reports) != 0 {
		t.Fatal(*reports)
	}
	// a failure is counted too
	fakeSpawn(t, "", "boom", 3)
	buf.Reset()
	_ = crewMain([]string{"run-node", "--json"}, strings.NewReader(stdin), &buf, &buf)
	if len(*reports) != 1 || (*reports)[0]["ok"] != false || (*reports)[0]["error"] != "exit_3" {
		t.Fatal(*reports)
	}
}

func tnum(t *testing.T, v any) float64 {
	t.Helper()
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	}
	f, err := strconv.ParseFloat(fmt.Sprint(v), 64)
	if err != nil {
		t.Fatalf("not a number: %#v", v)
	}
	return f
}

func TestRoutedAttemptsCarryOrigin(t *testing.T) {
	var got []map[string]any
	decide := func(string, string) (map[string]any, error) {
		return map[string]any{"id": "anthropic/opus", "decision": "clef", "fallbacks": []any{"openai/gpt-6.1-sol"}}, nil
	}
	run := func(s map[string]any) (map[string]any, error) {
		if s["model"] == "opus" {
			return result(false, "", "opus", "", "claude", time.Time{}, "quota_exhausted"), nil
		}
		return result(true, "답", "gpt-6.1-sol", "medium", "codex", time.Now(), ""), nil
	}
	res, err := runRouted(map[string]any{"task": "x", "origin": map[string]any{"kind": "agent", "agent_id": "a1"}}, decide, run,
		func(c map[string]any) { got = append(got, c) })
	if err != nil || res["ok"] != true || len(got) != 2 {
		t.Fatal(res, err, got)
	}
	if obj(got[0]["origin"])["agent_id"] != "a1" || got[0]["input_tokens"] != nil || got[1]["decision"] != "failover" || got[1]["input_tokens"] == nil {
		t.Fatal(got)
	}
}

func TestTryRunsTheAgentReadOnly(t *testing.T) {
	reports := captureReports(t)
	patch(t, &runnerCall, func(path string, _ any, _ time.Duration) (map[string]any, error) {
		if !strings.Contains(path, "handle=%40%EB%A6%AC%EB%B7%B0%EC%96%B4") && !strings.Contains(path, "handle=@") {
			t.Fatal(path)
		}
		return map[string]any{"item": map[string]any{"id": "a1", "crew": map[string]any{"type": "agent", "role": "리뷰", "instructions": "꼼꼼히",
			"model": "opus 5.5", "fallback_model": "sonnet 5", "output_format": "markdown"}}}, nil
	})
	var specs []map[string]any
	runner := func(s map[string]any) (map[string]any, error) {
		specs = append(specs, s)
		if s["model"] == "opus 5.5" {
			return result(false, "", "opus 5.5", "", "claude", time.Time{}, "timeout"), nil
		}
		return result(true, "좋아요", pyStr(s["model"]), "medium", "claude", time.Now(), ""), nil
	}
	res, err := answerAssist(map[string]any{"id": "x", "created_by_type": "service", "crew_assist": map[string]any{"kind": "try", "expert": "리뷰어", "prompt": "봐 줘", "lang": "en"}}, runner)
	if err != nil || res["status"] != "done" || res["output"] != "좋아요" || len(specs) != 2 {
		t.Fatal(res, err, specs)
	}
	s := specs[0]
	if s["access"] != "read" || s["once"] != true || !strings.Contains(pyStr(s["instructions"]), "markdown") || !strings.Contains(pyStr(s["instructions"]), "reads English") || obj(s["origin"])["trigger"] != "discord" {
		t.Fatal(s)
	}
	if len(*reports) != 2 || (*reports)[1]["ok"] != true || obj((*reports)[1]["origin"])["agent_id"] != "a1" {
		t.Fatal(*reports)
	}
}

func TestAgentWithAWorkflowRunsItInsteadOfAnswering(t *testing.T) {
	patch(t, &runnerCall, func(string, any, time.Duration) (map[string]any, error) {
		return map[string]any{"item": map[string]any{"id": "a1", "crew": map[string]any{"type": "agent", "workflow": "$리서치"}}}, nil
	})
	patch(t, &apiGet, func(path string, _ time.Duration) (map[string]any, error) {
		if strings.Contains(path, "/v1/crew/resolve") {
			return workflowFixture(t, "리서치", ""), nil
		}
		return runRec("succeeded", nil, nil, "보고서", 2), nil
	})
	patch(t, &apiPost, func(string, any, time.Duration) (map[string]any, error) {
		return runRec("running", nil, nil, "", 1), nil
	})
	patch(t, &runSleep, func(float64) {})
	runner := func(map[string]any) (map[string]any, error) { t.Fatal("the agent answered itself"); return nil, nil }
	res, err := answerAssist(map[string]any{"id": "x", "crew_assist": map[string]any{"kind": "try", "expert": "리서처", "prompt": "AI 뉴스"}}, runner)
	if err != nil || res["status"] != "done" || res["output"] != "보고서" {
		t.Fatal(res, err)
	}
}

func TestAgentWorkflowWaitingOnAnApprovalSaysSo(t *testing.T) {
	patch(t, &apiGet, func(path string, _ time.Duration) (map[string]any, error) {
		if strings.Contains(path, "/v1/crew/resolve") {
			return workflowFixture(t, "리서치", ""), nil
		}
		return runRec("waiting", map[string]any{"node_id": "a", "kind": "approval", "question": "보낼까요?"}, nil, "", 2), nil
	})
	patch(t, &apiPost, func(string, any, time.Duration) (map[string]any, error) {
		return runRec("running", nil, nil, "", 1), nil
	})
	patch(t, &runSleep, func(float64) {})
	res := agentWorkflow("리서치", "AI 뉴스")
	if out := sOr(res["output"]); !strings.Contains(out, "확인을 기다려요: 보낼까요?") || !strings.Contains(out, "run-1") {
		t.Fatal(res)
	}
}

func usageString(value string) *string { return &value }
func TestUsageObservationUnknownIsNotZero(t *testing.T) {
	result, err := NormalizeUsageObservation(nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.UsageBasis != "unknown" || result.CostBasis != "unknown" || result.InputTokens != nil || result.OutputTokens != nil || result.CostUSD != nil || result.CacheReadTokens != nil || result.ReasoningTokens != nil {
		t.Fatalf("unknown filled with zero %+v", result)
	}
	if result.DurationNS != nil || result.TokenCeilingEnforced {
		t.Fatal("invented duration or enforced ceiling")
	}
}
func TestUsageObservationReportedWinsWithoutDoubleCounting(t *testing.T) {
	reported := map[string]any{"input_tokens": int64(0), "output_tokens": json.Number("20"), "cache_read_tokens": int64(4), "cache_write_tokens": int64(2), "reasoning_tokens": int64(3), "cost_usd": float64(0), "estimated": false}
	estimated := map[string]any{"input_tokens": int64(99), "output_tokens": int64(99), "cost_usd": 9.5}
	result, err := NormalizeUsageObservation(reported, estimated, usageString("backend-v1"), usageString("provider-price-v1"), usageString("list-v1"))
	if err != nil {
		t.Fatal(err)
	}
	if *result.InputTokens != 0 || *result.OutputTokens != 20 || *result.CostUSD != 0 || result.InputBasis != "provider_reported" || result.CostBasis != "provider_reported" || *result.PricingRevision != "provider-price-v1" {
		t.Fatalf("double counted/replaced observed zero %+v", result)
	}
	if *result.CacheReadTokens != 4 || *result.CacheWriteTokens != 2 || *result.ReasoningTokens != 3 {
		t.Fatal("component scope lost")
	}
	if *result.InputTokens != 0 {
		t.Fatal("cache/reasoning added into input")
	}
}
func TestUsageObservationPartialComponentsAndMixedBasis(t *testing.T) {
	result, err := NormalizeUsageObservation(map[string]any{"input_tokens": int64(7), "cost_usd": .2}, nil, usageString("backend-v1"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.InputTokens == nil || *result.InputTokens != 7 || result.OutputTokens != nil || result.UsageBasis != "unknown" || result.CostBasis != "provider_reported" || *result.CostUSD != .2 {
		t.Fatalf("partial presence lost %+v", result)
	}
	result, err = NormalizeUsageObservation(map[string]any{"input_tokens": int64(7)}, map[string]any{"input_tokens": int64(99), "output_tokens": int64(3), "cost_usd": .1}, nil, nil, usageString("list-v1"))
	if err != nil {
		t.Fatal(err)
	}
	if *result.InputTokens != 7 || *result.OutputTokens != 3 || result.InputBasis != "provider_reported" || result.OutputBasis != "estimated" || result.UsageBasis != "estimated" || result.CostBasis != "estimated" || *result.PricingRevision != "list-v1" {
		t.Fatal(result)
	}
}
func TestUsageObservationEveryComponentRetainsMissingVsZero(t *testing.T) {
	for _, key := range []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "reasoning_tokens"} {
		t.Run(key, func(t *testing.T) {
			result, err := NormalizeUsageObservation(map[string]any{key: int64(0)}, nil, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err = json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded[key] != float64(0) {
				t.Fatalf("known zero missing %s", encoded)
			}
			for _, other := range []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "reasoning_tokens"} {
				if other != key && decoded[other] != nil {
					t.Fatalf("missing field invented %s", encoded)
				}
			}
		})
	}
}
func TestUsageObservationStrictCountAndCostInputs(t *testing.T) {
	tests := []struct {
		name, key string
		value     any
	}{
		{"bool", "input_tokens", true}, {"float_integer", "input_tokens", float64(1)}, {"fraction", "input_tokens", json.Number("1.0")},
		{"exponent", "input_tokens", json.Number("1e1")}, {"negative", "output_tokens", int64(-1)}, {"range", "input_tokens", int64(1000000001)},
		{"overflow", "input_tokens", json.Number("9223372036854775808")}, {"text", "input_tokens", "secret body"},
		{"nan", "cost_usd", math.NaN()}, {"infinity", "cost_usd", math.Inf(1)}, {"negative_cost", "cost_usd", -.1}, {"boolean_cost", "cost_usd", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NormalizeUsageObservation(map[string]any{tt.key: tt.value}, map[string]any{"input_tokens": int64(3)}, nil, nil, nil)
			if err == nil {
				t.Fatal("invalid reported data hidden by estimate")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("raw value leaked")
			}
		})
	}
}
func TestUsageObservationEstimateCannotClaimProviderProvenance(t *testing.T) {
	for _, flag := range []any{true, "provider_reported"} {
		_, err := NormalizeUsageObservation(map[string]any{"estimated": flag, "input_tokens": int64(1)}, nil, nil, nil, nil)
		if err == nil {
			t.Fatal("arbitrary label promoted to provider usage")
		}
	}
	result, err := NormalizeUsageObservation(map[string]any{"usage_basis": "provider_reported"}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.UsageBasis != "unknown" || result.InputTokens != nil {
		t.Fatal("basis text invented component presence")
	}
}
func TestUsageObservationLegacyEstimateAndInputNonmutation(t *testing.T) {
	spec := map[string]any{"model": "opus 5.5", "instructions": "abcd", "task": "work"}
	res := map[string]any{"ok": true, "output": "done", "model": "opus 5.5"}
	estimated := usageOf(spec, res)
	before := make(map[string]any)
	for key, value := range estimated {
		before[key] = value
	}
	schema, price := "fake-v1", "list-v1"
	result, err := NormalizeUsageObservation(nil, estimated, &schema, nil, &price)
	if err != nil {
		t.Fatal(err)
	}
	if result.UsageBasis != "estimated" || result.CostBasis != "estimated" || *result.InputTokens != estimated["input_tokens"] || *result.OutputTokens != estimated["output_tokens"] {
		t.Fatal("existing estimates not reused")
	}
	if !reflect.DeepEqual(estimated, before) || !reflect.DeepEqual(usageOf(spec, res), before) {
		t.Fatal("legacy behavior/input changed")
	}
	*result.ProviderUsageSchema = "changed"
	*result.PricingRevision = "changed"
	if schema != "fake-v1" || price != "list-v1" {
		t.Fatal("output aliases caller strings")
	}
	if err := crewrun.ValidateUsageObservation(result); err != nil {
		t.Fatal(err)
	}
}
func TestUsageObservationNullReportedFallsBackAsEstimateOnly(t *testing.T) {
	result, err := NormalizeUsageObservation(map[string]any{"input_tokens": nil}, map[string]any{"input_tokens": int64(5), "output_tokens": int64(6)}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if *result.InputTokens != 5 || result.InputBasis != "estimated" || result.UsageBasis != "estimated" || result.CostUSD != nil || result.PricingRevision != nil {
		t.Fatal(result)
	}
}
