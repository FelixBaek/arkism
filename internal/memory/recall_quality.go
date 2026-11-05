package memory

import (
	"context"
	"slices"
	"strings"
	"time"
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
//   - provisional (opt-in): pending candidates matching the query come back in a separate
//     provisional list, never among approved items, and only to callers that may read
//     candidates.
//   - auto_close_on_correction (opt-in): approving a correction kept next to the old memory
//     ends the old one at the new one's effective time; off, the old one gets a
//     valid_to_proposal a person accepts through the expiry route.
const (
	conflictPendingAttach  = "attach"
	conflictPendingExclude = "exclude"
	provisionalMax         = 3
	provisionalScan        = 100
	pendingTextRunes       = 600
	emptyBelowRelevance    = "below_relevance_floor"
	pendingCorrectionNote  = "정정 검토 중: 사람이 결정하기 전까지 이 기억은 틀렸을 수 있습니다."
	provisionalNote        = "승인 전 후보입니다. 확정된 기억이 아니며 사람이 검토하기 전입니다."
)

// SpaceRecallQuality is the recall_quality block; the block is inherited whole.
type SpaceRecallQuality struct {
	RelevanceGate         *bool  `json:"relevance_gate,omitempty" bson:"relevance_gate,omitempty"`
	ConflictPending       string `json:"conflict_pending,omitempty" bson:"conflict_pending,omitempty"`
	Provisional           bool   `json:"provisional,omitempty" bson:"provisional,omitempty"`
	AutoCloseOnCorrection bool   `json:"auto_close_on_correction,omitempty" bson:"auto_close_on_correction,omitempty"`
}

func (q *SpaceRecallQuality) valid() bool {
	return q == nil || q.ConflictPending == "" || q.ConflictPending == conflictPendingAttach || q.ConflictPending == conflictPendingExclude
}

func (c *SpaceConfig) recallQuality() SpaceRecallQuality {
	if c == nil || c.RecallQuality == nil {
		return SpaceRecallQuality{}
	}
	return *c.RecallQuality
}

func (q SpaceRecallQuality) gateOn() bool { return q.RelevanceGate == nil || *q.RelevanceGate }

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
	have := SearchTokens(h.Text + " " + h.Summary + " " + strings.Join(h.keywords, " "))
	found := map[string]bool{}
	for _, t := range terms {
		if slices.Contains(have, t) {
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
// adds the opt-in provisional candidates. Plain memory recall only.
func (s *Service) pendingRecall(ctx context.Context, token string, in SearchInput, collection string, out *ContextBundle) error {
	if collection != colMemories || in.IncludeInactive || in.RelatedTo != nil {
		return nil
	}
	q := in.space.recallQuality()
	pending := slices.ContainsFunc(out.Items, func(h Hit) bool { return h.conflictID != "" })
	if !pending && !q.Provisional {
		return nil
	}
	readCandidates := s.probeAllowed(ctx, token, "read:memory_candidates", in.Scope)
	if q.ConflictPending == conflictPendingExclude {
		out.Items = slices.DeleteFunc(out.Items, func(h Hit) bool { return h.conflictID != "" })
	} else if pending {
		for i := range out.Items {
			if out.Items[i].conflictID != "" {
				out.Items[i].PendingCorrection = s.pendingCorrection(ctx, in.Scope, out.Items[i].conflictID, readCandidates)
			}
		}
	}
	if !q.Provisional || !readCandidates {
		return nil
	}
	return s.provisionalHits(ctx, in, out)
}

// pendingCorrection reads the conflict a memory is held in and the correction candidate in
// it. A conflict it cannot read still labels the memory.
func (s *Service) pendingCorrection(ctx context.Context, scope Scope, conflictID string, withText bool) *PendingCorrection {
	out := &PendingCorrection{Notice: pendingCorrectionNote}
	rel, err := s.Repo.Get(ctx, "memory_relations", scope, conflictID)
	if err != nil || !live(rel) || rel.ContentErased {
		return out
	}
	for _, ref := range rel.Relations {
		if ref.Collection != "memory_candidates" {
			continue
		}
		c, err := s.Repo.Get(ctx, ref.Collection, scope, ref.ID)
		if err != nil || c.ConflictID != conflictID || c.ContentErased || c.PrivacyQuarantined || c.Sensitivity == "restricted" {
			return out
		}
		out.CandidateID = c.ID
		if withText {
			out.Text = runeCutString(c.Text, pendingTextRunes)
		}
	}
	return out
}

// provisionalHits adds up to provisionalMax pending candidates the query is about.
// shortcut: scans the provisionalScan most trusted pending candidates lexically; add an
// indexed candidate lane when review queues grow past that.
func (s *Service) provisionalHits(ctx context.Context, in SearchInput, out *ContextBundle) error {
	rows, err := s.Repo.ListCandidates(ctx, ReviewInput{Scope: in.Scope, Status: "candidate", Limit: provisionalScan})
	if err != nil {
		return err
	}
	terms := searchQueryTerms(in.Query)
	for _, r := range rows {
		if len(out.Provisional) == provisionalMax {
			break
		}
		if r.Scope != in.Scope || r.ContentErased || r.PrivacyQuarantined || r.Sensitivity == "restricted" || r.ConflictID != "" || !in.SearchCriteria.matchesRecord(r) {
			continue
		}
		h := HitFromRecord("memory_candidates", r)
		h.Origin, h.Text, h.Reasons, h.Authority = "provisional", runeCutString(h.Text, pendingTextRunes), []string{"provisional"}, AuthorityDataOnly
		if lexicallyRelevant(terms, h) {
			out.Provisional = append(out.Provisional, h)
		}
	}
	if len(out.Provisional) > 0 {
		out.ProvisionalNotice = provisionalNote
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
	at := time.Now().UTC().Truncate(time.Millisecond)
	if result.EffectiveAt != nil && result.EffectiveAt.After(at) {
		at = result.EffectiveAt.UTC().Truncate(time.Millisecond)
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
