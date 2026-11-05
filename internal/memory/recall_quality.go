package memory

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/langnote"
)

// Recall quality: the space block recall_quality and what memory recall does with it.
//
//   - relevance_gate (default on): a hit the exact or keyword lane found stays only when
//     its text, summary and keywords hold enough of the query's topic terms, or the vector
//     lane found it too. The temporal lane matches the query's own time words and the graph
//     lanes start from hits that passed, so neither is gated again. When the gate removes
//     every hit, the result is empty with empty_reason below_relevance_floor.
//   - conflict_pending: attach (default) puts the correction waiting for review on the old
//     memory it would replace; exclude leaves such memories out until a person decides.
//   - provisional (default on; false turns it off): pending candidates matching the query
//     come back in a separate, labelled provisional list, never among approved items, only to
//     callers holding read:memory_candidates (people and owner tokens; agents without the
//     scope never see them) and never to an external destination or an export. Candidates are
//     found through the same exact and keyword lanes as memories, and the vector lane too
//     with provisional_vectors.
//   - provisional_vectors (opt-in, spends embedding calls on candidates a person may reject):
//     a new candidate is queued for embedding, and approving it unchanged hands that vector
//     to the memory, so an approved fact is embedded once.
//
// The notices follow the space's memory_language, else the request's Accept-Language, else
// the query's script (internal/langnote).
//   - auto_close_on_correction (opt-in): approving a correction kept next to the old memory
//     ends the old one at the new one's effective time; off, the old one gets a
//     valid_to_proposal a person accepts through the expiry route.
//   - recall defaults a request leaves unset: rerank_options (the model rerank's prune and
//     dedup), prefer_observations, include_chunks (include "sources") and chunks_max_tokens
//     (include_options.sources.max_tokens). A request that sets one keeps its own value.
const (
	conflictPendingAttach  = "attach"
	conflictPendingExclude = "exclude"
	provisionalMax         = 3
	provisionalLaneLimit   = 20 // candidates each lane returns before the checks
	pendingTextRunes       = 600
	emptyBelowRelevance    = "below_relevance_floor"
)

// SpaceRecallQuality is the recall_quality block; the block is inherited whole.
type SpaceRecallQuality struct {
	RelevanceGate         *bool  `json:"relevance_gate,omitempty" bson:"relevance_gate,omitempty"`
	ConflictPending       string `json:"conflict_pending,omitempty" bson:"conflict_pending,omitempty"`
	Provisional           *bool  `json:"provisional,omitempty" bson:"provisional,omitempty"`
	ProvisionalVectors    bool   `json:"provisional_vectors,omitempty" bson:"provisional_vectors,omitempty"`
	AutoCloseOnCorrection bool   `json:"auto_close_on_correction,omitempty" bson:"auto_close_on_correction,omitempty"`
	// Recall defaults (applyRecallDefaults).
	RerankOptions      *RerankOptions `json:"rerank_options,omitempty" bson:"rerank_options,omitempty"`
	PreferObservations bool           `json:"prefer_observations,omitempty" bson:"prefer_observations,omitempty"`
	IncludeChunks      bool           `json:"include_chunks,omitempty" bson:"include_chunks,omitempty"`
	ChunksMaxTokens    int            `json:"chunks_max_tokens,omitempty" bson:"chunks_max_tokens,omitempty"`
}

func (q *SpaceRecallQuality) valid() bool {
	return q == nil || (q.ConflictPending == "" || q.ConflictPending == conflictPendingAttach || q.ConflictPending == conflictPendingExclude) &&
		q.ChunksMaxTokens >= 0 && q.ChunksMaxTokens <= includeTokensMax
}

// applyRecallDefaults fills the recall options the request left unset from the space.
func (in *SearchInput) applyRecallDefaults() {
	q := in.space.recallQuality()
	if in.PreferObservations == nil && q.PreferObservations {
		in.PreferObservations = &q.PreferObservations
	}
	if in.RerankOptions == nil && q.RerankOptions != nil {
		o := *q.RerankOptions
		in.RerankOptions = &o
	}
	if in.Include == nil && q.IncludeChunks {
		in.Include = []string{"sources"}
	}
	if q.ChunksMaxTokens > 0 && slices.Contains(in.Include, "sources") && (in.IncludeOptions == nil || in.IncludeOptions.Sources == nil) {
		o := IncludeOptions{}
		if in.IncludeOptions != nil {
			o = *in.IncludeOptions
		}
		o.Sources = &IncludeBudget{MaxTokens: q.ChunksMaxTokens}
		in.IncludeOptions = &o
	}
}

func (c *SpaceConfig) recallQuality() SpaceRecallQuality {
	if c == nil || c.RecallQuality == nil {
		return SpaceRecallQuality{}
	}
	return *c.RecallQuality
}

func (q SpaceRecallQuality) gateOn() bool { return q.RelevanceGate == nil || *q.RelevanceGate }

func (q SpaceRecallQuality) provisionalOn() bool { return q.Provisional == nil || *q.Provisional }

// noticeLang is the language recall notices are written in.
func (in SearchInput) noticeLang() string {
	setting := ""
	if in.space != nil {
		setting = in.space.MemoryLanguage
	}
	return langnote.Pick(setting, in.locale, in.Query)
}

// enqueueProvisional queues a new candidate for embedding when an embedder runs and its
// space shows provisional candidates with provisional_vectors.
func (s *Service) enqueueProvisional(ctx context.Context, tx Tx, col string, r Record) error {
	if col != "memory_candidates" || s.Embedder == nil || r.Status != "candidate" || r.Sensitivity == "restricted" {
		return nil
	}
	settings, err := s.spaceSettings(ctx, r.Scope)
	if q := settings.recallQuality(); err != nil || !q.provisionalOn() || !q.ProvisionalVectors {
		return nil
	}
	return enqueue(ctx, tx, col, r)
}

// PendingCorrection marks an approved memory a correction is waiting on. Text is the
// candidate's text, only for callers that may read candidates.
type PendingCorrection struct {
	CandidateID string `json:"candidate_id,omitempty"`
	Text        string `json:"text,omitempty"`
	Notice      string `json:"notice"`
}

// ValidToProposal proposes ending a memory at At because the approved memory By replaced it.
type ValidToProposal struct {
	At time.Time `json:"at" bson:"at"`
	By Ref       `json:"by" bson:"by"`
}

// relevanceGate drops the exact and keyword lane hits the vector lane did not also find
// and that share too few topic terms with the query.
func relevanceGate(in SearchInput, lists [][]Hit, trace *recallTrace) [][]Hit {
	if !in.space.recallQuality().gateOn() {
		return lists
	}
	semantic := map[string]bool{}
	for _, list := range lists {
		for _, h := range list {
			if slices.Contains(h.Reasons, reasonVector) {
				semantic[hitKey(h)] = true
			}
		}
	}
	terms, whole := searchQueryTerms(in.Query), strings.TrimSpace(in.Query)
	out := make([][]Hit, len(lists))
	for i, list := range lists {
		out[i] = slices.DeleteFunc(slices.Clone(list), func(h Hit) bool {
			lexical := slices.Contains(h.Reasons, reasonExactKeyword) || slices.Contains(h.Reasons, reasonTextSearch)
			// A keyword equal to the whole query is a deliberate exact lookup.
			whole := slices.ContainsFunc(h.keywords, func(k string) bool { return strings.EqualFold(k, whole) })
			if !lexical || whole || semantic[hitKey(h)] || lexicallyRelevant(terms, h) {
				return false
			}
			trace.drop("relevance")
			return true
		})
	}
	return out
}

// lexicallyRelevant: the hit's text, summary and keywords hold one of the query's topic
// concepts, or two once the query has three or more (the keyword lane's own rule).
func lexicallyRelevant(terms []string, h Hit) bool {
	concepts := searchQueryConcepts(terms)
	if len(concepts) == 0 {
		return false
	}
	// The same text the keyword lane indexes (search_tokens), conditions included.
	have := SearchTokens(searchTokensRecordText(Record{Text: h.Text, Summary: h.Summary, Keywords: h.keywords, MemoryContext: h.MemoryContext}))
	found := map[string]bool{}
	for _, t := range terms {
		// Any spelling of the concept counts, as in the keyword lane's tsquery.
		if slices.ContainsFunc(searchConceptWords(t), func(w string) bool { return slices.Contains(have, w) }) {
			found[searchQueryConcepts([]string{t})[0]] = true
		}
	}
	need := 1
	if len(concepts) >= 3 {
		need = 2
	}
	return len(found) >= need
}

// probeAllowed asks whether the caller holds action without a denial audit row: optional
// lanes probe on every recall.
func (s *Service) probeAllowed(ctx context.Context, token, action string, scope Scope) bool {
	d, err := s.Auth.Authorize(ctx, token, Authorization{Action: action, Scope: scope, Context: NewContextScope(scope, action, "internal")})
	return err == nil && d.Allowed && d.PrincipalID != "" && d.Scope == scope
}

// pendingRecall applies the space's conflict_pending mode to the recalled memories and
// adds the provisional candidates. Plain memory recall only.
func (s *Service) pendingRecall(ctx context.Context, token string, in SearchInput, collection string, out *ContextBundle) error {
	if collection != colMemories || in.IncludeInactive || in.RelatedTo != nil {
		return nil
	}
	q := in.space.recallQuality()
	pending := slices.ContainsFunc(out.Items, func(h Hit) bool { return h.conflictID != "" })
	// A context view replays a frozen set of approved refs, so it carries no candidates.
	provisional := q.provisionalOn() && !strings.HasPrefix(in.Destination, "external:") && in.contextView == nil
	if !pending && !provisional {
		return nil
	}
	readCandidates := s.probeAllowed(ctx, token, "read:memory_candidates", in.Scope)
	if q.ConflictPending == conflictPendingExclude {
		out.Items = slices.DeleteFunc(out.Items, func(h Hit) bool { return h.conflictID != "" })
	} else if pending {
		for i := range out.Items {
			if out.Items[i].conflictID != "" {
				out.Items[i].PendingCorrection = s.pendingCorrection(ctx, token, in, out, out.Items[i].conflictID, readCandidates)
			}
		}
	}
	if !provisional || !readCandidates {
		return nil
	}
	return s.provisionalHits(ctx, token, in, out)
}

// pendingCorrection reads the conflict a memory is held in and the correction candidate in
// it. A conflict it cannot read still labels the memory.
func (s *Service) pendingCorrection(ctx context.Context, token string, in SearchInput, bundle *ContextBundle, conflictID string, withText bool) *PendingCorrection {
	out := &PendingCorrection{Notice: langnote.Text(langnote.PendingCorrection, in.noticeLang())}
	rel, err := s.Repo.Get(ctx, "memory_relations", in.Scope, conflictID)
	if err != nil || !live(rel) || rel.ContentErased {
		return out
	}
	for _, ref := range rel.Relations {
		if ref.Collection != "memory_candidates" {
			continue
		}
		c, err := s.Repo.Get(ctx, ref.Collection, in.Scope, ref.ID)
		if err != nil || c.ConflictID != conflictID || c.ContentErased || c.PrivacyQuarantined || c.Sensitivity == "restricted" {
			return out
		}
		out.CandidateID = c.ID
		if withText && s.pendingReadable(ctx, token, in, bundle, c) {
			out.Text = runeCutString(c.Text, pendingTextRunes)
		}
	}
	return out
}

// pendingReadable runs a candidate through the checks every recalled memory passes:
// live sources, then the per-hit read or, for an external destination, transfer check.
func (s *Service) pendingReadable(ctx context.Context, token string, in SearchInput, bundle *ContextBundle, c Record) bool {
	if !strings.HasPrefix(in.Destination, "external:") && checkSources(ctx, s.Repo, c) != nil {
		return false
	}
	_, ok, err := s.authorizeSearchHit(ctx, token, in, HitFromRecord("memory_candidates", c), Record{}, false, bundle)
	return err == nil && ok
}

// provisionalHits adds up to provisionalMax pending candidates the query is about, found by the
// recall's own lanes (exact, keyword and, with the recall's query vector, vector) over the
// candidates waiting for review. A lexical match must also hold the query's topic.
func (s *Service) provisionalHits(ctx context.Context, token string, in SearchInput, out *ContextBundle) error {
	lane := SearchInput{Scope: in.Scope, Query: in.Query, Modes: in.Modes, Limit: provisionalLaneLimit, SearchCriteria: in.SearchCriteria}
	lists, err := s.Repo.SearchCollection(ctx, "memory_candidates", lane, in.queryVector, in.querySpace)
	if err != nil {
		return err
	}
	terms := searchQueryTerms(in.Query)
	for _, row := range fuseWeighted(lists, provisionalLaneLimit, nil) {
		if len(out.Provisional) == provisionalMax {
			break
		}
		r, err := s.Repo.Get(ctx, "memory_candidates", in.Scope, row.Hit.ID)
		if err != nil || r.Status != "candidate" || r.ContentErased || r.PrivacyQuarantined || r.Sensitivity == "restricted" || r.ConflictID != "" || !in.SearchCriteria.matchesRecord(r) {
			continue
		}
		h := HitFromRecord("memory_candidates", r)
		h.Origin, h.Text, h.Reasons, h.Authority = "provisional", runeCutString(h.Text, pendingTextRunes), []string{"provisional"}, AuthorityDataOnly
		if (slices.Contains(row.Hit.Reasons, reasonVector) || lexicallyRelevant(terms, h)) && s.pendingReadable(ctx, token, in, out, r) {
			out.Provisional = append(out.Provisional, h)
		}
	}
	if len(out.Provisional) > 0 {
		out.ProvisionalNotice = langnote.Text(langnote.Provisional, in.noticeLang())
	}
	return nil
}

// closeCorrectedFact runs when a person approves a correction and keeps the old memory:
// the old memory ends at the new one's effective time (auto) or gets a proposal to.
func closeCorrectedFact(ctx context.Context, tx Tx, review *CandidateReviewInput, result Record, auto bool, principal string) error {
	if review == nil || review.Conflict == nil || review.Conflict.Resolution != "keep_both" || result.Status != statusActive {
		return nil
	}
	old, err := tx.Get(ctx, colMemories, result.Scope, review.Conflict.Memory.ID)
	if err != nil {
		return err
	}
	if !live(old) || old.ExpiresAt != nil || old.ConflictID != "" {
		return nil
	}
	// The old fact ends when the new one took effect; now when that is unknown or not
	// after the old fact began.
	at := time.Now().UTC().Truncate(time.Millisecond)
	if e := result.EffectiveAt; e != nil && (old.EffectiveAt == nil || e.After(*old.EffectiveAt)) {
		at = e.UTC().Truncate(time.Millisecond)
	}
	previous, action := old.Revision, "propose_valid_to:memories"
	if auto {
		old.ExpiresAt, old.ValidToProposal, action = &at, nil, "close_valid_to:memories"
	} else {
		old.ValidToProposal = &ValidToProposal{At: at, By: Ref{Collection: colMemories, ID: result.ID, Revision: result.Revision}}
	}
	old.Revision++
	old.UpdatedAt = time.Now().UTC()
	if err = tx.Replace(ctx, colMemories, old, previous); err != nil {
		return err
	}
	return audit(ctx, tx, old.Scope, principal, action, old.ID)
}

// recordReviewOutcome records a candidate review, then closes or proposes closing the
// fact a kept correction replaced.
func recordReviewOutcome(ctx context.Context, tx Tx, id, principal string, review *CandidateReviewInput, result Record, autoClose bool) error {
	if err := recordMemoryReview(ctx, tx, id, principal, *review, result); err != nil {
		return err
	}
	return closeCorrectedFact(ctx, tx, review, result, autoClose, principal)
}

// autoCloseOn reads the space switch before a review that resolves a conflict keeping both.
func (s *Service) autoCloseOn(ctx context.Context, scope Scope, review *CandidateReviewInput) bool {
	if review == nil || review.Conflict == nil || review.Conflict.Resolution != "keep_both" {
		return false
	}
	settings, err := s.spaceSettings(ctx, scope)
	return err == nil && settings.recallQuality().AutoCloseOnCorrection
}
