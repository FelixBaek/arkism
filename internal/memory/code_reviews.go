package memory

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Code reviews (코드 리뷰): what the CI review on the operator's repositories
// found, collected from GitHub by the host, and the
// owner's review settings, which the host writes where the CI script reads them.
// One document per scope; Arkism makes no GitHub call itself.
const colCodeReviews = "code_reviews"

var reviewProviders = []string{"codex", "claude"}
var reviewOnBlocking = []string{"notify", "block"}
var reviewSeverities = []string{"blocking", "advisory"}
var reviewStates = []string{"running", "done", "failed"}

type ReviewFinding struct {
	Severity string `json:"severity"` // blocking | advisory
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Text     string `json:"text"`
}

type ReviewStep struct {
	Name       string     `json:"name"` // runner | quality | ai | comment
	Status     string     `json:"status"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Note       string     `json:"note,omitempty"`
}

type ReviewItem struct {
	Repo       string          `json:"repo"`
	Number     int             `json:"number"`
	Title      string          `json:"title"`
	Branch     string          `json:"branch"`
	Base       string          `json:"base"`
	HeadSHA    string          `json:"head_sha"`
	Commits    int             `json:"commits"`
	URL        string          `json:"url"`
	State      string          `json:"state"` // running | done | failed
	PRState    string          `json:"pr_state,omitempty"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
	Quality    string          `json:"quality,omitempty"` // the quality job conclusion
	Model      string          `json:"model,omitempty"`   // "codex / gpt-6.1-sol"
	Findings   []ReviewFinding `json:"findings"`
	Steps      []ReviewStep    `json:"steps,omitempty"`
}

type ReviewRepo struct {
	Name        string     `json:"name"`
	PRs         int        `json:"prs"`
	LastChecked *time.Time `json:"last_checked,omitempty"`
}

type ReviewSettings struct {
	Provider   string   `json:"provider"`
	OnBlocking string   `json:"on_blocking"`
	ReposOff   []string `json:"repos_off"`
}

type ReviewRerun struct {
	ID          string     `json:"id"`
	Repo        string     `json:"repo"`
	Number      int        `json:"number"`
	Status      string     `json:"status"` // pending | done | failed
	Error       string     `json:"error,omitempty"`
	RequestedAt time.Time  `json:"requested_at"`
	DoneAt      *time.Time `json:"done_at,omitempty"`
}

type CodeReviews struct {
	CollectedAt *time.Time     `json:"collected_at,omitempty"`
	Repos       []ReviewRepo   `json:"repos"`
	Items       []ReviewItem   `json:"items"`
	Settings    ReviewSettings `json:"settings"`
	Reruns      []ReviewRerun  `json:"reruns"`
}

type CodeReviewsReport struct {
	Scope   Scope             `json:"scope"`
	Repos   []ReviewRepo      `json:"repos"`
	Items   []ReviewItem      `json:"items"`
	Results []AgentToolResult `json:"results"`
}

type CodeReviewSettingsInput struct {
	Scope    Scope          `json:"scope"`
	Settings ReviewSettings `json:"settings"`
}

type CodeReviewRerunInput struct {
	Scope  Scope  `json:"scope"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

func codeReviewsID(scope Scope) string { return "code-reviews-" + ContentHash(structuredText(scope)) }

func validRepoName(s string) bool {
	owner, name, ok := strings.Cut(s, "/")
	return ok && validRunLabel(owner, 64) && validRunLabel(name, 100) && !strings.ContainsAny(s, " ?#")
}

func validReviewSettings(s ReviewSettings) bool {
	if !slices.Contains(reviewProviders, s.Provider) || !slices.Contains(reviewOnBlocking, s.OnBlocking) || len(s.ReposOff) > 50 {
		return false
	}
	for _, r := range s.ReposOff {
		if !validRepoName(r) {
			return false
		}
	}
	return true
}

func validReviewItems(items []ReviewItem) bool {
	if len(items) > 300 {
		return false
	}
	for _, it := range items {
		if !validReviewItem(it) {
			return false
		}
	}
	return true
}

func validReviewItem(it ReviewItem) bool {
	if !validRepoName(it.Repo) || it.Number < 1 || runes(it.Title) > 300 || len(it.Branch) > 200 || len(it.Base) > 200 || len(it.HeadSHA) > 64 ||
		!strings.HasPrefix(it.URL, "https://github.com/") || len(it.URL) > 300 || !slices.Contains(reviewStates, it.State) ||
		len(it.Quality) > 30 || len(it.PRState) > 20 || runes(it.Model) > 80 || len(it.Findings) > 50 || len(it.Steps) > 8 || it.Commits < 0 {
		return false
	}
	for _, f := range it.Findings {
		if !slices.Contains(reviewSeverities, f.Severity) || len(f.Path) > 300 || f.Line < 0 || runes(f.Text) > 2000 {
			return false
		}
	}
	for _, st := range it.Steps {
		if !validRunLabel(st.Name, 40) || len(st.Status) > 30 || runes(st.Note) > 200 {
			return false
		}
	}
	return true
}

func (s *Service) CodeReviews(ctx context.Context, token string, scope Scope) (Record, error) {
	if !validScope(scope) {
		return Record{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colAgentTools, scope, codeReviewsID(scope), ""); err != nil {
		return Record{}, err
	}
	r, err := s.Repo.Get(ctx, colCodeReviews, scope, codeReviewsID(scope))
	if errors.Is(err, ErrNotFound) {
		return Record{ID: codeReviewsID(scope), Scope: scope, CodeReviews: &CodeReviews{Repos: []ReviewRepo{}, Items: []ReviewItem{}, Reruns: []ReviewRerun{},
			Settings: ReviewSettings{Provider: "codex", OnBlocking: "notify", ReposOff: []string{}}}}, nil
	}
	return r, err
}

func (s *Service) updateCodeReviews(ctx context.Context, scope Scope, change func(*CodeReviews, time.Time) error) (Record, error) {
	var out Record
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		now := time.Now().UTC().Truncate(time.Millisecond)
		id := codeReviewsID(scope)
		r, err := tx.Get(ctx, colCodeReviews, scope, id)
		fresh := errors.Is(err, ErrNotFound)
		if err != nil && !fresh {
			return err
		}
		if fresh {
			r = Record{ID: id, Scope: scope, Kind: colCodeReviews, Status: statusActive, Revision: 1, CreatedAt: now, Text: "code reviews"}
		}
		doc := CodeReviews{Repos: []ReviewRepo{}, Items: []ReviewItem{}, Reruns: []ReviewRerun{}, Settings: ReviewSettings{Provider: "codex", OnBlocking: "notify", ReposOff: []string{}}}
		if r.CodeReviews != nil {
			doc = *r.CodeReviews
			doc.Reruns = append([]ReviewRerun(nil), doc.Reruns...)
			doc.Settings.ReposOff = append([]string{}, doc.Settings.ReposOff...)
		}
		if err := change(&doc, now); err != nil {
			return err
		}
		r.CodeReviews, r.UpdatedAt = &doc, now
		out = r
		if fresh {
			return tx.Insert(ctx, colCodeReviews, r)
		}
		prev := r.Revision
		r.Revision++
		out = r
		return tx.Replace(ctx, colCodeReviews, r, prev)
	})
	return out, err
}

// ReportCodeReviews replaces what the host collected and settles the reruns it started.
func (s *Service) ReportCodeReviews(ctx context.Context, token string, in CodeReviewsReport) (Record, error) {
	if !validScope(in.Scope) || !validReviewItems(in.Items) || len(in.Repos) > 50 || len(in.Results) > 50 {
		return Record{}, ErrInvalid
	}
	for _, r := range in.Repos {
		if !validRepoName(r.Name) || r.PRs < 0 {
			return Record{}, ErrInvalid
		}
	}
	if err := noSecrets(in); err != nil {
		return Record{}, err
	}
	if _, err := s.authorize(ctx, token, "write:"+colAgentTools, in.Scope, codeReviewsID(in.Scope), ""); err != nil {
		return Record{}, err
	}
	return s.updateCodeReviews(ctx, in.Scope, func(doc *CodeReviews, now time.Time) error {
		doc.Repos, doc.Items, doc.CollectedAt = in.Repos, in.Items, &now
		settleReruns(doc.Reruns, in.Results, now)
		return nil
	})
}

// settleReruns marks the reruns the host started; one no longer pending is left alone.
func settleReruns(reruns []ReviewRerun, results []AgentToolResult, now time.Time) {
	done := map[string]AgentToolResult{}
	for _, res := range results {
		done[res.ID] = res
	}
	for i, q := range reruns {
		if res, ok := done[q.ID]; ok && q.Status == "pending" {
			reruns[i].Status, reruns[i].Error, reruns[i].DoneAt = res.Status, res.Error, &now
		}
	}
}

func (s *Service) humanCodeReviews(ctx context.Context, token string, scope Scope) error {
	d, err := s.authorize(ctx, token, "write:"+colAgentTools, scope, codeReviewsID(scope), "")
	if err != nil {
		return err
	}
	if d.Role != roleHuman {
		return ErrDenied
	}
	return nil
}

func (s *Service) SetCodeReviewSettings(ctx context.Context, token string, in CodeReviewSettingsInput) (Record, error) {
	if !validScope(in.Scope) || !validReviewSettings(in.Settings) {
		return Record{}, ErrInvalid
	}
	if err := s.humanCodeReviews(ctx, token, in.Scope); err != nil {
		return Record{}, err
	}
	return s.updateCodeReviews(ctx, in.Scope, func(doc *CodeReviews, _ time.Time) error {
		doc.Settings = in.Settings
		return nil
	})
}

func (s *Service) RerunCodeReview(ctx context.Context, token string, in CodeReviewRerunInput) (Record, error) {
	if !validScope(in.Scope) || !validRepoName(in.Repo) || in.Number < 1 {
		return Record{}, ErrInvalid
	}
	if err := s.humanCodeReviews(ctx, token, in.Scope); err != nil {
		return Record{}, err
	}
	return s.updateCodeReviews(ctx, in.Scope, func(doc *CodeReviews, now time.Time) error {
		doc.Reruns = append(doc.Reruns, ReviewRerun{ID: ID(), Repo: in.Repo, Number: in.Number, Status: "pending", RequestedAt: now})
		if over := len(doc.Reruns) - 30; over > 0 {
			doc.Reruns = doc.Reruns[over:]
		}
		return nil
	})
}

func codeReviewRoutes(mux *http.ServeMux, s *Service) {
	mux.HandleFunc("GET /v1/code_reviews", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.CodeReviews(r.Context(), bearer(r), scopeQuery(r))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/code_reviews", func(w http.ResponseWriter, r *http.Request) {
		var in CodeReviewsReport
		if !decodeLimit(w, r, &in, 4<<20) {
			return
		}
		out, e := s.ReportCodeReviews(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/code_reviews/settings", func(w http.ResponseWriter, r *http.Request) {
		var in CodeReviewSettingsInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.SetCodeReviewSettings(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/code_reviews/rerun", func(w http.ResponseWriter, r *http.Request) {
		var in CodeReviewRerunInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.RerunCodeReview(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}
