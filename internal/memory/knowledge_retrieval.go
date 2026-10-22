package memory

// Deterministic retrieval ranks with Postgres full-text search over the
// chunks the binding, lifecycle and trust filters allowed.
import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"
)

type KnowledgeRetrieveInput struct {
	Scope          Scope    `json:"scope"`
	ProfileID      string   `json:"specialist_profile_id"`
	ProfileVersion string   `json:"specialist_profile_version_id"`
	SkillID        string   `json:"skill_id"`
	SkillVersion   string   `json:"skill_version"`
	BindingIDs     []string `json:"binding_ids"`
	Query          string   `json:"query"`
	MaxTokens      int      `json:"max_tokens"`
	Limit          int      `json:"limit"`
	Mode           string   `json:"retrieval_mode,omitempty"`
	// Fallback selects the failure contract for the semantic lanes:
	// "" / "fallback_allowed" degrades an embedder failure to deterministic
	// retrieval and records it; "strict" turns it into a typed
	// semantic_lane_unavailable failure instead. Benchmark and evaluation
	// callers must use strict.
	Fallback string `json:"retrieval_fallback,omitempty"`
}
type KnowledgeRevalidateInput struct {
	Scope          Scope  `json:"scope"`
	ProfileID      string `json:"specialist_profile_id"`
	ProfileVersion string `json:"specialist_profile_version_id"`
	SkillID        string `json:"skill_id"`
	SkillVersion   string `json:"skill_version"`
	RetrievalLogID string `json:"retrieval_log_id"`
	ContextHash    string `json:"context_hash"`
}
type KnowledgePin struct {
	BindingID         string `json:"binding_id" bson:"binding_id"`
	BindingRevision   int64  `json:"binding_revision" bson:"binding_revision"`
	KnowledgeSourceID string `json:"knowledge_source_id" bson:"knowledge_source_id"`
	SourceRevision    int64  `json:"source_revision" bson:"source_revision"`
	SourceVersion     string `json:"source_version" bson:"source_version"`
	DocumentID        string `json:"document_id" bson:"document_id"`
	DocumentRevision  int64  `json:"document_revision" bson:"document_revision"`
	ParserVersion     string `json:"parser_version" bson:"parser_version"`
	ChunkerVersion    string `json:"chunker_version" bson:"chunker_version"`
}
type KnowledgeSelection struct {
	BindingID      string   `json:"binding_id" bson:"binding_id"`
	ChunkID        string   `json:"chunk_id" bson:"chunk_id"`
	ChunkRevision  int64    `json:"chunk_revision" bson:"chunk_revision"`
	ContentHash    string   `json:"content_hash" bson:"content_hash"`
	Rank           int      `json:"rank,omitempty" bson:"rank,omitempty"`
	VectorDistance *float64 `json:"vector_distance,omitempty" bson:"vector_distance,omitempty"`
	FusionScore    *float64 `json:"fusion_score,omitempty" bson:"fusion_score,omitempty"`
}
type KnowledgeExclusion struct {
	ID     string `json:"id" bson:"id"`
	Reason string `json:"reason" bson:"reason"`
}

// Only identifiers, hashes and fixed reason codes. Never raw query/content.
type KnowledgeRetrievalLog struct {
	QueryHash          string               `json:"query_hash" bson:"query_hash"`
	BindingIDs         []string             `json:"binding_ids" bson:"binding_ids"`
	CandidateChunkIDs  []string             `json:"candidate_chunk_ids" bson:"candidate_chunk_ids"`
	Selected           []KnowledgeSelection `json:"selected" bson:"selected"`
	Exclusions         []KnowledgeExclusion `json:"exclusion_reasons" bson:"exclusion_reasons"`
	Pins               []KnowledgePin       `json:"pins" bson:"pins"`
	ContextHash        string               `json:"context_hash" bson:"context_hash"`
	ProfileID          string               `json:"specialist_profile_id" bson:"specialist_profile_id"`
	ProfileVersion     string               `json:"specialist_profile_version_id" bson:"specialist_profile_version_id"`
	SkillID            string               `json:"skill_id" bson:"skill_id"`
	SkillVersion       string               `json:"skill_version" bson:"skill_version"`
	MaxTokens          int                  `json:"max_tokens" bson:"max_tokens"`
	AdapterVersion     string               `json:"adapter_version" bson:"adapter_version"`
	RetrievalMode      string               `json:"retrieval_mode,omitempty" bson:"retrieval_mode,omitempty"`
	RequestedMode      string               `json:"requested_retrieval_mode,omitempty" bson:"requested_retrieval_mode,omitempty"`
	ModeFallback       string               `json:"retrieval_mode_fallback,omitempty" bson:"retrieval_mode_fallback,omitempty"`
	FallbackPolicy     string               `json:"retrieval_fallback,omitempty" bson:"retrieval_fallback,omitempty"`
	FallbackUsed       bool                 `json:"fallback_used,omitempty" bson:"fallback_used,omitempty"`
	EmbeddingModel     string               `json:"embedding_model,omitempty" bson:"embedding_model,omitempty"`
	EmbeddingRevision  string               `json:"embedding_revision,omitempty" bson:"embedding_revision,omitempty"`
	EmbeddingSpaceID   string               `json:"embedding_space_id,omitempty" bson:"embedding_space_id,omitempty"`
	EmbeddingDimension int                  `json:"embedding_dimension,omitempty" bson:"embedding_dimension,omitempty"`
}
type KnowledgeEvidence struct {
	BindingID      string `json:"binding_id"`
	SourceID       string `json:"source_id"`
	DocumentID     string `json:"document_id"`
	ChunkID        string `json:"chunk_id"`
	Content        string `json:"content"`
	ContentHash    string `json:"content_hash"`
	SourceVersion  string `json:"source_version"`
	ParserVersion  string `json:"parser_version"`
	ChunkerVersion string `json:"chunker_version"`
	TrustLevel     string `json:"trust_level"`
	SectionPath    string `json:"section_path"`
	Citation       string `json:"citation"`
	TokenEstimate  int    `json:"token_estimate"`
	Authority      string `json:"authority"`
	// Rank and scores stay empty in deterministic mode so its context hash is
	// unchanged by the semantic lanes.
	Rank           int      `json:"rank,omitempty"`
	VectorDistance *float64 `json:"vector_distance,omitempty"`
	FusionScore    *float64 `json:"fusion_score,omitempty"`
}
type KnowledgeContext struct {
	Authority string              `json:"authority"`
	Scope     Scope               `json:"scope"`
	Items     []KnowledgeEvidence `json:"items"`
}
type KnowledgeDependency struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type KnowledgeReceipt struct {
	Scope              Scope                 `json:"scope"`
	ProfileID          string                `json:"specialist_profile_id"`
	ProfileVersion     string                `json:"specialist_profile_version_id"`
	SkillID            string                `json:"skill_id"`
	SkillVersion       string                `json:"skill_version"`
	RetrievalLogID     string                `json:"retrieval_log_id"`
	ContextHash        string                `json:"context_hash"`
	Context            KnowledgeContext      `json:"context"`
	Pins               []KnowledgePin        `json:"pins"`
	SourceDependencies []KnowledgeDependency `json:"source_dependencies"`
	RetrievalMode      string                `json:"retrieval_mode,omitempty"`
	RequestedMode      string                `json:"requested_retrieval_mode,omitempty"`
	ModeFallback       string                `json:"retrieval_mode_fallback,omitempty"`
	FallbackPolicy     string                `json:"retrieval_fallback,omitempty"`
	FallbackUsed       bool                  `json:"fallback_used,omitempty"`
	EmbeddingModel     string                `json:"embedding_model,omitempty"`
	EmbeddingRevision  string                `json:"embedding_revision,omitempty"`
	EmbeddingSpaceID   string                `json:"embedding_space_id,omitempty"`
	EmbeddingDimension int                   `json:"embedding_dimension,omitempty"`
	// Counts and fixed reason codes only, so a caller can tell "nothing
	// matched" from "matched but excluded" without reading the log.
	CandidateCount   int            `json:"candidate_count"`
	SelectedCount    int            `json:"selected_count"`
	ExcludedCount    int            `json:"excluded_count"`
	ExclusionSummary map[string]int `json:"exclusion_summary"`
}

func validKnowledgeProfile(profile, version, skill, skillVersion string) bool {
	return slices.Contains([]string{"design_consultant", "illustrator", "listing_reviewer"}, profile) && version == "v1" && skill == profile+".v1" && skillVersion == "v1"
}
func knowledgeTrustRank(trust string) int {
	switch trust {
	case "untrusted":
		return 0
	case "user_supplied":
		return 1
	case "reviewed":
		return 2
	case "official":
		return 3
	}
	return -1
}
func knowledgeSourceFence(ctx context.Context, tx Tx, record Record) error {
	if record.ContentErased || record.PrivacyQuarantined || len(record.Sources) != 1 {
		return ErrNotFound
	}
	return tx.FenceSource(ctx, record.Scope, record.Sources[0].SourceID)
}
func knowledgeBindingRecords(ctx context.Context, tx Tx, scope Scope, id, profile, version, skill string) (Record, Record, Record, string, error) {
	b, err := tx.Get(ctx, "knowledge_bindings", scope, id)
	if err != nil {
		return b, Record{}, Record{}, "", err
	}
	if reason, err := knowledgeBindingState(ctx, tx, scope, b, profile, version, skill); reason != "" || err != nil {
		return b, Record{}, Record{}, reason, err
	}
	h, reason, err := knowledgeBindingSource(ctx, tx, scope, b)
	if reason != "" || err != nil {
		return b, h, Record{}, reason, err
	}
	doc, err := tx.Get(ctx, "knowledge_documents", scope, h.Knowledge.DocumentID)
	if err != nil {
		return b, h, doc, "", err
	}
	if doc.Knowledge == nil || !live(doc) || doc.Knowledge.SourceVersion != h.Knowledge.SourceVersion || doc.Knowledge.KnowledgeSourceID != h.ID || doc.SourceEventID != h.SourceEventID {
		return b, h, doc, "document_inactive", nil
	}
	for _, item := range []struct {
		col string
		r   Record
	}{{"knowledge_bindings", b}, {"knowledge_sources", h}, {"knowledge_documents", doc}} {
		if err = tx.FenceSearchRead(ctx, scope, item.col, item.r.ID, item.r.Revision); err != nil {
			return b, h, doc, "", err
		}
	}
	return b, h, doc, "", nil
}
func knowledgeBindingState(ctx context.Context, tx Tx, scope Scope, b Record, profile, version, skill string) (string, error) {
	if b.Knowledge == nil || b.Scope != scope || b.Knowledge.SpecialistProfileID != profile || b.Knowledge.SpecialistProfileVersionID != version || b.Knowledge.SkillID != skill {
		return "", ErrDenied
	}
	if err := knowledgeSourceFence(ctx, tx, b); err != nil {
		return "", err
	}
	k := b.Knowledge
	if b.Status != "active" || k.LifecycleStatus != "active_synthetic" {
		return "binding_inactive", nil
	}
	now := time.Now().UTC()
	if k.ValidFrom != nil && now.Before(*k.ValidFrom) || k.ValidTo != nil && !now.Before(*k.ValidTo) {
		return "binding_expired", nil
	}
	if !slices.Contains([]string{"synthetic_retrieval", "cite_required"}, k.AccessMode) {
		return "access_mode_disabled", nil
	}
	return "", nil
}
func knowledgeBindingSource(ctx context.Context, tx Tx, scope Scope, b Record) (Record, string, error) {
	k := b.Knowledge
	h, err := tx.Get(ctx, "knowledge_sources", scope, k.KnowledgeSourceID)
	if err != nil {
		return h, "", err
	}
	if h.Knowledge == nil || h.Scope != scope || h.SourceEventID != b.SourceEventID {
		return h, "", ErrDenied
	}
	if err = knowledgeSourceFence(ctx, tx, h); err != nil {
		return h, "", err
	}
	if !live(h) || h.Knowledge.LifecycleStatus != "active" {
		return h, "source_inactive", nil
	}
	if knowledgeTrustRank(k.TrustRequirement) < 0 || knowledgeTrustRank(h.Knowledge.TrustLevel) < max(2, knowledgeTrustRank(k.TrustRequirement)) {
		return h, "trust_requirement", nil
	}
	return h, "", nil
}
func knowledgePin(b, h, doc Record) KnowledgePin {
	return KnowledgePin{BindingID: b.ID, BindingRevision: b.Revision, KnowledgeSourceID: h.ID, SourceRevision: h.Revision, SourceVersion: h.Knowledge.SourceVersion, DocumentID: doc.ID, DocumentRevision: doc.Revision, ParserVersion: doc.Knowledge.ParserVersion, ChunkerVersion: doc.Knowledge.ChunkerVersion}
}
func knowledgeEvidence(b, h, doc, c Record) KnowledgeEvidence {
	return KnowledgeEvidence{BindingID: b.ID, SourceID: h.ID, DocumentID: doc.ID, ChunkID: c.ID, Content: c.Text, ContentHash: c.ContentHash, SourceVersion: h.Knowledge.SourceVersion, ParserVersion: doc.Knowledge.ParserVersion, ChunkerVersion: c.Knowledge.ChunkerVersion, TrustLevel: h.Knowledge.TrustLevel, SectionPath: c.Knowledge.SectionPath, Citation: "knowledge:" + h.ID + "/" + doc.ID + "/" + c.ID, TokenEstimate: c.Knowledge.TokenEstimate, Authority: "data_only_untrusted"}
}
func knowledgeContextHash(c KnowledgeContext, log *KnowledgeRetrievalLog) string {
	return ContentHash(structuredText(struct {
		Context                                                          KnowledgeContext
		Pins                                                             []KnowledgePin
		QueryHash, Profile, ProfileVersion, Skill, SkillVersion, Adapter string
	}{c, log.Pins, log.QueryHash, log.ProfileID, log.ProfileVersion, log.SkillID, log.SkillVersion, log.AdapterVersion}))
}
func knowledgeReceipt(id string, scope Scope, log *KnowledgeRetrievalLog, c KnowledgeContext, sources []SourceRef) KnowledgeReceipt {
	deps := []KnowledgeDependency{}
	for _, ref := range sources {
		deps = append(deps, KnowledgeDependency{Kind: "source_event", ID: ref.SourceID})
	}
	summary := map[string]int{}
	for _, e := range log.Exclusions {
		summary[e.Reason]++
	}
	return KnowledgeReceipt{Scope: scope, ProfileID: log.ProfileID, ProfileVersion: log.ProfileVersion, SkillID: log.SkillID, SkillVersion: log.SkillVersion, RetrievalLogID: id, ContextHash: log.ContextHash, Context: c, Pins: log.Pins, SourceDependencies: deps,
		RetrievalMode: log.RetrievalMode, RequestedMode: log.RequestedMode, ModeFallback: log.ModeFallback, FallbackPolicy: log.FallbackPolicy, FallbackUsed: log.FallbackUsed,
		EmbeddingModel: log.EmbeddingModel, EmbeddingRevision: log.EmbeddingRevision, EmbeddingSpaceID: log.EmbeddingSpaceID, EmbeddingDimension: log.EmbeddingDimension,
		CandidateCount: len(log.CandidateChunkIDs), SelectedCount: len(log.Selected), ExcludedCount: len(log.Exclusions), ExclusionSummary: summary}
}
func (s *Service) RetrieveKnowledge(ctx context.Context, token string, in KnowledgeRetrieveInput) (KnowledgeReceipt, error) {
	if !s.SyntheticHandoff || !validKnowledgeProfile(in.ProfileID, in.ProfileVersion, in.SkillID, in.SkillVersion) {
		return KnowledgeReceipt{}, ErrDenied
	}
	if in.MaxTokens == 0 {
		in.MaxTokens = 4096
	}
	if in.Limit == 0 {
		in.Limit = 4
	}
	if in.Mode == "" {
		in.Mode = knowledgeModeDeterministic
	}
	if !validKnowledgeRetrieve(in) {
		return KnowledgeReceipt{}, ErrInvalid
	}
	decision, err := s.knowledgeAuthorization(ctx, token, "retrieve:knowledge", in.Scope, "")
	if err != nil {
		return KnowledgeReceipt{}, err
	}
	ids := slices.Clone(in.BindingIDs)
	sort.Strings(ids)
	for i, id := range ids {
		if !validRunLabel(id, 256) || i > 0 && ids[i-1] == id {
			return KnowledgeReceipt{}, ErrInvalid
		}
	}
	// The embedding provider is a loopback dependency, never a transaction
	// participant. Under fallback_allowed a failure degrades to deterministic
	// retrieval and is recorded as an explicit fallback; under strict it is a
	// typed semantic_lane_unavailable failure and the deterministic fallback
	// never runs.
	policy := in.Fallback
	if policy == "" {
		policy = knowledgeFallbackAllowed
	}
	mode, fallback, embedding := in.Mode, "", (*Embedding)(nil)
	if mode != knowledgeModeDeterministic {
		embedding, err = s.embedKnowledgeQuery(ctx, decision, in.Query)
		if err != nil {
			reason := knowledgeFallbackReason(err)
			if policy == knowledgeFallbackStrict {
				failure := fmt.Errorf("semantic lane unavailable with retrieval_fallback=strict: %w: %w", ErrSemanticLaneDown, err)
				return KnowledgeReceipt{}, errors.Join(failure, s.recordKnowledgeRetrievalFailure(ctx, in.Scope, decision.PrincipalID, reason))
			}
			mode, fallback, embedding = knowledgeModeDeterministic, reason, nil
		}
	}
	var result KnowledgeReceipt
	semanticLaneEmpty := false
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		log := &KnowledgeRetrievalLog{QueryHash: ContentHash(in.Query), BindingIDs: ids, CandidateChunkIDs: []string{}, Selected: []KnowledgeSelection{}, Exclusions: []KnowledgeExclusion{}, Pins: []KnowledgePin{}, ProfileID: in.ProfileID, ProfileVersion: in.ProfileVersion, SkillID: in.SkillID, SkillVersion: in.SkillVersion, MaxTokens: in.MaxTokens, AdapterVersion: knowledgeAdapterVersion(mode)}
		knowledgeLogMode(log, in.Mode, mode, policy, fallback, embedding)
		c := KnowledgeContext{Authority: "data_only_untrusted", Scope: in.Scope, Items: []KnowledgeEvidence{}}
		if len([]rune(structuredText(c))) > in.MaxTokens {
			return ErrInvalid
		}
		sources := []SourceRef{}
		sourceSet := map[string]bool{}
		eligible := []knowledgeCandidate{}
		eligibleIDs := []string{}
		for _, id := range ids {
			b, h, doc, reason, e := knowledgeBindingRecords(ctx, tx, in.Scope, id, in.ProfileID, in.ProfileVersion, in.SkillID)
			if e != nil {
				return e
			}
			if !sourceSet[b.SourceEventID] {
				sourceSet[b.SourceEventID] = true
				sources = append(sources, SourceRef{SourceID: b.SourceEventID, Locator: "original"})
			}
			if reason != "" {
				log.Exclusions = append(log.Exclusions, KnowledgeExclusion{ID: id, Reason: reason})
				continue
			}
			log.Pins = append(log.Pins, knowledgePin(b, h, doc))
			if eligible, eligibleIDs, e = knowledgeBindingCandidates(ctx, tx, in.Scope, log, b, h, doc, embedding, eligible, eligibleIDs); e != nil {
				return e
			}
		}
		// The full-text lane ranks only the chunks the filters above allowed.
		sort.Strings(eligibleIDs)
		textRanks, e := tx.RankKnowledgeText(ctx, in.Scope, eligibleIDs, knowledgeQueryTerms(in.Query))
		if e != nil {
			return e
		}
		candidates := knowledgeMatchedCandidates(log, mode, eligible, textRanks)
		ordered, semanticRanked, e := knowledgeRank(ctx, tx, in.Scope, mode, candidates, embedding)
		if e != nil {
			return e
		}
		// fusion_strict: a fusion result is only a fusion result when the
		// vector lane actually ranked something. An empty semantic lane (no
		// indexed vectors reachable for these bindings) is a typed failure,
		// never a silently deterministic answer labelled fusion.
		if mode == knowledgeModeFusion && policy == knowledgeFallbackStrict && semanticRanked == 0 {
			semanticLaneEmpty = true
			return fmt.Errorf("fusion semantic lane ranked no candidates with retrieval_fallback=strict: %w", ErrSemanticLaneDown)
		}
		if e := knowledgeSelectEvidence(ctx, tx, in, mode, ordered, &c, log); e != nil {
			return e
		}
		sort.Slice(sources, func(i, j int) bool { return sources[i].SourceID < sources[j].SourceID })
		log.ContextHash = knowledgeContextHash(c, log)
		record := knowledgeRetrievalRecord(in.Scope, decision.PrincipalID, log, sources)
		if e := tx.Insert(ctx, colRetrievalLogs, record); e != nil {
			return e
		}
		if e := audit(ctx, tx, in.Scope, decision.PrincipalID, "retrieve:knowledge", record.ID); e != nil {
			return e
		}
		result = knowledgeReceipt(record.ID, in.Scope, log, c, sources)
		return nil
	})
	if err != nil && semanticLaneEmpty {
		return KnowledgeReceipt{}, errors.Join(err, s.recordKnowledgeRetrievalFailure(ctx, in.Scope, decision.PrincipalID, "semantic_lane_empty"))
	}
	return result, err
}
func validKnowledgeRetrieve(in KnowledgeRetrieveInput) bool {
	return len(in.BindingIDs) != 0 && len(in.BindingIDs) <= 8 && strings.TrimSpace(in.Query) != "" && len(in.Query) <= 2048 && !containsSecret(in.Query) && in.MaxTokens >= 256 && in.MaxTokens <= 8192 && in.Limit >= 1 && in.Limit <= 8 && validKnowledgeMode(in.Mode) && (in.Fallback == "" || validKnowledgeFallback(in.Fallback))
}

// shortcut: bounded sequential fixture scan; the vector lane ranks
// only what this scan already authorized.
func knowledgeBindingCandidates(ctx context.Context, tx Tx, scope Scope, log *KnowledgeRetrievalLog, b, h, doc Record, embedding *Embedding, eligible []knowledgeCandidate, eligibleIDs []string) ([]knowledgeCandidate, []string, error) {
	for _, documentID := range h.Knowledge.DocumentIDs {
		old, e := tx.Get(ctx, "knowledge_documents", scope, documentID)
		if e != nil {
			return eligible, eligibleIDs, e
		}
		if old.Knowledge == nil {
			return eligible, eligibleIDs, ErrConflict
		}
		for _, chunkID := range old.Knowledge.ChunkIDs {
			if len(log.CandidateChunkIDs) >= knowledgeCandidateLimit {
				return eligible, eligibleIDs, ErrInvalid
			}
			log.CandidateChunkIDs = append(log.CandidateChunkIDs, chunkID)
			if documentID != doc.ID {
				log.Exclusions = append(log.Exclusions, KnowledgeExclusion{ID: chunkID, Reason: "superseded_version"})
				continue
			}
			chunk, e := tx.Get(ctx, "knowledge_chunks", scope, chunkID)
			if e != nil {
				return eligible, eligibleIDs, e
			}
			if !validKnowledgeChunk(chunk, h, doc) {
				return eligible, eligibleIDs, ErrConflict
			}
			eligible = append(eligible, knowledgeCandidate{b: b, h: h, doc: doc, chunk: chunk, embedded: embedding != nil && knowledgeChunkEmbedded(chunk, embedding.Space)})
			if !slices.Contains(eligibleIDs, chunk.ID) {
				eligibleIDs = append(eligibleIDs, chunk.ID)
			}
		}
	}
	return eligible, eligibleIDs, nil
}

// One document is chunked by exactly one chunker version.
func validKnowledgeChunk(chunk, h, doc Record) bool {
	return chunk.Knowledge != nil && live(chunk) && !chunk.ContentErased && chunk.Knowledge.DocumentID == doc.ID && chunk.Knowledge.SourceVersion == h.Knowledge.SourceVersion && chunk.Knowledge.ChunkerVersion == doc.Knowledge.ChunkerVersion && chunk.SourceEventID == h.SourceEventID && ContentHash(chunk.Text) == chunk.ContentHash
}
func knowledgeMatchedCandidates(log *KnowledgeRetrievalLog, mode string, eligible []knowledgeCandidate, textRanks []KnowledgeTextRank) []knowledgeCandidate {
	matched := map[string]KnowledgeTextRank{}
	for _, rank := range textRanks {
		matched[rank.ID] = rank
	}
	candidates := []knowledgeCandidate{}
	for _, item := range eligible {
		rank := matched[item.chunk.ID]
		if reason := knowledgeCandidateExclusion(mode, rank.Matched, item.embedded); reason != "" {
			log.Exclusions = append(log.Exclusions, KnowledgeExclusion{ID: item.chunk.ID, Reason: reason})
			continue
		}
		item.overlap, item.textScore = rank.Matched, rank.Score
		candidates = append(candidates, item)
	}
	return candidates
}
func knowledgeSelectEvidence(ctx context.Context, tx Tx, in KnowledgeRetrieveInput, mode string, ordered []knowledgeCandidate, c *KnowledgeContext, log *KnowledgeRetrievalLog) error {
	selected := map[string]bool{}
	for _, item := range ordered {
		reason := ""
		if selected[item.chunk.ID] {
			reason = "duplicate_chunk"
		} else if len(c.Items) >= in.Limit {
			reason = "result_limit"
		}
		if reason != "" {
			log.Exclusions = append(log.Exclusions, KnowledgeExclusion{ID: item.chunk.ID, Reason: reason})
			continue
		}
		evidence := knowledgeEvidence(item.b, item.h, item.doc, item.chunk)
		knowledgeRankEvidence(&evidence, mode, item, len(c.Items)+1)
		c.Items = append(c.Items, evidence)
		if len([]rune(structuredText(*c))) > in.MaxTokens {
			c.Items = c.Items[:len(c.Items)-1]
			log.Exclusions = append(log.Exclusions, KnowledgeExclusion{ID: item.chunk.ID, Reason: "context_budget"})
			continue
		}
		if e := tx.FenceSearchRead(ctx, in.Scope, "knowledge_chunks", item.chunk.ID, item.chunk.Revision); e != nil {
			return e
		}
		selected[item.chunk.ID] = true
		log.Selected = append(log.Selected, knowledgeSelection(mode, item, len(c.Items)))
	}
	return nil
}
func knowledgeRetrievalRecord(scope Scope, principal string, log *KnowledgeRetrievalLog, sources []SourceRef) Record {
	record := consultationRecord("retrieval-"+ID(), scope, principal)
	record.Kind = "synthetic_knowledge_retrieval"
	record.OriginProduct = "synthetic-knowledge"
	record.KnowledgeRetrieval = log
	record.Sources = sources
	if len(sources) > 0 {
		record.SourceEventID = sources[0].SourceID
	}
	for _, pin := range log.Pins {
		record.Relations = append(record.Relations, Ref{Collection: "knowledge_bindings", ID: pin.BindingID, Revision: pin.BindingRevision}, Ref{Collection: "knowledge_documents", ID: pin.DocumentID, Revision: pin.DocumentRevision})
	}
	return record
}

// recordKnowledgeRetrievalFailure writes the typed strict-failure marker to
// the audit trail. No retrieval log row is created: a strict failure never
// served evidence, so it must not appear as a source-linked retrieval, and it
// must never carry an effective_mode that would suggest semantic results.
func (s *Service) recordKnowledgeRetrievalFailure(ctx context.Context, scope Scope, principal, reason string) error {
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return audit(ctx, tx, scope, principal, "retrieve:knowledge_failed", reason)
	})
}
func (s *Service) RevalidateKnowledge(ctx context.Context, token string, in KnowledgeRevalidateInput) (KnowledgeReceipt, error) {
	if !s.SyntheticHandoff || !validKnowledgeProfile(in.ProfileID, in.ProfileVersion, in.SkillID, in.SkillVersion) {
		return KnowledgeReceipt{}, ErrDenied
	}
	if !sourceHashPattern.MatchString(in.ContextHash) || !validRunLabel(in.RetrievalLogID, 256) {
		return KnowledgeReceipt{}, ErrInvalid
	}
	d, err := s.knowledgeAuthorization(ctx, token, "retrieve:knowledge", in.Scope, in.RetrievalLogID)
	if err != nil {
		return KnowledgeReceipt{}, err
	}
	var out KnowledgeReceipt
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, e := tx.Get(ctx, colRetrievalLogs, in.Scope, in.RetrievalLogID)
		if e != nil {
			return e
		}
		log := r.KnowledgeRetrieval
		if !live(r) || r.ContentErased || log == nil {
			return ErrNotFound
		}
		if !knowledgeLogMatches(log, in) {
			return ErrConflict
		}
		for _, ref := range r.Sources {
			if e = tx.FenceSource(ctx, in.Scope, ref.SourceID); e != nil {
				return e
			}
		}
		type records struct{ b, h, doc Record }
		current := map[string]records{}
		for _, pin := range log.Pins {
			b, h, doc, reason, e := knowledgeBindingRecords(ctx, tx, in.Scope, pin.BindingID, in.ProfileID, in.ProfileVersion, in.SkillID)
			if e != nil {
				return e
			}
			if reason != "" || !reflect.DeepEqual(pin, knowledgePin(b, h, doc)) {
				return ErrConflict
			}
			current[b.ID] = records{b, h, doc}
		}
		c := KnowledgeContext{Authority: "data_only_untrusted", Scope: in.Scope, Items: []KnowledgeEvidence{}}
		for _, selection := range log.Selected {
			records, ok := current[selection.BindingID]
			if !ok {
				return ErrConflict
			}
			chunk, e := tx.Get(ctx, "knowledge_chunks", in.Scope, selection.ChunkID)
			if e != nil {
				return e
			}
			if !validSelectedKnowledgeChunk(chunk, selection, records.h, records.doc) {
				return ErrConflict
			}
			if e = tx.FenceSearchRead(ctx, in.Scope, "knowledge_chunks", chunk.ID, chunk.Revision); e != nil {
				return e
			}
			evidence := knowledgeEvidence(records.b, records.h, records.doc, chunk)
			evidence.Rank, evidence.VectorDistance, evidence.FusionScore = selection.Rank, selection.VectorDistance, selection.FusionScore
			c.Items = append(c.Items, evidence)
		}
		if knowledgeContextHash(c, log) != in.ContextHash || len([]rune(structuredText(c))) > log.MaxTokens {
			return ErrConflict
		}
		if e = audit(ctx, tx, in.Scope, d.PrincipalID, "revalidate:knowledge", r.ID); e != nil {
			return e
		}
		out = knowledgeReceipt(r.ID, in.Scope, log, c, r.Sources)
		return nil
	})
	return out, err
}
func knowledgeLogMatches(log *KnowledgeRetrievalLog, in KnowledgeRevalidateInput) bool {
	return log.ContextHash == in.ContextHash && log.ProfileID == in.ProfileID && log.ProfileVersion == in.ProfileVersion && log.SkillID == in.SkillID && log.SkillVersion == in.SkillVersion
}
func validSelectedKnowledgeChunk(chunk Record, selection KnowledgeSelection, h, doc Record) bool {
	return chunk.Knowledge != nil && live(chunk) && !chunk.ContentErased && chunk.Revision == selection.ChunkRevision && chunk.ContentHash == selection.ContentHash && ContentHash(chunk.Text) == selection.ContentHash && chunk.Knowledge.DocumentID == doc.ID && chunk.SourceEventID == h.SourceEventID
}
func (s *Service) GetKnowledgeRetrievalLog(ctx context.Context, token, id string, scope Scope) (Record, error) {
	if !s.SyntheticHandoff {
		return Record{}, ErrDenied
	}
	_, err := s.knowledgeAuthorization(ctx, token, "retrieve:knowledge", scope, id)
	if err != nil {
		return Record{}, err
	}
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, e := tx.Get(ctx, colRetrievalLogs, scope, id)
		if e != nil {
			return e
		}
		if !live(r) || r.ContentErased || r.KnowledgeRetrieval == nil {
			return ErrNotFound
		}
		for _, ref := range r.Sources {
			if e = tx.FenceSource(ctx, scope, ref.SourceID); e != nil {
				return e
			}
		}
		out = r
		return nil
	})
	return out, err
}
func registerKnowledgeRetrievalHTTP(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/knowledge/retrieve", func(w http.ResponseWriter, r *http.Request) {
		var in KnowledgeRetrieveInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.RetrieveKnowledge(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/knowledge/revalidate", func(w http.ResponseWriter, r *http.Request) {
		var in KnowledgeRevalidateInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.RevalidateKnowledge(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/knowledge/retrieval-logs/{id}", func(w http.ResponseWriter, r *http.Request) {
		scope, e := parseSourceTraceQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.GetKnowledgeRetrievalLog(r.Context(), bearer(r), r.PathValue("id"), scope)
		respond(w, out, e)
	})
}
