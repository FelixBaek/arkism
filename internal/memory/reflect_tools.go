package memory

// Read-only tools the reflect model may call between answers. Every tool reuses
// an existing read path, and every record it returns has passed the same
// per-record external-transfer gate as the first search before it reaches a prompt.

import (
	"context"
	"errors"
	"slices"
	"time"
)

const (
	reflectToolRecall       = "recall"
	reflectToolObservations = "observations"
	reflectToolMentalModels = "mental_models"
	reflectToolEntity       = "entity"
	reflectToolExpand       = "expand"
	reflectMentalModelsMax  = 20
	// reflectParallelMax bounds the tools one model reply may ask for.
	reflectParallelMax = 4
)

const reflectToolsText = "\nTOOLS. If the records are not enough, instead of answering reply with exactly one JSON object and nothing else:\n" +
	`{"tool_call": {"name": "<tool>", "args": {...}}}` + "\n" +
	`or, to run up to 4 tools at once, {"tool_calls": [{"name": "<tool>", "args": {...}}, ...]}` + "\n" +
	"- recall {query, from?, to?, causal?}: search approved memories and observations again; from/to (RFC 3339) bound the creation time, causal adds memories one cause/effect link away.\n" +
	"- observations {query}: search approved observations only.\n" +
	"- mental_models {}: read the approved summaries a person keeps for this space.\n" +
	"- entity {name}: read memories that mention a named person, product, place or thing.\n" +
	"- expand {id}: read memories linked to a record id you already have.\n" +
	"Call a tool only when it can add something; answer as soon as the records suffice.\n"

// reflectToolArgs are the only arguments a tool takes; unknown fields are refused.
type reflectToolArgs struct {
	Query  string     `json:"query,omitempty"`
	From   *time.Time `json:"from,omitempty"`
	To     *time.Time `json:"to,omitempty"`
	Causal bool       `json:"causal,omitempty"`
	ID     string     `json:"id,omitempty"`
	Name   string     `json:"name,omitempty"`
}

type reflectToolCall struct {
	Name string          `json:"name"`
	Args reflectToolArgs `json:"args"`
}

// parseReflectToolCalls reads a strict {"tool_call": {...}} or {"tool_calls": [...]} reply
// (calls past reflectParallelMax are dropped); anything else is an answer.
func parseReflectToolCalls(text string) ([]reflectToolCall, bool) {
	var env struct {
		ToolCall  *reflectToolCall  `json:"tool_call"`
		ToolCalls []reflectToolCall `json:"tool_calls"`
	}
	if !decodeModelJSON(text, &env) {
		return nil, false
	}
	if env.ToolCall != nil {
		env.ToolCalls = append(env.ToolCalls, *env.ToolCall)
	}
	return env.ToolCalls[:min(len(env.ToolCalls), reflectParallelMax)], len(env.ToolCalls) > 0
}

// reflectTool runs one tool. A refused or malformed call is reported in the trace for
// the model to see; only a failure of the server itself stops the reflect.
func (s *Service) reflectTool(ctx context.Context, token string, run *reflectRun, call reflectToolCall) (ReflectToolStep, error) {
	step := ReflectToolStep{Tool: call.Name, Args: call.Args, IDs: []string{}}
	var hits []Hit
	var err error
	switch call.Name {
	case reflectToolRecall, reflectToolObservations:
		hits, err = s.reflectRecall(ctx, token, run.in, call)
	case reflectToolMentalModels:
		hits, err = s.reflectMentalModels(ctx, token, run.in.Scope)
	case reflectToolEntity:
		hits, err = s.reflectEntity(ctx, token, run.in.Scope, call.Args.Name)
	case reflectToolExpand:
		hits, err = s.reflectExpand(ctx, token, run, call.Args.ID)
	default:
		step.Error = "unknown_tool"
		return step, nil
	}
	switch {
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrNotFound), errors.Is(err, ErrConflict):
		step.Error = "invalid_arguments"
	case errors.Is(err, ErrDenied):
		step.Error = "denied"
	case err != nil:
		return step, err
	}
	step.IDs = run.add(hits)
	return step, nil
}

func (s *Service) reflectRecall(ctx context.Context, token string, in ReflectInput, call reflectToolCall) ([]Hit, error) {
	search := reflectSearch(in, call.Args.Query)
	search.ExpandCausal = call.Args.Causal
	if call.Args.From != nil || call.Args.To != nil {
		f := SearchFilters{Tags: in.Tags, CreatedFrom: call.Args.From, CreatedTo: call.Args.To}
		search.Filters = &f
	}
	if call.Name == reflectToolObservations {
		search.Kinds = []string{observationKind}
	}
	bundle, err := s.Search(ctx, token, search)
	return bundle.Items, err
}

// reflectExpand follows the relation graph from a memory this reflect already retrieved.
func (s *Service) reflectExpand(ctx context.Context, token string, run *reflectRun, id string) ([]Hit, error) {
	i := slices.IndexFunc(run.hits, func(h Hit) bool { return h.ID == id && h.Collection == colMemories })
	if i < 0 {
		return nil, ErrInvalid
	}
	search := reflectSearch(run.in, run.in.Query)
	search.RelatedTo = &Ref{Collection: colMemories, ID: id, Revision: run.hits[i].Revision}
	bundle, err := s.Search(ctx, token, search)
	return bundle.Items, err
}

// reflectEntity reads the approved memories that mention the best-matching entity.
func (s *Service) reflectEntity(ctx context.Context, token string, scope Scope, name string) ([]Hit, error) {
	if name == "" {
		return nil, ErrInvalid
	}
	list, err := s.ListEntities(ctx, token, EntityListInput{Scope: scope, Query: name, Limit: 1})
	if err != nil || len(list.Items) == 0 {
		return nil, err
	}
	detail, err := s.GetEntity(ctx, token, scope, list.Items[0].ID)
	if err != nil {
		return nil, err
	}
	rows := []Record{}
	for _, m := range detail.Memories {
		if r, err := s.Repo.Get(ctx, colMemories, scope, m.ID); err == nil && live(r) && r.ApprovedBy != "" {
			rows = append(rows, r)
		}
	}
	rows, err = s.transferableMemories(ctx, token, scope, rows)
	hits := make([]Hit, 0, len(rows))
	for _, r := range rows {
		hits = append(hits, searchHit(r, colMemories, reflectToolEntity))
	}
	return hits, err
}

// reflectMentalModels returns the approved summaries whose every citation may go to
// the model; the summary then passes the gate at its citations' highest sensitivity
// with their sources. A summary that fails any check is left out, as search filters a hit.
func (s *Service) reflectMentalModels(ctx context.Context, token string, scope Scope) ([]Hit, error) {
	page, err := s.ListMentalModels(ctx, token, scope, "", reflectMentalModelsMax)
	if err != nil {
		return nil, err
	}
	hits := []Hit{}
	for _, m := range page.Items {
		ok, err := s.mentalModelTransferable(ctx, token, scope, m)
		if err != nil {
			return nil, err
		}
		if ok {
			at := m.Active.At
			hits = append(hits, Hit{ID: m.ID, Collection: colMentalModels, Kind: mentalModelKind, Revision: m.Revision, Text: m.Title + ": " + m.Active.Text, EffectiveAt: &at})
		}
	}
	return hits, nil
}

func (s *Service) mentalModelTransferable(ctx context.Context, token string, scope Scope, m MentalModelView) (bool, error) {
	if m.Active == nil || m.Active.Withheld || len(m.Active.Citations) == 0 {
		return false, nil
	}
	cited := []Record{}
	for _, ref := range m.Active.Citations {
		if r, err := s.Repo.Get(ctx, ref.Collection, scope, ref.ID); err == nil {
			cited = append(cited, r)
		}
	}
	sent, err := s.transferableMemories(ctx, token, scope, cited)
	if err != nil || len(sent) != len(m.Active.Citations) {
		return false, err
	}
	sources := []SourceRef{}
	for _, r := range sent {
		sources = append(sources, r.Sources...)
	}
	_, err = s.authorizeTransfer(ctx, token, "context:read", scope, m.ID, citedSensitivity(sent, m.Active.Citations), wikiDestination, m.Active.Text, sources)
	if errors.Is(err, ErrDenied) || errors.Is(err, ErrInvalid) {
		return false, nil
	}
	return err == nil, err
}
