package memory

import (
	"context"
	"errors"
	"strings"
	"time"
)

func viewExtractJob(r Record) FileExtractJobView {
	return FileExtractJobView{ID: r.ID, Revision: r.Revision, Status: r.Status, Job: *r.FileExtract, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// createExtractJob queues the chunks; the same file in the same scope has one job.
func (s *Service) createExtractJob(ctx context.Context, d Decision, scope Scope, label string, chunks []FileExtractChunk, skipped []ExtractSkip) (Record, error) {
	ids := extractSourceIDs(chunks)
	texts := make([]string, len(chunks))
	for i, ch := range chunks {
		texts[i] = ch.Text
	}
	id := "file-extract-job-" + ContentHash(structuredText(scope)+"\x00"+strings.Join(ids, "\x00")+"\x00"+ContentHash(strings.Join(texts, "\x00")))
	var out Record
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		old, err := tx.Get(ctx, "jobs", scope, id)
		if err == nil {
			if old.Kind != fileExtractJobKind || old.FileExtract == nil {
				return ErrConflict
			}
			out = old
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		now := time.Now().UTC()
		out = Record{ID: id, Scope: scope, Kind: fileExtractJobKind, Status: JobStatusPending, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role,
			FileExtract: &FileExtractJob{Label: label, Sources: ids, Chunks: chunks, Total: len(chunks), Skipped: skipped, RequestedBy: d.PrincipalID}}
		if err = tx.Insert(ctx, "jobs", out); err != nil {
			return err
		}
		return audit(ctx, tx, scope, d.PrincipalID, "file_extract:job_created", id)
	})
	return out, err
}

func (s *Service) extractJob(ctx context.Context, tx Tx, scope Scope, id string) (Record, error) {
	r, err := tx.Get(ctx, "jobs", scope, id)
	if err == nil && (r.Kind != fileExtractJobKind || r.FileExtract == nil) {
		err = ErrNotFound
	}
	return r, err
}

// GetFileExtractJob shows a job's progress; chunk text never leaves the server.
func (s *Service) GetFileExtractJob(ctx context.Context, token string, scope Scope, id string) (FileExtractJobView, error) {
	if !validRunLabel(id, 256) {
		return FileExtractJobView{}, ErrInvalid
	}
	if _, err := s.wikiAuthorize(ctx, token, "read:memory_candidates", scope, id, ""); err != nil {
		return FileExtractJobView{}, err
	}
	r, err := s.extractJob(ctx, s.Repo, scope, id)
	if err != nil {
		return FileExtractJobView{}, err
	}
	return viewExtractJob(r), nil
}

// claimExtractJob takes the job's lease; a finished or leased job is returned unclaimed.
func (s *Service) claimExtractJob(ctx context.Context, scope Scope, id string) (Record, bool, error) {
	var claimed Record
	ok := false
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := s.extractJob(ctx, tx, scope, id)
		if err != nil {
			return err
		}
		claimed = r
		now := time.Now().UTC()
		if r.Status == JobStatusDone || r.Status == JobStatusRunning && r.LeaseUntil.After(now) {
			return nil
		}
		r.Status, r.LeaseUntil, r.LeaseToken, r.UpdatedAt = JobStatusRunning, now.Add(fileExtractJobLease), ID(), now
		r.Attempts++
		r.FileExtract.Error = ""
		prev := r.Revision
		r.Revision++
		if err = tx.Replace(ctx, "jobs", r, prev); err != nil {
			return err
		}
		claimed, ok = r, true
		return nil
	})
	return claimed, ok, err
}

// saveExtractJob writes progress while the lease is still ours.
func (s *Service) saveExtractJob(ctx context.Context, r Record) (Record, error) {
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		current, err := s.extractJob(ctx, tx, r.Scope, r.ID)
		if err != nil {
			return err
		}
		if current.LeaseToken != r.LeaseToken || current.Revision != r.Revision {
			return ErrConflict
		}
		r.UpdatedAt = time.Now().UTC()
		r.Revision++
		return tx.Replace(ctx, "jobs", r, current.Revision)
	})
	return r, err
}

// RunFileExtractJob works through the remaining chunks. A budget stop or an
// error leaves the job failed at the chunk it reached; running it again
// resumes there. A finished job drops its copy of the text.
func (s *Service) RunFileExtractJob(ctx context.Context, token string, scope Scope, id string) (FileExtractJobView, error) {
	if !validRunLabel(id, 256) {
		return FileExtractJobView{}, ErrInvalid
	}
	d, err := s.extractActor(ctx, token, "write:memory_candidates", scope)
	if err != nil {
		return FileExtractJobView{}, err
	}
	r, ok, err := s.claimExtractJob(ctx, scope, id)
	if err != nil || !ok {
		return viewExtractJob(r), err
	}
	job := r.FileExtract
	budget := &WikiJob{MaxTotalTokens: fileExtractJobTokens}
	for job.Next < len(job.Chunks) {
		created, skipped, runErr := s.extractAndCapture(ctx, token, d, scope, budget, job.Chunks[job.Next])
		job.Calls, job.Tokens = job.Calls+budget.Calls, job.Tokens+budget.Tokens
		budget.Calls, budget.Tokens = 0, 0
		if runErr != nil {
			code, _ := errorResponse(runErr)
			job.Error, r.Status = code, JobStatusFailed
			r, err = s.saveExtractJob(ctx, r)
			return viewExtractJob(r), err
		}
		job.CandidatesCreated += created
		job.Skipped = append(job.Skipped, skipped...)
		job.Next++
		if job.Next == len(job.Chunks) {
			r.Status, job.Chunks = JobStatusDone, nil
		}
		if r, err = s.saveExtractJob(ctx, r); err != nil {
			return FileExtractJobView{}, err
		}
		job = r.FileExtract
	}
	return viewExtractJob(r), nil
}

// ExtractKnowledgeMemories turns an existing knowledge source into candidates,
// with the knowledge file's source anchor as their evidence.
func (s *Service) ExtractKnowledgeMemories(ctx context.Context, token, id string, in KnowledgeExtractInput) (FileMemoriesResult, error) {
	if !validRunLabel(id, 256) {
		return FileMemoriesResult{}, ErrInvalid
	}
	d, err := s.extractActor(ctx, token, "write:memory_candidates", in.Scope)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	src, err := s.GetKnowledgeSource(ctx, token, id, in.Scope)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	text, err := s.knowledgeSourceText(ctx, in.Scope, src)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	anchor, err := s.Get(ctx, token, "sources", in.Scope, src.SourceEventID)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	chunks, redacted, err := extractFileChunks([]string{text}, false)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	out := FileMemoriesResult{Skipped: extractRedactionNote(redacted)}
	if in.DryRun {
		out.Status = JobStatusDone
		return out, s.previewChunks(ctx, token, d, in.Scope, chunks, &out)
	}
	for i := range chunks {
		chunks[i].Source = Ref{Collection: "sources", ID: anchor.ID, Revision: anchor.Revision}
	}
	return s.extractStored(ctx, token, d, in.Scope, src.Knowledge.DisplayName, chunks, out)
}
