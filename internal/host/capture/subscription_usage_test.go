package capture

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestSubscriptionUsageUsesOnlyHarnessCredential(t *testing.T) {
	capFake(t)
	var got []map[string]any
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		got, auth = append(got, body), append(auth, r.Header.Get("Authorization"))
		if r.URL.Path != "/v1/model-budget/subscription-usage" {
			t.Error(r.URL.Path)
		}
		w.Write([]byte(`{"recorded":true}`))
	}))
	defer srv.Close()
	base := host.BaseURL
	host.BaseURL = srv.URL
	defer func() { host.BaseURL = base }()
	result := map[string]any{"modelUsage": map[string]any{"claude-haiku-5-5": map[string]any{"inputTokens": 10.0, "cacheReadInputTokens": 5.0, "outputTokens": 7.0}}}

	t.Setenv("ARKISM_HARNESS_TOKEN_FILE", "")
	recordSubscriptionUsage("capture", "haiku", result)
	if len(got) != 0 {
		t.Fatal("recorded without the harness credential (owner token fallback)")
	}
	file := filepath.Join(t.TempDir(), "harness.token")
	os.WriteFile(file, []byte("svc-token\n"), 0o600)
	t.Setenv("ARKISM_HARNESS_TOKEN_FILE", file)
	t.Setenv("ARKISM_MODEL_BUDGET_SCOPE", `{"workspace_id":"w","project_id":"p","user_id":"u","agent_id":"a"}`)
	recordSubscriptionUsage(llmOperation(sessionSystem), "haiku", result)
	recordSubscriptionUsage("capture", "haiku", map[string]any{"result": "no usage"})
	if len(got) != 1 || auth[0] != "Bearer svc-token" || got[0]["operation"] != "session_processor" {
		t.Fatal(got, auth)
	}
	usage := got[0]["usage"].(map[string]any)
	if usage["model"] != "claude-haiku-5-5" || usage["input_tokens"] != 15.0 || usage["output_tokens"] != 7.0 || got[0]["scope"].(map[string]any)["agent_id"] != "a" {
		t.Fatal(got[0])
	}
}

func TestSessionQualityGateDefaultsToShippedEvidence(t *testing.T) {
	request, output := sessionFixture(t)
	capFake(t)
	fakeUsage(t, nil, errors.New("no reading"))
	t.Setenv("ARKISM_HAIKU_QUALITY_GATE", "")
	// Shipped quality evidence covers v2, not the v3 citation contract. No category may reuse it.
	if gate := sessionQualityGate(); gate["prompt_version"] == sessionPromptVersion {
		t.Fatal("update this expectation only after reviewing new measured quality evidence")
	}
	for category, want := range map[string]string{"korean_correction": sessionModel, "conditional_preference": sessionModel, "mixed_personal_work": sessionModel, "": sessionModel} {
		var models []string
		llm = func(_, _, model string, _ time.Duration, _, _ string) (any, error) {
			models = append(models, model)
			return output, nil
		}
		if _, err := sessionPrepare(map[string]any{}, "session", request["records"].([]any), 0, 1, 0, nil, nil, category, func(string) error { return nil }); err != nil || len(models) != 1 || models[0] != want {
			t.Fatal(category, models, err)
		}
	}
	if sessionFastModel != "haiku" || sessionModel != "sonnet" {
		t.Fatal(sessionFastModel, sessionModel)
	}
}
