package memory

import (
	"context"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Extraction labels a claim may carry beyond its kind. Each one only marks the
// candidate; a person still approves it, and only that approval acts on a label:
// a rule becomes a directive, stated relations become knowledge graph edges, and
// a temporary request keeps the expiry it was captured with.

const (
	ruleTagPrefix           = "rule:"
	decisionStatusTagPrefix = "decision_status:"
	temporaryTag            = "temporary"
	// temporaryCandidateTTL is the default life of a "this time only" request: the
	// candidate expires unreviewed after it, and an approved memory keeps the same end date.
	temporaryCandidateTTL = 7 * 24 * time.Hour
	entityRelationsMax    = 8
	ruleDirectivePriority = 50
)

var (
	claimRules            = []string{"always", "never"}
	claimDecisionStatuses = []string{"proposed", "confirmed", "dropped"}
	// temporaryWords are request words that limit a statement to now, in the extractor's
	// languages; the extractor's own temporary flag covers the rest. "temporarily" is left out:
	// it mostly describes a lasting fact ("when the DB is temporarily down, retry").
	temporaryWords = regexp.MustCompile(`(?i)이번만|이번 한 ?번만|이번에만|오늘만|당분간|일단 지금은|this time only|just this once|only this time|for now\b|for today only`)
	// temporaryKinds are the request kinds the words apply to: in a lesson, decision or event
	// "this time only" describes what happened, and expiring it would lose a lasting memory.
	temporaryKinds = []string{"user", "procedural"}
)

// EntityRelationInput is one relation the caller states between two entity names of
// the same claim (role, team, reports-to). It becomes a graph edge only when a person
// approves the memory.
type EntityRelationInput struct {
	Source   string `json:"source" bson:"source"`
	Relation string `json:"relation" bson:"relation"`
	Target   string `json:"target" bson:"target"`
}

// validClaimExtras checks the extraction labels: a known rule or status, a status only on
// a decision, and relations whose ends are names the claim states.
func validClaimExtras(c CaptureClaim) bool {
	if c.Rule != "" && !slices.Contains(claimRules, c.Rule) || c.DecisionStatus != "" && (c.Kind != "decision" || !slices.Contains(claimDecisionStatuses, c.DecisionStatus)) ||
		len(c.Relations) > entityRelationsMax {
		return false
	}
	stated := func(name string) bool {
		return slices.ContainsFunc(c.Entities, func(e EntityInput) bool { return entityKey(e.Name) == entityKey(name) })
	}
	for _, r := range c.Relations {
		if !graphLabel.MatchString(r.Relation) || containsSecret(r.Relation) || !stated(r.Source) || !stated(r.Target) || entityKey(r.Source) == entityKey(r.Target) {
			return false
		}
	}
	return true
}

// claimTemporary: the extractor said so, or a request's own words limit it to now.
func claimTemporary(c CaptureClaim) bool {
	return c.Temporary || slices.Contains(temporaryKinds, c.Kind) && temporaryWords.MatchString(c.Text)
}

// claimExtraTags are the system tags of the extraction labels (callers' plain tags cannot hold ':').
func claimExtraTags(c CaptureClaim) []string {
	var tags []string
	if c.Rule != "" {
		tags = append(tags, ruleTagPrefix+c.Rule)
	}
	if c.DecisionStatus != "" {
		tags = append(tags, decisionStatusTagPrefix+c.DecisionStatus)
	}
	if claimTemporary(c) {
		tags = append(tags, temporaryTag)
	}
	return tags
}

// claimExpiry is a temporary request's default end date, else nil.
func claimExpiry(c CaptureClaim, now time.Time) *time.Time {
	if !claimTemporary(c) {
		return nil
	}
	at := now.UTC().Add(temporaryCandidateTTL).Truncate(time.Millisecond)
	return &at
}

// ruleOf is the rule tag's value of a record, or "".
func ruleOf(r Record) string {
	for _, t := range r.Tags {
		if v, ok := strings.CutPrefix(t, ruleTagPrefix); ok && slices.Contains(claimRules, v) {
			return v
		}
	}
	return ""
}

// ruleDirective turns an approved rule memory into the active directive a person's
// approval asks for. It runs in the approval transaction, so both land or neither does.
func ruleDirective(ctx context.Context, tx Tx, memory Record, d Decision) error {
	rule := ruleOf(memory)
	if rule == "" || d.Role != roleHuman {
		return nil
	}
	scope := projectScope(memory.Scope)
	if e := checkDirectiveCapacity(ctx, tx, scope, d.PrincipalID, ""); e != nil {
		return e
	}

	title := ""
	if len(memory.Keywords) > 0 {
		title = strings.TrimSpace(memory.Keywords[0])
	}
	if title == "" || len([]rune(title)) > directiveTitleMaxRunes {
		title = map[string]string{"always": "항상 지킬 규칙", "never": "하지 말 것"}[rule]
	}
	// The directive meets every check a directive created by hand meets (length, private span, secret).
	in := DirectiveInput{Scope: scope, Title: title, Rule: memory.Text, Priority: ruleDirectivePriority, AppliesTo: "both"}
	if err := validDirectiveInput(in); err != nil {
		return err
	}
	now := time.Now().UTC()
	dir := Record{ID: ID(), Scope: scope, Kind: directiveKind, Status: "active", Revision: 1, Text: strings.TrimSpace(in.Rule), Tags: []string{},
		Directive: &Directive{Title: in.Title, Priority: in.Priority, AppliesTo: in.AppliesTo, SourceScope: &memory.Scope, SourceRule: ruleOf(memory)}, Proposal: &Ref{Collection: colMemories, ID: memory.ID, Revision: memory.Revision},
		CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role}
	if err := tx.Insert(ctx, colDirectives, dir); err != nil {
		return err
	}
	return audit(ctx, tx, scope, d.PrincipalID, "create:directive", dir.ID)
}

// applyEntityRelations writes the relations a person approved with a memory as graph
// edges between the memory's named entities, the memory as their evidence. Best effort,
// like the entity pass before it: an approval never fails on it.
func (s *Service) applyEntityRelations(ctx context.Context, actor string, r Record) {
	kg, ok := s.Repo.(knowledgeGraphStore)
	store, err := s.entityStore()
	if len(r.EntityRelations) == 0 || r.ApprovedBy == "" || !ok || err != nil {
		return
	}
	edges, err := approvedEntityRelations(ctx, store, r)
	if err != nil {
		slog.Warn("entity relations after approval: graph unreadable", "memory", r.ID, "error", err)
		return
	}
	for _, edge := range edges {
		edge.AssertedBy = actor
		if err := kg.PutKnowledgeRelation(ctx, edge); err != nil {
			slog.Warn("entity relation after approval not stored", "memory", r.ID, "error", err)
		}
	}
}

func approvedEntityRelations(ctx context.Context, store namedEntityStore, r Record) ([]KnowledgeRelation, error) {
	g, err := loadEntityGraph(ctx, store, r.Scope)
	if err != nil {
		return nil, err
	}
	if g.truncated {
		return nil, ErrUnavailable
	}
	var edges []KnowledgeRelation
	// A name alone must identify exactly one live endpoint. A unique explicit
	// type supplied by the approved memory can narrow it; never choose by order.
	node := func(name string) (GraphEntity, bool, error) {
		types := map[string]bool{}
		for _, e := range r.Entities {
			if entityKey(e.Name) == entityKey(name) && e.Type != "" && e.Type != entityTypeConcept {
				types[e.Type] = true
			}
		}
		// Extraction can reuse a pre-existing generic concept for a stated
		// type. Prefer exact typed nodes; otherwise preserve only an unambiguous
		// generic match, as the extraction resolver already does.
		exact := false
		for _, c := range g.byKey[entityKey(name)] {
			if !c.Deleted && types[c.Type] {
				exact = true
			}
		}
		var found GraphEntity
		for _, c := range g.byKey[entityKey(name)] {
			if c.Deleted || len(types) == 1 && (exact && !types[c.Type] || !exact && c.Type != entityTypeConcept) {
				continue
			}
			if found.ID != "" && found.ID != c.ID {
				return GraphEntity{}, false, ErrConflict
			}
			found = GraphEntity{Kind: namedEntityKind, ID: c.ID}
		}
		return found, found.ID != "", nil
	}
	for _, rel := range r.EntityRelations {
		src, okS, e := node(rel.Source)
		if e != nil {
			return nil, e
		}
		dst, okT, e := node(rel.Target)
		if e != nil {
			return nil, e
		}
		if !okS || !okT || src == dst {
			continue
		}
		edge := KnowledgeRelation{KnowledgeRelationInput: KnowledgeRelationInput{Scope: r.Scope, Source: src, Target: dst, Relation: rel.Relation,
			Evidence: Ref{Collection: colMemories, ID: r.ID, Revision: r.Revision}}}
		edges = append(edges, edge)
	}
	return edges, nil
}
