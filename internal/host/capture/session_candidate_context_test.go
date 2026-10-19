package capture

import "testing"

func TestSessionCandidateContextRequiresOneSupportingUserMessage(t *testing.T) {
	const source = "업무용 가람 보고서는 검수한 초안에만 적용한다. 긴급 보고서는 제외한다. 아직 잠정안이다. 2027년부터 시행한다."
	for _, mode := range []string{"literal", "empty", "invented", "paraphrase", "outside_quote", "split_quotes", "cross_message", "assistant", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			request, output := sessionFixture(t)
			request["records"].([]any)[0].(map[string]any)["text"] = source
			stateRef := output["session_state"].(map[string]any)["source_refs"].([]any)[0].(map[string]any)
			stateRef["quote"] = source
			c := output["memory_candidates"].([]any)[0].(map[string]any)
			c["statement"] = source
			c["applicability"], c["uncertainty"], c["valid_from"] = "긴급 보고서는 제외한다.", "아직 잠정안이다.", "2027년부터"
			ref := c["source_refs"].([]any)[0].(map[string]any)
			ref["quote"] = source
			want := ""
			switch mode {
			case "empty":
				c["applicability"], c["uncertainty"], c["valid_from"] = "", "", ""
			case "invented":
				c["applicability"] = "미검수 초안은 원문을 유지한다."
				want = "candidate_context_evidence_mismatch"
			case "cross_message":
				ref["quote"] = "긴급 보고서는 제외한다."
				request["records"] = append(request["records"].([]any), map[string]any{"source_id": "s2", "revision": 1, "role": "user", "text": "아직 잠정안이다. 2027년부터 시행한다."})
				output["coverage"].(map[string]any)["source_ids"] = []any{"s1", "s2"}
				c["source_refs"] = append(c["source_refs"].([]any), map[string]any{"source_id": "s2", "revision": 1, "quote": "아직 잠정안이다. 2027년부터 시행한다."})
				want = "candidate_context_evidence_mismatch"
			case "paraphrase":
				c["valid_from"] = "2027-01-01"
				want = "candidate_context_evidence_mismatch"
			case "outside_quote":
				ref["quote"] = "업무용 가람 보고서는 검수한 초안에만 적용한다."
				want = "candidate_context_evidence_mismatch"
			case "split_quotes":
				ref["quote"] = "긴급 보고서는 제외한다."
				c["source_refs"] = append(c["source_refs"].([]any), map[string]any{"source_id": "s1", "revision": 1, "quote": "아직 잠정안이다. 2027년부터 시행한다."})
			case "assistant":
				request["records"].([]any)[0].(map[string]any)["role"] = "assistant"
				want = "user_provenance_mismatch"
			case "legacy":
				request["prompt_version"] = "harness-session-v3"
				output["coverage"].(map[string]any)["prompt_version"] = "harness-session-v3"
				c["applicability"] = "legacy descriptive wording"
			}
			if err := validateSessionOutput(output, request); want == "" && err != nil || want != "" && errorCode(err) != want {
				t.Fatalf("context validation: got %v, want %q", err, want)
			}
		})
	}
}
