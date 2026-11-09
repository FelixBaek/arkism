package memory

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Capture is the single write path for automatically captured memory. Clients
// (Claude Code hooks, other agent events) only extract claims; privacy, scope,
// dedupe and promotion live here so every client behaves the same way.
//
// Extraction never approves a claim, even with a human owner's credential or
// basis=explicit_user. A person must review the resulting candidate separately.
// Corrections never overwrite and are marked as conflicts.

// captureAutomaticEvents are the events a hook extracts on its own; a space with extraction off refuses them.
var captureAutomaticEvents = []string{"turn_completed", "run_completed", "handoff_completed", "session_end"}
var captureEventTypes = []string{"turn_completed", "run_completed", "handoff_completed", "user_corrected_fact", "explicit_remember", "session_end", "backfill"}
var captureKinds = []string{"user", "decision", "procedural", "lesson", "episodic", "semantic"}
var captureBases = []string{"explicit_user", "assistant_result", "inferred"}

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
	// The extractor's risk call (ADR-0022): it orders the review list and never approves anything.
	Risk         string   `json:"risk,omitempty"`
	RiskReasons  []string `json:"risk_reasons,omitempty"`
	EvidenceSpan string   `json:"evidence_span,omitempty"`
	DupHint      string   `json:"dup_hint,omitempty"`
	// OccurredFrom/OccurredTo are the event dates the content states (a day, month or RFC 3339
	// time; relative words are read against the event time). Bad dates are dropped, not trusted.
	OccurredFrom string `json:"occurred_from,omitempty"`
	OccurredTo   string `json:"occurred_to,omitempty"`
	// Tags and Metadata are the caller's labels; the candidate carries them as
	// plain and meta:<key>=<value> tags.
	Tags     []string          `json:"tags,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	occurred *RecallTimeWindow
	// locator names where in the source the claim came from (a file page or
	// chunk); only server-side extraction sets it, clients always get "original".
	locator string

	// Entities the caller states for this claim (EntityInput); they go on the candidate.
	Entities []EntityInput `json:"entities,omitempty"`
	// Relations between two stated entity names (capture_labels.go); edges only after approval.
	Relations []EntityRelationInput `json:"relations,omitempty"`
	// Rule marks a standing always/never rule: a person's approval also makes it a directive.
	Rule string `json:"rule,omitempty"`
	// DecisionStatus (decision claims only) is proposed, confirmed or dropped.
	DecisionStatus string `json:"decision_status,omitempty"`
	// Temporary marks a "this time only" request; it gets a default expiry.
	Temporary bool `json:"temporary,omitempty"`
	// labels are the document's reviewer tags; only server-side extraction sets them.
	labels []string
}

// classification is nil for a claim without a risk call.
func (c CaptureClaim) classification() *CandidateClassification {
	if c.Risk == "" {
		return nil
	}
	return &CandidateClassification{Risk: c.Risk, Reasons: c.RiskReasons, EvidenceSpan: c.EvidenceSpan, DupHint: c.DupHint}
}

type CaptureInput struct {
	Automated bool           `json:"automated,omitempty"`
	Scope     Scope          `json:"scope"`
	Event     CaptureEvent   `json:"event"`
	Claims    []CaptureClaim `json:"claims"`
	Generator *Generator     `json:"generator"`
	// zone reads the claims' relative dates; the space's default time zone when unset.
	zone *time.Location
	// gateUntil ends the merge gate's wait on the embedder for the whole request (captureGateBudget).
	gateUntil time.Time
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
	return validCaptureClaimEvidence(c) && validCaptureClaimClassification(c) && validCaptureClaimFields(c) && validClaimExtras(c)
}

func validCaptureClaimEvidence(c CaptureClaim) bool {
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
	return true
}

func validCaptureClaimClassification(c CaptureClaim) bool {
	k := c.classification()
	return !(k == nil && (len(c.RiskReasons) > 0 || c.EvidenceSpan != "" || c.DupHint != "") || k != nil && !validClassification(*k))
}

func validCaptureClaimFields(c CaptureClaim) bool {
	return strings.TrimSpace(c.Text) != "" && len(c.Text) <= 4000 && validRunLabel(c.Subject, 128) && len(c.Reason) <= 500 &&
		len(c.OccurredFrom) <= 64 && len(c.OccurredTo) <= 64 &&
		(c.Repo == "" || validRunLabel(c.Repo, 128)) && validEntityInputs(c.Entities) && validClaimLabels(c.Tags, c.Metadata, nil) &&
		slices.Contains(captureKinds, c.Kind) && slices.Contains(captureBases, c.Basis) &&
		(c.Supersedes == nil || c.Supersedes.Collection == "memories" && c.Supersedes.ID != "" && c.Supersedes.Revision > 0)
}

var claimMetaKey = regexp.MustCompile(`^[a-z0-9_.-]{1,32}$`)

// validClaimLabels checks a caller's labels: plain tags may not look like system tags (no
// ':'), and every label and entity name passes the tag privacy filters.
func validClaimLabels(tags []string, meta map[string]string, entities []string) bool {
	if len(tags)+len(meta)+len(entities) > extractLabelsMax || slices.ContainsFunc(tags, func(t string) bool { return strings.Contains(t, ":") }) {
		return false
	}
	for k, v := range meta {
		if !claimMetaKey.MatchString(k) || !validRunLabel(v, 80) {
			return false
		}
	}
	for _, e := range entities {
		if !validRunLabel(e, 80) {
			return false
		}
	}
	return validTagInput(append(claimLabelTags(tags, meta), entities...))
}

// claimLabelTags is the caller's labels as candidate tags, metadata in key order.
func claimLabelTags(tags []string, meta map[string]string) []string {
	out := slices.Clone(tags)
	for _, k := range slices.Sorted(maps.Keys(meta)) {
		out = append(out, "meta:"+k+"="+meta[k])
	}
	return out
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

// Capture stores claims as candidates; an extension's OperationHook may refuse it first.
func (s *Service) Capture(ctx context.Context, token string, in CaptureInput) (CaptureResult, error) {
	return withOperation(s, ctx, token, OpCapture, in.Scope, in, func() (CaptureResult, error) { return s.capture(ctx, token, in) })
}

func (s *Service) capture(ctx context.Context, token string, in CaptureInput) (CaptureResult, error) {
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
	if in.Automated && actor.Role != roleService { // automated captures come from service principals only
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
	// Space defense rules run before every privacy and secret check (space_settings.go).
	settings, err := s.spaceSettings(ctx, in.Scope)
	if err != nil {
		return CaptureResult{}, err
	}
	in.zone = cmp.Or(in.zone, settings.zone())
	in.gateUntil = started.Add(s.captureGateBudget())
	out := CaptureResult{Items: make([]CaptureItem, len(in.Claims))}
	transient := false
	for i, c := range in.Claims {
		item, err := s.defendedClaim(ctx, token, in, settings, c)
		if err != nil {
			item = CaptureItem{Status: "rejected", Reason: captureErrorCode(err), DedupeKey: item.DedupeKey}
			transient = transient || captureTransient(err, c, item.Reason)
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
		// Without the receipt a retry runs the capture again; the result already stands.
		if err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			return tx.Insert(ctx, "jobs", Record{ID: receipt, Scope: in.Scope, Kind: "capture_receipt", Status: "done", Revision: 1, CreatedAt: now, UpdatedAt: now})
		}); err != nil {
			slog.Warn("memory capture receipt not stored", "workspace", in.Scope.WorkspaceID, "error", err)
		}
	}
	if capturedAny(out) {
		s.autoApplySpaceTemplate(ctx, token, actor, in.Scope, false)
		s.noteDreamActivity(ctx, in.Scope)
		if in.Scope.AgentID != "" { // a user's own statement lands without the agent (captureClaimScope)
			s.noteDreamActivity(ctx, Scope{WorkspaceID: in.Scope.WorkspaceID, ProjectID: in.Scope.ProjectID, UserID: in.Scope.UserID})
		}
	}
	return out, nil
}

// defendedClaim applies the space settings to a claim before capturing it: a space
// with extraction off takes no automatic captures, and the defense rules run on its texts.
func (s *Service) defendedClaim(ctx context.Context, token string, in CaptureInput, settings SpaceConfig, c CaptureClaim) (CaptureItem, error) {
	if !settings.ExtractionEnabled && slices.Contains(captureAutomaticEvents, in.Event.Type) {
		return CaptureItem{}, ErrExtractionDisabled
	}
	texts, labelsBack := claimLabelTexts(&c)
	if err := s.defendWith(ctx, "", defenseGuard{action: "capture"}, in.Scope, settings.DefenseRules, append([]*string{&c.Subject, &c.Text, &c.Reason, &c.EvidenceSpan}, texts...)...); err != nil {
		return CaptureItem{}, err
	}
	labelsBack()
	return s.captureClaim(ctx, token, in, c)
}

// claimLabelTexts exposes copies of the claim's labels to the defense rules; the returned
// func writes the (possibly redacted) metadata values back. The caller's slices stay untouched.
func claimLabelTexts(c *CaptureClaim) ([]*string, func()) {
	c.Tags, c.Entities, c.Relations = slices.Clone(c.Tags), slices.Clone(c.Entities), slices.Clone(c.Relations)
	keys := slices.Sorted(maps.Keys(c.Metadata))
	values := make([]string, len(keys))
	texts := []*string{}
	for i, k := range keys {
		values[i] = c.Metadata[k]
		texts = append(texts, &values[i])
	}
	for i := range c.Tags {
		texts = append(texts, &c.Tags[i])
	}
	for i := range c.Entities {
		texts = append(texts, &c.Entities[i].Name)
	}
	for i := range c.Relations { // redacted alike, so a relation still names its redacted entity
		texts = append(texts, &c.Relations[i].Source, &c.Relations[i].Target)
	}
	return texts, func() {
		if len(keys) > 0 {
			c.Metadata = map[string]string{}
			for i, k := range keys {
				c.Metadata[k] = values[i]
			}
		}
	}
}

// captureTransient reports a rejection a retry may get past, so the event keeps no receipt.
func captureTransient(err error, c CaptureClaim, reason string) bool {
	return c.Supersedes != nil || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrDenied) || errors.Is(err, ErrConflict) || errors.Is(err, ErrCandidateQuota) || reason == "internal_error"
}

func (s *Service) captureClaim(ctx context.Context, token string, in CaptureInput, c CaptureClaim) (CaptureItem, error) {
	if rejected, ok := s.rejectCaptureClaim(ctx, in, c); ok {
		return rejected, nil
	}
	ctx, scope := captureClaimScope(ctx, in, c)
	c, key, legacyKey := captureClaimKeys(in, c, scope)
	item := CaptureItem{DedupeKey: key}
	if _, err := s.authorize(ctx, token, "read:memory_candidates", scope, "", ""); err != nil {
		return item, err
	}
	if err := s.captureArchiveRefs(ctx, token, in, c, scope); err != nil {
		return item, err
	}
	canonicalSources, src, err := s.captureSourceRefs(ctx, token, c, scope)
	if err != nil {
		return item, err
	}

	existing, key, err := s.captureExistingPointer(ctx, c, scope, key, legacyKey)
	item.DedupeKey = key
	var stale *Record
	if err == nil && existing.Kind == "capture_pointer" {
		if out, done, e := s.captureFromPointer(ctx, token, in, c, scope, existing, item); done {
			return out, e
		}
		// The earlier memory was deleted, rejected or expired, or its candidate never got past the daily
		// limit (no target): saying it again is a new statement, not a duplicate of nothing.
		stale = &existing
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return item, err
	}

	write := newCaptureWrite(key, scope, stale, c)
	write.prefixSources = canonicalSources
	prepared, preparedSource, merged, err := s.capturePreparation(ctx, token, in, c, scope, key, write, stale, canonicalSources, src)
	if err != nil {
		return item, err
	}
	if merged {
		item.Status, item.Reason = "merged", "duplicate_source"
		return item, nil
	}
	if prepared.Status == "done" {
		return s.captureConcurrentDone(ctx, scope, prepared, item)
	}
	write.prepared, canonicalSources, src = prepared, prepared.Sources, preparedSource

	tags, suspect := captureTags(in, c)
	cand, err := s.Create(ctx, token, "memory_candidates", CreateInput{
		capture:       write,
		MemoryContext: c.MemoryContext, Occurred: c.occurred,
		Scope: scope, Kind: c.Kind, Text: c.Text, Summary: c.Reason, Keywords: []string{c.Subject}, Tags: tags, Entities: c.Entities, entityRelations: c.Relations,
		ExpiresAt: claimExpiry(c, time.Now()),
		Sources:   canonicalSources, Confidence: c.Confidence, Generated: true, Generator: in.Generator,
		SourceEventID: src.ID, Relations: c.SourceRefs, OnSecret: "block", Classification: c.classification(),
	})
	if err != nil {
		if c.Supersedes != nil || !errors.Is(err, ErrConflict) {
			return item, err
		}
		return s.captureCandidateConflict(ctx, scope, key, item, err)
	}

	item.CandidateID = cand.ID
	switch {
	case c.Supersedes != nil:
		pointer, e := s.Repo.Get(ctx, "jobs", scope, key)
		if e != nil {
			return item, e
		}
		return s.finishCaptureCorrection(ctx, token, in, c, scope, pointer, item)
	case suspect:
		item.Status, item.Reason = "candidate", "review_required:instruction_like"
		return item, nil
	}
	if s.captureMergeGate(ctx, token, in, scope, cand) {
		item.Status, item.Reason = "candidate", "merge_proposed"
		return item, nil
	}
	// Authorship describes evidence, not consent to store the extracted wording.
	item.Status, item.Reason = "candidate", "review_required"
	return item, nil
}

func (s *Service) rejectCaptureClaim(ctx context.Context, in CaptureInput, c CaptureClaim) (CaptureItem, bool) {
	if !validCaptureClaim(c) {
		return CaptureItem{Status: "rejected", Reason: "invalid_claim"}, true
	}
	if containsSecret(structuredText(c)) {
		// the block itself is audited; nothing of the claim is copied.
		s.auditDeniedInput(ctx, "capture:secret_detected", in.Scope, "")
		return CaptureItem{Status: "rejected", Reason: "secret_detected"}, true
	}
	return CaptureItem{}, false
}

func captureClaimScope(ctx context.Context, in CaptureInput, c CaptureClaim) (context.Context, Scope) {
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
	return ctx, scope
}

// captureClaimKeys stamps the claim's own memory context copy with the event
// time, resolves its event span against that time, and returns it with the evidence-bound key and the legacy key.
func captureClaimKeys(in CaptureInput, c CaptureClaim, scope Scope) (CaptureClaim, string, string) {
	c.MemoryContext = copyMemoryContext(c.MemoryContext)
	text := c.Text
	if c.locator != "" { // a file's claim was read later than the file was written: "어제" there is not the day before the read
		text = ""
	}
	c.occurred = resolveOccurred(c.OccurredFrom, c.OccurredTo, text, in.Event.OccurredAt.In(cmp.Or(in.zone, usageZone())))
	if c.MemoryContext != nil {
		at := in.Event.OccurredAt.UTC().Truncate(time.Millisecond)
		c.MemoryContext.ObservedAt = &at
	}
	key := captureKey(scope, c.Kind, c.Text, c.Supersedes)
	if c.MemoryContext != nil {
		key = "capture-" + ContentHash(key+"\n"+memoryContextKey(c.MemoryContext))
	}
	legacyKey := key
	if c.Kind == "episodic" && c.occurred != nil {
		key = "capture-" + ContentHash(key+"\nevent-v1\n"+structuredText(c.occurred))
	}
	return c, captureEvidenceKey(key, c), legacyKey
}

func (s *Service) captureArchiveRefs(ctx context.Context, token string, in CaptureInput, c CaptureClaim, scope Scope) error {
	if len(c.ArchiveMessages) == 0 {
		return nil
	}
	if in.Event.SessionID == "" {
		return ErrInvalid
	}
	role := ""
	if c.Basis == "explicit_user" {
		role = "user"
	}
	_, _, err := s.validateArchiveMessageRefs(ctx, token, scope, c.ArchiveMessages, in.Event.SessionID, role)
	return err
}

func (s *Service) captureSourceRefs(ctx context.Context, token string, c CaptureClaim, scope Scope) ([]SourceRef, Record, error) {
	var canonicalSources []SourceRef
	var src Record
	for _, ref := range c.SourceRefs {
		r, e := s.Get(ctx, token, "sources", scope, ref.ID)
		if e != nil {
			return nil, Record{}, e
		}
		if r.Revision != ref.Revision || !live(r) || r.ContentErased {
			return nil, Record{}, ErrConflict
		}
		if src.ID == "" {
			src = r
		}
		canonicalSources = append(canonicalSources, sourceReference(r, cmp.Or(c.locator, "original")))
	}
	return canonicalSources, src, nil
}

// captureExistingPointer returns the pointer stored under key, or under
// legacyKey when that older pointer proves the same evidence, with the key used.
func (s *Service) captureExistingPointer(ctx context.Context, c CaptureClaim, scope Scope, key, legacyKey string) (Record, string, error) {
	var existing Record
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		var e error
		existing, e = tx.Get(ctx, "jobs", scope, key)
		if e == nil && key == legacyKey && existing.Kind == "capture_pointer" && existing.Target != nil {
			candidate, candidateErr := tx.Get(ctx, "memory_candidates", scope, existing.Target.ID)
			if candidateErr != nil && !errors.Is(candidateErr, ErrNotFound) {
				return candidateErr
			}
			if candidateErr == nil && !captureLegacyEvidenceMatches(candidate, c) {
				// A legacy scoped claim cannot stand in for an unscoped one.
				// Fail explicitly instead of silently dropping the new context.
				return ErrConflict
			}
		}
		if errors.Is(e, ErrNotFound) && key != legacyKey {
			// Old pointers did not bind repository or explicit source identities.
			// Reuse one only when its immutable candidate proves the same context.
			old, oldErr := tx.Get(ctx, "jobs", scope, legacyKey)
			if oldErr != nil && !errors.Is(oldErr, ErrNotFound) {
				return oldErr
			}
			if oldErr == nil && old.Kind == "capture_pointer" && old.Target != nil {
				candidate, candidateErr := tx.Get(ctx, "memory_candidates", scope, old.Target.ID)
				if candidateErr != nil && !errors.Is(candidateErr, ErrNotFound) {
					return candidateErr
				}
				if candidateErr == nil && captureLegacyEvidenceMatches(candidate, c) {
					existing, e, key = old, nil, legacyKey
				}
			}
		}
		return e
	})
	return existing, key, err
}

// captureFromPointer settles a claim an existing pointer already answers;
// done is false when the pointer is stale and the claim must be captured anew.
func (s *Service) captureFromPointer(ctx context.Context, token string, in CaptureInput, c CaptureClaim, scope Scope, existing Record, item CaptureItem) (CaptureItem, bool, error) {
	if existing.Target != nil && existing.Status == "correction_pending" && c.Supersedes != nil {
		item.CandidateID = existing.Target.ID
		out, err := s.finishCaptureCorrection(ctx, token, in, c, scope, existing, item)
		return out, true, err
	}
	if existing.Target != nil {
		live, e := s.captureTargetLive(ctx, scope, existing.Target.ID)
		if e != nil {
			return item, true, e
		}
		if live {
			item.Status, item.Reason, item.CandidateID = "merged", "duplicate_claim", existing.Target.ID
			return item, true, nil
		}
	}
	return item, false, nil
}

// capturePreparation loads the claim's preparation, storing it first when
// missing; merged reports that another provenance already holds the source.
func (s *Service) capturePreparation(ctx context.Context, token string, in CaptureInput, c CaptureClaim, scope Scope, key string, write *captureWrite, stale *Record, canonicalSources []SourceRef, src Record) (Record, Record, bool, error) {
	prepared, preparedSource, prepareErr := s.loadCapturePreparation(ctx, token, write)
	if missingPreparation(prepareErr) {
		merged, err := s.captureEnsurePreparation(ctx, token, in, c, scope, key, write, stale, canonicalSources, src)
		if err != nil || merged {
			return Record{}, Record{}, merged, err
		}
		prepared, preparedSource, prepareErr = s.loadCapturePreparation(ctx, token, write)
	}
	return prepared, preparedSource, false, prepareErr
}

// captureEnsurePreparation stores the claim's source evidence and preparation;
// merged reports that another provenance already holds the same source.
func (s *Service) captureEnsurePreparation(ctx context.Context, token string, in CaptureInput, c CaptureClaim, scope Scope, key string, write *captureWrite, stale *Record, canonicalSources []SourceRef, src Record) (bool, error) {
	if len(c.SourceRefs) > 0 && len(c.ArchiveMessages) == 0 {
		err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			return write.prepare(ctx, tx, canonicalSources, src.ID)
		})
		if err != nil && !errors.Is(err, ErrConflict) {
			return false, err
		}
		return false, nil
	}
	sourceText, sourceGenerator := captureSourceText(in, c)
	_, err := s.Create(ctx, token, "sources", CreateInput{
		capture:         write,
		ArchiveMessages: c.ArchiveMessages,
		Scope:           scope, Kind: "message", Text: sourceText, Generator: sourceGenerator, Generated: sourceGenerator != nil, OriginProduct: in.Event.OriginProduct,
		SourceRef: "capture:" + in.Event.Type + ":" + in.Event.ID, CollectedAt: in.Event.OccurredAt,
		IdempotencyKey: "cap-" + ContentHash(key + "\n" + in.Event.ID)[:60], OnSecret: "block",
	})
	if errors.Is(err, ErrConflict) {
		return s.captureSourceConflict(ctx, token, scope, write, stale, canonicalSources, err)
	}
	return false, err
}

// captureSourceText labels every non-user addition to the claim text, and
// names the generator whenever the source is not purely the user's words.
func captureSourceText(in CaptureInput, c CaptureClaim) (string, *Generator) {
	sourceText := c.Text
	if c.Kind == "episodic" && c.occurred != nil {
		sourceText += "\n[해석된 사건 시각 · 사용자 원문 아님]\n" + structuredText(c.occurred)
	}
	var sourceGenerator *Generator
	if c.Kind == "episodic" && c.occurred != nil {
		sourceGenerator = in.Generator
	}
	if c.Repo != "" && c.Kind != "user" {
		sourceGenerator = in.Generator
		sourceText += "\n[저장소 맥락 메타데이터 · 사용자 원문 아님]\n" + structuredText(c.Repo)
	}
	if c.MemoryContext != nil {
		sourceGenerator = in.Generator
		sourceText += "\n[추출된 맥락 · 사용자 원문 아님]\n" + memoryContextKey(c.MemoryContext)
	}
	if c.Supersedes != nil {
		sourceGenerator = in.Generator
		sourceText += "\n[정정 연결 메타데이터 · 사용자 원문 아님]\n" + structuredText(c.Supersedes) + "\n" + in.Event.ID
	}
	return sourceText, sourceGenerator
}

func (s *Service) captureSourceConflict(ctx context.Context, token string, scope Scope, write *captureWrite, stale *Record, canonicalSources []SourceRef, err error) (bool, error) {
	// Another event may have committed the source/preparation first.
	_, _, prepareErr := s.loadCapturePreparation(ctx, token, write)
	if prepareErr == nil {
		return false, nil
	} else if !missingPreparation(prepareErr) {
		return false, prepareErr
	} else if stale != nil {
		// Legacy pointers predate preparation records. Reuse only their
		// verified live evidence, preserving immutable provenance.
		src, originalRef, err := s.staleCaptureEvidence(ctx, scope, *stale)
		if err == nil {
			canonicalSources = append(canonicalSources, originalRef)
			err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
				return write.prepare(ctx, tx, canonicalSources, src.ID)
			})
		}
		return false, err
	} else if errors.Is(err, errSourceOtherProvenance) {
		return true, nil
	} else if errors.Is(err, errSourceDeleted) {
		return false, errPreparedSourceGone // final not_found: retrying cannot bring the source back
	}
	return false, err
}

func (s *Service) captureConcurrentDone(ctx context.Context, scope Scope, prepared Record, item CaptureItem) (CaptureItem, error) {
	if prepared.Target == nil || prepared.Target.Collection != "memory_candidates" {
		return item, ErrConflict
	}
	stands, err := s.captureTargetLive(ctx, scope, prepared.Target.ID)
	if err != nil {
		return item, err
	}
	if !stands {
		return item, ErrConflict
	}
	item.Status, item.Reason, item.CandidateID = "merged", "concurrent_duplicate", prepared.Target.ID
	return item, nil
}

func captureTags(in CaptureInput, c CaptureClaim) ([]string, bool) {
	tags := []string{"capture", "event:" + in.Event.Type, "basis:" + c.Basis}
	if c.Supersedes != nil {
		tags = append(tags, pendingCaptureCorrectionTag)
	}
	// Event, session and trace ids travel as tags: the record's own
	// source_event_id/trace_id must name stored evidence, which these are not.
	tags = append(tags, "event_id:"+in.Event.ID)
	tags = append(tags, c.labels...)
	if in.Event.SessionID != "" {
		tags = append(tags, "session:"+in.Event.SessionID)
	}
	if in.Event.TraceID != "" {
		tags = append(tags, "trace:"+in.Event.TraceID)
	}
	if c.Repo != "" && c.Kind != "user" {
		tags = append(tags, "repo:"+c.Repo)
	}
	tags = wikiUnion(tags, append(claimLabelTags(c.Tags, c.Metadata), claimExtraTags(c)...))
	// The tag outlives this call, so no automatic approver can promote it later either.
	suspect := instructionLike.MatchString(structuredText(c))
	if suspect {
		tags = append(tags, "review:instruction_like")
	}
	return tags, suspect
}

func (s *Service) captureCandidateConflict(ctx context.Context, scope Scope, key string, item CaptureItem, err error) (CaptureItem, error) {
	// A conflict is a concurrent duplicate only if a committed, live
	// pointer won. Storage/policy failures must leave this event retryable.
	winner, readErr := s.Repo.Get(ctx, "jobs", scope, key)
	if errors.Is(readErr, ErrNotFound) {
		return item, err
	}
	if readErr != nil {
		return item, readErr
	}
	if winner.Kind != "capture_pointer" || winner.Status != "done" || winner.Target == nil || winner.Target.Collection != "memory_candidates" {
		return item, err
	}
	stands, readErr := s.captureTargetLive(ctx, scope, winner.Target.ID)
	if readErr != nil {
		return item, readErr
	}
	if !stands {
		return item, err
	}
	// The losing candidate transaction rolled back. Return the durable
	// winner rather than the discarded local candidate ID.
	item.Status, item.Reason, item.CandidateID = "merged", "concurrent_duplicate", winner.Target.ID
	return item, nil
}

// captureTargetLive reports whether a pointer's claim still stands: a pending
// or conflicted candidate, or an approved one whose memory is still active.
func (s *Service) captureTargetLive(ctx context.Context, scope Scope, id string) (bool, error) {
	targetLive := false
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		cand, e := tx.Get(ctx, "memory_candidates", scope, id)
		if errors.Is(e, ErrNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		if cand.ContentErased || cand.PrivacyQuarantined {
			return nil
		}
		switch cand.Status {
		case "candidate", "conflicted":
			targetLive = cand.ExpiresAt == nil || cand.ExpiresAt.After(time.Now())
		case "approved":
			m, e := tx.Get(ctx, "memories", scope, id)
			if errors.Is(e, ErrNotFound) {
				return nil
			}
			if e != nil {
				return e
			}
			targetLive = live(m) && !m.ContentErased
		}
		return nil
	})
	return targetLive, err
}

// staleCaptureSource returns the live source a withdrawn claim cited: the one
// its pointer names, else the one its (rejected) candidate names.
func (s *Service) staleCaptureSource(ctx context.Context, scope Scope, pointer Record) (Record, error) {
	src, _, err := s.staleCaptureEvidence(ctx, scope, pointer)
	return src, err
}

func (s *Service) staleCaptureEvidence(ctx context.Context, scope Scope, pointer Record) (Record, SourceRef, error) {
	var src Record
	var originalRef SourceRef
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		refs := append([]SourceRef(nil), pointer.Sources...)
		if pointer.Target != nil {
			cand, e := tx.Get(ctx, "memory_candidates", scope, pointer.Target.ID)
			if e != nil && !errors.Is(e, ErrNotFound) {
				return e
			}
			if e == nil {
				refs = append(refs, cand.Sources...)
			}
		}
		invalid := false
		for _, ref := range refs {
			r, e := tx.Get(ctx, "sources", scope, ref.SourceID)
			if errors.Is(e, ErrNotFound) {
				continue
			}
			if e != nil {
				return e
			}
			if live(r) && !r.ContentErased {
				src, originalRef = r, ref
				if originalRef.Span == nil {
					originalRef = sourceReference(r, "original")
				}
				return nil
			}
			invalid = true
		}
		if invalid {
			return ErrConflict
		}
		return ErrNotFound
	})
	return src, originalRef, err
}

func captureErrorCode(err error) string {
	for _, e := range []error{ErrSecretDetected, ErrDefenseBlocked, ErrExtractionDisabled, ErrDenied, ErrInvalid, ErrConflict, ErrNotFound, ErrCandidateQuota, ErrUnavailable} {
		if errors.Is(err, e) {
			return e.Error()
		}
	}
	return "internal_error"
}
