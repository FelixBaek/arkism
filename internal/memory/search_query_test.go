package memory

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchQueryCharacterBounds(t *testing.T) {
	for _, route := range []string{"/v1/search", "/v1/skills/route"} {
		for _, tc := range []struct {
			name, query string
			valid       bool
		}{
			{"one", "한", true},
			{"ASCII1000", strings.Repeat("a", 1000), true},
			{"ASCII1001", strings.Repeat("a", 1001), false},
			{"Korean1000", strings.Repeat("한", 1000), true},
			{"Korean1001", strings.Repeat("한", 1001), false},
			{"emoji1000", strings.Repeat("😀", 1000), true},
			{"emoji1001", strings.Repeat("😀", 1001), false},
			{"combining1000", strings.Repeat("e\u0301", 500), true},
			{"combining1002", strings.Repeat("e\u0301", 501), false},
			{"whitespace preserved", "  한 문장\n\t", true},
			{"spaces count", strings.Repeat(" ", 1000) + "한", false},
			{"blank", " \t\n\u3000", false},
			{"empty", "", false},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				f := newFixture(t)
				called := false
				content := ""
				f.svc.Auth = requestActorAuth{Authorizer: f.auth, payloadOnly: true, change: func(a Authorization, d *Decision) error {
					called = true
					if strings.HasPrefix(a.Action, "search:") {
						content = a.Content
					}
					return nil
				}}
				in := SearchInput{Scope: scopeA, Query: tc.query, Modes: []string{"fts"}}
				req := httptest.NewRequest("POST", route, strings.NewReader(structuredText(in)))
				req.Header.Set("Authorization", "Bearer "+f.human)
				w := httptest.NewRecorder()
				Handler(f.svc).ServeHTTP(w, req)
				if tc.valid {
					if w.Code != 200 || !called || content != tc.query {
						t.Fatalf("valid query status=%d, policy=%v, preserved=%v", w.Code, called, content == tc.query)
					}
				} else if w.Code != 400 || called || len(f.repo.rows("jobs")) != 0 {
					t.Fatalf("invalid query status=%d, policy=%v", w.Code, called)
				}
				if len(f.emb.calls) != 0 {
					t.Fatal("unselected provider called")
				}
			})
		}
	}
}
func TestSearchQueryInvalidUTF8(t *testing.T) {
	f := newFixture(t)
	called := false
	f.svc.Auth = requestActorAuth{Authorizer: f.auth, payloadOnly: true, change: func(a Authorization, d *Decision) error { called = true; return nil }}
	for _, route := range []bool{false, true} {
		in := SearchInput{Scope: scopeA, Query: string([]byte{255}), Modes: []string{"fts"}}
		var err error
		if route {
			_, err = f.svc.RouteSkills(testCtx, f.human, in)
		} else {
			_, err = f.svc.Search(testCtx, f.human, in)
		}
		if !errors.Is(err, ErrInvalid) || called || len(f.repo.rows("jobs")) != 0 {
			t.Fatal("invalid UTF8 reached policy or storage", err)
		}
	}
}
