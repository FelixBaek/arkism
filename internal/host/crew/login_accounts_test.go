package crew

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoginAccountsListModelsOnlyWhileSignedIn(t *testing.T) {
	status := map[string]string{"copilot": "missing", "nous": "expired", "xai": "missing"}
	asked := 0
	patch(t, &oauthStatus, func(pid string) string { return status[pid] })
	patch(t, &oauthModels, func(pid string) []string { asked++; return []string{pid + "-model"} })
	bin := filepath.Join(t.TempDir(), "cursor-agent")
	patch(t, &cursorBin, func() string { return bin })
	inv, err := inventory("", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]any{}
	for _, p := range inv {
		byID[pyStr(field(p, "id"))] = p
	}
	if byID["cursor"] != nil {
		t.Fatal("cursor shows only with its CLI installed")
	}
	for pid, want := range map[string]string{"copilot": "missing", "nous": "expired", "xai": "missing"} {
		accounts := list(field(byID[pid], "accounts"))
		if len(accounts) != 1 || field(accounts[0], "status") != want || field(accounts[0], "cli") != pid ||
			len(list(field(byID[pid], "models"))) != 0 {
			t.Fatalf("%s: %s", pid, dumpLine(byID[pid]))
		}
	}
	if asked != 0 {
		t.Fatal("signed-out logins are not asked for models")
	}
	status["copilot"] = "connected"
	if got := modelsFor("copilot"); len(got) != 1 || got[0] != "copilot-model" {
		t.Fatal(got)
	}
	if modelsFor("copilot"); asked != 1 {
		t.Fatal("models are cached for a day")
	}
	if err := os.WriteFile(bin, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	patch(t, &cursorStatus, func() string { return "connected" })
	if got := modelsFor("cursor"); len(got) != 1 || got[0] != "auto" || len(loginAccounts("cursor")) != 1 {
		t.Fatal(got)
	}
}

func TestCursorStatusText(t *testing.T) {
	for out, want := range map[string]string{"Logged in as a@b.c": "connected", "Not logged in": "missing", "": "missing"} {
		if got := cursorStatusText(out, nil); got != want {
			t.Fatalf("%q: %s", out, got)
		}
	}
	if cursorStatusText("Logged in", errors.New("exit 1")) != "missing" {
		t.Fatal("a failed run is not a login")
	}
}
