package memory

import (
	"context"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Deep Twin, Tacit Map and Reflection read views plus the conservative
// improvement promotion rule. The views are projections of records that
// already exist (memory candidates/memories, lesson candidates, the graph
// relations table); none of them grants execution authority.

const (
	tacitFollowedBy        = "often_followed_by"
	improvementMinRuns     = 3
	improvementMinConfid   = 0.7
	intelligenceListLimit  = 100
	twinTagPrefix          = "twin:"
	intelligenceAuthority  = "data_only_untrusted"
	improvementTwinClaim   = "twin_claim"
	improvementTacitEdge   = "tacit_relation"
	improvementSkillVersio = "skill_version"
)

var twinTypes = []string{"preference", "working_style", "stable_goal", "constraint", "interaction_pattern"}

type TwinClaim struct {
	ID            string     `json:"id"`
	Revision      int64      `json:"revision"`
	Type          string     `json:"type"`
	Claim         string     `json:"claim"`
	Confidence    float64    `json:"confidence"`
	EvidenceRefs  []Ref      `json:"evidence_refs"`
	DistinctRuns  int        `json:"distinct_runs"`
	Status        string     `json:"status"` // candidate | inferred | confirmed | superseded | rejected
	LastConfirmed *time.Time `json:"last_confirmed"`
	Scope         Scope      `json:"scope"`
}

type TacitEdge struct {
	Source          string     `json:"source"`
	Target          string     `json:"target"`
	RelationType    string     `json:"relation_type"`
	OccurrenceCount int        `json:"occurrence_count"`
	Confidence      float64    `json:"confidence"`
	LastSeen        time.Time  `json:"last_seen"`
	EvidenceRefs    []Ref      `json:"evidence_refs"`
	Status          string     `json:"status"` // candidate | inferred | confirmed
	ConfirmedAt     *time.Time `json:"confirmed_at,omitempty"`
	Scope           Scope      `json:"scope"`
}

// ReflectionView groups identical lesson candidates (same task type, same
// text) across runs: one failure is one observation, never a fact.
type ReflectionView struct {
	CandidateRefs  []Ref     `json:"candidate_refs"`
	ID             string    `json:"id"`
	Subject        string    `json:"subject"`
	Observation    string    `json:"observation"`
	Interpretation string    `json:"interpretation"`
	EvidenceRefs   []Ref     `json:"evidence_refs"`
	DistinctRuns   int       `json:"distinct_runs"`
	Confidence     float64   `json:"confidence"`
	Status         string    `json:"status"` // candidate | validated | rejected | expired
	CreatedAt      time.Time `json:"created_at"`
	Scope          Scope     `json:"scope"`
}

type IntelligencePage[T any] struct {
	Items     []T    `json:"items"`
	Authority string `json:"authority"`
}

// tacitStore is implemented by the Postgres repository only; the graph
// relations table is the source of truth for Tacit Map edges.
type tacitStore interface {
	TacitEdges(ctx context.Context, scope Scope, sourceKind, sourceID, relation string) ([]TacitEdge, error)
	ConfirmTacitEdge(ctx context.Context, scope Scope, source, target, relation, principal string, at time.Time) error
}

// countConfidence saturates with independent observations: 1, 2, 3 runs give
// 0.5, 0.75, 0.875. shortcut: replace with evaluated precision once edges and
// claims carry labelled outcomes.
func countConfidence(n int) float64 {
	return math.Round((1-math.Pow(0.5, float64(n)))*1000) / 1000
}

func evidenceRuns(refs []Ref) int {
	runs := map[string]bool{}
	for _, r := range refs {
		if r.Collection == "agent_runs" || r.Collection == "execution_traces" {
			runs[r.Collection+"/"+r.ID] = true
		}
	}
	// A trace and its run are one observation; count the larger family.
	traces, agentRuns := 0, 0
	for k := range runs {
		if strings.HasPrefix(k, "agent_runs/") {
			agentRuns++
		} else {
			traces++
		}
	}
	return max(traces, agentRuns)
}

func twinType(tags []string) string {
	for _, tag := range tags {
		if t, ok := strings.CutPrefix(tag, twinTagPrefix); ok && slices.Contains(twinTypes, t) {
			return t
		}
	}
	return ""
}

func twinFromRecord(r Record, status string, lastConfirmed *time.Time) TwinClaim {
	confidence := 0.0
	if r.Confidence != nil {
		confidence = *r.Confidence
	}
	evidence := append([]Ref{}, r.Relations...)
	runs := evidenceRuns(evidence)
	if status == "candidate" && runs >= improvementMinRuns && confidence >= improvementMinConfid {
		status = "inferred"
	}
	return TwinClaim{ID: r.ID, Revision: r.Revision, Type: twinType(r.Tags), Claim: r.Text, Confidence: confidence, EvidenceRefs: evidence,
		DistinctRuns: runs, Status: status, LastConfirmed: lastConfirmed, Scope: r.Scope}
}

// Confirmed claims need only the memory-read grant. Pending claims also expose
// the review queue, so HTTP and service validation share this distinction.
func twinClaimPermission(statuses []string) (string, error) {
	review := len(statuses) == 0
	for _, st := range statuses {
		if !slices.Contains([]string{"candidate", "inferred", "confirmed", "superseded", "rejected"}, st) {
			return "", ErrInvalid
		}
		review = review || st == "candidate" || st == "inferred" || st == "rejected"
	}
	if review {
		return reviewAction, nil
	}
	return "read:memories", nil
}

// ListTwinClaims returns Deep Twin claims for one user scope. The twin is the
// user's model, so specialist (agent) scopes have none of their own.
// shortcut: scan authorized rows; use tag-aware storage cursors if this exceeds request budgets.
func (s *Service) ListTwinClaims(ctx context.Context, token string, scope Scope, statuses []string) (IntelligencePage[TwinClaim], error) {
	out := IntelligencePage[TwinClaim]{Items: []TwinClaim{}, Authority: intelligenceAuthority}
	if _, err := twinClaimPermission(statuses); err != nil {
		return out, err
	}
	if !validScope(scope) {
		return out, ErrInvalid
	}
	if scope.AgentID != "" {
		return out, nil
	}
	want := func(st string) bool { return len(statuses) == 0 || slices.Contains(statuses, st) }
	claims := []TwinClaim{}
	for _, st := range []string{"candidate", "rejected"} {
		if st == "candidate" && !want("candidate") && !want("inferred") || st == "rejected" && !want("rejected") {
			continue
		}
		more, err := s.candidateTwinClaims(ctx, token, scope, st)
		if err != nil {
			return out, err
		}
		claims = append(claims, more...)
	}
	if want("confirmed") || want("superseded") {
		more, err := s.memoryTwinClaims(ctx, token, scope)
		if err != nil {
			return out, err
		}
		claims = append(claims, more...)
	}
	for _, c := range claims {
		if want(c.Status) {
			out.Items = append(out.Items, c)
		}
	}
	return out, nil
}

func (s *Service) candidateTwinClaims(ctx context.Context, token string, scope Scope, st string) ([]TwinClaim, error) {
	claims := []TwinClaim{}
	cursor := ""
	seen := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := s.ListCandidates(ctx, token, ReviewInput{omitAdvice: true, Scope: scope, Status: st, Kind: "user", Limit: intelligenceListLimit, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, r := range page.Items {
			if twinType(r.Tags) != "" && len(r.Relations) > 0 {
				claims = append(claims, twinFromRecord(r, st, nil))
			}
		}
		if page.NextCursor == "" {
			break
		}
		if seen[page.NextCursor] {
			return nil, ErrUnavailable
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
	return claims, nil
}
func (s *Service) memoryTwinClaims(ctx context.Context, token string, scope Scope) ([]TwinClaim, error) {
	claims := []TwinClaim{}
	cursor := ""
	seen := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := s.ListMemories(ctx, token, MemoryListInput{Scope: scope, Kind: "user", IncludeInactive: true, Limit: intelligenceListLimit, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, r := range page.Items {
			if twinType(r.Tags) == "" || len(r.Relations) == 0 {
				continue
			}
			st := "confirmed"
			if r.Status == "superseded" {
				st = "superseded"
			} else if !live(r) {
				continue
			}
			confirmed := r.CreatedAt
			claims = append(claims, twinFromRecord(r, st, &confirmed))
		}
		if page.NextCursor == "" {
			break
		}
		if seen[page.NextCursor] {
			return nil, ErrUnavailable
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
	return claims, nil
}

// ListReflections groups lesson candidates for one subject (task type, e.g.
// "skill:pdf-ocr/mineru-local"). Approved lessons are "validated".
func (s *Service) ListReflections(ctx context.Context, token string, scope Scope, subject string) (IntelligencePage[ReflectionView], error) {
	out := IntelligencePage[ReflectionView]{Items: []ReflectionView{}, Authority: intelligenceAuthority}
	if !validScope(scope) || !validRunLabel(subject, 128) {
		return out, ErrInvalid
	}
	tag := learningTag(subject)
	groups := map[string]*ReflectionView{}
	order := []string{}
	add := func(r Record, status string) {
		if !slices.Contains(r.Tags, tag) {
			return
		}
		key := status + "\x00" + r.Text
		g := groups[key]
		if g == nil {
			g = &ReflectionView{ID: r.ID, Subject: subject, Interpretation: r.Text, Status: status, CreatedAt: r.CreatedAt, Scope: r.Scope, EvidenceRefs: []Ref{}}
			if len(r.Keywords) > 1 {
				g.Observation = subject + " ended " + r.Keywords[1]
			}
			groups[key] = g
			order = append(order, key)
		}
		g.CandidateRefs = append(g.CandidateRefs, Ref{Collection: "memory_candidates", ID: r.ID, Revision: r.Revision})
		for _, ref := range r.Relations {
			if !slices.Contains(g.EvidenceRefs, ref) {
				g.EvidenceRefs = append(g.EvidenceRefs, ref)
			}
		}
		if r.CreatedAt.Before(g.CreatedAt) {
			g.CreatedAt = r.CreatedAt
		}
	}
	for _, st := range []string{"candidate", "approved", "rejected", "expired"} {
		status := map[string]string{"approved": "validated"}[st]
		if status == "" {
			status = st
		}
		cursor := ""
		seen := map[string]bool{}
		for {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			page, err := s.ListCandidates(ctx, token, ReviewInput{omitAdvice: true, Scope: scope, Status: st, Kind: "lesson", Limit: intelligenceListLimit, Cursor: cursor})
			if err != nil {
				return out, err
			}
			for _, r := range page.Items {
				add(r, status)
			}
			if page.NextCursor == "" {
				break
			}
			if seen[page.NextCursor] {
				return out, ErrUnavailable
			}
			seen[page.NextCursor] = true
			cursor = page.NextCursor
		}
	}
	for _, key := range order {
		g := groups[key]
		g.DistinctRuns = evidenceRuns(g.EvidenceRefs)
		g.Confidence = countConfidence(g.DistinctRuns)
		out.Items = append(out.Items, *g)
	}
	return out, nil
}

func parseTacitNode(node string) (string, string, bool) {
	kind, id, ok := strings.Cut(node, ":")
	return kind, id, ok && validRunLabel(kind, 64) && validRunLabel(id, 256)
}

// ListTacitEdges returns observed workflow edges leaving one node, or every
// tacit edge in the scope when node is empty.
func (s *Service) ListTacitEdges(ctx context.Context, token string, scope Scope, node, relation string) (IntelligencePage[TacitEdge], error) {
	out := IntelligencePage[TacitEdge]{Items: []TacitEdge{}, Authority: intelligenceAuthority}
	kind, id := "", ""
	if node != "" {
		var ok bool
		if kind, id, ok = parseTacitNode(node); !ok {
			return out, ErrInvalid
		}
	}
	if !validScope(scope) || relation != "" && relation != tacitFollowedBy {
		return out, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:agent_runs", scope, "", ""); err != nil {
		return out, err
	}
	store, ok := s.Repo.(tacitStore)
	if !ok {
		return out, ErrUnavailable
	}
	edges, err := store.TacitEdges(ctx, scope, kind, id, relation)
	if err != nil {
		return out, err
	}
	out.Items = append(out.Items, edges...)
	return out, nil
}

type ImprovementInput struct {
	Scope            Scope  `json:"scope"`
	Target           string `json:"target"` // twin_claim | tacit_relation | skill_version
	ID               string `json:"id"`     // claim id, "source|relation|target" edge key, or skill subject
	ExpectedRevision int64  `json:"expected_revision,omitempty"`
	DryRun           bool   `json:"dry_run"`
}

type ImprovementDecision struct {
	Target       string `json:"target"`
	ID           string `json:"id"`
	Promotable   bool   `json:"promotable"`
	Promoted     bool   `json:"promoted"`
	Reason       string `json:"reason"`
	DistinctRuns int    `json:"distinct_runs"`
	EvidenceRefs []Ref  `json:"evidence_refs"`
	Next         string `json:"next,omitempty"`
}

// promotionReason is the single conservative rule: >= 3 distinct runs and
// confidence >= 0.7. Contradicted twin claims are "conflicted" candidates and
// never reach it. Human approval is the caller's own role.
func promotionReason(status string, runs int, confidence float64) string {
	switch {
	case status != "candidate" && status != "inferred":
		return "not_promotable_status"
	case runs < improvementMinRuns:
		return "insufficient_distinct_runs"
	case confidence < improvementMinConfid:
		return "low_confidence"
	}
	return ""
}

// Improve validates an improvement candidate and, unless dry_run, promotes it.
// Only a human principal promotes; nothing auto-promotes. A new skill version
// is only validated here: its promotion stays on the skill review path
// (independent evaluation + approve + apply).
func (s *Service) Improve(ctx context.Context, token string, in ImprovementInput) (ImprovementDecision, error) {
	out := ImprovementDecision{Target: in.Target, ID: in.ID, EvidenceRefs: []Ref{}}
	if !validScope(in.Scope) || in.ID == "" || len(in.ID) > 1024 {
		return out, ErrInvalid
	}
	actor, err := s.authorize(ctx, token, "read:memory_candidates", in.Scope, "", "")
	if err != nil {
		return out, err
	}
	if actor.Role != "human" {
		return out, ErrDenied
	}
	status, confidence, err := s.improvementEvidence(ctx, token, &in, &out)
	if err != nil {
		return out, err
	}
	out.Reason = promotionReason(status, out.DistinctRuns, confidence)
	out.Promotable = out.Reason == ""
	if !out.Promotable || in.DryRun || in.Target == improvementSkillVersio {
		if out.Promotable {
			out.Reason = map[bool]string{true: "dry_run", false: "skill_review_required"}[in.DryRun]
		}
		return out, nil
	}
	switch in.Target {
	case improvementTwinClaim:
		if _, err = s.ReviewCandidate(ctx, token, in.ID, CandidateReviewInput{Scope: in.Scope, ExpectedRevision: in.ExpectedRevision, Action: "approve"}); err != nil {
			return out, err
		}
	case improvementTacitEdge:
		// Confirming writes the relation row, so reading candidates is not enough.
		if _, err = s.authorize(ctx, token, "transition:memory_relations", in.Scope, in.ID, ""); err != nil {
			return out, err
		}
		parts := strings.Split(in.ID, "|")
		now := time.Now().UTC().Truncate(time.Millisecond)
		err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			store, ok := tx.(tacitStore)
			if !ok {
				return ErrUnavailable
			}
			return store.ConfirmTacitEdge(ctx, in.Scope, parts[0], parts[2], parts[1], actor.PrincipalID, now)
		})
		if err != nil {
			return out, err
		}
	}
	out.Promoted, out.Reason = true, "promoted"
	return out, nil
}

// improvementEvidence fills out with the target's evidence and returns its
// review status and confidence.
func (s *Service) improvementEvidence(ctx context.Context, token string, in *ImprovementInput, out *ImprovementDecision) (status string, confidence float64, err error) {
	switch in.Target {
	case improvementTwinClaim:
		page, err := s.ListTwinClaims(ctx, token, in.Scope, nil)
		if err != nil {
			return "", 0, err
		}
		i := slices.IndexFunc(page.Items, func(c TwinClaim) bool { return c.ID == in.ID })
		if i < 0 {
			return "", 0, ErrNotFound
		}
		c := page.Items[i]
		status, confidence, out.DistinctRuns, out.EvidenceRefs = c.Status, c.Confidence, c.DistinctRuns, c.EvidenceRefs
		if in.ExpectedRevision != 0 && in.ExpectedRevision != c.Revision {
			return "", 0, ErrConflict
		}
		in.ExpectedRevision = c.Revision
	case improvementTacitEdge:
		parts := strings.Split(in.ID, "|")
		if len(parts) != 3 {
			return "", 0, ErrInvalid
		}
		page, err := s.ListTacitEdges(ctx, token, in.Scope, parts[0], parts[1])
		if err != nil {
			return "", 0, err
		}
		i := slices.IndexFunc(page.Items, func(e TacitEdge) bool { return e.Target == parts[2] })
		if i < 0 {
			return "", 0, ErrNotFound
		}
		e := page.Items[i]
		status, confidence, out.DistinctRuns, out.EvidenceRefs = e.Status, e.Confidence, e.OccurrenceCount, e.EvidenceRefs
	case improvementSkillVersio:
		// Evidence for a new version: an unreviewed reflection on the skill
		// subject (e.g. "skill:etsy-listing") repeated across >= 3 runs.
		page, err := s.ListReflections(ctx, token, in.Scope, in.ID)
		if err != nil {
			return "", 0, err
		}
		for _, r := range page.Items {
			if r.Status == "candidate" && r.DistinctRuns > out.DistinctRuns {
				status, confidence, out.DistinctRuns, out.EvidenceRefs = "candidate", r.Confidence, r.DistinctRuns, r.EvidenceRefs
			}
		}
		if status == "" {
			return "", 0, ErrNotFound
		}
		out.Next = "import the revised SKILL.md as a candidate version, then evaluate independently, approve and apply (skill review)"
	default:
		return "", 0, ErrInvalid
	}
	return status, confidence, nil
}

func parseIntelligenceQuery(raw string, extra ...string) (Scope, url.Values, error) {
	q, err := url.ParseQuery(raw)
	if err != nil {
		return Scope{}, nil, ErrInvalid
	}
	allowed := append([]string{"workspace_id", "project_id", "user_id", "agent_id"}, extra...)
	for key, values := range q {
		if !slices.Contains(allowed, key) || len(values) != 1 {
			return Scope{}, nil, ErrInvalid
		}
	}
	scope := Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")}
	if !validScope(scope) {
		return Scope{}, nil, ErrInvalid
	}
	return scope, q, nil
}

func splitStatuses(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}
