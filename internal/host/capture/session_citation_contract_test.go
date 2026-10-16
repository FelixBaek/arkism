package capture

import (
	"os"
	"strings"
	"testing"
)

func TestSessionCitationSchemaRejectsEmptyEvidence(t *testing.T) {
	capFake(t)
	for _, field := range []string{"state", "check:not_run", "check:unknown", "candidate"} {
		t.Run(field, func(t *testing.T) {
			request, output := sessionFixture(t)
			state := output["session_state"].(map[string]any)
			switch field {
			case "state":
				state["source_refs"] = []any{}
			case "candidate":
				output["memory_candidates"].([]any)[0].(map[string]any)["source_refs"] = []any{}
			default:
				state["checks"] = []any{map[string]any{"command": "go test", "outcome": field[6:], "summary": "No observed execution", "source_refs": []any{}}}
			}
			if err := validateSessionSchema(output, sessionSchema); !pending(err) || errorCode(err) != "schema_array_min_items" {
				t.Fatalf("empty %s evidence accepted by model schema: %v", field, err)
			}
			if err := validateSessionOutput(output, request); !pending(err) {
				t.Fatalf("empty evidence accepted by output validator: %v", err)
			}
		})
	}
}

func TestSessionCitationSchemaAllowsOmittingUnsupportedItems(t *testing.T) {
	request, output := sessionFixture(t)
	output["memory_candidates"] = []any{}
	if err := validateSessionOutput(output, request); err != nil {
		t.Fatal(err)
	}
}

func TestSessionCitationSchemaPreservesObservedChecksAndStrictReferences(t *testing.T) {
	for _, mutation := range []string{"valid", "quote", "revision", "role"} {
		t.Run(mutation, func(t *testing.T) {
			request, output := sessionFixture(t)
			tool := map[string]any{"source_id": "tool1", "revision": 1, "role": "tool", "text": "go test: PASS"}
			request["records"] = append(request["records"].([]any), tool)
			output["coverage"].(map[string]any)["source_ids"] = []any{"s1", "tool1"}
			ref := map[string]any{"source_id": "tool1", "revision": 1, "quote": "go test: PASS"}
			state := output["session_state"].(map[string]any)
			check := map[string]any{"command": "go test", "outcome": "passed", "summary": "Observed result", "source_refs": []any{ref}}
			state["checks"] = []any{check}
			switch mutation {
			case "quote":
				ref["quote"] = "invented success"
			case "revision":
				ref["revision"] = 2
			case "role":
				// Keep a real tool in the input: it cannot legitimize citing a user.
				ref["source_id"], ref["quote"] = "s1", request["records"].([]any)[0].(map[string]any)["text"]
			}
			err := validateSessionOutput(output, request)
			if mutation == "valid" {
				if err != nil || check["outcome"] != "passed" {
					t.Fatalf("valid observation lost: %v", err)
				}
			} else if !pending(err) {
				t.Fatalf("invalid %s evidence accepted: %v", mutation, err)
			}
		})
	}
}

func TestSessionCitationSchemaAdvertisesNonemptyReferences(t *testing.T) {
	properties := sessionSchema["properties"].(map[string]any)
	state := properties["session_state"].(map[string]any)["properties"].(map[string]any)
	check := state["checks"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	candidate := properties["memory_candidates"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	for name, properties := range map[string]map[string]any{"state": state, "check": check, "candidate": candidate} {
		if properties["source_refs"].(map[string]any)["minItems"] != 1 {
			t.Errorf("%s evidence schema permits empty references", name)
		}
	}
}

func TestSessionCitationSchemaKeepsSafeMissingFieldDiagnostics(t *testing.T) {
	realMetric := metric
	capFake(t)
	metric = realMetric
	request, output := sessionFixture(t)
	state := output["session_state"].(map[string]any)
	state["checks"] = []any{map[string]any{"command": "PRIVATE-COMMAND", "outcome": "not_run", "summary": "PRIVATE-SUMMARY", "source_refs": []any{}}}
	if err := validateSessionOutput(output, request); errorCode(err) != "schema_array_min_items" {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(metricsFile())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "session_evidence_missing") || !strings.Contains(string(raw), "check:not_run") || strings.Contains(string(raw), "PRIVATE-") {
		t.Fatalf("missing field diagnostic absent or leaked content: %s", raw)
	}
	state["checks"].([]any)[0].(map[string]any)["outcome"] = "PRIVATE-INVALID-OUTCOME"
	if field := sessionEmptyEvidenceField(output); field != "check" {
		t.Fatal(field)
	}
}
