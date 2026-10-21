package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func exerciseSearchFilters(t *testing.T, repo Repository) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	f.svc.Embedder = nil
	src := f.source()
	start := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	end := start.Add(48 * time.Hour)
	// The span hash is the file digest a caller can filter by; it must match
	// the real source bytes or the evidence check refuses the hit. The noise
	// rows cite the first source, so good cites a second file of its own.
	secondSource := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "a different original message"}))
	fileDigest := ContentHash(secondSource.Text)
	good := Record{ID: "zz-match", Scope: scopeA, Kind: "decision", Tags: []string{"입시", "Qwen"}, Status: "active", ApprovedBy: "human", CreatedBy: "owner", Generator: &Generator{Model: "qwen-max", PromptVersion: "p1", PipelineVersion: "v1"}, Revision: 1, Text: "filterneedle", Keywords: []string{"filterneedle"}, Sources: []SourceRef{{SourceID: secondSource.ID, Locator: "span 0-" + fmt.Sprint(len(secondSource.Text)), Span: &SourceSpan{ContentHash: fileDigest, Start: 0, End: len(secondSource.Text)}}}, CreatedAt: start, UpdatedAt: start.Add(time.Hour), Trust: 1}
	for i := 0; i < 40; i++ {
		r := good
		r.ID = fmt.Sprintf("noise-%02d", i)
		r.Kind = "semantic"
		r.Tags = []string{"other"}
		r.CreatedBy = "agent-noise"
		r.Generator = &Generator{Model: "noise-model", PromptVersion: "p1", PipelineVersion: "v1"}
		r.Sources = refs(src)
		r.CreatedAt = start.Add(-time.Hour)
		r.UpdatedAt = end
		if err := repo.Insert(testCtx, colMemories, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.Insert(testCtx, colMemories, good); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"superseded", "candidate"} {
		r := good
		r.ID = "inactive-" + status
		r.Status = status
		r.CreatedBy = "agent-noise"
		r.Generator = nil
		r.Sources = refs(src)
		if err := repo.Insert(testCtx, colMemories, r); err != nil {
			t.Fatal(err)
		}
	}
	foreign := good
	foreign.ID = "foreign"
	foreign.Scope = scopeB
	foreign.CreatedBy = "agent-noise"
	foreign.Generator = nil
	foreign.Sources = refs(src)
	if err := repo.Insert(testCtx, colMemories, foreign); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"exact", "fts"} {
		for _, tc := range []struct {
			name  string
			c     SearchCriteria
			found bool
		}{
			{"kind", SearchCriteria{Kinds: []string{"decision", "unknown"}}, true},
			{"tag", SearchCriteria{Filters: &SearchFilters{Tags: []string{"입시", "missing"}}}, true},
			{"created", SearchCriteria{Filters: &SearchFilters{CreatedFrom: &start, CreatedTo: &end}}, true},
			{"updated", SearchCriteria{Filters: &SearchFilters{UpdatedFrom: &start, UpdatedTo: &end}}, true},
			{"combined", SearchCriteria{Kinds: []string{"decision"}, Filters: &SearchFilters{Tags: []string{"Qwen"}, CreatedFrom: &start, CreatedTo: &end, UpdatedFrom: &start, UpdatedTo: &end}}, true},
			{"unknown kind", SearchCriteria{Kinds: []string{"unrecognized"}}, false},
			{"case", SearchCriteria{Filters: &SearchFilters{Tags: []string{"qwen"}}}, false},
			{"exclusive end", SearchCriteria{Kinds: []string{"decision"}, Filters: &SearchFilters{CreatedTo: &start}}, false},
			{"author", SearchCriteria{Filters: &SearchFilters{CreatedBy: []string{"owner", "someone-else"}}}, true},
			{"author mismatch", SearchCriteria{Filters: &SearchFilters{CreatedBy: []string{"agent-9"}}}, false},
			{"model", SearchCriteria{Filters: &SearchFilters{GeneratorModels: []string{"qwen-max"}}}, true},
			{"model mismatch", SearchCriteria{Filters: &SearchFilters{GeneratorModels: []string{"gemini-x"}}}, false},
			{"file by digest", SearchCriteria{Filters: &SearchFilters{FileDigests: []string{fileDigest}}}, true},
			{"file mismatch", SearchCriteria{Filters: &SearchFilters{FileDigests: []string{ContentHash("other file")}}}, false},
			{"identity combined", SearchCriteria{Filters: &SearchFilters{CreatedBy: []string{"owner"}, GeneratorModels: []string{"qwen-max"}, FileDigests: []string{fileDigest}}}, true},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				in := SearchInput{SearchCriteria: tc.c, Scope: scopeA, Query: "filterneedle", Limit: 1, Modes: []string{mode}, BudgetTokens: 2000, TokenEncoding: "cl100k_base"}
				req := httptest.NewRequest("POST", "/v1/search", strings.NewReader(structuredText(in)))
				req.Header.Set("Authorization", "Bearer "+f.human)
				w := httptest.NewRecorder()
				Handler(f.svc).ServeHTTP(w, req)
				var out ContextBundle
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
					t.Fatal(w.Code, w.Body.String())
				}
				if tc.found {
					if len(out.Items) != 1 || out.Items[0].ID != good.ID || out.Items[0].CreatedAt == nil || !out.Items[0].CreatedAt.Equal(start) || !strings.Contains(out.ContextData, good.ID) {
						t.Fatal("prefilter lost match behind 40 nonmatches", out)
					}
				} else if len(out.Items) != 0 {
					t.Fatal("filter mismatch returned", out)
				}
			})
		}
	}
}
func TestSearchFilters(t *testing.T) { exerciseSearchFilters(t, newFakeRepo()) }

func TestSearchFilterValidation(t *testing.T) {
	f := newFixture(t)
	called := false
	f.svc.Auth = requestActorAuth{Authorizer: f.auth, payloadOnly: true, change: func(a Authorization, d *Decision) error { called = true; return nil }}
	tooManyNames := make([]string, 17)
	for i := range tooManyNames {
		tooManyNames[i] = fmt.Sprintf("principal-%02d", i)
	}
	tooManyNamesJSON := `"filters":{"created_by":[` + strings.Join(tooManyNames, ",") + `]}`
	for _, fields := range []string{`"kinds":["x","x"]`, `"kinds":[""]`, `"kinds":[" x"]`, `"kinds":["password=synthetic-secret-value"]`, `"filters":{"tags":["x","x"]}`, `"filters":{"tags":["password=synthetic-secret-value"]}`, `"filters":{"unknown":true}`, `"filters":{"created_from":"2025-01-02T00:00:00Z","created_to":"2025-01-01T00:00:00Z"}`, `"filters":{"updated_from":"0001-01-01T00:00:00Z"}`, `"filters":{"created_from":"bad"}`, `"filters":{"created_by":["owner","owner"]}`, `"filters":{"created_by":["password=synthetic-secret-value"]}`, `"filters":{"created_by":[""]}`, `"filters":{"generator_models":[" x"]}`, `"filters":{"file_digests":["not-a-digest"]}`, tooManyNamesJSON} {
		req := httptest.NewRequest("POST", "/v1/search", strings.NewReader(`{"scope":`+structuredText(scopeA)+`,"query":"needle",`+fields+`}`))
		req.Header.Set("Authorization", "Bearer "+f.human)
		w := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(w, req)
		if w.Code != 400 || called || len(f.emb.calls) > 0 {
			t.Fatal("invalid filter reached policy/providers", fields, w.Code)
		}
	}
	empty, err := normalizeSearchCriteria(SearchCriteria{Kinds: []string{}, Filters: &SearchFilters{Tags: []string{}}})
	if err != nil || structuredText(empty) != "{}" {
		t.Fatal("empty filters changed the normalized payload", empty, err)
	}
	emptyNew, err := normalizeSearchCriteria(SearchCriteria{Filters: &SearchFilters{CreatedBy: []string{}, GeneratorModels: []string{}, FileDigests: []string{}}})
	if err != nil || emptyNew.Filters != nil {
		t.Fatal("empty identity filters must collapse", emptyNew, err)
	}
	at := time.Date(2025, 1, 2, 9, 0, 0, 123456789, time.FixedZone("KST", 9*3600))
	normalized, err := normalizeSearchCriteria(SearchCriteria{Filters: &SearchFilters{CreatedFrom: &at}})
	if err != nil || normalized.Filters.CreatedFrom.Location() != time.UTC || normalized.Filters.CreatedFrom.Nanosecond() != 123000000 || at.Nanosecond() != 123456789 {
		t.Fatal("date normalization", normalized, err)
	}
}

func TestSearchFilterFinalFence(t *testing.T) {
	for _, field := range []string{"kind", "tags", "created", "updated"} {
		t.Run(field, func(t *testing.T) {
			f := newFixture(t)
			r := f.activeMemory("filterfence")
			r.Tags = []string{"allowed"}
			f.repo.mu.Lock()
			f.repo.store[colMemories][recKey(scopeA, r.ID)] = r
			f.repo.mu.Unlock()
			from := r.CreatedAt.Add(-time.Hour)
			to := r.UpdatedAt.Add(time.Hour)
			criteria := SearchCriteria{Kinds: []string{r.Kind}, Filters: &SearchFilters{Tags: []string{"allowed"}, CreatedFrom: &from, CreatedTo: &to, UpdatedFrom: &from, UpdatedTo: &to}}
			f.svc.Auth = requestActorAuth{Authorizer: f.auth, change: func(a Authorization, d *Decision) error {
				if a.Action == "context:read" && a.ResourceID == r.ID {
					f.repo.mu.Lock()
					current := f.repo.store[colMemories][recKey(scopeA, r.ID)]
					switch field {
					case "kind":
						current.Kind = "unknown"
					case "tags":
						current.Tags = nil
					case "created":
						current.CreatedAt = to
					case "updated":
						current.UpdatedAt = to
					}
					f.repo.store[colMemories][recKey(scopeA, r.ID)] = current
					f.repo.mu.Unlock()
				}
				return nil
			}}
			out, err := f.svc.Search(testCtx, f.human, SearchInput{SearchCriteria: criteria, Scope: scopeA, Query: "filterfence", Modes: []string{"fts"}})
			if !errors.Is(err, ErrConflict) || len(out.Items) > 0 || out.ContextID != "" {
				t.Fatal("stale filtered context returned", out, err)
			}
		})
	}
}

// The date/tag predicates also remain ordinary BSON values, never raw query JSON.

func TestSearchFilterPolicyIncludesPrivateCriteria(t *testing.T) {
	f := newFixture(t)
	email := "person@example.test"
	seen := map[string]bool{}
	f.svc.Auth = requestActorAuth{Authorizer: f.auth, change: func(a Authorization, d *Decision) error {
		if strings.HasPrefix(a.Action, "search:") {
			seen[a.Action] = strings.Contains(a.Content, email)
		}
		return nil
	}}
	in := SearchInput{SearchCriteria: SearchCriteria{Filters: &SearchFilters{Tags: []string{email}}}, Scope: scopeA, Query: "ordinary", Purpose: "knowledge"}
	out, err := f.svc.Search(testCtx, f.human, in)
	if err != nil || !seen["search:memories"] || len(f.emb.calls) > 0 {
		t.Fatal("private filter bypassed policy/transfer guard", out, err)
	}
}

func TestSearchFiltersRoute(t *testing.T) {
	for _, phase := range []string{"normal", "excluded", "judge", "final_policy"} {
		t.Run(phase, func(t *testing.T) {
			f := newFixture(t)
			v := analyzedSkill(t, f)
			mutate := func() {
				f.repo.mu.Lock()
				defer f.repo.mu.Unlock()
				r := f.repo.store[colSkillVersions][recKey(scopeA, v.ID)]
				r.Kind = "changed"
				f.repo.store[colSkillVersions][recKey(scopeA, v.ID)] = r
			}
			if phase == "judge" {
				f.svc.Router = judgeFunc(func(_ context.Context, _ string, c []SkillCandidate) (RouteResult, error) {
					mutate()
					return RouteResult{SelectedID: c[0].ID, Provider: "test", Judgments: []SkillJudgment{{ID: c[0].ID, Suitability: 3, Confidence: 1}}}, nil
				})
			}
			if phase == "final_policy" {
				reads := 0
				f.svc.Auth = requestActorAuth{Authorizer: f.auth, change: func(a Authorization, d *Decision) error {
					if a.Action == "context:read" {
						reads++
						if reads == 2 {
							mutate()
						}
					}
					return nil
				}}
			}
			kind := v.Kind
			if phase == "excluded" {
				kind = "absent"
			}
			in := SearchInput{SearchCriteria: SearchCriteria{Kinds: []string{kind}}, Scope: scopeA, Query: "index", Modes: []string{"fts"}}
			req := httptest.NewRequest("POST", "/v1/skills/route", strings.NewReader(structuredText(in)))
			req.Header.Set("Authorization", "Bearer "+f.human)
			w := httptest.NewRecorder()
			Handler(f.svc).ServeHTTP(w, req)
			if phase == "judge" || phase == "final_policy" {
				if w.Code != 409 || strings.Contains(w.Body.String(), v.ID) {
					t.Fatal("stale filtered route", w.Code, w.Body.String())
				}
				return
			}
			var out RouteResult
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
				t.Fatal(w.Code, w.Body.String())
			}
			if (phase == "normal" && out.SelectedID != v.ID) || (phase == "excluded" && out.SelectedID != "") {
				t.Fatal("route ignored filter", out)
			}
		})
	}
}

type searchFilterStaleRepo struct {
	Repository
	lists [][]Hit
}

func (r searchFilterStaleRepo) SearchCollection(context.Context, string, SearchInput, []float64, string) ([][]Hit, error) {
	return r.lists, nil
}
func TestSearchFiltersRecheckStaleLanes(t *testing.T) {
	for lane, mode := range []string{"exact", "fts", "vector"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			good := f.activeMemory("stale filter")
			good.Tags = []string{"allowed"}
			f.repo.mu.Lock()
			f.repo.store[colMemories][recKey(scopeA, good.ID)] = good
			f.repo.mu.Unlock()
			wrong := good
			wrong.ID = "wrong-filter"
			wrong.Tags = []string{"other"}
			expired := good
			expired.ID = "expired"
			past := time.Now().Add(-time.Hour)
			expired.ExpiresAt = &past
			for _, r := range []Record{wrong, expired} {
				if err := f.repo.Insert(testCtx, colMemories, r); err != nil {
					t.Fatal(err)
				}
			}
			stale := HitFromRecord(colMemories, wrong)
			stale.Tags = []string{"allowed"}
			lists := make([][]Hit, 3)
			lists[lane] = []Hit{stale, HitFromRecord(colMemories, expired), HitFromRecord(colMemories, good)}
			f.svc.Repo = searchFilterStaleRepo{f.repo, lists}
			out, err := f.svc.Search(testCtx, f.human, SearchInput{SearchCriteria: SearchCriteria{Filters: &SearchFilters{Tags: []string{"allowed"}}}, Scope: scopeA, Query: "stale", Modes: []string{mode}})
			if err != nil || len(out.Items) != 1 || out.Items[0].ID != good.ID {
				t.Fatal("stale lane trusted", out, err)
			}
		})
	}
}
