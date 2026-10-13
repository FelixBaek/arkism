package memory

import (
	"errors"
	"testing"
	"time"
)

const loginAuthURL = "https://mcp.example.com/authorize?response_type=code&redirect_uri=http%3A%2F%2Flocalhost%3A52606%2Fcallback&state=s1"

func loginRequest(t *testing.T, f *fixture) (string, func(string, AgentToolLogin) (Record, error)) {
	t.Helper()
	r, err := f.svc.RequestAgentTool(testCtx, f.human, AgentToolSwitch{Scope: scopeA, Agent: "codex", Kind: "mcp_login", Name: "cloudflare-api"})
	if err != nil {
		t.Fatal(err)
	}
	id := r.AgentTools.Requests[0].ID
	return id, func(tok string, in AgentToolLogin) (Record, error) {
		in.Scope = scopeA
		return f.svc.AgentToolLoginStep(testCtx, tok, id, in)
	}
}

// MCP sign-in: steps out of order, from the wrong principal or with bad addresses are refused.
func TestAgentToolLoginRefusals(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.RequestAgentTool(testCtx, f.human, AgentToolSwitch{Scope: scopeA, Agent: "codex", Kind: "mcp_login", Name: "cloudflare-api", URL: "https://x"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("a login request carried an address", err)
	}
	_, step := loginRequest(t, f)
	if _, err := step(f.human, AgentToolLogin{Callback: "/callback?code=c&state=s1"}); !errors.Is(err, ErrConflict) {
		t.Fatal("a callback before the sign-in page was ready", err)
	}
	if _, err := step(f.agent, AgentToolLogin{Status: "waiting", AuthURL: "https://evil.example/authorize?redirect_uri=https%3A%2F%2Fevil.example%2Fcb"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("a sign-in that does not come back to the loopback", err)
	}
	if _, err := step(f.agent, AgentToolLogin{Status: "waiting", AuthURL: loginAuthURL}); err != nil {
		t.Fatal(err)
	}
	if _, err := step(f.agent, AgentToolLogin{Callback: "/callback?code=c&state=s1"}); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent handed back the redirect", err)
	}
	for _, bad := range []string{"http://evil.example/callback?code=c&state=s1", "//evil.example/x?code=c&state=s1", "/callback?state=s1"} {
		if _, err := step(f.human, AgentToolLogin{Callback: bad}); !errors.Is(err, ErrInvalid) {
			t.Fatal("bad callback accepted", bad, err)
		}
	}
	if _, err := f.svc.AgentToolLoginStep(testCtx, f.agent, "nope", AgentToolLogin{Scope: scopeA, Status: "failed"}); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown request", err)
	}
}

// MCP sign-in: pending → waiting (host) → callback (owner, once) → done (host), then the one-time values are gone.
func TestAgentToolLoginFlow(t *testing.T) {
	f := newFixture(t)
	_, step := loginRequest(t, f)
	if r, err := step(f.agent, AgentToolLogin{Status: "waiting", AuthURL: loginAuthURL}); err != nil || r.AgentTools.Requests[0].Status != "waiting" {
		t.Fatal("waiting", err)
	}
	if r, err := step(f.human, AgentToolLogin{Callback: "/callback?code=c&state=s1"}); err != nil || r.AgentTools.Requests[0].Callback == "" {
		t.Fatal("callback", err)
	}
	if _, err := step(f.human, AgentToolLogin{Callback: "/callback?code=d&state=s1"}); !errors.Is(err, ErrConflict) {
		t.Fatal("a second callback", err)
	}
	r, err := step(f.agent, AgentToolLogin{Status: "done"})
	if q := r.AgentTools.Requests[0]; err != nil || q.Status != "done" || q.AuthURL != "" || q.Callback != "" || q.DoneAt == nil {
		t.Fatal("done must clear the one-time values", q, err)
	}
}

func TestAgentToolEntryLoginMarks(t *testing.T) {
	f := newFixture(t)
	cf := AgentToolEntry{Name: "cloudflare-api", Transport: "http", Agents: map[string]string{"claude_code": "on", "codex": "on"}, Login: map[string]string{"codex": "needed"}}
	r, err := f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, Servers: []AgentToolEntry{cf}})
	if err != nil || r.AgentTools.Servers[0].Login["codex"] != "needed" {
		t.Fatal("login mark", err)
	}
	cf.Login = map[string]string{"codex": "yes"}
	if _, err = f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, Servers: []AgentToolEntry{cf}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown login value", err)
	}
}

// A sign-in left waiting past the limit is ended by the next host report, one-time values and all.
func TestAgentToolLoginExpires(t *testing.T) {
	f := newFixture(t)
	_, step := loginRequest(t, f)
	if _, err := step(f.agent, AgentToolLogin{Status: "waiting", AuthURL: loginAuthURL}); err != nil {
		t.Fatal(err)
	}
	tools := AgentTools{Requests: []AgentToolRequest{{Kind: "mcp_login", Status: "waiting", AuthURL: loginAuthURL, Callback: "/cb?code=c&state=s", RequestedAt: time.Now().Add(-16 * time.Minute)}}}
	tools.expireLogins(time.Now())
	if q := tools.Requests[0]; q.Status != "failed" || q.AuthURL != "" || q.Callback != "" {
		t.Fatal("expired sign-in kept its values", q)
	}
	r, err := f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA})
	if err != nil || r.AgentTools.Requests[0].Status != "waiting" {
		t.Fatal("a fresh sign-in must survive a report", err)
	}
}
