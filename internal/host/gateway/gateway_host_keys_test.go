package gateway

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// Target, codex profiles, execution and
// the routing list with stored host API keys. The key selection itself (chat_connection) is
// covered in internal/host/subscription/gateway_auth_test.go.

const (
	gxHostKey   = "synthetic-host-secret"
	gxSafeError = "host_api_configuration_unavailable"
)

var gxProviders = []struct{ provider, cli, name, base string }{
	{"openai", "codex", "gpt-test", "https://api.openai.com/v1"},
	{"xai", "grok", "grok-test", "https://api.x.ai/v1"},
}

// gxKeys isolates host files, sets API mode and writes the keys file (rows is a JSON value).
func gxKeys(t *testing.T, mode []string, rows string) {
	t.Helper()
	isolate(t, t.TempDir())
	for _, cli := range mode {
		host.APIMode[cli] = true
	}
	if rows != "" {
		writeFile(t, host.KeysFile, `{"keys":`+rows+`}`)
	}
}

func gxWantRefusal(t *testing.T, model string) {
	t.Helper()
	w, err := target(model)
	var crew *host.CrewError
	if w != nil || !errors.As(err, &crew) || err.Error() != gxSafeError {
		t.Fatalf("%s: %+v %v", model, w, err)
	}
	for _, secret := range []string{gxHostKey, "synthetic-caller-secret", host.KeysFile} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("%s: leaked %q", model, secret)
		}
	}
}

func TestHostKeysAPIModesSelectFirstMatchingNonemptyKeyAndDefaultURL(t *testing.T) {
	for _, mode := range [][]string{{"codex"}, {"grok"}, {"codex", "grok"}, {"all"}} {
		for _, p := range gxProviders {
			gxKeys(t, mode, `[{"provider":"anthropic","key":"synthetic-other-secret"},{"provider":"`+p.provider+`","key":""},`+
				`{"provider":"`+p.provider+`","key":"`+gxHostKey+`"},`+
				`{"provider":"`+p.provider+`","key":"synthetic-later-secret","base_url":"https://later.invalid/v1"}]`)
			w, err := target(p.provider + "/" + p.name)
			on := host.APIModeOn(p.cli)
			switch {
			case on && (err != nil || *w != where{kind: "chat", base: p.base, key: gxHostKey, name: p.name}):
				t.Fatalf("%v %s: %+v %v", mode, p.provider, w, err)
			case !on && p.provider == "openai" && (err != nil || *w != where{kind: "codex", model: p.name}):
				t.Fatalf("%v %s: %+v %v", mode, p.provider, w, err)
			case !on && p.provider == "xai" && (err != nil || w != nil):
				t.Fatalf("%v %s: %+v %v", mode, p.provider, w, err)
			}
		}
	}
}

func TestHostKeysStoredBaseURLIsUsedAndTrailingSlashesRemoved(t *testing.T) {
	for _, base := range []string{"https://host.invalid:443/v1///", "http://127.0.0.1:8099/v1/",
		"http://[::1]:8099/v1/", "https://host.invalid:1/v1", "https://host.invalid:65535/v1"} {
		for _, p := range gxProviders {
			gxKeys(t, []string{"all"}, `[{"provider":"`+p.provider+`","key":"`+gxHostKey+`","base_url":"`+base+`"}]`)
			w, err := target(p.provider + "/" + p.name)
			if err != nil || *w != (where{kind: "chat", base: strings.TrimRight(base, "/"), key: gxHostKey, name: p.name}) {
				t.Fatalf("%s %s: %+v %v", base, p.provider, w, err)
			}
		}
	}
}

func TestHostKeysAPIModeOffDefersCodexAccountSelectionUntilExecution(t *testing.T) {
	gxKeys(t, nil, "")
	writeFile(t, host.KeysFile, `{"keys":[]}`)
	os.Chmod(host.KeysFile, 0o644) // reading it would fail the target
	if w, err := target("openai/gpt-test"); err != nil || *w != (where{kind: "codex", model: "gpt-test"}) {
		t.Fatalf("openai: %+v %v", w, err)
	}
	if w, err := target("xai/grok-test"); err != nil || w != nil {
		t.Fatalf("xai: %+v %v", w, err)
	}
}

func TestHostKeysAPIModeOnNeverUsesSubscriptionProfileEvenWhenKeyMissing(t *testing.T) {
	gxKeys(t, []string{"all"}, "")
	for _, p := range gxProviders {
		if profile := codexProfile(p.name, nil); profile != nil {
			t.Fatalf("%s: %v", p.provider, profile)
		}
		gxWantRefusal(t, p.provider+"/"+p.name)
	}
}

func TestHostKeysMissingKeysOrOnlyOtherProviderKeysRefuseEnvCredentials(t *testing.T) {
	for key, value := range map[string]string{"OPENAI_API_KEY": "synthetic-caller-secret", "CODEX_API_KEY": "synthetic-caller-secret",
		"XAI_API_KEY": "synthetic-caller-secret", "OPENAI_BASE_URL": "https://caller.invalid/v1", "XAI_BASE_URL": "https://caller.invalid/v1"} {
		t.Setenv(key, value)
	}
	for _, p := range gxProviders {
		other := map[string]string{"openai": "xai", "xai": "openai"}[p.provider]
		for _, rows := range []string{`[]`, `[{"provider":"custom","key":"` + gxHostKey + `"}]`,
			`[{"provider":"` + other + `","key":"` + gxHostKey + `"}]`, `[{"provider":"` + p.provider + `","key":""}]`,
			`[{"provider":"` + p.provider + `","key":null}]`} {
			gxKeys(t, []string{"all"}, rows)
			gxWantRefusal(t, p.provider+"/"+p.name)
		}
	}
}

func TestHostKeysCallerHeadersAndEnvCannotReplaceHostAuthOrEndpoint(t *testing.T) {
	for key, value := range map[string]string{"OPENAI_API_KEY": "synthetic-caller-secret", "CODEX_API_KEY": "synthetic-caller-secret",
		"XAI_API_KEY": "synthetic-caller-secret", "OPENAI_BASE_URL": "https://caller.invalid/v1", "XAI_BASE_URL": "https://caller.invalid/v1"} {
		t.Setenv(key, value)
	}
	for _, p := range gxProviders {
		f := newFixture(t)
		swap(t, &target, resolve)
		host.APIMode["all"] = true
		// The provider default endpoint, kept local.
		defaults := map[string]string{}
		for k, v := range host.ModelsURL {
			defaults[k] = v
		}
		defaults[p.provider] = f.base + "/v1"
		swap(t, &host.ModelsURL, defaults)
		writeFile(t, host.KeysFile, `{"keys":[{"provider":"`+p.provider+`","key":"`+gxHostKey+`"}]}`)
		f.reply(p.name, respChatReply(t, p.name))
		request := `{"model":"` + p.provider + "/" + p.name + `","messages":[{"role":"user","content":"test"}]}`
		reply := f.post(request, "authorization", "Bearer synthetic-caller-secret", "x-api-key", "synthetic-caller-secret")
		if reply.status != 200 || f.seenCount() != 1 {
			t.Fatalf("%s: %d %s", p.provider, reply.status, reply.body)
		}
		seen := f.seenAt(0)
		if seen.path != "/v1/chat/completions" || seen.header.Get("authorization") != "Bearer "+gxHostKey ||
			seen.header.Get("content-type") != "application/json" || seen.header.Get("x-api-key") != "" ||
			strings.Contains(dumpJSON(seen.header), "caller-") || strings.Contains(dumpJSON(seen.header), "synthetic-caller-secret") {
			t.Fatalf("%s: %s %v", p.provider, seen.path, seen.header)
		}
		same(t, seen.body, `{"model":"`+p.name+`","messages":[{"role":"user","content":"test"}]}`)
	}
}

func TestHostKeysFileFailuresHaveFixedErrorWithoutPathOrSecret(t *testing.T) {
	for _, c := range []struct {
		name, text string
		mode       os.FileMode
	}{{"permission", `{"keys":[{"provider":"openai","key":"` + gxHostKey + `"},{"provider":"xai","key":"` + gxHostKey + `"}]}`, 0o644},
		{"invalid", `{"keys": ` + gxHostKey, 0o600}, {"shape", `{"keys":{"provider":"openai"}}`, 0o600}} {
		for _, p := range gxProviders {
			gxKeys(t, []string{"all"}, "")
			writeFile(t, host.KeysFile, c.text)
			os.Chmod(host.KeysFile, c.mode)
			gxWantRefusal(t, p.provider+"/"+p.name)
		}
	}
}

func TestHostKeysInvalidFirstTokenRefusesInsteadOfSelectingNextKey(t *testing.T) {
	for _, p := range gxProviders {
		for _, token := range []string{`"bad token"`, `"bad\tkey"`, `"bad\r\nHeader: injected"`, `"bad\u0000key"`, `"bad\u001fkey"`,
			`"bad\u007fkey"`, `"한글"`, `123`, `true`, `["key"]`, `{"key":"value"}`, `"` + strings.Repeat("x", 65537) + `"`} {
			gxKeys(t, []string{"all"}, `[{"provider":"`+p.provider+`","key":`+token+`},{"provider":"`+p.provider+`","key":"`+gxHostKey+`"}]`)
			gxWantRefusal(t, p.provider+"/"+p.name)
		}
	}
}

func TestHostKeysTokenLengthBoundaryIsAccepted(t *testing.T) {
	for _, p := range gxProviders {
		for _, token := range []string{"k", strings.Repeat("k", 65536)} {
			gxKeys(t, []string{"all"}, `[{"provider":"`+p.provider+`","key":"`+token+`"}]`)
			if w, err := target(p.provider + "/" + p.name); err != nil || w.key != token || w.base != p.base {
				t.Fatalf("%s %d: %v", p.provider, len(token), err)
			}
		}
	}
}

func TestHostKeysInvalidFirstURLRefusesInsteadOfSelectingNextKey(t *testing.T) {
	urls := []any{"file:///synthetic/private/provider-keys.json", "ftp://host.invalid/v1", "/v1", "https:///v1",
		"https://host.invalid:0/v1", "https://host.invalid:-1/v1", "https://host.invalid:65536/v1",
		"https://host.invalid:port/v1", "https://host.invalid:/v1", "https://[::1/v1",
		"https://user@host.invalid/v1", "https://user:password@host.invalid/v1", "https://@host.invalid/v1",
		"https://host.invalid/v1?key=" + gxHostKey, "https://host.invalid/v1?",
		"https://host.invalid/v1#" + gxHostKey, "https://host.invalid/v1#",
		"https://host.invalid/v 1", "https://host.invalid/\t/v1", "https://host.invalid/\r\n/v1",
		"https://host.invalid/\x00/v1", "https://host.invalid/\x1f/v1", "https://host.invalid/\x7f/v1",
		"https://host.invalid/ /v1", "https://host.invalid\\other/v1",
		"https://host.invalid/" + strings.Repeat("x", 2048), 123, true, false, 0, []any{}, M{}, []any{"https://host.invalid/v1"}}
	for _, p := range gxProviders {
		for _, base := range urls {
			gxKeys(t, []string{"all"}, dumpJSON([]any{M{"provider": p.provider, "key": gxHostKey, "base_url": base},
				M{"provider": p.provider, "key": "synthetic-later-secret"}}))
			gxWantRefusal(t, p.provider+"/"+p.name)
		}
	}
}

func TestHostKeysExistingNonHostTargetsKeepTheirShapeWithoutKeyReads(t *testing.T) {
	gxKeys(t, []string{"all"}, `[]`)
	os.Chmod(host.KeysFile, 0o644) // reading it would fail the target
	for model, want := range map[string]*where{"claude-test": {kind: "anthropic"}, "anthropic/claude-test": {kind: "anthropic"},
		"google/Gemini Test": {kind: "google", model: "gemini-test"}, "unknown/test": nil} {
		w, err := target(model)
		if err != nil || (w == nil) != (want == nil) || w != nil && *w != *want {
			t.Fatalf("%s: %+v %v", model, w, err)
		}
	}
}

func TestHostKeysRoutingListUsesRealTargetAndFiltersDisabledOrUnavailableModels(t *testing.T) {
	rows := `[{"id":"openai/gpt-test","enabled":true,"label":"OpenAI"},{"id":"xai/grok-test","enabled":true,"label":"xAI"},` +
		`{"id":"anthropic/claude-test","enabled":true},{"id":"openai/gpt-disabled","enabled":false},` +
		`{"id":"unknown/test","enabled":true},{"id":"invalid","enabled":true}]`
	groups := `[{"id":"group/api","enabled":true,"members":["xai/grok-test"]},` +
		`{"id":"group/disabled","enabled":false,"members":["openai/gpt-test"]},` +
		`{"id":"group/unavailable","enabled":true,"members":["unknown/test"]}]`
	var calls atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("authorization") != "Bearer synthetic-engine-token" {
			w.WriteHeader(401)
			return
		}
		items := rows
		if r.URL.Path == "/v1/routing/groups" {
			items = groups
		}
		w.Write([]byte(`{"authority":"data_only_untrusted","items":` + items + `}`))
	}))
	t.Cleanup(engine.Close)
	swap(t, &host.BaseURL, engine.URL)
	for _, c := range []struct {
		mode   []string
		broken bool
		want   string
	}{{nil, false, `["openai/gpt-test","anthropic/claude-test"]`},
		{[]string{"codex"}, false, `["openai/gpt-test","anthropic/claude-test"]`},
		{[]string{"grok"}, false, `["openai/gpt-test","xai/grok-test","anthropic/claude-test","group/api"]`},
		{[]string{"all"}, false, `["openai/gpt-test","xai/grok-test","anthropic/claude-test","group/api"]`},
		{[]string{"all"}, true, `["anthropic/claude-test"]`}} {
		key := gxHostKey
		if c.broken {
			key = `bad\r\nkey`
		}
		gxKeys(t, c.mode, `[{"provider":"openai","key":"`+key+`"},{"provider":"xai","key":"`+key+`"}]`)
		if len(c.mode) == 0 {
			os.Chmod(host.KeysFile, 0o644) // API mode off must not read keys
		}
		writeFile(t, host.TokenFile, "synthetic-engine-token\n")
		calls.Store(0)
		result := routingModels()
		ids := []any{}
		for _, row := range result {
			ids = append(ids, get(row, "id"))
		}
		same(t, ids, c.want)
		same(t, routingModels(), dumpJSON(result))
		if calls.Load() != 2 || strings.Contains(dumpJSON(result), gxHostKey) {
			t.Fatalf("%v: engine calls %d", c.mode, calls.Load())
		}
	}
}

func TestHostKeysModelsResponseRetainsChatAndMessagesListFields(t *testing.T) {
	f := newFixture(t)
	swap(t, &routingModels, func() []host.Obj {
		return []host.Obj{{{Key: "id", Value: "openai/gpt-test"}, {Key: "label", Value: "OpenAI"}}, {{Key: "id", Value: "xai/grok-test"}}}
	})
	reply := fetch(t, f.gw.URL+"/v1/models")
	if reply.status != 200 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	result := parsed(t, reply.body)
	if at(result, "object") != "list" || at(result, "has_more") != false {
		t.Fatalf("%s", reply.body)
	}
	data := at(result, "data").([]any)
	for i, want := range [][2]string{{"openai/gpt-test", "OpenAI"}, {"xai/grok-test", "xai/grok-test"}} {
		row := data[i]
		owner, _, _ := strings.Cut(want[0], "/")
		if len(data) != 2 || at(row, "id") != want[0] || at(row, "display_name") != want[1] || at(row, "type") != "model" ||
			at(row, "object") != "model" || at(row, "created_at") != "2026-01-01T00:00:00Z" || at(row, "owned_by") != owner {
			t.Fatalf("row %d: %s", i, dumpJSON(row))
		}
		if _, ok := host.Int(at(row, "created")); !ok {
			t.Fatalf("created %v", at(row, "created"))
		}
	}
}
