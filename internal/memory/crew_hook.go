package memory

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/FelixBaek/arkism/internal/crewrun"
	"github.com/FelixBaek/arkism/internal/host"
)

// CrewHookInput asks for a workflow's webhook address and token.
type CrewHookInput struct {
	Scope Scope `json:"scope"`
}

// CrewHook is where another service sends a request to start the workflow, and the token it must send.
type CrewHook struct {
	URL    string `json:"url"`
	Header string `json:"header"`
	Token  string `json:"token"`
	// ThisPCOnly is true when the runner listens on loopback only: other computers cannot reach the address.
	ThisPCOnly bool `json:"this_pc_only"`
}

// shortcut: like the notify file, the hooks file is read on the machine running this server, so it
// only reaches a runner on the same machine (the desktop's own server, or a server beside its runner).
func crewHooksPath() string {
	if v := strings.TrimSpace(os.Getenv("ARKISM_CREW_HOOKS")); v != "" {
		return v
	}
	return filepath.Join(host.Home, ".config", "arkism", "crew-hooks.json")
}

func crewHookBase() string {
	if v := strings.TrimSpace(os.Getenv("ARKISM_CREW_PUBLIC_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	listen := strings.TrimSpace(os.Getenv("ARKISM_CREW_LISTEN"))
	if listen == "" {
		listen = "127.0.0.1:8095"
	}
	return "http://" + listen
}

// CrewHookToken returns the webhook of a workflow, creating its token on first use. Owner only:
// the token starts runs, so it is shown only to a person who may change 설정.
func (s *Service) CrewHookToken(ctx context.Context, token, id string, in CrewHookInput) (CrewHook, error) {
	if !validScope(in.Scope) || !validRunLabel(id, 256) {
		return CrewHook{}, ErrInvalid
	}
	if _, err := s.humanProviders(ctx, token, in.Scope); err != nil {
		return CrewHook{}, err
	}
	if _, err := s.authorize(ctx, token, "write:"+colCrew, in.Scope, id, ""); err != nil {
		return CrewHook{}, err
	}
	r, err := s.Repo.Get(ctx, colCrew, in.Scope, id)
	if err != nil {
		return CrewHook{}, err
	}
	if r.Status != statusActive || r.Crew == nil || r.Crew.Type != "workflow" {
		return CrewHook{}, ErrNotFound
	}
	secret, err := crewrun.HookToken(crewHooksPath(), id, true)
	if err != nil {
		return CrewHook{}, err
	}
	u := crewHookBase() + "/hooks/" + url.PathEscape(id)
	local := false
	if p, err := url.Parse(u); err == nil {
		ip := net.ParseIP(p.Hostname())
		local = p.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	}
	return CrewHook{URL: u, Header: "X-Arkism-Hook-Token", Token: secret, ThisPCOnly: local}, nil
}

func crewHookRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/crew/{id}/hook", func(w http.ResponseWriter, r *http.Request) {
		var in CrewHookInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.CrewHookToken(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
}
