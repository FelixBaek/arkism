package subscription

import (
	"errors"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

type accountsFixture struct {
	t                   *testing.T
	home, state, config string
	profile             map[string]any
	base                float64 // a whole second, like float(int(time.time()))
}

func newAccounts(t *testing.T) *accountsFixture {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	a := &accountsFixture{t: t, home: home, state: filepath.Join(home, "state"), config: filepath.Join(home, ".claude"),
		base: float64(time.Now().Unix())}
	a.login(a.config, "test-private-token", "")
	a.profile = Profiles("claude", home, a.state)[0]
	oldFetch, oldSubmit, oldIdentity, oldNow := fetchUsage, submit, identity, now
	t.Cleanup(func() { fetchUsage, submit, identity, now = oldFetch, oldSubmit, oldIdentity, oldNow })
	fetchUsage = func(string, string, string, float64) map[string]any {
		t.Error("unexpected live usage probe")
		return result(time.Now(), "", nil)
	}
	return a
}

func (a *accountsFixture) login(config, token, account string) {
	a.t.Helper()
	os.MkdirAll(config, 0o700)
	oauth := map[string]any{"accessToken": token, "subscriptionType": "max"}
	if account != "" {
		oauth["accountId"] = account
	}
	if err := os.WriteFile(filepath.Join(config, ".credentials.json"), []byte(mustDumps(map[string]any{"claudeAiOauth": oauth})), 0o600); err != nil {
		a.t.Fatal(err)
	}
}

func timeOf(t float64) time.Time { return time.Unix(0, int64(math.Round(t*1e9))) }

func freeze(t float64) { now = func() time.Time { return timeOf(t) } }

func noSubmit(t *testing.T) {
	submit = func(func()) { t.Error("unexpected background probe") }
}

// usage is a confirmed quota checked at `checked` that resets at `reset`.
func usage(left any, model string, checked, reset float64) map[string]any {
	return map[string]any{"usage_status": "ok", "usage_checked_at": isoUTC(timeOf(checked)),
		"quotas": []any{map[string]any{"window": "week", "left_pct": left, "model": model, "resets_at": isoUTC(timeOf(reset))}}}
}

func (a *accountsFixture) usage() map[string]any { return usage(0, "", a.base, a.base+3600) }

func (a *accountsFixture) cache(profile, u map[string]any) {
	a.t.Helper()
	if err := host.WriteJSONFile(cachePath(profile, a.state), map[string]any{"credential_stamp": stamp(profile), "usage": u}); err != nil {
		a.t.Fatal(err)
	}
}

func (a *accountsFixture) choose(model string) []any {
	p, code := Choose("claude", model, "not-run", a.home, a.state, nil)
	if p == nil {
		return []any{nil, code}
	}
	return []any{p["id"], code}
}

func (a *accountsFixture) cooldown() map[string]any {
	return host.ReadJSONFile(cooldownPath(a.profile, a.state), manifestLimit)
}

func num(v any) float64 { n, _ := number(v, false); return n }

func TestExhaustedFreshModelLimitAndResetExpiry(t *testing.T) {
	a := newAccounts(t)
	n := unixNow()
	with := func(u map[string]any, k string, v any) map[string]any { u = maps.Clone(u); u[k] = v; return u }
	for _, c := range []struct {
		usage map[string]any
		model string
		want  bool
	}{
		{a.usage(), "opus", true},
		{usage(0, "opus", a.base, a.base+3600), "claude-opus-5-5", true},
		{usage(0, "opus", a.base, a.base+3600), "sonnet", false},
		{usage(0, "opus", a.base, a.base+3600), "octopus", false},
		{usage(0, "claude-", a.base, a.base+3600), "opus", false},
		{with(a.usage(), "usage_checked_at", nil), "opus", false},
		{usage(0, "", n-121, a.base+3600), "opus", true},
		{usage(0, "", n+30, a.base+3600), "opus", false},
		{usage(0, "", a.base, n-1), "opus", false},
		{with(a.usage(), "usage_status", "unavailable"), "opus", false},
		{usage(false, "", a.base, a.base+3600), "opus", true}, // Python's False == 0
	} {
		if got := Exhausted(c.usage, c.model, n); got != c.want {
			t.Errorf("%s for %s: got %v", mustDumps(c.usage), c.model, got)
		}
	}
}

func TestStaleZeroWithoutResetExpiresAndFutureResetRecovers(t *testing.T) {
	a := newAccounts(t)
	noSubmit(t)
	u := usage(0, "", a.base-121, a.base+30)
	a.cache(a.profile, u)
	freeze(a.base)
	eq(t, a.choose("opus"), []any{nil, "quota_exhausted"})
	freeze(a.base + 30)
	eq(t, a.choose("opus"), []any{"claude", ""})
	q := u["quotas"].([]any)[0].(map[string]any)
	delete(q, "resets_at")
	eq(t, Exhausted(u, "opus", a.base), false)
	u["usage_checked_at"] = isoUTC(timeOf(a.base - 120))
	eq(t, Exhausted(u, "opus", a.base), true)
}

func TestCooldownUsesApplicableResetOrTrustedSeconds(t *testing.T) {
	a := newAccounts(t)
	n := a.base
	a.cache(a.profile, usage(0, "opus", n-900, n+900))
	freeze(n)
	if err := RecordLimit(a.profile, a.state, "claude-opus-5-5", 60); err != nil {
		t.Fatal(err)
	}
	eq(t, []any{CoolingDown(a.profile, a.state, n+899, ""), CoolingDown(a.profile, a.state, n+900, "")}, []any{true, false})
	for _, c := range []struct {
		model            string
		left             int
		reset, retry, at float64
	}{
		{"sonnet", 0, n + 900, 75.5, 75.5},
		{"opus", 10, n + 900, -1, 300},
		{"opus", 0, n - 1, -1, 300},
	} {
		os.Remove(cooldownPath(a.profile, a.state))
		a.cache(a.profile, usage(c.left, "opus", n, c.reset))
		if err := RecordLimit(a.profile, a.state, c.model, c.retry); err != nil {
			t.Fatal(err)
		}
		if !CoolingDown(a.profile, a.state, n+c.at-0.1, "") || CoolingDown(a.profile, a.state, n+c.at, "") {
			t.Errorf("%+v: wrong cooldown %s", c, mustDumps(a.cooldown()))
		}
	}
}

func TestUnknownLimitFallbackRejectsUntrustedRetryAndPreservesPrivacy(t *testing.T) {
	a := newAccounts(t)
	n := a.base
	freeze(n)
	for _, retry := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1), 0} {
		if err := RecordLimit(a.profile, a.state, "opus", retry); err != nil {
			t.Fatal(err)
		}
		if !CoolingDown(a.profile, a.state, n+299, "") || CoolingDown(a.profile, a.state, n+300, "") {
			t.Errorf("retry %v: %s", retry, mustDumps(a.cooldown()))
		}
	}
	data := a.cooldown()
	if len(data) != 1 || num(data["until"]) != n+300 {
		t.Errorf("cooldown %s", mustDumps(data))
	}
	path := cooldownPath(a.profile, a.state)
	for p, mode := range map[string]os.FileMode{path: 0o600, strings.TrimSuffix(path, ".json") + ".lock": 0o600, filepath.Dir(path): 0o700} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != mode {
			t.Errorf("%s mode %v", p, fi.Mode().Perm())
		}
	}
	if !regexp.MustCompile(`^[a-f0-9]{64}\.json$`).MatchString(filepath.Base(path)) {
		t.Error(path)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), a.config) || strings.Contains(string(raw), "test-private-token") {
		t.Error("cooldown leaks the folder or token")
	}
	renamed := maps.Clone(a.profile)
	renamed["id"] = "renamed"
	other := maps.Clone(a.profile)
	other["config_dir"] = filepath.Join(a.home, "different")
	eq(t, []any{CoolingDown(renamed, a.state, n, ""), CoolingDown(other, a.state, n, "")}, []any{true, false})
}

func TestCooldownSurvivesRestartAndSelectionRecovers(t *testing.T) {
	a := newAccounts(t)
	noSubmit(t)
	freeze(a.base)
	if err := RecordLimit(a.profile, a.state, "opus", -1); err != nil {
		t.Fatal(err)
	}
	// Only the file carries the cooldown: a bare profile, as a restarted runner builds it, sees it.
	if !CoolingDown(map[string]any{"cli": "claude", "config_dir": a.config}, a.state, a.base, "") {
		t.Error("cooldown not persisted")
	}
	eq(t, a.choose("opus"), []any{nil, "quota_exhausted"})
	// An expired token must not reach crew's default-CLI login recovery path.
	identity = func(string, string) map[string]any { return map[string]any{"status": "expired"} }
	eq(t, a.choose("opus"), []any{nil, "quota_exhausted"})
	identity = Identity
	freeze(a.base + 300)
	eq(t, a.choose("opus"), []any{"claude", ""})
}

func TestConcurrentWritersLockAndKeepLongestCooldown(t *testing.T) {
	a := newAccounts(t)
	if err := RecordLimit(a.profile, a.state, "opus", -1); err != nil {
		t.Fatal(err)
	}
	path := cooldownPath(a.profile, a.state)
	freeze(a.base)
	unlock, err := lockFile(strings.TrimSuffix(path, ".json")+".lock", 0o600, true)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var done atomic.Int32
	for _, seconds := range []float64{3600, 30, 7200, 60} {
		wg.Go(func() {
			if err := RecordLimit(a.profile, a.state, "opus", seconds); err != nil {
				t.Error(err)
			}
			done.Add(1)
		})
	}
	time.Sleep(50 * time.Millisecond)
	if done.Load() != 0 {
		t.Error("a writer ran while the lock was held")
	}
	unlock()
	wg.Wait()
	if data := a.cooldown(); len(data) != 1 || num(data["until"]) != a.base+7200 {
		t.Errorf("cooldown %s", mustDumps(data))
	}
}

func TestFailedUsageRefreshPreservesZeroAndThrottlesRetries(t *testing.T) {
	a := newAccounts(t)
	old := usage(0, "", unixNow()-600, a.base+3600)
	a.cache(a.profile, old)
	var calls atomic.Int32
	fetchUsage = func(string, string, string, float64) map[string]any {
		calls.Add(1)
		return map[string]any{"usage_status": "unavailable", "quotas": []any{}, "usage_checked_at": isoUTC(time.Now())}
	}
	refresh(a.profile, "fake", a.state, "test")
	refresh(a.profile, "fake", a.state, "test")
	noSubmit(t)
	Account(a.profile, "fake", a.state, true)
	eq(t, calls.Load(), int32(1))
	cached := maps.Clone(Snapshot(a.profile, a.state))
	delete(cached, "last_probe_at")
	eq(t, cached, old)
	eq(t, Exhausted(cached, "opus", unixNow()), true)

	future := unixNow() + 61
	freeze(future)
	calls.Store(0)
	fetchUsage = func(string, string, string, float64) map[string]any {
		calls.Add(1)
		return usage(40, "", future, future+3600)
	}
	refresh(a.profile, "fake", a.state, "test")
	eq(t, calls.Load(), int32(1))
	eq(t, Exhausted(Snapshot(a.profile, a.state), "opus", future), false)
}

func TestModelScopedLimitDoesNotBlockOtherModels(t *testing.T) {
	a := newAccounts(t)
	noSubmit(t)
	n, week := a.base, a.base+7*86400
	a.cache(a.profile, usage(0, "opus", n, week))
	freeze(n)
	if err := RecordLimit(a.profile, a.state, "claude-opus-5-5", -1); err != nil {
		t.Fatal(err)
	}
	eq(t, a.cooldown(), map[string]any{"until": 0, "models": map[string]any{"opus": week}})
	eq(t, []any{CoolingDown(a.profile, a.state, n+3600, "claude-opus-5-5"), CoolingDown(a.profile, a.state, n+3600, "claude-sonnet-5-5"),
		CoolingDown(a.profile, a.state, n+3600, ""), CoolingDown(a.profile, a.state, week, "opus")}, []any{true, false, true, false})
	eq(t, a.choose("claude-sonnet-5-5"), []any{"claude", ""})
	eq(t, a.choose("claude-opus-5-5"), []any{nil, "quota_exhausted"})
	// Retry-After stays account-wide; merging keeps the longer scoped deadline.
	if err := RecordLimit(a.profile, a.state, "claude-sonnet-5-5", 60); err != nil {
		t.Fatal(err)
	}
	data := a.cooldown()
	if num(data["until"]) != n+60 || mustDumps(data["models"]) != mustDumps(map[string]any{"opus": week}) {
		t.Errorf("cooldown %s", mustDumps(data))
	}
	eq(t, CoolingDown(a.profile, a.state, n+59, "sonnet"), true)
	// Expired scoped entries are dropped on the next write; junk is ignored.
	path := cooldownPath(a.profile, a.state)
	host.WriteJSONFile(path, map[string]any{"until": 0, "models": map[string]any{"opus": n - 1, "": week, "bad": "x", "none": nil}})
	a.cache(a.profile, map[string]any{})
	if err := RecordLimit(a.profile, a.state, "opus", -1); err != nil {
		t.Fatal(err)
	}
	if data := a.cooldown(); len(data) != 1 || num(data["until"]) != n+300 {
		t.Errorf("cooldown %s", mustDumps(data))
	}
	host.WriteJSONFile(path, map[string]any{"until": 0, "models": []any{"opus"}})
	eq(t, CoolingDown(a.profile, a.state, n, "opus"), false)
}

func TestTokenRefreshKeepsOnlyFutureZero(t *testing.T) {
	a := newAccounts(t)
	noSubmit(t)
	n := a.base
	u := usage(0, "opus", n, n+600)
	first := u["quotas"].([]any)[0].(map[string]any)
	u["quotas"] = append(u["quotas"].([]any), map[string]any{"window": "5h", "left_pct": 40, "resets_at": first["resets_at"]},
		map[string]any{"window": "week", "left_pct": nil})
	u["credits"] = 5
	a.cache(a.profile, u)
	a.login(a.config, "renewed-private-token", "")
	freeze(n)
	kept := Snapshot(a.profile, a.state)
	eq(t, kept, map[string]any{"usage_status": "ok", "usage_checked_at": u["usage_checked_at"], "quotas": []any{first}})
	eq(t, []any{Exhausted(kept, "opus", n), Exhausted(kept, "sonnet", n)}, []any{true, false})
	freeze(n + 600)
	eq(t, Snapshot(a.profile, a.state), map[string]any{})
	eq(t, a.choose("opus"), []any{"claude", ""})
}

func TestConfirmedAccountChangeInvalidatesCachedUsage(t *testing.T) {
	a := newAccounts(t)
	a.login(a.config, "test-private-token", "acct-a")
	path := cachePath(a.profile, a.state)
	host.WriteJSONFile(path, map[string]any{"credential_stamp": stamp(a.profile), "account": accountHash(a.profile), "usage": a.usage()})
	if raw, _ := os.ReadFile(path); strings.Contains(string(raw), "acct-a") {
		t.Error("account id stored in clear")
	}
	a.login(a.config, "same-user-renewed", "acct-a")
	eq(t, Snapshot(a.profile, a.state)["quotas"].([]any)[0].(map[string]any)["left_pct"], 0)
	a.login(a.config, "other-user", "acct-b")
	eq(t, Snapshot(a.profile, a.state), map[string]any{})
}

func TestRefreshKeepsNewUsageAcrossRenewalAndDropsAccountSwitch(t *testing.T) {
	a := newAccounts(t)
	for _, c := range []struct {
		account string
		want    any
	}{{"acct-a", 40}, {"acct-b", nil}} {
		a.login(a.config, "test-private-token", "acct-a")
		os.Remove(cachePath(a.profile, a.state))
		fetchUsage = func(string, string, string, float64) map[string]any {
			a.login(a.config, "renewed-"+c.account+"-token", c.account)
			return usage(40, "", unixNow(), unixNow()+3600)
		}
		refresh(a.profile, "fake", a.state, "test")
		var got any
		if q, _ := Snapshot(a.profile, a.state)["quotas"].([]any); len(q) > 0 {
			got = q[0].(map[string]any)["left_pct"]
		}
		eq(t, got, c.want)
	}
}

func TestNextAccountSelectedWithoutTouchingLogin(t *testing.T) {
	a := newAccounts(t)
	extra := filepath.Join(a.home, "other")
	a.login(extra, "test-private-token", "")
	host.WriteJSONFile(filepath.Join(a.state, "subscription-accounts.json"),
		map[string]any{"accounts": []any{map[string]any{"cli": "claude", "id": "backup", "label": "보조", "config_dir": extra}}})
	a.cache(a.profile, a.usage())
	original, _ := os.ReadFile(filepath.Join(a.config, ".credentials.json"))
	chosen, code := host.AccountsChoose("claude", "opus", "not-run", a.home, a.state, nil)
	eq(t, []any{chosen["id"], code}, []any{"backup", ""})
	env := host.AccountsChildEnvironment(map[string]string{"HOME": a.home, "ANTHROPIC_AUTH_TOKEN": "gateway", "ANTHROPIC_BASE_URL": "http://gateway"}, chosen)
	eq(t, env, map[string]any{"HOME": a.home, "CLAUDE_CONFIG_DIR": extra})
	if now, _ := os.ReadFile(filepath.Join(a.config, ".credentials.json")); string(now) != string(original) {
		t.Error("login changed")
	}
	public, _ := host.AccountsAccount(chosen, "not-run", a.state, false)
	if strings.Contains(mustDumps(public), "test-private-token") {
		t.Error("token in public view")
	}
	a.cache(chosen, a.usage())
	eq(t, a.choose("opus"), []any{nil, "quota_exhausted"})
	codex := ChildEnvironment(map[string]string{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": "u", "CODEX_API_KEY": "k"}, map[string]any{"cli": "codex", "config_dir": extra})
	eq(t, codex, map[string]any{"CODEX_HOME": extra})
}

func TestUsageProbeDoesNotHoldUpInventoryOrDuplicate(t *testing.T) {
	a := newAccounts(t)
	started, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	fetchUsage = func(string, string, string, float64) map[string]any {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-finish:
		case <-time.After(3 * time.Second):
		}
		return usage(42, "", unixNow(), unixNow()+3600)
	}
	before := time.Now()
	got, _ := Account(a.profile, "fake", a.state, true)
	if time.Since(before) > 500*time.Millisecond || got["usage_status"] != "unavailable" {
		t.Errorf("inventory waited for the probe: %v", got["usage_status"])
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe never started")
	}
	Account(a.profile, "fake", a.state, true)
	close(finish)
	WaitRefresh()
	eq(t, calls.Load(), int32(1))
	eq(t, Snapshot(a.profile, a.state)["quotas"].([]any)[0].(map[string]any)["left_pct"], 42)
}

func TestInvalidAndDuplicateProfilesDoNotEscapeManifest(t *testing.T) {
	a := newAccounts(t)
	for _, data := range []any{map[string]any{"accounts": 1}, []any{},
		map[string]any{"accounts": []any{map[string]any{"id": "same", "cli": "claude", "label": "same", "config_dir": a.config}}},
		map[string]any{"accounts": []any{map[string]any{"id": "Bad!", "cli": "claude", "label": "x", "config_dir": "/x"},
			map[string]any{"id": "ok", "cli": "claude", "label": "a\nb", "config_dir": "/y"},
			map[string]any{"id": "rel", "cli": "claude", "label": "x", "config_dir": "relative"}}}} {
		host.WriteJSONFile(filepath.Join(a.state, "subscription-accounts.json"), data)
		eq(t, len(Profiles("claude", a.home, a.state)), 1)
	}
	eq(t, len(Profiles("gemini", a.home, a.state)), 0)
}

func TestRegisterRemoveDoesNotCopyOrEraseCredentials(t *testing.T) {
	a := newAccounts(t)
	extra := filepath.Join(a.home, "extra-login")
	a.login(extra, "test-private-token", "")
	raw, _ := os.ReadFile(filepath.Join(extra, ".credentials.json"))
	public, err := host.AccountsRegister("claude", "보조", extra, a.home, a.state)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := Register("claude", " 보조 ", extra, a.home, a.state)
	eq(t, again, public)
	eq(t, len(host.AccountsProfiles("claude", a.home, a.state)), 2)
	manifest := filepath.Join(a.state, "subscription-accounts.json")
	text, _ := os.ReadFile(manifest)
	if strings.Contains(string(text), "test-private-token") {
		t.Error("token copied into the manifest")
	}
	if fi, _ := os.Stat(manifest); fi.Mode().Perm() != 0o600 {
		t.Errorf("manifest mode %v", fi.Mode().Perm())
	}
	if err := host.AccountsUnregister(public["id"].(string), a.state); err != nil {
		t.Fatal(err)
	}
	eq(t, len(Profiles("claude", a.home, a.state)), 1)
	if after, _ := os.ReadFile(filepath.Join(extra, ".credentials.json")); string(after) != string(raw) {
		t.Error("login changed")
	}

	// Refusals carry the Korean message as a crew error.
	var crew *host.CrewError
	for _, err := range []error{
		func() error { _, err := Register("gemini", "x", extra, a.home, a.state); return err }(),
		func() error { _, err := Register("claude", "a\tb", extra, a.home, a.state); return err }(),
		func() error {
			_, err := Register("claude", "x", filepath.Join(a.home, "nobody"), a.home, a.state)
			return err
		}(),
		Unregister(public["id"].(string), a.state),
	} {
		if !errors.As(err, &crew) {
			t.Errorf("want a crew error, got %v", err)
		}
	}
}
