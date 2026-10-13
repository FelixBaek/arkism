package memory

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeUnsplash answers search and download pings; the second hit lives off images.unsplash.com.
func fakeUnsplash(auth, hit *string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*auth, *hit = r.Header.Get("Authorization"), r.URL.Path
		if r.URL.Path == "/photos/abc_1/download" {
			w.Write([]byte(`{"url":"x"}`))
			return
		}
		w.Write([]byte(`{"total_pages":3,"results":[
			{"id":"abc_1","alt_description":"code","urls":{"regular":"https://images.unsplash.com/photo-1?w=1080","small":"https://images.unsplash.com/photo-1?w=400"},"user":{"name":"Kim","links":{"html":"https://unsplash.com/@kim"}}},
			{"id":"evil","urls":{"regular":"https://evil.example/p.jpg","small":"https://evil.example/p.jpg"},"user":{"name":"X","links":{"html":"https://unsplash.com/@x"}}}]}`))
	}))
}

// card background photos come from Unsplash through the server.
func TestUnsplashSearchAndUse(t *testing.T) {
	var auth, hit string
	api := fakeUnsplash(&auth, &hit)
	defer api.Close()
	f := newFixture(t)
	if _, err := f.svc.SearchUnsplash(testCtx, f.human, scopeA, "code", 1); !errors.Is(err, ErrUnavailable) {
		t.Fatal("search without a key", err)
	}
	f.svc.Unsplash = &Unsplash{Key: "k", Base: api.URL}
	page, err := f.svc.SearchUnsplash(testCtx, f.human, scopeA, "code", 1)
	if err != nil || len(page.Items) != 1 || page.TotalPages != 3 || auth != "Client-ID k" || hit != "/search/photos" {
		t.Fatal("search", page, err, auth, hit)
	}
	if p := page.Items[0]; p.Thumb != "https://images.unsplash.com/photo-1?w=400" || !strings.HasSuffix(p.AuthorURL, "utm_medium=referral") {
		t.Fatal("photo", p)
	}
	if _, err = f.svc.SearchUnsplash(testCtx, f.human, scopeA, " ", 1); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty query", err)
	}
	if err = f.svc.UseUnsplash(testCtx, f.human, scopeA, "abc_1"); err != nil || hit != "/photos/abc_1/download" {
		t.Fatal("use", err, hit)
	}
	if err = f.svc.UseUnsplash(testCtx, f.human, scopeA, "../x"); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad id", err)
	}
}

// only workflows carry a cover, and only from images.unsplash.com.
func TestCrewCover(t *testing.T) {
	f := newFixture(t)
	cover := &CrewCover{ID: "abc_1", URL: "https://images.unsplash.com/photo-1?w=1080", Thumb: "https://images.unsplash.com/photo-1?w=400",
		Author: "Kim", AuthorURL: "https://unsplash.com/@kim?" + unsplashRef}
	team := crewWorkflow("백엔드팀", "백엔드팀")
	team.Cover = cover
	if r := f.crew(team); r.Crew.Cover == nil || r.Crew.Cover.URL != cover.URL {
		t.Fatal("workflow cover not kept", r.Crew)
	}
	ex := crewTemplate("작가")
	ex.Cover = cover
	if _, err := f.svc.CreateCrew(testCtx, f.human, CrewInput{Scope: scopeA, Crew: ex}); !errors.Is(err, ErrInvalid) {
		t.Fatal("a template took a cover", err)
	}
	off := *cover
	off.URL = "https://evil.example/p.jpg"
	team.Handle, team.Cover = "다른팀", &off
	if _, err := f.svc.CreateCrew(testCtx, f.human, CrewInput{Scope: scopeA, Crew: team}); !errors.Is(err, ErrInvalid) {
		t.Fatal("a cover off images.unsplash.com", err)
	}
}
