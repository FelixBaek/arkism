package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func errorBody(t *testing.T, err error) (int, map[string]string) {
	t.Helper()
	w := httptest.NewRecorder()
	respond(w, nil, err)
	body := map[string]string{}
	if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
		t.Fatal(e)
	}
	return w.Code, body
}

func TestErrorResponsesCarryAReason(t *testing.T) {
	code, body := errorBody(t, fmt.Errorf("wrapped: %w", invalidf("limit: must be 1 to 50")))
	if code != 400 || body["code"] != "invalid_request" || body["detail"] != "limit: must be 1 to 50" {
		t.Fatalf("invalid: %d %v", code, body)
	}
	code, body = errorBody(t, revisionConflict(3, 5))
	if code != 409 || !strings.Contains(body["detail"], "3") || !strings.Contains(body["detail"], "5") {
		t.Fatalf("conflict: %d %v", code, body)
	}
	if code, body = errorBody(t, ErrInvalid); code != 400 || body["detail"] != "" {
		t.Fatalf("bare: %d %v", code, body)
	}
	// A write the database's row rules refuse is a permission answer, not an outage.
	if code, body = errorBody(t, rlsRefusal{}); code != 403 || body["code"] != "access_denied" || body["detail"] == "" {
		t.Fatalf("refusal: %d %v", code, body)
	}
}

func TestDecodeNamesTheBadField(t *testing.T) {
	for in, want := range map[string]string{
		`{"limit":"x"}`:      "limit: wrong JSON type",
		`{"nope":1}`:         `unknown field \"nope\"`,
		`{"limit":1`:         "body: JSON ends early",
		``:                   "body: empty",
		`{"limit":1}{"a":2}`: "more than one JSON value",
		`{"limit":1,"pad":"` + strings.Repeat("x", 200) + `"}`: "larger than 64 bytes",
	} {
		w := httptest.NewRecorder()
		var v struct {
			Limit int    `json:"limit"`
			Pad   string `json:"pad"`
		}
		if decodeLimit(w, httptest.NewRequest("POST", "/", strings.NewReader(in)), &v, 64) {
			t.Fatalf("%q decoded", in)
		}
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("%q: got %s, want %q", in, w.Body.String(), want)
		}
	}
}

func TestSearchRefusesUnknownKindsAndLocale(t *testing.T) {
	if err := knownSearchKinds(colMemories, []string{"lesson", "nope"}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `"nope"`) {
		t.Fatalf("kinds: %v", err)
	}
	if err := knownSearchKinds(colMemories, []string{"lesson", "decision"}); err != nil {
		t.Fatal(err)
	}
	if err := knownSearchKinds("sources", []string{"conversation"}); err != nil {
		t.Fatal(err)
	}
	for tag, ok := range map[string]bool{"ko": true, "en-US": true, "zh_Hant_TW": true, "!!": false, "k": false} {
		if localeTag.MatchString(tag) != ok {
			t.Errorf("locale %q", tag)
		}
	}
}

func TestSkillSizeReason(t *testing.T) {
	err := skillSizeReason(ImportSkillInput{Content: strings.Repeat("x", maxSkillContentBytes+1)})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "64001 bytes") {
		t.Fatalf("content: %v", err)
	}
	if err := skillSizeReason(ImportSkillInput{Content: "ok"}); err != nil {
		t.Fatal(err)
	}
}

// A rate limited model is asked once more; a second refusal says which kind of failure it was.
func TestChatRetriesOnceAndNamesTheFailure(t *testing.T) {
	old := llmRetryWait
	llmRetryWait = time.Millisecond
	defer func() { llmRetryWait = old }()
	calls, failures := 0, 1
	status := http.StatusTooManyRequests
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls <= failures {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()
	cfg := LLMConfig{Endpoint: server.URL, APIKey: "test", Model: "m"}
	req := chatRequest{op: "reflect", provider: "llm", user: "hello", maxTokens: 16}
	if reply, err := cfg.chat(context.Background(), req); err != nil || reply.text != "hi" || calls != 2 {
		t.Fatalf("retry: %v %q calls %d", err, reply.text, calls)
	}
	for code, want := range map[int]string{http.StatusTooManyRequests: "rate limited", http.StatusInternalServerError: "provider failed"} {
		calls, failures, status = 0, 9, code
		_, err := cfg.chat(context.Background(), req)
		if code, body := errorBody(t, err); calls != 2 || code != 503 || !strings.Contains(body["detail"], want) {
			t.Errorf("%d: calls %d, %d %v", status, calls, code, body)
		}
	}
}

func TestContextPackMarksBudgetDrops(t *testing.T) {
	out := ContextBundle{Items: []Hit{{ID: "a", Text: strings.Repeat("word ", 400)}}}
	if err := packContext(context.Background(), &out, 60, "cl100k_base"); err != nil {
		t.Fatal(err)
	}
	pack := contextPack(nil, out, nil)
	if len(out.Items) != 0 || !pack.Partial || !strings.Contains(pack.Notice, "1건") {
		t.Fatalf("items %d partial %v notice %q", len(out.Items), pack.Partial, pack.Notice)
	}
}
