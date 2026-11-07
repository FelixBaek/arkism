package crew

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/subscription"
)

// Account logins in 설정 › 에이전트: copilot, nous and xai sign in with `arkism oauth login <id>`
// (subscription/oauth.go), cursor with its own CLI. Each shows one account row with its health;
// its models are listed only while it is signed in, so nothing turns on before a login.

var (
	oauthStatus   = subscription.OAuthStatus // test seams
	oauthModels   = subscription.OAuthModels
	cursorBin     = subscription.CursorBin
	cursorStatus  = cursorLogin
	cursorMu      sync.Mutex
	cursorChecked time.Time
	cursorLast    string
)

// loginAccount is the account row of an `arkism oauth` login.
func loginAccount(pid string) host.Obj {
	return host.Obj{{Key: "id", Value: pid + "-login"}, {Key: "label", Value: subscription.OAuthProviders[pid].Name + " 로그인"},
		{Key: "cli", Value: pid}, {Key: "status", Value: oauthStatus(pid)}, {Key: "default", Value: true}}
}

// loginAccounts is a provider's account-login rows; cursor's only when its CLI is installed.
func loginAccounts(pid string) []any {
	if subscription.OAuthProviders[pid] != nil {
		return []any{loginAccount(pid)}
	}
	if pid != "cursor" || !exists(cursorBin()) {
		return nil
	}
	return []any{host.Obj{{Key: "id", Value: "cursor-login"}, {Key: "label", Value: "Cursor CLI 로그인"},
		{Key: "cli", Value: "cursor"}, {Key: "status", Value: cursorStatus()}, {Key: "default", Value: true}}}
}

// loginModels is what a signed-in account login offers, asked once a day; nothing while signed out.
func loginModels(pid string) []string {
	if pid == "cursor" {
		if exists(cursorBin()) && cursorStatus() == "connected" {
			return []string{"auto"} // the CLI picks; other models can be added by hand
		}
		return nil
	}
	if oauthStatus(pid) != "connected" {
		return nil
	}
	path := stateFile(pid + "-login-models.json")
	cached, fresh := cachedModels(path)
	if fresh {
		return cached
	}
	if models := oauthModels(pid); len(models) > 0 {
		saveModels(path, models)
		return models
	}
	return cached
}

// cursorLogin asks the CLI whether it is signed in, at most every five minutes.
func cursorLogin() string {
	cursorMu.Lock()
	defer cursorMu.Unlock()
	if time.Since(cursorChecked) < 5*time.Minute {
		return cursorLast
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, cursorBin(), "status").CombinedOutput()
	cursorLast = cursorStatusText(string(out), err)
	cursorChecked = time.Now()
	return cursorLast
}

func cursorStatusText(out string, err error) string {
	text := strings.ToLower(out)
	if err != nil || strings.Contains(text, "not logged in") || !strings.Contains(text, "logged in") {
		return "missing"
	}
	return "connected"
}
