package capture

import "testing"

func TestSessionCandidateContextRequiresOneSupportingUserMessage(t *testing.T) {
	const source = "업무용 가람 보고서는 검수한 초안에만 적용한다. 긴급 보고서는 제외한다. 아직 잠정안이다. 2027년부터 시행한다."
	for _, mode := range []string{"literal", "empty", "invented", "paraphrase", "outside_quote", "split_quotes", "split_record", "cross_message", "split_parts", "prior_split_part", "parts_different_revisions", "parts_missing_parent", "parts_invalid_parent", "parts_invalid_number", "assistant", "legacy"} {
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
			case "split_parts", "prior_split_part", "parts_different_revisions", "parts_missing_parent", "parts_invalid_parent", "parts_invalid_number":
				first := map[string]any{"source_id": "s1-p1", "split_of": "s1", "revision": 1, "role": "user", "text": "긴급 보고서는 제외한다."}
				second := map[string]any{"source_id": "s1-p2", "split_of": "s1", "revision": 1, "role": "user", "text": "아직 잠정안이다. 2027년부터 시행한다."}
				if mode == "parts_different_revisions" {
					second["revision"] = 2
				}
				if mode == "parts_missing_parent" {
					delete(first, "split_of")
					delete(second, "split_of")
				}
				if mode == "parts_invalid_parent" {
					first["split_of"], second["split_of"] = 42, 42
				}
				if mode == "parts_invalid_number" {
					second["source_id"] = "s1-pinvalid"
				}
				request["records"] = []any{first, second}
				output["coverage"].(map[string]any)["source_ids"] = []any{first["source_id"], second["source_id"]}
				stateRef["source_id"], stateRef["quote"] = first["source_id"], first["text"]
				c["source_refs"] = []any{
					map[string]any{"source_id": first["source_id"], "revision": first["revision"], "quote": first["text"]},
					map[string]any{"source_id": second["source_id"], "revision": second["revision"], "quote": second["text"]},
				}
				if mode == "prior_split_part" {
					request["prior_evidence"] = []any{first}
					request["records"] = []any{second}
					output["coverage"].(map[string]any)["source_ids"] = []any{second["source_id"]}
				}
				if mode != "split_parts" && mode != "prior_split_part" {
					want = "candidate_context_evidence_mismatch"
				}
			case "split_record":
				first := request["records"].([]any)[0].(map[string]any)
				first["source_id"], first["split_of"], first["text"] = "s1-p1", "s1", "업무용 가람 보고서는 검수한 초안에만 적용한다. 긴급 보고서는 제외한다."
				request["records"] = append(request["records"].([]any), map[string]any{"source_id": "s1-p2", "split_of": "s1", "revision": 1, "role": "user", "text": " 아직 잠정안이다. 2027년부터 시행한다."})
				output["coverage"].(map[string]any)["source_ids"] = []any{"s1-p1", "s1-p2"}
				stateRef["source_id"], stateRef["quote"] = "s1-p1", first["text"]
				ref["source_id"], ref["quote"] = "s1-p1", "긴급 보고서는 제외한다."
				c["source_refs"] = append(c["source_refs"].([]any), map[string]any{"source_id": "s1-p2", "revision": 1, "quote": "아직 잠정안이다. 2027년부터 시행한다."})
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
