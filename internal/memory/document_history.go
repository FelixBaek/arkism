package memory

// Document views: the chunks a document was split into (with the review
// candidates each chunk produced) and the document's upload history. They show
// hashes, locators and states; extraction chunk text never leaves the server.
import (
	"context"
	"errors"
	"strconv"
	"time"
)

const (
	documentPageDefault = 50
	documentPageMax     = 200
)

// DocumentCandidate is one candidate a chunk produced; an approved candidate
// keeps its id as a memory.
type DocumentCandidate struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	MemoryID string `json:"memory_id,omitempty"`
}

type DocumentChunk struct {
	Locator    string              `json:"locator"`
	Hash       string              `json:"hash"`
	Status     string              `json:"status"` // unchanged | extracted | pending
	Source     Ref                 `json:"source"`
	Candidates []DocumentCandidate `json:"candidates"`
}

type DocumentChunkPage struct {
	DocumentID string          `json:"document_id"`
	JobID      string          `json:"job_id"`
	Items      []DocumentChunk `json:"items"`
	Total      int             `json:"total"`
	NextCursor string          `json:"next_cursor"`
}

type DocumentUpload struct {
	JobID             string           `json:"job_id"`
	Status            string           `json:"status"`
	Label             string           `json:"label"`
	Delta             FileExtractDelta `json:"delta"`
	CandidatesCreated int              `json:"candidates_created"`
	RequestedBy       string           `json:"requested_by"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
}

type DocumentHistoryPage struct {
	DocumentID string           `json:"document_id"`
	Items      []DocumentUpload `json:"items"`
	NextCursor string           `json:"next_cursor"`
}

// KnowledgeChunkView is a knowledge chunk without its text when the text is restricted.
type KnowledgeChunkView struct {
	ID            string `json:"id"`
	ChunkIndex    int    `json:"chunk_index"`
	SectionPath   string `json:"section_path,omitempty"`
	Hash          string `json:"hash"`
	TokenEstimate int    `json:"token_estimate"`
	Status        string `json:"status"`
	Text          string `json:"text,omitempty"`
	TextWithheld  bool   `json:"text_withheld,omitempty"`
}

type KnowledgeChunkPage struct {
	SourceID string               `json:"source_id"`
	Document string               `json:"document_id"`
	Items    []KnowledgeChunkView `json:"items"`
	Total    int                  `json:"total"`
	// ExtractDocumentID names the caller's memory-extraction document of this source, if any.
	ExtractDocumentID string `json:"extract_document_id,omitempty"`
	NextCursor        string `json:"next_cursor"`
}

type KnowledgeHistoryPage struct {
	SourceID   string                     `json:"source_id"`
	Items      []KnowledgeDocumentSummary `json:"items"`
	Total      int                        `json:"total"`
	NextCursor string                     `json:"next_cursor"`
}

// DocumentPage is the cursor and limit of a document list request.
type DocumentPage struct {
	Cursor string
	Limit  int
	Tag    string // the document list only
}

// parseDocumentQuery reads the scope plus cursor and limit (1..200, default 50).
func parseDocumentQuery(raw string) (Scope, DocumentPage, error) {
	scope, q, err := parseWikiQuery(raw, "cursor", "limit", "tag")
	if err != nil {
		return Scope{}, DocumentPage{}, err
	}
	page := DocumentPage{Cursor: q.Get("cursor"), Limit: documentPageDefault, Tag: q.Get("tag")}
	if v := q.Get("limit"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 || n > documentPageMax {
			return Scope{}, DocumentPage{}, ErrInvalid
		}
		page.Limit = n
	}
	if len(page.Cursor) > 256 || page.Tag != "" && !validTagEdit([]string{page.Tag}) {
		return Scope{}, DocumentPage{}, ErrInvalid
	}
	return scope, page, nil
}

// pageOffset is the window of a document page over n items (parseOffsetCursor, offsetWindow).
func pageOffset(page DocumentPage, n int) (int, int, string, error) {
	offset, ok := parseOffsetCursor(page.Cursor, n)
	if !ok {
		return 0, 0, "", ErrInvalid
	}
	start, end, next := offsetWindow(offset, page.Limit, n)
	return start, end, next, nil
}

func (s *Service) extractDocument(ctx context.Context, token string, scope Scope, docID string) (Record, error) {
	if !validRunLabel(docID, 256) {
		return Record{}, ErrInvalid
	}
	if _, err := s.wikiAuthorize(ctx, token, "read:memory_candidates", scope, docID, ""); err != nil {
		return Record{}, err
	}
	doc, err := s.Repo.Get(ctx, "jobs", scope, docID)
	if err == nil && (doc.Kind != fileExtractDocKind || doc.Target == nil) {
		err = ErrNotFound
	}
	return doc, err
}

// DocumentChunks lists the chunks of a document's last upload with their candidates.
func (s *Service) DocumentChunks(ctx context.Context, token string, scope Scope, docID string, page DocumentPage) (DocumentChunkPage, error) {
	doc, err := s.extractDocument(ctx, token, scope, docID)
	if err != nil {
		return DocumentChunkPage{}, err
	}
	last, err := s.extractJob(ctx, s.Repo, scope, doc.Target.ID)
	if err != nil {
		return DocumentChunkPage{}, err
	}
	job := last.FileExtract
	rows := make([]DocumentChunk, 0, len(job.Kept)+len(job.Chunks))
	for _, ch := range job.Kept {
		rows = append(rows, DocumentChunk{Locator: ch.Locator, Hash: ch.Hash, Status: "unchanged", Source: ch.Source})
	}
	for i, ch := range job.Chunks {
		status := "pending"
		if i < job.Next {
			status = "extracted"
		}
		rows = append(rows, DocumentChunk{Locator: ch.Locator, Hash: ch.Hash, Status: status, Source: ch.Source})
	}
	start, end, next, err := pageOffset(page, len(rows))
	if err != nil {
		return DocumentChunkPage{}, err
	}
	out := DocumentChunkPage{DocumentID: docID, JobID: last.ID, Items: rows[start:end], Total: len(rows), NextCursor: next}
	chunks := append(append([]FileExtractChunk{}, job.Kept...), job.Chunks...)[start:end]
	for i := range out.Items {
		if out.Items[i].Candidates, err = s.documentCandidates(ctx, scope, chunks[i].Candidates); err != nil {
			return DocumentChunkPage{}, err
		}
	}
	return out, nil
}

// documentCandidates reads each candidate's current state; a deleted one is "gone".
func (s *Service) documentCandidates(ctx context.Context, scope Scope, ids []string) ([]DocumentCandidate, error) {
	out := []DocumentCandidate{}
	for _, id := range ids {
		c := DocumentCandidate{ID: id, Status: "gone"}
		r, err := s.Repo.Get(ctx, "memory_candidates", scope, id)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		if err == nil {
			c.Status = r.Status
			if r.Status == "approved" {
				c.MemoryID = id
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// DocumentHistory walks a document's uploads newest first; the cursor is the
// job id to continue from.
func (s *Service) DocumentHistory(ctx context.Context, token string, scope Scope, docID string, page DocumentPage) (DocumentHistoryPage, error) {
	doc, err := s.extractDocument(ctx, token, scope, docID)
	if err != nil {
		return DocumentHistoryPage{}, err
	}
	id := doc.Target.ID
	if page.Cursor != "" {
		id = page.Cursor
	}
	out := DocumentHistoryPage{DocumentID: docID, Items: []DocumentUpload{}}
	for id != "" && len(out.Items) < page.Limit {
		r, err := s.extractJob(ctx, s.Repo, scope, id)
		if errors.Is(err, ErrNotFound) && len(out.Items) > 0 {
			break // an older job was removed; history ends there
		}
		if err != nil {
			return DocumentHistoryPage{}, err
		}
		if r.FileExtract.Document != docID {
			return DocumentHistoryPage{}, ErrInvalid
		}
		j := r.FileExtract
		out.Items = append(out.Items, DocumentUpload{JobID: r.ID, Status: r.Status, Label: j.Label, Delta: j.Delta, CandidatesCreated: j.CandidatesCreated, RequestedBy: j.RequestedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt})
		id = j.Previous
	}
	out.NextCursor = id
	return out, nil
}

// KnowledgeChunks lists the current document's chunks. Chunk text is shown to
// a person only: an agent reads knowledge through retrieval, which requires a
// reviewed source and a binding. Restricted, quarantined or erased text is
// withheld from everyone.
func (s *Service) KnowledgeChunks(ctx context.Context, token string, scope Scope, id string, page DocumentPage) (KnowledgeChunkPage, error) {
	d, err := s.knowledgeAuthorization(ctx, token, "read:"+colKnowledgeSources, scope, id)
	if err != nil {
		return KnowledgeChunkPage{}, err
	}
	src, err := s.GetKnowledgeSource(ctx, token, id, scope)
	if err != nil {
		return KnowledgeChunkPage{}, err
	}
	doc, err := s.Repo.Get(ctx, colKnowledgeDocuments, scope, src.Knowledge.DocumentID)
	if err == nil && (doc.Knowledge == nil || doc.Knowledge.KnowledgeSourceID != src.ID) {
		err = ErrConflict
	}
	if err == nil {
		err = knowledgeLive(ctx, s.Repo, doc)
	}
	if err != nil {
		return KnowledgeChunkPage{}, err
	}
	ids := doc.Knowledge.ChunkIDs
	start, end, next, err := pageOffset(page, len(ids))
	if err != nil {
		return KnowledgeChunkPage{}, err
	}
	out := KnowledgeChunkPage{SourceID: id, Document: doc.ID, Items: []KnowledgeChunkView{}, Total: len(ids), NextCursor: next}
	chunks, err := knowledgeChunkRecords(ctx, s.Repo, scope, ids[start:end], nil)
	if err != nil {
		return KnowledgeChunkPage{}, err
	}
	for _, c := range chunks {
		if c.Knowledge == nil || c.Knowledge.DocumentID != doc.ID {
			return KnowledgeChunkPage{}, ErrConflict
		}
		v := KnowledgeChunkView{ID: c.ID, ChunkIndex: c.Knowledge.ChunkIndex, SectionPath: c.Knowledge.SectionPath, Hash: c.ContentHash, TokenEstimate: c.Knowledge.TokenEstimate, Status: c.Status}
		if v.TextWithheld = d.Role != roleHuman || c.Sensitivity == "restricted" || knowledgeLive(ctx, s.Repo, c) != nil; !v.TextWithheld {
			v.Text = c.Text
		}
		out.Items = append(out.Items, v)
	}
	if d, e := s.authorize(ctx, token, "read:memory_candidates", scope, "", ""); e == nil && d.Role == roleHuman {
		extractID := extractDocID(scope, d.PrincipalID, "knowledge:"+id)
		if _, e = s.Repo.Get(ctx, "jobs", scope, extractID); e == nil {
			out.ExtractDocumentID = extractID
		}
	}
	return out, nil
}

// KnowledgeHistory pages the source's document versions, newest first, from
// the document list (ListKnowledgeDocuments).
func (s *Service) KnowledgeHistory(ctx context.Context, token string, scope Scope, id string, page DocumentPage) (KnowledgeHistoryPage, error) {
	list, err := s.ListKnowledgeDocuments(ctx, token, scope, id)
	if err != nil {
		return KnowledgeHistoryPage{}, err
	}
	start, end, next, err := pageOffset(page, len(list.Items))
	if err != nil {
		return KnowledgeHistoryPage{}, err
	}
	return KnowledgeHistoryPage{SourceID: id, Items: list.Items[start:end], Total: len(list.Items), NextCursor: next}, nil
}
