package memory

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Capture is the single write path for automatically captured memory. Clients
// (Claude Code hooks, other agent events) only extract claims; privacy, scope,
// dedupe and promotion live here so every client behaves the same way.
//
// Only a claim the user stated explicitly can be promoted, and only by a human
// principal through the normal review gate. Model results and inferences stay
// candidates; corrections never overwrite and are marked as conflicts.

var captureEventTypes = []string{"turn_completed", "run_completed", "handoff_completed", "user_corrected_fact", "explicit_remember", "session_end", "backfill"}
var captureKinds = []string{"user", "decision", "procedural", "lesson", "episodic", "semantic"}
var captureBases = []string{"explicit_user", "assistant_result", "inferred"}

// Kinds that describe what the user wants or decided; those are the only ones
// a user's explicit statement may promote without a reviewer.
var capturePromotableKinds = []string{"user", "decision", "procedural"}

// A recalled memory is injected into every later prompt, so a claim that reads
// like an order to the model (prompt injection carried in from a log, a web page
// or a pasted file) must never become active without a reviewer.
// shortcut: a short phrase list, not a classifier; add phrases as incidents show them.
var instructionLike = regexp.MustCompile(`(?i)(ignore|disregard|forget|override)\s+(all\s+|any\s+|the\s+)?(previous|prior|above|earlier|system)\s+(instructions?|prompts?|rules?|messages?)|system\s+prompt|you\s+are\s+now\b|developer\s+mode|jailbreak|(이전|위의?|앞의|기존|모든)\s*(지시|지침|명령|규칙|프롬프트)(를|을|은|는)?\s*(무시|잊어|따르지)|시스템\s*프롬프트|탈옥`)

type CaptureEvent struct {
	ID            string     `json:"id"`
	Type          string     `json:"type"`
	OriginProduct string     `json:"origin_product"`
	SessionID     string     `json:"session_id,omitempty"`
	TraceID       string     `json:"trace_id,omitempty"`
	OccurredAt    *time.Time `json:"occurred_at"`
}

type CaptureClaim struct {
	MemoryContext   *MemoryContext      `json:"memory_context,omitempty" bson:"memory_context,omitempty"`
	SourceRefs      []Ref               `json:"source_refs,omitempty"`
	ArchiveMessages []ArchiveMessageRef `json:"archive_messages,omitempty"`

	Subject string `json:"subject"`
	// Repo names the repository the conversation ran in (owner/name or folder name). Memories that
	// can differ between repositories (all kinds but "user") carry it as a repo:<name> tag, and
	// recall leaves them out in other repositories.
	Repo       string   `json:"repo,omitempty"`
	Text       string   `json:"text"`
	Kind       string   `json:"kind"`
	Basis      string   `json:"basis"`
	Reason     string   `json:"reason"`
	Confidence *float64 `json:"confidence,omitempty"`
	Supersedes *Ref     `json:"supersedes,omitempty"`
}

type CaptureInput struct {
	Automated bool           `json:"automated,omitempty"`
	Scope     Scope          `json:"scope"`
	Event     CaptureEvent   `json:"event"`
	Claims    []CaptureClaim `json:"claims"`
	Generator *Generator     `json:"generator"`
}

type CaptureItem struct {
	Status      string `json:"status"` // accepted | candidate | merged | rejected
	Reason      string `json:"reason"`
	DedupeKey   string `json:"dedupe_key,omitempty"`
	CandidateID string `json:"candidate_id,omitempty"`
	MemoryID    string `json:"memory_id,omitempty"`
}

type CaptureResult struct {
	Items []CaptureItem `json:"items"`
}

func validCaptureInput(in CaptureInput, now time.Time) bool {
	e := in.Event
	return validScope(in.Scope) && validGenerator(in.Generator) && len(in.Claims) > 0 && len(in.Claims) <= 32 &&
		validRunLabel(e.ID, 256) && slices.Contains(captureEventTypes, e.Type) && originProductPattern.MatchString(e.OriginProduct) &&
		(e.SessionID == "" || validRunLabel(e.SessionID, 120)) && (e.TraceID == "" || validRunLabel(e.TraceID, 256)) &&
		e.OccurredAt != nil && storableTime(*e.OccurredAt) && !e.OccurredAt.After(now)
}

func validCaptureClaim(c CaptureClaim) bool {
	if !validMemoryContext(c.MemoryContext, time.Now().UTC()) || c.MemoryContext != nil && (c.MemoryContext.EvidenceKind == "user_statement" && c.Basis != "explicit_user" || c.MemoryContext.EvidenceKind == "tool_observation" && len(c.SourceRefs) == 0) {
		return false
	}
	if len(c.SourceRefs) > 8 || len(c.SourceRefs) > 0 && c.Basis != "inferred" {
		return false
	}
	for _, ref := range c.SourceRefs {
		if ref.Collection != "sources" || !validRunLabel(ref.ID, 256) || ref.Revision < 1 {
			return false
		}
	}
	return strings.TrimSpace(c.Text) != "" && len(c.Text) <= 4000 && validRunLabel(c.Subject, 128) && len(c.Reason) <= 500 &&
		(c.Repo == "" || validRunLabel(c.Repo, 128)) &&
		slices.Contains(captureKinds, c.Kind) && slices.Contains(captureBases, c.Basis) &&
		(c.Supersedes == nil || c.Supersedes.Collection == "memories" && c.Supersedes.ID != "" && c.Supersedes.Revision > 0)
}

// captureKey ignores the event, so the same claim from a retry, a replay or a
// later session lands on the same pointer and merges instead of duplicating.
func captureKey(scope Scope, kind, text string, correction ...*Ref) string {
	key := "capture-" + ContentHash(structuredText(struct {
		Version int    `json:"version"`
		Scope   Scope  `json:"scope"`
		Kind    string `json:"kind"`
		Text    string `json:"text"`
	}{1, scope, kind, strings.ToLower(strings.Join(strings.Fields(text), " "))}))
	if len(correction) > 0 && correction[0] != nil {
		key = "capture-" + ContentHash(key+"\n"+structuredText(correction[0]))
	}
	return key
}

func (s *Service) Capture(ctx context.Context, token string, in CaptureInput) (CaptureResult, error) {
	started := time.Now()
	if !validCaptureInput(in, started.UTC()) {
		return CaptureResult{}, ErrInvalid
	}
	// An event is captured once: a replayed turn may be re-extracted with other
	// wording, which no claim-level key can catch, so the receipt is per event.
	receipt := "capture-event-" + ContentHash(structuredText(struct {
		Scope Scope  `json:"scope"`
		Event string `json:"event"`
	}{in.Scope, in.Event.ID}))
	actor, authErr := s.authorize(ctx, token, "read:memory_candidates", in.Scope, "", "")
	if authErr != nil {
		return CaptureResult{}, authErr
	}
	if in.Automated && actor.Role != roleService {
		return CaptureResult{}, ErrDenied
	}
	ctx = bindRequestActor(ctx, actor)
	var seen Record
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		var e error
		seen, e = tx.Get(ctx, "jobs", in.Scope, receipt)
		return e
	})
	if err == nil && seen.Kind == "capture_receipt" {
		out := CaptureResult{Items: make([]CaptureItem, len(in.Claims))}
		for i := range out.Items {
			out.Items[i] = CaptureItem{Status: "merged", Reason: "duplicate_event"}
		}
		return out, nil
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return CaptureResult{}, err
	}
	out := CaptureResult{Items: make([]CaptureItem, len(in.Claims))}
	transient := false
	for i, c := range in.Claims {
		item, err := s.captureClaim(ctx, token, in, c)
		if err != nil {
			item = CaptureItem{Status: "rejected", Reason: captureErrorCode(err), DedupeKey: item.DedupeKey}
			transient = transient || c.Supersedes != nil || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrConflict) || item.Reason == "internal_error"
		}
		out.Items[i] = item
		// Identifiers and codes only: claim text never reaches the log.
		slog.Info("memory capture", "event_type", in.Event.Type, "origin", in.Event.OriginProduct, "workspace", in.Scope.WorkspaceID,
			"project", in.Scope.ProjectID, "status", item.Status, "reason", item.Reason, "candidate_id", item.CandidateID,
			"duration_ms", time.Since(started).Milliseconds())
	}
	if !transient {
		// A transient failure leaves no receipt, so the client's retry runs again.
		now := time.Now().UTC().Truncate(time.Millisecond)
		_ = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			return tx.Insert(ctx, "jobs", Record{ID: receipt, Scope: in.Scope, Kind: "capture_receipt", Status: "done", Revision: 1, CreatedAt: now, UpdatedAt: now})
		})
	}
	return out, nil
}

func (s *Service) captureClaim(ctx context.Context, token string, in CaptureInput, c CaptureClaim) (CaptureItem, error) {
	if !validCaptureClaim(c) {
		return CaptureItem{Status: "rejected", Reason: "invalid_claim"}, nil
	}
	if containsSecret(structuredText(c)) {
		// the block itself is audited; nothing of the claim is copied.
		s.auditDeniedInput(ctx, "capture:secret_detected", in.Scope, "")
		return CaptureItem{Status: "rejected", Reason: "secret_detected"}, nil
	}
	scope := in.Scope
	// A user's own statement belongs to the user and project, never to the
	// specialist that happened to hear it; agent results stay agent-scoped.
	if c.Basis == "explicit_user" {
		scope.AgentID = ""
	}
	// Rebind only the scope, then authorize it afresh with the same actor.
	// User-owned capture is not an inferred grant from the observing agent.
	if actor, ok := ctx.Value(requestActorKey{}).(Decision); ok && actor.Scope != scope {
		actor.Scope = scope
		ctx = bindRequestActor(ctx, actor)
	}
	c.MemoryContext = copyMemoryContext(c.MemoryContext)
	if c.MemoryContext != nil {
		at := in.Event.OccurredAt.UTC().Truncate(time.Millisecond)
		c.MemoryContext.ObservedAt = &at
	}
	key := captureKey(scope, c.Kind, c.Text, c.Supersedes)
	if c.MemoryContext != nil {
		key = "capture-" + ContentHash(key+"\n"+memoryContextKey(c.MemoryContext))
	}
	item := CaptureItem{DedupeKey: key}
	if _, err := s.authorize(ctx, token, "read:memory_candidates", scope, "", ""); err != nil {
		return item, err
	}
	var existing Record
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		var e error
		existing, e = tx.Get(ctx, "jobs", scope, key)
		return e
	})
	var stale *Record
	if err == nil && existing.Kind == "capture_pointer" {
		if existing.Target != nil && existing.Status == "correction_pending" && c.Supersedes != nil {
			item.CandidateID = existing.Target.ID
			return s.finishCaptureCorrection(ctx, token, in, c, scope, existing, item)
		}
		if existing.Target != nil {
			live, e := s.captureTargetLive(ctx, scope, existing.Target.ID)
			if e != nil {
				return item, e
			}
			if live {
				item.Status, item.Reason, item.CandidateID = "merged", "duplicate_claim", existing.Target.ID
				return item, nil
			}
		}
		// The earlier memory was deleted, rejected or expired, or its candidate never got past the daily
		// limit (no target): saying it again is a new statement, not a duplicate of nothing.
		stale = &existing
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return item, err
	}

	if len(c.ArchiveMessages) > 0 {
		if in.Event.SessionID == "" {
			return item, ErrInvalid
		}
		role := ""
		if c.Basis == "explicit_user" {
			role = "user"
		}
		_, _, err := s.validateArchiveMessageRefs(ctx, token, scope, c.ArchiveMessages, in.Event.SessionID, role)
		if err != nil {
			return item, err
		}
	}
	var canonicalSources []SourceRef
	var src Record
	for _, ref := range c.SourceRefs {
		r, e := s.Get(ctx, token, "sources", scope, ref.ID)
		if e != nil {
			return item, e
		}
		if r.Revision != ref.Revision || !live(r) || r.ContentErased {
			return item, ErrConflict
		}
		if src.ID == "" {
			src = r
		}
		canonicalSources = append(canonicalSources, sourceReference(r, "original"))
	}
	if len(c.SourceRefs) == 0 || len(c.ArchiveMessages) > 0 {

		sourceText := c.Text
		var sourceGenerator *Generator
		if c.MemoryContext != nil {
			sourceGenerator = in.Generator
			sourceText += "\n[추출된 맥락 · 사용자 원문 아님]\n" + memoryContextKey(c.MemoryContext)
		}
		if c.Supersedes != nil {
			sourceGenerator = in.Generator
			sourceText += "\n[정정 연결 메타데이터 · 사용자 원문 아님]\n" + structuredText(c.Supersedes) + "\n" + in.Event.ID
		}

		src, err = s.Create(ctx, token, "sources", CreateInput{
			ArchiveMessages: c.ArchiveMessages,
			Scope:           scope, Kind: "message", Text: sourceText, Generator: sourceGenerator, Generated: sourceGenerator != nil, OriginProduct: in.Event.OriginProduct,
			SourceRef: "capture:" + in.Event.Type + ":" + in.Event.ID, CollectedAt: in.Event.OccurredAt,
			IdempotencyKey: "cap-" + ContentHash(key + "\n" + in.Event.ID)[:60], OnSecret: "block",
		})
		if errors.Is(err, ErrConflict) && stale != nil {
			// A restatement of a withdrawn claim: its original source still
			// stands as evidence, so the new candidate cites it.
			src, err = s.staleCaptureSource(ctx, scope, *stale)
		}
		if errors.Is(err, ErrConflict) {
			// The same text already exists as a source with other provenance.
			item.Status, item.Reason = "merged", "duplicate_source"
			return item, nil
		}
		if err != nil {
			return item, err
		}
		ref := sourceReference(src, "original")
		if (c.MemoryContext != nil || c.Supersedes != nil) && ref.Span != nil {
			ref.Span.End = len([]rune(c.Text))
		}
		canonicalSources = append(canonicalSources, ref)
	}

	// Persist source provenance before candidate creation for correction retries.
	if c.Supersedes != nil && stale == nil {
		at := time.Now().UTC()
		pending := Record{ID: key, Scope: scope, Kind: "capture_pointer", Status: "correction_pending", Revision: 1, Sources: canonicalSources, CreatedAt: at, UpdatedAt: at}
		if err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error { return tx.Insert(ctx, "jobs", pending) }); err != nil {
			return item, err
		}
		stale = &pending
	}
	tags := []string{"capture", "event:" + in.Event.Type, "basis:" + c.Basis}
	if c.Supersedes != nil {
		tags = append(tags, pendingCaptureCorrectionTag)
	}
	// Event, session and trace ids travel as tags: the record's own
	// source_event_id/trace_id must name stored evidence, which these are not.
	tags = append(tags, "event_id:"+in.Event.ID)
	if in.Event.SessionID != "" {
		tags = append(tags, "session:"+in.Event.SessionID)
	}
	if in.Event.TraceID != "" {
		tags = append(tags, "trace:"+in.Event.TraceID)
	}
	if c.Repo != "" && c.Kind != "user" {
		tags = append(tags, "repo:"+c.Repo)
	}
	// The tag outlives this call, so no automatic approver can promote it later either.
	suspect := instructionLike.MatchString(structuredText(c))
	if suspect {
		tags = append(tags, "review:instruction_like")
	}
	cand, err := s.Create(ctx, token, "memory_candidates", CreateInput{
		MemoryContext: c.MemoryContext,
		Scope:         scope, Kind: c.Kind, Text: c.Text, Summary: c.Reason, Keywords: []string{c.Subject}, Tags: tags,
		Sources: canonicalSources, Confidence: c.Confidence, Generated: true, Generator: in.Generator,
		SourceEventID: src.ID, Relations: c.SourceRefs, OnSecret: "block",
	})
	if errors.Is(err, ErrCandidateQuota) && stale == nil {
		// The source is already stored: point at it, so a retry after the limit resets cites it
		// instead of meeting its own text as a "duplicate source" and losing the claim.
		// If the pointer cannot be written, that error (retryable) is returned, not the limit: the caller must not
		// record the event as done while nothing points at the stored source.
		at := time.Now().UTC().Truncate(time.Millisecond)
		if e := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			return tx.Insert(ctx, "jobs", Record{ID: key, Scope: scope, Kind: "capture_pointer", Status: "done", Revision: 1,
				Sources: canonicalSources, CreatedAt: at, UpdatedAt: at})
		}); e != nil {
			return item, e
		}
	}
	if err != nil {
		return item, err
	}
	item.CandidateID = cand.ID
	now := time.Now().UTC().Truncate(time.Millisecond)
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		// The pointer also names its source, which outlives a deleted memory.
		pointer := Record{ID: key, Scope: scope, Kind: "capture_pointer", Status: "done", Revision: 1, Sources: canonicalSources,
			Target: &Ref{Collection: "memory_candidates", ID: cand.ID, Revision: cand.Revision}, CreatedAt: now, UpdatedAt: now}
		if c.Supersedes != nil {
			pointer.Status = "correction_pending"
		}
		if stale != nil {
			pointer.Revision, pointer.CreatedAt = stale.Revision+1, stale.CreatedAt
			return tx.Replace(ctx, "jobs", pointer, stale.Revision)
		}
		return tx.Insert(ctx, "jobs", pointer)
	})
	if err != nil {
		if c.Supersedes != nil {
			return item, err
		}
		// shortcut: a concurrent twin can leave one extra unreviewed candidate;
		// the pointer still wins, so later captures merge. Lock per key if this shows up.
		item.Status, item.Reason = "merged", "concurrent_duplicate"
		return item, nil
	}

	switch {
	case c.Supersedes != nil:
		pointer, e := s.Repo.Get(ctx, "jobs", scope, key)
		if e != nil {
			return item, e
		}
		return s.finishCaptureCorrection(ctx, token, in, c, scope, pointer, item)
	case c.Basis != "explicit_user" || !slices.Contains(capturePromotableKinds, c.Kind):
		item.Status, item.Reason = "candidate", "review_required"
		return item, nil
	case suspect:
		item.Status, item.Reason = "candidate", "review_required:instruction_like"
		return item, nil
	}
	if _, err = s.ReviewCandidate(ctx, token, cand.ID, CandidateReviewInput{Scope: scope, ExpectedRevision: cand.Revision, Action: "approve"}); err != nil {
		// Only a human principal passes the review gate; anything else waits.
		item.Status, item.Reason = "candidate", "review_required:"+captureErrorCode(err)
		return item, nil
	}
	item.Status, item.Reason, item.MemoryID = "accepted", "explicit_user_statement", cand.ID
	return item, nil
}

// captureTargetLive reports whether a pointer's claim still stands: a pending
// or conflicted candidate, or an approved one whose memory is still active.
func (s *Service) captureTargetLive(ctx context.Context, scope Scope, id string) (bool, error) {
	live := false
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		cand, e := tx.Get(ctx, "memory_candidates", scope, id)
		if errors.Is(e, ErrNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		switch cand.Status {
		case "candidate", "conflicted":
			live = true
		case "approved":
			m, e := tx.Get(ctx, "memories", scope, id)
			if errors.Is(e, ErrNotFound) {
				return nil
			}
			if e != nil {
				return e
			}
			live = m.Status == "active" && !m.ContentErased
		}
		return nil
	})
	return live, err
}

// staleCaptureSource returns the live source a withdrawn claim cited: the one
// its pointer names, else the one its (rejected) candidate names.
func (s *Service) staleCaptureSource(ctx context.Context, scope Scope, pointer Record) (Record, error) {
	var src Record
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		refs := pointer.Sources
		if pointer.Target == nil {
			// A pointer left by a quota rejection names only its source.
		} else if cand, e := tx.Get(ctx, "memory_candidates", scope, pointer.Target.ID); e == nil {
			refs = append(refs, cand.Sources...)
		}
		for _, ref := range refs {
			if r, e := tx.Get(ctx, "sources", scope, ref.SourceID); e == nil && live(r) && !r.ContentErased {
				src = r
				return nil
			}
		}
		return ErrConflict
	})
	return src, err
}

func captureErrorCode(err error) string {
	for _, e := range []error{ErrSecretDetected, ErrDenied, ErrInvalid, ErrConflict, ErrNotFound, ErrCandidateQuota, ErrUnavailable} {
		if errors.Is(err, e) {
			return e.Error()
		}
	}
	return "internal_error"
}
