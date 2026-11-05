package memory

// Wiki folders: a page carries a folder path ("" = root); a person may also
// create an empty folder, kept as a wiki_folder row in the wiki_pages
// collection (every page path skips rows without a page). The tree is
// derived from both. A person moves or renames a page with PATCH; a rename
// that changes the slug gives the page a new id and is refused while other
// reviewed pages still link to it, so no link is left dangling.

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	wikiFolderMaxBytes = 256
	wikiFolderMaxDepth = 8
	wikiFolderMaxRunes = 64
)

type WikiFolder struct {
	Path string `json:"path" bson:"path"`
}

type WikiFolderInput struct {
	Scope Scope  `json:"scope"`
	Path  string `json:"path"`
}

// WikiPagePatchInput moves (folder) and/or renames (title) a page.
type WikiPagePatchInput struct {
	Scope            Scope   `json:"scope"`
	ExpectedRevision int64   `json:"expected_revision"`
	Folder           *string `json:"folder,omitempty"`
	Title            string  `json:"title,omitempty"`
}

type WikiTreeFolder struct {
	Path  string `json:"path"`
	Pages int    `json:"pages"`
}

type WikiTreePage struct {
	ID          string `json:"id"`
	Revision    int64  `json:"revision"`
	Slug        string `json:"slug"`
	Type        string `json:"type"`
	Title       string `json:"title"`
	Folder      string `json:"folder"`
	ReviewState string `json:"review_state"`
	Locked      bool   `json:"locked"`
	// Stale is set on a query page (type "query", id = mental model id) whose text
	// is behind its sources; a refresh writes a draft a person reviews.
	Stale bool `json:"stale,omitempty"`
}

type WikiTree struct {
	Folders   []WikiTreeFolder `json:"folders"`
	Pages     []WikiTreePage   `json:"pages"`
	Authority string           `json:"authority"`
	// Truncated: the scope has more than wikiListLimit wiki rows; only the newest are listed.
	Truncated bool `json:"truncated"`
}

// normalizeWikiFolder trims spaces and outer slashes; "" is the root. A
// segment is a plain name: no dot names, no path or shell metacharacters.
func normalizeWikiFolder(path string) (string, bool) {
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		return "", true
	}
	parts := strings.Split(path, "/")
	if len(path) > wikiFolderMaxBytes || len(parts) > wikiFolderMaxDepth || !utf8.ValidString(path) || containsSecret(path) {
		return "", false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || part != strings.TrimSpace(part) || utf8.RuneCountInString(part) > wikiFolderMaxRunes ||
			strings.ContainsAny(part, `\<>:"|?*`) || strings.IndexFunc(part, unicode.IsControl) >= 0 {
			return "", false
		}
	}
	return path, true
}

// wikiFolderAncestors lists "a", "a/b", "a/b/c" for "a/b/c".
func wikiFolderAncestors(path string) []string {
	var out []string
	for i, r := range path {
		if r == '/' {
			out = append(out, path[:i])
		}
	}
	if path != "" {
		out = append(out, path)
	}
	return out
}

func wikiFolderID(scope Scope, path string) string {
	return "wiki-folder-" + ContentHash(structuredText(scope)+"\x00"+path)
}

// CreateWikiFolder adds an empty folder; asking again returns the same folder.
func (s *Service) CreateWikiFolder(ctx context.Context, token string, in WikiFolderInput) (WikiTreeFolder, error) {
	path, ok := normalizeWikiFolder(in.Path)
	if !ok || path == "" {
		return WikiTreeFolder{}, ErrInvalid
	}
	id := wikiFolderID(in.Scope, path)
	d, err := s.wikiAuthorize(ctx, token, "write:"+colWikiPages, in.Scope, id, path, "human")
	if err != nil {
		return WikiTreeFolder{}, err
	}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := tx.Get(ctx, colWikiPages, in.Scope, id); err == nil {
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		now := time.Now().UTC()
		folder := Record{ID: id, Scope: in.Scope, Kind: "wiki_folder", Status: "active", Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role,
			OriginProduct: "wiki", Sensitivity: "private", Wiki: &WikiRecord{Folder: &WikiFolder{Path: path}}}
		if err := tx.Insert(ctx, colWikiPages, folder); err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "wiki:folder_created", id)
	})
	if err != nil {
		return WikiTreeFolder{}, err
	}
	return WikiTreeFolder{Path: path}, nil
}

// WikiTree lists folders and pages; people see drafts, other roles only
// servable reviewed pages. Bounded by wikiListLimit rows like ListWikiPages;
// Truncated says when rows were left out.
func (s *Service) WikiTree(ctx context.Context, token string, scope Scope) (WikiTree, error) {
	out := WikiTree{Folders: []WikiTreeFolder{}, Pages: []WikiTreePage{}, Authority: wikiAuthority}
	d, err := s.wikiAuthorize(ctx, token, "read:"+colWikiPages, scope, "", "")
	if err != nil {
		return out, err
	}
	rows, err := s.Repo.ListRecords(ctx, scope, colWikiPages, wikiListLimit+1)
	if err != nil {
		return out, err
	}
	// shortcut: no cursor, the newest wikiListLimit rows; add one when wikis outgrow it.
	if len(rows) > wikiListLimit {
		rows, out.Truncated = rows[:wikiListLimit], true
	}
	counts := map[string]int{}
	for _, r := range rows {
		if r.Wiki != nil && r.Wiki.Folder != nil {
			for _, f := range wikiFolderAncestors(r.Wiki.Folder.Path) {
				counts[f] += 0
			}
			continue
		}
		p := wikiPageOf(r)
		if p == nil || r.Status == "deleted" || d.Role != "human" && !wikiServable(ctx, s.Repo, r) {
			continue
		}
		for _, f := range wikiFolderAncestors(p.Folder) {
			counts[f] += 0
		}
		counts[p.Folder]++
		out.Pages = append(out.Pages, WikiTreePage{ID: r.ID, Revision: r.Revision, Slug: p.Slug, Type: p.Type, Title: wikiTitle(*p), Folder: p.Folder, ReviewState: p.ReviewState, Locked: p.Locked})
	}
	queries, err := s.wikiQueryPages(ctx, token, scope, d.Role == roleHuman)
	if err != nil {
		return out, err
	}
	for _, q := range queries {
		for _, f := range wikiFolderAncestors(q.Folder) {
			counts[f] += 0
		}
		counts[q.Folder]++
	}
	out.Pages = append(out.Pages, queries...)
	for path, n := range counts {
		if path != "" {
			out.Folders = append(out.Folders, WikiTreeFolder{Path: path, Pages: n})
		}
	}
	sort.Slice(out.Folders, func(i, j int) bool { return out.Folders[i].Path < out.Folders[j].Path })
	sort.Slice(out.Pages, func(i, j int) bool {
		a, b := out.Pages[i], out.Pages[j]
		return a.Folder < b.Folder || a.Folder == b.Folder && a.Slug < b.Slug
	})
	return out, nil
}

// PatchWikiPage moves a page to a folder and/or renames it (a person only).
// Locked pages may be moved and renamed; the body is not touched.
func (s *Service) PatchWikiPage(ctx context.Context, token, id string, in WikiPagePatchInput) (WikiPageView, error) {
	in.Title = strings.TrimSpace(in.Title)
	folder, ok := "", true
	if in.Folder != nil {
		folder, ok = normalizeWikiFolder(*in.Folder)
	}
	if !ok || !validRunLabel(id, 256) || in.Folder == nil && in.Title == "" || in.Title != "" && !validWikiTitle(in.Title) {
		return WikiPageView{}, ErrInvalid
	}
	if containsSecret(in.Title) {
		return WikiPageView{}, ErrSecretDetected
	}
	d, err := s.wikiAuthorize(ctx, token, "write:"+colWikiPages, in.Scope, id, in.Title+"\n"+folder, "human")
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
		if in.Folder != nil {
			p.Folder = folder
		}
		r.Wiki = &WikiRecord{Page: &p}
		if in.Title != "" && in.Title != wikiTitle(p) {
			err = s.renameWikiPage(ctx, tx, in.Scope, &r, in.Title, d.PrincipalID)
		} else {
			err = s.replaceWikiPage(ctx, tx, &r)
		}
		if err != nil {
			return err
		}
		out = r
		return audit(ctx, tx, in.Scope, d.PrincipalID, "wiki:patch", id)
	})
	if err != nil {
		return WikiPageView{}, err
	}
	return viewWikiPage(out, true), nil
}

// renameWikiPage retitles r. A new slug is a new page identity: the row moves
// to the new id with its link rows, refused while reviewed links point at it
// or the new id is taken.
func (s *Service) renameWikiPage(ctx context.Context, tx Tx, scope Scope, r *Record, title, actor string) error {
	p := *r.Wiki.Page
	slug := wikiSlug(title)
	retitle := func(b *WikiBody) *WikiBody {
		if b == nil {
			return nil
		}
		c := *b
		c.Title, c.Links = title, wikiLinks(c.Text, slug)
		return &c
	}
	old := *r
	p.Reviewed, p.Draft = retitle(p.Reviewed), retitle(p.Draft)
	if p.Reviewed != nil {
		r.Summary = title
	}
	r.Wiki = &WikiRecord{Page: &p}
	if slug == p.Slug {
		return s.replaceWikiPage(ctx, tx, r)
	}
	if err := s.wikiRenameFree(ctx, tx, scope, old.ID, wikiPageID(scope, slug)); err != nil {
		return err
	}
	gone := old
	gone.Status = "deleted"
	if err := syncWikiLinks(ctx, tx, scope, gone, old.Wiki.Page.Reviewed, actor); err != nil {
		return err
	}
	if err := tx.Delete(ctx, colWikiPages, scope, old.ID, old.Revision); err != nil {
		return err
	}
	p.Slug = slug
	r.ID, r.Revision, r.UpdatedAt = wikiPageID(scope, slug), 1, time.Now().UTC()
	r.Wiki = &WikiRecord{Page: &p}
	if err := tx.Insert(ctx, colWikiPages, *r); err != nil {
		return err
	}
	if err := syncWikiLinks(ctx, tx, scope, *r, nil, actor); err != nil {
		return err
	}
	return s.queueWikiEmbedding(ctx, tx, *r)
}

// wikiRenameFree: nothing links to from, and to is not an existing page.
func (s *Service) wikiRenameFree(ctx context.Context, tx Tx, scope Scope, from, to string) error {
	if _, err := tx.Get(ctx, colWikiPages, scope, to); err == nil {
		return ErrConflict
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	links, err := s.Repo.ListRecords(ctx, scope, colWikiLinks, wikiListLimit+1)
	if err != nil {
		return err
	}
	// A full window may hide an inbound link: refuse rather than leave one dangling.
	if len(links) > wikiListLimit {
		return ErrConflict
	}
	for _, l := range links {
		if l.Wiki != nil && l.Wiki.Link != nil && l.Wiki.Link.ToID == from && l.Wiki.Link.FromID != from {
			return ErrConflict
		}
	}
	return nil
}

// wikiQueryPages are the mental models placed in the wiki (a folder set): people
// see proposals and drafts too, other roles only models with approved text. A
// caller that may not read mental models gets none.
func (s *Service) wikiQueryPages(ctx context.Context, token string, scope Scope, human bool) ([]WikiTreePage, error) {
	if _, err := s.authorize(ctx, token, "read:"+colMentalModels, scope, "", ""); err != nil {
		return nil, nil
	}
	rows, _, err := s.wikiFolderModels(ctx, scope, human)
	if err != nil {
		return nil, err
	}
	out := []WikiTreePage{}
	for _, r := range rows {
		m := r.MentalModel
		if !human && (r.Status != statusActive || m.Active == nil) {
			continue
		}
		state := "reviewed"
		switch {
		case r.Status == obsProposed:
			state = "proposed"
		case m.Draft != nil:
			state = "draft"
		}
		stale, _ := s.mentalModelStale(ctx, r)
		out = append(out, WikiTreePage{ID: r.ID, Revision: r.Revision, Type: "query", Title: m.Title, Folder: *m.Folder, ReviewState: state, Stale: stale})
	}
	return out, nil
}

// wikiFolderModels lists the scope's mental models that sit in the wiki; full
// reports the list was cut at mmListLimit (folder changes refuse then).
func (s *Service) wikiFolderModels(ctx context.Context, scope Scope, withProposed bool) ([]Record, bool, error) {
	statuses := []string{statusActive}
	if withProposed {
		statuses = append(statuses, obsProposed)
	}
	out, full := []Record{}, false
	for _, status := range statuses {
		rows, err := s.Repo.ListRecordsByStatus(ctx, scope, colMentalModels, status, mmListLimit)
		if err != nil {
			return nil, false, err
		}
		full = full || len(rows) >= mmListLimit
		for _, r := range rows {
			if r.Scope == scope && r.MentalModel != nil && r.MentalModel.Folder != nil {
				out = append(out, r)
			}
		}
	}
	return out, full, nil
}

// WikiFolderMoveInput renames or moves folder From, with everything under it, to To.
type WikiFolderMoveInput struct {
	Scope Scope  `json:"scope"`
	From  string `json:"from"`
	To    string `json:"to"`
}

type WikiFolderDeleteInput struct {
	Scope     Scope  `json:"scope"`
	Path      string `json:"path"`
	Recursive bool   `json:"recursive,omitempty"`
}

type WikiPageDeleteInput struct {
	Scope            Scope `json:"scope"`
	ExpectedRevision int64 `json:"expected_revision"`
}

// WikiFolderMoveResult names the folder's new path and the page and query-page ids moved.
type WikiFolderMoveResult struct {
	Path  string   `json:"path"`
	Moved []string `json:"moved"`
}

// wikiInFolder: folder is path or lies under it.
func wikiInFolder(folder, path string) bool {
	return folder == path || strings.HasPrefix(folder, path+"/")
}

// wikiFolderSet is everything a folder change touches, read before its transaction.
type wikiFolderSet struct {
	rows    []Record // every wiki row, for unlinking
	folders []Record // folder rows under the path
	pages   []Record // live pages under the path
	models  []Record // query pages under the path
}

// readWikiFolder collects the rows under path. A change over a cut list could
// leave part of a folder behind, so a full window is refused. Query pages under
// path are mental models: changing them also needs the caller's mental model write.
func (s *Service) readWikiFolder(ctx context.Context, scope Scope, path, token string) (wikiFolderSet, error) {
	var set wikiFolderSet
	rows, err := s.Repo.ListRecords(ctx, scope, colWikiPages, wikiListLimit+1)
	if err != nil {
		return set, err
	}
	models, full, err := s.wikiFolderModels(ctx, scope, true)
	if err != nil {
		return set, err
	}
	if len(rows) > wikiListLimit || full {
		return set, ErrConflict
	}
	set.rows = rows
	for _, r := range rows {
		if r.Wiki != nil && r.Wiki.Folder != nil && wikiInFolder(r.Wiki.Folder.Path, path) {
			set.folders = append(set.folders, r)
		} else if p := wikiPageOf(r); p != nil && r.Status != "deleted" && wikiInFolder(p.Folder, path) {
			set.pages = append(set.pages, r)
		}
	}
	for _, r := range models {
		if wikiInFolder(*r.MentalModel.Folder, path) {
			set.models = append(set.models, r)
		}
	}
	if len(set.models) > 0 {
		if _, err = s.derivedHuman(ctx, token, colMentalModels, scope, ""); err != nil {
			return wikiFolderSet{}, err
		}
	}
	return set, nil
}

// MoveWikiFolder renames or moves a folder (a person only): its folder rows,
// pages and query pages take the new path in one transaction. Moving a folder
// into itself is refused; a rebased path must still be a valid folder.
func (s *Service) MoveWikiFolder(ctx context.Context, token string, in WikiFolderMoveInput) (WikiFolderMoveResult, error) {
	from, okFrom := normalizeWikiFolder(in.From)
	to, okTo := normalizeWikiFolder(in.To)
	if !okFrom || !okTo || from == "" || to == "" || wikiInFolder(to, from) {
		return WikiFolderMoveResult{}, ErrInvalid
	}
	d, err := s.wikiAuthorize(ctx, token, "write:"+colWikiPages, in.Scope, wikiFolderID(in.Scope, from), from+"\n"+to, "human")
	if err != nil {
		return WikiFolderMoveResult{}, err
	}
	set, err := s.readWikiFolder(ctx, in.Scope, from, token)
	if err != nil {
		return WikiFolderMoveResult{}, err
	}
	rebase := func(folder string) (string, error) {
		path, ok := normalizeWikiFolder(to + folder[len(from):])
		if !ok {
			return "", ErrInvalid
		}
		return path, nil
	}
	out := WikiFolderMoveResult{Path: to, Moved: []string{}}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out.Moved = []string{}
		if err := s.moveWikiFolderRows(ctx, tx, in.Scope, set.folders, rebase); err != nil {
			return err
		}
		for _, seen := range set.pages {
			r, err := s.getWikiPage(ctx, tx, in.Scope, seen.ID)
			if err != nil {
				return err
			}
			p := *r.Wiki.Page
			if p.Folder, err = rebase(p.Folder); err != nil {
				return err
			}
			r.Wiki = &WikiRecord{Page: &p}
			if err = s.replaceWikiPage(ctx, tx, &r); err != nil {
				return err
			}
			out.Moved = append(out.Moved, r.ID)
		}
		for _, seen := range set.models {
			if err := moveWikiQueryPage(ctx, tx, in.Scope, seen, rebase); err != nil {
				return err
			}
			out.Moved = append(out.Moved, seen.ID)
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "wiki:folder_moved", wikiFolderID(in.Scope, to))
	})
	if err != nil {
		return WikiFolderMoveResult{}, err
	}
	return out, nil
}

// moveWikiFolderRows re-keys the folder rows (their id is the path's hash).
func (s *Service) moveWikiFolderRows(ctx context.Context, tx Tx, scope Scope, folders []Record, rebase func(string) (string, error)) error {
	for _, r := range folders {
		path, err := rebase(r.Wiki.Folder.Path)
		if err != nil {
			return err
		}
		if err = tx.Delete(ctx, colWikiPages, scope, r.ID, r.Revision); err != nil {
			return err
		}
		id := wikiFolderID(scope, path)
		if _, err = tx.Get(ctx, colWikiPages, scope, id); err == nil {
			continue // the target folder already exists
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		r.ID, r.Revision, r.UpdatedAt = id, 1, time.Now().UTC()
		r.Wiki = &WikiRecord{Folder: &WikiFolder{Path: path}}
		if err = tx.Insert(ctx, colWikiPages, r); err != nil {
			return err
		}
	}
	return nil
}

func moveWikiQueryPage(ctx context.Context, tx Tx, scope Scope, seen Record, rebase func(string) (string, error)) error {
	r, err := tx.Get(ctx, colMentalModels, scope, seen.ID)
	if err != nil {
		return err
	}
	if r.Revision != seen.Revision || r.MentalModel == nil || r.MentalModel.Folder == nil {
		return ErrConflict
	}
	path, err := rebase(*r.MentalModel.Folder)
	if err != nil {
		return err
	}
	m := *r.MentalModel
	m.Folder = &path
	prev := r.Revision
	r.MentalModel, r.Revision, r.UpdatedAt = &m, r.Revision+1, time.Now().UTC().Truncate(time.Millisecond)
	return tx.Replace(ctx, colMentalModels, r, prev)
}

// DeleteWikiFolder removes a folder (a person only). A folder that still holds
// pages or query pages is refused unless recursive, which deletes them too:
// pages become erased "deleted" rows and links to them turn into plain text.
func (s *Service) DeleteWikiFolder(ctx context.Context, token string, in WikiFolderDeleteInput) (WikiCleanupResult, error) {
	out := WikiCleanupResult{Deleted: []string{}, Detached: []string{}, Unlinked: []string{}, JobsEnded: []string{}}
	path, ok := normalizeWikiFolder(in.Path)
	if !ok || path == "" {
		return out, ErrInvalid
	}
	d, err := s.wikiAuthorize(ctx, token, "write:"+colWikiPages, in.Scope, wikiFolderID(in.Scope, path), path, "human")
	if err != nil {
		return out, err
	}
	set, err := s.readWikiFolder(ctx, in.Scope, path, token)
	if err != nil {
		return out, err
	}
	if !in.Recursive && len(set.pages)+len(set.models) > 0 {
		return out, ErrConflict
	}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out.Deleted, out.Unlinked = []string{}, []string{}
		if err := s.deleteWikiPages(ctx, tx, in.Scope, d.PrincipalID, set.rows, set.pages, &out); err != nil {
			return err
		}
		for _, m := range set.models {
			if err := tx.Delete(ctx, colMentalModels, in.Scope, m.ID, m.Revision); err != nil {
				return err
			}
			if err := audit(ctx, tx, in.Scope, d.PrincipalID, "delete:"+colMentalModels, m.ID); err != nil {
				return err
			}
			out.Deleted = append(out.Deleted, m.ID)
		}
		for _, f := range set.folders {
			if err := tx.Delete(ctx, colWikiPages, in.Scope, f.ID, f.Revision); err != nil {
				return err
			}
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "wiki:folder_deleted", wikiFolderID(in.Scope, path))
	})
	return out, err
}

// deleteWikiPages deletes the given pages and unlinks them from the rest.
func (s *Service) deleteWikiPages(ctx context.Context, tx Tx, scope Scope, actor string, rows, pages []Record, out *WikiCleanupResult) error {
	deleted := map[string]string{}
	for _, seen := range pages {
		r, err := s.getWikiPage(ctx, tx, scope, seen.ID)
		if err != nil {
			return err
		}
		if r.Revision != seen.Revision || r.Status == "deleted" {
			return ErrConflict
		}
		if err = s.deleteWikiPageRow(ctx, tx, scope, &r, actor); err != nil {
			return err
		}
		deleted[r.Wiki.Page.Slug] = r.ID
		out.Deleted = append(out.Deleted, r.ID)
	}
	if len(deleted) == 0 {
		return nil
	}
	return s.unlinkDeletedWikiPages(ctx, tx, scope, rows, deleted, out)
}

// DeleteWikiPage deletes one page (a person only, locked pages too: the lock
// stops automation, not its owner). A query page (mental model id) is deleted
// as its mental model.
func (s *Service) DeleteWikiPage(ctx context.Context, token, id string, in WikiPageDeleteInput) (WikiCleanupResult, error) {
	out := WikiCleanupResult{Deleted: []string{}, Detached: []string{}, Unlinked: []string{}, JobsEnded: []string{}}
	if in.ExpectedRevision < 1 || !validRunLabel(id, 256) {
		return out, ErrInvalid
	}
	if strings.HasPrefix(id, "mm_") {
		if err := s.DeleteMentalModel(ctx, token, in.Scope, id, in.ExpectedRevision); err != nil {
			return out, err
		}
		out.Deleted = append(out.Deleted, id)
		return out, nil
	}
	d, err := s.wikiAuthorize(ctx, token, "write:"+colWikiPages, in.Scope, id, "", "human")
	if err != nil {
		return out, err
	}
	rows, err := s.Repo.ListRecords(ctx, in.Scope, colWikiPages, wikiListLimit+1)
	if err != nil {
		return out, err
	}
	// A full window may hide a page that links here: refuse rather than leave a link dangling.
	if len(rows) > wikiListLimit {
		return out, ErrConflict
	}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out.Deleted, out.Unlinked = []string{}, []string{}
		page := Record{ID: id, Revision: in.ExpectedRevision}
		if err := s.deleteWikiPages(ctx, tx, in.Scope, d.PrincipalID, rows, []Record{page}, &out); err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "wiki:page_deleted", id)
	})
	return out, err
}
