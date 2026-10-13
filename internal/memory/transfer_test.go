package memory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestExternalContextSensitivity(t *testing.T) {
	for _, level := range []string{"public", "internal", "private", "restricted", "", "unknown"} {
		t.Run(level, func(t *testing.T) {
			f := newFixture(t)
			r := f.activeMemory("context boundary")
			r.Sensitivity = level
			for _, source := range f.repo.rows("sources") {
				source.Sensitivity = "public"
				f.repo.store["sources"][recKey(source.Scope, source.ID)] = source
			}
			f.repo.store["memories"][recKey(r.Scope, r.ID)] = r
			internal, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "context", Destination: "internal"})
			if e != nil || len(internal.Items) != 1 || internal.Items[0].Sensitivity != level {
				t.Fatal("internal result lost sensitivity", e)
			}
			external, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "context", Destination: "external:provider"})
			want := 1
			if level == "restricted" || level == "" || level == "unknown" {
				want = 0
			}
			if e != nil || len(external.Items) != want {
				t.Fatal("external classification not enforced", level, e)
			}
			if want == 0 && !strings.Contains(structuredText(external.Degraded), "external_content_filtered") {
				t.Fatal("filter not reported")
			}
			f.svc.Auth = unscannedAuth{f.auth}
			external, e = f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "context", Destination: "external:provider"})
			if e != nil || len(external.Items) != 0 {
				t.Fatal("unscanned external context")
			}
		})
	}
	f := newFixture(t)
	for _, dest := range []string{"external:", "external:https://host", "local", "external:" + strings.Repeat("x", 65)} {
		if _, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "context", Destination: dest}); !errors.Is(e, ErrInvalid) {
			t.Fatal("invalid destination accepted")
		}
	}
}
func TestRestrictedSkillNeverSentToJev(t *testing.T) {
	f := newFixture(t)
	r := analyzedSkill(t, f)
	r.Sensitivity = "restricted"
	f.repo.store[colSkillVersions][recKey(r.Scope, r.ID)] = r
	f.svc.Router = judgeFunc(func(context.Context, string, []SkillCandidate) (RouteResult, error) {
		t.Fatal("restricted Jev payload")
		return RouteResult{}, nil
	})
	out, e := f.svc.RouteSkills(testCtx, f.human, SearchInput{Scope: scopeA, Query: "index"})
	if e != nil || out.Provider != "rules" || out.SelectedID != r.ID {
		t.Fatal("safe local fallback failed", e)
	}
}

func TestTransferPolicyReceivesDestinationAndClassification(t *testing.T) {
	f := newFixture(t)
	f.activeMemory("policy transfer metadata")
	auth := &inspectionAuth{Authorizer: f.auth}
	f.svc.Auth = auth
	out, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "policy", Destination: "external:test-model"})
	if e != nil || len(out.Items) != 1 {
		t.Fatal("allowed export failed", e)
	}
	found := false
	for _, payload := range auth.payloads {
		var req Authorization
		if json.Unmarshal([]byte(payload), &req) != nil {
			t.Fatal("invalid captured request")
		}
		if req.Action == "context:read" {
			var body map[string]string
			if json.Unmarshal([]byte(req.Content), &body) != nil || body["destination"] != "external:test-model" || body["sensitivity"] != "internal" {
				t.Fatal("policy metadata missing")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("item export not authorized")
	}
	who := f.auth.actors[f.human]
	who.external = false
	f.auth.actors[f.human] = who
	out, e = f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "policy", Destination: "external:test-model"})
	if e != nil || len(out.Items) != 0 {
		t.Fatal("export denied but item returned")
	}
}

func TestRestrictedSourceCannotBeExportedViaDerivedRecord(t *testing.T) {
	f := newFixture(t)
	r := f.activeMemory("derived source confidentiality")
	src := f.repo.rows("sources")[0]
	src.Sensitivity = "restricted"
	f.repo.store["sources"][recKey(src.Scope, src.ID)] = src
	out, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "derived", Destination: "external:test"})
	if e != nil || len(out.Items) != 0 {
		t.Fatal("restricted source laundered into context")
	}
	if e = f.svc.WorkOnce(testCtx, f.work); !errors.Is(e, ErrDenied) {
		t.Fatal("restricted source embedded")
	}
	for _, call := range f.emb.calls {
		if strings.HasPrefix(call, "document:") {
			t.Fatal("document exported")
		}
	}
	// The local record itself remains valid and readable.
	if got, e := f.svc.Get(testCtx, f.human, "memories", scopeA, r.ID); e != nil || got.ID != r.ID {
		t.Fatal("local record lost")
	}
}

type sourceBatchSpy struct {
	Repository
	batches     int
	sourceReads int
	maxIDs      int
}

func (s *sourceBatchSpy) Get(ctx context.Context, col string, scope Scope, id string) (Record, error) {
	if col == "sources" {
		s.sourceReads++
	}
	return s.Repository.Get(ctx, col, scope, id)
}
func (s *sourceBatchSpy) SourceMetadata(ctx context.Context, scope Scope, ids []string, includeText bool) ([]Record, error) {
	s.batches++
	if len(ids) > s.maxIDs {
		s.maxIDs = len(ids)
	}
	return s.Repository.SourceMetadata(ctx, scope, ids, includeText)
}
func TestExternalContextBatchesDuplicateSources(t *testing.T) {
	f := newFixture(t)
	r := f.activeMemory("batch source evidence")
	first := r.Sources[0]
	r.Sources = make([]SourceRef, 64)
	for i := range r.Sources {
		r.Sources[i] = first
	}
	f.repo.store[colMemories][recKey(r.Scope, r.ID)] = r
	spy := &sourceBatchSpy{Repository: f.repo}
	f.svc.Repo = spy
	out, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "batch", Destination: "external:test"})
	if e != nil || len(out.Items) != 1 || spy.batches == 0 || spy.batches > 3 || spy.maxIDs != 1 || spy.sourceReads != 0 {
		t.Fatalf("unbounded evidence reads: batches=%d ids=%d err=%v", spy.batches, spy.maxIDs, e)
	}
}
