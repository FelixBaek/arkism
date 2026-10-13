package memory

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSourceOriginRequiredAndImmutable(t *testing.T) {
	f := newFixture(t)
	at := time.Date(2026, 9, 1, 3, 0, 0, 123456789, time.UTC)
	good := CreateInput{Kind: "document", Scope: scopeA, Text: "attributed source", OriginProduct: "note", SourceRef: "post/123", CollectedAt: &at}
	for _, mutate := range []func(*CreateInput){
		func(in *CreateInput) { in.OriginProduct = "" },
		func(in *CreateInput) { in.OriginProduct = "Note" },
		func(in *CreateInput) { in.SourceRef = "" },
		func(in *CreateInput) { in.SourceRef = "post/123\n" },
		func(in *CreateInput) { in.CollectedAt = nil },
		func(in *CreateInput) { future := time.Now().Add(time.Hour); in.CollectedAt = &future },
	} {
		in := good
		mutate(&in)
		if _, e := f.svc.Create(testCtx, f.human, "sources", in); !errors.Is(e, ErrInvalid) {
			t.Fatal("invalid source provenance accepted", e)
		}
	}
	first, e := f.svc.Create(testCtx, f.human, "sources", good)
	if e != nil || first.OriginProduct != "note" || first.SourceRef != "post/123" || !first.CollectedAt.Equal(at.Truncate(time.Millisecond)) {
		t.Fatal("provenance lost", e)
	}
	replay := good
	otherZone := at.In(time.FixedZone("KST", 9*60*60))
	replay.CollectedAt = &otherZone
	if again, e := f.svc.Create(testCtx, f.human, "sources", replay); e != nil || again.ID != first.ID || !again.Duplicate {
		t.Fatal("equivalent source timestamp rejected", e)
	}
	for _, mutate := range []func(*CreateInput){
		func(in *CreateInput) { in.OriginProduct = "demo-app" },
		func(in *CreateInput) { in.SourceRef = "post/456" },
	} {
		in := good
		mutate(&in)
		if _, e := f.svc.Create(testCtx, f.human, "sources", in); !errors.Is(e, ErrConflict) {
			t.Fatal("dedup changed provenance", e)
		}
	}
	recrawl := good
	later := at.Add(time.Second)
	recrawl.CollectedAt = &later
	if again, e := f.svc.Create(testCtx, f.human, "sources", recrawl); e != nil || !again.Duplicate || again.ID != first.ID || !again.CollectedAt.Equal(*first.CollectedAt) {
		t.Fatal("recrawl changed first provenance", e)
	}
	if _, e := f.svc.Create(testCtx, f.human, "memory_candidates", candidateInput(good)); !errors.Is(e, ErrInvalid) {
		t.Fatal("source metadata accepted on candidate", e)
	}
	good.SourceRef = syntheticSecrets[0]
	good.OnSecret = "redact"
	if _, e := f.svc.Create(testCtx, f.human, "sources", good); !errors.Is(e, ErrSecretDetected) {
		t.Fatal("source reference secret accepted", e)
	}
}

func TestAnalyzedSkillSourceOrigin(t *testing.T) {
	f := newFixture(t)
	r := analyzedSkill(t, f)
	src := f.get(f.human, "sources", scopeA, r.Sources[0].SourceID)
	if src.OriginProduct != "brain" || src.SourceRef != r.Sources[0].Locator || src.CollectedAt == nil || src.CollectedAt.IsZero() {
		t.Fatal("skill source attribution missing")
	}
}

func TestSourceOriginHTTPContract(t *testing.T) {
	f := newFixture(t)
	valid := sourceInput(CreateInput{Scope: scopeA, Text: "http source"})
	for _, test := range []struct {
		in     CreateInput
		status int
	}{
		{CreateInput{Scope: scopeA, Text: "missing attribution"}, 400},
		{valid, 201},
	} {
		req := httptest.NewRequest("POST", "/v1/sources", strings.NewReader(structuredText(test.in)))
		req.Header.Set("Authorization", "Bearer "+f.human)
		out := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(out, req)
		if out.Code != test.status {
			t.Fatalf("source HTTP status %d want %d", out.Code, test.status)
		}
	}
}
