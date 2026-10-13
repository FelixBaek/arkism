package host

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Cross-area calls. The port is split into areas worked on in parallel; a function one
// area owns and another calls is declared here as a variable, so callers compile before
// the owner lands, and tests can replace it the way a mock patch would.
// The owning area assigns the real function in an init() in its own file and does not
// edit this file. Values are the Python shapes: dicts are map[string]any, lists []any.

var errNotPorted = errors.New("host: not ported yet")

// Area subscription owns these.
var (
	// AccountsProfiles lists registered subscription profiles for cli ("claude" or "codex").
	AccountsProfiles = func(cli, home, state string) []map[string]any { return nil }
	// AccountsChoose returns an eligible profile, or nil and a safe error code
	// ("quota_exhausted" or "login_unavailable").
	AccountsChoose = func(cli, model, executable, home, state string, exclude []string) (map[string]any, string) {
		return nil, "login_unavailable"
	}
	// AccountsChildEnvironment returns env with the profile's config dir set.
	AccountsChildEnvironment = func(env map[string]string, profile map[string]any) map[string]string { return env }
	// AccountsRecordLimit persists a confirmed limit; retryAfter < 0 means none.
	AccountsRecordLimit = func(profile map[string]any, state, model string, retryAfter float64) error { return errNotPorted }
	// AccountsRegister registers an already signed-in folder and returns its public row.
	AccountsRegister = func(cli, label, configDir, home, state string) (map[string]any, error) { return nil, errNotPorted }
	// AccountsUnregister removes only Arkism's profile reference, never the login.
	AccountsUnregister = func(id, state string) error { return errNotPorted }
	// AccountsWait waits for the usage probes AccountsAccount started.
	AccountsWait = func() {}
	// AccountsForce makes the following AccountsAccount calls probe even right after a probe.
	AccountsForce = func() {}
	// AccountsAccount returns the public view of a profile with identity and usage.
	AccountsAccount = func(profile map[string]any, executable, state string, refresh bool) (map[string]any, error) {
		return nil, errNotPorted
	}
)

// Area C (capture, work, PC client) owns these.
var (
	// UtilityCounts returns {memory id: [injected, used, unread]} over settled sessions since
	// an ISO time ("" = all), and the session count.
	UtilityCounts = func(since string) (map[string][3]int, int, error) { return nil, 0, errNotPorted }
)

// Area RC (crew runner) owns these.
var (
	// ChildEnv is a child CLI's environment: ARKISM_CAPTURE_CHILD=1, plus a stored key only for
	// a CLI in API mode; keys nil means load them. model/key pick a 직접 추가 provider's key.
	ChildEnv = func(row CLI, keys []map[string]any, model string, key map[string]any) (map[string]string, error) {
		return nil, errNotPorted
	}
	// CustomInfo is {models, api, at} for a 직접 추가 key, cached six hours.
	CustomInfo = func(key map[string]any, refresh bool) (map[string]any, error) { return nil, errNotPorted }
	// DecideRoute asks Arkism 라우팅 for {id, model, provider, decision, classifier_ms, fallbacks}.
	DecideRoute = func(task, group string) (map[string]any, error) { return nil, errNotPorted }
	// ReportCall is best effort: the routing screen counts the call; it never fails the run.
	ReportCall = func(call map[string]any) {
		// Replaced by the gateway package at init; a no-op until then.
	}
	// CrewHookContext is the text to inject for the $handles in prompt ("" when none is live).
	CrewHookContext = func(prompt, callerModel string, timeout time.Duration) (string, error) { return "", nil }
	// ListCrew lists workflows callable by $handle.
	ListCrew = func(timeout time.Duration) ([]map[string]any, error) { return nil, errNotPorted }
	// StartWorkflow starts $handle with input {"request": task} (+ cwd) and waits up to wait.
	StartWorkflow = func(handle, task string, wait time.Duration, cwd string) (map[string]any, error) {
		return nil, errNotPorted
	}
	// DecideRun answers the approval a run waits on, then waits like StartWorkflow.
	DecideRun = func(runID string, approve bool, note string, wait time.Duration) (map[string]any, error) {
		return nil, errNotPorted
	}
)

// SkillHubError is a typed skill hub failure; Code is a fixed string.
type SkillHubError struct{ Code, Detail string }

func (e *SkillHubError) Error() string {
	if e.Detail != "" {
		return e.Code + ": " + e.Detail
	}
	return e.Code
}

// Area RS (skills and agents) owns these.
var (
	// ResolveSkills resolves refs through the hub (cached offline copies when it is unreachable,
	// labelled by resolution_mode); failures are *SkillHubError.
	ResolveSkills = func(url, token string, scope map[string]any, cacheDir string, timeout time.Duration,
		refs []string, trialID, runtime string) ([]map[string]any, error) {
		return nil, errNotPorted
	}
)

// ReadJSONFile reads a small JSON file; anything missing, larger than limit or invalid is {}.
func ReadJSONFile(path string, limit int64) map[string]any {
	f, err := os.Open(path)
	if err != nil {
		return map[string]any{}
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(bufio.NewReader(f), limit+1))
	if err != nil || int64(len(raw)) > limit {
		return map[string]any{}
	}
	v, err := Loads(string(raw))
	if obj, ok := v.(map[string]any); err == nil && ok {
		return obj
	}
	return map[string]any{}
}

// WriteJSONFile replaces path with obj (fsynced temp file then rename; the dir is created 0700).
func WriteJSONFile(path string, obj any) error {
	text, err := Dumps(obj, DumpOpts{NoASCII: true})
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".usage-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
