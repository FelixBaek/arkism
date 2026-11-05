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
}

type WikiTree struct {
	Folders   []WikiTreeFolder `json:"folders"`
	Pages     []WikiTreePage   `json:"pages"`
	Authority string           `json:"authority"`
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
// servable reviewed pages. Bounded by wikiListLimit rows like ListWikiPages.
func (s *Service) WikiTree(ctx context.Context, token string, scope Scope) (WikiTree, error) {
	out := WikiTree{Folders: []WikiTreeFolder{}, Pages: []WikiTreePage{}, Authority: wikiAuthority}
	d, err := s.wikiAuthorize(ctx, token, "read:"+colWikiPages, scope, "", "")
	if err != nil {
		return out, err
	}
	rows, err := s.Repo.ListRecords(ctx, scope, colWikiPages, wikiListLimit)
	if err != nil {
		return out, err
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
	links, err := s.Repo.ListRecords(ctx, scope, colWikiLinks, wikiListLimit)
	if err != nil {
		return err
	}
	for _, l := range links {
		if l.Wiki != nil && l.Wiki.Link != nil && l.Wiki.Link.ToID == from && l.Wiki.Link.FromID != from {
			return ErrConflict
		}
	}
	return nil
}
