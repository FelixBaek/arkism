package crew

// Provider keys: the secret lives in the OS credential store (keystore) when one answers, with the keys file (0600)
// holding only metadata and a "secret_ref"; on a host without one (a headless server) the secret stays in the file
// as before. Each row says which ("storage": "keyring" | "file"). Secrets reach a child CLI through its environment,
// never its arguments, and are never printed or logged.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/crew/keystore"
)

var providers = []string{"openai", "anthropic", "google", "zai", "xai", "custom"}

const bearer, jsonType = "Bearer ", "application/json"

// crewErr is a *host.CrewError (a Korean message safe to show).
func crewErr(msg string) error { return &host.CrewError{Msg: msg} }

func isCrewErr(err error) bool {
	var c *host.CrewError
	return errors.As(err, &c)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func nowZ() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

// sOr is str(v or "") for a JSON value.
func sOr(v any) string {
	if !truthy(v) {
		return ""
	}
	return pyStr(v)
}

// ordered writes m's keys in the given order first (the rest sorted), the way Python kept insertion order.
func ordered(m map[string]any, order ...string) host.Obj {
	out := host.Obj{}
	for _, k := range order {
		if v, ok := m[k]; ok {
			out = append(out, host.KV{Key: k, Value: v})
		}
	}
	for _, k := range sortedKeys(m) {
		if !in(order, k) {
			out = append(out, host.KV{Key: k, Value: m[k]})
		}
	}
	return out
}

// loadKeys reads the keys file with every keyring secret resolved into "key", and moves any plaintext secret into
// the credential store when one answers.
func loadKeys() ([]map[string]any, error) {
	keys, err := host.LoadKeys()
	if err != nil {
		return nil, err
	}
	keystore.Resolve(keys)
	migrateKeys(keys)
	return keys, nil
}

// migrateKeys moves plaintext secrets into the credential store: each is written and read back before its row is
// marked, and only then is the file rewritten without them. A failure anywhere leaves the plaintext row as it was
// (a stored copy is harmless: the next load overwrites it), so no key is ever lost.
func migrateKeys(keys []map[string]any) {
	moved := false
	for _, k := range keys {
		id, _ := k["id"].(string)
		if k["storage"] != keystore.StorageFile || id == "" || !truthy(k["key"]) || !keystore.Available() {
			continue
		}
		ref, err := keystore.Put(id, str(k["key"]))
		if err != nil {
			continue
		}
		k["secret_ref"], k["storage"] = ref, keystore.StorageKeyring
		moved = true
	}
	if moved {
		_ = saveKeys(keys) // on failure the file still holds the plaintext; the verified stored copies stand
	}
}

func saveKeys(keys []map[string]any) error {
	if err := os.MkdirAll(host.AgentsState, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(host.KeysFile), 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(host.KeysFile), "."+filepath.Base(host.KeysFile)+".tmp")
	rows := make([]any, len(keys))
	for i, k := range keys {
		row := make(map[string]any, len(k))
		for f, v := range k {
			row[f] = v
		}
		if ref, _ := row["secret_ref"].(string); ref != "" && row["storage"] == keystore.StorageKeyring {
			delete(row, "key") // the secret is in the credential store; the file keeps only the reference
		} else {
			delete(row, "secret_ref")
			row["storage"] = keystore.StorageFile
		}
		rows[i] = ordered(row, "id", "provider", "name", "key", "secret_ref", "storage", "base_url", "monthly_budget_usd", "created_at")
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(dumpIndent(host.Obj{{Key: "keys", Value: rows}}))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, host.KeysFile)
}

func publicKey(k map[string]any) host.Obj {
	r := []rune(sOr(k["key"]))
	if len(r) > 4 {
		r = r[len(r)-4:]
	}
	return host.Obj{{Key: "id", Value: k["id"]}, {Key: "provider", Value: k["provider"]}, {Key: "name", Value: k["name"]},
		{Key: "last4", Value: string(r)}, {Key: "base_url", Value: sOr(k["base_url"])},
		{Key: "monthly_budget_usd", Value: k["monthly_budget_usd"]}, {Key: "created_at", Value: k["created_at"]},
		{Key: "storage", Value: keyStorage(k)}}
}

// keyStorage is where a row's secret lives: "keyring" or "file".
func keyStorage(k map[string]any) string {
	if k["storage"] == keystore.StorageKeyring {
		return keystore.StorageKeyring
	}
	return keystore.StorageFile
}

var baseURLRe = regexp.MustCompile(`^https?://\S+$`)

// checkBaseURL accepts an empty address or http(s); a Z.ai key travels in the request, so its address must be https.
func checkBaseURL(provider, baseURL string) error {
	if provider == "zai" && baseURL != "" && !strings.HasPrefix(baseURL, "https://") {
		return crewErr("Z.ai 주소는 https://로 시작해야 해요")
	}
	if baseURL != "" && !baseURLRe.MatchString(baseURL) {
		return crewErr("주소는 http(s)://로 시작해야 해요")
	}
	return nil
}

func addKey(provider, name, key, baseURL string, budget any, keyID string) (host.Obj, error) {
	key = strings.TrimSpace(key)
	if !in(providers, provider) {
		return nil, crewErr("제공자는 " + strings.Join(providers, "·") + " 중 하나여야 해요")
	}
	if strings.TrimSpace(name) == "" || host.Len(name) > 60 {
		return nil, crewErr("이름은 1~60자여야 해요")
	}
	if host.Len(key) < 8 || strings.IndexFunc(key, unicode.IsSpace) >= 0 {
		return nil, crewErr("키가 비었거나 올바르지 않아요")
	}
	if err := checkBaseURL(provider, baseURL); err != nil {
		return nil, err
	}
	if provider == "custom" && baseURL == "" {
		return nil, crewErr("직접 추가한 제공자는 주소가 필요해요")
	}
	keys, err := loadKeys()
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if keyID != "" && k["id"] == keyID {
			return nil, crewErr("같은 키가 이미 있어요")
		}
	}
	if keyID == "" {
		keyID = "key_" + randHex(6)
	}
	row := map[string]any{"id": keyID, "provider": provider, "name": strings.TrimSpace(name), "key": key, "base_url": baseURL,
		"monthly_budget_usd": budget, "created_at": nowZ(), "storage": keystore.StorageFile}
	if ref, err := keystore.Put(keyID, key); err == nil { // no store (or it failed): the file keeps the secret
		row["secret_ref"], row["storage"] = ref, keystore.StorageKeyring
	}
	if err := saveKeys(append(keys, row)); err != nil {
		if ref, _ := row["secret_ref"].(string); ref != "" {
			_ = keystore.Delete(ref)
		}
		return nil, err
	}
	return publicKey(row), nil
}

func removeKey(keyID string) error {
	keys, err := loadKeys()
	if err != nil {
		return err
	}
	kept, refs := []map[string]any{}, []string{}
	for _, k := range keys {
		if k["id"] != keyID {
			kept = append(kept, k)
		} else if ref, _ := k["secret_ref"].(string); ref != "" {
			refs = append(refs, ref)
		}
	}
	if len(kept) == len(keys) {
		return crewErr("그 키를 찾지 못했어요")
	}
	if err := saveKeys(kept); err != nil {
		return err
	}
	for _, ref := range refs { // after the file no longer names it, so a failed delete never strands a row
		if err := keystore.Delete(ref); err != nil {
			return crewErr("키는 지웠지만 자격 증명 저장소의 값을 지우지 못했어요")
		}
	}
	return nil
}

func osEnv() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}

func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// customEnv points Claude Code at a 직접 추가 provider's Anthropic endpoint, with that key (it has no login).
func customEnv(env map[string]string, model string, key map[string]any) (map[string]string, error) {
	if key == nil {
		return nil, crewErr(model + "에 맞는 직접 추가 키가 없어요")
	}
	delete(env, "ANTHROPIC_API_KEY")
	for k, v := range map[string]string{"ANTHROPIC_BASE_URL": host.AnthropicURL(sOr(key["base_url"])), "ANTHROPIC_AUTH_TOKEN": str(key["key"]),
		"ANTHROPIC_MODEL": model, "ANTHROPIC_DEFAULT_OPUS_MODEL": model, "ANTHROPIC_DEFAULT_SONNET_MODEL": model,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": model, "CLAUDE_CODE_SUBAGENT_MODEL": model, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"} {
		env[k] = v
	}
	return env, nil
}

// zaiAnthropic is Z.ai's Anthropic-compatible address (docs.z.ai, Claude Code setup).
const zaiAnthropic = "https://api.z.ai/api/anthropic"

// zaiEnv points Claude Code at Z.ai with the stored Z.ai key; a stored address counts only if it is Anthropic-compatible.
func zaiEnv(env map[string]string, model string, keys []map[string]any) (map[string]string, error) {
	if keys == nil {
		var err error
		if keys, err = loadKeys(); err != nil {
			return nil, err
		}
	}
	for _, k := range keys {
		if k["provider"] == "zai" && truthy(k["key"]) {
			base := sOr(k["base_url"])
			if !strings.HasPrefix(base, "https://") || !strings.Contains(base, "/anthropic") { // a pre-0.1.4 http address is not used (#170)
				base = zaiAnthropic
			}
			return customEnv(env, model, map[string]any{"key": k["key"], "base_url": base})
		}
	}
	return nil, crewErr("Z.ai API 키가 없어요. 모델 설정 › Z.ai › API 키에서 키를 넣어 주세요")
}

// childEnv is the child's environment: never re-trigger the capture hook; a stored key only for a CLI in API mode
// (a 직접 추가 provider always uses its key: the one given, or the one its model belongs to). keys nil = load them.
func childEnv(row host.CLI, keys []map[string]any, model string, key map[string]any) (map[string]string, error) {
	env := osEnv()
	env["ARKISM_CAPTURE_CHILD"] = "1"
	if row.Name == host.CustomCLI.Name {
		if key == nil {
			var err error
			if key, err = customKeyFor(model, keys, ""); err != nil {
				return nil, err
			}
		}
		return customEnv(env, model, key)
	}
	if row.Name == "glm" && !glmWrapper() {
		return zaiEnv(env, model, keys)
	}
	if row.Proxy && host.PCProxy != "" {
		env["HTTPS_PROXY"], env["HTTP_PROXY"], env["ALL_PROXY"] = host.PCProxy, host.PCProxy, host.PCProxy
	}
	if !host.APIModeOn(row.Name) || row.KeyEnv == "" {
		return env, nil
	}
	if keys == nil {
		var err error
		if keys, err = loadKeys(); err != nil {
			return nil, err
		}
	}
	applyStoredKey(env, row, keys)
	return env, nil
}

// applyStoredKey puts the row's first stored key, and its address, into a CLI's environment.
func applyStoredKey(env map[string]string, row host.CLI, keys []map[string]any) {
	if row.Provider == "zai" && !strings.HasPrefix(env[row.BaseEnv], "https://") { // nor an inherited http one
		delete(env, row.BaseEnv)
	}
	for _, k := range keys {
		if k["provider"] == row.Provider && truthy(k["key"]) {
			env[row.KeyEnv] = str(k["key"])
			if row.Name == "codex" {
				env["CODEX_API_KEY"] = str(k["key"])
			}
			if b := str(k["base_url"]); b != "" && (row.Provider != "zai" || strings.HasPrefix(b, "https://")) { // a wrapper gets no pre-0.1.4 http Z.ai address (#170)
				env[row.BaseEnv] = b
			}
			return
		}
	}
}

// redact keeps a child that echoes its key from carrying it out through our output.
func redact(text string, env map[string]string) string {
	for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY", "CODEX_API_KEY", "GEMINI_API_KEY", "ZAI_API_KEY", "XAI_API_KEY"} {
		if v := env[k]; len(v) >= 8 {
			text = strings.ReplaceAll(text, v, "***")
		}
	}
	return text
}

// ---------------------------------------------------------------- provider HTTP

// Cloudflare-fronted providers refuse a library's default User-Agent with 403 (error 1010), which reads as
// "키가 맞지 않아요"; provider calls name this runner instead. They honour the environment's proxy.
const providerAgent = "Arkism-crew/1"

// providerClient follows redirects but never from https to plain http: Go keeps Authorization on a same-host hop.
var providerClient = &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
	if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return errors.New("provider redirected to plain http")
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}}

// providerDo sends one provider request; a non-2xx answer is an *host.HTTPError carrying up to 512 bytes of body.
func providerDo(method, url string, body []byte, headers map[string]string, timeout time.Duration, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", providerAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := providerClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		head, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, &host.HTTPError{Code: resp.StatusCode, Body: head}
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

var httpScheme = regexp.MustCompile(`^https?://`)

// httpsOnly refuses anything but http(s) before a request is built (file:// …).
func httpsOnly(url string) (string, error) {
	if !httpScheme.MatchString(url) {
		return "", crewErr("주소는 http(s)://로 시작해야 해요")
	}
	return url, nil
}

var modelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/ -]{0,79}$`)

// testKeyFn is testKey (a fake in tests).
var testKeyFn = testKey

// testKey lists the models a key can see by asking the provider's /models.
// keyTestErr turns a failed /models call into the sentence the key dialog shows.
func keyTestErr(err error) error {
	var he *host.HTTPError
	if !errors.As(err, &he) {
		return crewErr("제공자에 연결하지 못했어요")
	}
	if he.Code == 403 && strings.Contains(strings.ToLower(string(he.Body)), "cloudflare") {
		return crewErr("제공자 방화벽(Cloudflare)이 막았어요 (403)")
	}
	if he.Code == 401 || he.Code == 403 {
		return crewErr("키가 맞지 않아요")
	}
	return crewErr("연결 실패 (" + strconv.Itoa(he.Code) + ")")
}

func testKey(provider, key, baseURL string, timeout time.Duration) ([]string, error) {
	if err := checkBaseURL(provider, baseURL); err != nil { // the key must not reach an address addKey would refuse (#169)
		return nil, err
	}
	base := baseURL
	if base == "" {
		base = host.ModelsURL[provider]
	}
	base = strings.TrimRight(base, "/")
	if !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://") {
		return nil, crewErr("확인할 주소가 없어요")
	}
	headers := map[string]string{"Accept": jsonType}
	switch {
	case provider == "anthropic" && baseURL == "":
		headers["x-api-key"], headers["anthropic-version"] = key, "2023-06-01"
	case provider == "google" && baseURL == "":
		headers["x-goog-api-key"] = key
	default:
		headers["Authorization"] = bearer + key
	}
	raw, err := providerDo(http.MethodGet, base+"/models", nil, headers, timeout, 2<<20)
	if err != nil {
		return nil, keyTestErr(err)
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	v, err := host.Loads(string(raw))
	if err != nil {
		return nil, crewErr("제공자에 연결하지 못했어요")
	}
	body := obj(v)
	rows := body["data"]
	if !truthy(rows) {
		rows = body["models"]
	}
	names := []string{}
	for _, r := range list(rows) {
		m, _ := r.(map[string]any)
		id := m["id"]
		if !truthy(id) {
			id = m["name"]
		}
		name, _ := id.(string)
		name = strings.TrimPrefix(name, "models/")
		if modelID.MatchString(name) && !in(names, name) {
			names = append(names, name)
		}
	}
	if len(names) > 300 {
		names = names[:300]
	}
	return names, nil
}

// probeAPIFn is probeAPI (a fake in tests).
var probeAPIFn = probeAPI

// probeAPI is "anthropic" when <address>/anthropic answers (Claude Code can drive it), else "openai".
func probeAPI(key map[string]any, model string) string {
	if model == "" {
		model = "x"
	}
	url, err := httpsOnly(host.AnthropicURL(sOr(key["base_url"])) + "/v1/messages")
	if err != nil {
		return "openai"
	}
	body := host.MustDumps(host.Obj{{Key: "model", Value: model}, {Key: "max_tokens", Value: 1},
		{Key: "messages", Value: []any{host.Obj{{Key: "role", Value: "user"}, {Key: "content", Value: "hi"}}}}}, host.DumpOpts{})
	_, err = providerDo(http.MethodPost, url, []byte(body), map[string]string{"x-api-key": str(key["key"]), "Authorization": bearer + str(key["key"]),
		"anthropic-version": "2023-06-01", "content-type": jsonType}, 15*time.Second, 1<<20)
	var he *host.HTTPError
	switch {
	case err == nil:
		return "anthropic"
	case errors.As(err, &he):
		if he.Code == 404 || he.Code == 405 {
			return "openai"
		}
		return "anthropic"
	}
	return "openai"
}

func customCachePath() string { return filepath.Join(host.AgentsState, "custom-providers.json") }

const customTTL = 6 * 3600

// customInfo is {models, api, at} for a 직접 추가 key, from its /models and an endpoint probe; kept six hours
// (five minutes when empty).
func customInfo(key map[string]any, refresh bool) (map[string]any, error) {
	cache := map[string]any{}
	if raw, err := os.ReadFile(customCachePath()); err == nil {
		if v, err := host.Loads(string(raw)); err == nil {
			if m, ok := v.(map[string]any); ok {
				cache = m
			}
		}
	}
	id := sOr(key["id"])
	if hit, ok := cache[id].(map[string]any); ok && len(hit) > 0 && !refresh {
		at, _ := num(getOr(hit, "at", 0))
		ttl := 300.0
		if truthy(hit["models"]) {
			ttl = customTTL
		}
		if float64(time.Now().UnixNano())/1e9-at < ttl {
			return hit, nil
		}
	}
	names, err := testKeyFn("custom", str(key["key"]), sOr(key["base_url"]), 10*time.Second)
	if err != nil {
		if !isCrewErr(err) {
			return nil, err
		}
		names = nil
	}
	models := make([]any, len(names))
	first := ""
	for i, n := range names {
		models[i] = n
	}
	if len(names) > 0 {
		first = names[0]
	}
	hit := map[string]any{"models": models, "api": probeAPIFn(key, first), "at": pyFloat(float64(time.Now().UnixNano()) / 1e9)}
	keys, err := loadKeys()
	if err != nil {
		return nil, err
	}
	live := map[string]bool{}
	for _, k := range keys {
		live[sOr(k["id"])] = true
	}
	kept := map[string]any{}
	for k, v := range cache {
		if live[k] {
			kept[k] = v
		}
	}
	kept[str(key["id"])] = hit
	if err := os.MkdirAll(host.AgentsState, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(customCachePath(), []byte(host.MustDumps(kept, host.DumpOpts{})), 0o666); err != nil {
		return nil, err
	}
	return hit, nil
}

var wordSplit = regexp.MustCompile(`[-_ .:/]`)

// customKeyFor is the 직접 추가 key a model runs on: the one whose /models lists it (provider narrows to one key), else
// the first whose name or address shares the model's first word ("deepseek-flash" → api.deepseek.com).
func customKeyFor(model string, keys []map[string]any, provider string) (map[string]any, error) {
	name := strings.TrimSpace(model)
	if keys == nil {
		var err error
		if keys, err = loadKeys(); err != nil {
			return nil, err
		}
	}
	mine := []map[string]any{}
	for _, k := range keys {
		if k["provider"] == "custom" && truthy(k["key"]) && (provider == "" || host.CustomPID(k) == provider) {
			mine = append(mine, k)
		}
	}
	for _, k := range mine {
		info, err := customInfo(k, false)
		if err != nil {
			return nil, err
		}
		for _, m := range list(info["models"]) {
			if strings.EqualFold(pyStr(m), name) {
				return k, nil
			}
		}
	}
	word := wordSplit.Split(strings.ToLower(name), 2)[0]
	if host.Len(word) < 3 {
		return nil, nil
	}
	for _, k := range mine {
		if strings.Contains(strings.ToLower(sOr(k["name"])), word) || strings.Contains(strings.ToLower(netloc(sOr(k["base_url"]))), word) {
			return k, nil
		}
	}
	return nil, nil
}

// netloc is urllib.parse.urlparse(url).netloc.
func netloc(url string) string {
	rest := url
	if i := strings.Index(rest, ":"); i > 0 && isScheme(rest[:i]) {
		rest = rest[i+1:]
	}
	if !strings.HasPrefix(rest, "//") {
		return ""
	}
	rest = rest[2:]
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

func isScheme(s string) bool {
	for i, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && (r >= '0' && r <= '9' || r == '+' || r == '-' || r == '.')) {
			return false
		}
	}
	return true
}
