package memory

// Read-only knowledge listing for the UI and the binding-resolving search the
// MCP search_knowledge tool calls. Listing returns metadata only: never the
// original file, never chunk text. Search resolves the caller profile's
// active bindings and then runs the ordinary RetrieveKnowledge, so bindings,
// trust, token budget, retrieval log and receipt are the same contract.
import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"time"
)

const (
	knowledgeListDefault      = 100
	knowledgeListMax          = 500
	knowledgeBindingScanLimit = 1000
	knowledgeSearchBindings   = 8
)

type KnowledgeBindingSummary struct {
	ID                         string     `json:"id"`
	Revision                   int64      `json:"revision"`
	Status                     string     `json:"status"`
	LifecycleStatus            string     `json:"lifecycle_status"`
	SpecialistProfileID        string     `json:"specialist_profile_id"`
	SpecialistProfileVersionID string     `json:"specialist_profile_version_id"`
	SkillID                    string     `json:"skill_id,omitempty"`
	AccessMode                 string     `json:"access_mode"`
	ValidFrom                  *time.Time `json:"valid_from,omitempty"`
	ValidTo                    *time.Time `json:"valid_to,omitempty"`
}

type KnowledgeSourceSummary struct {
	ID              string                    `json:"id"`
	Revision        int64                     `json:"revision"`
	Status          string                    `json:"status"`
	LifecycleStatus string                    `json:"lifecycle_status"`
	TrustLevel      string                    `json:"trust_level"`
	DisplayName     string                    `json:"display_name"`
	SourceType      string                    `json:"source_type"`
	Format          string                    `json:"format"`
	Synthetic       bool                      `json:"synthetic"`
	SourceVersion   string                    `json:"source_version"`
	DocumentID      string                    `json:"document_id"`
	DocumentCount   int                       `json:"document_count"`
	ChunkCount      int                       `json:"chunk_count"`
	ApprovedBy      string                    `json:"approved_by,omitempty"`
	CreatedAt       time.Time                 `json:"created_at"`
	UpdatedAt       time.Time                 `json:"updated_at"`
	Bindings        []KnowledgeBindingSummary `json:"bindings"`
}

type KnowledgeSourceList struct {
	Scope     Scope                    `json:"scope"`
	Items     []KnowledgeSourceSummary `json:"items"`
	Truncated bool                     `json:"truncated"`
}

type KnowledgeDocumentSummary struct {
	ID               string    `json:"id"`
	Revision         int64     `json:"revision"`
	Status           string    `json:"status"`
	Current          bool      `json:"current"`
	Filename         string    `json:"filename"`
	MIMEType         string    `json:"mime_type"`
	ContentHash      string    `json:"content_hash"`
	SourceVersion    string    `json:"source_version"`
	ParserVersion    string    `json:"parser_version"`
	ChunkerVersion   string    `json:"chunker_version"`
	ExtractionStatus string    `json:"extraction_status"`
	ErrorCode        string    `json:"error_code,omitempty"`
	ChunkCount       int       `json:"chunk_count"`
	IndexedChunks    int       `json:"indexed_chunks"`
	Sections         []string  `json:"sections"`
	CreatedAt        time.Time `json:"created_at"`
}

type KnowledgeDocumentList struct {
	Scope    Scope                      `json:"scope"`
	SourceID string                     `json:"source_id"`
	Items    []KnowledgeDocumentSummary `json:"items"`
}

func knowledgeBindingSummary(r Record) KnowledgeBindingSummary {
	k := r.Knowledge
	return KnowledgeBindingSummary{ID: r.ID, Revision: r.Revision, Status: r.Status, LifecycleStatus: k.LifecycleStatus, SpecialistProfileID: k.SpecialistProfileID, SpecialistProfileVersionID: k.SpecialistProfileVersionID, SkillID: k.SkillID, AccessMode: k.AccessMode, ValidFrom: k.ValidFrom, ValidTo: k.ValidTo}
}

// liveKnowledgeBindings lists the scope's bindings that are still live.
func (s *Service) liveKnowledgeBindings(ctx context.Context, scope Scope) ([]Record, error) {
	rows, err := s.Repo.ListRecords(ctx, scope, colKnowledgeBindings, knowledgeBindingScanLimit)
	if err != nil {
		return nil, err
	}
	out := []Record{}
	for _, r := range rows {
		if r.Scope != scope || knowledgeLive(ctx, s.Repo, r) != nil {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Service) ListKnowledgeSources(ctx context.Context, token string, scope Scope, limit int) (KnowledgeSourceList, error) {
	out := KnowledgeSourceList{Scope: scope, Items: []KnowledgeSourceSummary{}}
	if limit == 0 {
		limit = knowledgeListDefault
	}
	if limit < 1 || limit > knowledgeListMax {
		return out, ErrInvalid
	}
	if _, err := s.knowledgeAuthorization(ctx, token, "read:"+colKnowledgeSources, scope, ""); err != nil {
		return out, err
	}
	rows, err := s.Repo.ListRecords(ctx, scope, colKnowledgeSources, limit+1)
	if err != nil {
		return out, err
	}
	if len(rows) > limit {
		rows, out.Truncated = rows[:limit], true
	}
	bindings := map[string][]KnowledgeBindingSummary{}
	if _, err := s.knowledgeAuthorization(ctx, token, "read:"+colKnowledgeBindings, scope, ""); err == nil {
		all, err := s.liveKnowledgeBindings(ctx, scope)
		if err != nil {
			return out, err
		}
		for _, b := range all {
			bindings[b.Knowledge.KnowledgeSourceID] = append(bindings[b.Knowledge.KnowledgeSourceID], knowledgeBindingSummary(b))
		}
	}
	for _, r := range rows {
		if r.Scope != scope || knowledgeLive(ctx, s.Repo, r) != nil {
			continue
		}
		item := knowledgeCatalogSummary(r, bindings)
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// ListKnowledgeDocuments lists every stored version of one source, newest
// first. Section paths are read for the current document only.
func (s *Service) ListKnowledgeDocuments(ctx context.Context, token string, scope Scope, sourceID string) (KnowledgeDocumentList, error) {
	out := KnowledgeDocumentList{Scope: scope, SourceID: sourceID, Items: []KnowledgeDocumentSummary{}}
	if !validRunLabel(sourceID, 256) {
		return out, ErrInvalid
	}
	if _, err := s.knowledgeAuthorization(ctx, token, "read:"+colKnowledgeDocuments, scope, sourceID); err != nil {
		return out, err
	}
	head, err := s.GetKnowledgeSource(ctx, token, sourceID, scope)
	if err != nil {
		return out, err
	}
	ids := slices.Clone(head.Knowledge.DocumentIDs)
	slices.Reverse(ids)
	for _, id := range ids {
		doc, err := s.Repo.Get(ctx, colKnowledgeDocuments, scope, id)
		if err != nil {
			return out, err
		}
		if doc.Knowledge == nil || doc.Knowledge.KnowledgeSourceID != head.ID {
			return out, ErrConflict
		}
		if knowledgeLive(ctx, s.Repo, doc) != nil {
			continue
		}
		k := doc.Knowledge
		item := KnowledgeDocumentSummary{ID: doc.ID, Revision: doc.Revision, Status: doc.Status, Current: doc.ID == head.Knowledge.DocumentID, Filename: k.OriginalFilename, MIMEType: k.MIMEType, ContentHash: doc.ContentHash, SourceVersion: k.SourceVersion, ParserVersion: k.ParserVersion, ChunkerVersion: k.ChunkerVersion, ExtractionStatus: k.ExtractionStatus, ErrorCode: k.ErrorCode, ChunkCount: len(k.ChunkIDs), Sections: []string{}, CreatedAt: doc.CreatedAt}
		if item.Current {
			for _, cid := range k.ChunkIDs {
				chunk, err := s.Repo.Get(ctx, colKnowledgeChunks, scope, cid)
				if err != nil {
					return out, err
				}
				if chunk.Knowledge == nil || chunk.Knowledge.KnowledgeSourceID != head.ID || chunk.Knowledge.DocumentID != doc.ID {
					return out, ErrConflict
				}
				if knowledgeLive(ctx, s.Repo, chunk) != nil {
					continue
				}
				if chunk.Knowledge.IndexStatus == "indexed" {
					item.IndexedChunks++
				}
				if path := chunk.Knowledge.SectionPath; !slices.Contains(item.Sections, path) {
					item.Sections = append(item.Sections, path)
				}
			}
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// KnowledgeSearchInput is RetrieveKnowledge without binding IDs: the active
// bindings of the profile are resolved here. An omitted profile is the default
// scope every agent shares; omitted profile version and skill fields take the
// profile's v1 defaults.
type KnowledgeSearchInput struct {
	Scope          Scope  `json:"scope"`
	ProfileID      string `json:"specialist_profile_id"`
	ProfileVersion string `json:"specialist_profile_version_id,omitempty"`
	SkillID        string `json:"skill_id,omitempty"`
	SkillVersion   string `json:"skill_version,omitempty"`
	Query          string `json:"query"`
	MaxTokens      int    `json:"max_tokens,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	Mode           string `json:"retrieval_mode,omitempty"`
	Fallback       string `json:"retrieval_fallback,omitempty"`
}

type KnowledgeSearchResult struct {
	Authority         string            `json:"authority"`
	BindingIDs        []string          `json:"binding_ids"`
	BindingsTruncated bool              `json:"bindings_truncated"`
	Reason            string            `json:"reason,omitempty"`
	Receipt           *KnowledgeReceipt `json:"receipt,omitempty"`
}

func (s *Service) SearchKnowledge(ctx context.Context, token string, in KnowledgeSearchInput) (KnowledgeSearchResult, error) {
	out := KnowledgeSearchResult{Authority: "data_only_untrusted", BindingIDs: []string{}}
	if in.ProfileID == "" {
		in.ProfileID = knowledgeDefaultProfile
	}
	if in.ProfileVersion == "" {
		in.ProfileVersion = "v1"
	}
	if in.SkillID == "" {
		in.SkillID = in.ProfileID + ".v1"
	}
	if in.SkillVersion == "" {
		in.SkillVersion = "v1"
	}
	if !s.SyntheticHandoff || !validKnowledgeProfile(in.ProfileID, in.ProfileVersion, in.SkillID, in.SkillVersion) {
		return out, ErrDenied
	}
	if _, err := s.knowledgeAuthorization(ctx, token, "retrieve:knowledge", in.Scope, ""); err != nil {
		return out, err
	}
	bindings, err := s.liveKnowledgeBindings(ctx, in.Scope)
	if err != nil {
		return out, err
	}
	for _, b := range bindings {
		k := b.Knowledge
		if b.Status != "active" || k.LifecycleStatus != "active_synthetic" || k.SpecialistProfileID != in.ProfileID || k.SpecialistProfileVersionID != in.ProfileVersion || k.SkillID != in.SkillID {
			continue
		}
		if len(out.BindingIDs) == knowledgeSearchBindings {
			out.BindingsTruncated = true
			break
		}
		out.BindingIDs = append(out.BindingIDs, b.ID)
	}
	if len(out.BindingIDs) == 0 {
		out.Reason = "no_active_binding"
		return out, nil
	}
	receipt, err := s.RetrieveKnowledge(ctx, token, KnowledgeRetrieveInput{Scope: in.Scope, ProfileID: in.ProfileID, ProfileVersion: in.ProfileVersion, SkillID: in.SkillID, SkillVersion: in.SkillVersion, BindingIDs: out.BindingIDs, Query: in.Query, MaxTokens: in.MaxTokens, Limit: in.Limit, Mode: in.Mode, Fallback: in.Fallback})
	if err != nil {
		return out, err
	}
	out.Receipt = &receipt
	return out, nil
}

// parseKnowledgeListQuery reads the scope plus at most one extra key.
func parseKnowledgeListQuery(raw, extra string) (Scope, string, error) {
	q, err := url.ParseQuery(raw)
	if err != nil {
		return Scope{}, "", ErrInvalid
	}
	value := ""
	for key, values := range q {
		if len(values) != 1 {
			return Scope{}, "", ErrInvalid
		}
		switch key {
		case "workspace_id", "project_id", "user_id", "agent_id":
		case extra:
			value = values[0]
		default:
			return Scope{}, "", ErrInvalid
		}
	}
	scope := Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")}
	if !validScope(scope) {
		return Scope{}, "", ErrInvalid
	}
	return scope, value, nil
}

func (s *Service) registerKnowledgeCatalogHTTP(mux routeRegistrar) {
	mux.HandleFunc("GET /v1/knowledge/sources", func(w http.ResponseWriter, r *http.Request) {
		scope, raw, err := parseKnowledgeListQuery(r.URL.RawQuery, "limit")
		limit := 0
		if err == nil && raw != "" {
			if limit, err = strconv.Atoi(raw); err != nil || limit < 1 {
				err = ErrInvalid
			}
		}
		if err != nil {
			respond(w, nil, ErrInvalid)
			return
		}
		out, err := s.ListKnowledgeSources(r.Context(), bearer(r), scope, limit)
		respond(w, out, err)
	})
	mux.HandleFunc("GET /v1/knowledge/documents", func(w http.ResponseWriter, r *http.Request) {
		scope, source, err := parseKnowledgeListQuery(r.URL.RawQuery, "source")
		if err != nil || source == "" {
			respond(w, nil, ErrInvalid)
			return
		}
		out, err := s.ListKnowledgeDocuments(r.Context(), bearer(r), scope, source)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/knowledge/search", func(w http.ResponseWriter, r *http.Request) {
		var in KnowledgeSearchInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.SearchKnowledge(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
}

func knowledgeCatalogSummary(r Record, bindings map[string][]KnowledgeBindingSummary) KnowledgeSourceSummary {
	k := r.Knowledge
	item := KnowledgeSourceSummary{ID: r.ID, Revision: r.Revision, Status: r.Status, LifecycleStatus: k.LifecycleStatus, TrustLevel: k.TrustLevel, DisplayName: k.DisplayName, SourceType: k.SourceType, Format: k.SourceMetadata["format"], Synthetic: k.SourceMetadata["fixture"] == "synthetic", SourceVersion: k.SourceVersion, DocumentID: k.DocumentID, DocumentCount: len(k.DocumentIDs), ChunkCount: len(k.ChunkIDs), ApprovedBy: r.ApprovedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Bindings: bindings[r.ID]}
	if item.Bindings == nil {
		item.Bindings = []KnowledgeBindingSummary{}
	}
	return item
}
