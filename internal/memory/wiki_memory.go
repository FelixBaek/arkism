package memory

// Wiki pages from approved memories. A person asks for a build by query,
// topic or entity name; the job recalls live (human-approved) memories through
// the normal search path, fences them as data and runs the same two model
// steps as a knowledge build (wiki_job.go). Every written block must cite the
// memories it uses as [mem:ID], and only ids from the recalled set are
// accepted. The result is a page draft a person reviews; locked pages are
// never sent to the model or written. dry_run returns the drafts and writes
// nothing (the model calls are still metered).

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	wikiLLMDestination  = "external:wiki-llm"
	wikiMemoryMaxRecall = 40
	wikiMemoryMaxQuery  = 512
)

// errWikiModelMissing: no wiki model is configured on this server.
var errWikiModelMissing = fmt.Errorf("%w: model_not_configured", ErrUnavailable)

var wikiCitationPattern = regexp.MustCompile(`\[mem:([0-9A-Za-z_.:-]{1,256})\]`)

const wikiCitationRule = " Each memory in SOURCE_DATA starts with its id as [mem:ID]. Cite every fact with the [mem:ID] of the memory it comes from, copied exactly; never invent an id. A page without a citation is dropped."

// WikiMemoryRequest says which memories a build reads; exactly one field is set.
type WikiMemoryRequest struct {
	Query      string `json:"query,omitempty" bson:"query,omitempty"`
	Topic      string `json:"topic,omitempty" bson:"topic,omitempty"`
	EntityName string `json:"entity_name,omitempty" bson:"entity_name,omitempty"`
	// MemoryIDs are the memories the last run recalled and sent to the model.
	MemoryIDs []string `json:"memory_ids,omitempty" bson:"memory_ids,omitempty"`
}

type WikiMemoryBuildInput struct {
	Scope      Scope  `json:"scope"`
	Query      string `json:"query,omitempty"`
	Topic      string `json:"topic,omitempty"`
	EntityName string `json:"entity_name,omitempty"`
	DryRun     bool   `json:"dry_run,omitempty"`
	// MaxTotalTokens lowers the per-job token budget (default 40000, max 120000).
	MaxTotalTokens int64 `json:"max_total_tokens,omitempty"`
}

// WikiDraftPreview is one page a dry run would write as a draft.
type WikiDraftPreview struct {
	Slug      string   `json:"slug"`
	Type      string   `json:"type"`
	Title     string   `json:"title"`
	Text      string   `json:"text"`
	Mode      string   `json:"mode"`
	Citations []string `json:"citations"`
}

type WikiMemoryBuildResult struct {
	DryRun    bool               `json:"dry_run"`
	Job       *WikiJobView       `json:"job,omitempty"`
	Drafts    []WikiDraftPreview `json:"drafts,omitempty"`
	Result    *WikiJobResult     `json:"result,omitempty"`
	MemoryIDs []string           `json:"memory_ids,omitempty"`
}

func (in WikiMemoryBuildInput) request() (WikiMemoryRequest, string, bool) {
	req := WikiMemoryRequest{Query: strings.TrimSpace(in.Query), Topic: strings.TrimSpace(in.Topic), EntityName: strings.TrimSpace(in.EntityName)}
	set := 0
	text := ""
	for _, v := range []string{req.Query, req.Topic, req.EntityName} {
		if v != "" {
			set++
			text = v
		}
	}
	ok := set == 1 && utf8.RuneCountInString(text) <= wikiMemoryMaxQuery && validRunLabel(text, 4*wikiMemoryMaxQuery) && in.MaxTotalTokens >= 0 && in.MaxTotalTokens <= wikiMaxJobTokens
	return req, text, ok
}

func (r WikiMemoryRequest) text() string { return r.Query + r.Topic + r.EntityName }

// BuildWikiFromMemories queues a memory build job (run it with the wiki job
// routes) or, with dry_run, runs it now and returns the drafts unsaved.
func (s *Service) BuildWikiFromMemories(ctx context.Context, token string, in WikiMemoryBuildInput) (WikiMemoryBuildResult, error) {
	req, text, ok := in.request()
	if !ok {
		return WikiMemoryBuildResult{}, ErrInvalid
	}
	if containsSecret(text) {
		return WikiMemoryBuildResult{}, ErrSecretDetected
	}
	d, err := s.wikiAuthorize(ctx, token, "write:"+colWikiPages, in.Scope, "", text, "human")
	if err != nil {
		return WikiMemoryBuildResult{}, err
	}
	if s.Wiki == nil {
		return WikiMemoryBuildResult{}, errWikiModelMissing
	}
	budget := in.MaxTotalTokens
	if budget == 0 {
		budget = wikiDefaultJobTokens
	}
	if in.DryRun {
		return s.previewWikiFromMemories(ctx, token, d, in.Scope, req, budget)
	}
	job, err := s.createWikiMemoryJob(ctx, d, in.Scope, req, budget)
	if err != nil {
		return WikiMemoryBuildResult{}, err
	}
	return WikiMemoryBuildResult{Job: &job}, nil
}

func (s *Service) previewWikiFromMemories(ctx context.Context, token string, d Decision, scope Scope, req WikiMemoryRequest, budget int64) (WikiMemoryBuildResult, error) {
	job := WikiJob{Memory: &req, MaxTotalTokens: budget, RequestedBy: d.PrincipalID}
	w, err := s.buildWiki(ctx, token, d, Record{Scope: scope}, &job)
	if err != nil {
		return WikiMemoryBuildResult{}, err
	}
	out := WikiMemoryBuildResult{DryRun: true, Drafts: []WikiDraftPreview{}, Result: &w.result, MemoryIDs: job.Memory.MemoryIDs}
	for _, b := range w.blocks {
		out.Drafts = append(out.Drafts, WikiDraftPreview{Slug: b.Slug, Type: b.Type, Title: b.Title, Text: b.Body, Mode: b.Mode, Citations: b.Citations})
	}
	return out, nil
}

// createWikiMemoryJob: one job per scope and request; asking again while it
// waits returns it, and a finished one is queued again (memories change).
func (s *Service) createWikiMemoryJob(ctx context.Context, d Decision, scope Scope, req WikiMemoryRequest, budget int64) (WikiJobView, error) {
	id := "wiki-job-" + ContentHash(structuredText(scope)+"\x00memory\x00"+structuredText(req))
	var out Record
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		now := time.Now().UTC()
		old, err := tx.Get(ctx, "jobs", scope, id)
		if err == nil {
			if wikiJobOf(old) == nil {
				return ErrConflict
			}
			if old.Status == JobStatusPending || old.Status == JobStatusRunning {
				old.Duplicate, out = true, old
				return nil
			}
			if err = requeueWikiJob(ctx, tx, &old, budget, now); err != nil {
				return err
			}
			out = old
			return audit(ctx, tx, scope, d.PrincipalID, "wiki:job_requeued", id)
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		out = Record{ID: id, Scope: scope, Kind: wikiJobKind, Status: JobStatusPending, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role,
			Wiki: &WikiRecord{Job: &WikiJob{Memory: &req, MaxTotalTokens: budget, RequestedBy: d.PrincipalID}}}
		if err = tx.Insert(ctx, "jobs", out); err != nil {
			return err
		}
		return audit(ctx, tx, scope, d.PrincipalID, "wiki:job_created", id)
	})
	if err != nil {
		return WikiJobView{}, err
	}
	return viewWikiJob(out), nil
}

// buildWikiFromMemories is buildWiki for a memory job: recall, two model
// steps, then the citation boundary.
func (s *Service) buildWikiFromMemories(ctx context.Context, token string, d Decision, scope Scope, job *WikiJob, gen Generator) (*wikiWrite, error) {
	source, ids, err := s.wikiMemorySource(ctx, token, scope, job.Memory.text())
	if err != nil {
		return nil, err
	}
	req := *job.Memory
	req.MemoryIDs = ids
	job.Memory = &req
	w := newWikiWrite(nil, gen)
	if err = s.wikiPlanAndWrite(ctx, token, d, scope, job, w, "", source, wikiCitationRule); err != nil {
		return nil, err
	}
	w.blocks = wikiCheckCitations(w, ids)
	return w, nil
}

// wikiMemorySource recalls live memories for the query through the normal
// search path (so the caller's search permission applies) and fences each as
// "[mem:ID] summary: text". Private spans are masked; memories that are
// erased, carry a secret or fail the per-record external transfer check
// (restricted or unclassified, restricted sources, policy) are left out.
func (s *Service) wikiMemorySource(ctx context.Context, token string, scope Scope, query string) (string, []string, error) {
	bundle, err := s.Search(ctx, token, SearchInput{Scope: scope, Query: query, Limit: wikiMemoryMaxRecall, MaxChars: 64000})
	if err != nil {
		return "", nil, err
	}
	var b strings.Builder
	ids := []string{}
	for _, hit := range bundle.Items {
		if hit.Collection != colMemories || slices.Contains(ids, hit.ID) {
			continue
		}
		r, err := s.Repo.Get(ctx, colMemories, scope, hit.ID)
		if err != nil || !live(r) || r.ContentErased {
			continue
		}
		line := "[mem:" + r.ID + "] " + redactSessionPrivate(strings.TrimSpace(r.Summary+": "+r.Text)) + "\n"
		if containsSecret(line) || utf8.RuneCountInString(b.String()+line) > wikiMaxSourceRunes {
			continue
		}
		// Each memory leaves the server on its own: classification, source lineage and policy per record.
		if _, err = s.authorizeTransfer(ctx, token, "context:read", scope, r.ID, r.Sensitivity, wikiLLMDestination, line, r.Sources); err != nil {
			continue
		}
		b.WriteString(line)
		ids = append(ids, r.ID)
	}
	if len(ids) == 0 {
		return "", nil, ErrNotFound
	}
	return b.String(), ids, nil
}

// wikiCheckCitations keeps the blocks whose every [mem:ID] is one of the
// recalled memories and that cite at least one; the rest are rejected.
func wikiCheckCitations(w *wikiWrite, recalled []string) []wikiBlock {
	var kept []wikiBlock
	for _, b := range w.blocks {
		cited := wikiCitations(b.Body)
		reason := ""
		if len(cited) == 0 {
			reason = "no_citation"
		} else if slices.ContainsFunc(cited, func(id string) bool { return !slices.Contains(recalled, id) }) {
			reason = "unknown_citation"
		}
		if reason != "" {
			w.result.Rejected = append(w.result.Rejected, wikiRejection{Path: wikiClip(b.Path), Reason: reason})
			continue
		}
		b.Citations = cited
		kept = append(kept, b)
	}
	return kept
}

// wikiCitations lists the distinct [mem:ID] ids in text, in order.
func wikiCitations(text string) []string {
	var out []string
	for _, m := range wikiCitationPattern.FindAllStringSubmatch(text, -1) {
		out = wikiUnion(out, []string{m[1]})
	}
	return out
}

// wikiMemoriesLive: every cited memory still exists, is live and not erased.
func wikiMemoriesLive(ctx context.Context, tx Tx, scope Scope, ids []string) error {
	for _, id := range ids {
		r, err := tx.Get(ctx, colMemories, scope, id)
		if err != nil {
			return err
		}
		if !live(r) || r.ContentErased {
			return ErrConflict
		}
	}
	return nil
}
