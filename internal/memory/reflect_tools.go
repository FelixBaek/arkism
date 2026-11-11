package memory

// Read-only tools the reflect model may call between answers. Every tool reuses
// an existing read path, and every record it returns has passed the same
// per-record external-transfer gate as the first search before it reaches a prompt.

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
	reflectToolRecall       = "recall"
	reflectToolObservations = "observations"
	reflectToolMentalModels = "mental_models"
	reflectToolEntity       = "entity"
	reflectToolExpand       = "expand"
	reflectToolReadModels   = "read_mental_models"
	reflectMentalModelsMax  = 20
	reflectExpandIDsMax     = 5
	reflectSnippetRunes     = 300
	// reflectParallelMax bounds the tools one model reply may ask for.
	reflectParallelMax = 4
)

// reflectToolLines describe each tool to the model; reflectToolsText leaves out the ones this reflect excludes.
var reflectToolLines = []struct{ name, text string }{
	{reflectToolRecall, "- recall {query, from?, to?, causal?, max_tokens?, max_chunk_tokens?}: search approved memories and observations again; from/to (RFC 3339) bound the creation time, causal adds memories one cause/effect link away.\n"},
	{reflectToolObservations, "- observations {query, max_tokens?}: search approved observations only.\n"},
	{reflectToolMentalModels, "- mental_models {max_tokens?}: list the approved summaries a person keeps for this space, as short snippets with ids.\n"},
	{reflectToolReadModels, "- read_mental_models {ids, max_tokens?}: read those summaries in full.\n"},
	{reflectToolEntity, "- entity {name}: read memories that mention a named person, product, place or thing.\n"},
	{reflectToolExpand, "- expand {ids, depth?, max_tokens?}: for memory ids you already have, depth memory (default) reads linked memories, chunk the source passages they came from, document the whole source.\n"},
}

func reflectToolsText(run *reflectRun) string {
	var b strings.Builder
	b.WriteString("\nTOOLS. If the records are not enough, instead of answering reply with exactly one JSON object and nothing else:\n" +
		`{"tool_call": {"name": "<tool>", "args": {...}}}` + "\n" +
		`or, to run up to 4 tools at once, {"tool_calls": [{"name": "<tool>", "args": {...}}, ...]}` + "\n")
	for _, t := range reflectToolLines {
		if run.toolExcluded(t.name) == "" {
			b.WriteString(t.text)
		}
	}
	b.WriteString("max_tokens caps what one call returns. Call a tool only when it can add something; answer as soon as the records suffice.\n")
	return b.String()
}

// reflectToolArgs are the only arguments a tool takes; unknown fields are refused.
type reflectToolArgs struct {
	Query  string     `json:"query,omitempty"`
	From   *time.Time `json:"from,omitempty"`
	To     *time.Time `json:"to,omitempty"`
	Causal bool       `json:"causal,omitempty"`
	ID     string     `json:"id,omitempty"`
	Name   string     `json:"name,omitempty"`
	// IDs name mental models (read_mental_models) or memories (expand).
	IDs            []string `json:"ids,omitempty"`
	Depth          string   `json:"depth,omitempty"`
	MaxTokens      int      `json:"max_tokens,omitempty"`
	MaxChunkTokens int      `json:"max_chunk_tokens,omitempty"`
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
	if step.Error = run.toolExcluded(call.Name); step.Error != "" {
		return step, nil
	}
	var hits []Hit
	var err error
	switch call.Name {
	case reflectToolRecall, reflectToolObservations:
		hits, err = s.reflectRecall(ctx, token, run.in, call)
	case reflectToolMentalModels, reflectToolReadModels:
		hits, err = s.reflectMentalModels(ctx, token, run, call)
	case reflectToolEntity:
		hits, err = s.reflectEntity(ctx, token, run.in.Scope, call.Args.Name)
	case reflectToolExpand:
		hits, err = s.reflectExpand(ctx, token, run, call.Args)
	default:
		step.Error = "unknown_tool"
		return step, nil
	}
	hits = packToolHits(hits, run.toolTokens(call), run.chunkTokens(call))
	switch {
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrNotFound), errors.Is(err, ErrConflict):
		step.Error = "invalid_arguments"
	case errors.Is(err, ErrDenied):
		step.Error = "denied"
	case err != nil:
		return step, err
	}
	walked := call.Name == reflectToolEntity || call.Name == reflectToolExpand && (call.Args.Depth == "" || call.Args.Depth == "memory")
	step.IDs = run.add(hits, walked)
	if run.in.Include != nil && run.in.Include.ToolCalls.Output {
		step.Output = hitTexts(hits, run)
	}
	return step, nil
}

// toolExcluded is why this reflect may not call a tool ("excluded"), or "".
func (r *reflectRun) toolExcluded(name string) string {
	mm := name == reflectToolMentalModels || name == reflectToolReadModels
	if mm && r.in.ExcludeMentalModels || name == reflectToolObservations && !factTypeKept(r.in.FactTypes, colObservations) {
		return "excluded"
	}
	return ""
}

// toolTokens is the tool's token cap: the call's max_tokens, else the space's, else the shipped default.
func (r *reflectRun) toolTokens(call reflectToolCall) int {
	if n := call.Args.MaxTokens; n > 0 {
		return min(max(n, 64), reflectMaxPromptTokens)
	}
	t := r.tools
	switch call.Name {
	case reflectToolRecall:
		return t.RecallMaxTokens
	case reflectToolObservations:
		return t.ObservationsMaxTokens
	case reflectToolMentalModels:
		return t.MentalModelsMaxTokens
	case reflectToolReadModels:
		return t.ReadMentalModelsMaxTokens
	}
	return t.ExpandMaxTokens
}

func (r *reflectRun) chunkTokens(call reflectToolCall) int {
	if n := call.Args.MaxChunkTokens; n > 0 {
		return min(max(n, 16), reflectMaxPromptTokens)
	}
	if call.Name == reflectToolRecall || call.Name == reflectToolExpand {
		return r.tools.RecallMaxChunkTokens
	}
	return 0
}

// approxTokens counts about three characters a token, as the reflect prompt cap does.
func approxTokens(text string) int { return utf8.RuneCountInString(text)/3 + 1 }

// packToolHits cuts each text to chunk tokens (0 keeps it whole) and keeps hits, in order, while they fit in budget.
func packToolHits(hits []Hit, budget, chunk int) []Hit {
	out, used := []Hit{}, 0
	for _, h := range hits {
		if chunk > 0 && approxTokens(h.Text) > chunk {
			h.Text = string([]rune(h.Text)[:chunk*3])
		}
		n := approxTokens(h.Text)
		if budget > 0 && used+n > budget {
			continue // a smaller later hit may still fit
		}
		used += n
		out = append(out, h)
	}
	return out
}

// hitTexts is what a tool returned, for the caller's trace (include.tool_calls.output); fact types already applied.
func hitTexts(hits []Hit, run *reflectRun) []ReflectToolOutput {
	out := []ReflectToolOutput{}
	for _, h := range hits {
		if h.Collection == colMentalModels || factTypeKept(run.in.FactTypes, h.Collection) {
			out = append(out, ReflectToolOutput{ID: h.ID, Kind: h.Kind, Text: h.Text})
		}
	}
	return out
}

func (s *Service) reflectRecall(ctx context.Context, token string, in ReflectInput, call reflectToolCall) ([]Hit, error) {
	search := reflectSearch(in, call.Args.Query)
	search.ExpandCausal = call.Args.Causal
	if call.Args.From != nil || call.Args.To != nil {
		f := SearchFilters{}
		if search.Filters != nil {
			f = *search.Filters
		}
		// The request's own window wins over the tool's.
		f.CreatedFrom, f.CreatedTo = cmp.Or(f.CreatedFrom, call.Args.From), cmp.Or(f.CreatedTo, call.Args.To)
		search.Filters = &f
	}
	if call.Name == reflectToolObservations {
		search.Kinds = []string{observationKind}
	}
	bundle, err := s.Search(ctx, token, search)
	return bundle.Items, err
}

// reflectExpand reads around memories this reflect already retrieved: linked memories
// (depth memory, the relation graph), their source passages (chunk) or whole sources (document).
func (s *Service) reflectExpand(ctx context.Context, token string, run *reflectRun, args reflectToolArgs) ([]Hit, error) {
	ids := args.IDs
	if args.ID != "" {
		ids = append([]string{args.ID}, ids...)
	}
	if len(ids) == 0 || len(ids) > reflectExpandIDsMax || !slices.Contains([]string{"", "memory", "chunk", "document"}, args.Depth) {
		return nil, ErrInvalid
	}
	out := []Hit{}
	for _, id := range ids {
		i := slices.IndexFunc(run.hits, func(h Hit) bool { return h.ID == id && h.Collection == colMemories })
		if i < 0 {
			return nil, ErrInvalid
		}
		var hits []Hit
		var err error
		if args.Depth == "" || args.Depth == "memory" {
			search := reflectSearch(run.in, run.in.Query)
			search.RelatedTo = &Ref{Collection: colMemories, ID: id, Revision: run.hits[i].Revision}
			var bundle ContextBundle
			bundle, err = s.Search(ctx, token, search)
			hits = bundle.Items
		} else {
			hits, err = s.reflectSources(ctx, token, run.in.Scope, id, args.Depth == "document")
		}
		if err != nil {
			return nil, err
		}
		out = append(out, hits...)
	}
	return out, nil
}

// reflectSources reads a memory's source passages (the spans it was taken from) or, for
// document, each whole source. A whole source passes the transfer gate on its own.
func (s *Service) reflectSources(ctx context.Context, token string, scope Scope, id string, document bool) ([]Hit, error) {
	trace, err := s.traceRecordSources(ctx, token, scope, id, colMemories, 8)
	if err != nil {
		return nil, err
	}
	mem, err := s.Repo.Get(ctx, colMemories, scope, id)
	if err != nil {
		return nil, err
	}
	hits, kind := []Hit{}, "source_chunk"
	if document {
		kind = "source_document"
	}
	for _, src := range trace.Sources {
		text := src.Excerpt
		if document && src.Source != nil {
			text = s.transferableSource(ctx, token, scope, mem, *src.Source)
		}
		if text == "" || containsSecret(text) {
			continue
		}
		hits = append(hits, Hit{ID: "src_" + src.SourceID + ":" + id, Collection: "sources", Kind: kind, Text: redactSessionPrivate(text)})
	}
	return hits, nil
}

func (s *Service) transferableSource(ctx context.Context, token string, scope Scope, mem Record, ref Ref) string {
	src, err := s.Repo.Get(ctx, ref.Collection, scope, ref.ID)
	if err != nil || src.ContentErased || src.PrivacyQuarantined {
		return ""
	}
	if _, err := s.authorizeTransfer(ctx, token, "context:read", scope, src.ID, mem.Sensitivity, wikiDestination, src.Text, mem.Sources); err != nil {
		return ""
	}
	return src.Text
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
// mental_models returns snippets, read_mental_models the named summaries in full;
// exclude_mental_model_ids are never returned.
func (s *Service) reflectMentalModels(ctx context.Context, token string, run *reflectRun, call reflectToolCall) ([]Hit, error) {
	read := call.Name == reflectToolReadModels
	if read && (len(call.Args.IDs) == 0 || len(call.Args.IDs) > reflectMentalModelsMax) {
		return nil, ErrInvalid
	}
	page, err := s.ListMentalModels(ctx, token, run.in.Scope, "", "", reflectMentalModelsMax)
	if err != nil {
		return nil, err
	}
	hits := []Hit{}
	for _, m := range page.Items {
		if slices.Contains(run.in.ExcludeMentalModelIDs, m.ID) || read && !slices.Contains(call.Args.IDs, m.ID) {
			continue
		}
		ok, err := s.mentalModelTransferable(ctx, token, run.in.Scope, m)
		if err != nil {
			return nil, err
		}
		if ok {
			at, text := m.Active.At, m.Active.Text
			if !read {
				text = runeCutString(text, reflectSnippetRunes)
			}
			hits = append(hits, Hit{ID: m.ID, Collection: colMentalModels, Kind: mentalModelKind, Revision: m.Revision, Text: m.Title + ": " + text, EffectiveAt: &at})
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
