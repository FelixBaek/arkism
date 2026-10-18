package capture

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const contextGroundingSource = "앞으로 업무용 가상 전시 안내문을 작성할 때에는 2027년 1월 15일부터 사전 검수를 통과한 초안에 한해서만 한글로 요약하는 방식을 적용할 예정이야. 검수를 받지 않은 초안에는 이 방식을 적용하지 말고, 긴급 안내문은 예외로 원문을 유지해. 이 방침은 아직 잠정안이라 실제 운영에 적합한지는 확인되지 않았어. 이 조건과 예외를 포함해서 기억해."

func groundedContextClaim() map[string]any {
	context := map[string]any{"domains": []any{"work"}, "semantic_kind": "fact",
		"applicability": "검수를 받지 않은 초안에는 이 방식을 적용하지 말고, 긴급 안내문은 예외로 원문을 유지해.",
		"uncertainty":   "이 방침은 아직 잠정안이라 실제 운영에 적합한지는 확인되지 않았어.", "valid_from": "2027년 1월 15일부터"}
	return map[string]any{"subject": "가상 전시 안내문", "kind": "decision", "text": contextGroundingSource,
		"quote": contextGroundingSource, "memory_context": context, "risk": "medium"}
}

func TestCaptureOptionalContextRejectsInventedRule(t *testing.T) {
	capFake(t)
	for _, quote := range []string{
		"2027년 1월 15일부터 사전 검수를 통과한 초안에 한해서만 한글로 요약하는 방식을 적용할 예정이야",
		contextGroundingSource,
	} {
		raw := groundedContextClaim()
		raw["quote"] = quote
		raw["text"] = "업무용 가상 전시 안내문 작성 시, 2027년 1월 15일부터 사전 검수를 통과한 초안에 한해서만 한글로 요약하는 방식을 적용한다. 검수를 받지 않은 초안에는 적용하지 않으며, 긴급 안내문은 예외로 원문을 유지한다. 이 방침은 잠정안이며 실제 운영에 적합한지는 확인되지 않았다."
		// An exact real quotation, even the whole message, cannot certify this
		// added instruction. The source excludes unreviewed drafts from the
		// summarization rule; it does not order their originals to be retained.
		raw["memory_context"].(map[string]any)["applicability"] = "업무용 가상 전시 안내문 작성에만 적용. 사전 검수를 통과한 초안에만 한글 요약을 적용하고, 검수 전 초안과 긴급 안내문은 원문 유지."
		if quote != contextGroundingSource {
			// The observed model response also paraphrased uncertainty and
			// normalized the date. The full-quote case isolates the invented rule.
			raw["memory_context"].(map[string]any)["uncertainty"] = "잠정안이며 실제 운영 적합성은 확인되지 않음"
			raw["memory_context"].(map[string]any)["valid_from"] = "2027-01-15"
		}
		claims, receipt, err := buildClaimsWithReceipt([]any{raw}, contextGroundingSource, nil,
			[]any{map[string]any{"text": contextGroundingSource, "role": "user"}})
		if err != nil || len(claims) != 0 || receipt == nil || len(receipt.Rejected) != 1 || receipt.Rejected[0].Reason != "context_evidence_unbound" {
			t.Fatal("an exact claim quote authenticated an invented context rule", err, len(claims), receipt)
		}
		if !validClaimContextReceipt(receipt) || receipt.Rejected[0].TextSHA256 != sha256Hex([]byte(raw["text"].(string))) {
			t.Fatal("withheld context lost its fixed-code/hash receipt")
		}
	}
}

func TestCaptureOptionalContextGroundingSourceAndRetainedEvidence(t *testing.T) {
	capFake(t)
	for _, mode := range []string{"user", "legacy_user_row", "assistant_duplicate_ignored", "nil_aggregate", "empty_rows", "assistant", "nonstring_role", "nonstring_quote", "missing_quote", "duplicate_quote", "different_message", "cross_message_phrase", "case_fold", "space_fold", "date_normalization", "outside_quote", "outside_retained_quote", "long_quote_keeps_early_phrase"} {
		t.Run(mode, func(t *testing.T) {
			raw := groundedContextClaim()
			context := raw["memory_context"].(map[string]any)
			users := []any{map[string]any{"role": "user", "text": contextGroundingSource}}
			want := mode == "user" || mode == "legacy_user_row" || mode == "assistant_duplicate_ignored" || mode == "long_quote_keeps_early_phrase"
			switch mode {
			case "legacy_user_row":
				delete(users[0].(map[string]any), "role")
			case "assistant_duplicate_ignored":
				users = append(users, map[string]any{"role": "assistant", "text": contextGroundingSource})
			case "nil_aggregate":
				users = nil
			case "empty_rows":
				users = []any{}
			case "assistant":
				users[0].(map[string]any)["role"] = "assistant"
			case "nonstring_role":
				users[0].(map[string]any)["role"] = []string{"user"}
			case "nonstring_quote":
				raw["quote"] = []string{contextGroundingSource}
			case "missing_quote":
				delete(raw, "quote")
			case "duplicate_quote":
				users = append(users, map[string]any{"role": "user", "text": contextGroundingSource})
			case "different_message":
				raw["quote"] = "이 조건과 예외를 포함해서 기억해."
				users = []any{map[string]any{"text": raw["quote"]}, map[string]any{"text": strings.TrimSuffix(contextGroundingSource, raw["quote"].(string))}}
			case "cross_message_phrase":
				raw["quote"] = "첫 문장 기억해."
				context["applicability"], context["uncertainty"], context["valid_from"] = "업무에서만 검토 후 적용", "", ""
				users = []any{map[string]any{"text": "첫 문장 기억해. 업무에서만"}, map[string]any{"text": " 검토 후 적용"}}
			case "case_fold", "space_fold":
				source := "Keep ScopeID\tunchanged."
				raw["quote"] = source
				users = []any{map[string]any{"text": source}}
				context["uncertainty"], context["valid_from"] = "", ""
				if mode == "case_fold" {
					context["applicability"] = "keep scopeid\tunchanged."
				} else {
					context["applicability"] = "Keep ScopeID unchanged."
				}
			case "date_normalization":
				context["valid_from"] = "2027-01-15"
			case "outside_quote":
				raw["quote"] = context["valid_from"]
			case "outside_retained_quote", "long_quote_keeps_early_phrase":
				source := "앞부분 원문 조건. " + strings.Repeat("한", 2100) + " 뒷부분 원문 조건."
				raw["quote"] = source
				users = []any{map[string]any{"text": source}}
				context["uncertainty"], context["valid_from"] = "", ""
				if mode == "outside_retained_quote" {
					context["applicability"] = "뒷부분 원문 조건."
				} else {
					context["applicability"] = "앞부분 원문 조건."
				}
			}
			claims, receipt, err := buildClaimsWithReceipt([]any{raw}, contextGroundingSource, nil, users)
			if err != nil {
				t.Fatal(err)
			}
			if !want {
				if len(claims) != 0 || receipt == nil || len(receipt.Rejected) != 1 || receipt.Rejected[0].Reason != "context_evidence_unbound" {
					t.Fatal("unbound context escaped withholding", len(claims), receipt)
				}
				return
			}
			if len(claims) != 1 || receipt != nil {
				t.Fatal("literal user context was discarded", len(claims), receipt)
			}
			claim := claims[0].(map[string]any)
			span := claim["evidence_span"].(string)
			for _, key := range []string{"applicability", "uncertainty", "valid_from"} {
				if !strings.Contains(span, context[key].(string)) || field(claim["memory_context"], key) != context[key] {
					t.Fatal("stored evidence lost the literal context field", key)
				}
			}
			if field(claim["memory_context"], "evidence_kind") != "inference" || field(claim["memory_context"], "observed_at") != nil {
				t.Fatal("exact text was promoted to source authority")
			}
		})
	}
}

func TestCaptureOptionalContextUngroundedItemKeepsOtherClaimAndNilLegacy(t *testing.T) {
	capFake(t)
	bad, good := groundedContextClaim(), groundedContextClaim()
	bad["memory_context"].(map[string]any)["uncertainty"] = "원문에 없는 확정 판단"
	claims, receipt, err := buildClaimsWithReceipt([]any{bad, good}, contextGroundingSource, nil, []any{map[string]any{"text": contextGroundingSource}})
	if err != nil || len(claims) != 1 || receipt == nil || len(receipt.Rejected) != 1 || receipt.Rejected[0].Index != 0 || receipt.Rejected[0].Reason != "context_evidence_unbound" {
		t.Fatal("one unsupported item discarded its supported neighbor", err, len(claims), receipt)
	}
	legacy := groundedContextClaim()
	delete(legacy, "memory_context")
	withNil, receipt, err := buildClaimsWithReceipt([]any{legacy}, contextGroundingSource, nil, nil)
	if err != nil || len(withNil) != 1 || receipt != nil || withNil[0].(map[string]any)["basis"] != "explicit_user" {
		t.Fatal("context-free aggregate compatibility changed", err, withNil, receipt)
	}
	prepared := map[string]any{"generator": map[string]any{"model": "prior-model", "prompt_version": "capture-extract-v3", "pipeline_version": "prior-pipeline"}}
	before := dumps(prepared)
	gen, err := preparedClaimGenerator(prepared, "changed-pipeline")
	if err != nil || field(gen, "prompt_version") != "capture-extract-v3" || dumps(prepared) != before || !reflect.DeepEqual(field(gen, "model"), "prior-model") {
		t.Fatal("existing v3 prepared generator changed", err, gen)
	}
}

func TestCaptureOptionalContextV3PreparedRetryRetainsPayload(t *testing.T) {
	capFake(t)
	t.Setenv("ARKISM_SESSION_PROCESSOR", "")
	raw := groundedContextClaim()
	users := []any{map[string]any{"text": contextGroundingSource, "at": "2026-01-01T00:00:00Z"}}
	claims, _, err := buildClaimsWithReceipt([]any{raw}, contextGroundingSource, nil, users)
	if err != nil || len(claims) != 1 {
		t.Fatal(err, len(claims))
	}
	// A persisted v3 request may contain paraphrased context. A delivery retry
	// sends those original bytes; the v4 checks apply only to new extraction.
	claim := claims[0].(map[string]any)
	context, err := asDict(claim["memory_context"])
	if err != nil {
		t.Fatal(err)
	}
	context["applicability"] = "legacy paraphrase retained for the original request"
	claim["memory_context"] = context
	prepared := map[string]any{"claims": claims, "event_type": "explicit_remember", "occurred_at": "2026-01-01T00:00:00Z",
		"generator": map[string]any{"model": "prior-model", "prompt_version": "capture-extract-v3", "pipeline_version": "prior-pipeline"}}
	before := dumps(prepared)
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		t.Fatal("prepared v3 retry re-extracted")
		return nil, nil
	}
	search = func(string, string, int, float64) ([]any, bool, error) {
		t.Fatal("prepared v3 retry searched")
		return nil, false, nil
	}
	posts := 0
	capPost = func(_ string, payload any, _ time.Duration) (map[string]any, error) {
		posts++
		if dumps(field(payload, "claims")) != dumps(claims) || !reflect.DeepEqual(mustLoad(t, dumps(field(payload, "generator"))), mustLoad(t, dumps(prepared["generator"]))) {
			t.Fatal("prepared v3 delivery changed the original context or generator")
		}
		return map[string]any{"items": []any{map[string]any{"status": "candidate"}}}, nil
	}
	job := map[string]any{"session_id": "legacy-context", "event_id": "legacy-context-event", "type": "turn_completed",
		"users": users, "prepared_capture": prepared}
	if err := process(job, func() error { return nil }); err != nil || posts != 1 || dumps(prepared) != before {
		t.Fatal("prepared v3 retry was rewritten or discarded", err, posts)
	}
}

func TestCaptureOptionalContextUngroundedReceiptRetriesWithoutExtraction(t *testing.T) {
	capFake(t)
	t.Setenv("ARKISM_SESSION_PROCESSOR", "")
	search = func(string, string, int, float64) ([]any, bool, error) { return []any{}, true, nil }
	models := 0
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		models++
		raw := groundedContextClaim()
		raw["memory_context"].(map[string]any)["applicability"] = "검수를 받지 않은 초안도 원문 유지"
		return []any{raw}, nil
	}
	capPost = func(string, any, time.Duration) (map[string]any, error) {
		t.Fatal("unsupported context was delivered")
		return nil, nil
	}
	job := map[string]any{"session_id": "unbound-context", "event_id": "unbound-context-event", "type": "turn_completed",
		"users": []any{map[string]any{"text": contextGroundingSource, "at": "2026-01-01T00:00:00Z"}}}
	checkpoints := 0
	if err := process(job, func() error { checkpoints++; return nil }); errorCode(err) != "capture_context_all_withheld" || models != 1 || checkpoints != 1 {
		t.Fatal("unsupported context looked like an empty success", err, models, checkpoints)
	}
	files, err := os.ReadDir(filepath.Join(capState, "claim-context-rejections"))
	if err != nil || len(files) != 1 {
		t.Fatal("fixed reason receipt was not retained", err, len(files))
	}
	envelope := mustLoad(t, dumps(field(job["prepared_capture"], "context_rejections")))
	receipt := field(envelope, "context_rejections")
	rejected, ok := field(receipt, "rejected").([]any)
	if !ok || len(rejected) != 1 || field(rejected[0], "reason") != "context_evidence_unbound" {
		t.Fatal("serialized receipt envelope lost the fixed rejection")
	}
	job = mustLoad(t, dumps(job))
	prepared := dumps(job["prepared_capture"]) // The persisted/read-back retry representation.
	if err := process(job, func() error { checkpoints++; return nil }); errorCode(err) != "capture_context_all_withheld" || models != 1 || checkpoints != 1 || dumps(job["prepared_capture"]) != prepared {
		t.Fatal("unsupported context retry changed its source-bound prepared request", err, models, checkpoints)
	}
}
