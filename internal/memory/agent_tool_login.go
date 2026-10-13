package memory

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MCP sign-in (mcp_login) runs the agent's own CLI on the host ("claude|codex mcp login NAME
// --no-browser"). The host posts the CLI's sign-in address here, the desktop app opens it in a
// browser and catches the redirect on the PC's loopback, posts that path back, and the host
// replays it on its own loopback where the CLI waits. Tokens never pass through Arkism: the
// one-time code in the callback is cleared as soon as the sign-in ends.

// AgentToolLogin is either the host's progress (status, auth_url, error) or the owner's callback.
type AgentToolLogin struct {
	Scope    Scope  `json:"scope"`
	Status   string `json:"status,omitempty"` // waiting | done | failed (host)
	AuthURL  string `json:"auth_url,omitempty"`
	Error    string `json:"error,omitempty"`
	Callback string `json:"callback,omitempty"` // path and query the browser came back to (owner)
}

const agentToolLoginTimeout = 15 * time.Minute

func validAuthURL(u string) bool {
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "https" || p.Host == "" || len(u) > 8000 {
		return false
	}
	r, err := url.Parse(p.Query().Get("redirect_uri"))
	return err == nil && r.Scheme == "http" && (r.Hostname() == "localhost" || r.Hostname() == "127.0.0.1") && r.Port() != ""
}

func validCallback(c string) bool {
	p, err := url.Parse(c)
	if err != nil || !strings.HasPrefix(c, "/") || strings.HasPrefix(c, "//") || len(c) > 2000 || p.Host != "" {
		return false
	}
	q := p.Query()
	return q.Get("state") != "" && (q.Get("code") != "" || q.Get("error") != "")
}

func (in AgentToolLogin) valid() bool {
	if !validScope(in.Scope) || len(in.Error) > 500 {
		return false
	}
	if in.Callback != "" { // the owner's half
		return in.Status == "" && in.AuthURL == "" && in.Error == "" && validCallback(in.Callback)
	}
	switch in.Status {
	case "waiting":
		return validAuthURL(in.AuthURL) && in.Error == ""
	case "done", "failed":
		return in.AuthURL == ""
	}
	return false
}

// apply moves one mcp_login request a step: the owner's callback while waiting, the host's
// waiting while pending, or the end. An end clears the one-time values.
func (in AgentToolLogin) apply(q *AgentToolRequest, now time.Time) error {
	expired := now.Sub(q.RequestedAt) > agentToolLoginTimeout
	switch {
	case in.Callback != "":
		if q.Status != "waiting" || q.Callback != "" || expired {
			return ErrConflict
		}
		q.Callback = in.Callback
	case in.Status == "waiting":
		if q.Status != "pending" || expired {
			return ErrConflict
		}
		q.Status, q.AuthURL = "waiting", in.AuthURL
	default:
		if q.Status != "pending" && q.Status != "waiting" {
			return ErrConflict
		}
		q.Status, q.Error, q.DoneAt, q.AuthURL, q.Callback = in.Status, in.Error, &now, "", ""
	}
	return nil
}

// expireLogins ends sign-ins nobody finished in time, so a stored redirect (and its one-time
// code) never outlives agentToolLoginTimeout. The host reports every minute, which runs this.
func (t *AgentTools) expireLogins(now time.Time) {
	for i := range t.Requests {
		q := &t.Requests[i]
		if q.Kind == "mcp_login" && (q.Status == "pending" || q.Status == "waiting") && now.Sub(q.RequestedAt) > agentToolLoginTimeout {
			q.Status, q.Error, q.DoneAt, q.AuthURL, q.Callback = "failed", "시간이 지나 멈췄어요", &now, "", ""
		}
	}
}

// AgentToolLoginStep records one step of an MCP sign-in.
func (s *Service) AgentToolLoginStep(ctx context.Context, token, id string, in AgentToolLogin) (Record, error) {
	if !in.valid() {
		return Record{}, ErrInvalid
	}
	if err := noSecrets(in.Error); err != nil {
		return Record{}, err
	}
	d, err := s.authorize(ctx, token, "write:"+colAgentTools, in.Scope, agentToolsID, "")
	if err != nil {
		return Record{}, err
	}
	if in.Callback != "" && d.Role != roleHuman {
		return Record{}, ErrDenied // only the person who signed in hands back the redirect
	}
	stepErr := ErrNotFound
	out, err := s.updateAgentTools(ctx, in.Scope, func(t *AgentTools, now time.Time) {
		for i := range t.Requests {
			if t.Requests[i].ID == id && t.Requests[i].Kind == "mcp_login" {
				stepErr = in.apply(&t.Requests[i], now)
			}
		}
	})
	if stepErr != nil {
		return Record{}, stepErr
	}
	return out, err
}

func agentToolLoginRoutes(mux *http.ServeMux, s *Service) {
	mux.HandleFunc("POST /v1/agent_tools/requests/{id}/login", func(w http.ResponseWriter, r *http.Request) {
		var in AgentToolLogin
		if !decode(w, r, &in) {
			return
		}
		out, e := s.AgentToolLoginStep(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
}
