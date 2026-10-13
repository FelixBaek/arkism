package memory

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Unsplash searches photos for team and automation card backgrounds. The access
// key stays on the server; the desktop app only sees photo URLs and credits.
type Unsplash struct {
	Key  string
	Base string // default https://api.unsplash.com
	HTTP *http.Client
}

// UnsplashPhoto is one search hit, trimmed to what a card needs. Download is
// not sent back; the server pings it when a photo is picked (API guideline).
type UnsplashPhoto struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Thumb     string `json:"thumb"`
	Alt       string `json:"alt,omitempty"`
	Author    string `json:"author"`
	AuthorURL string `json:"author_url"`
}

type UnsplashPage struct {
	Items      []UnsplashPhoto `json:"items"`
	TotalPages int             `json:"total_pages"`
}

var unsplashIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// The referral tag Unsplash asks for on every author and Unsplash link.
const unsplashRef = "utm_source=arkism&utm_medium=referral"

func (u *Unsplash) get(ctx context.Context, path string, q url.Values, out any) error {
	base := u.Base
	if base == "" {
		base = "https://api.unsplash.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path+"?"+q.Encode(), nil)
	if err != nil {
		return ErrInvalid
	}
	req.Header.Set("Authorization", "Client-ID "+u.Key)
	req.Header.Set("Accept-Version", "v1")
	client := u.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ErrUnavailable
	}
	if out == nil {
		return nil
	}
	if json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(out) != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *Service) unsplashReady(ctx context.Context, token string, scope Scope) error {
	if !validScope(scope) {
		return ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colCrew, scope, "", ""); err != nil {
		return err
	}
	if s.Unsplash == nil || s.Unsplash.Key == "" {
		return ErrUnavailable
	}
	return nil
}

// SearchUnsplash returns one page (24) of landscape photos for query.
func (s *Service) SearchUnsplash(ctx context.Context, token string, scope Scope, query string, page int) (UnsplashPage, error) {
	query = strings.TrimSpace(query)
	if query == "" || runes(query) > 100 || page < 1 || page > 50 {
		return UnsplashPage{}, ErrInvalid
	}
	if err := s.unsplashReady(ctx, token, scope); err != nil {
		return UnsplashPage{}, err
	}
	var raw struct {
		TotalPages int `json:"total_pages"`
		Results    []struct {
			ID   string `json:"id"`
			Alt  string `json:"alt_description"`
			URLs struct {
				Regular string `json:"regular"`
				Small   string `json:"small"`
			} `json:"urls"`
			User struct {
				Name  string `json:"name"`
				Links struct {
					HTML string `json:"html"`
				} `json:"links"`
			} `json:"user"`
		} `json:"results"`
	}
	q := url.Values{"query": {query}, "page": {strconv.Itoa(page)}, "per_page": {"24"}, "orientation": {"landscape"}, "content_filter": {"high"}}
	if err := s.Unsplash.get(ctx, "/search/photos", q, &raw); err != nil {
		return UnsplashPage{}, err
	}
	out := UnsplashPage{Items: []UnsplashPhoto{}, TotalPages: raw.TotalPages}
	for _, r := range raw.Results {
		p := UnsplashPhoto{ID: r.ID, URL: r.URLs.Regular, Thumb: r.URLs.Small, Alt: r.Alt, Author: r.User.Name, AuthorURL: r.User.Links.HTML + "?" + unsplashRef}
		if validCrewCover(&CrewCover{ID: p.ID, URL: p.URL, Thumb: p.Thumb, Author: p.Author, AuthorURL: p.AuthorURL}) {
			out.Items = append(out.Items, p)
		}
	}
	return out, nil
}

// UseUnsplash tells Unsplash a photo was picked (their download-tracking rule).
func (s *Service) UseUnsplash(ctx context.Context, token string, scope Scope, id string) error {
	if !unsplashIDRe.MatchString(id) {
		return ErrInvalid
	}
	if err := s.unsplashReady(ctx, token, scope); err != nil {
		return err
	}
	return s.Unsplash.get(ctx, "/photos/"+id+"/download", url.Values{}, nil)
}

func unsplashRoutes(mux *http.ServeMux, s *Service) {
	mux.HandleFunc("GET /v1/unsplash/search", func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 0 {
			page = 1
		}
		out, e := s.SearchUnsplash(r.Context(), bearer(r), scopeQuery(r), r.URL.Query().Get("q"), page)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/unsplash/photos/{id}/use", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Scope Scope `json:"scope"`
		}
		if !decode(w, r, &in) {
			return
		}
		respond(w, map[string]bool{"ok": true}, s.UseUnsplash(r.Context(), bearer(r), in.Scope, r.PathValue("id")))
	})
}
