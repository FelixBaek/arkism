//go:build linux

package crew

import (
	"errors"

	"github.com/FelixBaek/arkism/internal/host"
	"strings"
	"testing"
)

func jobsFixture(t *testing.T) (string, map[string]any, *int) {
	calls := 0
	oldInfo, oldPerform := cliInfoFn, updatePerform
	t.Cleanup(func() { cliInfoFn, updatePerform = oldInfo, oldPerform })
	cliInfoFn = func(string, string, string, bool) map[string]any { return map[string]any{} }
	return t.TempDir(), map[string]any{"id": strings.Repeat("a", 32), "provider": "openai", "cli": "codex", "expected_version": "1.2.3", "expected_via": "npm"}, &calls
}

func TestJobsReportRetryUsesSavedResult(t *testing.T) {
	state, request, calls := jobsFixture(t)
	updatePerform = func(cli, exe string, v, via any, dir string) map[string]any {
		*calls++
		return map[string]any{"ok": true, "info": map[string]any{"version": "1.2.4", "updated": true}}
	}
	first, err1 := executeUpdate(request, "/synthetic/codex", state)
	second, err2 := executeUpdate(withKey(request, "id", request["id"]), "/synthetic/codex", state)
	if err1 != nil || err2 != nil || !same(first, second) || first["status"] != "done" || first["note"] != "새 버전을 확인했어요." || *calls != 1 {
		t.Fatal(first, second, *calls)
	}
	if r, _ := executeUpdate(withKey(request, "expected_version", "1.2.5"), "/synthetic/codex", state); r["error"] != "receipt_conflict" {
		t.Fatal(r)
	}
}

func TestJobsInterruptedUpdateIsNotRunAgain(t *testing.T) {
	state, request, calls := jobsFixture(t)
	updatePerform = func(string, string, any, any, string) map[string]any { *calls++; panic("synthetic interruption") }
	func() {
		defer func() { _ = recover() }()
		_, _ = executeUpdate(request, "/synthetic/codex", state)
	}()
	r, err := executeUpdate(request, "/synthetic/codex", state)
	if err != nil || *calls != 1 || r["error"] != "update_uncertain" {
		t.Fatal(r, err, *calls)
	}
}

func TestJobsRequestLockDefersSecondWorker(t *testing.T) {
	state, request, calls := jobsFixture(t)
	updatePerform = func(string, string, any, any, string) map[string]any {
		*calls++
		if _, err := executeUpdate(request, "/synthetic/codex", state); !errors.Is(err, errUpdateBusy) {
			t.Error("second worker not deferred:", err)
		}
		return map[string]any{"ok": false, "error": "conflict"}
	}
	result, _ := executeUpdate(request, "/synthetic/codex", state)
	again, _ := executeUpdate(request, "/synthetic/codex", state)
	if !same(result, again) || result["error"] != "conflict" || *calls != 1 {
		t.Fatal(result, again)
	}
}

func TestJobsInvalidProviderOrReceiptIsNeverExecuted(t *testing.T) {
	state, request, calls := jobsFixture(t)
	updatePerform = func(string, string, any, any, string) map[string]any { *calls++; return nil }
	for k, v := range map[string]any{"provider": "anthropic", "id": "../outside", "cli": "shell"} {
		if r, _ := executeUpdate(withKey(request, k, v), "/synthetic/codex", state); r["error"] != "invalid_request" {
			t.Error(k, r)
		}
	}
	if *calls != 0 {
		t.Fatal(*calls)
	}
}

func TestCLIInfoCachesAndRefreshesInBackground(t *testing.T) {
	f := newCLIFixture(t)
	path := f.installed("codex", "self", false)
	state := t.TempDir()
	f.http(`{"version":"1.0.0"}`, 200)
	f.reply(runReply{"", "1.0.0"})
	if got := cliInfo("codex", path, state, true); len(got) != 0 {
		t.Fatal(got)
	}
	background.Wait()
	got := cliInfo("codex", path, state, false)
	if got["version"] != "1.0.0" || got["via"] != "self" {
		t.Fatal(got)
	}
	if len(cliInfo("gemini", path, state, true)) != 0 {
		t.Fatal("unsupported cli")
	}
}

func TestRefreshCLINowCachesTheNewBinaryBeforeReturning(t *testing.T) {
	f := newCLIFixture(t)
	path := f.installed("codex", "self", false)
	state := t.TempDir()
	f.http(`{"version":"1.0.0"}`, 200)
	f.reply(runReply{"", "1.0.0"})
	refreshCLINow("codex", path, state)
	if got := cliInfo("codex", path, state, false); got["version"] != "1.0.0" {
		t.Fatal(got)
	}
}

func TestCLIUpdateRequestRefreshesTheCacheWhenDone(t *testing.T) {
	state, request, _ := jobsFixture(t)
	patch(t, &host.AgentsState, state)
	updatePerform = func(string, string, any, any, string) map[string]any {
		return map[string]any{"ok": true, "info": map[string]any{"version": "1.2.4", "updated": true}}
	}
	request["kind"] = "cli_update"
	res, err := carryOut(map[string]any{"request": request}, nil, nil)
	if err != nil || res["status"] != "done" {
		t.Fatal(res, err)
	}
	if res, _ := carryOut(map[string]any{"request": withKey(request, "cli", "custom")}, nil, nil); res["status"] != "failed" {
		t.Fatal("custom CLI must be refused", res)
	}
	refreshCLINow("gemini", "/synthetic/gemini", state) // unsupported CLIs are not inspected
}
