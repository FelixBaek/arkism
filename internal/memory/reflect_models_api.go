package memory

// Routes of the reflect-models package that are not a model call: observation
// scopes, the consolidation strategy preview, a person's bulk delete of
// observations, and the prompt preview. None of them calls a model.

import (
	"context"
	"slices"
	"strings"
	"time"
)

const (
	obsScopesMax       = 200
	strategySamplesMax = 5
	promptPlaceholder  = "[memory text]"
)

// ObservationScopeCount is one tag set observations are kept under (empty: the untagged scope).
type ObservationScopeCount struct {
	Tags     []string `json:"tags"`
	Proposed int      `json:"proposed"`
	Active   int      `json:"active"`
}

type ObservationScopeList struct {
	Items     []ObservationScopeCount `json:"items"`
	Truncated bool                    `json:"truncated,omitempty"`
	Authority string                  `json:"authority"`
}

// ListObservationScopes lists the tag scopes of proposed and active observations with counts.
func (s *Service) ListObservationScopes(ctx context.Context, token string, scope Scope) (ObservationScopeList, error) {
	if !validScope(scope) {
		return ObservationScopeList{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colObservations, scope, "", ""); err != nil {
		return ObservationScopeList{}, err
	}
	out := ObservationScopeList{Items: []ObservationScopeCount{}, Authority: AuthorityDataOnly}
	index := map[string]int{}
	for _, status := range []string{obsProposed, obsActive} {
		rows, err := s.Repo.ListRecordsByStatus(ctx, scope, colObservations, status, obsListLimit)
		if err != nil {
			return ObservationScopeList{}, err
		}
		out.Truncated = out.Truncated || len(rows) == obsListLimit
		for _, r := range rows {
			tags := obsScopeTags(r.Tags)
			i, seen := index[obsScopeKey(tags)]
			if !seen {
				if len(out.Items) == obsScopesMax {
					out.Truncated = true
					continue
				}
				i, index[obsScopeKey(tags)] = len(out.Items), len(out.Items)
				out.Items = append(out.Items, ObservationScopeCount{Tags: append([]string{}, tags...)})
			}
			if status == obsActive {
				out.Items[i].Active++
			} else {
				out.Items[i].Proposed++
			}
		}
	}
	return out, nil
}

// StrategyPreviewInput previews strategies over the scope's approved memories: the given
// consolidation block, or the space's when it is left out.
type StrategyPreviewInput struct {
	Scope         Scope               `json:"scope"`
	Consolidation *SpaceConsolidation `json:"consolidation,omitempty"`
}

type StrategyPreviewSample struct {
	Tags      []string `json:"tags"`
	Count     int      `json:"count"`
	HandledBy string   `json:"handled_by,omitempty"`
}

type StrategyPreviewRule struct {
	Matched int                     `json:"matched"`
	Taken   int                     `json:"taken"`
	Samples []StrategyPreviewSample `json:"samples"`
}

type StrategyPreviewItem struct {
	Name    string                `json:"name"`
	Active  bool                  `json:"active"`
	Claimed int                   `json:"claimed"`
	Rules   []StrategyPreviewRule `json:"rules"`
}

type StrategyPreview struct {
	Scopes     int                   `json:"scopes"`
	Unclaimed  int                   `json:"unclaimed"`
	Strategies []StrategyPreviewItem `json:"strategies"`
	Authority  string                `json:"authority"`
}

// PreviewConsolidationStrategies shows which observation scopes each strategy rule
// matches and which it takes (the first claiming strategy wins). No model, no write.
func (s *Service) PreviewConsolidationStrategies(ctx context.Context, token string, in StrategyPreviewInput) (StrategyPreview, error) {
	if !validScope(in.Scope) || !in.Consolidation.valid() {
		return StrategyPreview{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colObservations, in.Scope, "", ""); err != nil {
		return StrategyPreview{}, err
	}
	block := in.Consolidation
	if block == nil {
		c, err := s.spaceSettings(ctx, in.Scope)
		if err != nil {
			return StrategyPreview{}, err
		}
		block = c.Consolidation
	}
	members, err := s.approvedMemories(ctx, token, in.Scope, "")
	if err != nil {
		return StrategyPreview{}, err
	}
	scopes := block.partition(members)
	out := StrategyPreview{Scopes: len(scopes), Strategies: []StrategyPreviewItem{}, Authority: AuthorityDataOnly}
	if block == nil {
		out.Unclaimed = len(scopes)
		return out, nil
	}
	for i, st := range block.Strategies {
		out.Strategies = append(out.Strategies, previewStrategy(block, i, st, scopes))
	}
	for _, sc := range scopes {
		if sc.strategy == nil {
			out.Unclaimed++
		}
	}
	return out, nil
}

func previewStrategy(block *SpaceConsolidation, i int, st ConsolidationStrategy, scopes []obsScope) StrategyPreviewItem {
	item := StrategyPreviewItem{Name: st.Name, Rules: []StrategyPreviewRule{}}
	for j, rule := range st.Scopes {
		r := StrategyPreviewRule{Samples: []StrategyPreviewSample{}}
		for _, sc := range scopes {
			if !rule.matches(sc.tags) {
				continue
			}
			r.Matched++
			// partition already picked each scope's strategy (the first that claims it).
			first := slices.IndexFunc(st.Scopes, func(x ObservationTagRule) bool { return x.matches(sc.tags) })
			if sc.strategy == &block.Strategies[i] && first == j {
				r.Taken++
			}
			if len(r.Samples) < strategySamplesMax {
				sample := StrategyPreviewSample{Tags: sc.tags, Count: len(sc.members)}
				if sc.strategy != nil {
					sample.HandledBy = sc.strategy.Name
				}
				r.Samples = append(r.Samples, sample)
			}
		}
		item.Claimed += r.Taken
		item.Rules = append(item.Rules, r)
	}
	item.Active = item.Claimed > 0
	return item
}

// ObservationDeleteInput names observations to delete, or all of them (optionally one status).
type ObservationDeleteInput struct {
	Scope  Scope    `json:"scope"`
	IDs    []string `json:"ids,omitempty"`
	All    bool     `json:"all,omitempty"`
	Status string   `json:"status,omitempty"`
}

type ObservationDeleteResult struct {
	Deleted []string `json:"deleted"`
	// More: with all, observations remain past this call's bound; call again.
	More bool `json:"more,omitempty"`
}

// DeleteObservations is a person's bulk delete: named ids (up to 50) or all (up to 500 a call).
// Like a review, each one keeps its row and history and is audited: it moves to status deleted,
// which is never recalled, refreshed or counted as covering its memories, so consolidation may
// propose it again. Already deleted ones are skipped.
func (s *Service) DeleteObservations(ctx context.Context, token string, in ObservationDeleteInput) (ObservationDeleteResult, error) {
	if !validScope(in.Scope) || in.All == (len(in.IDs) > 0) || len(in.IDs) > obsBatchMax || in.Status != "" && (!in.All || !slices.Contains(obsLiveStatuses, in.Status)) ||
		slices.ContainsFunc(in.IDs, func(id string) bool { return !validRunLabel(id, 256) }) {
		return ObservationDeleteResult{}, ErrInvalid
	}
	d, err := s.derivedHuman(ctx, token, colObservations, in.Scope, "")
	if err != nil {
		return ObservationDeleteResult{}, err
	}
	ids, more, err := s.observationsToDelete(ctx, in)
	if err != nil {
		return ObservationDeleteResult{}, err
	}
	out := ObservationDeleteResult{Deleted: []string{}, More: more}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out.Deleted = out.Deleted[:0]
		now := time.Now().UTC().Truncate(time.Millisecond)
		for _, id := range ids {
			r, err := tx.Get(ctx, colObservations, in.Scope, id)
			if err != nil {
				return err
			}
			if r.Observation == nil || r.Status == obsDeleted {
				continue
			}
			r.Review = &ReviewDecision{Action: "delete", By: d.PrincipalID, ActorRole: d.Role, At: now}
			if err := replaceObservation(ctx, tx, r, obsDeleted, "delete", d.PrincipalID, now); err != nil {
				return err
			}
			out.Deleted = append(out.Deleted, r.ID)
		}
		return nil
	})
	return out, err
}

// obsLiveStatuses are the statuses a bulk delete takes (every one but deleted).
var obsLiveStatuses = []string{obsProposed, obsActive, obsRejected, obsStale}

// observationsToDelete is the named ids, or up to obsListLimit listed ids and whether more remain.
func (s *Service) observationsToDelete(ctx context.Context, in ObservationDeleteInput) ([]string, bool, error) {
	if !in.All {
		return slices.Compact(slices.Sorted(slices.Values(in.IDs))), false, nil
	}
	statuses := obsLiveStatuses
	if in.Status != "" {
		statuses = []string{in.Status}
	}
	ids := []string{}
	for _, status := range statuses {
		rows, err := s.Repo.ListRecordsByStatus(ctx, in.Scope, colObservations, status, obsListLimit-len(ids)+1)
		if err != nil {
			return nil, false, err
		}
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		if len(ids) > obsListLimit {
			return ids[:obsListLimit], true, nil
		}
	}
	return ids, false, nil
}

// PromptPreviewInput renders one operation's prompt with placeholders for the data.
type PromptPreviewInput struct {
	Scope     Scope    `json:"scope"`
	Operation string   `json:"operation"`
	Tags      []string `json:"tags,omitempty"`
}

type PromptPreview struct {
	Operation string `json:"operation"`
	System    string `json:"system"`
	User      string `json:"user"`
	Authority string `json:"authority"`
}

// PreviewPrompt renders the retain (extraction), consolidation or reflect prompt the
// space would send, with the same builders as the real calls and bracketed placeholders
// for the data. It reads settings and directives only; no record text, no model call.
func (s *Service) PreviewPrompt(ctx context.Context, token string, in PromptPreviewInput) (PromptPreview, error) {
	if !validScope(in.Scope) || !validTags(in.Tags) || !slices.Contains([]string{"retain", "consolidation", "reflect"}, in.Operation) {
		return PromptPreview{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "search:memories", in.Scope, "", ""); err != nil {
		return PromptPreview{}, err
	}
	settings, err := s.spaceSettings(ctx, in.Scope)
	if err != nil {
		return PromptPreview{}, err
	}
	out := PromptPreview{Operation: in.Operation, Authority: AuthorityDataOnly}
	switch in.Operation {
	case "retain":
		var plan FileExtractJob
		if plan, err = s.extractPlan(settings, ExtractOptions{}, nil); err == nil {
			out.System, out.User = extractPrompt(promptPlaceholder, &plan)
		}
	case "consolidation":
		out.System, out.User, err = s.previewConsolidationPrompt(ctx, in, settings)
	default:
		out.System, out.User, err = s.previewReflectPrompt(ctx, token, in, settings)
	}
	return out, err
}

func previewMembers() []Record {
	at := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	return []Record{{ID: "[memory_id_1]", Text: promptPlaceholder, CreatedAt: at}, {ID: "[memory_id_2]", Text: promptPlaceholder, CreatedAt: at}}
}

func (s *Service) previewConsolidationPrompt(ctx context.Context, in PromptPreviewInput, settings SpaceConfig) (string, string, error) {
	scoped := ObservationConsolidateInput{Scope: in.Scope}
	if c := settings.Consolidation; c != nil {
		_, st := c.strategyFor(obsScopeTags(in.Tags))
		scoped.mission = consolidationMission(c, st)
	}
	system, err := s.observationSystemFor(ctx, scoped)
	return system, observationPrompt(previewMembers(), ""), err
}

func (s *Service) previewReflectPrompt(ctx context.Context, token string, in PromptPreviewInput, settings SpaceConfig) (string, string, error) {
	if _, err := s.wikiAuthorize(ctx, token, "read:"+colDirectives, in.Scope, "", ""); err != nil {
		return "", "", err
	}
	rules, err := s.directives(ctx, in.Scope, "reflect")
	if err != nil {
		return "", "", err
	}
	if d := settings.ReflectDefaults; d != nil && d.TagScopedDirectives {
		rules = directivesForTags(rules, in.Tags)
	}
	run := &reflectRun{in: ReflectInput{Scope: in.Scope, Query: "[question]", Tags: in.Tags, PromptOnly: true, space: &settings}, rules: rules, seen: map[string]bool{}}
	if err := s.reflectGuidance(ctx, token, run); err != nil {
		return "", "", err
	}
	run.hits = []Hit{{ID: "[memory_id]", Kind: "[kind]", Text: promptPlaceholder}}
	system, user := reflectPrompt(run, false)
	return system, strings.TrimSpace(user), nil
}
