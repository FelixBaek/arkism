package memory

// Synthetic file knowledge uses the existing scoped canonical records and private
// artifact store. The source anchor is stable; documents and chunks are immutable.
import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	colKnowledgeSources     = "knowledge_sources"
	colKnowledgeDocuments   = "knowledge_documents"
	colKnowledgeChunks      = "knowledge_chunks"
	colKnowledgeBindings    = "knowledge_bindings"
	colRetrievalLogs        = "retrieval_logs"
	knowledgeParserVersion  = "utf8-text-json-v1"
	knowledgeChunkerVersion = "md-sections-512-o64-v2"
)

var knowledgeCollections = []string{colKnowledgeSources, colKnowledgeDocuments, colKnowledgeChunks, colKnowledgeBindings, colRetrievalLogs}

func isKnowledgeCollection(col string) bool {
	for _, c := range knowledgeCollections {
		if c == col {
			return true
		}
	}
	return false
}

// Record supplies id, scope, timestamps, status, revision, content hash, provenance
// and protected text. Only fields relevant to the collection are populated.
type KnowledgeRecord struct {
	DocumentIDs                []string          `json:"document_ids,omitempty" bson:"document_ids,omitempty"`
	LifecycleStatus            string            `json:"lifecycle_status,omitempty" bson:"lifecycle_status,omitempty"`
	SourceMetadata             map[string]string `json:"source_metadata,omitempty" bson:"source_metadata,omitempty"`
	Title                      string            `json:"title,omitempty" bson:"title,omitempty"`
	DeletedAt                  *time.Time        `json:"deleted_at" bson:"deleted_at,omitempty"`
	SourceUpdatedAt            *time.Time        `json:"source_updated_at" bson:"source_updated_at,omitempty"`
	KnowledgeSourceID          string            `json:"knowledge_source_id,omitempty" bson:"knowledge_source_id,omitempty"`
	SourceType                 string            `json:"source_type,omitempty" bson:"source_type,omitempty"`
	OwnerUserID                string            `json:"owner_user_id,omitempty" bson:"owner_user_id,omitempty"`
	DisplayName                string            `json:"display_name,omitempty" bson:"display_name,omitempty"`
	TrustLevel                 string            `json:"trust_level,omitempty" bson:"trust_level,omitempty"`
	IngestionMode              string            `json:"ingestion_mode,omitempty" bson:"ingestion_mode,omitempty"`
	SourceVersion              string            `json:"source_version,omitempty" bson:"source_version,omitempty"`
	DocumentID                 string            `json:"document_id,omitempty" bson:"document_id,omitempty"`
	ChunkIDs                   []string          `json:"chunk_ids,omitempty" bson:"chunk_ids,omitempty"`
	OriginalFilename           string            `json:"original_filename,omitempty" bson:"original_filename,omitempty"`
	MIMEType                   string            `json:"mime_type,omitempty" bson:"mime_type,omitempty"`
	StorageRef                 string            `json:"storage_ref,omitempty" bson:"storage_ref,omitempty"`
	ParserVersion              string            `json:"parser_version,omitempty" bson:"parser_version,omitempty"`
	ChunkerVersion             string            `json:"chunker_version,omitempty" bson:"chunker_version,omitempty"`
	ExtractionStatus           string            `json:"extraction_status,omitempty" bson:"extraction_status,omitempty"`
	ErrorCode                  string            `json:"error_code,omitempty" bson:"error_code,omitempty"`
	ChunkIndex                 int               `json:"chunk_index" bson:"chunk_index"`
	SectionPath                string            `json:"section_path,omitempty" bson:"section_path,omitempty"`
	TokenEstimate              int               `json:"token_estimate,omitempty" bson:"token_estimate,omitempty"`
	IndexRef                   string            `json:"index_ref,omitempty" bson:"index_ref,omitempty"`
	IndexStatus                string            `json:"index_status,omitempty" bson:"index_status,omitempty"`
	IndexError                 string            `json:"index_error,omitempty" bson:"index_error,omitempty"`
	SpecialistProfileID        string            `json:"specialist_profile_id,omitempty" bson:"specialist_profile_id,omitempty"`
	SpecialistProfileVersionID string            `json:"specialist_profile_version_id,omitempty" bson:"specialist_profile_version_id,omitempty"`
	SkillID                    string            `json:"skill_id,omitempty" bson:"skill_id,omitempty"`
	TrustRequirement           string            `json:"trust_requirement,omitempty" bson:"trust_requirement,omitempty"`
	AccessMode                 string            `json:"access_mode,omitempty" bson:"access_mode,omitempty"`
	ValidFrom                  *time.Time        `json:"valid_from,omitempty" bson:"valid_from,omitempty"`
	ValidTo                    *time.Time        `json:"valid_to,omitempty" bson:"valid_to,omitempty"`
}
type KnowledgeIngestInput struct {
	Scope            Scope  `json:"scope"`
	SourceKey        string `json:"source_key"`
	Filename         string `json:"filename"`
	MIMEType         string `json:"mime_type"`
	Content          string `json:"content"`
	Synthetic        bool   `json:"synthetic"`
	ExpectedRevision int64  `json:"expected_revision,omitempty"`
}
type KnowledgeIngestResult struct {
	Source    Record   `json:"source"`
	Document  Record   `json:"document"`
	Chunks    []Record `json:"chunks"`
	Duplicate bool     `json:"duplicate"`
}
type KnowledgeStateInput struct {
	Scope            Scope  `json:"scope"`
	ExpectedRevision int64  `json:"expected_revision"`
	Status           string `json:"status"`
	TrustLevel       string `json:"trust_level,omitempty"`
}
type KnowledgeBindingInput struct {
	Scope                      Scope      `json:"scope"`
	BindingKey                 string     `json:"binding_key"`
	KnowledgeSourceID          string     `json:"knowledge_source_id"`
	SpecialistProfileID        string     `json:"specialist_profile_id"`
	SpecialistProfileVersionID string     `json:"specialist_profile_version_id"`
	SkillID                    string     `json:"skill_id,omitempty"`
	TrustRequirement           string     `json:"trust_requirement"`
	AccessMode                 string     `json:"access_mode"`
	ValidFrom                  *time.Time `json:"valid_from,omitempty"`
	ValidTo                    *time.Time `json:"valid_to,omitempty"`
}

func (s *Service) knowledgeAuthorization(ctx context.Context, token, action string, scope Scope, id string) (Decision, error) {
	if !s.SyntheticHandoff || !validScope(scope) || scope.UserID == "" || scope.AgentID == "" {
		return Decision{}, ErrDenied
	}
	return s.authorize(ctx, token, action, scope, id, "")
}
func knowledgeRecord(id string, scope Scope, actor, kind string, sources []SourceRef) Record {
	r := consultationRecord(id, scope, actor)
	r.Kind = kind
	r.OriginProduct = "synthetic-knowledge"
	r.Sensitivity = "private"
	r.Sources = sources
	if len(sources) == 1 {
		r.SourceEventID = sources[0].SourceID
	}
	r.CreatedByType = "human"
	r.Knowledge = &KnowledgeRecord{}
	return r
}
func knowledgeLive(ctx context.Context, tx Tx, r Record) error {
	if r.Knowledge == nil || r.PrivacyQuarantined || r.ContentErased || r.Status == "deleted" || r.Status == "quarantined" {
		return ErrNotFound
	}
	return checkSources(ctx, tx, r)
}
func (s *Service) IngestKnowledge(ctx context.Context, token string, in KnowledgeIngestInput) (KnowledgeIngestResult, error) {
	var out KnowledgeIngestResult
	d, e := s.knowledgeAuthorization(ctx, token, "write:"+colKnowledgeSources, in.Scope, "")
	if e != nil {
		return out, e
	}
	if d.Role != "human" {
		return out, ErrDenied
	}
	ext := strings.ToLower(path.Ext(in.Filename))
	mime := map[string]string{".txt": "text/plain", ".md": "text/markdown", ".json": "application/json"}[ext]
	if !validRunLabel(in.SourceKey, 128) || !validRunLabel(in.Filename, 200) || strings.ContainsAny(in.Filename, "/\\") || path.Base(in.Filename) != in.Filename || mime == "" || in.MIMEType != mime || len(in.Content) == 0 || len(in.Content) > 64<<10 || !utf8.ValidString(in.Content) || strings.ContainsRune(in.Content, 0) || in.ExpectedRevision < 0 {
		return out, ErrInvalid
	}
	if containsSecret(in.Content) || containsSecret(in.Filename) || containsSecret(in.SourceKey) {
		return out, ErrSecretDetected
	}
	for _, action := range []string{"write:sources", "write:artifacts", "write:" + colKnowledgeDocuments, "write:" + colKnowledgeChunks} {
		a, err := s.authorize(ctx, token, action, in.Scope, "", in.Content)
		if err != nil {
			return out, err
		}
		if a.PrincipalID != d.PrincipalID || a.Role != "human" || !a.ContentSafe {
			return out, ErrDenied
		}
	}
	hash := ContentHash(in.Content)
	id := "knowledge-" + ContentHash(structuredText(in.Scope)+in.SourceKey)
	anchorID := "knowledge-source-" + ContentHash(id)
	version := ContentHash(in.MIMEType + ":" + hash + ":" + knowledgeParserVersion + ":" + knowledgeChunkerVersion)
	documentID := "knowledge-document-" + ContentHash(id+version)
	normalized := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(in.Content, "\r\n", "\n"), "\r", "\n"))
	failed := false
	if ext == ".json" {
		var v any
		decoder := json.NewDecoder(strings.NewReader(in.Content))
		decoder.UseNumber()
		if !json.Valid([]byte(in.Content)) || decoder.Decode(&v) != nil {
			failed = true
		} else {
			b, _ := json.Marshal(v)
			normalized = string(b)
		}
	}
	if normalized == "" {
		failed = true
	}
	e = s.withHandoffArtifactWriter(ctx, func(write handoffArtifactWriter) error {
		return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			old, err := tx.Get(ctx, colKnowledgeSources, in.Scope, id)
			exists := err == nil
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			if exists {
				if err = knowledgeLive(ctx, tx, old); err != nil {
					return err
				}
				if old.Knowledge.SourceVersion == version {
					for _, ref := range old.Sources {
						if err = tx.FenceSource(ctx, in.Scope, ref.SourceID); err != nil {
							return err
						}
					}
					doc, err := tx.Get(ctx, colKnowledgeDocuments, in.Scope, documentID)
					if err != nil {
						return err
					}
					out = KnowledgeIngestResult{Source: old, Document: doc, Chunks: []Record{}, Duplicate: true}
					for _, cid := range doc.Knowledge.ChunkIDs {
						c, err := tx.Get(ctx, colKnowledgeChunks, in.Scope, cid)
						if err != nil {
							return err
						}
						out.Chunks = append(out.Chunks, c)
					}
					return nil
				}
				if in.ExpectedRevision != old.Revision {
					return ErrConflict
				}
			}
			if !exists && in.ExpectedRevision != 0 {
				return ErrConflict
			}
			anchor, err := tx.Get(ctx, "sources", in.Scope, anchorID)
			if !exists {
				if err == nil {
					return ErrConflict
				}
				if !errors.Is(err, ErrNotFound) {
					return err
				}
				anchor = knowledgeRecord(anchorID, in.Scope, d.PrincipalID, "document", nil)
				anchor.Knowledge = nil
				anchor.Text, anchor.SourceRef = "Knowledge file anchor", "knowledge/"+id
				if in.Synthetic {
					anchor.Text, anchor.SourceRef = "Synthetic knowledge file anchor", "synthetic/knowledge/"+id
				}
				anchor.ContentHash = ContentHash(anchor.Text)
				anchor.CollectedAt = &anchor.CreatedAt
				anchor.Sources = []SourceRef{{SourceID: anchor.ID, Locator: "original"}}
				if err = tx.Insert(ctx, "sources", anchor); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			if !live(anchor) {
				return ErrNotFound
			}
			if err = tx.FenceSource(ctx, in.Scope, anchor.ID); err != nil {
				return err
			}
			refs := []SourceRef{{SourceID: anchor.ID, Locator: "knowledge-original"}}
			doc, err := tx.Get(ctx, colKnowledgeDocuments, in.Scope, documentID)
			chunks := []Record{}
			if errors.Is(err, ErrNotFound) {
				if exists && len(old.Knowledge.DocumentIDs) >= 32 {
					return ErrInvalid
				}
				artifact, err := write(ctx, tx, in.Scope, Ref{Collection: "sources", ID: anchor.ID, Revision: anchor.Revision}, in.Content, d)
				if err != nil {
					return err
				}
				doc = knowledgeRecord(documentID, in.Scope, d.PrincipalID, "knowledge_document", refs)
				doc.ContentHash = hash
				doc.Knowledge = &KnowledgeRecord{Title: in.Filename, KnowledgeSourceID: id, SourceVersion: version, OriginalFilename: in.Filename, MIMEType: mime, StorageRef: artifact.ID, ParserVersion: knowledgeParserVersion, ChunkerVersion: knowledgeChunkerVersion, ExtractionStatus: "completed", ChunkIDs: []string{}}
				if failed {
					doc.Status = "failed"
					doc.Knowledge.ExtractionStatus = "failed"
					doc.Knowledge.ErrorCode = "extraction_failed"
				} else {
					for index, piece := range knowledgeChunkText(normalized, ext) {
						text := piece.Text
						cid := "knowledge-chunk-" + ContentHash(documentID+":"+structuredText(index)+":"+text)
						c := knowledgeRecord(cid, in.Scope, d.PrincipalID, "knowledge_chunk", refs)
						c.Text = text
						c.ContentHash = ContentHash(text)
						c.Relations = []Ref{{Collection: colKnowledgeDocuments, ID: documentID, Revision: 1}}
						c.Knowledge = &KnowledgeRecord{KnowledgeSourceID: id, DocumentID: documentID, SourceVersion: version, ChunkIndex: index, SectionPath: piece.SectionPath, TokenEstimate: (len([]rune(text)) + 3) / 4, ChunkerVersion: knowledgeChunkerVersion, IndexRef: knowledgeFTSAdapter + ":" + c.ContentHash}
						chunks = append(chunks, c)
						doc.Knowledge.ChunkIDs = append(doc.Knowledge.ChunkIDs, cid)
					}
				}
			} else if err != nil {
				return err
			} else {
				for _, cid := range doc.Knowledge.ChunkIDs {
					c, err := tx.Get(ctx, colKnowledgeChunks, in.Scope, cid)
					if err != nil {
						return err
					}
					chunks = append(chunks, c)
				}
				out = KnowledgeIngestResult{Source: old, Document: doc, Chunks: chunks, Duplicate: true}
				return nil
			}
			head := knowledgeRecord(id, in.Scope, d.PrincipalID, "knowledge_source", refs)
			head.Status = "draft"
			head.ContentHash = hash
			head.SourceEventID = anchor.ID
			head.Knowledge = &KnowledgeRecord{DocumentIDs: append(append([]string{}, old.KnowledgeDocumentIDs()...), documentID), SourceMetadata: knowledgeSourceMetadata(ext, in.Synthetic), LifecycleStatus: "draft", SourceType: "file", OwnerUserID: in.Scope.UserID, DisplayName: in.Filename, TrustLevel: "user_supplied", IngestionMode: "one_time", SourceVersion: version, DocumentID: documentID, ChunkIDs: doc.Knowledge.ChunkIDs}
			if failed {
				head.Status = "failed"
				head.Knowledge.LifecycleStatus = "failed"
			}
			if exists {
				head.CreatedAt = old.CreatedAt
				head.CreatedBy = old.CreatedBy
				head.CreatedByType = old.CreatedByType
				head.Revision = old.Revision + 1
				if err = tx.Replace(ctx, colKnowledgeSources, head, old.Revision); err != nil {
					return err
				}
			} else {
				if err = tx.Insert(ctx, colKnowledgeSources, head); err != nil {
					return err
				}
			}
			doc.Relations = []Ref{{Collection: colKnowledgeSources, ID: head.ID, Revision: head.Revision}}
			if err = tx.Insert(ctx, colKnowledgeDocuments, doc); err != nil {
				return err
			}
			for _, chunk := range chunks {
				if err = tx.Insert(ctx, colKnowledgeChunks, chunk); err != nil {
					return err
				}
			}
			out = KnowledgeIngestResult{Source: head, Document: doc, Chunks: chunks}
			action := "knowledge:ingested"
			if failed {
				action = "knowledge:extraction_failed"
			}
			return audit(ctx, tx, in.Scope, d.PrincipalID, action, id)
		})
	})
	return out, e
}
func (s *Service) GetKnowledgeSource(ctx context.Context, token, id string, scope Scope) (Record, error) {
	_, e := s.knowledgeAuthorization(ctx, token, "read:"+colKnowledgeSources, scope, id)
	if e != nil {
		return Record{}, e
	}
	r, e := s.Repo.Get(ctx, colKnowledgeSources, scope, id)
	if e == nil {
		e = knowledgeLive(ctx, s.Repo, r)
	}
	return r, e
}
func (s *Service) SetKnowledgeState(ctx context.Context, token, col, id string, in KnowledgeStateInput) (Record, error) {
	var out Record
	if col != colKnowledgeSources && col != colKnowledgeBindings {
		return out, ErrInvalid
	}
	d, e := s.knowledgeAuthorization(ctx, token, "write:"+col, in.Scope, id)
	if e != nil {
		return out, e
	}
	if d.Role != "human" {
		return out, ErrDenied
	}
	if col == colKnowledgeSources && (in.Status != "active" || in.TrustLevel != "reviewed") || col == colKnowledgeBindings && (in.TrustLevel != "" || in.Status != "active_synthetic" && in.Status != "suspended") {
		return out, ErrInvalid
	}
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, col, in.Scope, id)
		if err != nil {
			return err
		}
		if err = knowledgeLive(ctx, tx, r); err != nil {
			return err
		}
		if r.Revision != in.ExpectedRevision {
			return ErrConflict
		}
		copyKnowledge := *r.Knowledge
		r.Knowledge = &copyKnowledge
		if col == colKnowledgeSources {
			if r.Status != "draft" && r.Status != "active" {
				return ErrDenied
			}
			r.Knowledge.TrustLevel = "reviewed"
		} else {
			source, err := tx.Get(ctx, colKnowledgeSources, in.Scope, r.Knowledge.KnowledgeSourceID)
			if err != nil {
				return err
			}
			if err = knowledgeLive(ctx, tx, source); err != nil {
				return err
			}
			if in.Status == "active_synthetic" && (source.Status != "active" || source.Knowledge.TrustLevel != "reviewed") {
				return ErrDenied
			}
		}
		for _, ref := range r.Sources {
			if err = tx.FenceSource(ctx, in.Scope, ref.SourceID); err != nil {
				return err
			}
		}
		old := r.Revision
		r.Revision++
		r.Status = in.Status
		r.Knowledge.LifecycleStatus = in.Status
		if in.Status == "active_synthetic" {
			r.Status = "active"
		}
		r.ApprovedBy = d.PrincipalID
		r.UpdatedAt = time.Now().UTC()
		if err = tx.Replace(ctx, col, r, old); err != nil {
			return err
		}
		out = r
		return audit(ctx, tx, in.Scope, d.PrincipalID, "knowledge:state", id)
	})
	return out, e
}
func (s *Service) CreateKnowledgeBinding(ctx context.Context, token string, in KnowledgeBindingInput) (Record, error) {
	var out Record
	d, e := s.knowledgeAuthorization(ctx, token, "write:"+colKnowledgeBindings, in.Scope, "")
	if e != nil {
		return out, e
	}
	if d.Role != "human" {
		return out, ErrDenied
	}
	if !validRunLabel(in.BindingKey, 128) || !validRunLabel(in.KnowledgeSourceID, 256) || !validRunLabel(in.SpecialistProfileID, 128) || !validRunLabel(in.SpecialistProfileVersionID, 128) || in.SkillID != "" && !validRunLabel(in.SkillID, 128) || in.TrustRequirement != "reviewed" || in.AccessMode != "synthetic_retrieval" && in.AccessMode != "cite_required" && in.AccessMode != "future_read_only_rag" || in.ValidFrom != nil && in.ValidTo != nil && !in.ValidTo.After(*in.ValidFrom) {
		return out, ErrInvalid
	}
	id := "knowledge-binding-" + ContentHash(structuredText(in.Scope)+in.BindingKey)
	requestHash := ContentHash(structuredText(in))
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		source, err := tx.Get(ctx, colKnowledgeSources, in.Scope, in.KnowledgeSourceID)
		if err != nil {
			return err
		}
		if err = knowledgeLive(ctx, tx, source); err != nil {
			return err
		}
		for _, ref := range source.Sources {
			if err = tx.FenceSource(ctx, in.Scope, ref.SourceID); err != nil {
				return err
			}
		}
		old, err := tx.Get(ctx, colKnowledgeBindings, in.Scope, id)
		if err == nil {
			if err = knowledgeLive(ctx, tx, old); err != nil {
				return err
			}
			if old.RequestHash != requestHash {
				return ErrConflict
			}
			out = old
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		r := knowledgeRecord(id, in.Scope, d.PrincipalID, "knowledge_binding", source.Sources)
		r.Status = "candidate"
		r.RequestHash = requestHash
		r.SourceEventID = source.SourceEventID
		r.Relations = []Ref{{Collection: colKnowledgeSources, ID: source.ID, Revision: source.Revision}}
		r.Knowledge = &KnowledgeRecord{LifecycleStatus: "candidate", KnowledgeSourceID: source.ID, SpecialistProfileID: in.SpecialistProfileID, SpecialistProfileVersionID: in.SpecialistProfileVersionID, SkillID: in.SkillID, TrustRequirement: in.TrustRequirement, AccessMode: in.AccessMode, ValidFrom: in.ValidFrom, ValidTo: in.ValidTo}
		if err = tx.Insert(ctx, colKnowledgeBindings, r); err != nil {
			return err
		}
		out = r
		return audit(ctx, tx, in.Scope, d.PrincipalID, "knowledge:binding", id)
	})
	return out, e
}
func (s *Service) registerKnowledgeHTTP(mux *http.ServeMux) {
	s.registerKnowledgeSemanticHTTP(mux)
	s.registerKnowledgeCatalogHTTP(mux)
	mux.HandleFunc("POST /v1/knowledge/ingest", func(w http.ResponseWriter, r *http.Request) {
		var in KnowledgeIngestInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.IngestKnowledge(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/knowledge/sources/{id}", func(w http.ResponseWriter, r *http.Request) {
		scope, e := parseSourceTraceQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.GetKnowledgeSource(r.Context(), bearer(r), r.PathValue("id"), scope)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/knowledge/bindings", func(w http.ResponseWriter, r *http.Request) {
		var in KnowledgeBindingInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.CreateKnowledgeBinding(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	for route, col := range map[string]string{"POST /v1/knowledge/sources/{id}/review": colKnowledgeSources, "POST /v1/knowledge/bindings/{id}/state": colKnowledgeBindings} {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			var in KnowledgeStateInput
			if !decode(w, r, &in) {
				return
			}
			out, e := s.SetKnowledgeState(r.Context(), bearer(r), col, r.PathValue("id"), in)
			respond(w, out, e)
		})
	}
}

func (r Record) KnowledgeDocumentIDs() []string {
	if r.Knowledge == nil {
		return nil
	}
	return r.Knowledge.DocumentIDs
}

func knowledgeSourceMetadata(ext string, synthetic bool) map[string]string {
	out := map[string]string{"format": ext}
	if synthetic {
		out["fixture"] = "synthetic"
	}
	return out
}
