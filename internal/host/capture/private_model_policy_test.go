package capture

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func privateTestPause(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(captureSettingsPath(), []byte(`{"paused":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateIdentityCannotBeRedactedIntoDifferentEvidence(t *testing.T) {
	for _, key := range []string{"source_id", "split_of", "session_id", "scope"} {
		t.Run(key, func(t *testing.T) {
			capFake(t)
			request, _ := sessionFixture(t)
			records := request["records"].([]any)
			scope, sid := any(map[string]any{}), "session"
			marked := "source<private>synthetic-identity</private>"
			switch key {
			case "session_id":
				sid = marked
			case "scope":
				scope = map[string]any{"project_id": marked}
			default:
				records[0].(map[string]any)[key] = marked
			}
			before := dumps(records)
			prepared, err := sessionPrepare(scope, sid, records, 0, 1, 0, nil, nil, "", func(string) error {
				t.Fatal("private identity reached authorization")
				return nil
			})
			if prepared != nil || errorCode(err) != "session_private_identity" || dumps(records) != before {
				t.Fatal(prepared, err)
			}
		})
	}
}

func TestPrivateIdentityNormalProvenanceSurvivesMaskingForEveryRole(t *testing.T) {
	for _, role := range []string{"user", "assistant", "tool"} {
		record := map[string]any{"source_id": "same-source", "split_of": "whole-source", "revision": 3, "role": role,
			"text": "visible <private>synthetic-text</private>"}
		rows, err := maskSessionRecords([]any{record})
		if err != nil || autosaveGet(rows[0], "text") != "visible [비공개]" {
			t.Fatal(rows, err)
		}
		for _, key := range []string{"source_id", "split_of", "revision", "role"} {
			if !eq(autosaveGet(rows[0], key), record[key]) {
				t.Fatal("provenance changed", key)
			}
		}
	}
}

func TestPrivatePolicyChangedDuringAuthorizationPreventsModel(t *testing.T) {
	capFake(t)
	fakeUsage(t, nil, errors.New("synthetic usage unavailable"))
	request, _ := sessionFixture(t)
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		t.Fatal("model ran after capture paused during authorization")
		return nil, nil
	}
	_, err := sessionPrepare(map[string]any{}, "session", request["records"].([]any), 0, 1, 0, nil, nil, "", func(string) error {
		privateTestPause(t)
		return nil
	})
	if !pending(err) || !strings.Contains(errorCode(err), "capture_policy_wait") {
		t.Fatal(err)
	}
}

func TestPrivatePolicyChangedDuringModelNeverPreparesCapture(t *testing.T) {
	for _, policy := range []string{"paused", "ignore"} {
		t.Run(policy, func(t *testing.T) {
			capFake(t)
			fakeUsage(t, nil, errors.New("synthetic usage unavailable"))
			request, output := sessionFixture(t)
			repository := t.TempDir()
			llm = func(string, string, string, time.Duration, string, string) (any, error) {
				if policy == "paused" {
					privateTestPause(t)
				} else if err := os.WriteFile(filepath.Join(repository, captureIgnoreFile), nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return output, nil
			}
			prepared, err := sessionPrepare(map[string]any{}, "session", request["records"].([]any), 0, 1, 0, nil, nil, "", func(string) error { return nil }, repository)
			if prepared != nil || errorCode(err) != "model_capture_policy_wait" {
				t.Fatal(prepared, err)
			}
		})
	}
}

func TestPrivatePolicyChangedDuringAutosaveExtractionPreventsSaveAndPersist(t *testing.T) {
	f := newAutosaveFixture(t)
	before := dumps(f.job)
	extract := func(any, []any) (any, error) { privateTestPause(t); return f.claims, nil }
	_, err := autosaveProcess(&f.job, f.request, f.scope, extract, func() error { t.Fatal("paused extraction persisted"); return nil })
	if errorCode(err) != "work_capture_policy_wait" || len(f.saves()) != 0 || dumps(f.job) != before {
		t.Fatal(err, f.saves())
	}
}

func TestPrivateEvidenceChecksOriginalPCFolderAndMetadataBeforeLookup(t *testing.T) {
	for _, policy := range []string{"source_ignore", "source_exclude", "metadata_ignore", "pause_after_get"} {
		t.Run(policy, func(t *testing.T) {
			capFake(t)
			root, project := t.TempDir(), t.TempDir()
			raw, err := os.ReadFile("testdata/evidence_zcode_snapshot.jsonl")
			if err != nil {
				t.Fatal(err)
			}
			_, body, ok := strings.Cut(string(raw), "\n")
			if !ok {
				t.Fatal("missing fixture header")
			}
			header, _ := json.Marshal(map[string]any{"session": map[string]any{"id": "s", "time_updated": timeNow() * 1000, "directory": project}})
			path := filepath.Join(root, sha256Hex([]byte("s"))+".jsonl")
			if err := os.WriteFile(path, append(append(header, '\n'), []byte(body)...), 0o600); err != nil {
				t.Fatal(err)
			}
			fixture, err := os.ReadFile("testdata/evidence_hints.json")
			if err != nil {
				t.Fatal(err)
			}
			var values map[string]any
			if err := json.Unmarshal(fixture, &values); err != nil {
				t.Fatal(err)
			}
			locator := values["locator"]
			switch policy {
			case "source_ignore":
				if err := os.WriteFile(filepath.Join(root, captureIgnoreFile), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "source_exclude":
				data, _ := json.Marshal(map[string]any{"exclude": []string{root}})
				if err := os.WriteFile(captureSettingsPath(), data, 0o600); err != nil {
					t.Fatal(err)
				}
			case "metadata_ignore":
				if err := os.WriteFile(filepath.Join(project, captureIgnoreFile), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			post := func(route string, _ any, _ time.Duration) (map[string]any, error) {
				calls++
				if policy != "pause_after_get" || calls != 1 || !strings.HasSuffix(route, "/get") {
					t.Fatal("policy allowed a forbidden evidence request", route)
				}
				privateTestPause(t)
				return nil, &host.HTTPError{Code: 404, Body: []byte(`{}`)}
			}
			_, err = evidencePrepareZcode(locator, []any{map[string]any{"text": "visible"}}, root, post, map[string]any{})
			if !pending(err) || calls != map[bool]int{true: 1, false: 0}[policy == "pause_after_get"] {
				t.Fatal(err, calls)
			}
		})
	}
}
