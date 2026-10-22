package memory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

type Service struct {
	ModelBudgetPolicy       *ModelBudgetPolicy
	MemoryReviewPolicies    []MemoryReviewPolicy
	SkillAutomationPolicies []SkillAutomationPolicy
	// GoogleLogin, when set, lets the desktop app trade a Google sign-in for its token.
	GoogleLogin *GoogleLogin
	// Unsplash, when set, searches card background photos for teams and automations.
	Unsplash *Unsplash
	// ReadinessToken binds /readyz's blob-root check to the service principal
	// on Postgres, the same binding startup uses. Empty elsewhere.
	ReadinessToken string
	// ReflectionToken is the service principal that evaluates failed
	// skill.usage traces and records their lessons. Empty disables it.
	ReflectionToken string
	// SkillScope is the operator's canonical skill scope, consulted last when
	// validating skill.usage events recorded in another runtime's scope.
	SkillScope Scope
	// SessionArchiveKey is the 32-byte AES-256-GCM key for session-archive
	// chunks. Unset or wrong-length keys make chunk writes and raw reads
	// unavailable; nothing else in the service shares this key.
	SessionArchiveKey []byte
	// SessionArchiveNow replaces the service clock in tests so expiry
	// boundaries are exact. Nil means time.Now.
	SessionArchiveNow   func() time.Time
	SyntheticHandoff    bool
	BlobDirectory       string
	CandidateDailyLimit int
	Analyzer            SkillAnalyzer
	Drafter             SkillDrafter
	Router              SkillJudge
	// ModelPicker picks a model for 라우팅 (Clef-flash); nil sends every task to the fixed or first model.
	ModelPicker   ModelPicker
	searchCommits searchCommitGate
	analysisMu    sync.Mutex
	draftMu       sync.Mutex
	providerKeys  providerKeyHold
	// discordTokens holds typed Discord bot tokens until the host collects them, like providerKeys.
	discordTokens providerKeyHold
	Repo          Repository
	Auth          Authorizer
	Embedder      Embedder
	CostRates     CostRates
	// Wiki, when set, is the model the optional LLM-Wiki jobs call.
	Wiki WikiLLM

	// ReviewClassifier orders pending candidates by likely rejection; nil leaves the queue order alone.
	ReviewClassifier *ReviewClassifier
	reviewScores     reviewScoreCache
}
type CreateInput struct {
	capture         *captureWrite       // Server-owned; omitted from all JSON/BSON input.
	MemoryContext   *MemoryContext      `json:"memory_context,omitempty" bson:"memory_context,omitempty"`
	ArchiveMessages []ArchiveMessageRef `json:"archive_messages,omitempty" bson:"archive_messages,omitempty"`

	TrustLevel     string      `json:"trust_level,omitempty"`
	Confidence     *float64    `json:"confidence,omitempty"`
	Tags           []string    `json:"tags,omitempty"`
	Run            *RunDetails `json:"run,omitempty"`
	ToolCall       *ToolCall   `json:"tool_call,omitempty"`
	OriginProduct  string      `json:"origin_product,omitempty" bson:"origin_product,omitempty"`
	SourceRef      string      `json:"source_ref,omitempty" bson:"source_ref,omitempty"`
	SourceEventID  string      `json:"source_event_id,omitempty"`
	TraceID        string      `json:"trace_id,omitempty"`
	CollectedAt    *time.Time  `json:"collected_at,omitempty" bson:"collected_at,omitempty"`
	Generator      *Generator  `json:"generator,omitempty"`
	Generated      bool        `json:"generated,omitempty"`
	IdempotencyKey string      `json:"idempotency_key,omitempty"`
	OnSecret       string      `json:"on_secret,omitempty"`
	Sensitivity    string      `json:"sensitivity,omitempty"`
	Reflection     *Reflection `json:"reflection,omitempty"`
	// Classification is the extractor's risk call; kept on memory candidates only.
	Classification *CandidateClassification `json:"classification,omitempty"`
	Scope          Scope                    `json:"scope"`
	Kind           string                   `json:"kind"`
	Text           string                   `json:"text"`
	Summary        string                   `json:"summary"`
	Keywords       []string                 `json:"keywords"`
	Sources        []SourceRef              `json:"sources"`
	Relations      []Ref                    `json:"relations"`
	EffectiveAt    *time.Time               `json:"effective_at,omitempty"`
	ExpiresAt      *time.Time               `json:"expires_at"`
	SkillID        string                   `json:"skill_id"`
	BaseVersion    string                   `json:"base_version"`
	Target         *Ref                     `json:"target"`
	Evaluation     *Evaluation              `json:"evaluation,omitempty"`
	Passed         bool                     `json:"passed"`
}
type ChangeInput struct {
	ScopedTrialEvidence []ScopedTrialEvidence `json:"scoped_trial_evidence,omitempty"`
	TrialID             string                `json:"trial_id,omitempty"`
	TrialEvidence       []Ref                 `json:"trial_evidence,omitempty"`
	Scope               Scope                 `json:"scope"`
	ExpectedRevision    int64                 `json:"expected_revision"`
	Status              string                `json:"status"`
	ValidationID        string                `json:"validation_id"`
	VersionID           string                `json:"version_id"`
}

func ID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func ContentHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}
func validScope(s Scope) bool {
	return scopeName(s.WorkspaceID) && scopeName(s.ProjectID) && len(s.UserID) < 256 && len(s.AgentID) < 256
}

// Workspace and project names are plain labels, never path segments.
func scopeName(v string) bool {
	return v != "" && len(v) < 256 && v != "." && v != ".." && !strings.ContainsAny(v, "/\\") && strings.IndexFunc(v, unicode.IsControl) < 0
}
func knownCollection(c string) bool {
	// Session archive and work checkpoint collections are private: only their
	// dedicated endpoints touch them, generic CRUD never does.
	if c == colBlobs || c == colArtifacts || c == colSessionArchives || c == colSessionArchiveChunks ||
		c == colWorkCheckpoints || c == colWorkCheckpointReceipts {
		return false
	}
	for _, v := range Collections {
		if c == v {
			return true
		}
	}
	return false
}
func live(r Record) bool {
	return liveAt(r, time.Now())
}
func liveAt(r Record, now time.Time) bool {
	return !r.PrivacyQuarantined && r.Status == "active" && (r.EffectiveAt == nil || !r.EffectiveAt.After(now)) && (r.ExpiresAt == nil || r.ExpiresAt.After(now))
}
func (s *Service) authorize(ctx context.Context, token, action string, scope Scope, id, content string) (Decision, error) {
	destination := "internal"
	switch action {
	case "search:memories", "search:skill_versions", "search:reflections":
		destination = "external:embedding"
	case "route:skills", "route:models":
		destination = "external:jev"
	case "analyze:skills":
		destination = "external:skill-analysis"
	}
	return s.authorizeAt(ctx, token, action, scope, id, content, destination)
}

// An immutable, request-local constraint; each nested action still needs its
// own authorization. Only callers with an already validated decision bind it.
type requestActorKey struct{}

func bindRequestActor(ctx context.Context, actor Decision) context.Context {
	return context.WithValue(ctx, requestActorKey{}, actor)
}

func (s *Service) authorizeAt(ctx context.Context, token, action string, scope Scope, id, content, destination string) (Decision, error) {

	if s.Auth == nil || token == "" || !validScope(scope) || containsSecret(structuredText(scope)) || containsSecret(id) {
		if s.Auth != nil {
			s.auditDeniedInput(ctx, action, scope, id)
		}
		return Decision{}, ErrDenied
	}
	request := Authorization{Action: action, Scope: scope, ResourceID: id, Content: content, Context: NewContextScope(scope, action, destination)}
	if requestID, ok := ctx.Value(auditRequestKey{}).(string); ok {
		request.Context.RequestID = requestID
	}
	d, e := s.Auth.Authorize(ctx, token, request)
	if errors.Is(e, ErrDenied) {
		// An unknown or revoked token has no principal, so FORCE RLS may refuse
		// its audit row; the denial stays a denial and the write is best effort.
		s.auditDeniedInput(ctx, action, scope, id)
		return Decision{}, ErrDenied
	}
	if e != nil {
		// A provider failure is recorded without trusting any answer field.
		_ = s.auditDenial(ctx, auditClassPolicyError, request, boundAuditRequestID(ctx), "", "", "", "")
		return Decision{}, ErrUnavailable
	}
	if !d.Allowed || d.PrincipalID == "" || d.Scope != scope {
		if !d.Allowed && verifiedDenial(request, d) {
			if err := s.auditPolicyDenial(ctx, request, d); denialAuditFailed(err) {
				return Decision{}, ErrUnavailable
			}
		} else if !d.Allowed {
			// The provider answered deny but the answer failed verification
			// (mismatched context or malformed fields); keep the fact of the
			// denial while storing none of the unverified answer fields.
			if err := s.auditDenial(ctx, auditClassPolicyInvalid, request, boundAuditRequestID(ctx), "", d.PrincipalID, d.Role, ""); denialAuditFailed(err) {
				return Decision{}, ErrUnavailable
			}
		}
		return Decision{}, ErrDenied
	}
	if !sameRequestActor(ctx, d) {
		s.auditDeniedInput(ctx, action, scope, id)
		return Decision{}, ErrDenied
	}
	return d, nil
}

// Row-level security refuses a denial row in a scope the caller cannot write;
// that refusal confirms the denial, so only other storage failures become 503.
func denialAuditFailed(err error) bool {
	var refused rlsRefusal
	return err != nil && !errors.As(err, &refused)
}

func sameRequestActor(ctx context.Context, d Decision) bool {
	match := true
	if actor, ok := ctx.Value(requestActorKey{}).(Decision); ok {
		match = d.PrincipalID == actor.PrincipalID && d.Role == actor.Role && d.Scope == actor.Scope
	}
	if actor, ok := ctx.Value(httpActorKey{}).(*httpActor); ok {
		match = match && !actor.changed.Load() && d.PrincipalID == actor.principal && d.Role == actor.role
		if !match {
			actor.changed.Store(true)
		}
	}
	return match
}
func audit(ctx context.Context, tx Tx, scope Scope, actor, action, id string) error {
	if containsSecret(structuredText(scope)) || containsSecret(actor) || containsSecret(action) || containsSecret(id) {
		return ErrSecretDetected
	}
	now := time.Now().UTC()
	return tx.Insert(ctx, "audit_events", Record{Audit: &AuditMetadata{RequestID: auditRequestID(ctx), Decision: "allow"}, ID: ID(), Scope: scope, Kind: action, Status: "active", Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: actor, Target: &Ref{ID: id}})
}

// Match the Create source-reference limit; bound even legacy/direct DB inputs.
func sourceMetadata(ctx context.Context, tx Tx, scope Scope, refs []SourceRef) ([]Record, error) {
	if len(refs) == 0 || len(refs) > 64 {
		return nil, ErrInvalid
	}
	return sourceMetadataBatch(ctx, tx, scope, refs)
}

// sourceMetadataBatch is sourceMetadata for the refs of many records, read 64 sources at a time.
func sourceMetadataBatch(ctx context.Context, tx Tx, scope Scope, refs []SourceRef) ([]Record, error) {
	ids := []string{}
	wanted := map[string]bool{}
	spans := map[string][]SourceSpan{}
	for _, ref := range refs {
		if ref.SourceID == "" || ref.Locator == "" {
			return nil, ErrInvalid
		}
		if ref.Span != nil {
			if ref.Span.Start < 0 || ref.Span.End <= ref.Span.Start || ref.Span.End > 64000 || !sourceHashPattern.MatchString(ref.Span.ContentHash) {
				return nil, ErrInvalid
			}
			spans[ref.SourceID] = append(spans[ref.SourceID], *ref.Span)
		}
		if !wanted[ref.SourceID] {
			wanted[ref.SourceID] = true
			ids = append(ids, ref.SourceID)
		}
	}
	rows, e := readSourceGroups(ctx, tx, scope, ids, spans)
	if e != nil {
		return nil, e
	}
	if len(rows) != len(ids) {
		return nil, ErrDenied
	}
	for i, r := range rows {
		if !wanted[r.ID] || r.Scope != scope || !live(r) || r.ContentErased {
			return nil, ErrDenied
		}
		if len(spans[r.ID]) > 0 {
			hash, length := ContentHash(r.Text), utf8.RuneCountInString(r.Text)
			for _, span := range spans[r.ID] {
				if err := validateSourceSpan(&span, hash, length); err != nil {
					return nil, err
				}
			}
		}
		rows[i] = Record{TrustLevel: r.TrustLevel, ID: r.ID, Scope: r.Scope, Status: r.Status, ExpiresAt: r.ExpiresAt, Sensitivity: r.Sensitivity}
		delete(wanted, r.ID)
	}
	return rows, nil
}

// readSourceGroups reads text only for sources with spans to check, 64 sources per read.
func readSourceGroups(ctx context.Context, tx Tx, scope Scope, ids []string, spans map[string][]SourceSpan) ([]Record, error) {
	var rows []Record
	for _, includeText := range []bool{false, true} {
		var group []string
		for _, id := range ids {
			if (len(spans[id]) > 0) == includeText {
				group = append(group, id)
			}
		}
		for start := 0; start < len(group); start += 64 {
			batch, e := tx.SourceMetadata(ctx, scope, group[start:min(start+64, len(group))], includeText)
			if e != nil {
				return nil, e
			}
			rows = append(rows, batch...)
		}
	}
	return rows, nil
}
func checkSources(ctx context.Context, tx Tx, r Record) error {
	_, err := sourceMetadata(ctx, tx, r.Scope, r.Sources)
	return err
}

type canonicalEvidenceStore interface {
	CanonicalEvidence(context.Context, Scope, string, string) error
}

func memoryProvenance(ctx context.Context, tx Tx, r Record) error {
	sourceEventID := effectiveSourceEventID(r)
	if sourceEventID == "" && r.TraceID == "" {
		return ErrInvalid
	}
	if sourceEventID != "" {
		found := false
		for _, source := range r.Sources {
			found = found || source.SourceID == sourceEventID
		}
		if !found {
			return ErrDenied
		}
	}
	if fenced, ok := tx.(searchReadTx); ok {
		tx = fenced.Tx
	}
	if r.TraceID == "" {
		if evidence, ok := tx.(canonicalEvidenceStore); ok {
			return evidence.CanonicalEvidence(ctx, r.Scope, sourceEventID, "")
		}
		return nil
	}
	linked := false
	for _, relation := range r.Relations {
		linked = linked || relation.ID == r.TraceID && (relation.Collection == "execution_traces" || relation.Collection == "agent_runs")
	}
	if !linked {
		return ErrDenied
	}
	if evidence, ok := tx.(canonicalEvidenceStore); ok {
		return evidence.CanonicalEvidence(ctx, r.Scope, sourceEventID, r.TraceID)
	}
	trace, err := tx.Get(ctx, "agent_runs", r.Scope, r.TraceID)
	if err != nil {
		return err
	}
	if !live(trace) || trace.Scope != r.Scope {
		return ErrDenied
	}
	sharedSource := false
	for _, candidateSource := range r.Sources {
		for _, traceSource := range trace.Sources {
			sharedSource = sharedSource || candidateSource.SourceID == traceSource.SourceID
		}
	}
	if !sharedSource {
		return ErrDenied
	}
	return nil
}

func effectiveSourceEventID(r Record) string {
	if r.SourceEventID != "" {
		return r.SourceEventID
	}
	if len(r.Sources) > 0 {
		return r.Sources[0].SourceID
	}
	return ""
}

func normalizeMemoryProvenance(r *Record) {
	if r.SourceEventID == "" {
		r.SourceEventID = effectiveSourceEventID(*r)
	}
	if r.VerificationStatus == "" {
		if r.ApprovedBy != "" {
			r.VerificationStatus = VerificationVerified
		} else {
			r.VerificationStatus = VerificationCandidate
		}
	}
}
func checkRelations(ctx context.Context, tx Tx, r Record) error {
	if err := checkSkillBasis(ctx, tx, r); err != nil {
		return err
	}
	return checkDirectRelations(ctx, tx, r)
}

func checkDirectRelations(ctx context.Context, tx Tx, r Record) error {
	if r.Kind == proposedSkillKind {
		if r.ComparisonPlan == nil || !validComparisonPlan(r.ComparisonPlan) || !validGenerator(r.Generator) {
			return ErrInvalid
		}
		evidence, e := skillLessons(ctx, tx, r.Scope, proposedLessons(r))
		if e != nil {
			return e
		}
		for _, ref := range evidence.Relations {
			if !slices.Contains(r.Relations, ref) {
				return ErrConflict
			}
		}
	}
	for _, ref := range r.Relations {
		if r.Kind == proposedSkillKind && ref.Collection == "memory_candidates" {
			continue
		} // Checked above, including candidate state and exact revision.
		if !knownCollection(ref.Collection) || ref.Collection == "jobs" || ref.Collection == "audit_events" || ref.Revision < 1 {
			return ErrInvalid
		}
		v, e := tx.Get(ctx, ref.Collection, r.Scope, ref.ID)
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e // keep store outages (and transient retries) distinct from a stale relation
		}
		if e != nil || v.Revision != ref.Revision || !live(v) {
			return ErrConflict
		}
		if ref.Collection != "sources" {
			if e = checkSources(ctx, tx, v); e != nil {
				return e
			}
		}
	}
	return nil
}
func validTags(tags []string) bool {
	if len(tags) > 32 {
		return false
	}
	for i, tag := range tags {
		if !utf8.ValidString(tag) || !validRunLabel(tag, 128) || slices.Contains(tags[:i], tag) {
			return false
		}
	}
	return true
}

func validCreateKind(collection, kind string) bool {
	switch collection {
	case "sources":
		return slices.Contains([]string{"conversation", "message", "note", "document", "url", "image", "tool_result", "design_page"}, kind)
	case "memory_candidates":
		return slices.Contains([]string{"raw", "semantic", "episodic", "procedural", "user", "decision", "lesson"}, kind)
	default:
		return true
	}
}
func (s *Service) Create(ctx context.Context, token, col string, in CreateInput) (Record, error) {
	return s.create(ctx, token, col, in, nil)
}

func (s *Service) create(ctx context.Context, token, col string, in CreateInput, file *textArtifact) (Record, error) {
	if len(in.ArchiveMessages) > 0 && col != "sources" {
		return Record{}, ErrInvalid
	}
	if in.TrustLevel != "" && (col != "sources" || in.TrustLevel != "external_untrusted" && in.TrustLevel != "user_authored") {
		return Record{}, ErrInvalid
	}
	if in.Confidence != nil {
		confidence := *in.Confidence
		if col != "memory_candidates" || math.IsNaN(confidence) || math.IsInf(confidence, 0) || confidence < 0 || confidence > 1 {
			return Record{}, ErrInvalid
		}
		in.Confidence = &confidence
	}
	if col == "sources" && !validScope(in.Scope) {
		return Record{}, ErrInvalid
	}
	if in.IdempotencyKey != "" && (col != "sources" || !idempotencyPattern.MatchString(in.IdempotencyKey)) {
		return Record{}, ErrInvalid
	}
	if !validMemoryContext(in.MemoryContext, time.Now().UTC()) || in.MemoryContext != nil && col != "memory_candidates" {
		return Record{}, ErrInvalid
	}
	allowed := map[string]bool{"sources": true, "memory_candidates": true, "agent_runs": true, "tool_calls": true, "evaluations": true, "reflections": true, "memory_relations": true}
	if !validTags(in.Tags) || len(in.Tags) > 0 && col != "memory_candidates" || !allowed[col] || (file != nil && col != "sources") || (in.Evaluation != nil && col != "evaluations") || (in.Run != nil && col != "agent_runs") || (in.ToolCall != nil && col != "tool_calls") || (in.Reflection != nil && col != colReflections) || (in.SourceEventID != "" || in.TraceID != "") && col != "memory_candidates" && col != colReflections || in.SourceEventID != "" && !validRunLabel(in.SourceEventID, 256) || in.TraceID != "" && !validRunLabel(in.TraceID, 256) || (file == nil && strings.TrimSpace(in.Text) == "") || len(in.Text) > 64000 || len(in.Summary) > 8000 || len(in.Sources) > 64 || len(in.Relations) > 64 || len(in.Keywords) > 128 || len(in.SkillID) > 256 || len(in.BaseVersion) > 256 {
		return Record{}, ErrInvalid
	}
	for _, k := range in.Keywords {
		if len(k) > 256 {
			return Record{}, ErrInvalid
		}
	}
	if containsSecret(structuredText(in.Scope)) {
		return Record{}, ErrSecretDetected
	}
	clean, inspection, inspectionErr := inspectCreateInput(in, file != nil)
	if file != nil {
		fileInspection, err := inspectArtifactText(file.content, file.metadata.Data.MIME)
		if err != nil && (inspectionErr == nil || err == ErrSecretDetected) {
			inspectionErr = err
		}
		inspection.PersonalData = inspection.PersonalData || fileInspection.PersonalData
		if inspection.PersonalData && clean.Sensitivity != "restricted" {
			clean.Sensitivity = "private"
		}
	}
	if inspectionErr != nil {
		if inspectionErr != ErrSecretDetected {
			return Record{}, inspectionErr
		}
		// Never send rejected content to policy or record its value/hash in audit.
		d, err := s.authorize(ctx, token, "write:"+col, in.Scope, "", "")
		if err != nil {
			return Record{}, err
		}
		err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			return audit(ctx, tx, in.Scope, d.PrincipalID, "inspection:blocked:"+inspectionVersion, "")
		})
		if err != nil {
			return Record{}, err
		}
		return Record{}, ErrSecretDetected
	}
	clean.capture = in.capture
	in = clean
	// Policy inspects the sanitized complete payload, including secondary fields.
	raw, _ := json.Marshal(in)
	if file != nil {
		raw, _ = json.Marshal(struct {
			Source   CreateInput        `json:"source"`
			Artifact FileArtifactResult `json:"artifact"`
			Content  string             `json:"file_content"`
		}{in, file.descriptor(), string(file.content)})
	}
	d, e := s.authorize(ctx, token, "write:"+col, in.Scope, "", string(raw))
	if e != nil {
		return Record{}, e
	}
	if file != nil {
		if !d.ContentSafe || !validArtifactActor(d, in.Scope) {
			return Record{}, ErrDenied
		}
		artifactDecision, err := s.authorize(ctx, token, "write:artifacts", in.Scope, "", string(raw))
		if err != nil {
			return Record{}, err
		}
		if !artifactDecision.ContentSafe || artifactDecision.PrincipalID != d.PrincipalID || artifactDecision.Role != d.Role {
			return Record{}, ErrDenied
		}
	}
	if (in.Generated || in.Generator != nil || requiresGenerator(d.Role, col)) && !validGenerator(in.Generator) {
		return Record{}, ErrInvalid
	}
	if d.Role != "human" && d.Role != "agent" && d.Role != "service" && d.Role != "pipeline" {
		return Record{}, ErrDenied
	}
	if in.TrustLevel == "user_authored" && (d.Role != "human" || in.Generated || in.Generator != nil) {
		return Record{}, ErrDenied
	}
	if !validCreateKind(col, in.Kind) {
		return Record{}, ErrInvalid
	}
	now := time.Now().UTC()
	if col == "memory_candidates" {
		effective := now.Truncate(time.Millisecond)
		if in.EffectiveAt != nil {
			effective = in.EffectiveAt.UTC().Truncate(time.Millisecond)
			if !storableTime(effective) {
				return Record{}, ErrInvalid
			}
		}
		in.EffectiveAt = &effective
		if in.ExpiresAt != nil {
			expires := in.ExpiresAt.UTC().Truncate(time.Millisecond)
			if !expires.After(effective) || !storableTime(expires) {
				return Record{}, ErrInvalid
			}
			in.ExpiresAt = &expires
		}
	} else if in.EffectiveAt != nil {
		return Record{}, ErrInvalid
	}
	if col == "sources" {
		if !validSourceOrigin(in.OriginProduct, in.SourceRef, in.CollectedAt, now) {
			return Record{}, ErrInvalid
		}
		normalized := in.CollectedAt.UTC().Truncate(time.Millisecond)
		in.CollectedAt = &normalized
	} else if in.OriginProduct != "" || in.SourceRef != "" || in.CollectedAt != nil {
		return Record{}, ErrInvalid
	}
	r := Record{MemoryContext: copyMemoryContext(in.MemoryContext), ArchiveMessages: append([]ArchiveMessageRef(nil), in.ArchiveMessages...), Confidence: in.Confidence, Evaluation: in.Evaluation, Run: in.Run, ToolCall: in.ToolCall, OriginProduct: in.OriginProduct, SourceRef: in.SourceRef, SourceEventID: in.SourceEventID, TraceID: in.TraceID, CollectedAt: in.CollectedAt, Generator: in.Generator, CreatedByType: d.Role, Sensitivity: in.Sensitivity, Inspection: &inspection, ID: ID(), Scope: in.Scope, Kind: in.Kind, Status: "active", Revision: 1, Text: in.Text, Summary: in.Summary, Keywords: in.Keywords, Tags: in.Tags, Sources: in.Sources, Relations: in.Relations, CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, Trust: 0.5, EffectiveAt: in.EffectiveAt, ExpiresAt: in.ExpiresAt, SkillID: in.SkillID, BaseVersion: in.BaseVersion, Target: in.Target, Passed: in.Passed, Reflection: in.Reflection}
	if r.ExpiresAt != nil && !r.ExpiresAt.After(now) {
		return Record{}, ErrInvalid
	}
	if col == "memory_candidates" || col == colMemories {
		sg := suggestReach(r)
		r.ReachSuggestion = &sg
	}
	if col == "memory_candidates" {
		r.Status = "candidate"
		if in.Classification != nil {
			c := *in.Classification
			if !validClassification(c) {
				return Record{}, ErrInvalid
			}
			c.By, c.At = d.PrincipalID, &now
			r.Classification = &c
		}
	} else if in.Classification != nil {
		return Record{}, ErrInvalid
	}
	if col == "sources" {
		r.TrustLevel = sourceTrustLevel(in.TrustLevel)
		r.Sources = []SourceRef{sourceReference(r, "original")}
		if file != nil {
			return s.persistTextArtifact(ctx, token, in, r, d, *file)
		}
		return s.createSource(ctx, token, in, r, d, nil)
	}
	if col == "memory_candidates" {
		if r.SourceEventID == "" && len(r.Sources) > 0 {
			r.SourceEventID = r.Sources[0].SourceID
		}
		r.VerificationStatus = VerificationCandidate
		r.NormalizedKey = candidateNormalizedKey(r)
		for _, ref := range r.Sources {
			if ref.Span == nil {
				return Record{}, ErrInvalid
			}
		}
	}
	if col == "reflections" && r.Reflection == nil {
		return Record{}, ErrInvalid
	}
	if col == "reflections" && r.Reflection != nil {
		x := *r.Reflection
		x.SecretChecked = false
		if x.Outcome != "success" && x.Outcome != "partial" && x.Outcome != "failed" {
			return Record{}, ErrInvalid
		}
		if x.TaskType == "" || x.Lesson == "" || x.VerificationMethod == "" || !safeTransfer(structuredText(x)) {
			return Record{}, ErrInvalid
		}
		if x.Verified && d.Role != "human" && d.Role != "service" {
			return Record{}, ErrDenied
		}
		x.SecretChecked = d.ContentSafe && d.ExternalTransfer
		r.Reflection = &x
		// Ignore raw conversation/tool copies: only structured summary is stored.
		r.Text = structuredText(x)
		r.Summary = x.Lesson
		r.Keywords = []string{x.TaskType, x.Outcome, x.SkillID}
	}
	if col == "reflections" && len(r.Relations) == 0 {
		return Record{}, ErrInvalid
	}
	if col == "agent_runs" {
		if !validRun(r.Run, r.Scope, now) {
			return Record{}, ErrInvalid
		}
		details := *r.Run
		details.StartedAt = details.StartedAt.UTC().Truncate(time.Millisecond)
		if details.FinishedAt != nil {
			finished := details.FinishedAt.UTC().Truncate(time.Millisecond)
			details.FinishedAt = &finished
		}
		r.Run = &details
	}
	if col == "tool_calls" {
		if !validToolCall(r.ToolCall, r.Target, now) {
			return Record{}, ErrInvalid
		}
		details := *r.ToolCall
		details.StartedAt = details.StartedAt.UTC().Truncate(time.Millisecond)
		details.FinishedAt = details.FinishedAt.UTC().Truncate(time.Millisecond)
		r.ToolCall = &details
		parent, err := s.authorize(ctx, token, "read:agent_runs", r.Scope, r.Target.ID, "")
		if err != nil {
			return Record{}, err
		}
		if parent.PrincipalID != d.PrincipalID || parent.Role != d.Role {
			return Record{}, ErrDenied
		}
	}
	if col == "evaluations" {
		if r.Target == nil || !knownCollection(r.Target.Collection) || r.Target.Collection == "jobs" || r.Target.Collection == "audit_events" || !validRunLabel(r.Target.ID, 256) || r.Target.Revision < 1 || !validEvaluation(r.Evaluation) {
			return Record{}, ErrInvalid
		}
		if !evaluationRole(r.Evaluation.EvalType, d.Role) {
			return Record{}, ErrDenied
		}
		passed := r.Evaluation.Passed != nil && *r.Evaluation.Passed
		if in.Passed && !passed {
			return Record{}, ErrInvalid
		}
		r.Passed = passed
		parent, err := s.authorize(ctx, token, "read:"+r.Target.Collection, r.Scope, r.Target.ID, "")
		if err != nil {
			return Record{}, err
		}
		if parent.PrincipalID != d.PrincipalID || parent.Role != d.Role {
			return Record{}, ErrDenied
		}
	}
	contextHeaders, contextErr := s.validateMemoryContextEvidence(ctx, token, r.Scope, r.Sources, r.MemoryContext)
	if contextErr != nil {
		return Record{}, contextErr
	}
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if err := s.fenceSourceArchives(ctx, tx, contextHeaders); err != nil {
			return err
		}
		if col != "sources" {
			sources, err := sourceMetadata(ctx, tx, r.Scope, r.Sources)
			if err != nil {
				return err
			}
			if col == "memory_candidates" {
				r.TrustLevel = candidateTrustLevel(r, sources)
			}
		}
		if e := checkRelations(ctx, tx, r); e != nil {
			return e
		}
		if col == "memory_candidates" || col == colReflections && (r.SourceEventID != "" || r.TraceID != "") {
			if e := memoryProvenance(ctx, tx, r); e != nil {
				return e
			}
		}
		if col == "tool_calls" {
			run, err := tx.Get(ctx, "agent_runs", r.Scope, r.Target.ID)
			if err != nil {
				return err
			}
			if run.Revision != r.Target.Revision || !live(run) {
				return ErrConflict
			}
			if run.Run != nil && (!validRun(run.Run, run.Scope, now) || r.ToolCall.StartedAt.Before(run.Run.StartedAt) || run.Run.FinishedAt != nil && r.ToolCall.FinishedAt.After(*run.Run.FinishedAt)) {
				return ErrInvalid
			}
			if err = checkSources(ctx, tx, run); err != nil {
				return err
			}
			if err = tx.FenceRunCall(ctx, r.Scope, run.ID, run.Revision); err != nil {
				return err
			}
		}
		if col == "evaluations" {
			if !knownCollection(r.Target.Collection) || r.Target.Collection == "jobs" || r.Target.Collection == "audit_events" {
				return ErrInvalid
			}
			v, e := tx.Get(ctx, r.Target.Collection, r.Scope, r.Target.ID)
			if e != nil {
				return e
			}
			if v.Revision != r.Target.Revision {
				return ErrConflict
			}
			if (r.Evaluation.EvalType == "model_self") != (v.CreatedBy == d.PrincipalID) {
				return ErrDenied
			}
			if r.Target.Collection == colMemories && r.Evaluation.EvalType == "automated_check" && v.ApprovedBy == d.PrincipalID {
				return ErrDenied
			}
			if e = checkSources(ctx, tx, v); e != nil {
				return e
			}
			if r.Target.Collection == "agent_runs" && v.Run != nil && (!validRun(v.Run, v.Scope, now) || !terminalOutcome(v.Run.Outcome)) {
				return ErrConflict
			}
		}
		if col == "reflections" && r.Reflection != nil && r.Reflection.Verified {
			if e := checkReflection(ctx, tx, r); e != nil {
				return e
			}
			if e := enqueue(ctx, tx, col, r); e != nil {
				return e
			}
		}
		if col == "memory_candidates" && (requiresGenerator(d.Role, col) || in.Generated || in.Generator != nil) {
			if e := reserveCandidate(ctx, tx, r.Scope, now, s.CandidateDailyLimit); e != nil {
				return e
			}
		}
		var insertErr error
		if col != "sources" {
			insertErr = insertSourceRecord(ctx, tx, col, r)
		} else {
			insertErr = tx.Insert(ctx, col, r)
		}
		if e := insertErr; e != nil {
			return e
		}
		if in.capture != nil {
			if col != "memory_candidates" {
				return ErrInvalid
			}
			if err := in.capture.complete(ctx, tx, r); err != nil {
				return err
			}
		}
		if col == "evaluations" {
			if e := recordMemoryEvaluation(ctx, tx, r); e != nil {
				return e
			}
		}
		if inspection.Redacted || inspection.PersonalData {
			if err := audit(ctx, tx, r.Scope, d.PrincipalID, "inspection:processed:"+inspectionVersion, r.ID); err != nil {
				return err
			}
		}
		return audit(ctx, tx, r.Scope, d.PrincipalID, "create:"+col, r.ID)
	})
	if e != nil {
		return Record{}, e
	}
	return r, nil
}
func (s *Service) Get(ctx context.Context, token, col string, scope Scope, id string) (Record, error) {
	if col == colProviderUsage {
		return Record{}, ErrInvalid
	}
	// Digests are served only by /v1/digests, which withholds a body whose basis is gone.
	if !knownCollection(col) || col == "jobs" || col == colConsultations || col == colHandoffs || isKnowledgeCollection(col) || col == colTopicDigests || col == colUserDigests || isWikiCollection(col) {
		return Record{}, ErrInvalid
	}
	d, e := s.authorize(ctx, token, "read:"+col, scope, id, "")
	if e != nil {
		return Record{}, e
	}
	r, e := s.Repo.Get(ctx, col, scope, id)
	if e != nil {
		return Record{}, e
	}
	if r.PrivacyQuarantined {
		return Record{}, ErrNotFound
	}
	if col == "memory_relations" && r.ContentErased {
		r, e = conflictControlRecord(r)
		if e != nil {
			return Record{}, e
		}
	} else if col != "sources" && col != "audit_events" {
		if e = checkSources(ctx, s.Repo, r); e != nil {
			return Record{}, e
		}
	}
	if col == colSkillVersions && r.Kind == proposedSkillKind {
		if e = checkRelations(ctx, s.Repo, r); e != nil {
			return Record{}, e
		}
	}
	if col == "sources" && r.Kind == "skill_draft_prepared" {
		if e = checkPreparedDraftEvidence(ctx, s.Repo, r); e != nil {
			return Record{}, e
		}
	}
	if col == "sources" && r.Kind == proposedSkillSourceKind {
		if e = checkSkillBasis(ctx, s.Repo, r); e != nil {
			return Record{}, e
		}
		if _, e = skillLessons(ctx, s.Repo, scope, proposedLessons(r)); e != nil {
			return Record{}, e
		}
	}
	if col == colMemories || col == colSkillVersions {
		r.ContentHash = ContentHash(recordContent(r))
	}
	if col == colMemories || col == "memory_candidates" {
		normalizeMemoryProvenance(&r)
	}
	// Inactive records remain explicitly retrievable for authorized review/history.
	if e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		// A privacy deletion that committed after the first read must win over the response.
		cur, e := tx.Get(ctx, col, scope, id)
		if e != nil {
			return e
		}
		if cur.PrivacyQuarantined {
			return ErrNotFound
		}
		if cur.Revision != r.Revision {
			return ErrConflict
		}
		if col != "sources" && col != "audit_events" && !(col == "memory_relations" && cur.ContentErased) {
			if e = checkSources(ctx, tx, cur); e != nil {
				return e
			}
		}
		return audit(ctx, tx, scope, d.PrincipalID, "read:"+col, id)
	}); e != nil {
		return Record{}, e
	}
	return r, nil
}
func validTransition(from, to string) bool {
	switch from {
	case "candidate":
		return to == "approved" || to == "rejected" || to == "expired"
	case "approved":
		return to == "active" || to == "rejected" || to == "expired"
	case "active":
		return to == "superseded" || to == "rejected" || to == "expired"
	}
	return false
}
func enqueue(ctx context.Context, tx Tx, col string, r Record) error {
	now := time.Now().UTC()
	return tx.Insert(ctx, "jobs", Record{ID: ID(), Scope: r.Scope, Kind: "embedding", Status: "pending", Revision: 1, CreatedAt: now, UpdatedAt: now, Target: &Ref{Collection: col, ID: r.ID, Revision: r.Revision}})
}
func validation(ctx context.Context, tx Tx, r Record, id string, checkRevision bool) error {
	v, e := tx.Get(ctx, "evaluations", r.Scope, id)
	if e != nil {
		return e
	}
	if !live(v) || !independentEvaluation(v) || v.Target == nil || v.Target.Collection != "skill_versions" || v.Target.ID != r.ID || v.CreatedBy == r.CreatedBy || !comparisonPasses(r, v) {
		return ErrDenied
	}
	if checkRevision && v.Target.Revision != r.Revision {
		return ErrConflict
	}
	return checkEvaluationEvidence(ctx, tx, v)
}
func (s *Service) Transition(ctx context.Context, token, col, id string, in ChangeInput) (Record, error) {
	if col == colProviderUsage {
		return Record{}, ErrInvalid
	}
	if col == "memories" && in.Status == "superseded" {
		return Record{}, ErrInvalid
	}
	return s.transition(ctx, token, col, id, in, nil)
}
func (s *Service) transition(ctx context.Context, token, col, id string, in ChangeInput, review *CandidateReviewInput) (Record, error) {
	if col != "memory_candidates" && col != "memories" && col != "skill_versions" {
		return Record{}, ErrInvalid
	}
	content := ""
	if review != nil {
		content = structuredText(review)
	}
	d, e := s.authorize(ctx, token, "transition:"+col, in.Scope, id, content)
	if e != nil {
		return Record{}, e
	}
	if review != nil {
		// Review is an explicit human decision; legacy service policies cannot approve or reject.
		if review.Automated || d.Role != roleHuman || !d.ContentSafe {
			return Record{}, ErrDenied
		}
	} else if d.Role != roleHuman {
		if col != colSkillVersions || (in.Status != "approved" && in.Status != "rejected") {
			return Record{}, ErrDenied
		}
		candidate, err := s.Repo.Get(ctx, colSkillVersions, in.Scope, id)
		if err != nil {
			return Record{}, err
		}
		if _, err = s.authorizeSkillAutomation(ctx, token, d, in.Scope, candidate.SkillID, ""); err != nil {
			return Record{}, err
		}
		ctx = bindRequestActor(ctx, d)
	}
	if review != nil && review.Conflict != nil {
		other, err := s.authorize(ctx, token, "transition:memories", in.Scope, review.Conflict.Memory.ID, content)
		if err != nil {
			return Record{}, err
		}
		if other.PrincipalID != d.PrincipalID || other.Role != d.Role || !other.ContentSafe {
			return Record{}, ErrDenied
		}
	}
	if col == "skill_versions" && in.Status == "active" {
		return Record{}, ErrInvalid
	}
	var result Record
	if col == "skill_versions" && in.Status == "approved" {
		r, e := s.Repo.Get(ctx, col, in.Scope, id)
		if e != nil {
			return r, e
		}
		if r.Analysis == nil {
			return r, ErrUnavailable
		}
	}
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		result = Record{}
		r, e := tx.Get(ctx, col, in.Scope, id)
		if e != nil {
			return e
		}
		if r.ContentErased || r.PrivacyQuarantined {
			return ErrNotFound
		}

		if review != nil {
			if !s.memoryReviewerAllowed(d, in.Scope, r.Kind) {
				return ErrDenied
			}
			var replay bool
			result, replay, e = replayMemoryReview(ctx, tx, id, d.PrincipalID, *review)
			if e != nil {
				return e
			}
			if replay {
				return audit(ctx, tx, in.Scope, d.PrincipalID, "review:memory_candidates:replay", id)
			}
		}
		var replaced *Record
		expectedRevision := in.ExpectedRevision
		resolvedRejection := false
		if review != nil && review.Conflict != nil {
			if r.Revision != expectedRevision {
				return ErrConflict
			}
			r, replaced, e = s.resolveReviewConflict(ctx, tx, r, *review, d)
			if e != nil {
				return e
			}
			expectedRevision = r.Revision
			resolvedRejection = review.Action == "reject"
		}
		if col == colMemories || col == "memory_candidates" {
			normalizeMemoryProvenance(&r)
		}
		if r.ConflictID != "" || r.Revision != expectedRevision {
			return ErrConflict
		}
		if review != nil && !resolvedRejection && r.Status != "candidate" && r.Status != "conflicted" {
			return ErrConflict
		}
		if review != nil && in.Status == "approved" && r.ExpiresAt != nil && !r.ExpiresAt.After(time.Now()) {
			return ErrConflict
		}
		if (col == "memory_candidates" && !resolvedRejection && r.Status != "candidate" && !(review != nil && r.Status == "conflicted")) || (col == "skill_versions" && r.Status != "candidate" && r.Status != "approved") {
			return ErrInvalid
		}
		if !resolvedRejection && !validTransition(r.Status, in.Status) && !(review != nil && r.Status == "conflicted") {
			return ErrInvalid
		}
		if review == nil || in.Status != "rejected" {
			if e = checkSources(ctx, tx, r); e != nil {
				return e
			}
			if e = checkRelations(ctx, tx, r); e != nil {
				return e
			}
		}
		if col == "memory_candidates" && in.Status == "active" {
			return ErrInvalid
		}
		if col == "skill_versions" && in.Status == "approved" {
			if e = validation(ctx, tx, r, in.ValidationID, true); e != nil {
				return e
			}
			r.ValidationID = in.ValidationID
		}
		r.Status = in.Status
		r.Revision++
		r.UpdatedAt = time.Now().UTC()
		if review != nil {
			r.Review = &ReviewDecision{ReceiptID: ID(), ActorRole: d.Role, Channel: "explicit_review_api", Action: review.Action, By: d.PrincipalID, At: r.UpdatedAt, Reason: review.Reason}
			if d.Role == roleHuman {
				r.Review.Person, r.Review.Overturned = true, overturned(r.Classification, review.Action)
				if r.Classification != nil {
					r.Review.AIRisk = r.Classification.Risk
				}
			}
			if personalPattern.MatchString(review.Content + " " + review.Reason) {
				if r.Sensitivity != "restricted" {
					r.Sensitivity = "private"
				}
				report := Inspection{RulesVersion: inspectionVersion}
				if r.Inspection != nil {
					report = *r.Inspection
				}
				report.PersonalData = true
				r.Inspection = &report
			}
		}
		if in.Status == "approved" {
			r.ApprovedBy = d.PrincipalID
			if col == "memory_candidates" {
				r.VerificationStatus = VerificationVerified
			}
		}
		if col == "memory_candidates" && in.Status == "approved" {
			if e = memoryProvenance(ctx, tx, r); e != nil {
				return e
			}
		}
		if e = tx.Replace(ctx, col, r, expectedRevision); e != nil {
			return e
		}
		if col == "memory_candidates" && r.Status == "approved" {
			memory := r
			if d.Role == roleHuman {
				memory.TrustLevel = "user_verified"
			}
			memory.Proposal = &Ref{Collection: "memory_candidates", ID: r.ID, Revision: r.Revision}
			if review != nil {
				memory.Status = "active"
				if d.Role == roleHuman {
					memory.Trust = 1
				}
				memory.Embedding = nil
				if review.Action == "edit_approve" {
					memory.Text = review.Content
					memory.Summary = ""
					memory.Keywords = nil
					memory.KeywordsLower = nil
					sg := suggestReach(memory) // The suggestion follows the edited text.
					memory.ReachSuggestion = &sg
				}
				memory.ContentHash = ContentHash(memory.Text)
			}
			var replacementRelation Record
			if replaced != nil {
				replacementRelation, e = finishReviewReplacement(ctx, tx, replaced, &memory, *review, d)
				if e != nil {
					return e
				}
			}
			if e = insertSourceRecord(ctx, tx, "memories", memory); e != nil {
				return e
			}
			if replacementRelation.ID != "" {
				if e = insertSourceRecord(ctx, tx, "memory_relations", replacementRelation); e != nil {
					return e
				}
			}
			if review != nil {
				if e = enqueue(ctx, tx, "memories", memory); e != nil {
					return e
				}
				if e = enqueueMemoryEvaluation(ctx, tx, memory); e != nil {
					return e
				}
				result = memory
			}
		}
		if col == "memories" && r.Status == "active" {
			if e = enqueue(ctx, tx, col, r); e != nil {
				return e
			}
			if e = enqueueMemoryEvaluation(ctx, tx, r); e != nil {
				return e
			}
		}
		if result.ID == "" {
			result = r
		}
		action := "transition:" + col + ":" + in.Status
		if review != nil {
			if e = recordMemoryReview(ctx, tx, id, d.PrincipalID, *review, result); e != nil {
				return e
			}
			action = "review:memory_candidates:" + review.Action
		}
		return audit(ctx, tx, r.Scope, d.PrincipalID, action, id)
	})
	if e != nil {
		return Record{}, e
	}
	return result, nil
}
func (s *Service) ApplySkill(ctx context.Context, token, id string, in ChangeInput, rollback bool) (Record, error) {
	action := "apply"
	if rollback {
		action = "rollback"
	}
	d, e := s.authorize(ctx, token, action+":skills", in.Scope, id, "")
	if e != nil {
		return Record{}, e
	}
	var automation SkillAutomationPolicy
	if d.Role != roleHuman {
		automation, e = s.authorizeSkillAutomation(ctx, token, d, in.Scope, id, "")
		if e != nil {
			return Record{}, e
		}
		// Automatic rollback needs an explicit validated target; never clear all skills.
		if in.VersionID == "" {
			return Record{}, ErrDenied
		}
		ctx = bindRequestActor(ctx, d)
		if !rollback {
			if e = s.authorizeTrialEvidence(ctx, token, d, in, automation); e != nil {
				return Record{}, e
			}
		}
	}
	var target Record
	if in.VersionID != "" {
		target, e = s.Repo.Get(ctx, "skill_versions", in.Scope, in.VersionID)
		if e != nil {
			return Record{}, e
		}
		if target.SkillID != id || target.Analysis == nil {
			return Record{}, ErrInvalid
		}
	} else if !rollback {
		return Record{}, ErrInvalid
	}
	// An imported baseline never went through transition, so it has no
	// validation; rolling back to it needs a fresh independent evaluation.
	vid := target.ValidationID
	if d.Role == roleService && !rollback {
		vid = in.ValidationID
	}
	if rollback && vid == "" {
		vid = in.ValidationID
	}
	var result Record
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		skill, e := tx.Get(ctx, "skills", in.Scope, id)
		if e != nil {
			return e
		}
		if skill.Revision != in.ExpectedRevision {
			return ErrConflict
		}
		if skill.ActiveVersion == in.VersionID {
			return ErrConflict
		}
		if in.VersionID != "" {
			if !live(skill) {
				return ErrConflict
			}
			if e = checkSources(ctx, tx, skill); e != nil {
				return e
			}
			r, e := tx.Get(ctx, "skill_versions", in.Scope, in.VersionID)
			if e != nil {
				return e
			}
			if r.Revision != target.Revision || r.SkillID != id || r.ApprovedBy == "" || (r.ExpiresAt != nil && !r.ExpiresAt.After(time.Now())) {
				return ErrConflict
			}
			if (!rollback && (r.Status != "approved" || r.BaseVersion != skill.ActiveVersion)) || (rollback && r.Status != "superseded") {
				return ErrConflict
			}
			if e = checkSources(ctx, tx, r); e != nil {
				return e
			}
			if e = checkRelations(ctx, tx, r); e != nil {
				return e
			}
			if e = validation(ctx, tx, r, vid, vid != target.ValidationID); e != nil {
				return e
			}
			if d.Role == roleService && !rollback {
				if e = checkAutomaticSkillPromotion(ctx, tx, r, in, automation); e != nil {
					return e
				}
			}
			prev := r.Revision
			r.Revision++
			r.Status = "active"
			r.ValidationID = vid
			r.UpdatedAt = time.Now().UTC()
			if e = tx.Replace(ctx, "skill_versions", r, prev); e != nil {
				return e
			}
			if e = enqueue(ctx, tx, "skill_versions", r); e != nil {
				return e
			}
		}
		if skill.ActiveVersion != "" {
			old, e := tx.Get(ctx, "skill_versions", in.Scope, skill.ActiveVersion)
			if e != nil {
				return e
			}
			prev := old.Revision
			old.Status = "superseded"
			old.Revision++
			old.UpdatedAt = time.Now().UTC()
			if e = tx.Replace(ctx, "skill_versions", old, prev); e != nil {
				return e
			}
		}
		prev := skill.Revision
		skill.ActiveVersion = in.VersionID
		skill.Revision++
		skill.UpdatedAt = time.Now().UTC()
		if e = tx.Replace(ctx, "skills", skill, prev); e != nil {
			return e
		}
		// Applying also changes the header and old version: neither may invalidate
		// the selected version's evaluation evidence within this transaction.
		if in.VersionID != "" {
			if e = validation(ctx, tx, target, vid, false); e != nil {
				return e
			}
		}
		result = skill
		return audit(ctx, tx, in.Scope, d.PrincipalID, action+":skills", id)
	})
	return result, e
}
func (s *Service) Delete(ctx context.Context, token, col, id string, in ChangeInput) error {
	if col == colProviderUsage {
		return ErrInvalid
	}
	if !knownCollection(col) || col == "audit_events" || col == "jobs" || col == "skills" || col == "execution_traces" || col == colConsultations || col == colHandoffs || isKnowledgeCollection(col) || isWikiCollection(col) {
		return ErrInvalid
	}
	if col == "sources" {
		if _, ok := s.Repo.(privacyStore); ok {
			_, err := s.RequestPrivacyDeletion(ctx, token, PrivacyDeletionRequest{Scope: in.Scope, IdempotencyKey: "legacy-source-delete-" + ContentHash(id+":"+fmt.Sprint(in.ExpectedRevision)), TargetType: "source_event", TargetIDs: []string{id}, Reason: "legacy_source_delete", RetentionPolicyRef: retentionExternal, ExpectedRevision: in.ExpectedRevision})
			return err
		}
		// Production repositories without the canonical quarantine saga fail
		// closed. A private test-only marker keeps legacy erasure unit coverage.
		if _, testOnly := s.Repo.(legacySourceDeleteTestStore); !testOnly {
			return ErrUnavailable
		}
	}
	d, e := s.authorize(ctx, token, "delete:"+col, in.Scope, id, "")
	if e != nil {
		return e
	}
	if d.Role != "human" {
		return ErrDenied
	}
	if col == "memories" {
		return s.deleteMemoryAndProposal(ctx, token, id, in, d)
	}
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		current, e := tx.Get(ctx, col, in.Scope, id)
		if e != nil {
			return e
		}
		if current.PrivacyQuarantined {
			return ErrConflict
		}
		if col == "memory_relations" {
			if current.Kind == "conflicts_with" && current.Status == "active" && current.Conflict != nil {
				return ErrConflict
			}
		}
		if col == "skill_versions" {
			h, e := tx.Get(ctx, "skills", in.Scope, current.SkillID)
			if e != nil {
				return e
			}
			if h.ActiveVersion == id {
				return ErrConflict
			}
		}
		if e := tx.Delete(ctx, col, in.Scope, id, in.ExpectedRevision); e != nil {
			return e
		}
		if col == "memory_candidates" {
			if e := enqueueMemoryErasure(ctx, tx, col, in.Scope, id, in.ExpectedRevision, d.PrincipalID, nil, ""); e != nil {
				return e
			}
		}
		if col == "sources" {
			now := time.Now().UTC().Truncate(time.Millisecond)
			receipt := Record{ID: sourceDeletionID(in.Scope, id), Scope: in.Scope, Kind: "source_deletion", Status: "done", Revision: 1, CreatedAt: now, UpdatedAt: now, Target: &Ref{Collection: "sources", ID: id, Revision: in.ExpectedRevision}}
			if e := tx.Insert(ctx, "jobs", receipt); e != nil {
				return e
			}
			if e := enqueueSourceMemoryErasure(ctx, tx, in.Scope, id, in.ExpectedRevision, d.PrincipalID); e != nil {
				return e
			}
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "delete:"+col, id)
	})
}
func HitFromRecord(col string, r Record) Hit {
	origin := "memory"
	if col == colReflections {
		origin = "reflection"
	}
	if col == colSkillVersions {
		origin = "skill"
	}
	if col == colMemories || col == "memory_candidates" {
		normalizeMemoryProvenance(&r)
	}
	hit := Hit{MemoryContext: copyMemoryContext(r.MemoryContext), TrustLevel: r.TrustLevel, CreatedBy: r.CreatedBy, CreatedAt: &r.CreatedAt, Tags: r.Tags, EffectiveAt: r.EffectiveAt, Digest: ContentHash(recordContent(r)), Kind: r.Kind, Sensitivity: r.Sensitivity, ID: r.ID, Collection: col, Origin: origin, Scope: r.Scope, Status: r.Status, Revision: r.Revision, Text: r.Text, Summary: r.Summary, Sources: r.Sources, SourceEventID: r.SourceEventID, TraceID: r.TraceID, VerificationStatus: r.VerificationStatus, UpdatedAt: r.UpdatedAt, Trust: r.Trust, Relations: r.Relations, Authority: "data_only_untrusted"}
	if col == colMemories {
		hit.ApprovalOrigin = memoryApprovalOrigin(r)
		hit.Reach, hit.ReachProject = memoryReach(r), r.ReachProject
		hit.ConflictPending = r.ConflictID != ""
		hit.Supersedes = append([]Ref(nil), r.Supersedes...)
		hit.SupersededBy = r.SupersededBy
	}
	if r.Generator != nil {
		hit.GeneratorModel = r.Generator.Model
	}
	return hit
}
func recordContent(r Record) string {
	if r.MemoryContext != nil {
		return structuredText(struct {
			Text    string         `json:"text"`
			Summary string         `json:"summary"`
			Context *MemoryContext `json:"memory_context"`
		}{r.Text, r.Summary, r.MemoryContext})
	}
	if r.Reflection != nil {
		return structuredText(map[string]any{"reflection": r.Reflection, "scope": r.Scope, "sources": r.Sources, "created_at": r.CreatedAt.UTC().Truncate(time.Millisecond)})
	}
	if r.Analysis != nil {
		return structuredText(r.Analysis)
	}
	return fmt.Sprintf("%s\n%s", r.Text, r.Summary)
}
