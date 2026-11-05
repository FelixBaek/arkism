package memory

// Document management for memory-extraction documents: the list of a scope's
// documents, reviewer tags on a document that reach the candidates and
// memories it produced, and reprocessing a finished document from its stored
// evidence. Tag edits and reprocessing never approve anything: a reprocess
// only creates candidates, and an approved memory changes only by a human's tag edit.
import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
)

// documentTagMax leaves room for the capture tags (validTags allows 32).
const documentTagMax = 16

type ExtractDocument struct {
	ID                string    `json:"id"`
	Label             string    `json:"label"`
	Status            string    `json:"status"`
	JobID             string    `json:"job_id"`
	Tags              []string  `json:"tags"`
	Chunks            int       `json:"chunks"`
	CandidatesCreated int       `json:"candidates_created"`
	RequestedBy       string    `json:"requested_by"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type ExtractDocumentPage struct {
	Items      []ExtractDocument `json:"items"`
	NextCursor string            `json:"next_cursor"`
}

type DocumentTagsInput struct {
	Scope  Scope    `json:"scope"`
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
}

// DocumentTagsResult counts where the edit reached: waiting candidates and
// approved memories; Skipped are ones closed, changed meanwhile or gone.
type DocumentTagsResult struct {
	DocumentID string   `json:"document_id"`
	Tags       []string `json:"tags"`
	Candidates int      `json:"candidates"`
	Memories   int      `json:"memories"`
	Skipped    int      `json:"skipped"`
}

// documentScanMax bounds the documents one text-filtered page reads; past it the
// page ends early with a cursor to keep looking.
const documentScanMax = 1000

// ListExtractDocuments lists the scope's extraction documents, newest first,
// optionally only those with a tag or whose name, tags or chunk text contain
// page.Q. An agent sees only the documents whose last upload it started, as
// with the upload jobs themselves.
func (s *Service) ListExtractDocuments(ctx context.Context, token string, scope Scope, page DocumentPage) (ExtractDocumentPage, error) {
	out := ExtractDocumentPage{Items: []ExtractDocument{}}
	d, err := s.wikiAuthorize(ctx, token, "read:memory_candidates", scope, "", "")
	if err != nil {
		return out, err
	}
	cursor, scanned := page.Cursor, 0
	for {
		rows, next, err := s.opsPage(ctx, OpsListInput{Scope: scope, Kind: fileExtractDocKind, Tag: page.Tag, Cursor: cursor, Limit: page.Limit - len(out.Items)}, "jobs")
		if err != nil {
			return out, err
		}
		if err = s.addExtractDocuments(ctx, d, scope, page.Q, rows, &out); err != nil {
			return out, err
		}
		scanned += len(rows)
		cursor = next
		if page.Q == "" || next == "" || len(out.Items) >= page.Limit || scanned >= documentScanMax {
			break
		}
	}
	out.NextCursor = cursor
	return out, nil
}

// addExtractDocuments appends the rows the caller may see and, with q, that match it.
func (s *Service) addExtractDocuments(ctx context.Context, d Decision, scope Scope, q string, rows []Record, out *ExtractDocumentPage) error {
	var err error
	for _, doc := range rows {
		item := ExtractDocument{ID: doc.ID, Tags: append([]string{}, doc.Tags...), CreatedAt: doc.CreatedAt, UpdatedAt: doc.UpdatedAt}
		var job Record
		if doc.Target != nil {
			job, err = s.Repo.Get(ctx, "jobs", scope, doc.Target.ID)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			if err == nil && job.FileExtract != nil {
				j := job.FileExtract
				item.JobID, item.Status, item.Label, item.Chunks, item.CandidatesCreated, item.RequestedBy = job.ID, job.Status, j.Label, j.Delta.ChunksTotal, j.CandidatesCreated, j.RequestedBy
			}
		}
		if d.Role == roleAgent && (item.JobID == "" || !ownsJob(d, job)) || q != "" && !documentMatches(item, job.FileExtract, q) {
			continue
		}
		out.Items = append(out.Items, item)
	}
	return nil
}

// documentMatches reports whether q is in the document's name, a tag or a chunk's text, ignoring case.
func documentMatches(item ExtractDocument, job *FileExtractJob, q string) bool {
	has := func(v string) bool { return strings.Contains(strings.ToLower(v), strings.ToLower(q)) }
	if has(item.Label) || slices.ContainsFunc(item.Tags, has) {
		return true
	}
	if job == nil {
		return false
	}
	return slices.ContainsFunc(job.Kept, func(c FileExtractChunk) bool { return has(c.Text) }) ||
		slices.ContainsFunc(job.Chunks, func(c FileExtractChunk) bool { return has(c.Text) })
}

// documentJob is the document's last upload job.
func (s *Service) documentJob(ctx context.Context, scope Scope, doc Record) (Record, error) {
	job, err := s.Repo.Get(ctx, "jobs", scope, doc.Target.ID)
	if err == nil && job.FileExtract == nil {
		err = ErrNotFound
	}
	return job, err
}

// EditDocumentTags changes a document's tags and the same tags on what it
// produced: waiting candidates directly, approved memories as one audited
// revision each. Human only.
func (s *Service) EditDocumentTags(ctx context.Context, token, id string, in DocumentTagsInput) (DocumentTagsResult, error) {
	out := DocumentTagsResult{DocumentID: id, Tags: []string{}}
	if !validScope(in.Scope) || len(in.Add)+len(in.Remove) == 0 || !validTagInput(in.Add) || !validTagEdit(in.Remove) {
		return out, ErrInvalid
	}
	d, err := s.opsAuthorize(ctx, token, "transition:memories", in.Scope, "", roleHuman)
	if err != nil {
		return out, err
	}
	doc, err := s.extractDocument(ctx, token, in.Scope, id)
	if err != nil {
		return out, err
	}
	job, err := s.documentJob(ctx, in.Scope, doc)
	if err != nil {
		return out, err
	}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, e := tx.Get(ctx, "jobs", in.Scope, id)
		if e != nil {
			return e
		}
		r.Tags = editTags(r.Tags, in.Add, in.Remove)
		if len(r.Tags) > documentTagMax {
			return ErrInvalid
		}
		out.Tags = r.Tags
		r.Revision++
		r.UpdatedAt = time.Now().UTC()
		if e = tx.Replace(ctx, "jobs", r, r.Revision-1); e != nil {
			return e
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "set_tags:file_extract_document", id)
	})
	if err != nil {
		return out, err
	}
	for _, cid := range documentCandidateIDs(*job.FileExtract) {
		kind, err := s.tagDocumentCandidate(ctx, token, d, in, cid)
		if err != nil {
			return out, err
		}
		switch kind {
		case "candidate":
			out.Candidates++
		case "memory":
			out.Memories++
		default:
			out.Skipped++
		}
	}
	return out, nil
}

func documentCandidateIDs(j FileExtractJob) []string {
	ids := []string{}
	for _, ch := range append(slices.Clone(j.Kept), j.Chunks...) {
		for _, id := range ch.Candidates {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// tagDocumentCandidate edits one produced candidate's tags; it says whether it
// reached a waiting candidate, an approved memory, or was skipped.
func (s *Service) tagDocumentCandidate(ctx context.Context, token string, d Decision, in DocumentTagsInput, id string) (string, error) {
	cand, err := s.Repo.Get(ctx, "memory_candidates", in.Scope, id)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if cand.Status == "approved" {
		changed := false
		_, err = s.editLiveMemory(ctx, token, id, in.Scope, 0, "set_tags:memories", func(r *Record) error {
			e := retag(r, in)
			changed = e == nil
			return e
		})
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || err == nil && !changed {
			return "", nil
		}
		return "memory", err
	}
	if cand.Status != "candidate" {
		return "", nil
	}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, e := tx.Get(ctx, "memory_candidates", in.Scope, id)
		if e != nil {
			return e
		}
		if r.Status != "candidate" || r.ContentErased || r.PrivacyQuarantined {
			return ErrConflict
		}
		if e = retag(&r, in); e != nil {
			return e
		}
		r.Revision++
		r.UpdatedAt = time.Now().UTC()
		if e = tx.Replace(ctx, "memory_candidates", r, r.Revision-1); e != nil {
			return e
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "set_tags:memory_candidates", id)
	})
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, errMemoryUnchanged) {
		return "", nil
	}
	return "candidate", err
}

func retag(r *Record, in DocumentTagsInput) error {
	next := editTags(r.Tags, in.Add, in.Remove)
	if slices.Equal(next, r.Tags) {
		return errMemoryUnchanged
	}
	if !validTags(next) {
		return ErrInvalid
	}
	r.Tags = next
	return nil
}

// ReprocessDocument extracts a finished document again from its stored
// evidence (a knowledge document from its knowledge source). Every chunk is
// read again and the new claims only become candidates; Capture still merges
// a claim already waiting or approved. Only the uploader may run it.
func (s *Service) ReprocessDocument(ctx context.Context, token, id string, in WikiScopeInput) (FileMemoriesResult, error) {
	if !validScope(in.Scope) {
		return FileMemoriesResult{}, ErrInvalid
	}
	d, _, err := s.extractActor(ctx, token, "write:memory_candidates", in.Scope, roleHuman, roleAgent)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	doc, err := s.extractDocument(ctx, token, in.Scope, id)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	job, err := s.documentJob(ctx, in.Scope, doc)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	j := job.FileExtract
	if j.RequestedBy != d.PrincipalID {
		return FileMemoriesResult{}, ErrDenied
	}
	if job.Status != JobStatusDone {
		// A pending or failed job goes on through its run route.
		return FileMemoriesResult{}, ErrConflict
	}
	round := ID()[:12]
	if src, ok := strings.CutPrefix(j.Ref, "knowledge:"); ok {
		return s.extractKnowledge(ctx, token, src, KnowledgeExtractInput{Scope: in.Scope}, round)
	}
	chunks, err := s.storedChunks(ctx, in.Scope, append(slices.Clone(j.Kept), j.Chunks...))
	if err != nil {
		return FileMemoriesResult{}, err
	}
	for i := range chunks {
		chunks[i].Round = round
	}
	fresh, _, delta := diffExtractChunks(nil, chunks)
	// The new round keeps the document's settings and options; its progress starts over.
	plan := FileExtractJob{Label: j.Label, Document: id, Ref: j.Ref, Chunks: fresh, Delta: delta, Settings: j.Settings, Options: j.Options, Zone: j.Zone, Language: j.Language}
	return s.launchExtract(ctx, token, d, in.Scope, plan, FileMemoriesResult{Skipped: []ExtractSkip{}}, fileExtractSyncChunks)
}

// storedChunks reads each chunk's text back from its source, where
// storeExtractSources wrote it after a "[locator]" line; the chunk hash must
// match, so an edited or redacted source is refused rather than guessed.
func (s *Service) storedChunks(ctx context.Context, scope Scope, chunks []FileExtractChunk) ([]FileExtractChunk, error) {
	texts := map[string]string{}
	out := make([]FileExtractChunk, 0, len(chunks))
	for _, ch := range chunks {
		text, ok := texts[ch.Source.ID]
		if !ok {
			src, err := s.Repo.Get(ctx, "sources", scope, ch.Source.ID)
			if errors.Is(err, ErrNotFound) {
				return nil, ErrConflict
			}
			if err != nil {
				return nil, err
			}
			text, texts[ch.Source.ID] = src.Text, src.Text
		}
		piece, ok := sourceChunkText(text, ch)
		if !ok {
			return nil, ErrConflict
		}
		ch.Text, ch.Candidates = piece, nil
		out = append(out, ch)
	}
	if len(out) == 0 {
		return nil, ErrConflict
	}
	return out, nil
}

func sourceChunkText(text string, ch FileExtractChunk) (string, bool) {
	marker := "[" + ch.Locator + "]\n"
	for at := 0; at < len(text); {
		i := strings.Index(text[at:], marker)
		if i < 0 {
			return "", false
		}
		start := at + i
		at = start + len(marker)
		if start > 0 && !strings.HasSuffix(text[:start], "\n\n") {
			continue
		}
		// The piece ends at a later part's "\n\n[" or the end; the hash picks the right end.
		for end := at; ; {
			next := strings.Index(text[end:], "\n\n[")
			stop := len(text)
			if next >= 0 {
				stop = end + next
			}
			if piece := text[at:stop]; ContentHash(extractSpaces(piece)) == ch.Hash {
				return piece, true
			}
			if next < 0 {
				break
			}
			end = stop + 3
		}
	}
	return "", false
}

// DocumentDeleteResult counts what a document delete reached: waiting
// candidates it rejected, approved memories it left as they are, and the
// upload jobs it removed.
type DocumentDeleteResult struct {
	DocumentID string `json:"document_id"`
	Rejected   int    `json:"rejected"`
	Kept       int    `json:"kept"`
	Jobs       int    `json:"jobs"`
}

// documentJobChainMax bounds the upload history a delete walks.
const documentJobChainMax = 1000

// DeleteExtractDocument removes a document and its upload jobs. Its waiting
// candidates are rejected through the normal review path; approved memories
// stay, they only lose the document that listed them. Human only, audited.
// A document whose last upload is still running is refused.
func (s *Service) DeleteExtractDocument(ctx context.Context, token, id string, in WikiScopeInput) (DocumentDeleteResult, error) {
	out := DocumentDeleteResult{DocumentID: id}
	d, err := s.opsAuthorize(ctx, token, "delete:jobs", in.Scope, id, roleHuman)
	if err != nil {
		return out, err
	}
	doc, err := s.extractDocument(ctx, token, in.Scope, id)
	if err != nil {
		return out, err
	}
	job, err := s.documentJob(ctx, in.Scope, doc)
	if err != nil {
		return out, err
	}
	if job.Status != JobStatusDone && job.Status != JobStatusFailed {
		return out, ErrConflict
	}
	for _, cid := range documentCandidateIDs(*job.FileExtract) {
		cand, err := s.Repo.Get(ctx, "memory_candidates", in.Scope, cid)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return out, err
		}
		switch cand.Status {
		case "approved":
			out.Kept++
		case "candidate":
			_, err = s.ReviewCandidate(ctx, token, cid, CandidateReviewInput{Scope: in.Scope, ExpectedRevision: cand.Revision, Action: "reject", Reason: "document deleted"})
			if err == nil {
				out.Rejected++
			} else if !errors.Is(err, ErrConflict) {
				return out, err
			}
		}
	}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out.Jobs = 0
		cur, e := tx.Get(ctx, "jobs", in.Scope, id)
		if e != nil {
			return e
		}
		if cur.Target == nil || cur.Target.ID != job.ID {
			return ErrConflict // a new upload arrived meanwhile
		}
		for next, n := job.ID, 0; next != "" && n < documentJobChainMax; n++ {
			r, e := s.extractJob(ctx, tx, in.Scope, next)
			if errors.Is(e, ErrNotFound) {
				break
			}
			if e != nil {
				return e
			}
			if r.FileExtract.Document != id {
				break
			}
			if e = tx.Delete(ctx, "jobs", in.Scope, r.ID, r.Revision); e != nil {
				return e
			}
			out.Jobs++
			next = r.FileExtract.Previous
		}
		if e = tx.Delete(ctx, "jobs", in.Scope, id, cur.Revision); e != nil {
			return e
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "delete:file_extract_document", id)
	})
	return out, err
}
