package memory

// LLM-Wiki: an optional path on top of knowledge sources. A worker job reads
// one ingested knowledge source and asks a model, in two steps, for entity and
// concept pages with [[links]] (wiki_job.go). Everything the model writes lands
// as a review draft; only a person turns a draft into the reviewed text that
// search and the read_wiki tool serve. A person can lock a page, and no job
// ever changes a locked page. Pages and links live in the DB only: a path the
// model names is a claim that is checked and then thrown away
// (wiki_protocol.go), never used to open a file.
//
// Page record: Text/Summary hold the reviewed body/title (what agents may read),
// Wiki.Page.Draft holds the pending model draft. Status is "active" once a
// reviewed body exists, "draft" before that, "rejected" for a dropped first
// draft and "deleted" after cascade cleanup removed its last source.
// wiki_links rows mirror the [[links]] of reviewed bodies only.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	colWikiPages  = "wiki_pages"
	colWikiLinks  = "wiki_links"
	wikiAuthority = "data_only_untrusted"
	wikiListLimit = 1000
)

func isWikiCollection(col string) bool { return col == colWikiPages || col == colWikiLinks }

// WikiRecord is the wiki part of a Record; exactly one member is set.
type WikiRecord struct {
	Page *WikiPage `json:"page,omitempty" bson:"page,omitempty"`
	Link *WikiLink `json:"link,omitempty" bson:"link,omitempty"`
	Job  *WikiJob  `json:"job,omitempty" bson:"job,omitempty"`
}

type WikiPage struct {
	Slug string `json:"slug" bson:"slug"`
	Type string `json:"type" bson:"type"`
	// Locked: a person owns this page; jobs skip it and never send it to a model.
	Locked bool `json:"locked" bson:"locked"`
	// ReviewState: draft (a model draft waits for a person) | reviewed | rejected | deleted.
	ReviewState string `json:"review_state" bson:"review_state"`
	// SourceIDs are the knowledge sources that support this page.
	SourceIDs []string  `json:"knowledge_source_ids" bson:"knowledge_source_ids"`
	Draft     *WikiBody `json:"draft,omitempty" bson:"draft,omitempty"`
	Reviewed  *WikiBody `json:"reviewed,omitempty" bson:"reviewed,omitempty"`
	LastMerge string    `json:"last_merge,omitempty" bson:"last_merge,omitempty"`
	LastJobID string    `json:"last_job_id,omitempty" bson:"last_job_id,omitempty"`
	// StaleSources: removed sources whose words may still be in the body.
	StaleSources []string `json:"stale_sources,omitempty" bson:"stale_sources,omitempty"`
}

type WikiBody struct {
	Title string    `json:"title" bson:"title"`
	Text  string    `json:"text" bson:"text"`
	Links []string  `json:"links" bson:"links"`
	By    string    `json:"by" bson:"by"`
	At    time.Time `json:"at" bson:"at"`
}

type WikiLink struct {
	FromID   string `json:"from_id" bson:"from_id"`
	ToID     string `json:"to_id" bson:"to_id"`
	FromSlug string `json:"from_slug" bson:"from_slug"`
	ToSlug   string `json:"to_slug" bson:"to_slug"`
}

// WikiPageView is the API shape of a page. Draft is only filled for people.
type WikiPageView struct {
	ID           string    `json:"id"`
	Revision     int64     `json:"revision"`
	Status       string    `json:"status"`
	Slug         string    `json:"slug"`
	Type         string    `json:"type"`
	Locked       bool      `json:"locked"`
	ReviewState  string    `json:"review_state"`
	SourceIDs    []string  `json:"knowledge_source_ids"`
	Reviewed     *WikiBody `json:"reviewed,omitempty"`
	Draft        *WikiBody `json:"draft,omitempty"`
	LastMerge    string    `json:"last_merge,omitempty"`
	LastJobID    string    `json:"last_job_id,omitempty"`
	StaleSources []string  `json:"stale_sources,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
	Authority    string    `json:"authority"`
}

type WikiPageList struct {
	Items     []WikiPageView `json:"items"`
	Authority string         `json:"authority"`
}

type WikiReviewInput struct {
	Scope            Scope  `json:"scope"`
	ExpectedRevision int64  `json:"expected_revision"`
	Decision         string `json:"decision"` // approve | reject
}

type WikiLockInput struct {
	Scope            Scope `json:"scope"`
	ExpectedRevision int64 `json:"expected_revision"`
	Locked           bool  `json:"locked"`
}

type WikiEditInput struct {
	Scope            Scope  `json:"scope"`
	ExpectedRevision int64  `json:"expected_revision"`
	Title            string `json:"title"`
	Text             string `json:"text"`
}

type WikiScopeInput struct {
	Scope Scope `json:"scope"`
}

// WikiSourceRemoveInput is the remove body; dry_run (here or ?dry_run=1)
// previews the cascade and writes nothing.
type WikiSourceRemoveInput struct {
	Scope  Scope `json:"scope"`
	DryRun bool  `json:"dry_run,omitempty"`
}

type WikiCleanupResult struct {
	// DryRun: nothing was changed; the lists say what the removal would do.
	DryRun    bool     `json:"dry_run"`
	Deleted   []string `json:"deleted"`
	Detached  []string `json:"detached"`
	Unlinked  []string `json:"unlinked"`
	JobsEnded []string `json:"jobs_ended"`
}

func wikiPageID(scope Scope, slug string) string {
	return "wiki-page-" + ContentHash(structuredText(scope)+"\x00"+slug)
}

func wikiLinkID(scope Scope, from, to string) string {
	return "wiki-link-" + ContentHash(structuredText(scope)+"\x00"+from+"\x00"+to)
}

func wikiPageOf(r Record) *WikiPage {
	if r.Wiki == nil {
		return nil
	}
	return r.Wiki.Page
}

func viewWikiPage(r Record, human bool) WikiPageView {
	p := r.Wiki.Page
	v := WikiPageView{ID: r.ID, Revision: r.Revision, Status: r.Status, Slug: p.Slug, Type: p.Type, Locked: p.Locked, ReviewState: p.ReviewState,
		SourceIDs: append([]string{}, p.SourceIDs...), Reviewed: p.Reviewed, LastMerge: p.LastMerge, LastJobID: p.LastJobID, StaleSources: p.StaleSources,
		UpdatedAt: r.UpdatedAt, Authority: wikiAuthority}
	if human {
		v.Draft = p.Draft
	}
	return v
}

// wikiServable: a reviewed body exists and every supporting source is still live.
// A locked page with no sources left is the person's own page.
func wikiServable(ctx context.Context, tx Tx, r Record) bool {
	p := wikiPageOf(r)
	if p == nil || r.Status != "active" || p.Reviewed == nil || r.PrivacyQuarantined || r.ContentErased {
		return false
	}
	if len(r.Sources) == 0 {
		return p.Locked
	}
	return checkSources(ctx, tx, r) == nil
}

func (s *Service) wikiAuthorize(ctx context.Context, token, action string, scope Scope, id, content string, roles ...string) (Decision, error) {
	if !validScope(scope) {
		return Decision{}, ErrInvalid
	}
	d, err := s.authorize(ctx, token, action, scope, id, content)
	if err != nil {
		return d, err
	}
	if len(roles) > 0 && !slices.Contains(roles, d.Role) {
		return Decision{}, ErrDenied
	}
	if content != "" && !d.ContentSafe {
		return Decision{}, ErrDenied
	}
	return d, nil
}

func (s *Service) getWikiPage(ctx context.Context, tx Tx, scope Scope, id string) (Record, error) {
	r, err := tx.Get(ctx, colWikiPages, scope, id)
	if err != nil {
		return r, err
	}
	if wikiPageOf(r) == nil {
		return Record{}, ErrNotFound
	}
	return r, nil
}

// ListWikiPages: people see drafts; other roles only servable reviewed pages.
func (s *Service) ListWikiPages(ctx context.Context, token string, scope Scope, state string) (WikiPageList, error) {
	out := WikiPageList{Items: []WikiPageView{}, Authority: wikiAuthority}
	if state == "" {
		state = "all"
	}
	if !slices.Contains([]string{"all", "draft", "reviewed", "rejected", "deleted", "locked"}, state) {
		return out, ErrInvalid
	}
	d, err := s.wikiAuthorize(ctx, token, "read:"+colWikiPages, scope, "", "")
	if err != nil {
		return out, err
	}
	rows, err := s.Repo.ListRecords(ctx, scope, colWikiPages, wikiListLimit)
	if err != nil {
		return out, err
	}
	human := d.Role == "human"
	for _, r := range rows {
		p := wikiPageOf(r)
		if p == nil {
			continue
		}
		if !human && !wikiServable(ctx, s.Repo, r) {
			continue
		}
		if state == "locked" && !p.Locked || state != "all" && state != "locked" && p.ReviewState != state {
			continue
		}
		out.Items = append(out.Items, viewWikiPage(r, human))
	}
	return out, nil
}

func (s *Service) GetWikiPage(ctx context.Context, token string, scope Scope, id string) (WikiPageView, error) {
	if !validRunLabel(id, 256) {
		return WikiPageView{}, ErrInvalid
	}
	d, err := s.wikiAuthorize(ctx, token, "read:"+colWikiPages, scope, id, "")
	if err != nil {
		return WikiPageView{}, err
	}
	r, err := s.getWikiPage(ctx, s.Repo, scope, id)
	if err != nil {
		return WikiPageView{}, err
	}
	if d.Role != "human" && !wikiServable(ctx, s.Repo, r) {
		return WikiPageView{}, ErrNotFound
	}
	return viewWikiPage(r, d.Role == "human"), nil
}

// ReviewWikiPage: a person approves the pending draft (it becomes the served
// body and its links become wiki_links rows) or rejects it.
func (s *Service) ReviewWikiPage(ctx context.Context, token, id string, in WikiReviewInput) (WikiPageView, error) {
	if in.Decision != "approve" && in.Decision != "reject" || !validRunLabel(id, 256) {
		return WikiPageView{}, ErrInvalid
	}
	d, err := s.wikiAuthorize(ctx, token, "review:"+colWikiPages, in.Scope, id, "", "human")
	if err != nil {
		return WikiPageView{}, err
	}
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := s.getWikiPage(ctx, tx, in.Scope, id)
		if err != nil {
			return err
		}
		if r.Revision != in.ExpectedRevision {
			return ErrConflict
		}
		p := *r.Wiki.Page
		if p.Draft == nil || p.ReviewState != "draft" {
			return ErrConflict
		}
		if in.Decision == "approve" && len(r.Sources) > 0 {
			if err = checkSources(ctx, tx, r); err != nil {
				return err
			}
		}
		old := p.Reviewed
		now := time.Now().UTC()
		if in.Decision == "approve" {
			body := *p.Draft
			body.By, body.At = d.PrincipalID, now
			p.Reviewed, p.Draft, p.ReviewState = &body, nil, "reviewed"
			r.Status, r.Text, r.Summary, r.ApprovedBy = "active", body.Text, body.Title, d.PrincipalID
		} else {
			p.Draft = nil
			if p.Reviewed != nil {
				p.ReviewState = "reviewed"
			} else {
				p.ReviewState, r.Status = "rejected", "rejected"
			}
		}
		r.Wiki = &WikiRecord{Page: &p}
		if err = s.replaceWikiPage(ctx, tx, &r); err != nil {
			return err
		}
		if in.Decision == "approve" {
			if err = syncWikiLinks(ctx, tx, in.Scope, r, old, d.PrincipalID); err != nil {
				return err
			}
		}
		out = r
		return audit(ctx, tx, in.Scope, d.PrincipalID, "wiki:"+in.Decision, id)
	})
	if err != nil {
		return WikiPageView{}, err
	}
	return viewWikiPage(out, true), nil
}

// LockWikiPage sets or clears the person-owned flag.
func (s *Service) LockWikiPage(ctx context.Context, token, id string, in WikiLockInput) (WikiPageView, error) {
	if !validRunLabel(id, 256) {
		return WikiPageView{}, ErrInvalid
	}
	d, err := s.wikiAuthorize(ctx, token, "write:"+colWikiPages, in.Scope, id, "", "human")
	if err != nil {
		return WikiPageView{}, err
	}
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := s.getWikiPage(ctx, tx, in.Scope, id)
		if err != nil {
			return err
		}
		if r.Revision != in.ExpectedRevision || r.Status == "deleted" {
			return ErrConflict
		}
		p := *r.Wiki.Page
		p.Locked = in.Locked
		r.Wiki = &WikiRecord{Page: &p}
		if err = s.replaceWikiPage(ctx, tx, &r); err != nil {
			return err
		}
		out = r
		action := "wiki:unlock"
		if in.Locked {
			action = "wiki:lock"
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, action, id)
	})
	if err != nil {
		return WikiPageView{}, err
	}
	return viewWikiPage(out, true), nil
}

// EditWikiPage: a person writes the page. The text is reviewed by its author,
// any pending model draft is dropped, and the page is locked so later jobs
// leave the person's words alone (unlock to let jobs merge again).
func (s *Service) EditWikiPage(ctx context.Context, token, id string, in WikiEditInput) (WikiPageView, error) {
	in.Text = strings.TrimSpace(in.Text)
	if !validRunLabel(id, 256) || in.Text == "" || len(in.Text) > wikiMaxBodyBytes || strings.ContainsRune(in.Text, 0) || in.Title != "" && !validWikiTitle(in.Title) {
		return WikiPageView{}, ErrInvalid
	}
	if containsSecret(in.Text) || containsSecret(in.Title) {
		return WikiPageView{}, ErrSecretDetected
	}
	d, err := s.wikiAuthorize(ctx, token, "write:"+colWikiPages, in.Scope, id, in.Title+"\n"+in.Text, "human")
	if err != nil {
		return WikiPageView{}, err
	}
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := s.getWikiPage(ctx, tx, in.Scope, id)
		if err != nil {
			return err
		}
		if r.Revision != in.ExpectedRevision || r.Status == "deleted" {
			return ErrConflict
		}
		p := *r.Wiki.Page
		title := in.Title
		if title == "" {
			title = wikiTitle(p)
		}
		if wikiSlug(title) != p.Slug {
			return ErrInvalid // renaming would change the page identity
		}
		old := p.Reviewed
		now := time.Now().UTC()
		p.Reviewed = &WikiBody{Title: title, Text: in.Text, Links: wikiLinks(in.Text, p.Slug), By: d.PrincipalID, At: now}
		p.Draft, p.ReviewState, p.Locked, p.LastMerge = nil, "reviewed", true, "human_edit"
		r.Wiki = &WikiRecord{Page: &p}
		r.Status, r.Text, r.Summary, r.ApprovedBy = "active", in.Text, title, d.PrincipalID
		if err = s.replaceWikiPage(ctx, tx, &r); err != nil {
			return err
		}
		if err = syncWikiLinks(ctx, tx, in.Scope, r, old, d.PrincipalID); err != nil {
			return err
		}
		out = r
		return audit(ctx, tx, in.Scope, d.PrincipalID, "wiki:edit", id)
	})
	if err != nil {
		return WikiPageView{}, err
	}
	return viewWikiPage(out, true), nil
}

func wikiTitle(p WikiPage) string {
	if p.Reviewed != nil {
		return p.Reviewed.Title
	}
	if p.Draft != nil {
		return p.Draft.Title
	}
	return p.Slug
}

// replaceWikiPage writes the next revision. Provenance (sources, generator,
// creator) is immutable on a row, so when a page gains or loses a supporting
// source the row is re-inserted at the next revision in the same transaction.
func (s *Service) replaceWikiPage(ctx context.Context, tx Tx, r *Record) error {
	prev := r.Revision
	r.Revision++
	r.UpdatedAt = time.Now().UTC()
	current, err := tx.Get(ctx, colWikiPages, r.Scope, r.ID)
	if err != nil {
		return err
	}
	if current.Revision != prev {
		return ErrConflict
	}
	if sameProvenance(current, *r) {
		return tx.Replace(ctx, colWikiPages, *r, prev)
	}
	if err = tx.Delete(ctx, colWikiPages, r.Scope, r.ID, prev); err != nil {
		return err
	}
	return tx.Insert(ctx, colWikiPages, *r)
}

// syncWikiLinks makes the wiki_links rows from page r equal its reviewed links.
func syncWikiLinks(ctx context.Context, tx Tx, scope Scope, r Record, old *WikiBody, actor string) error {
	p := r.Wiki.Page
	var before, after []string
	if old != nil {
		before = old.Links
	}
	if p.Reviewed != nil && r.Status == "active" {
		after = p.Reviewed.Links
	}
	for _, slug := range before {
		if !slices.Contains(after, slug) {
			if err := deleteWikiLink(ctx, tx, scope, r.ID, wikiPageID(scope, slug)); err != nil {
				return err
			}
		}
	}
	now := time.Now().UTC()
	for _, slug := range after {
		to := wikiPageID(scope, slug)
		id := wikiLinkID(scope, r.ID, to)
		if _, err := tx.Get(ctx, colWikiLinks, scope, id); err == nil {
			continue
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		link := Record{ID: id, Scope: scope, Kind: "wiki_link", Status: "active", Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: actor, CreatedByType: "human",
			OriginProduct: "wiki", Sensitivity: "private", Relations: []Ref{{Collection: colWikiPages, ID: r.ID, Revision: r.Revision}},
			Wiki: &WikiRecord{Link: &WikiLink{FromID: r.ID, ToID: to, FromSlug: p.Slug, ToSlug: slug}}}
		if err := tx.Insert(ctx, colWikiLinks, link); err != nil {
			return err
		}
	}
	return nil
}

func deleteWikiLink(ctx context.Context, tx Tx, scope Scope, from, to string) error {
	id := wikiLinkID(scope, from, to)
	l, err := tx.Get(ctx, colWikiLinks, scope, id)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return tx.Delete(ctx, colWikiLinks, scope, id, l.Revision)
}

// WikiGraph returns the reviewed link graph of servable pages.
func (s *Service) WikiGraph(ctx context.Context, token string, scope Scope) ([]WikiLink, error) {
	if _, err := s.wikiAuthorize(ctx, token, "read:"+colWikiLinks, scope, "", ""); err != nil {
		return nil, err
	}
	pages, links, err := s.wikiServableGraph(ctx, scope)
	if err != nil {
		return nil, err
	}
	out := []WikiLink{}
	for _, l := range links {
		if pages[l.FromID] != nil && pages[l.ToID] != nil {
			out = append(out, l)
		}
	}
	return out, nil
}

func (s *Service) wikiServableGraph(ctx context.Context, scope Scope) (map[string]*Record, []WikiLink, error) {
	rows, err := s.Repo.ListRecords(ctx, scope, colWikiPages, wikiListLimit)
	if err != nil {
		return nil, nil, err
	}
	pages := map[string]*Record{}
	for i := range rows {
		if wikiServable(ctx, s.Repo, rows[i]) {
			pages[rows[i].ID] = &rows[i]
		}
	}
	linkRows, err := s.Repo.ListRecords(ctx, scope, colWikiLinks, wikiListLimit)
	if err != nil {
		return nil, nil, err
	}
	links := []WikiLink{}
	for _, l := range linkRows {
		if l.Wiki != nil && l.Wiki.Link != nil {
			links = append(links, *l.Wiki.Link)
		}
	}
	return pages, links, nil
}

// RemoveWikiSource is the cascade a person starts when a knowledge source is
// removed (or no longer wanted in the wiki). Deterministic, no model: pages
// that only this source supported are deleted unless a person locked them;
// shared pages keep their text, drop the source and are marked stale; links
// to deleted pages become plain text in unlocked pages and their rows go.
func (s *Service) RemoveWikiSource(ctx context.Context, token, sourceID string, in WikiScopeInput) (WikiCleanupResult, error) {
	return s.removeWikiSource(ctx, token, sourceID, in, false)
}

// PreviewWikiSourceRemoval is RemoveWikiSource without the change: the same
// cascade runs in a transaction that is rolled back, so the confirm dialog can
// say how many pages would go, be marked stale or lose a link.
func (s *Service) PreviewWikiSourceRemoval(ctx context.Context, token, sourceID string, in WikiScopeInput) (WikiCleanupResult, error) {
	return s.removeWikiSource(ctx, token, sourceID, in, true)
}

// errWikiDryRun rolls a preview's transaction back.
var errWikiDryRun = errors.New("wiki dry run")

func (s *Service) removeWikiSource(ctx context.Context, token, sourceID string, in WikiScopeInput, dryRun bool) (WikiCleanupResult, error) {
	out := WikiCleanupResult{Deleted: []string{}, Detached: []string{}, Unlinked: []string{}, JobsEnded: []string{}}
	if !validRunLabel(sourceID, 256) {
		return out, ErrInvalid
	}
	d, err := s.wikiAuthorize(ctx, token, "write:"+colWikiPages, in.Scope, sourceID, "", "human")
	if err != nil {
		return out, err
	}
	pages, err := s.Repo.ListRecords(ctx, in.Scope, colWikiPages, wikiListLimit)
	if err != nil {
		return out, err
	}
	jobs, err := s.wikiJobRows(ctx, in.Scope, JobStatusPending, JobStatusRunning)
	if err != nil {
		return out, err
	}
	anchor := "knowledge-source-" + ContentHash(sourceID)
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out = WikiCleanupResult{Deleted: []string{}, Detached: []string{}, Unlinked: []string{}, JobsEnded: []string{}}
		deleted := map[string]string{} // slug -> page id
		for _, seen := range pages {
			if p := wikiPageOf(seen); p == nil || !slices.Contains(p.SourceIDs, sourceID) {
				continue
			}
			r, err := s.getWikiPage(ctx, tx, in.Scope, seen.ID)
			if err != nil {
				return err
			}
			p := *r.Wiki.Page
			p.SourceIDs = slices.DeleteFunc(append([]string{}, p.SourceIDs...), func(x string) bool { return x == sourceID })
			r.Sources = slices.DeleteFunc(append([]SourceRef{}, r.Sources...), func(x SourceRef) bool { return x.SourceID == anchor })
			if len(p.SourceIDs) == 0 && !p.Locked {
				old := p.Reviewed
				p.ReviewState, p.Draft, p.Reviewed, p.StaleSources = "deleted", nil, nil, nil
				r.Status, r.Text, r.Summary, r.ContentErased = "deleted", "", "", true
				r.Wiki = &WikiRecord{Page: &p}
				if err = s.replaceWikiPage(ctx, tx, &r); err != nil {
					return err
				}
				if err = syncWikiLinks(ctx, tx, in.Scope, r, old, d.PrincipalID); err != nil {
					return err
				}
				deleted[p.Slug] = r.ID
				out.Deleted = append(out.Deleted, r.ID)
				continue
			}
			if !slices.Contains(p.StaleSources, sourceID) {
				p.StaleSources = append(append([]string{}, p.StaleSources...), sourceID)
			}
			r.Wiki = &WikiRecord{Page: &p}
			if err = s.replaceWikiPage(ctx, tx, &r); err != nil {
				return err
			}
			out.Detached = append(out.Detached, r.ID)
		}
		if len(deleted) > 0 {
			for _, seen := range pages {
				if _, gone := deleted[wikiSlugOf(seen)]; gone || wikiPageOf(seen) == nil {
					continue
				}
				r, err := s.getWikiPage(ctx, tx, in.Scope, seen.ID)
				if err != nil {
					return err
				}
				p := *r.Wiki.Page
				changed := false
				for slug, to := range deleted {
					linked := p.Reviewed != nil && slices.Contains(p.Reviewed.Links, slug) || p.Draft != nil && slices.Contains(p.Draft.Links, slug)
					if !linked {
						continue
					}
					// The row goes even from a locked page; its text stays as the person wrote it.
					if err = deleteWikiLink(ctx, tx, in.Scope, r.ID, to); err != nil {
						return err
					}
					if p.Locked {
						continue
					}
					p.Reviewed = unlinkBody(p.Reviewed, slug, p.Slug)
					p.Draft = unlinkBody(p.Draft, slug, p.Slug)
					changed = true
				}
				if !changed {
					continue
				}
				if p.Reviewed != nil {
					r.Text = p.Reviewed.Text
				}
				r.Wiki = &WikiRecord{Page: &p}
				if err = s.replaceWikiPage(ctx, tx, &r); err != nil {
					return err
				}
				out.Unlinked = append(out.Unlinked, r.ID)
			}
		}
		for _, seen := range jobs {
			if seen.Kind != wikiJobKind || seen.Wiki == nil || seen.Wiki.Job == nil || seen.Wiki.Job.KnowledgeSourceID != sourceID || seen.Status == JobStatusDone || seen.Status == JobStatusFailed {
				continue
			}
			j, err := tx.Get(ctx, "jobs", in.Scope, seen.ID)
			if err != nil {
				return err
			}
			job := *j.Wiki.Job
			job.Error = "source_removed"
			j.Wiki = &WikiRecord{Job: &job}
			j.Status, j.LeaseToken, j.LeaseUntil = JobStatusFailed, "", time.Time{}
			prev := j.Revision
			j.Revision++
			j.UpdatedAt = time.Now().UTC()
			if err = tx.Replace(ctx, "jobs", j, prev); err != nil {
				return err
			}
			out.JobsEnded = append(out.JobsEnded, j.ID)
		}
		if dryRun {
			return errWikiDryRun
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "wiki:source_removed", sourceID)
	})
	if dryRun && errors.Is(err, errWikiDryRun) {
		out.DryRun, err = true, nil
	}
	return out, err
}

func wikiSlugOf(r Record) string {
	if p := wikiPageOf(r); p != nil {
		return p.Slug
	}
	return ""
}

func unlinkBody(b *WikiBody, slug, self string) *WikiBody {
	if b == nil {
		return nil
	}
	c := *b
	c.Text = wikiUnlink(c.Text, slug)
	c.Links = wikiLinks(c.Text, self)
	return &c
}

// parseWikiQuery reads the scope plus the named extra keys from a GET query.
func parseWikiQuery(raw string, extra ...string) (Scope, url.Values, error) {
	q, err := url.ParseQuery(raw)
	if err != nil {
		return Scope{}, nil, ErrInvalid
	}
	for key, values := range q {
		if len(values) != 1 || !slices.Contains(append([]string{"workspace_id", "project_id", "user_id", "agent_id"}, extra...), key) {
			return Scope{}, nil, ErrInvalid
		}
	}
	scope := Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")}
	if !validScope(scope) {
		return Scope{}, nil, ErrInvalid
	}
	return scope, q, nil
}

func registerWikiHTTP(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/wiki/jobs", func(w http.ResponseWriter, r *http.Request) {
		var in WikiJobInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.CreateWikiJob(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/wiki/jobs", func(w http.ResponseWriter, r *http.Request) {
		scope, q, e := parseWikiQuery(r.URL.RawQuery, "status")
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.ListWikiJobs(r.Context(), bearer(r), scope, q.Get("status"))
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/wiki/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		scope, _, e := parseWikiQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.GetWikiJob(r.Context(), bearer(r), scope, r.PathValue("id"))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/wiki/jobs/{id}/run", func(w http.ResponseWriter, r *http.Request) {
		var in WikiScopeInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.RunWikiJob(r.Context(), bearer(r), in.Scope, r.PathValue("id"))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/wiki/work", func(w http.ResponseWriter, r *http.Request) {
		var in WikiScopeInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.WikiWorkOnce(r.Context(), bearer(r), in.Scope)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/wiki/pages", func(w http.ResponseWriter, r *http.Request) {
		scope, q, e := parseWikiQuery(r.URL.RawQuery, "state")
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.ListWikiPages(r.Context(), bearer(r), scope, q.Get("state"))
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/wiki/pages/{id}", func(w http.ResponseWriter, r *http.Request) {
		scope, _, e := parseWikiQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.GetWikiPage(r.Context(), bearer(r), scope, r.PathValue("id"))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/wiki/pages/{id}/review", func(w http.ResponseWriter, r *http.Request) {
		var in WikiReviewInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.ReviewWikiPage(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/wiki/pages/{id}/lock", func(w http.ResponseWriter, r *http.Request) {
		var in WikiLockInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.LockWikiPage(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/wiki/pages/{id}/edit", func(w http.ResponseWriter, r *http.Request) {
		var in WikiEditInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.EditWikiPage(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/wiki/graph", func(w http.ResponseWriter, r *http.Request) {
		scope, _, e := parseWikiQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		links, e := s.WikiGraph(r.Context(), bearer(r), scope)
		respond(w, map[string]any{"links": links, "authority": wikiAuthority}, e)
	})
	mux.HandleFunc("POST /v1/wiki/search", func(w http.ResponseWriter, r *http.Request) {
		var in WikiSearchInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.SearchWiki(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/wiki/read", func(w http.ResponseWriter, r *http.Request) {
		var in WikiSearchInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.ReadWiki(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/wiki/sources/{id}/remove", func(w http.ResponseWriter, r *http.Request) {
		var in WikiSourceRemoveInput
		if !decode(w, r, &in) {
			return
		}
		remove := s.RemoveWikiSource
		if q := r.URL.Query().Get("dry_run"); in.DryRun || q == "1" || q == "true" {
			remove = s.PreviewWikiSourceRemoval
		}
		out, e := remove(r.Context(), bearer(r), r.PathValue("id"), WikiScopeInput{Scope: in.Scope})
		respond(w, out, e)
	})
}
