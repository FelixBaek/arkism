package crew

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func writeHome(t *testing.T, rel, text string) string {
	t.Helper()
	p := filepath.Join(host.Home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(p) })
	return p
}

func TestGrokModelsReadsBothShapesAndKeepsTheLastGoodList(t *testing.T) {
	patch(t, &grokLast, nil)
	writeHome(t, ".grok/models_cache.json", `{"models":{"grok-5":{},"grok-5-mini":{}}}`)
	eq(t, grokModels(), []string{"grok-5", "grok-5-mini"})
	writeHome(t, ".grok/models_cache.json", `{"models":["grok-6"]}`)
	eq(t, grokModels(), []string{"grok-6"})
	writeHome(t, ".grok/models_cache.json", `{"models":[`) // caught mid-write
	eq(t, grokModels(), []string{"grok-6"})
}

func TestCodexModelsFollowThePickerOrder(t *testing.T) {
	os.Remove(filepath.Join(host.Home, ".codex/models_cache.json"))
	if codexModels() != nil {
		t.Fatal("no cache must list nothing")
	}
	writeHome(t, ".codex/models_cache.json", `{"models":[
		{"slug":"gpt-late","visibility":"list"},
		{"slug":"gpt-hidden","visibility":"hide","priority":0},
		{"slug":"gpt-first","visibility":"list","priority":1},
		{"slug":"","visibility":"list","priority":0},
		"junk",
		{"slug":"gpt-bad-priority","visibility":"list","priority":"x"},
		{"slug":"gpt-second","visibility":"list","priority":5}]}`)
	eq(t, codexModels(), []string{"gpt-first", "gpt-second", "gpt-late", "gpt-bad-priority"})
}

func TestZaiModelsAsksOnceADayNewestFirst(t *testing.T) {
	path := stateFile("zai-models.json")
	os.Remove(path)
	t.Cleanup(func() { os.Remove(path) })
	var auth string
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		auth = r.Header.Get("Authorization")
		w.Write([]byte(`{"data":[{"id":"glm-4.6"},{"id":"glm-5"},{"id":"glm-5.3-flash"},{"id":"glm-5.3"},{"id":"glm-10"},{"id":"other"}]}`))
	}))
	defer srv.Close()
	patch(t, &host.ModelsURL, map[string]string{"zai": srv.URL})
	if got := zaiModels(); got != nil || hits != 0 {
		t.Fatal("no key file must not ask", got)
	}
	writeHome(t, ".config/claude-3p/zai.env", "OTHER=1\n")
	if got := zaiModels(); got != nil || hits != 0 {
		t.Fatal("a file without KEY must not ask", got)
	}
	writeHome(t, ".config/claude-3p/zai.env", "KEY='sk-zai'\n")
	eq(t, zaiModels(), []string{"glm-10", "glm-5.3", "glm-5.3-flash", "glm-5"})
	if auth != "Bearer sk-zai" {
		t.Fatalf("auth = %q", auth)
	}
	eq(t, zaiModels(), []string{"glm-10", "glm-5.3", "glm-5.3-flash", "glm-5"})
	if hits != 1 {
		t.Fatal("a fresh cache must not ask again", hits)
	}
	// A stale cache asks again; a failure keeps the stale list.
	os.WriteFile(path, []byte(`{"at":1,"models":["glm-5"]}`), 0o600)
	srv.Close()
	eq(t, zaiModels(), []string{"glm-5"})
}

func TestAgyModelsRunsTheCLIOnceADay(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script CLI")
	}
	path := stateFile("agy-models.json")
	os.Remove(path)
	t.Cleanup(func() { os.Remove(path) })
	t.Setenv("ARKISM_AGY_BIN", filepath.Join(t.TempDir(), "absent"))
	if agyModels() != nil {
		t.Fatal("no agy must list nothing")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "agy")
	os.WriteFile(exe, []byte("#!/bin/sh\nprintf 'NAME\\n gemini-3-pro\\tGemini 3 Pro\\nclaude-x\\tClaude\\n\\n'\n"), 0o755)
	t.Setenv("ARKISM_AGY_BIN", exe)
	eq(t, agyModels(), []string{"antigravity-gemini-3-pro", "antigravity-claude-x"})
	os.WriteFile(exe, []byte("#!/bin/sh\nexit 1\n"), 0o755)
	eq(t, agyModels(), []string{"antigravity-gemini-3-pro", "antigravity-claude-x"}) // fresh cache: not run
	os.WriteFile(path, []byte(`{"at":1,"models":["antigravity-old"]}`), 0o600)
	eq(t, agyModels(), []string{"antigravity-old"}) // stale, and the CLI printed nothing
}

type providersFake struct {
	mu      sync.Mutex
	items   []any
	reports []map[string]any
	fail    map[string]error
}

func (f *providersFake) call(path string, payload any, _ time.Duration) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key, _, _ := strings.Cut(path, "?")
	if err := f.fail[key]; err != nil {
		return nil, err
	}
	if strings.HasPrefix(path, "/v1/providers/handoff") {
		items := f.items
		f.items = nil
		return map[string]any{"items": items}, nil
	}
	if path == "/v1/providers" {
		v, err := host.Loads(host.MustDumps(payload, host.DumpOpts{}))
		if err != nil {
			return nil, err
		}
		f.reports = append(f.reports, obj(v))
	}
	return map[string]any{}, nil
}

func TestProvidersRoundCarriesOutRequestsAndReports(t *testing.T) {
	f := &providersFake{fail: map[string]error{}}
	patch(t, &runnerCall, f.call)
	f.items = []any{
		map[string]any{"request": map[string]any{"id": "r1", "kind": "key_remove", "key_id": "nope"}},
		map[string]any{"request": map[string]any{"id": "r2", "kind": "mystery"}},
		map[string]any{"request": map[string]any{"id": "r3", "kind": "model_test", "model": "", "provider": "openai"}},
	}
	n, err := providersRound(false)
	if err != nil {
		t.Fatal(err)
	}
	poolJobs.Wait()
	if n != 2 {
		t.Fatalf("n = %d", n)
	}
	results := map[string]map[string]any{}
	for _, r := range f.reports {
		for _, row := range list(r["results"]) {
			results[pyStr(obj(row)["id"])] = obj(row)
		}
	}
	eq(t, results["r2"], map[string]any{"id": "r2", "status": "failed", "error": "모르는 요청이에요"})
	if results["r1"] == nil || results["r3"] == nil || results["r3"]["status"] != "failed" {
		t.Fatalf("results = %v", results)
	}

	// Nothing waiting and no report due: no call to /v1/providers.
	before := len(f.reports)
	if n, err := providersRound(false); err != nil || n != 0 || len(f.reports) != before {
		t.Fatal(n, err, len(f.reports))
	}
	// A due report sends the inventory even with no requests.
	if _, err := providersRound(true); err != nil {
		t.Fatal(err)
	}
	if last := f.reports[len(f.reports)-1]; last["providers"] == nil {
		t.Fatalf("report without inventory: %v", last)
	}
	f.fail["/v1/providers/handoff"] = errors.New("down")
	if _, err := providersRound(true); err == nil {
		t.Fatal("handoff failure must surface")
	}
}

func TestModelTestResultIsKeptUntilReported(t *testing.T) {
	f := &providersFake{fail: map[string]error{"/v1/providers": errors.New("down")}}
	patch(t, &runnerCall, f.call)
	item := map[string]any{"request": map[string]any{"id": "mt1", "kind": "model_test", "model": "", "provider": "openai"}}
	modelTestJob(item)
	poolMu.Lock()
	kept := testsDone["mt1"]
	poolMu.Unlock()
	if kept == nil || kept["status"] != "failed" {
		t.Fatalf("unreported result not kept: %v", kept)
	}
	delete(f.fail, "/v1/providers")
	modelTestJob(item)
	poolMu.Lock()
	_, still := testsDone["mt1"]
	poolMu.Unlock()
	if still || len(f.reports) != 1 {
		t.Fatal("reported result not cleared", still, f.reports)
	}
}

func TestProvidersWatchReportsFailuresAndStops(t *testing.T) {
	f := &providersFake{fail: map[string]error{"/v1/providers/handoff": errors.New("down")}}
	stop := make(chan struct{})
	var once sync.Once
	patch(t, &runnerCall, func(path string, payload any, d time.Duration) (map[string]any, error) {
		once.Do(func() { close(stop) })
		return f.call(path, payload, d)
	})
	var errb bytes.Buffer
	done := make(chan struct{})
	go func() { providersWatchUntil(time.Millisecond, time.Hour, &errb, stop); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("watch did not stop")
	}
	if !strings.Contains(errb.String(), "모델 설정 동기화 실패") {
		t.Fatalf("stderr = %q", errb.String())
	}
}

func TestAnthropicModelsListsTheNewestOfEachFamilyAndRefreshSkipsTheCache(t *testing.T) {
	path := stateFile("anthropic-models.json")
	os.Remove(path)
	t.Cleanup(func() { os.Remove(path) })
	var auth, beta string
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		auth, beta = r.Header.Get("Authorization"), r.Header.Get("anthropic-beta")
		w.Write([]byte(`{"data":[{"id":"claude-haiku-5-5"},{"id":"claude-opus-5-5"},{"id":"claude-fable-5-1"},{"id":"claude-opus-5"},{"id":"claude-haiku-4-5-20251001"},{"id":"other"}]}`))
	}))
	defer srv.Close()
	patch(t, &host.ModelsURL, map[string]string{"anthropic": srv.URL})
	os.Remove(filepath.Join(host.Home, logins["claude"]))
	if got := anthropicModels(); got != nil || hits != 0 {
		t.Fatal("no sign-in must not ask", got)
	}
	writeHome(t, logins["claude"], `{"claudeAiOauth":{"accessToken":"tok"}}`)
	eq(t, anthropicModels(), []string{"haiku 5.5", "opus 5.5", "fable 5.1"})
	if auth != "Bearer tok" || beta != "oauth-2025-04-20" {
		t.Fatalf("auth = %q, beta = %q", auth, beta)
	}
	anthropicModels()
	if hits != 1 {
		t.Fatal("a fresh cache must not ask again", hits)
	}
	freshModels.Store(true)
	defer freshModels.Store(false)
	anthropicModels()
	if hits != 2 {
		t.Fatal("새로고침 must ask past the cache", hits)
	}
}
