package memory

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// 세션 찾기 and 이어가기 for the session list: words inside every archived dialogue,
// grouped by the session they came from, and what another agent needs to pick a
// session up. Both only read; nothing here runs an agent or a command.

type UsageSessionSearchInput struct {
	Scope   Scope  `json:"scope"`
	Query   string `json:"q"`
	Agent   string `json:"source,omitempty"`
	Project string `json:"project,omitempty"`
	After   string `json:"after,omitempty"`
	Limit   int    `json:"limit"`
}

type UsageSessionSearchHit struct {
	Role    string `json:"role"`
	Snippet string `json:"snippet"`
	// Match is where the words sit in Snippet (bytes), so the app can underline them.
	Match int `json:"match"`
}

type UsageSessionSearchGroup struct {
	Session map[string]any          `json:"session"`
	Hits    []UsageSessionSearchHit `json:"hits"`
}

type UsageSessionSearchOutput struct {
	Items []UsageSessionSearchGroup `json:"items"`
	Next  string                    `json:"next_cursor,omitempty"`
	// Hidden counts matching messages left out because the words were only inside <private>.
	Hidden int `json:"hidden"`
	// Secrets counts scanned messages never searched because they hold a secret.
	Secrets      int    `json:"secret_records"`
	ScannedBytes int    `json:"scanned_bytes"`
	Authority    string `json:"authority"`
}

const usageSearchSnippetBefore, usageSearchSnippetAfter = 80, 160

// SearchUsageSessions reads one page of the archive scan (POST /v1/session-archives/search,
// same budget and cursor) and keeps the hits whose session is still listed.
func (s *Service) SearchUsageSessions(ctx context.Context, token string, in UsageSessionSearchInput) (UsageSessionSearchOutput, error) {
	out := UsageSessionSearchOutput{Items: []UsageSessionSearchGroup{}, Authority: AuthorityDataOnly}
	if in.Limit == 0 {
		in.Limit = 20
	}
	q := strings.TrimSpace(in.Query)
	if s.Repo == nil {
		return out, ErrUnavailable
	}
	if !validUsageSessionSearch(in, q) {
		return out, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colRouting, in.Scope, routingID(in.Scope), ""); err != nil {
		return out, err
	}
	if _, ok := s.Repo.(SessionArchiveStore); !ok {
		return out, nil // a store without archives has no dialogue to search
	}
	page, err := s.SearchSessionArchiveMessages(ctx, token, ArchiveSearchInput{Scope: in.Scope, Query: q, After: in.After, Limit: in.Limit})
	if err != nil {
		return out, err
	}
	out.Next, out.ScannedBytes, out.Secrets = page.Next, page.ScannedBytes, page.Secrets
	s.groupUsageSearchHits(ctx, in, q, page.Items, &out)
	return out, nil
}

func validUsageSessionSearch(in UsageSessionSearchInput, q string) bool {
	return validScope(in.Scope) && q != "" && q == in.Query && len(q) <= 256 && utf8.ValidString(q) && (in.Agent == "" || usageNameRe.MatchString(in.Agent)) &&
		len(in.Project) <= 200 && in.Limit >= 1 && in.Limit <= 50
}

// groupUsageSearchHits keeps the hits whose session is still listed and matches the filters, grouped by session.
func (s *Service) groupUsageSearchHits(ctx context.Context, in UsageSessionSearchInput, q string, hits []ArchiveMessageView, out *UsageSessionSearchOutput) {
	sessions, groups := map[string]*Record{}, map[string]int{}
	for _, it := range hits {
		row, ok := sessions[it.Ref.ArchiveID]
		if !ok {
			row = s.usageRowForArchive(ctx, in.Scope, it.Ref.ArchiveID)
			sessions[it.Ref.ArchiveID] = row
		}
		if row == nil || in.Agent != "" && row.UsageSession.Agent != in.Agent || in.Project != "" && row.UsageSession.Project != in.Project {
			continue
		}
		hit, ok := usageSearchSnippet(it.Role, it.Text, q)
		if !ok {
			out.Hidden++
			continue
		}
		g, seen := groups[row.ID]
		if !seen {
			g = len(out.Items)
			groups[row.ID] = g
			out.Items = append(out.Items, UsageSessionSearchGroup{Session: usageCatalogItem(*row), Hits: []UsageSessionSearchHit{}})
		}
		out.Items[g].Hits = append(out.Items[g].Hits, hit)
	}
}

// usageRowForArchive is the listed usage session an archive belongs to (same device, agent and
// session id), or nil when it is gone, quarantined or erased.
func (s *Service) usageRowForArchive(ctx context.Context, scope Scope, archiveID string) *Record {
	h, err := s.Repo.Get(ctx, colSessionArchives, scope, archiveID)
	if err != nil || !isSessionArchiveHeader(h) {
		return nil
	}
	a := h.SessionArchive
	r, err := s.Repo.Get(ctx, colUsageSessions, scope, usageSessionID(UsageSession{Device: a.Device, Agent: a.Origin, SessionID: a.SessionID}))
	if err != nil || r.UsageSession == nil || r.PrivacyQuarantined || r.ContentErased {
		return nil
	}
	return &r
}

// usageSearchSnippet cuts a short line around the words after <private> spans are masked;
// false when the words were only inside one.
func usageSearchSnippet(role, text, q string) (UsageSessionSearchHit, bool) {
	text = redactSessionPrivate(text)
	at := strings.Index(text, q)
	if at < 0 || containsSecret(text) {
		return UsageSessionSearchHit{}, false
	}
	begin, end := max(0, at-usageSearchSnippetBefore), min(len(text), at+len(q)+usageSearchSnippetAfter)
	for begin > 0 && !utf8.RuneStart(text[begin]) {
		begin--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}
	snippet, match := text[begin:end], at-begin
	trimmed := strings.TrimLeft(snippet, " \t\r\n")
	match -= len(snippet) - len(trimmed)
	snippet = strings.TrimRight(trimmed, " \t\r\n")
	if begin > 0 {
		snippet, match = "…"+snippet, match+len("…")
	}
	if end < len(text) {
		snippet += "…"
	}
	return UsageSessionSearchHit{Role: role, Snippet: snippet, Match: match}, true
}

type UsageSessionHandoffInput struct {
	Scope Scope  `json:"scope"`
	ID    string `json:"id"`
}

type UsageSessionHandoffWork struct {
	ID        string    `json:"id"`
	Revision  int64     `json:"revision"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UsageSessionHandoffOutput is what another agent can start from: the saved work state
// (resume_work) when one exists for this session, otherwise the live archive
// (search_session_archives with archive_id). Empty fields mean nothing to hand over.
type UsageSessionHandoffOutput struct {
	ID        string                   `json:"id"`
	Agent     string                   `json:"agent"`
	SessionID string                   `json:"session_id"`
	Device    string                   `json:"device"`
	Folder    string                   `json:"folder"`
	Work      *UsageSessionHandoffWork `json:"work,omitempty"`
	ArchiveID string                   `json:"archive_id,omitempty"`
	Authority string                   `json:"authority"`
}

// workCheckpointsForSession is the optional repository query for the newest work
// checkpoint a session saved.
type workCheckpointsForSession interface {
	WorkCheckpointsForSession(ctx context.Context, scope Scope, sessionID string, limit int) ([]Record, error)
}

func (p *Postgres) WorkCheckpointsForSession(ctx context.Context, scope Scope, sessionID string, limit int) ([]Record, error) {
	if checkScope(scope) != nil || sessionID == "" || limit < 1 || limit > recentWorkCandidateLimit {
		return nil, ErrInvalid
	}
	return p.selectRecords(ctx, colWorkCheckpoints, scope, `status='active' AND doc->'work_checkpoint'->'package'->>'session_id'=$6`,
		[]any{sessionID}, "updated_at DESC, id DESC", limit)
}

func (s *Service) UsageSessionHandoff(ctx context.Context, token string, in UsageSessionHandoffInput) (UsageSessionHandoffOutput, error) {
	if s.Repo == nil {
		return UsageSessionHandoffOutput{}, ErrUnavailable
	}
	if !validScope(in.Scope) || !sourceHashPattern.MatchString(in.ID) {
		return UsageSessionHandoffOutput{}, ErrInvalid
	}
	row, err := s.usageSessionRecord(ctx, token, in.Scope, in.ID)
	if err != nil {
		return UsageSessionHandoffOutput{}, err
	}
	u := row.UsageSession
	if row.PrivacyQuarantined || row.ContentErased {
		return UsageSessionHandoffOutput{}, ErrNotFound
	}
	out := UsageSessionHandoffOutput{ID: row.ID, Agent: u.Agent, SessionID: u.SessionID, Device: u.Device, Folder: u.Folder, Authority: AuthorityDataOnly}
	work, err := s.sessionWork(ctx, token, in.Scope, u.SessionID)
	if err != nil {
		return UsageSessionHandoffOutput{}, err
	}
	out.Work = work
	headers, err := s.sessionArchiveHeaders(ctx, in.Scope, u)
	if err != nil {
		return UsageSessionHandoffOutput{}, err
	}
	now := s.sessionArchiveNow()
	for i := len(headers) - 1; i >= 0; i-- { // the newest live archive
		if h := headers[i]; !h.ContentErased && sessionArchiveStateOf(h, now) == sessionArchiveStateActive && archiveSupported(h.SessionArchive.Format) {
			out.ArchiveID = h.ID
			break
		}
	}
	return out, nil
}

// sessionWork reads the newest checkpoint through the normal read gate; a caller who may
// not read work checkpoints just gets none.
func (s *Service) sessionWork(ctx context.Context, token string, scope Scope, sessionID string) (*UsageSessionHandoffWork, error) {
	var rows []Record
	var err error
	if q, ok := s.Repo.(workCheckpointsForSession); ok {
		rows, err = q.WorkCheckpointsForSession(ctx, scope, sessionID, 5)
	} else {
		var all []Record
		all, err = s.Repo.ListRecords(ctx, scope, colWorkCheckpoints, 1<<20)
		for _, r := range all {
			if r.Status == "active" && r.WorkCheckpoint != nil && r.WorkCheckpoint.Package != nil && r.WorkCheckpoint.Package.SessionID == sessionID {
				rows = append(rows, r)
			}
		}
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].UpdatedAt.After(rows[j].UpdatedAt) || rows[i].UpdatedAt.Equal(rows[j].UpdatedAt) && rows[i].ID > rows[j].ID
		})
	}
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		v, err := s.GetWorkCheckpoint(ctx, token, WorkCheckpointGetInput{Scope: scope, ID: r.ID})
		if errors.Is(err, ErrDenied) {
			return nil, nil
		}
		if err != nil || v.Package.SessionID != sessionID {
			continue // erased, expired or changed since: try the next one
		}
		return &UsageSessionHandoffWork{ID: v.ID, Revision: v.Revision, Title: v.Package.Title, Status: v.Package.Status, UpdatedAt: v.UpdatedAt}, nil
	}
	return nil, nil
}

func usageSessionFindRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/usage/sessions/search", func(w http.ResponseWriter, r *http.Request) {
		var in UsageSessionSearchInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.SearchUsageSessions(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/usage/sessions/handoff", func(w http.ResponseWriter, r *http.Request) {
		var in UsageSessionHandoffInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.UsageSessionHandoff(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}
