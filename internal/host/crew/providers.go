package crew

// 모델 설정 (providers). The desktop's screen talks to Arkism; this runner does the host side: it reports which CLIs
// are logged in, their quota and stored keys (last four only), and carries out key requests. Arkism holds a typed key
// in memory only until this runner collects it once (GET /v1/providers/handoff).

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/desktopcontrol"
)

var providerNames = map[string]string{"anthropic": "Anthropic", "openai": "OpenAI", "google": "Google", "zai": "Z.ai", "xai": "xAI", "custom": "직접 추가"}

// defaultModels is what each provider offers to AI nodes. ARKISM_PROVIDER_MODELS='{"openai":["gpt-6.1-sol"]}' replaces a row.
var defaultModels = map[string][]string{
	"anthropic": {"opus 5.5", "fable 5.1", "sonnet 5.5", "haiku 4.5"},
	"openai":    {"gpt-6.1-sol", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna"},
	"google":    {"gemini 3 pro", "gemini 3 flash"},
	"zai":       {"glm-5.3"},
}

// logins is where each CLI keeps its login; present means logged in.
var logins = map[string]string{"claude": ".claude/.credentials.json", "codex": ".codex/auth.json", "gemini": ".gemini/oauth_creds.json",
	"grok": ".grok/auth.json", "agy": ".gemini/antigravity-cli/antigravity-oauth-token"}

func stateFile(name string) string { return filepath.Join(host.AgentsState, name) }

func readJSONObject(path string) (map[string]any, []byte, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, false
	}
	v, err := host.Loads(string(raw))
	m, ok := v.(map[string]any)
	return m, raw, err == nil && ok
}

func nowFloat() float64 { return float64(time.Now().UnixNano()) / 1e9 }

var (
	grokMu   sync.Mutex
	grokLast []string
)

// grokModels is the models the logged-in grok CLI last fetched. grok rewrites that file while it runs; a read
// caught mid-write keeps the last good list.
func grokModels() []string {
	models := []string{}
	if cache, raw, ok := readJSONObject(filepath.Join(host.Home, ".grok/models_cache.json")); ok {
		switch m := cache["models"].(type) {
		case map[string]any:
			models = memberKeys(raw, "models")
		case []any:
			for _, x := range m {
				models = append(models, pyStr(x))
			}
		}
	}
	grokMu.Lock()
	defer grokMu.Unlock()
	if len(models) > 0 {
		grokLast = models
		return models
	}
	return append([]string{}, grokLast...)
}

// codexModels is the models the logged-in codex CLI lists in its picker, in its order.
func codexModels() []string {
	cache, _, ok := readJSONObject(filepath.Join(host.Home, ".codex/models_cache.json"))
	if !ok {
		return nil
	}
	type row struct {
		slug     string
		priority float64
	}
	rows := []row{}
	for _, r := range list(cache["models"]) {
		m, ok := r.(map[string]any)
		if !ok || m["visibility"] != "list" || !truthy(m["slug"]) {
			continue
		}
		p, ok := num(getOr(m, "priority", 99))
		if !ok {
			p = 99
		}
		rows = append(rows, row{pyStr(m["slug"]), p})
	}
	sort.SliceStable(rows, func(a, b int) bool { return rows[a].priority < rows[b].priority })
	out := []string{}
	for _, r := range rows {
		out = append(out, r.slug)
	}
	return out
}

// freshModels is set while the 새로고침 button rereads every provider's model list past its cache.
var freshModels atomic.Bool

// refreshModels asks every provider for its current models now; the lists land in the caches the next report reads.
func refreshModels() error {
	freshModels.Store(true)
	defer freshModels.Store(false)
	_, err := inventory("", nil, true)
	return err
}

func cachedModels(path string) ([]string, bool) {
	cache, _, ok := readJSONObject(path)
	if !ok {
		return nil, false
	}
	at, _ := num(getOr(cache, "at", 0))
	models := stringList(cache["models"])
	_, has := cache["models"]
	return models, has && nowFloat()-at < 86400 && !freshModels.Load()
}

var claudeID = regexp.MustCompile(`^claude-([a-z]+)-(\d+)(?:-(\d{1,2}))?(?:-\d{8})?$`)

// anthropicModels is what Anthropic's /models offers the signed-in Claude Code subscription, asked once a day:
// the newest model of each family, named as routing writes them (claude-opus-5-5 → "opus 5.5").
func anthropicModels() []string {
	path := stateFile("anthropic-models.json")
	cached, fresh := cachedModels(path)
	if fresh {
		return cached
	}
	creds, _, ok := readJSONObject(filepath.Join(host.Home, logins["claude"]))
	token := pyStr(getOr(obj(creds["claudeAiOauth"]), "accessToken", ""))
	if !ok || token == "" {
		return cached
	}
	raw, err := providerDo(http.MethodGet, host.ModelsURL["anthropic"]+"/models?limit=100", nil, map[string]string{"Authorization": bearer + token,
		"anthropic-version": "2023-06-01", "anthropic-beta": "oauth-2025-04-20", "Accept": jsonType}, 10*time.Second, 8<<20)
	if err != nil {
		return cached
	}
	v, err := host.Loads(string(raw))
	if err != nil {
		return cached
	}
	models, seen := []string{}, map[string]bool{}
	for _, m := range list(obj(v)["data"]) { // newest first
		g := claudeID.FindStringSubmatch(pyStr(getOr(obj(m), "id", "")))
		if g == nil || seen[g[1]] {
			continue
		}
		seen[g[1]] = true
		name := g[1] + " " + g[2]
		if g[3] != "" {
			name += "." + g[3]
		}
		models = append(models, name)
	}
	if len(models) > 0 {
		saveModels(path, models)
		return models
	}
	return cached
}

func saveModels(path string, models []string) {
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte(host.MustDumps(host.Obj{{Key: "at", Value: pyFloat(nowFloat())}, {Key: "models", Value: toAny(models)}}, host.DumpOpts{})), 0o666)
}

var (
	glmName  = regexp.MustCompile(`^glm-([5-9]|\d\d)`)
	digitsRe = regexp.MustCompile(`\d+`)
)

// zaiModels is Z.ai's live model list (/models with the glm wrapper's key), asked once a day, newest first, GLM 5 and up.
func zaiModels() []string {
	path := stateFile("zai-models.json")
	cached, fresh := cachedModels(path)
	if fresh {
		return cached
	}
	ids, err := func() ([]string, error) {
		f, err := os.Open(filepath.Join(host.Home, ".config/claude-3p/zai.env")) // the key the glm wrapper runs on (KEY=…)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		key, found := "", false
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "KEY=") {
				key, found = strings.Trim(strings.TrimSpace(line[4:]), `'"`), true
				break
			}
		}
		if !found {
			return nil, errors.New("no key")
		}
		raw, err := providerDo(http.MethodGet, host.ModelsURL["zai"]+"/models", nil, map[string]string{"Authorization": bearer + key, "Accept": jsonType}, 10*time.Second, 8<<20)
		if err != nil {
			return nil, err
		}
		v, err := host.Loads(string(raw))
		if err != nil {
			return nil, err
		}
		out := []string{}
		for _, m := range list(obj(v)["data"]) {
			out = append(out, pyStr(getOr(obj(m), "id", "")))
		}
		return out, nil
	}()
	if err != nil {
		return cached
	}
	// glm-4.x stays reachable by 모델 추가; the list shows the current generation.
	ver := func(m string) []int { // glm-5.3-flash → (5, 3)
		parts := strings.Split(m, "-")
		out := []int{}
		if len(parts) > 1 {
			for _, d := range digitsRe.FindAllString(parts[1], -1) {
				n, _ := strconv.Atoi(d)
				out = append(out, n)
			}
		}
		return out
	}
	models := []string{}
	for _, m := range ids {
		if glmName.MatchString(m) {
			models = append(models, m)
		}
	}
	sort.SliceStable(models, func(a, b int) bool {
		va, vb := ver(models[a]), ver(models[b])
		for k := 0; k < len(va) && k < len(vb); k++ {
			if va[k] != vb[k] {
				return va[k] > vb[k]
			}
		}
		if len(va) != len(vb) {
			return len(va) > len(vb)
		}
		return len([]rune(models[a])) < len([]rune(models[b]))
	})
	if len(models) > 0 {
		saveModels(path, models)
		return models
	}
	return cached
}

// agyModels is antigravity's model list (agy models, through PCProxy when set), asked once a day, each with the antigravity- prefix.
func agyModels() []string {
	path := stateFile("agy-models.json")
	cached, fresh := cachedModels(path)
	if fresh {
		return cached
	}
	exe := host.Bin("ARKISM_AGY_BIN", "agy")
	if !exists(exe) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "models")
	env := osEnv()
	if host.PCProxy != "" {
		env["HTTPS_PROXY"], env["HTTP_PROXY"], env["ALL_PROXY"] = host.PCProxy, host.PCProxy, host.PCProxy
	}
	cmd.Env = envList(env)
	raw, err := cmd.Output()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) || ctx.Err() != nil {
		return cached
	}
	models := []string{}
	for _, line := range pySplitlines(newlines(string(raw))) {
		if strings.Contains(line, "\t") && strings.TrimSpace(line) != "" {
			first, _, _ := strings.Cut(line, "\t")
			models = append(models, "antigravity-"+strings.TrimSpace(first))
		}
	}
	if len(models) > 0 {
		saveModels(path, models)
		return models
	}
	return cached
}

func modelsFor(provider string) []string {
	extra := map[string]any{}
	if v, err := host.Loads(host.Env("ARKISM_PROVIDER_MODELS", "{}")); err == nil {
		extra = obj(v)
	}
	// A list set by hand wins without asking the CLI (agy may go out through a proxy and be slow).
	var found []string
	if truthy(extra[provider]) {
		for _, m := range list(extra[provider]) {
			if s, ok := m.(string); ok {
				found = append(found, s)
			}
		}
	} else {
		switch provider {
		case "anthropic":
			found = anthropicModels()
		case "openai":
			found = codexModels()
		case "xai":
			found = grokModels()
		case "zai":
			found = zaiModels()
		case "google":
			found = agyModels()
		}
	}
	if len(found) == 0 {
		found = defaultModels[provider]
	}
	out := []string{}
	for _, m := range found {
		if modelID.MatchString(m) {
			out = append(out, m)
		}
	}
	return out
}

// ---------------------------------------------------------------- Codex quota

// pyISO is datetime.fromtimestamp(t, timezone.utc).isoformat() with "Z".
func pyISO(t float64) string {
	whole, frac := math.Modf(t)
	us := math.RoundToEven(frac * 1e6)
	if us >= 1e6 {
		whole, us = whole+1, us-1e6
	} else if us < 0 {
		whole, us = whole-1, us+1e6
	}
	at := time.Unix(int64(whole), int64(us)*1000).UTC()
	if us == 0 {
		return at.Format("2006-01-02T15:04:05Z")
	}
	return at.Format("2006-01-02T15:04:05.000000Z")
}

// pyNum is isinstance(v, (int, float)) (bools count) as a float.
func pyNum(v any) (float64, bool) {
	if b, ok := v.(bool); ok {
		if b {
			return 1, true
		}
		return 0, true
	}
	return num(v)
}

func quotaWindow(limit map[string]any, newest string) host.Obj {
	used, ok := pyNum(limit["used_percent"])
	if !ok {
		return nil
	}
	minutes := int64(300)
	if truthy(limit["window_minutes"]) {
		if n, ok := pyIntArg(limit["window_minutes"]); ok {
			minutes = n
		}
	}
	window := "week"
	if minutes < 7*24*60 {
		window = strconv.FormatInt(int64(math.Floor(float64(minutes)/60)), 10) + "h"
	}
	out := host.Obj{{Key: "window", Value: window}, {Key: "left_pct", Value: int64(max(0, min(100, math.RoundToEven(100-used))))}}
	if resets, ok := pyNum(limit["resets_at"]); ok {
		out = append(out, host.KV{Key: "resets_at", Value: pyISO(resets)})
	} else if in, ok := pyNum(limit["resets_in_seconds"]); ok {
		if st, err := os.Stat(newest); err == nil {
			out = append(out, host.KV{Key: "resets_at", Value: pyISO(float64(st.ModTime().UnixNano())/1e9 + in)})
		}
	}
	return out
}

// rateLimits is the first rate_limits object with a primary window inside a parsed JSONL line.
func rateLimits(v any) map[string]any {
	stack := []any{v}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch x := cur.(type) {
		case map[string]any:
			if limits, ok := x["rate_limits"].(map[string]any); ok {
				if _, ok := limits["primary"].(map[string]any); ok {
					return limits
				}
			}
			// shortcut: nested objects are walked in key order, not file order; differs only with two rate_limits in one line.
			for _, k := range sortedKeys(x) {
				stack = append(stack, x[k])
			}
		case []any:
			stack = append(stack, x...)
		}
	}
	return nil
}

// codexQuotas is the 5-hour and weekly windows from the newest Codex session log, primary first; [] when unknown.
func codexQuotas(home string) []any {
	newest, newestAt := "", time.Time{}
	_ = filepath.WalkDir(host.CodexSessionsDir(home), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if ok, _ := filepath.Match("rollout-*.jsonl", d.Name()); ok {
			if info, err := d.Info(); err == nil && (newest == "" || info.ModTime().After(newestAt)) {
				newest, newestAt = p, info.ModTime()
			}
		}
		return nil
	})
	if newest == "" {
		return []any{}
	}
	raw, err := os.ReadFile(newest)
	if err != nil {
		return []any{}
	}
	lines := pySplitlines(strings.ToValidUTF8(string(raw), "�"))
	if len(lines) > 400 {
		lines = lines[len(lines)-400:]
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if !strings.Contains(lines[i], `"rate_limits"`) {
			continue
		}
		v, err := host.Loads(lines[i])
		if err != nil {
			continue
		}
		if limits := rateLimits(v); limits != nil {
			out := []any{}
			for _, k := range []string{"primary", "secondary"} {
				if w, ok := limits[k].(map[string]any); ok {
					if q := quotaWindow(w, newest); q != nil {
						out = append(out, q)
					}
				}
			}
			return out
		}
	}
	return []any{}
}

// ---------------------------------------------------------------- inventory

func keyRow(pub host.Obj) host.Obj {
	get := func(k string) any { v, _ := pub.Get(k); return v }
	row := host.Obj{{Key: "id", Value: get("id")}, {Key: "name", Value: get("name")}, {Key: "last4", Value: get("last4")}, {Key: "base_url", Value: get("base_url")}}
	// No "storage": the server's ProviderKey rejects unknown fields, so the whole report would fail
	if b := get("monthly_budget_usd"); b != nil {
		row = append(row, host.KV{Key: "monthly_budget_usd", Value: b})
	}
	created := get("created_at")
	if !truthy(created) {
		created = "1970-01-01T00:00:00Z"
	}
	return append(row, host.KV{Key: "created_at", Value: created})
}

func onPath(name string) bool { _, err := exec.LookPath(name); return err == nil }

// inventory is what 모델 설정 shows: per provider, logged-in CLIs, stored keys (last four) and models.
func inventory(home string, keys []map[string]any, refreshUsage bool) ([]any, error) {
	if home == "" {
		home = host.Home
	}
	if keys == nil {
		var err error
		if keys, err = loadKeys(); err != nil {
			return nil, err
		}
	}
	out := []any{}
	for _, pid := range []string{"anthropic", "openai", "google", "zai", "xai"} { // xai runs through grok
		accounts, err := providerAccounts(pid, home, refreshUsage)
		if err != nil {
			return nil, err
		}
		mine := []any{}
		for _, k := range keys {
			if k["provider"] == pid {
				mine = append(mine, keyRow(publicKey(k)))
			}
		}
		if len(accounts) == 0 && len(mine) == 0 && pid == "xai" {
			continue
		}
		kind := "api"
		if len(accounts) > 0 {
			kind = "subscription"
		}
		out = append(out, host.Obj{{Key: "id", Value: pid}, {Key: "name", Value: providerNames[pid]}, {Key: "kind", Value: kind},
			{Key: "custom", Value: false}, {Key: "accounts", Value: accounts}, {Key: "keys", Value: mine}, {Key: "models", Value: toAny(modelsFor(pid))}})
	}
	// 직접 추가: one provider per key, listing what its /models offers; the owner turns models on.
	for _, k := range keys {
		if k["provider"] != "custom" {
			continue
		}
		provider, err := customProvider(k)
		if err != nil {
			return nil, err
		}
		out = append(out, provider)
	}
	return out, nil
}

// providerAccounts lists the installed CLIs of provider pid with their sign-ins.
func providerAccounts(pid, home string, refreshUsage bool) ([]any, error) {
	accounts := []any{}
	for _, row := range host.CLIS {
		if row.Provider != pid {
			continue
		}
		exe := host.Bin(row.BinEnv, row.BinName) // the CLI itself: GLM without its wrapper has no login, only keys
		if !exists(exe) && !onPath(row.BinName) {
			continue
		}
		isDefault := !host.APIModeOn(row.Name)
		if row.Name == "claude" || row.Name == "codex" {
			for _, profile := range host.AccountsProfiles(row.Name, home, host.AgentsState) {
				detail, err := host.AccountsAccount(profile, exe, host.AgentsState, refreshUsage)
				if err != nil {
					return nil, err
				}
				detail = withKey(detail, "cli_info", cliInfoFn(row.Name, exe, host.AgentsState, refreshUsage))
				accounts = append(accounts, withKey(detail, "default", isDefault))
			}
			continue
		}
		accounts = append(accounts, cliLogin(row, home, isDefault))
	}
	return accounts, nil
}

// cliLogin is the sign-in row of a CLI without account profiles.
func cliLogin(row host.CLI, home string, isDefault bool) host.Obj {
	status := "connected"
	if login, ok := logins[row.Name]; ok && !exists(filepath.Join(home, login)) {
		status = "missing"
		// Antigravity on Windows keeps its sign-in outside ~/.gemini (no token file there);
		// a CLI that has already held conversations here counts as signed in.
		if row.Name == "agy" && runtime.GOOS == "windows" && exists(filepath.Join(home, ".gemini", "antigravity-cli", "conversations")) {
			status = "connected"
		}
	}
	account := host.Obj{{Key: "id", Value: row.Name}, {Key: "label", Value: row.Name + " CLI 로그인"},
		{Key: "cli", Value: row.Name}, {Key: "status", Value: status}, {Key: "default", Value: isDefault}}
	if row.Name == "gemini" && status == "connected" {
		q := readGeminiQuota(home, host.AgentsState, time.Now())
		quotas := make([]any, len(q.Quotas))
		for i, x := range q.Quotas {
			quotas[i] = x
		}
		account = append(account, host.KV{Key: "usage_status", Value: q.Status}, host.KV{Key: "usage_checked_at", Value: q.CheckedAt},
			host.KV{Key: "quotas", Value: quotas})
		if q.Status == "expired" {
			account.Set("status", "expired")
		}
	}
	return account
}

func customProvider(k map[string]any) (host.Obj, error) {
	info, err := customInfo(k, freshModels.Load())
	if err != nil {
		return nil, err
	}
	models := []any{}
	for _, m := range list(info["models"]) {
		if s, ok := m.(string); ok && modelID.MatchString(s) && len(models) < 300 {
			models = append(models, s)
		}
	}
	name := sOr(k["name"])
	if name == "" {
		name = "직접 추가"
	}
	return host.Obj{{Key: "id", Value: host.CustomPID(k)}, {Key: "name", Value: name}, {Key: "kind", Value: "api"},
		{Key: "custom", Value: true}, {Key: "accounts", Value: []any{}}, {Key: "keys", Value: []any{keyRow(publicKey(k))}}, {Key: "models", Value: models}}, nil
}

// ---------------------------------------------------------------- 연결 시험 and handoff requests

// testModel sends one short prompt to model through its CLI (연결 시험) and returns a note like '응답 1.8초'.
// The run goes through the CLI (and key) of the model's own provider, so a model listed under another provider is
// refused rather than tested on the wrong connection.
func testModel(model string, runner runFn, provider string) (string, error) {
	if runner == nil {
		runner = func(s map[string]any) (map[string]any, error) { return runNodeFn(s) }
	}
	row, ok := cliFor(model)
	owner := ""
	if ok {
		owner = row.Provider
	}
	custom := ok && row.Name == host.CustomCLI.Name
	if custom {
		narrow := ""
		if strings.HasPrefix(provider, "custom-") {
			narrow = provider
		}
		key, err := customKeyFor(model, nil, narrow)
		if err != nil {
			return "", err
		}
		owner = ""
		if key != nil {
			owner = host.CustomPID(key)
		}
	}
	if provider != "" && owner != provider {
		name := func(p string) string {
			if n, ok := providerNames[p]; ok {
				return n
			}
			return p
		}
		runsOn := "알 수 없는"
		if ok {
			runsOn = name(row.Provider)
		}
		return "", crewErr(fmt.Sprintf("%s은(는) %s 연결로 시험할 수 없어요. 실행은 %s CLI로 가요", model, name(provider), runsOn))
	}
	work, err := os.MkdirTemp("", "arkism-test-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	named := ""
	if custom {
		named = provider
	}
	out, err := runner(map[string]any{"model": model, "effort": "low", "instructions": "연결 시험이다. OK 한 단어만 답한다.", "task": "OK라고만 답해",
		"cwd": work, "timeout_s": 90, "once": true, "provider": named})
	if err != nil {
		return "", err
	}
	if !truthy(out["ok"]) {
		msg := "답이 없어요"
		if truthy(out["error"]) {
			msg = pyStr(out["error"])
		}
		return "", crewErr(clipRunes(msg, 200))
	}
	ms, _ := num(getOr(out, "duration_ms", 0))
	if !truthy(out["duration_ms"]) {
		ms = 0
	}
	return fmt.Sprintf("응답 %.1f초", ms/1000), nil
}

type (
	keysTester  func(provider, key, baseURL string) ([]string, error)
	modelTester func(model, provider string) (string, error)
)

func strField(q map[string]any, k string) string { s, _ := q[k].(string); return s }

// carryOut turns one handoff request into a result row for Arkism. It never puts the key in the result.
func carryOut(item map[string]any, testKeys keysTester, testModels modelTester) (map[string]any, error) {
	if testKeys == nil {
		testKeys = func(p, k, b string) ([]string, error) { return testKeyFn(p, k, b, 10*time.Second) }
	}
	if testModels == nil {
		testModels = func(m, p string) (string, error) { return testModel(m, nil, p) }
	}
	q := obj(item["request"])
	key := sOr(item["key"])
	res := map[string]any{"id": getOr(q, "id", ""), "status": "done"}
	var err error
	switch q["kind"] {
	case "key_add":
		_, err = addKey(strField(q, "provider"), strField(q, "name"), key, strField(q, "base_url"), q["monthly_budget_usd"], strField(q, "key_id"))
	case "key_test":
		var models []string
		if models, err = testKeys(strField(q, "provider"), key, strField(q, "base_url")); err == nil {
			res["models"] = toAny(models)
		}
	case "key_remove":
		err = removeKey(strField(q, "key_id"))
	case "model_test":
		var note string
		if note, err = testModels(strField(q, "model"), strField(q, "provider")); err == nil {
			res["note"] = note
		}
	case "models_refresh":
		if refreshModels() != nil {
			err = crewErr("모델 목록을 새로 받지 못했어요.")
		}
	case "cli_update":
		row, ok := host.CLIByName(strField(q, "cli"))
		if !ok || row.Name == host.CustomCLI.Name {
			err = crewErr("지원하지 않는 CLI예요.")
			break
		}
		return executeUpdate(q, binOf(row), host.AgentsState)
	default:
		err = crewErr("모르는 요청이에요")
	}
	if err != nil {
		if !isCrewErr(err) {
			return nil, err
		}
		msg := err.Error()
		if key != "" {
			msg = strings.ReplaceAll(msg, key, "***")
		}
		res["status"], res["error"] = "failed", msg
	}
	return res, nil
}

var resultRowOrder = []string{"id", "status", "models", "note", "error"}

// The pool that runs model tests and AI help beside the watch loop (two at a time).
var (
	poolSlots = make(chan struct{}, 2)
	poolJobs  sync.WaitGroup
	poolMu    sync.Mutex
	testsBusy = map[string]bool{}
	// testsDone keeps results whose report failed: sent again instead of testing the model twice.
	testsDone = map[string]map[string]any{}
)

// submit runs job in the pool; job may call release to hand its slot on early (once), when it only waits on someone else.
func submit(job func(release func())) bool {
	var once sync.Once
	release := func() { once.Do(func() { <-poolSlots }) }
	return desktopcontrol.Submit(desktopcontrol.Default, desktopcontrol.Pool, func(work func()) {
		poolJobs.Add(1)
		go func() {
			defer poolJobs.Done()
			poolSlots <- struct{}{}
			defer release()
			work()
		}()
	}, func() { job(release) })
}

// errName is the Python exception name a failure is reported under (never its message).
func errName(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		if errors.Is(err, fs.ErrNotExist) {
			return "FileNotFoundError"
		}
		return "OSError"
	}
	if isValueErr(err) {
		return "ValueError"
	}
	return errorKind(err)
}

func isValueErr(err error) bool {
	return err != nil && (errors.Is(err, errValue) || errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "invalid character"))
}

func modelTestJob(item map[string]any) {
	qid := pyStr(obj(item["request"])["id"])
	defer func() {
		poolMu.Lock()
		delete(testsBusy, qid)
		poolMu.Unlock()
	}()
	poolMu.Lock()
	res := testsDone[qid]
	poolMu.Unlock()
	if res == nil {
		var err error
		if res, err = carryOut(item, nil, nil); err != nil {
			if !errors.Is(err, errUpdateBusy) { // another host worker owns this request; it will report the result
				fmt.Fprintf(os.Stderr, "모델 시험 보고 실패: %s\n", errName(err))
			}
			return
		}
		poolMu.Lock()
		testsDone[qid] = res
		poolMu.Unlock()
	}
	if _, err := runnerCall("/v1/providers", host.Obj{{Key: "scope", Value: scope()}, {Key: "results", Value: []any{ordered(res, resultRowOrder...)}}}, 15*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "모델 시험 보고 실패: %s\n", errName(err))
		return
	}
	poolMu.Lock()
	delete(testsDone, qid)
	poolMu.Unlock()
}

func queueModelTest(item map[string]any) {
	qid := pyStr(obj(item["request"])["id"])
	poolMu.Lock()
	busy := testsBusy[qid]
	testsBusy[qid] = true
	poolMu.Unlock()
	if busy || submit(func(func()) { modelTestJob(item) }) {
		return
	}
	poolMu.Lock()
	delete(testsBusy, qid)
	poolMu.Unlock()
}

// providersRound collects waiting requests, carries them out and reports; with report, it also sends the inventory.
// Model tests (up to a minute and a half) run beside the loop so keys and AI help are not held up.
func providersRound(report bool) (int, error) {
	page, err := runnerCall("/v1/providers/handoff?"+scopeQuery(), nil, 15*time.Second)
	if err != nil {
		return 0, err
	}
	items := list(page["items"])
	results := []any{}
	done := false
	for _, it := range items {
		item := obj(it)
		kind := obj(item["request"])["kind"]
		if kind == "model_test" || kind == "cli_update" {
			queueModelTest(item)
		}
	}
	for _, it := range items {
		item := obj(it)
		kind := obj(item["request"])["kind"]
		if kind == "model_test" || kind == "cli_update" {
			continue
		}
		res, err := carryOut(item, nil, nil)
		if err != nil {
			return 0, err
		}
		done = done || (res["status"] == "done" && truthy(res["id"]))
		results = append(results, ordered(res, resultRowOrder...))
	}
	if len(results) == 0 && !report {
		return 0, nil
	}
	body := host.Obj{{Key: "scope", Value: scope()}, {Key: "results", Value: results}}
	if report || done {
		inv, err := inventory("", nil, true)
		if err != nil {
			return 0, err
		}
		body = append(body, host.KV{Key: "providers", Value: inv})
	}
	if _, err := runnerCall("/v1/providers", body, 15*time.Second); err != nil {
		return 0, err
	}
	return len(results), nil
}

// providersWatch runs forever: requests every interval, the inventory every reportEvery, old installs every minute.
func providersWatch(interval, reportEvery time.Duration, stderr io.Writer) {
	providersWatchUntil(interval, reportEvery, stderr, nil)
}

func providersWatchUntil(interval, reportEvery time.Duration, stderr io.Writer, stop <-chan struct{}) {
	var last, installed time.Time
	watchLoop(desktopcontrol.Default, stop, interval, func() {
		due := time.Since(last) >= reportEvery
		if _, err := providersRound(due); err != nil {
			fmt.Fprintf(stderr, "모델 설정 동기화 실패: %s\n", errName(err))
		} else if due {
			last = time.Now()
		}
		if _, err := assistsRound(); err != nil {
			fmt.Fprintf(stderr, "AI 도움 확인 실패: %s\n", errName(err))
		}
		if time.Since(installed) >= time.Minute {
			installed = time.Now()
			if _, err := syncInstalls(""); err != nil {
				fmt.Fprintf(stderr, "옛 전문가 파일 정리 실패: %s\n", errName(err))
			}
		}
	})
}
