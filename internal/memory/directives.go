package memory

// Directives are hard rules a person writes for answers (reflect) and for the
// context agents receive (context build). A person writes them, so they are
// active on creation. An agent may only propose one: it is stored as
// "proposed", applies nowhere, and becomes active when a person saves it. No
// agent or service principal may change or delete one. They live at project
// scope (workspace + project) so every user and agent of the project reads the same rules.

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	colDirectives          = "directives"
	directiveKind          = "directive"
	directiveMaxPerScope   = 200
	directiveTitleMaxRunes = 200
	directiveRuleMaxRunes  = 4000
	directiveMaxPriority   = 100
	directiveListDefault   = 50
	// directiveProposedMax keeps agent proposals from filling the per-scope cap people need.
	directiveProposedMax = 20
	// DirectiveAuthority labels directives apart from retrieved data: they are rules people set.
	DirectiveAuthority = "human_hard_rule"
)

var directiveTargets = []string{"reflect", "context", "both"}

// Directive is the directive-only part of a record; the rule text is Record.Text.
type Directive struct {
	// SourceScope binds an automatically approved rule to its original memory scope.
	SourceScope *Scope `json:"source_scope,omitempty" bson:"source_scope,omitempty"`
	SourceRule  string `json:"source_rule,omitempty" bson:"source_rule,omitempty"`
	Independent bool   `json:"independent,omitempty" bson:"independent,omitempty"`
	// SourceState is derived at read time, never persisted.
	SourceState string `json:"-" bson:"-"`
	Title       string `json:"title" bson:"title"`
	Priority    int    `json:"priority" bson:"priority"`
	AppliesTo   string `json:"applies_to" bson:"applies_to"`
}

type DirectiveInput struct {
	Scope            Scope    `json:"scope"`
	ExpectedRevision int64    `json:"expected_revision,omitempty"`
	Title            string   `json:"title"`
	Rule             string   `json:"rule"`
	Priority         int      `json:"priority"`
	AppliesTo        string   `json:"applies_to"`
	Active           *bool    `json:"active,omitempty"`
	Tags             []string `json:"tags,omitempty"`
}

type DirectiveView struct {
	SourceState string    `json:"source_state,omitempty"`
	ID          string    `json:"id"`
	Revision    int64     `json:"revision"`
	Scope       Scope     `json:"scope"`
	Title       string    `json:"title"`
	Rule        string    `json:"rule"`
	Priority    int       `json:"priority"`
	AppliesTo   string    `json:"applies_to"`
	Active      bool      `json:"active"`
	Proposed    bool      `json:"proposed,omitempty"`
	Tags        []string  `json:"tags"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type DirectiveList struct {
	Items      []DirectiveView `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

type DirectiveDeleteInput struct {
	Scope            Scope `json:"scope"`
	ExpectedRevision int64 `json:"expected_revision"`
}

func viewDirective(r Record) DirectiveView {
	tags := r.Tags
	if tags == nil {
		tags = []string{}
	}
	return DirectiveView{SourceState: r.Directive.SourceState, ID: r.ID, Revision: r.Revision, Scope: r.Scope, Title: r.Directive.Title, Rule: r.Text, Priority: r.Directive.Priority,
		AppliesTo: r.Directive.AppliesTo, Active: r.Status == "active", Proposed: r.Status == obsProposed, Tags: tags, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func validDirectiveInput(in DirectiveInput) error {
	title, rule := strings.TrimSpace(in.Title), strings.TrimSpace(in.Rule)
	if !validScope(in.Scope) || title == "" || rule == "" || !utf8.ValidString(in.Title+in.Rule) ||
		utf8.RuneCountInString(title) > directiveTitleMaxRunes || utf8.RuneCountInString(rule) > directiveRuleMaxRunes ||
		in.Priority < 0 || in.Priority > directiveMaxPriority || !slices.Contains(directiveTargets, in.AppliesTo) || !validTags(in.Tags) {
		return ErrInvalid
	}
	// A private span is never stored, not even as a rule.
	if hasPrivateMarker(in.Title + in.Rule + strings.Join(in.Tags, " ")) {
		return ErrInvalid
	}
	if containsSecret(structuredText(in)) {
		return ErrSecretDetected
	}
	return nil
}

func directiveStatus(active *bool) string {
	if active != nil && !*active {
		return "inactive"
	}
	return "active"
}

func (s *Service) CreateDirective(ctx context.Context, token string, in DirectiveInput) (DirectiveView, error) {
	if err := validDirectiveInput(in); err != nil || in.ExpectedRevision != 0 {
		return DirectiveView{}, cmp.Or(err, ErrInvalid)
	}
	d, err := s.wikiAuthorize(ctx, token, "write:"+colDirectives, in.Scope, "", "", roleHuman, roleAgent)
	if err != nil {
		return DirectiveView{}, err
	}
	status := directiveStatus(in.Active)
	if d.Role == roleAgent {
		status = obsProposed
		// An agent's text passes the space's defense rules like any other ingestion.
		if err = s.defendSpace(ctx, "", defenseGuard{}, in.Scope, &in.Title, &in.Rule); err != nil {
			return DirectiveView{}, err
		}
		if err = validDirectiveInput(in); err != nil {
			return DirectiveView{}, err
		}
	}
	now := time.Now().UTC()
	r := Record{ID: ID(), Scope: projectScope(in.Scope), Kind: directiveKind, Status: status, Revision: 1,
		Text: strings.TrimSpace(in.Rule), Tags: in.Tags, Directive: &Directive{Title: strings.TrimSpace(in.Title), Priority: in.Priority, AppliesTo: in.AppliesTo},
		CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role}
	// shortcut: the total per-scope cap is checked outside the transaction, so two racing creates may pass it by one.
	existing, err := s.Repo.ListRecords(ctx, r.Scope, colDirectives, directiveMaxPerScope)
	if err != nil {
		return DirectiveView{}, err
	}
	if len(existing) >= directiveMaxPerScope {
		return DirectiveView{}, ErrConflict
	}
	err = s.Repo.Transaction(s.digestCtx(ctx), func(ctx context.Context, tx Tx) error {
		if status == obsProposed {
			if err := proposalCapReached(ctx, tx, r.Scope, colDirectives, directiveProposedMax); err != nil {
				return err
			}
		}
		if err := tx.Insert(ctx, colDirectives, r); err != nil {
			return err
		}
		return audit(ctx, tx, r.Scope, d.PrincipalID, "create:directive", r.ID)
	})
	if err != nil {
		return DirectiveView{}, err
	}
	return viewDirective(r), nil
}

func (s *Service) getDirective(ctx context.Context, get func(context.Context, string, Scope, string) (Record, error), scope Scope, id string) (Record, error) {
	r, err := get(ctx, colDirectives, projectScope(scope), id)
	if err != nil {
		return r, err
	}
	if r.Directive == nil || r.Kind != directiveKind {
		return Record{}, ErrNotFound
	}
	return r, nil
}

func (s *Service) GetDirective(ctx context.Context, token string, scope Scope, id string) (DirectiveView, error) {
	if !validRunLabel(id, 256) {
		return DirectiveView{}, ErrInvalid
	}
	if _, err := s.wikiAuthorize(ctx, token, "read:"+colDirectives, scope, id, ""); err != nil {
		return DirectiveView{}, err
	}
	r, err := s.getDirective(ctx, s.Repo.Get, scope, id)
	if err != nil {
		return DirectiveView{}, err
	}
	r, err = s.resolveDirectiveSource(ctx, r)
	if err != nil {
		return DirectiveView{}, err
	}
	return viewDirective(r), nil
}

// UpdateDirective replaces every field; expected_revision guards against a concurrent edit.
// A person saving a proposed directive approves it (active unless they turn it off).
func (s *Service) UpdateDirective(ctx context.Context, token, id string, in DirectiveInput) (DirectiveView, error) {
	if err := validDirectiveInput(in); err != nil || in.ExpectedRevision < 1 || !validRunLabel(id, 256) {
		return DirectiveView{}, cmp.Or(err, ErrInvalid)
	}
	d, err := s.wikiAuthorize(ctx, token, "write:"+colDirectives, in.Scope, id, "", roleHuman)
	if err != nil {
		return DirectiveView{}, err
	}
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := s.getDirective(ctx, tx.Get, in.Scope, id)
		if err != nil {
			return err
		}
		if r.Revision != in.ExpectedRevision {
			return ErrConflict
		}
		prev := r.Revision
		r.Revision++
		r.UpdatedAt = time.Now().UTC()
		r.Text, r.Tags, r.Status = strings.TrimSpace(in.Rule), in.Tags, directiveStatus(in.Active)
		r.Directive = &Directive{Title: strings.TrimSpace(in.Title), Priority: in.Priority, AppliesTo: in.AppliesTo}
		// An explicit human edit establishes an independent rule.
		r.Directive.Independent = r.Proposal != nil
		if err = tx.Replace(ctx, colDirectives, r, prev); err != nil {
			return err
		}
		out = r
		return audit(ctx, tx, r.Scope, d.PrincipalID, "update:directive", r.ID)
	})
	if err != nil {
		return DirectiveView{}, err
	}
	return viewDirective(out), nil
}

func (s *Service) DeleteDirective(ctx context.Context, token, id string, in DirectiveDeleteInput) error {
	if in.ExpectedRevision < 1 || !validRunLabel(id, 256) {
		return ErrInvalid
	}
	d, err := s.wikiAuthorize(ctx, token, "write:"+colDirectives, in.Scope, id, "", roleHuman)
	if err != nil {
		return err
	}
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := s.getDirective(ctx, tx.Get, in.Scope, id)
		if err != nil {
			return err
		}
		if r.Revision != in.ExpectedRevision {
			return ErrConflict
		}
		if err = tx.Delete(ctx, colDirectives, r.Scope, r.ID, r.Revision); err != nil {
			return err
		}
		return audit(ctx, tx, r.Scope, d.PrincipalID, "delete:directive", r.ID)
	})
}

// ListDirectives pages a scope's directives by priority (highest first). cursor is an offset.
func (s *Service) ListDirectives(ctx context.Context, token string, scope Scope, limit int, cursor string) (DirectiveList, error) {
	out := DirectiveList{Items: []DirectiveView{}}
	offset, ok := parseOffsetCursor(cursor, directiveMaxPerScope-1)
	if !ok {
		return out, ErrInvalid
	}
	if limit == 0 {
		limit = directiveListDefault
	}
	if limit < 1 || limit > directiveMaxPerScope {
		return out, ErrInvalid
	}
	if _, err := s.wikiAuthorize(ctx, token, "read:"+colDirectives, scope, "", ""); err != nil {
		return out, err
	}
	rows, err := s.directives(ctx, scope, "")
	if err != nil {
		return out, err
	}
	page, next := offsetPage(rows, offset, limit)
	for _, r := range page {
		out.Items = append(out.Items, viewDirective(r))
	}
	out.NextCursor = next
	return out, nil
}

// directives reads a scope's directives, highest priority first. A non-empty
// target keeps only active ones that apply to it ("reflect" or "context").
// Callers authorize first.
func (s *Service) directives(ctx context.Context, scope Scope, target string) ([]Record, error) {
	rows, err := s.Repo.ListRecords(ctx, projectScope(scope), colDirectives, directiveMaxPerScope)
	if err != nil {
		return nil, err
	}
	out := rows[:0:0]
	for _, r := range rows {
		if r.Directive == nil || r.Kind != directiveKind {
			continue
		}
		if target != "" && (r.Status != "active" || r.Directive.AppliesTo != target && r.Directive.AppliesTo != "both") {
			continue
		}
		r, err = s.resolveDirectiveSource(ctx, r)
		if err != nil {
			return nil, err
		}
		if target != "" && r.Status != "active" {
			continue
		}
		out = append(out, r)
	}
	slices.SortStableFunc(out, func(a, b Record) int {
		if a.Directive.Priority != b.Directive.Priority {
			return b.Directive.Priority - a.Directive.Priority
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

// resolveDirectiveSource preserves linked rules in the management view even when
// their source is no longer usable. Legacy rows without a scope binding require
// human review; searching other users' scopes by ID would weaken isolation.
func (s *Service) resolveDirectiveSource(ctx context.Context, r Record) (Record, error) {
	if r.Proposal == nil || r.Proposal.Collection != colMemories || r.Directive.Independent {
		return r, nil
	}
	d := *r.Directive
	r.Directive = &d
	inactivate := func(state string) (Record, error) {
		d.SourceState = state
		r.Status = "inactive"
		return r, nil
	}
	if d.SourceScope == nil || d.SourceRule == "" {
		return inactivate("needs_review")
	}
	if projectScope(*d.SourceScope) != projectScope(r.Scope) {
		return inactivate("needs_review")
	}
	var memory Record
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		checked := searchReadTx{Tx: tx}
		var err error
		memory, err = checked.Get(ctx, colMemories, *d.SourceScope, r.Proposal.ID)
		if err != nil || !live(memory) || memory.ContentErased {
			return err
		}
		return checkSources(ctx, checked, memory)
	})
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrDenied) || errors.Is(err, ErrConflict) {
		return inactivate("unavailable")
	}
	if err != nil {
		return Record{}, err
	}
	if memory.Scope != *d.SourceScope || memory.ID != r.Proposal.ID || memory.ApprovedBy == "" || memory.Revision < r.Proposal.Revision || !live(memory) || memory.ContentErased || memory.Text != r.Text || ruleOf(memory) != d.SourceRule {
		return inactivate("inactive")
	}
	d.SourceState = "active"
	return r, nil
}

// directivesForTags keeps the untagged directives and the tagged ones sharing a tag with the request.
func directivesForTags(rows []Record, tags []string) []Record {
	return slices.DeleteFunc(slices.Clone(rows), func(r Record) bool {
		return len(r.Tags) > 0 && !slices.ContainsFunc(r.Tags, func(t string) bool { return slices.Contains(tags, t) })
	})
}

// DirectiveRule is one directive as handed to a model or an agent.
type DirectiveRule struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Rule     string `json:"rule"`
	Priority int    `json:"priority"`
}

func directiveRule(r Record) DirectiveRule {
	return DirectiveRule{ID: r.ID, Title: r.Directive.Title, Rule: r.Text, Priority: r.Directive.Priority}
}

// proposalCapReached refuses an agent proposal once a scope holds max open ones of a
// collection. It counts inside the insert's serializable transaction, so two racing
// proposals cannot both take the last slot (one is retried and then sees the other).
func proposalCapReached(ctx context.Context, tx Tx, scope Scope, col string, max int) error {
	lister := listerOf(ctx, tx)
	if lister == nil {
		return ErrUnavailable
	}
	open, err := lister.ListRecordsByStatus(ctx, scope, col, obsProposed, max)
	if err != nil {
		return err
	}
	if len(open) >= max {
		return ErrConflict
	}
	return nil
}
