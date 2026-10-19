package memory

import (
	"context"
	"net/http"
	"os"
	"path/filepath"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/notifycfg"
)

// Telegram and Slack destinations live in the host notify file the crew runner reads (ARKISM_CREW_NOTIFY).
// shortcut: this edits the file on the machine running this server, so it only reaches a runner on the same
// machine (the desktop's own server); a remote runner needs the provider-key style handoff.
func notifyPath() string {
	if v := os.Getenv("ARKISM_CREW_NOTIFY"); v != "" {
		return v
	}
	return filepath.Join(host.AgentsState, "notify.json")
}

type MessengerSave struct {
	Scope    Scope  `json:"scope"`
	Channel  string `json:"channel"`
	Name     string `json:"name,omitempty"`
	BotToken string `json:"bot_token,omitempty"`
	ChatID   string `json:"chat_id,omitempty"`
	Webhook  string `json:"webhook_url,omitempty"`
}

type MessengerRef struct {
	Scope   Scope  `json:"scope"`
	Channel string `json:"channel"`
	Name    string `json:"name,omitempty"`
}

func notifyErr(err error) error {
	if err == nil {
		return nil
	}
	if err == notifycfg.ErrInvalid {
		return ErrInvalid
	}
	return err
}

func refName(n string) string {
	if n == "" {
		return "default"
	}
	return n
}

func (s *Service) Messengers(ctx context.Context, token string, scope Scope) (map[string]any, error) {
	if !validScope(scope) {
		return nil, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colProviders, scope, providersID(scope), ""); err != nil {
		return nil, err
	}
	l, err := notifycfg.List(notifyPath())
	return map[string]any{"destinations": l}, err
}

func (s *Service) SaveMessenger(ctx context.Context, token string, in MessengerSave) (map[string]any, error) {
	if !validScope(in.Scope) {
		return nil, ErrInvalid
	}
	if _, err := s.humanProviders(ctx, token, in.Scope); err != nil {
		return nil, err
	}
	if err := notifyErr(notifycfg.Save(notifyPath(), in.Channel, in.Name, map[string]string{"bot_token": in.BotToken, "chat_id": in.ChatID, "webhook_url": in.Webhook})); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (s *Service) RemoveMessenger(ctx context.Context, token string, in MessengerRef) (map[string]any, error) {
	if !validScope(in.Scope) {
		return nil, ErrInvalid
	}
	if _, err := s.humanProviders(ctx, token, in.Scope); err != nil {
		return nil, err
	}
	if err := notifyErr(notifycfg.Remove(notifyPath(), in.Channel, refName(in.Name))); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// TestMessenger answers 200 {"ok":false,"error":...} when the destination refuses, so the screen can show why.
func (s *Service) TestMessenger(ctx context.Context, token string, in MessengerRef) (map[string]any, error) {
	if !validScope(in.Scope) {
		return nil, ErrInvalid
	}
	if _, err := s.humanProviders(ctx, token, in.Scope); err != nil {
		return nil, err
	}
	err := notifycfg.Test(ctx, notifyPath(), in.Channel, refName(in.Name))
	if err == notifycfg.ErrInvalid {
		return nil, ErrInvalid
	}
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}, nil
	}
	return map[string]any{"ok": true}, nil
}

func messengerRoutes(mux *http.ServeMux, s *Service) {
	mux.HandleFunc("GET /v1/messengers", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.Messengers(r.Context(), bearer(r), scopeQuery(r))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/messengers", func(w http.ResponseWriter, r *http.Request) {
		var in MessengerSave
		if !decode(w, r, &in) {
			return
		}
		out, e := s.SaveMessenger(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/messengers/remove", func(w http.ResponseWriter, r *http.Request) {
		var in MessengerRef
		if !decode(w, r, &in) {
			return
		}
		out, e := s.RemoveMessenger(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/messengers/test", func(w http.ResponseWriter, r *http.Request) {
		var in MessengerRef
		if !decode(w, r, &in) {
			return
		}
		out, e := s.TestMessenger(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}
