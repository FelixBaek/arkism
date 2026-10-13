package memory

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"
)

// Design systems: the operator's library of design systems, grouped by product
// and purpose (an app's screens, a landing page, slides). One record per
// system; archiving hides it from the list without deleting it.
const colDesignSystems = "design_systems"

type DesignColor struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type DesignSystem struct {
	Title       string        `json:"title"`
	Product     string        `json:"product,omitempty"`
	Purpose     string        `json:"purpose,omitempty"`
	Description string        `json:"description,omitempty"`
	Source      string        `json:"source,omitempty"`
	Colors      []DesignColor `json:"colors,omitempty"`
	Fonts       []string      `json:"fonts,omitempty"`
	Guide       string        `json:"guide,omitempty"`
	Tags        []string      `json:"tags,omitempty"`
}

type DesignSystemInput struct {
	Scope  Scope        `json:"scope"`
	System DesignSystem `json:"system"`
}

type DesignSystemUpdate struct {
	Scope            Scope         `json:"scope"`
	ExpectedRevision int64         `json:"expected_revision"`
	System           *DesignSystem `json:"system,omitempty"`
	Archive          bool          `json:"archive,omitempty"`
}

type DesignSystemPage struct {
	Items []Record `json:"items"`
}

func runesAtMost(s string, n int) bool { return utf8.RuneCountInString(s) <= n }

func validDesignSystem(d DesignSystem) bool {
	title := utf8.RuneCountInString(strings.TrimSpace(d.Title))
	if title == 0 || title > 80 || !runesAtMost(d.Product, 60) || !runesAtMost(d.Purpose, 60) || !runesAtMost(d.Description, 2000) ||
		!runesAtMost(d.Source, 500) || !runesAtMost(d.Guide, 20000) || len(d.Colors) > 64 || !shortList(d.Fonts, 8, 80) || !shortList(d.Tags, 16, 40) {
		return false
	}
	for _, c := range d.Colors {
		if strings.TrimSpace(c.Name) == "" || !runesAtMost(c.Name, 40) || strings.TrimSpace(c.Value) == "" || len(c.Value) > 64 {
			return false
		}
	}
	return true
}

func (s *Service) ListDesignSystems(ctx context.Context, token string, scope Scope) (DesignSystemPage, error) {
	if !validScope(scope) {
		return DesignSystemPage{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colDesignSystems, scope, "", ""); err != nil {
		return DesignSystemPage{}, err
	}
	rows, err := s.Repo.ListRecords(ctx, scope, colDesignSystems, 500)
	if err != nil {
		return DesignSystemPage{}, err
	}
	out := DesignSystemPage{Items: []Record{}}
	for _, r := range rows {
		if r.Status == statusActive && r.DesignSystem != nil {
			out.Items = append(out.Items, r)
		}
	}
	return out, nil
}

func (s *Service) CreateDesignSystem(ctx context.Context, token string, in DesignSystemInput) (Record, error) {
	if !validScope(in.Scope) || !validDesignSystem(in.System) {
		return Record{}, ErrInvalid
	}
	if err := noSecrets(in.System); err != nil {
		return Record{}, err
	}
	d, err := s.authorize(ctx, token, "write:"+colDesignSystems, in.Scope, "", "")
	if err != nil {
		return Record{}, err
	}
	if d.Role != roleHuman {
		return Record{}, ErrDenied
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	sys := in.System
	r := Record{ID: ID(), Scope: in.Scope, Kind: colDesignSystems, Status: statusActive, Revision: 1, Text: sys.Title, DesignSystem: &sys,
		CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if err := tx.Insert(ctx, colDesignSystems, r); err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "create:"+colDesignSystems, r.ID)
	})
	return r, err
}

// UpdateDesignSystem replaces the system's fields, or archives it.
func (s *Service) UpdateDesignSystem(ctx context.Context, token, id string, in DesignSystemUpdate) (Record, error) {
	if !validScope(in.Scope) || !validRunLabel(id, 256) || in.ExpectedRevision < 1 || (in.System == nil) == !in.Archive || in.System != nil && !validDesignSystem(*in.System) {
		return Record{}, ErrInvalid
	}
	if in.System != nil {
		if err := noSecrets(*in.System); err != nil {
			return Record{}, err
		}
	}
	d, err := s.authorize(ctx, token, "write:"+colDesignSystems, in.Scope, id, "")
	if err != nil {
		return Record{}, err
	}
	if d.Role != roleHuman {
		return Record{}, ErrDenied
	}
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, colDesignSystems, in.Scope, id)
		if err != nil {
			return err
		}
		if r.Revision != in.ExpectedRevision || r.Status != statusActive {
			return ErrConflict
		}
		prev := r.Revision
		action := "update:" + colDesignSystems
		if in.Archive {
			r.Status, action = "archived", "archive:"+colDesignSystems
		} else {
			sys := *in.System
			r.DesignSystem, r.Text = &sys, sys.Title
		}
		r.Revision++
		r.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
		if err := tx.Replace(ctx, colDesignSystems, r, prev); err != nil {
			return err
		}
		out = r
		return audit(ctx, tx, in.Scope, d.PrincipalID, action, id)
	})
	return out, err
}
