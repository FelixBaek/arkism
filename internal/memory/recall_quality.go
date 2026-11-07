package memory

import (
	"context"
	"math"
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
//   - diversity (default on; false keeps the plain score order): each recall lane reads
//     twice the limit (at most 50), and memory recall picks its
//     limit rows from the fused, reranked and recency-weighted pool greedily, each pick's
//     score lowered by diversityPenalty times how far its highest embedding similarity to a
//     row already picked sits into the near-duplicate band (selectDiverse). Near-duplicates of one fact then give way to the next
//     distinct fact instead of filling the result; the count never exceeds the limit.
//   - expand (opt-in, spends one server model call per recall): the model's sub-queries
//     search beside the question and join its fusion (recall_expand.go).
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
	LocalExpand           bool          `json:"local_expand,omitempty" bson:"local_expand,omitempty"`
	SignalPolicy          *SignalPolicy `json:"signal_policy,omitempty" bson:"signal_policy,omitempty"`
	RelevanceGate         *bool         `json:"relevance_gate,omitempty" bson:"relevance_gate,omitempty"`
	ConflictPending       string        `json:"conflict_pending,omitempty" bson:"conflict_pending,omitempty"`
	Provisional           *bool         `json:"provisional,omitempty" bson:"provisional,omitempty"`
	ProvisionalVectors    bool          `json:"provisional_vectors,omitempty" bson:"provisional_vectors,omitempty"`
	AutoCloseOnCorrection bool          `json:"auto_close_on_correction,omitempty" bson:"auto_close_on_correction,omitempty"`
	Diversity             *bool         `json:"diversity,omitempty" bson:"diversity,omitempty"`
	Expand                bool          `json:"expand,omitempty" bson:"expand,omitempty"`
	// Recall defaults (applyRecallDefaults).
	RerankOptions      *RerankOptions `json:"rerank_options,omitempty" bson:"rerank_options,omitempty"`
	PreferObservations bool           `json:"prefer_observations,omitempty" bson:"prefer_observations,omitempty"`
	IncludeChunks      bool           `json:"include_chunks,omitempty" bson:"include_chunks,omitempty"`
	ChunksMaxTokens    int            `json:"chunks_max_tokens,omitempty" bson:"chunks_max_tokens,omitempty"`
}

func (q *SpaceRecallQuality) valid() bool {
	return q == nil || (q.ConflictPending == "" || q.ConflictPending == conflictPendingAttach || q.ConflictPending == conflictPendingExclude) &&
		q.SignalPolicy.valid() && q.ChunksMaxTokens >= 0 && q.ChunksMaxTokens <= includeTokensMax
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

func (q SpaceRecallQuality) diversityOn() bool { return q.Diversity == nil || *q.Diversity }

// diversityPenalty weighs how near-duplicate a candidate is to the rows already picked
// (nearDuplicateShare) against its score relative to the best row. At 0.5 a row that repeats a
// picked one (similarity near 1) loses half the best score, while rows on distinct facts keep
// their order.
const diversityPenalty = 0.5

// selectDiverse picks limit rows from rows (best score first) one at a time by score/best
// minus diversityPenalty times the row's highest near-duplicate share to a picked row. A row
// must have comparable stored vectors before shared evidence can strengthen that penalty. Scores are kept,
// so score floors still apply to the real values; the result is in pick order.
func selectDiverse(rows []ScoredHit, limit int) []ScoredHit {
	if len(rows) <= limit || limit <= 0 || rows[0].Score <= 0 {
		return rows[:min(limit, len(rows))]
	}
	best, picked := rows[0].Score, make([]ScoredHit, 0, limit)
	used, closest := make([]bool, len(rows)), make([]float64, len(rows))
	spans := make([]map[string]bool, len(rows))
	for i, r := range rows {
		spans[i] = make(map[string]bool)
		for _, token := range SearchTokens(r.evidenceSpan) {
			spans[i][token] = true
		}
	}
	for len(picked) < limit {
		at, value := -1, math.Inf(-1)
		for i, r := range rows {
			if v := r.Score/best - diversityPenalty*closest[i]; !used[i] && v > value {
				at, value = i, v
			}
		}
		used[at] = true
		picked = append(picked, rows[at])
		for i := range rows {
			if used[i] || closest[i] >= 1 {
				continue
			}
			if len(rows[at].vector) > 0 && len(rows[at].vector) == len(rows[i].vector) {
				similarity := cosine(rows[at].vector, rows[i].vector)
				closest[i] = max(closest[i], nearDuplicateShare(similarity))
				if similarity >= defaultCaptureMergeSimilarity && rows[at].Scope == rows[i].Scope && sameSourceSpan(spans[at], spans[i]) {
					closest[i] = 1
				}
			}
		}
	}
	return picked
}

// sameSourceSpan compares precomputed evidence token sets. Short quotes cannot
// establish restatement; longer quotes require 90 percent containment. The caller
// also requires the same scope and vectors in the near-duplicate band, since one
// broad quote can support several distinct facts.
func sameSourceSpan(left, right map[string]bool) bool {
	if len(left) > len(right) {
		left, right = right, left
	}
	if len(left) < 4 {
		return false
	}
	shared := 0
	for t := range left {
		if right[t] {
			shared++
		}
	}
	return float64(shared)/float64(len(left)) >= 0.9
}

// nearDuplicateShare is how far a similarity sits into the near-duplicate band: 0 below the
// bar capture uses for a rewording of one fact (defaultCaptureMergeSimilarity), 1 at identical.
// Rows that merely share a topic, such as every memory about one person, keep their order;
// only rewordings of one fact give way.
func nearDuplicateShare(similarity float64) float64 {
	return max(0, similarity-defaultCaptureMergeSimilarity) / (1 - defaultCaptureMergeSimilarity)
}

// noticeLang is the language recall notices are written in.
func (in SearchInput) noticeLang() string {
	setting := ""
	if in.space != nil {
		setting = in.space.MemoryLanguage
	}
	return langnote.Pick(setting, in.locale, in.Query)
}

// enqueueSideLane queues a new record of a recall side lane for embedding when an embedder
// runs: a candidate when its space shows provisional candidates with provisional_vectors, a
// conversation source when its space keeps conversations (capture_density conversation).
func (s *Service) enqueueSideLane(ctx context.Context, tx Tx, col string, r Record) error {
	side := col == "memory_candidates" && r.Status == "candidate" || conversationSource(col, r)
	if !side || s.Embedder == nil || r.Sensitivity == "restricted" {
		return nil
	}
	settings, err := s.spaceSettingsFrom(ctx, tx, r.Scope)
	if err != nil {
		return nil
	}
	if q := settings.recallQuality(); col == "memory_candidates" && (!q.provisionalOn() || !q.ProvisionalVectors) ||
		col == "sources" && settings.CaptureDensity != captureDensityConversation {
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
	found, concepts := conceptsFound(terms, h)
	if concepts == 0 {
		return false
	}
	need := 1
	if concepts >= 3 {
		need = 2
	}
	return found >= need
}

// conceptsFound counts the query's topic concepts the hit's text, summary and keywords hold,
// out of how many the query has.
func conceptsFound(terms []string, h Hit) (found, concepts int) {
	concepts = len(searchQueryConcepts(terms))
	if concepts == 0 {
		return 0, 0
	}
	// The same text the keyword lane indexes (search_tokens), conditions included.
	have := SearchTokens(searchTokensRecordText(Record{Text: h.Text, Summary: h.Summary, Keywords: h.keywords, MemoryContext: h.MemoryContext}))
	seen := map[string]bool{}
	for _, t := range terms {
		// Any spelling of the concept counts, as in the keyword lane's tsquery.
		if slices.ContainsFunc(searchConceptWords(t), func(w string) bool { return slices.Contains(have, w) }) {
			seen[searchQueryConcepts([]string{t})[0]] = true
		}
	}
	return len(seen), concepts
}

// weighKeywordCoverage gives each keyword lane hit the share of the query's topic concepts it
// holds, which fusion multiplies into the hit's lane credit: a hit that matches only a word
// most of the space shares (a person's name, a project name) no longer ties a semantic match
// of the whole question, while a hit holding every concept counts as before.
func weighKeywordCoverage(in SearchInput, lists [][]Hit) [][]Hit {
	terms := searchQueryTerms(in.Query)
	keywordKey := ""
	if in.space.recallQuality().LocalExpand && len(terms) > 0 {
		canonical := slices.Clone(terms)
		slices.Sort(canonical)
		keywordKey = strings.Join(canonical, "\x00")
	}
	out := make([][]Hit, len(lists))
	for i, list := range lists {
		out[i] = slices.Clone(list)
		for j, h := range out[i] {
			out[i][j].keywordQueryKey = ""
			if slices.Contains(h.Reasons, reasonTextSearch) && !slices.Contains(h.Reasons, reasonExactKeyword) && !slices.Contains(h.Reasons, reasonVector) {
				out[i][j].keywordQueryKey = keywordKey
			}
			exact := slices.Contains(h.Reasons, reasonExactKeyword)
			if exact && (!in.space.recallQuality().LocalExpand || slices.ContainsFunc(h.keywords, func(k string) bool { return strings.EqualFold(k, strings.TrimSpace(in.Query)) })) {
				continue
			}
			if !exact && !slices.Contains(h.Reasons, reasonTextSearch) {
				continue
			}
			if found, concepts := conceptsFound(terms, h); concepts > 0 && found > 0 {
				out[i][j].termShare = float64(found) / float64(concepts)
			}
		}
	}
	return out
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
	in.optionalFailures = &out.Degraded
	rows, err := s.sideLane(ctx, "memory_candidates", in, in.SearchCriteria)
	if err != nil {
		return err
	}
	terms := searchQueryTerms(in.Query)
	for _, row := range rows {
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

// sideLane runs the recall's own lanes (exact, keyword and, with the recall's query vector,
// vector) over another collection and fuses them, best first. The caller rechecks each row.
func (s *Service) sideLane(ctx context.Context, collection string, in SearchInput, criteria SearchCriteria) ([]ScoredHit, error) {
	lane := SearchInput{Scope: in.Scope, Query: in.Query, Modes: in.Modes, Limit: provisionalLaneLimit, SearchCriteria: criteria, space: in.space}
	lists, err := s.Repo.SearchCollection(ctx, collection, lane, in.queryVector, in.querySpace)
	if err != nil {
		return nil, err
	}
	if in.space.recallQuality().LocalExpand {
		lists = weighKeywordCoverage(lane, lists)
	}
	original := slices.Clone(lists)
	if in.expandStatus == "local_applied" {
		for _, expanded := range in.expanded {
			sub := lane
			sub.Query = expanded.query
			extra, err := s.Repo.SearchCollection(ctx, collection, sub, expanded.vector, expanded.space)
			if err != nil {
				if in.optionalFailures != nil {
					*in.optionalFailures = append(*in.optionalFailures, "local_expansion")
				}
				return fuseWeighted(original, provisionalLaneLimit, nil), nil
			}
			lists = append(lists, weighKeywordCoverage(sub, extra)...)
		}
	}
	return fuseWeighted(lists, provisionalLaneLimit, nil), nil
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
