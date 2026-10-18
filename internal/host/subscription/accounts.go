package subscription

// Host-only CLI profiles and a bounded, token-free usage cache.
// Login files stay in their original directories. Selecting a profile changes only the
// child CLI's configuration environment, never the user's active login.

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/desktopcontrol"
)

const manifestLimit = 131072

var (
	idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

	// Test seams.
	identity   = Identity
	fetchUsage = FetchUsage
	submit     = func(fn func()) {
		pending.Add(1)
		go func() {
			defer pending.Done()
			slots <- struct{}{} // two probes at a time, like the Python pool
			defer func() { <-slots }()
			fn()
		}()
	}

	slots   = make(chan struct{}, 2)
	pending sync.WaitGroup
	busyMu  sync.Mutex
	busy    = map[string]bool{}
)

func init() {
	host.AccountsProfiles = Profiles
	host.AccountsChoose = Choose
	host.AccountsChildEnvironment = ChildEnvironment
	host.AccountsRecordLimit = RecordLimit
	host.AccountsRegister = Register
	host.AccountsUnregister = Unregister
	host.AccountsAccount = Account
	host.AccountsWait = WaitRefresh
	host.AccountsForce = func() { forceProbe.Store(true) }
}

// WaitRefresh waits for background usage probes; call it before the process exits.
func WaitRefresh() { pending.Wait() }

// forceProbe is set by an explicit refresh (the app's button): probe even within a minute of the last one.
var forceProbe atomic.Bool

func unixNow() float64 { return float64(now().UnixNano()) / 1e9 }

func expandUser(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return host.Home + p[1:]
	}
	return p
}

func dirOf(profile map[string]any) string { s, _ := profile["config_dir"].(string); return s }
func cliOf(profile map[string]any) string { s, _ := profile["cli"].(string); return s }

func validLabel(label string) bool {
	if strings.TrimSpace(label) == "" || utf8.RuneCountInString(label) > 120 {
		return false
	}
	return !strings.ContainsFunc(label, func(r rune) bool { return r < 32 })
}

// Profiles lists the default login plus up to 19 registered folders for cli.
func Profiles(cli, home, state string) []map[string]any {
	if cli != "claude" && cli != "codex" {
		return nil
	}
	env := "CLAUDE_CONFIG_DIR"
	if cli == "codex" {
		env = "CODEX_HOME"
	}
	root := os.Getenv(env)
	if root == "" {
		root = filepath.Join(home, "."+cli)
	}
	root = resolve(expandUser(root))
	rows := []map[string]any{{"id": cli, "cli": cli, "label": cli + " CLI 로그인", "config_dir": root}}
	seen, ids := map[string]bool{root: true}, map[string]bool{cli: true}
	extra, _ := host.ReadJSONFile(filepath.Join(state, "subscription-accounts.json"), manifestLimit)["accounts"].([]any)
	for _, raw := range extra[:min(len(extra), 19)] {
		row, _ := raw.(map[string]any)
		if row == nil || row["cli"] != cli {
			continue
		}
		ident, _ := row["id"].(string)
		label, okLabel := row["label"].(string)
		folder, okFolder := row["config_dir"].(string)
		if !idRe.MatchString(ident) || ids[ident] || !okLabel || !validLabel(label) || !okFolder || !filepath.IsAbs(folder) {
			continue
		}
		config := resolve(folder)
		if seen[config] {
			continue
		}
		rows = append(rows, map[string]any{"id": ident, "cli": cli, "label": label, "config_dir": config})
		ids[ident], seen[config] = true, true
	}
	return rows
}

// Register adds an already signed-in folder, keeping its credentials untouched.
func Register(cli, label, configDir, home, state string) (map[string]any, error) {
	if (cli != "claude" && cli != "codex") || !validLabel(label) {
		return nil, host.Crewf("계정 종류와 이름을 확인해 주세요.")
	}
	folder := resolve(expandUser(configDir))
	if identity(cli, folder)["status"] == "missing" {
		return nil, host.Crewf("해당 폴더에서 CLI 로그인 정보를 찾지 못했어요.")
	}
	sum := sha256.Sum256([]byte(folder))
	ident := cli + "-" + hex.EncodeToString(sum[:])[:16]
	path := filepath.Join(state, "subscription-accounts.json")
	unlock, err := lockManifest(path)
	if err != nil {
		return nil, err
	}
	defer unlock()
	for _, existing := range Profiles(cli, home, state) {
		if existing["config_dir"] == folder {
			return map[string]any{"id": existing["id"], "cli": cli, "label": existing["label"]}, nil
		}
	}
	manifest := map[string]any{"accounts": []any{}}
	if _, err := os.Stat(path); err == nil {
		manifest = host.ReadJSONFile(path, manifestLimit)
	}
	rows, ok := manifest["accounts"].([]any)
	if !ok {
		return nil, host.Crewf("계정 목록 파일을 확인해 주세요.")
	}
	if len(rows) >= 19 {
		return nil, host.Crewf("추가 계정은 19개까지 등록할 수 있어요.")
	}
	public := map[string]any{"id": ident, "cli": cli, "label": strings.TrimSpace(label)}
	row := maps.Clone(public)
	row["config_dir"] = folder
	if err := host.WriteJSONFile(path, map[string]any{"accounts": append(rows, row)}); err != nil {
		return nil, err
	}
	return public, nil
}

// Unregister removes only Arkism's profile reference, never the login itself.
func Unregister(ident, state string) error {
	path := filepath.Join(state, "subscription-accounts.json")
	unlock, err := lockManifest(path)
	if err != nil {
		return err
	}
	defer unlock()
	rows, ok := host.ReadJSONFile(path, manifestLimit)["accounts"].([]any)
	if !ok {
		return host.Crewf("계정 목록 파일을 확인해 주세요.")
	}
	kept := slices.DeleteFunc(slices.Clone(rows), func(raw any) bool {
		row, isObj := raw.(map[string]any)
		return isObj && row["id"] == ident
	})
	if len(kept) == len(rows) {
		return host.Crewf("등록한 추가 계정을 찾지 못했어요.")
	}
	return host.WriteJSONFile(path, map[string]any{"accounts": kept})
}

func lockManifest(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return lockFile(strings.TrimSuffix(path, ".json")+".lock", 0o666, true)
}

// stamp identifies the credential file version; empty when it cannot be read.
func stamp(profile map[string]any) []any {
	name := ".credentials.json"
	if cliOf(profile) == "codex" {
		name = "auth.json"
	}
	fi, err := os.Stat(filepath.Join(dirOf(profile), name))
	if err != nil {
		return []any{}
	}
	return statStamp(fi)
}

func same(a, b any) bool {
	return host.MustDumps(a, host.DumpOpts{}) == host.MustDumps(b, host.DumpOpts{})
}

func cachePath(profile map[string]any, state string) string {
	sum := sha256.Sum256([]byte(cliOf(profile) + "\x00" + dirOf(profile)))
	return filepath.Join(state, "subscription-usage", hex.EncodeToString(sum[:])+".json")
}

func cooldownPath(profile map[string]any, state string) string {
	return filepath.Join(state, "subscription-cooldowns", filepath.Base(cachePath(profile, state)))
}

var naiveLayouts = []string{"2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999", "2006-01-02T15:04", "2006-01-02"}

// at is an ISO time as Unix seconds, or 0 when it is not one.
// shortcut: covers the ISO shapes this code writes, not every fromisoformat variant; extend if a CLI writes others.
func at(v any) float64 {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	s = strings.ReplaceAll(s, "Z", "+00:00")
	if t, err := time.Parse("2006-01-02T15:04:05.999999999Z07:00", s); err == nil {
		return float64(t.UnixNano()) / 1e9
	}
	if t, err := time.Parse("2006-01-02 15:04:05.999999999Z07:00", s); err == nil {
		return float64(t.UnixNano()) / 1e9
	}
	for _, layout := range naiveLayouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return float64(t.UnixNano()) / 1e9
		}
	}
	return 0
}

// isoUTC mirrors Python's datetime.isoformat() for an aware UTC time.
func isoUTC(t time.Time) string {
	t = t.UTC().Truncate(time.Microsecond)
	if t.Nanosecond() == 0 {
		return t.Format("2006-01-02T15:04:05+00:00")
	}
	return t.Format("2006-01-02T15:04:05.000000+00:00")
}

// accountHash is a hash of the signed-in account id, or nil when the login has none.
func accountHash(profile map[string]any) any {
	ident, _ := identity(cliOf(profile), dirOf(profile))["account_id"].(string)
	if ident == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(cliOf(profile) + "\x00" + ident))
	return hex.EncodeToString(sum[:])
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	n, ok := number(v, false)
	return !ok || n != 0
}

// isZero is Python's `value == 0` (False counts too).
func isZero(v any) bool {
	if b, ok := v.(bool); ok {
		return !b
	}
	n, ok := number(v, false)
	return ok && n == 0
}

func finite(v any) float64 {
	n, ok := number(v, false)
	if !ok {
		return 0
	}
	return n
}

func quotas(usage map[string]any) []any { q, _ := usage["quotas"].([]any); return q }

// Snapshot is the cached usage that still applies to the current login.
func Snapshot(profile map[string]any, state string) map[string]any {
	cached := host.ReadJSONFile(cachePath(profile, state), manifestLimit)
	usage, ok := cached["usage"].(map[string]any)
	if !ok {
		return map[string]any{}
	}
	if same(cached["credential_stamp"], stamp(profile)) {
		return usage
	}
	// The credential file changed: usually a token refresh by the same user.
	// Keep only confirmed zeros with a future reset so a refresh cannot unblock
	// an exhausted account; positive/unknown numbers must be re-probed.
	// A confirmed different account id discards everything. Claude logins
	// without accountId fall back to this config folder (the cache key), so a
	// different user signing into the same folder may stay blocked until reset.
	old, isStr := cached["account"].(string)
	fresh := accountHash(profile)
	if usage["usage_status"] != "ok" || (isStr && fresh != nil && old != fresh) {
		return map[string]any{}
	}
	t := unixNow()
	var zeros []any
	for _, raw := range quotas(usage) {
		if q, ok := raw.(map[string]any); ok && isZero(q["left_pct"]) && at(q["resets_at"]) > t {
			zeros = append(zeros, q)
		}
	}
	if len(zeros) == 0 {
		return map[string]any{}
	}
	return map[string]any{"usage_status": "ok", "usage_checked_at": usage["usage_checked_at"], "quotas": zeros}
}

func scopeName(v any) string {
	if !truthy(v) {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return host.MustDumps(v, host.DumpOpts{NoASCII: true})
}

func applies(quota map[string]any, model string) bool {
	if !truthy(quota["model"]) {
		return true
	}
	scope := strings.TrimPrefix(strings.ToLower(scopeName(quota["model"])), "claude-")
	name := strings.TrimPrefix(strings.ToLower(model), "claude-")
	rest, ok := strings.CutPrefix(name, scope)
	return scope != "" && ok && (rest == "" || rest == "\n" || strings.ContainsAny(rest[:1], "-._:/ "))
}

func until(data map[string]any) float64 { return finite(data["until"]) }

// scoped is the unexpired, well-formed model-scope deadlines from a cooldown file.
func scoped(data map[string]any, t float64) map[string]float64 {
	out := map[string]float64{}
	models, _ := data["models"].(map[string]any)
	for scope, deadline := range models {
		if scope != "" && finite(deadline) > t {
			out[scope] = finite(deadline)
		}
	}
	return out
}

// RecordLimit persists a confirmed limit; retryAfter is trusted seconds, < 0 for none.
func RecordLimit(profile map[string]any, state, model string, retryAfter float64) error {
	t := unixNow()
	// Account-wide zeros and Retry-After block the whole account; a zero on a
	// model-scoped quota blocks only that scope so other models stay usable.
	var deadlines []float64
	scopes := map[string]float64{}
	for _, raw := range quotas(Snapshot(profile, state)) {
		q, ok := raw.(map[string]any)
		if !ok || !isZero(q["left_pct"]) || !applies(q, model) {
			continue
		}
		reset := at(q["resets_at"])
		if reset <= t {
			continue
		}
		if !truthy(q["model"]) {
			deadlines = append(deadlines, reset)
		} else {
			scope := scopeName(q["model"])
			scopes[scope] = max(reset, scopes[scope])
		}
	}
	if retryAfter > 0 && !math.IsInf(t+retryAfter, 0) {
		deadlines = append(deadlines, t+retryAfter)
	}
	end := t + 300
	if len(deadlines) > 0 {
		end = slices.Max(deadlines)
	} else if len(scopes) > 0 {
		end = 0
	}
	path := cooldownPath(profile, state)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	unlock, err := lockFile(strings.TrimSuffix(path, ".json")+".lock", 0o600, true)
	if err != nil {
		return err
	}
	defer unlock()
	// A later concurrent failure must never shorten an existing cooldown.
	old := host.ReadJSONFile(path, manifestLimit)
	for scope, deadline := range scoped(old, t) {
		scopes[scope] = max(deadline, scopes[scope])
	}
	var data = map[string]any{"until": 0}
	if end = max(end, until(old)); end != 0 {
		data["until"] = end
	}
	if len(scopes) > 0 {
		models := map[string]any{}
		for scope, deadline := range scopes {
			models[scope] = deadline
		}
		data["models"] = models
	}
	return host.WriteJSONFile(path, data)
}

// CoolingDown reads the persisted cooldown, including after a runner restart.
// An empty model reports any cooldown, account-wide or model-scoped.
func CoolingDown(profile map[string]any, state string, t float64, model string) bool {
	data := host.ReadJSONFile(cooldownPath(profile, state), manifestLimit)
	if until(data) > t {
		return true
	}
	for scope := range scoped(data, t) {
		if model == "" || applies(map[string]any{"model": scope}, model) {
			return true
		}
	}
	return false
}

func refresh(profile map[string]any, executable, state, key string) {
	defer func() {
		busyMu.Lock()
		delete(busy, key)
		busyMu.Unlock()
	}()
	// A host can have several runner processes. Never launch duplicate CLI
	// usage probes for one login at the same time.
	path := cachePath(profile, state)
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	unlock, err := lockFile(strings.TrimSuffix(path, ".json")+".lock", 0o666, false)
	if err != nil {
		return
	}
	defer unlock()
	cached := Snapshot(profile, state)
	if recentProbe(cached) {
		return
	}
	before, account := stamp(profile), accountHash(profile)
	cached = maps.Clone(cached)
	cached["last_probe_at"] = isoUTC(now())
	if host.WriteJSONFile(path, map[string]any{"credential_stamp": before, "account": account, "usage": cached}) != nil {
		return
	}
	usage := fetchUsage(cliOf(profile), dirOf(profile), executable, 8)
	// The CLI may renew its token while probing. Same (or unconfirmed)
	// account keeps the fresh usage under the new stamp; a confirmed
	// account switch drops it. A failed probe across a renewal leaves the
	// old-stamp entry for Snapshot to filter.
	renewed, after := stamp(profile), accountHash(profile)
	if account != nil && after != nil && account != after {
		return
	}
	if usage["usage_status"] != "ok" {
		if !same(renewed, before) {
			return
		}
		if cached["usage_status"] == "ok" {
			usage = cached
		}
	}
	if after == nil {
		after = account
	}
	usage = maps.Clone(usage)
	usage["last_probe_at"] = cached["last_probe_at"]
	host.WriteJSONFile(path, map[string]any{"credential_stamp": renewed, "account": after, "usage": usage})
}

func recentProbe(usage map[string]any) bool {
	age := unixNow() - max(at(usage["last_probe_at"]), at(usage["usage_checked_at"]))
	return 0 <= age && age < 60
}

// Account is the public view of a profile; refresh starts a background usage probe.
func Account(profile map[string]any, executable, state string, refreshUsage bool) (map[string]any, error) {
	ident := identity(cliOf(profile), dirOf(profile))
	public := map[string]any{"id": profile["id"], "cli": profile["cli"], "label": profile["label"], "status": "missing"}
	if status, ok := ident["status"]; ok {
		public["status"] = status
	}
	if truthy(ident["plan"]) {
		public["plan"] = ident["plan"]
	}
	usage := Snapshot(profile, state)
	for _, key := range []string{"quotas", "usage_status", "usage_checked_at", "credits"} {
		if v, ok := usage[key]; ok {
			public[key] = v
		}
	}
	if q, ok := public["quotas"].([]any); ok && len(q) > 0 {
		public["quota"] = q[0]
	}
	if _, ok := usage["usage_status"]; !ok {
		public["usage_status"] = "unavailable"
	}
	if refreshUsage && public["status"] == "connected" {
		queueRefresh(profile, executable, state, usage)
	}
	return public, nil
}

func queueRefresh(profile map[string]any, executable, state string, usage map[string]any) {
	key := cachePath(profile, state)
	busyMu.Lock()
	defer busyMu.Unlock()
	if busy[key] || (!forceProbe.Load() && recentProbe(usage)) {
		return
	}
	busy[key] = true
	if !desktopcontrol.Submit(desktopcontrol.Default, desktopcontrol.Account, submit, func() { refresh(profile, executable, state, key) }) {
		delete(busy, key)
	}
}

// Exhausted reports a fresh zero quota that applies to model at time t.
func Exhausted(account map[string]any, model string, t float64) bool {
	checked := at(account["usage_checked_at"])
	age := t - checked
	if account["usage_status"] != "ok" || checked == 0 || age < -5 {
		return false
	}
	q, _ := account["quotas"].([]any)
	for _, raw := range q {
		quota, ok := raw.(map[string]any)
		if !ok || !isZero(quota["left_pct"]) || !applies(quota, model) {
			continue
		}
		if reset := at(quota["resets_at"]); reset > t || (reset == 0 && age <= 120) {
			return true
		}
	}
	return false
}

// Choose returns an eligible profile, or nil and a safe error code. Unknown usage is usable.
func Choose(cli, model, executable, home, state string, exclude []string) (map[string]any, string) {
	blocked := false
	for _, profile := range Profiles(cli, home, state) {
		if slices.Contains(exclude, profile["id"].(string)) {
			continue
		}
		info, _ := Account(profile, executable, state, false)
		if CoolingDown(profile, state, unixNow(), model) || Exhausted(info, model, unixNow()) {
			blocked = true
			continue
		}
		if info["status"] != "connected" {
			continue
		}
		return profile, ""
	}
	if blocked {
		return nil, "quota_exhausted"
	}
	return nil, "login_unavailable"
}

// ChildEnvironment points the child CLI at the profile's folder and drops gateway/API
// settings that would silently charge another provider while this subscription is selected.
func ChildEnvironment(env map[string]string, profile map[string]any) map[string]string {
	out := maps.Clone(env)
	if out == nil {
		out = map[string]string{}
	}
	names := []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"}
	dirVar := "CLAUDE_CONFIG_DIR"
	if cliOf(profile) == "codex" {
		names, dirVar = []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "CODEX_API_KEY"}, "CODEX_HOME"
	}
	out[dirVar] = dirOf(profile)
	for _, name := range names {
		delete(out, name)
	}
	return out
}
