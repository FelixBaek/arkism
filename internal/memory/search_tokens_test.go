package memory

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSearchTokensNormalizesKoreanParticles(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"서버를 배포할 때는 태그를 먼저 확인한다", []string{"서버", "배포", "때는", "태그", "먼저", "확인"}},
		{"커밋은 서버에서는 데이터베이스의", []string{"커밋", "서버", "데이터베이스"}},
		{"사용자에게 모델이 정보가 결과를", []string{"사용자", "모델", "정보", "결과"}},
		// Stripping never leaves a single rune: 회의, 결과, 아이 stay whole.
		{"회의 결과 아이가", []string{"회의", "결과", "아이"}},
		// A particle glued to a Latin word is dropped; the Hangul tail of a mixed word splits off.
		{"PR을 Docker컨테이너를 main에서", []string{"pr", "docker", "컨테이너", "main"}},
		{"Deploy the API-server, v2 (go1.22) a I", []string{"deploy", "the", "api", "server", "v2", "go1", "22"}},
		{"  ...  ", []string{}},
	}
	for _, c := range cases {
		if got := SearchTokens(c.in); !slices.Equal(got, c.want) {
			t.Errorf("SearchTokens(%q)=%q, want %q", c.in, got, c.want)
		}
	}
}

func TestSearchTokensQuerySideMatchesDocumentSide(t *testing.T) {
	doc := SearchTokens("서버를 배포할 때는 태그를 먼저 확인한다")
	for _, q := range []string{"서버 배포", "서버가 배포", "태그는"} {
		for _, term := range searchQueryTerms(q) {
			if !slices.Contains(doc, term) {
				t.Errorf("query %q term %q not in %q", q, term, doc)
			}
		}
	}
	if got := searchQueryTerms("서버 서버를 서버에서"); !slices.Equal(got, []string{"서버"}) {
		t.Fatalf("query terms not distinct: %q", got)
	}
}

func TestSearchTokensLiterals(t *testing.T) {
	if got := searchTokensVector("서버를 배포 서버"); got != "'서버':1 '배포':2 '서버':3" {
		t.Fatalf("vector literal=%q", got)
	}
	if got := searchTermsQuery([]string{"서버", "배포"}); got != "'서버' | '배포'" {
		t.Fatalf("query literal=%q", got)
	}
	if got := tsQuote(`a'b\c`); got != `'a''b\\c'` {
		t.Fatalf("quote=%q", got)
	}
	if pgSearchTokens(colJobs, Record{Text: "x"}) != nil || pgSearchTokens(colMemories, Record{}) != "" {
		t.Fatal("search_tokens must be NULL outside searched collections and empty for empty text")
	}
	long := strings.Repeat("단어 ", searchTokenMaxPerDoc+10)
	if n := strings.Count(searchTokensVector(long), "'단어'"); n != searchTokenMaxPerDoc {
		t.Fatalf("tokens per document=%d", n)
	}
}

func TestPostgresSearchKoreanParticlesIntegration(t *testing.T) {
	ctx := context.Background()
	p := isolatedPostgres(t)
	scope := Scope{WorkspaceID: "ko-" + ID(), ProjectID: "search", UserID: "u", AgentID: "a"}
	now := time.Now().UTC()
	insert := func(text string) Record {
		t.Helper()
		r := Record{ID: ID(), Scope: scope, Kind: "memory", Status: statusActive, Revision: 1, Text: text, ApprovedBy: "reviewer", CreatedAt: now, UpdatedAt: now}
		if err := p.Insert(ctx, colMemories, r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	fts := func(query string) []Hit {
		t.Helper()
		lanes, err := p.Search(ctx, scope, query, nil, "", 10, "fts")
		if err != nil {
			t.Fatal(err)
		}
		return lanes[1]
	}
	ko := insert("서버를 배포할 때는 태그를 먼저 확인한다")
	full := insert("deploy the release server after tagging")
	partial := insert("the release notes mention nothing else")
	other := insert("unrelated cooking recipe")

	if hits := fts("서버 배포"); len(hits) != 1 || hits[0].ID != ko.ID {
		t.Fatalf("서버 배포 hits=%v", hits)
	}
	// An extra word no longer empties the lane (the old AND did).
	if hits := fts("서버 배포 롤백"); !hasPostgresHit(hits, ko.ID) {
		t.Fatalf("extra-word hits=%v", hits)
	}
	// English: the row with every term ranks first; partial matches follow; misses stay out.
	hits := fts("release server deploy")
	if len(hits) != 2 || hits[0].ID != full.ID || hits[1].ID != partial.ID || hasPostgresHit(hits, other.ID) {
		t.Fatalf("english hits=%v", hits)
	}
	if hits := fts("server"); len(hits) != 1 || hits[0].ID != full.ID {
		t.Fatalf("single english term hits=%v", hits)
	}
	if hits := fts("?? !"); len(hits) != 0 {
		t.Fatalf("punctuation query hits=%v", hits)
	}

	// Rows written before the column existed: found by the legacy expression,
	// then by normalized tokens once backfilled.
	legacy := insert("커밋은 리뷰가 끝난 뒤에 푸시한다")
	if _, err := p.pool.Exec(ctx, `UPDATE memory_records SET search_tokens=NULL WHERE workspace_id=$1`, scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if hits := fts("release server"); !hasPostgresHit(hits, full.ID) {
		t.Fatalf("unbackfilled english row not found: %v", hits)
	}
	if hits := fts("커밋 푸시"); hasPostgresHit(hits, legacy.ID) {
		t.Fatalf("legacy expression unexpectedly stripped particles: %v", hits)
	}
	filled, more, err := p.BackfillSearchTokens(ctx, time.Minute)
	if err != nil || more || filled < 5 {
		t.Fatalf("backfill filled=%d more=%v err=%v", filled, more, err)
	}
	if hits := fts("커밋 푸시"); len(hits) != 1 || hits[0].ID != legacy.ID {
		t.Fatalf("backfilled hits=%v", hits)
	}
	if filled, more, err = p.BackfillSearchTokens(ctx, time.Minute); err != nil || more || filled != 0 {
		t.Fatalf("second backfill filled=%d more=%v err=%v", filled, more, err)
	}

	// Replace rewrites the tokens: the old text stops matching.
	changed, err := p.Get(ctx, colMemories, scope, ko.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed.Text, changed.Keywords = "로그를 확인한다", nil
	changed.Revision++
	if err := p.Replace(ctx, colMemories, changed, changed.Revision-1); err != nil {
		t.Fatal(err)
	}
	if hits := fts("서버 배포"); hasPostgresHit(hits, ko.ID) {
		t.Fatalf("replaced text still matched: %v", hits)
	}
	if hits := fts("로그"); len(hits) != 1 || hits[0].ID != ko.ID {
		t.Fatalf("replaced text hits=%v", hits)
	}
}
