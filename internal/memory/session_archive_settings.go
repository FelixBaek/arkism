package memory

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"
)

// colSessionArchiveSettings holds one retention choice per workspace and project: how long
// conversation originals live before expiry (and how far a real use extends them).
const colSessionArchiveSettings = "session_archive_settings"

// sessionArchiveDayChoices are the windows a person can pick; 7 is the default.
var sessionArchiveDayChoices = []int{7, 30, 90}

type SessionArchiveSettings struct {
	Days int `json:"days" bson:"days"`
}

type SessionArchiveSettingsInput struct {
	Scope Scope `json:"scope"`
	Days  int   `json:"days"`
}

// The choice covers the workspace and project, whichever user or agent saved the original.
func sessionArchiveSettingsKey(scope Scope) (Scope, string) {
	ws := Scope{WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID}
	return ws, "session-archive-settings:" + ContentHash(structuredText(ws))[:16]
}

// sessionArchiveTTL is the workspace's chosen window, or SessionArchiveTTL when none was saved.
func sessionArchiveTTL(ctx context.Context, tx Tx, scope Scope) (time.Duration, error) {
	ws, id := sessionArchiveSettingsKey(scope)
	r, err := tx.Get(ctx, colSessionArchiveSettings, ws, id)
	if errors.Is(err, ErrNotFound) || err == nil && r.SessionArchiveSettings == nil {
		return SessionArchiveTTL, nil
	}
	if err != nil {
		return 0, err
	}
	return time.Duration(r.SessionArchiveSettings.Days) * 24 * time.Hour, nil
}

func (s *Service) SessionArchiveSettings(ctx context.Context, token string, scope Scope) (SessionArchiveSettings, error) {
	if s.Repo == nil {
		return SessionArchiveSettings{}, ErrUnavailable
	}
	if !validScope(scope) {
		return SessionArchiveSettings{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colSessionArchives, scope, "", ""); err != nil {
		return SessionArchiveSettings{}, err
	}
	var ttl time.Duration
	err := s.sessionArchiveTransaction(ctx, func(ctx context.Context, tx Tx) error {
		var e error
		ttl, e = sessionArchiveTTL(ctx, tx, scope)
		return e
	})
	return SessionArchiveSettings{Days: int(ttl / (24 * time.Hour))}, err
}

// SetSessionArchiveSettings saves the retention window. Only a person may change it:
// it decides when originals are deleted. Originals pick up the new window on their
// next save or use.
func (s *Service) SetSessionArchiveSettings(ctx context.Context, token string, in SessionArchiveSettingsInput) (SessionArchiveSettings, error) {
	if s.Repo == nil {
		return SessionArchiveSettings{}, ErrUnavailable
	}
	if !validScope(in.Scope) || !slices.Contains(sessionArchiveDayChoices, in.Days) {
		return SessionArchiveSettings{}, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "write:"+colSessionArchives, in.Scope, "", "")
	if err != nil {
		return SessionArchiveSettings{}, err
	}
	if d.Role != roleHuman {
		return SessionArchiveSettings{}, ErrDenied
	}
	ws, id := sessionArchiveSettingsKey(in.Scope)
	err = s.sessionArchiveTransaction(ctx, func(ctx context.Context, tx Tx) error {
		now := time.Now().UTC().Truncate(time.Millisecond)
		settings := SessionArchiveSettings{Days: in.Days}
		r, err := tx.Get(ctx, colSessionArchiveSettings, ws, id)
		if errors.Is(err, ErrNotFound) {
			r = Record{ID: id, Scope: ws, Kind: "settings", Status: statusActive, Revision: 1, Text: "settings", SessionArchiveSettings: &settings,
				CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role}
			err = tx.Insert(ctx, colSessionArchiveSettings, r)
		} else if err == nil {
			prev := r.Revision
			r.SessionArchiveSettings, r.Revision, r.UpdatedAt = &settings, r.Revision+1, now
			err = tx.Replace(ctx, colSessionArchiveSettings, r, prev)
		}
		if err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "settings:"+colSessionArchives, id)
	})
	if err != nil {
		return SessionArchiveSettings{}, err
	}
	return SessionArchiveSettings{Days: in.Days}, nil
}

func sessionArchiveSettingsRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/session-archives/settings", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.SessionArchiveSettings(r.Context(), bearer(r), scopeQuery(r))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/session-archives/settings", func(w http.ResponseWriter, r *http.Request) {
		var in SessionArchiveSettingsInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.SetSessionArchiveSettings(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}
