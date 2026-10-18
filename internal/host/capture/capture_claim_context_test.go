package capture

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func optionalClaimContext() map[string]any {
	return map[string]any{"domains": []any{"work"}, "applicability": "격리된 시험에서만 사용하고 운영에서는 사용하지 않는다.",
		"uncertainty": "아직 확정되지 않았다.", "valid_from": "검토 후 확정될 때부터", "semantic_kind": "preference"}
}

func optionalContextClaim(context any) map[string]any {
	return map[string]any{"text": "격리된 시험에서만 사용하고 운영에서는 사용하지 않는다. 아직 확정되지 않았으며 검토 후 확정될 때부터 적용한다.",
		"subject": "시험 설정", "kind": "user", "quote": "앞으로 시험 설정은 검토 후에만 적용해", "risk": "low", "memory_context": context}
}

func TestCaptureOptionalContextKeepsConditionsWithoutClaimingAuthority(t *testing.T) {
	capFake(t)
	raw := optionalContextClaim(optionalClaimContext())
	users := []any{map[string]any{"text": raw["quote"], "archive_ref": map[string]any{"archive_id": "unverified-shape"}}}
	claims, receipt, err := buildClaimsWithReceipt([]any{raw}, raw["quote"].(string), nil, users)
	if err != nil || len(claims) != 1 || receipt != nil {
		t.Fatal(err, claims, receipt)
	}
	claim := claims[0].(map[string]any)
	context := claim["memory_context"]
	if claim["text"] != raw["text"] || claim["basis"] != "explicit_user" || field(context, "evidence_kind") != "inference" {
		t.Fatal("body or evidence authority changed", claim)
	}
	if field(context, "observed_at") != nil || claim["effective_at"] != nil || claim["expires_at"] != nil {
		t.Fatal("model timing became observation or activation")
	}
	encoded := host.MustDumps(claims, host.DumpOpts{NoASCII: true})
	var wire []map[string]any
	if err := json.Unmarshal([]byte(encoded), &wire); err != nil || field(wire[0]["memory_context"], "valid_from") != optionalClaimContext()["valid_from"] {
		t.Fatal("context did not survive the actual capture serializer", err)
	}
}

func TestCaptureOptionalContextNilAndDomainRestriction(t *testing.T) {
	capFake(t)
	for _, mode := range []string{"absent", "null", "labels_only", "unknown_domain", "personal_only"} {
		t.Run(mode, func(t *testing.T) {
			raw := optionalContextClaim(nil)
			switch mode {
			case "absent":
				delete(raw, "memory_context")
			case "labels_only":
				raw["memory_context"] = map[string]any{"domains": []any{"personal"}, "applicability": "", "uncertainty": "", "valid_from": "", "semantic_kind": "preference"}
			case "personal_only":
				context := optionalClaimContext()
				context["domains"], context["applicability"] = []any{"personal"}, "개인 작업에만 적용하고 업무에는 적용하지 않는다."
				raw["memory_context"], raw["text"] = context, context["applicability"]
			}
			claims, receipt, err := buildClaimsWithReceipt([]any{raw}, raw["quote"].(string), nil, nil)
			if err != nil || len(claims) != 1 || receipt != nil || claims[0].(map[string]any)["text"] != raw["text"] {
				t.Fatal(err, claims, receipt)
			}
			context := claims[0].(map[string]any)["memory_context"]
			if (context != nil) != (mode == "personal_only") {
				t.Fatal("labels minted context, or a material domain restriction was discarded")
			}
		})
	}
}

func TestCaptureOptionalContextInvalidItemDoesNotDiscardOtherClaims(t *testing.T) {
	capFake(t)
	for _, mode := range []string{"extra_authority", "model_observation", "unknown_key", "missing_key", "empty_domain", "unknown_domain", "duplicate_domain", "array_text", "null_text", "NUL", "invalid_utf8", "long_runes", "duplicate_object_key"} {
		t.Run(mode, func(t *testing.T) {
			context := optionalClaimContext()
			var value any = context
			switch mode {
			case "extra_authority":
				context["evidence_kind"] = "user_statement"
			case "model_observation":
				context["observed_at"] = "2020-01-01T00:00:00Z"
			case "unknown_key":
				context["opaque_probe"] = "not exposed"
			case "missing_key":
				delete(context, "valid_from")
			case "empty_domain":
				context["domains"] = []any{}
			case "unknown_domain":
				context["domains"] = []any{"other"}
			case "duplicate_domain":
				context["domains"] = []any{"work", "work"}
			case "array_text":
				context["uncertainty"] = []any{"tentative"}
			case "null_text":
				context["uncertainty"] = nil
			case "NUL":
				context["uncertainty"] = "before\x00after"
			case "invalid_utf8":
				context["uncertainty"] = string([]byte{0xff})
			case "long_runes":
				context["uncertainty"] = strings.Repeat("한", 513)
			case "duplicate_object_key":
				row := host.Obj{}
				for key, item := range context {
					row = append(row, host.KV{Key: key, Value: item})
				}
				value = append(row, host.KV{Key: "domains", Value: []any{"work"}})
			}
			good := optionalContextClaim(optionalClaimContext())
			claims, receipt, err := buildClaimsWithReceipt([]any{optionalContextClaim(value), good}, good["quote"].(string), nil, nil)
			if err != nil || len(claims) != 1 || receipt == nil || len(receipt.Rejected) != 1 || receipt.Rejected[0].Index != 0 {
				t.Fatal("bad context discarded a batch or escaped its receipt", err, claims, receipt)
			}
			if !validClaimContextReceipt(receipt) || receipt.BindingStatus != "unbound" || receipt.UserInputSHA256 != sha256Hex([]byte(good["quote"].(string))) {
				t.Fatal("invalid rejection binding", receipt)
			}
		})
	}
}

func TestCaptureOptionalContextRuneAndSerializedBudgets(t *testing.T) {
	capFake(t)
	context := optionalClaimContext()
	context["uncertainty"] = strings.Repeat("한", 300)
	if parsed, _, reason := parseClaimContext(context); parsed == nil || reason != "" {
		t.Fatal("valid Korean runes were treated as byte count", reason)
	}
	for key, limit := range map[string]int{"applicability": 2000, "uncertainty": 512, "valid_from": 256} {
		context[key] = strings.Repeat("\x01", limit)
	}
	if _, _, reason := parseClaimContext(context); reason != "context_bytes_exceeded" {
		t.Fatal("serialized bytes were unbounded", reason)
	}
	for key, limit := range map[string]int{"applicability": 2000, "uncertainty": 512, "valid_from": 256} {
		context[key] = strings.Repeat("🙂", limit)
	}
	raw := []any{}
	for range 6 {
		raw = append(raw, optionalContextClaim(context))
	}
	raw = append(raw, optionalContextClaim(nil))
	claims, receipt, err := buildClaimsWithReceipt(raw, "input", nil, nil)
	if err != nil || len(claims) != 6 || receipt == nil || len(receipt.Rejected) != 1 || receipt.Rejected[0].Index != 5 || receipt.Rejected[0].Reason != "context_batch_bytes_exceeded" {
		t.Fatal("batch byte overflow lost an unrelated normal claim", err, len(claims), receipt)
	}
}

func TestCaptureOptionalContextHasNoRawAliases(t *testing.T) {
	capFake(t)
	context := optionalClaimContext()
	raw := optionalContextClaim(context)
	claims, _, err := buildClaimsWithReceipt([]any{raw, raw}, "input", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := host.MustDumps(claims, host.DumpOpts{})
	context["domains"].([]any)[0] = "personal"
	context["applicability"] = "changed input"
	if host.MustDumps(claims, host.DumpOpts{}) != before {
		t.Fatal("raw context mutation changed prepared claims")
	}
	field(claims[0].(map[string]any)["memory_context"], "domains").([]string)[0] = "personal"
	if field(claims[1].(map[string]any)["memory_context"], "domains").([]string)[0] != "work" {
		t.Fatal("two output claims share mutable context")
	}
}

func TestCaptureOptionalContextExportDoesNotReportWithheldAsEmptySuccess(t *testing.T) {
	capFake(t)
	var output []any
	llm = func(_, _, _ string, _ time.Duration, schema, _ string) (any, error) {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
			t.Fatal(err)
		}
		item := parsed["properties"].(map[string]any)["claims"].(map[string]any)["items"].(map[string]any)
		if item["properties"].(map[string]any)["memory_context"] == nil {
			t.Fatal("real extraction call did not advertise optional context")
		}
		return output, nil
	}
	invalid := optionalClaimContext()
	invalid["evidence_kind"] = "user_statement"
	output = []any{optionalContextClaim(invalid)}
	withheld, err := ExtractClaims("input", "2026-01-01", nil, "", "synthetic-context")
	if errorCode(err) != "capture_context_all_withheld" || len(withheld.Claims) != 0 || withheld.ContextRejections == nil || withheld.ContextRejections.BindingStatus != "unbound" {
		t.Fatal(err, withheld)
	}
	output = []any{}
	empty, err := ExtractClaims("input", "2026-01-01", nil, "", "synthetic-context")
	if err != nil || empty.ContextRejections != nil || !reflect.DeepEqual(empty.Claims, []any{}) {
		t.Fatal("real empty extraction lost its distinction", err, empty)
	}
}
