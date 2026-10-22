package memory

import (
	"context"
	"errors"
	"regexp"
)

// Graph relations are explicit human assertions supported by approved memory,
// not inferred facts. Opaque node IDs avoid copying names into a second store.
type GraphEntity struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type KnowledgeRelationInput struct {
	Scope    Scope       `json:"scope"`
	Source   GraphEntity `json:"source"`
	Target   GraphEntity `json:"target"`
	Relation string      `json:"relation"`
	Evidence Ref         `json:"evidence"`
}
type KnowledgeRelation struct {
	KnowledgeRelationInput
	AssertedBy string `json:"asserted_by"`
}
type KnowledgeGraphInput struct {
	Scope    Scope       `json:"scope"`
	Start    GraphEntity `json:"start"`
	MaxDepth int         `json:"max_depth"`
	Limit    int         `json:"limit"`
}
type KnowledgeGraphResult struct {
	Items     []KnowledgeRelation `json:"items"`
	Authority string              `json:"authority"`
	Truncated bool                `json:"truncated"`
}
type knowledgeGraphStore interface {
	PutKnowledgeRelation(context.Context, KnowledgeRelation) error
	KnowledgeRelations(context.Context, Scope, GraphEntity, int) ([]KnowledgeRelation, error)
}

var graphLabel = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var graphID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func validGraphEntity(e GraphEntity) bool {
	return graphLabel.MatchString(e.Kind) && graphID.MatchString(e.ID) && !containsSecret(e.ID)
}
func validKnowledgeEvidence(r Record, ref Ref, scope Scope) bool {
	return r.Scope == scope && r.ID == ref.ID && r.Revision == ref.Revision && live(r) && !r.ContentErased && r.ApprovedBy != "" && len(r.Sources) > 0
}
func (s *Service) PutKnowledgeRelation(ctx context.Context, token string, in KnowledgeRelationInput) (KnowledgeRelation, error) {
	out := KnowledgeRelation{}
	if !validScope(in.Scope) || !validGraphEntity(in.Source) || !validGraphEntity(in.Target) || in.Source == in.Target || !graphLabel.MatchString(in.Relation) || in.Evidence.Collection != colMemories || !validRunLabel(in.Evidence.ID, 256) || in.Evidence.Revision < 1 {
		return out, ErrInvalid
	}
	actor, err := s.authorize(ctx, token, "write:memory_relations", in.Scope, "", structuredText(in))
	if err != nil {
		return out, err
	}
	if actor.Role != roleHuman || !actor.ContentSafe {
		return out, ErrDenied
	}
	ctx = context.WithValue(ctx, requestActorKey{}, actor)
	evidence, err := s.Get(ctx, token, colMemories, in.Scope, in.Evidence.ID)
	if err != nil {
		return out, err
	}
	if !validKnowledgeEvidence(evidence, in.Evidence, in.Scope) {
		return out, ErrConflict
	}
	out = KnowledgeRelation{KnowledgeRelationInput: in, AssertedBy: actor.PrincipalID}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		store, ok := tx.(knowledgeGraphStore)
		if !ok {
			return ErrUnavailable
		}
		checked := searchReadTx{Tx: tx}
		current, e := checked.Get(ctx, colMemories, in.Scope, in.Evidence.ID)
		if e != nil {
			return e
		}
		if !validKnowledgeEvidence(current, in.Evidence, in.Scope) || searchHitHash(HitFromRecord(colMemories, current)) != searchHitHash(HitFromRecord(colMemories, evidence)) {
			return ErrConflict
		}
		if e = checkSources(ctx, checked, current); e != nil {
			return e
		}
		if e = store.PutKnowledgeRelation(ctx, out); e != nil {
			return e
		}
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "create:knowledge_relation", ContentHash(structuredText(in)))
	})
	if err != nil {
		return KnowledgeRelation{}, err
	}
	return out, nil
}

// Expansion is directed, breadth-first and bounded to three hops and fifty
// returned edges. Each inspected edge requires readable, live evidence; denied
// nodes cannot become a bridge into otherwise accessible parts of the graph.
func (s *Service) ExpandKnowledgeGraph(ctx context.Context, token string, in KnowledgeGraphInput) (KnowledgeGraphResult, error) {
	out := KnowledgeGraphResult{Items: []KnowledgeRelation{}, Authority: AuthorityDataOnly}
	if !validKnowledgeGraphInput(in) {
		return out, ErrInvalid
	}
	if in.MaxDepth == 0 {
		in.MaxDepth = 1
	}
	if in.Limit == 0 {
		in.Limit = 20
	}
	actor, err := s.authorize(ctx, token, "read:memory_relations", in.Scope, "", structuredText(in))
	if err != nil {
		return out, err
	}
	ctx = context.WithValue(ctx, requestActorKey{}, actor)
	store, ok := s.Repo.(knowledgeGraphStore)
	if !ok {
		return out, ErrUnavailable
	}
	finish := func() (KnowledgeGraphResult, error) {
		// Authorizers may use the same one-connection database pool. Recheck
		// before opening the transaction, then fence these exact snapshots.
		snapshots := map[Ref]string{}
		for _, edge := range out.Items {
			r, e := s.Repo.Get(ctx, colMemories, in.Scope, edge.Evidence.ID)
			if e != nil {
				return KnowledgeGraphResult{}, e
			}
			if !validKnowledgeEvidence(r, edge.Evidence, in.Scope) {
				return KnowledgeGraphResult{}, ErrConflict
			}
			if _, e = s.authorize(ctx, token, "read:memories", in.Scope, r.ID, structuredText(r)); e != nil {
				return KnowledgeGraphResult{}, e
			}
			snapshots[edge.Evidence] = searchHitHash(HitFromRecord(colMemories, r))
		}
		err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			checked := searchReadTx{Tx: tx}
			for _, edge := range out.Items {
				r, e := checked.Get(ctx, colMemories, in.Scope, edge.Evidence.ID)
				if e != nil {
					return e
				}
				if !validKnowledgeEvidence(r, edge.Evidence, in.Scope) || searchHitHash(HitFromRecord(colMemories, r)) != snapshots[edge.Evidence] {
					return ErrConflict
				}
				if e = checkSources(ctx, checked, r); e != nil {
					return e
				}
			}
			return audit(ctx, tx, in.Scope, actor.PrincipalID, "read:knowledge_graph", ContentHash(structuredText(in)))
		})
		if err != nil {
			return KnowledgeGraphResult{}, err
		}
		return out, nil
	}
	frontier := []GraphEntity{in.Start}
	seen := map[GraphEntity]bool{in.Start: true}
	// Hard scan bound is independent of authorized result count.
	inspected := 0
	for depth := 0; depth < in.MaxDepth && len(frontier) > 0; depth++ {
		next := []GraphEntity{}
		for _, node := range frontier {
			remaining := 200 - inspected
			if remaining <= 0 {
				out.Truncated = true
				return finish()
			}
			edges, e := store.KnowledgeRelations(ctx, in.Scope, node, remaining+1)
			if e != nil {
				return KnowledgeGraphResult{}, e
			}
			if len(edges) > remaining {
				edges = edges[:remaining]
				out.Truncated = true
			}
			inspected += len(edges)
			for _, edge := range edges {
				if !knowledgeEdgeCandidate(edge, in.Scope, node) {
					continue
				}
				ok, e := s.readableKnowledgeEvidence(ctx, token, in.Scope, edge)
				if e != nil {
					return KnowledgeGraphResult{}, e
				}
				if !ok {
					continue
				}
				if len(out.Items) == in.Limit {
					out.Truncated = true
					return finish()
				}
				out.Items = append(out.Items, edge)
				if !seen[edge.Target] {
					seen[edge.Target] = true
					next = append(next, edge.Target)
				}
			}
		}
		frontier = next
	}
	return finish()
}
func validKnowledgeGraphInput(in KnowledgeGraphInput) bool {
	return validScope(in.Scope) && validGraphEntity(in.Start) && in.MaxDepth >= 0 && in.MaxDepth <= 3 && in.Limit >= 0 && in.Limit <= 50
}
func knowledgeEdgeCandidate(edge KnowledgeRelation, scope Scope, node GraphEntity) bool {
	return edge.Scope == scope && edge.Source == node && validGraphEntity(edge.Target) && edge.Evidence.Collection == colMemories
}

// readableKnowledgeEvidence skips evidence the caller cannot read instead of failing the expansion.
func (s *Service) readableKnowledgeEvidence(ctx context.Context, token string, scope Scope, edge KnowledgeRelation) (bool, error) {
	r, e := s.Get(ctx, token, colMemories, scope, edge.Evidence.ID)
	if e != nil {
		if errors.Is(e, ErrDenied) || errors.Is(e, ErrNotFound) || errors.Is(e, ErrConflict) {
			return false, nil
		}
		return false, e
	}
	return validKnowledgeEvidence(r, edge.Evidence, scope), nil
}
