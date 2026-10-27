package memory

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	errBlobDigestMismatch = errors.New("digest_mismatch")
	errBlobTooLarge       = errors.New("artifact_too_large")
	ErrSecretDetected     = errors.New("secret_detected")
	ErrNotFound           = errors.New("not_found")
	ErrConflict           = errors.New("revision_conflict")
	// ErrHandleTaken is a conflict (errors.Is ErrConflict holds) that the API names apart, so a client can say "name taken".
	ErrHandleTaken = fmt.Errorf("handle_taken: %w", ErrConflict)
	ErrInvalid     = errors.New("invalid_request")
	ErrDenied      = errors.New("access_denied")
	ErrUnavailable = errors.New("dependency_unavailable")
	// ErrSemanticLaneDown is the typed retrieval failure for a strict semantic
	// or fusion request whose embedding lane failed and whose policy forbids
	// the deterministic fallback.
	ErrSemanticLaneDown = errors.New("semantic_lane_unavailable")
	ErrModelBudget      = errors.New("model_budget_exceeded")
	ErrCandidateQuota   = errors.New("candidate_daily_limit")
)

var Collections = []string{"skill_trials", "knowledge_sources", "knowledge_documents", "knowledge_chunks", "knowledge_bindings", "retrieval_logs", "consultations", "agent_handoffs", "sources", "memories", "memory_candidates", "memory_relations", "skills", "skill_versions", "agent_runs", "execution_traces", "tool_calls", "evaluations", "reflections", "audit_events", "jobs", "provider_usage", "blobs", "artifacts", "agent_tools", "prompts", "design_systems", "crew", "crew_runs", "providers", "crew_assists", "code_reviews", "maintenance_proposals", "routing", "routing_calls", "skill_routes", "usage_sessions", "crew_meetings", "discord_bots", "merge_proposals", "topic_digests", "user_digests", "wiki_pages", "wiki_links"}

const (
	VerificationCandidate = "candidate"
	VerificationVerified  = "verified"
)

type Scope struct {
	WorkspaceID string `json:"workspace_id" bson:"workspace_id"`
	ProjectID   string `json:"project_id" bson:"project_id"`
	UserID      string `json:"user_id,omitempty" bson:"user_id"`
	AgentID     string `json:"agent_id,omitempty" bson:"agent_id"`
}

type SourceSpan struct {
	ContentHash string `json:"content_hash" bson:"content_hash"`
	Start       int    `json:"start" bson:"start"`
	End         int    `json:"end" bson:"end"`
}

type SourceRef struct {
	Span     *SourceSpan `json:"span,omitempty" bson:"span,omitempty"`
	SourceID string      `json:"source_id" bson:"source_id"`
	Locator  string      `json:"locator" bson:"locator"`
}

type Ref struct {
	Collection string `json:"collection" bson:"collection"`
	ID         string `json:"id" bson:"id"`
	Revision   int64  `json:"revision" bson:"revision"`
}

type Embedding struct {
	Provider    string    `json:"provider" bson:"provider"`
	Model       string    `json:"model" bson:"model"`
	Revision    string    `json:"revision" bson:"revision"`
	Dimension   int       `json:"dimension" bson:"dimension"`
	Space       string    `json:"space" bson:"space"`
	EmbeddedAt  time.Time `json:"embedded_at" bson:"embedded_at"`
	ContentHash string    `json:"content_hash" bson:"content_hash"`
	Vector      []float64 `json:"-" bson:"vector"`
}

type ProviderUsage struct {
	Provider     string `json:"provider" bson:"provider"`
	Model        string `json:"model" bson:"model"`
	Requests     int64  `json:"requests" bson:"requests"`
	InputTokens  int64  `json:"input_tokens" bson:"input_tokens"`
	OutputTokens int64  `json:"output_tokens" bson:"output_tokens"`
	TotalTokens  int64  `json:"total_tokens" bson:"total_tokens"`
}

type EmbeddingBatch struct {
	Items []Embedding
	Usage ProviderUsage
}

type ProviderCost struct {
	Operation            string `json:"operation" bson:"operation"`
	Provider             string `json:"provider" bson:"provider"`
	Model                string `json:"model" bson:"model"`
	PricingRevision      string `json:"pricing_revision,omitempty" bson:"pricing_revision,omitempty"`
	Requests             int64  `json:"requests" bson:"requests"`
	InputTokens          int64  `json:"input_tokens" bson:"input_tokens"`
	OutputTokens         int64  `json:"output_tokens" bson:"output_tokens"`
	TotalTokens          int64  `json:"total_tokens" bson:"total_tokens"`
	EstimatedCostPicoUSD int64  `json:"estimated_cost_pico_usd" bson:"estimated_cost_pico_usd"`
	PricingAvailable     bool   `json:"pricing_available" bson:"pricing_available"`
}

// Record keeps original text independent of replaceable embeddings. References
// bind evidence to a specific immutable content/version revision.
type Record struct {
	MemoryContext *MemoryContext `json:"memory_context,omitempty" bson:"memory_context,omitempty"`
	// Reach (ST-824): where a memory is recalled; "" reads as memoryReach's default. See memory_reach.go.
	Reach        string `json:"reach,omitempty" bson:"reach,omitempty"`
	ReachProject string `json:"reach_project,omitempty" bson:"reach_project,omitempty"`
	ReachOrigin  string `json:"-" bson:"reach_origin,omitempty"`
	// ReachSuggestion (ST-825) is the save-time proposal; ReachSuggestionDismissed is "reach|project" a person turned down.
	ReachSuggestion          *ReachSuggestion     `json:"reach_suggestion,omitempty" bson:"reach_suggestion,omitempty"`
	ReachSuggestionDismissed string               `json:"reach_suggestion_dismissed,omitempty" bson:"reach_suggestion_dismissed,omitempty"`
	ModelBudget              *ModelBudgetState    `json:"-" bson:"model_budget,omitempty"`
	SkillBundle              *SkillBundle         `json:"skill_bundle,omitempty" bson:"skill_bundle,omitempty"`
	SkillBundleSHA256        string               `json:"skill_bundle_sha256,omitempty" bson:"skill_bundle_sha256,omitempty"`
	SkillTrial               *SkillTrial          `json:"skill_trial,omitempty" bson:"skill_trial,omitempty"`
	SkillBasis               *SkillBasis          `json:"skill_basis,omitempty" bson:"skill_basis,omitempty"`
	ComparisonPlan           *SkillComparisonPlan `json:"comparison_plan,omitempty" bson:"comparison_plan,omitempty"`
	MemoryTimeline           []MemoryState        `json:"-" bson:"memory_timeline,omitempty"`
	ArchiveMessages          []ArchiveMessageRef  `json:"archive_messages,omitempty" bson:"archive_messages,omitempty"`

	KnowledgeRetrieval   *KnowledgeRetrievalLog `json:"knowledge_retrieval,omitempty" bson:"knowledge_retrieval,omitempty"`
	Knowledge            *KnowledgeRecord       `json:"knowledge,omitempty" bson:"knowledge,omitempty"`
	Wiki                 *WikiRecord            `json:"wiki,omitempty" bson:"wiki,omitempty"`
	Consultation         *Consultation          `json:"consultation,omitempty" bson:"consultation,omitempty"`
	Handoff              *SpecialistHandoff     `json:"handoff,omitempty" bson:"handoff,omitempty"`
	Audit                *AuditMetadata         `json:"audit,omitempty" bson:"audit,omitempty"`
	TrustLevel           string                 `json:"trust_level,omitempty" bson:"trust_level,omitempty"`
	NormalizedKey        string                 `json:"normalized_key,omitempty" bson:"normalized_key,omitempty"`
	Confidence           *float64               `json:"confidence,omitempty" bson:"confidence,omitempty"`
	Tags                 []string               `json:"tags,omitempty" bson:"tags,omitempty"`
	BlobRootID           string                 `json:"-" bson:"blob_root_id,omitempty"`
	ArtifactGeneration   string                 `json:"-" bson:"artifact_generation,omitempty"`
	Blob                 *blobMetadata          `json:"-" bson:"blob,omitempty"`
	Artifact             *artifactMetadata      `json:"-" bson:"artifact,omitempty"`
	ErasureParentID      string                 `json:"-" bson:"erasure_parent_id,omitempty"`
	ContentErased        bool                   `json:"content_erased,omitempty" bson:"content_erased,omitempty"`
	MemoryReferenceFence int64                  `json:"-" bson:"memory_reference_fence,omitempty"`
	SourceMemoryErasure  *SourceMemoryErasure   `json:"-" bson:"source_memory_erasure,omitempty"`
	PrivacyQuarantined   bool                   `json:"privacy_quarantined,omitempty" bson:"privacy_quarantined,omitempty"`
	PrivacyDeletion      *PrivacyDeletion       `json:"privacy_deletion,omitempty" bson:"privacy_deletion,omitempty"`
	// SessionArchive carries encrypted session-archive chunks (cipher, parent)
	// and header metadata. JSON-silent: raw originals never leave the store.
	SessionArchive *SessionArchiveData `json:"-" bson:"session_archive,omitempty"`
	// WorkCheckpoint carries program work-state packages and their
	// idempotency receipts. JSON-silent: packages are served only through the
	// dedicated /v1/work-checkpoints endpoints, never through generic Record
	// serialization; receipts carry request hash/id/revision only.
	WorkCheckpoint *WorkCheckpoint `json:"-" bson:"work_checkpoint,omitempty"`
	// MemorySignal is the derived ST-813 trust signal on a jobs row (kind memory_signal).
	MemorySignal *MemorySignal `json:"memory_signal,omitempty" bson:"memory_signal,omitempty"`

	// StaleMark: written from a memory a person has since replaced (stale_cascade.go).
	StaleMark *StaleMark `json:"stale_mark,omitempty" bson:"stale_mark,omitempty"`
	// ProofCount and LastConfirmedAt: how often reflect saw this lesson hold again (memory_confirm.go).
	ProofCount      int        `json:"proof_count,omitempty" bson:"proof_count,omitempty"`
	LastConfirmedAt *time.Time `json:"last_confirmed_at,omitempty" bson:"last_confirmed_at,omitempty"`

	RunCallFence       int64                    `json:"-" bson:"run_call_fence,omitempty"`
	Run                *RunDetails              `json:"run,omitempty" bson:"run,omitempty"`
	ToolCall           *ToolCall                `json:"tool_call,omitempty" bson:"tool_call,omitempty"`
	QuotaUsed          int                      `json:"-" bson:"quota_used,omitempty"`
	Usage              *ContextUsage            `json:"usage,omitempty" bson:"usage,omitempty"`
	Cost               *ProviderCost            `json:"cost,omitempty" bson:"cost,omitempty"`
	Proposal           *Ref                     `json:"proposal,omitempty" bson:"proposal,omitempty"`
	ConflictID         string                   `json:"conflict_id,omitempty" bson:"conflict_id,omitempty"`
	Conflict           *ConflictDetails         `json:"conflict,omitempty" bson:"conflict,omitempty"`
	SupersededBy       *Ref                     `json:"superseded_by,omitempty" bson:"superseded_by,omitempty"`
	Supersedes         []Ref                    `json:"supersedes,omitempty" bson:"supersedes,omitempty"`
	Review             *ReviewDecision          `json:"review,omitempty" bson:"review,omitempty"`
	Classification     *CandidateClassification `json:"classification,omitempty" bson:"classification,omitempty"`
	OriginProduct      string                   `json:"origin_product,omitempty" bson:"origin_product,omitempty"`
	SourceRef          string                   `json:"source_ref,omitempty" bson:"source_ref,omitempty"`
	SourceEventID      string                   `json:"source_event_id,omitempty" bson:"source_event_id,omitempty"`
	TraceID            string                   `json:"trace_id,omitempty" bson:"trace_id,omitempty"`
	VerificationStatus string                   `json:"verification_status,omitempty" bson:"verification_status,omitempty"`
	CollectedAt        *time.Time               `json:"collected_at,omitempty" bson:"collected_at,omitempty"`
	Generator          *Generator               `json:"generator,omitempty" bson:"generator,omitempty"`
	CreatedByType      string                   `json:"created_by_type,omitempty" bson:"created_by_type,omitempty"`
	ContentHash        string                   `json:"content_hash,omitempty" bson:"content_hash,omitempty"`
	Duplicate          bool                     `json:"duplicate" bson:"duplicate,omitempty"`
	RequestHash        string                   `json:"-" bson:"request_hash,omitempty"`
	Sensitivity        string                   `json:"sensitivity,omitempty" bson:"sensitivity,omitempty"`
	Inspection         *Inspection              `json:"inspection,omitempty" bson:"inspection,omitempty"`
	Reflection         *Reflection              `json:"reflection,omitempty" bson:"reflection,omitempty"`
	Analysis           *SkillAnalysis           `json:"analysis,omitempty" bson:"analysis,omitempty"`
	ID                 string                   `json:"id" bson:"_id"`
	Scope              Scope                    `json:"scope" bson:"scope"`
	Kind               string                   `json:"kind" bson:"kind"`
	Status             string                   `json:"status" bson:"status"`
	Revision           int64                    `json:"revision" bson:"revision"`
	Text               string                   `json:"text" bson:"text"`
	Summary            string                   `json:"summary,omitempty" bson:"summary"`
	Keywords           []string                 `json:"keywords,omitempty" bson:"keywords"`
	KeywordsLower      []string                 `json:"-" bson:"keywords_lower"`
	Sources            []SourceRef              `json:"sources" bson:"sources"`
	Relations          []Ref                    `json:"relations,omitempty" bson:"relations"`
	CreatedAt          time.Time                `json:"created_at" bson:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at" bson:"updated_at"`
	EffectiveAt        *time.Time               `json:"effective_at,omitempty" bson:"effective_at,omitempty"`
	ExpiresAt          *time.Time               `json:"expires_at,omitempty" bson:"expires_at,omitempty"`
	ReviewDueAt        *time.Time               `json:"review_due_at,omitempty" bson:"review_due_at,omitempty"`
	CreatedBy          string                   `json:"created_by" bson:"created_by"`
	Trust              float64                  `json:"trust" bson:"trust"`
	SkillID            string                   `json:"skill_id,omitempty" bson:"skill_id,omitempty"`
	ActiveVersion      string                   `json:"active_version,omitempty" bson:"active_version,omitempty"`
	BaseVersion        string                   `json:"base_version,omitempty" bson:"base_version,omitempty"`
	// Skill hub: ordered version IDs on the parent skill (index+1 is the
	// public version number), import origin and runtime compatibility on a
	// version. Content stays immutable; only lifecycle status moves.
	VersionHistory     []string             `json:"version_history,omitempty" bson:"version_history,omitempty"`
	SkillOrigin        *SkillOrigin         `json:"skill_origin,omitempty" bson:"skill_origin,omitempty"`
	AgentTools         *AgentTools          `json:"agent_tools,omitempty" bson:"agent_tools,omitempty"`
	Providers          *Providers           `json:"providers,omitempty" bson:"providers,omitempty"`
	ImagePrompt        *ImagePrompt         `json:"image_prompt,omitempty" bson:"image_prompt,omitempty"`
	Crew               *Crew                `json:"crew,omitempty" bson:"crew,omitempty"`
	CrewRun            *CrewRun             `json:"crew_run,omitempty" bson:"crew_run,omitempty"`
	FlowDelivery       *FlowDelivery        `json:"delivery,omitempty" bson:"delivery,omitempty"`
	FlowEffect         *FlowEffect          `json:"flow_effect,omitempty" bson:"flow_effect,omitempty"`
	FlowAttempt        *FlowAttempt         `json:"flow_attempt,omitempty" bson:"flow_attempt,omitempty"`
	FlowBudget         *FlowBudget          `json:"flow_budget,omitempty" bson:"flow_budget,omitempty"`
	FlowImprovement    *FlowImprovement     `json:"flow_improvement,omitempty" bson:"flow_improvement,omitempty"`
	CrewAssist         *CrewAssist          `json:"crew_assist,omitempty" bson:"crew_assist,omitempty"`
	CrewMeeting        *CrewMeeting         `json:"crew_meeting,omitempty" bson:"crew_meeting,omitempty"`
	DiscordBot         *DiscordBot          `json:"discord_bot,omitempty" bson:"discord_bot,omitempty"`
	DiscordSettings    *DiscordSettings     `json:"discord_settings,omitempty" bson:"discord_settings,omitempty"`
	Routing            *Routing             `json:"routing,omitempty" bson:"routing,omitempty"`
	RoutingCall        *RoutingCall         `json:"routing_call,omitempty" bson:"routing_call,omitempty"`
	SkillRoute         *SkillRoute          `json:"skill_route,omitempty" bson:"skill_route,omitempty"`
	UsageSession       *UsageSession        `json:"usage_session,omitempty" bson:"usage_session,omitempty"`
	CodeReviews        *CodeReviews         `json:"code_reviews,omitempty" bson:"code_reviews,omitempty"`
	DesignSystem       *DesignSystem        `json:"design_system,omitempty" bson:"design_system,omitempty"`
	Maintenance        *MaintenanceProposal `json:"maintenance,omitempty" bson:"maintenance,omitempty"`
	Merge              *MergeRecord         `json:"merge,omitempty" bson:"merge,omitempty"`
	Digest             *DigestData          `json:"digest,omitempty" bson:"digest,omitempty"`
	SkillCompatibility map[string]string    `json:"skill_compatibility,omitempty" bson:"skill_compatibility,omitempty"`
	ApprovedBy         string               `json:"approved_by,omitempty" bson:"approved_by,omitempty"`
	ValidationID       string               `json:"validation_id,omitempty" bson:"validation_id,omitempty"`
	Evaluation         *Evaluation          `json:"evaluation,omitempty" bson:"evaluation,omitempty"`
	Passed             bool                 `json:"passed,omitempty" bson:"passed"`
	Embedding          *Embedding           `json:"embedding,omitempty" bson:"embedding,omitempty"`
	Target             *Ref                 `json:"target,omitempty" bson:"target,omitempty"`
	RunAt              *time.Time           `json:"-" bson:"run_at,omitempty"`
	LeaseUntil         time.Time            `json:"-" bson:"lease_until"`
	LeaseToken         string               `json:"-" bson:"lease_token"`
	Attempts           int                  `json:"-" bson:"attempts"`

	// RejectScore is the review classifier's advice when the queue is listed; never stored.
	RejectScore *float64 `json:"reject_score,omitempty" bson:"-"`
}

// VectorRank is one pgvector cosine distance for an already-authorized row.
// Lower distance is closer.
type VectorRank struct {
	ID       string  `json:"id"`
	Distance float64 `json:"distance"`
}

type Tx interface {
	FenceSearchRead(context.Context, Scope, string, string, int64) error
	// RankVectors orders an explicit, already-authorized ID allowlist by vector
	// distance. It never widens the candidate set.
	RankVectors(context.Context, Scope, string, []string, []float64, string, int) ([]VectorRank, error)
	// RankKnowledgeText full-text ranks an explicit knowledge chunk ID allowlist; it never widens it.
	RankKnowledgeText(context.Context, Scope, []string, []string) ([]KnowledgeTextRank, error)
	// SourceDerivedReferences returns at most sourceDerivedCountLimit+1 references in ascending ID order.
	SourceDerivedReferences(context.Context, string, DerivedCountInput) ([]Ref, error)
	// CheckArtifactPrivacyHold fences GC against holds on detached cleanup intents.
	CheckArtifactPrivacyHold(context.Context, string) error
	BlobReferences(context.Context, string) (int64, error)
	DeletionRecords(context.Context, Scope) ([]Record, error)
	ErasureChildren(context.Context, Scope, string) ([]Record, error)
	MemoryErasureBatch(context.Context, Scope, string, bool, string, string) ([]Ref, error)
	FenceRelationMember(context.Context, Scope, string, string) error
	FenceSource(context.Context, Scope, string) error
	SourceErasureBatch(context.Context, Scope, string, string) ([]Ref, error)
	DeletionReferences(context.Context, string, Scope, Ref, int) ([]Ref, error)
	EvaluationsFor(context.Context, Scope, Ref) ([]Record, error)
	CheckRunToolTimes(context.Context, Scope, string, time.Time) error
	FenceRunCall(context.Context, Scope, string, int64) error
	SourceMetadata(context.Context, Scope, []string, bool) ([]Record, error)
	Get(context.Context, string, Scope, string) (Record, error)
	Insert(context.Context, string, Record) error
	Replace(context.Context, string, Record, int64) error
	Delete(context.Context, string, Scope, string, int64) error
}

type Repository interface {
	ListFlowDeliveries(context.Context, Scope, string, string, string, string, int) ([]Record, error)
	ListSkillTrialRecords(context.Context, Scope, string, string, int, string) ([]Record, error)
	// ListRecords returns up to limit live-or-not rows of one collection in a scope, newest first.
	ListRecords(context.Context, Scope, string, int) ([]Record, error)
	// ListRecordsByStatus is ListRecords for one status.
	ListRecordsByStatus(context.Context, Scope, string, string, int) ([]Record, error)
	// ListCrewRunsByStatus returns up to limit crew runs of one status in a scope, newest first.
	ListCrewRunsByStatus(context.Context, Scope, string, int) ([]Record, error)
	SearchRelatedMemories(context.Context, SearchInput, Record) ([]Hit, error)
	ValidateBlobRestore(context.Context, map[string]int64) error
	BlobGCBatch(context.Context, string) ([]string, error)
	BlobStorageIdentity(context.Context) (string, error)
	ExpiringCandidates(context.Context, Scope, time.Time, int) ([]Record, error)
	MemoryUsage(context.Context, Scope, string) ([]Record, error)
	// RecentContextUsage returns the newest context_usage receipts of one scope (limit rows).
	RecentContextUsage(context.Context, Scope, int) ([]Record, error)
	// MemoryOutcomes pages one memory's memory_outcome evaluations, newest first (limit+1 rows).
	MemoryOutcomes(context.Context, MemoryOutcomeQuery) ([]Record, error)
	// MemoryOutcomeCounts aggregates memory_outcome evaluations per memory id under the same filters.
	MemoryOutcomeCounts(context.Context, MemoryOutcomeQuery) ([]MemoryOutcomeCount, error)
	// MemoryOutcomeSessionCounts groups judged sessions by confirmed delivery,
	// server-only return, or unknown observation (ST-819).
	MemoryOutcomeSessionCounts(context.Context, MemoryOutcomeQuery) ([]MemoryOutcomeSessionCount, error)
	// MemoryUseCounts aggregates context_usage receipts per memory id (all revisions) in one scope.
	MemoryUseCounts(context.Context, Scope, []string) ([]MemoryUseCount, error)
	// MemoryWorkCounts aggregates the host-observed work of the traces that delivered each memory id in one scope.
	MemoryWorkCounts(context.Context, Scope, []string) ([]MemoryWorkCount, error)
	// MemoryUsageWindow counts context_usage receipts per live memory created since the first time, and since the second.
	MemoryUsageWindow(context.Context, Scope, time.Time, time.Time) ([]MemoryUsageCount, error)
	// ST-820 graph reads; each applies the scope and memoryGraphWhere (reach) filter.
	MemoryGraphNodes(context.Context, MemoryGraphQuery, []string) ([]Record, error)
	// MemoryGraphMapNodes returns up to limit visible memories with id after the given one, by id (ST-820 all mode).
	MemoryGraphMapNodes(context.Context, MemoryGraphQuery, string, int) ([]Record, error)
	// MemoryGraphLinks returns memory_relations rows naming any of the memory ids (up to memoryGraphScan+1).
	MemoryGraphLinks(context.Context, MemoryGraphQuery, []string) ([]Record, error)
	// MemoryGraphSessions returns, per asking memory id, up to limit other visible memories of the
	// same reach and reach_project sharing one of its session tags, by id.
	MemoryGraphSessions(context.Context, MemoryGraphQuery, []string, int) ([]MemoryGraphSessionHit, error)
	// MemoryGraphSimilar returns up to limit nearest visible same-reach memories per id within maxDistance.
	MemoryGraphSimilar(context.Context, MemoryGraphQuery, []string, float64, int) ([]MemoryGraphSimilarPair, error)
	ListCandidates(context.Context, ReviewInput) ([]Record, error)
	ListSources(context.Context, SourceListInput) ([]Record, error)
	ListMemories(context.Context, MemoryListInput) ([]Record, error)
	ListSourceArtifacts(context.Context, string, ArtifactListInput) ([]Record, error)
	ListSourceDerived(context.Context, string, DerivedListInput) ([]Record, error)
	OperationalMetrics(context.Context, Scope) (StoreMetrics, error)
	SearchReady(context.Context) error
	SearchCollection(context.Context, string, SearchInput, []float64, string) ([][]Hit, error)
	Tx
	Transaction(context.Context, func(context.Context, Tx) error) error
	// Search returns ordered exact-keyword, native text and vector lists; unselected lanes stay empty.
	Search(context.Context, Scope, string, []float64, string, int, ...string) ([][]Hit, error)
	ClaimJob(context.Context, time.Time, time.Duration) (Record, error)
	FinishJob(context.Context, Record, bool) error
}

type Decision struct {
	Context          ContextScopeV1 `json:"context"`
	DecisionID       string         `json:"decision_id"`
	Allowed          bool           `json:"allowed"`
	PrincipalID      string         `json:"principal_id"`
	Role             string         `json:"role"`
	Scope            Scope          `json:"scope"`
	ExternalTransfer bool           `json:"external_transfer"`
	ContentSafe      bool           `json:"content_safe"`
}
type Authorization struct {
	Context    ContextScopeV1 `json:"context"`
	Action     string         `json:"action"`
	Scope      Scope          `json:"scope"`
	ResourceID string         `json:"resource_id,omitempty"`
	Content    string         `json:"content,omitempty"`
}
type Authorizer interface {
	Authorize(context.Context, string, Authorization) (Decision, error)
}
type Embedder interface {
	Embed(context.Context, []string, string) ([]Embedding, error)
	Space() string
}

type MeteredEmbedder interface {
	Embedder
	EmbedMeasured(context.Context, []string, string) (EmbeddingBatch, error)
}
type Hit struct {
	ProofCount      int        `json:"proof_count,omitempty" bson:"-"`
	LastConfirmedAt *time.Time `json:"last_confirmed_at,omitempty" bson:"-"`

	ApprovalOrigin     string         `json:"approval_origin,omitempty" bson:"-"`
	MemoryContext      *MemoryContext `json:"memory_context,omitempty" bson:"memory_context,omitempty"`
	Reach              string         `json:"reach,omitempty" bson:"reach,omitempty"`
	ReachProject       string         `json:"reach_project,omitempty" bson:"reach_project,omitempty"`
	ConflictPending    bool           `json:"conflict_pending,omitempty" bson:"conflict_pending,omitempty"`
	Supersedes         []Ref          `json:"supersedes,omitempty" bson:"supersedes,omitempty"`
	SupersededBy       *Ref           `json:"superseded_by,omitempty" bson:"superseded_by,omitempty"`
	TrustLevel         string         `json:"trust_level,omitempty" bson:"trust_level,omitempty"`
	GeneratorModel     string         `json:"generator_model,omitempty" bson:"generator_model,omitempty"`
	CreatedBy          string         `json:"created_by,omitempty" bson:"created_by,omitempty"`
	CreatedAt          *time.Time     `json:"created_at,omitempty" bson:"created_at,omitempty"`
	Tags               []string       `json:"tags,omitempty" bson:"tags,omitempty"`
	Digest             string         `json:"digest,omitempty" bson:"digest,omitempty"`
	ReviewStatus       string         `json:"review_status,omitempty" bson:"review_status,omitempty"`
	SourceEventID      string         `json:"source_event_id,omitempty" bson:"source_event_id,omitempty"`
	TraceID            string         `json:"trace_id,omitempty" bson:"trace_id,omitempty"`
	VerificationStatus string         `json:"verification_status,omitempty" bson:"verification_status,omitempty"`
	CollectedAt        *time.Time     `json:"collected_at,omitempty" bson:"collected_at,omitempty"`
	EffectiveAt        *time.Time     `json:"effective_at" bson:"effective_at,omitempty"`
	Rank               int            `json:"rank,omitempty" bson:"rank,omitempty"`
	Score              float64        `json:"score,omitempty" bson:"-"`
	Lanes              []string       `json:"lanes,omitempty" bson:"-"`
	VectorMatch        *VectorMatch   `json:"vector_match,omitempty" bson:"-"`
	Kind               string         `json:"kind,omitempty" bson:"kind"`
	Sensitivity        string         `json:"sensitivity,omitempty" bson:"sensitivity"`
	ID                 string         `json:"id" bson:"id"`
	Collection         string         `json:"collection" bson:"collection"`
	Origin             string         `json:"origin" bson:"origin"`
	Scope              Scope          `json:"scope" bson:"scope"`
	Status             string         `json:"status" bson:"status"`
	Revision           int64          `json:"revision" bson:"revision"`
	Text               string         `json:"text" bson:"text"`
	Summary            string         `json:"summary,omitempty" bson:"summary"`
	Sources            []SourceRef    `json:"sources" bson:"sources"`
	SourceURL          string         `json:"source_url,omitempty" bson:"source_url"`
	ImageURL           string         `json:"image_url,omitempty" bson:"image_url"`
	UpdatedAt          time.Time      `json:"updated_at" bson:"updated_at"`
	Trust              float64        `json:"trust" bson:"trust"`
	Relations          []Ref          `json:"relations,omitempty" bson:"relations"`
	Reasons            []string       `json:"reasons" bson:"reasons"`
	Authority          string         `json:"authority" bson:"authority"`
	// Repository-only evidence that its configured vector distance gate ran.
	// Service rehydrates, authorizes and recomputes before exposing VectorMatch.
	vectorDistanceLimit float64
}
type ContextBundle struct {
	History       []HistoricalHit `json:"history,omitempty"`
	ContextData   string          `json:"context_data,omitempty"`
	TokensUsed    int             `json:"tokens_used,omitempty"`
	TokenEncoding string          `json:"token_encoding,omitempty"`
	ContextID     string          `json:"context_id,omitempty"`
	Authority     string          `json:"authority"`
	Items         []Hit           `json:"items"`
	Degraded      []string        `json:"degraded,omitempty"`
}

type HistoricalHit struct {
	Hit
	ExpiresAt *time.Time `json:"expires_at"`
}
