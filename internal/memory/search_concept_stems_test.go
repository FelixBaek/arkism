package memory

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSearchConceptStemIsIndependentOfCompoundParts(t *testing.T) {
	for _, tc := range []struct{ query, stem string }{
		{"선생님께서", "선생님"},
		{"동료한테서", "동료"},
		{"담당자에게서", "담당자"},
		{"자료집이랑", "자료집"},
		{"도서관마다", "도서관"},
	} {
		terms := searchQueryTerms(tc.query)
		concepts := searchQueryConcepts(terms)
		if !slices.Contains(terms, tc.stem) || len(concepts) != 1 || !strings.HasSuffix(concepts[0], " | "+tsQuote(tc.stem)+")") {
			t.Errorf("%q: stem must be a standalone alternative, not only part of a compound: %v", tc.query, concepts)
		}
	}
	for _, terms := range [][]string{{"deploy", "deploys", "deploy"}, {"deploys", "deploy", "deploys"}} {
		concepts := searchQueryConcepts(terms)
		if len(concepts) != 1 || strings.Count(concepts[0], "'deploy'") != 1 || strings.Count(concepts[0], "'deploys'") != 1 {
			t.Fatalf("standalone aliases duplicated: %v", concepts)
		}
	}
}

func TestPostgresParticleStemsPreserveCompoundEvidence(t *testing.T) {
	p := isolatedPostgres(t)
	scope := scopeA
	scope.ProjectID = "particle-stem-concepts"
	now := time.Now().UTC()
	for id, text := range map[string]string{
		"stem-topic":    "도서관 운영",
		"prefix-only":   "도서 운영",
		"compound-only": "도서관 관리",
	} {
		r := Record{ID: id, Scope: scope, Kind: "fact", Status: statusActive, Revision: 1, Text: text, ApprovedBy: "reviewer", CreatedAt: now, UpdatedAt: now}
		if err := p.Insert(testCtx, colMemories, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"도서관마다 운영 안내", []string{"stem-topic"}},
		{"도서관관리 서버 배포", nil},
	} {
		lanes, err := p.Search(testCtx, scope, tc.query, nil, "", 10, "fts")
		if err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, h := range Fuse(lanes, 10) {
			got = append(got, h.ID)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%q: want %v, got %v", tc.query, tc.want, got)
		}
	}
}
