package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

type countingAnalyzer struct{ calls int }

func (a *countingAnalyzer) Generator() Generator {
	return Generator{Model: "test-model", PromptVersion: "test-prompt", PipelineVersion: "test-pipeline"}
}

func (a *countingAnalyzer) Analyze(context.Context, string) (SkillAnalysis, error) {
	a.calls++
	return SkillAnalysis{Name: "index helper", Description: "verify safe indexes", Problem: "search indexing", UseWhen: []string{"index"}, InputFormat: "query", OutputFormat: "report", SuccessCriteria: []string{"passes"}, Verification: []string{"test"}}, nil
}

type judgeFunc func(context.Context, string, []SkillCandidate) (RouteResult, error)

func (f judgeFunc) Judge(c context.Context, q string, s []SkillCandidate) (RouteResult, error) {
	return f(c, q, s)
}

func analyzedSkill(t *testing.T, f *fixture, evidence ...Ref) Record {
	t.Helper()
	jobsBefore := len(f.repo.rows("jobs"))
	a := &countingAnalyzer{}
	f.svc.Analyzer = a
	in := AnalyzeSkillInput{Scope: scopeA, SkillID: "index-helper", Content: "# index helper\nVerify indexes.", Path: "/skills/index-helper/SKILL.md"}
	first, e := f.svc.AnalyzeSkill(testCtx, f.human, in)
	if e != nil {
		t.Fatal(e)
	}
	again, e := f.svc.AnalyzeSkill(testCtx, f.human, in)
	if e != nil || !again.Reused || a.calls != 1 || again.Record.ID != first.Record.ID {
		t.Fatalf("hash cache: reused=%v calls=%d err=%v", again.Reused, a.calls, e)
	}
	if len(f.repo.rows("jobs")) != jobsBefore {
		t.Fatal("candidate must not automatically embed")
	}
	v := first.Record
	ev := f.create(f.human2, "evaluations", ratingInput(CreateInput{Scope: scopeA, Text: "independent verification", Sources: v.Sources, Relations: evidence, Target: &Ref{Collection: colSkillVersions, ID: v.ID, Revision: v.Revision}, Passed: true}))
	approved := f.transition(f.human, colSkillVersions, v.ID, ChangeInput{Scope: scopeA, ExpectedRevision: v.Revision, Status: "approved", ValidationID: ev.ID})
	f.apply(f.human, v.SkillID, ChangeInput{Scope: scopeA, ExpectedRevision: 1, VersionID: approved.ID}, false)
	return f.get(f.human, colSkillVersions, scopeA, v.ID)
}
func verifiedReflection(t *testing.T, f *fixture, evidence ...Ref) Record {
	t.Helper()
	src := f.source()
	run := f.create(f.agent, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "index task", Sources: refs(src)}))
	ev := f.create(f.human, "evaluations", ratingInput(CreateInput{Scope: scopeA, Text: "verified index test", Sources: refs(src), Relations: evidence, Target: &Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}, Passed: true}))
	return f.create(f.human, colReflections, CreateInput{Scope: scopeA, Text: "summary", Sources: refs(src), Relations: []Ref{{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}, {Collection: "evaluations", ID: ev.ID, Revision: ev.Revision}}, Reflection: &Reflection{TaskType: "index", Outcome: "success", Lesson: "verify index readiness", VerificationMethod: "live test", EvaluationScore: 1, Verified: true, RunID: run.ID, EvaluationID: ev.ID}})
}
func TestStructuredEmbeddingReuseAndPurposeIsolation(t *testing.T) {
	for _, col := range []string{colSkillVersions, colReflections} {
		t.Run(col, func(t *testing.T) {
			f := newFixture(t)
			var r Record
			if col == colSkillVersions {
				r = analyzedSkill(t, f)
			} else {
				r = verifiedReflection(t, f)
			}
			if e := f.svc.WorkOnce(testCtx, f.work); e != nil {
				t.Fatal(e)
			}
			current := f.get(f.human, col, scopeA, r.ID)
			if current.Embedding == nil {
				t.Fatal("missing embedding")
			}
			if e := f.repo.Insert(testCtx, "jobs", Record{ID: ID(), Scope: scopeA, Kind: "embedding", Status: JobStatusPending, Revision: 1, Target: &Ref{Collection: col, ID: r.ID, Revision: current.Revision}}); e != nil {
				t.Fatal(e)
			}
			if e := f.svc.WorkOnce(testCtx, f.work); e != nil {
				t.Fatal(e)
			}
			if len(f.emb.calls) != 1 {
				t.Fatalf("duplicate embedding: %d", len(f.emb.calls))
			}
			embeddingCosts := 0
			for _, usage := range f.repo.rows(colProviderUsage) {
				if usage.Cost != nil && usage.Cost.Operation == "embedding_document" {
					embeddingCosts++
				}
			}
			if embeddingCosts != 1 {
				t.Fatalf("duplicate embedding cost record: %d", embeddingCosts)
			}
			if got := f.search(f.human, SearchInput{Scope: scopeA, Query: "index"}); len(got.Items) != 0 {
				t.Fatal("non-memory leaked into answer")
			}
			purpose := "skills"
			if col == colReflections {
				purpose = "self_improvement"
			}
			got := f.search(f.human, SearchInput{Scope: scopeA, Query: "index", Purpose: purpose})
			if len(got.Items) != 1 || got.Items[0].Authority != AuthorityDataOnly {
				t.Fatalf("purpose result: %+v", got)
			}
			if col == colReflections {
				current = f.get(f.human, col, scopeA, r.ID)
				x := *current.Reflection
				x.Verified = false
				current.Reflection = &x
				if e := f.repo.Replace(testCtx, col, current, current.Revision); e != nil {
					t.Fatal(e)
				}
				if got := f.search(f.human, SearchInput{Scope: scopeA, Query: "index", Purpose: purpose}); len(got.Items) != 0 {
					t.Fatal("unverified reflection leaked")
				}
			}
		})
	}
}
func TestRoutingFallbackAndRevocation(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "revocation"}[revoke], func(t *testing.T) {
			f := newFixture(t)
			v := analyzedSkill(t, f)
			f.svc.Router = judgeFunc(func(_ context.Context, _ string, c []SkillCandidate) (RouteResult, error) {
				payload := structuredText(c)
				for _, forbidden := range []string{"source_path", "secret_names", "raw_output", "mongodb"} {
					if strings.Contains(payload, forbidden) {
						t.Fatal("disallowed routing data")
					}
				}
				if revoke {
					r := f.get(f.human, colSkillVersions, scopeA, v.ID)
					r.Status = "rejected"
					if e := f.repo.Replace(testCtx, colSkillVersions, r, r.Revision); e != nil {
						t.Fatal(e)
					}
				}
				return RouteResult{}, ErrUnavailable
			})
			got, e := f.svc.RouteSkills(testCtx, f.human, SearchInput{Scope: scopeA, Query: "index"})
			if revoke {
				if !errors.Is(e, ErrConflict) || got.SelectedID != "" {
					t.Fatal("revoked fallback selected")
				}
			} else if e != nil || got.Provider != "rules" || got.SelectedID != v.ID || !got.Judgments[0].NeedsHumanReview {
				t.Fatalf("unsafe fallback: %+v %v", got, e)
			}
		})
	}
}
func TestTransferRejectsSecretsAndRawOutputs(t *testing.T) {
	for _, s := range []string{"sk-abcdefghijklmnop", "API_KEY=abcdefgh12345", "Bearer abcdefghijk", "person@example.com", `{"raw_output":"hello"}`, `{"api_key":"abcdefgh12345"}`, structuredText(map[string]string{"description": `{"raw_output":"hello"}`})} {
		if safeTransfer(s) {
			t.Fatalf("accepted sensitive payload %q", s)
		}
	}
	if !safeTransfer(`{"secret_names":["VOYAGE_API_KEY"],"lesson":"verify index"}`) {
		t.Fatal("name is not a secret value")
	}
}

func TestJevTypedContract(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var p map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&p) != nil {
			t.Error("bad payload")
		}
		if len(p) != 3 || string(p["model"]) != `"jev-1.13.0"` {
			t.Error("unexpected request fields")
		}
		var state map[string]json.RawMessage
		_ = json.Unmarshal(p["state"], &state)
		if len(state) != 2 || state["query"] == nil || state["candidates"] == nil {
			t.Error("payload allowlist")
		}
		answers := map[string]any{
			"selection":   map[string]any{"type": "choice", "choice": "a", "confidence": .95, "probabilities": map[string]float64{"a": 1}},
			"c0_fit":      map[string]any{"type": "score", "score": 3, "confidence": .95, "probabilities": map[string]float64{"0": 0, "1": 0, "2": 0, "3": 1}},
			"c0_conflict": map[string]any{"type": "noul", "noul": .01},
			"c0_verify":   map[string]any{"type": "noul", "noul": .9},
			"c0_review":   map[string]any{"type": "noul", "noul": .9},
		}
		if requests == 2 {
			delete(answers, "c0_fit")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers})
	}))
	defer server.Close()
	judge := &JevJudge{APIKey: "test", Endpoint: server.URL}
	candidates := []SkillCandidate{{ID: "a", Name: "index helper"}}
	got, e := judge.Judge(testCtx, "index", candidates)
	if e != nil || got.SelectedID != "a" || !got.Judgments[0].NeedsHumanReview {
		t.Fatalf("valid response: %+v %v", got, e)
	}
	if _, e = judge.Judge(testCtx, "index", candidates); e == nil {
		t.Fatal("missing answer accepted")
	}
	if _, e = judge.Judge(testCtx, "Bearer abcdefghijklmnop", candidates); !errors.Is(e, ErrDenied) || requests != 2 {
		t.Fatal("sensitive query sent")
	}
}

// Clef-flash speaks Jev's API behind Cloudflare's {"result": …} wrapper, wants two options in a choice, and
// counts input tokens; a failed Clef answer falls back to Jev, a refused (sensitive) query does not.
func TestClefFlashJudgeAndJevFallback(t *testing.T) {
	answers := func(choice string, probs map[string]float64) map[string]any {
		return map[string]any{
			"selection":   map[string]any{"type": "choice", "choice": choice, "confidence": .9, "probabilities": probs},
			"c0_fit":      map[string]any{"type": "score", "score": 2.6, "confidence": .7, "probabilities": map[string]float64{"0": .03, "1": .07, "2": .16, "3": .74}},
			"c0_conflict": map[string]any{"type": "noul", "noul": .1},
			"c0_verify":   map[string]any{"type": "noul", "noul": .2},
			"c0_review":   map[string]any{"type": "noul", "noul": .2},
		}
	}
	var seen map[string]any
	choice := "a"
	clef := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cf-token" {
			t.Error("Cloudflare token missing")
		}
		_ = json.NewDecoder(r.Body).Decode(&seen)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"model": "clef-flash",
			"answers": answers(choice, map[string]float64{"a": .9, noneOfThem: .1}), "usage": map[string]any{"input_tokens": 362}}})
	}))
	defer clef.Close()
	judge := NewClefJudge("acct", "cf-token")
	if !strings.HasSuffix(judge.Endpoint, "/accounts/acct/ai/run/@cf/cloudflare/clef-flash") {
		t.Fatalf("endpoint %s", judge.Endpoint)
	}
	judge.Endpoint = clef.URL
	one := []SkillCandidate{{ID: "a", Name: "pdf"}}
	got, usage, e := judge.JudgeMeasured(testCtx, "PDF 표 추출", one)
	criteria := seen["questions"].(map[string]any)["selection"].(map[string]any)["criteria"].(map[string]any)
	if e != nil || got.SelectedID != "a" || got.lowSelection || got.Provider != "clef-flash" || seen["model"] != "clef-flash" || len(criteria) != 2 {
		t.Fatalf("clef answer: %+v %v %v", got, e, seen["model"])
	}
	if usage.Provider != "cloudflare" || usage.Model != "clef-flash" || usage.InputTokens != 362 {
		t.Fatalf("usage %+v", usage)
	}
	choice = noneOfThem
	if got, _ = judge.Judge(testCtx, "PDF 표 추출", one); got.SelectedID != "a" || !got.lowSelection {
		t.Fatalf("a refused lone candidate must withhold the pick: %+v", got)
	}

	jevCalls := 0
	jev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jevCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers("a", map[string]float64{"a": 1})})
	}))
	defer jev.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	defer down.Close()
	fallback := &FallbackJudge{Primary: &JevJudge{APIKey: "cf", Endpoint: down.URL, Model: "clef-flash", Provider: "cloudflare", Envelope: true},
		Secondary: &JevJudge{APIKey: "jev", Endpoint: jev.URL}}
	got, usage, e = fallback.JudgeMeasured(testCtx, "PDF 표 추출", one)
	if e != nil || got.Provider != "jev-1.13.0" || usage.Provider != "typesafe" || jevCalls != 1 {
		t.Fatalf("fallback to Jev: %+v %+v %v", got, usage, e)
	}
	if p, m := judgeIdentity(fallback); p != "cloudflare" || m != "clef-flash" {
		t.Fatalf("reserved under %s/%s", p, m)
	}
	if _, e = fallback.Judge(testCtx, "Bearer abcdefghijklmnop", one); !errors.Is(e, ErrDenied) || jevCalls != 1 {
		t.Fatal("a refused query must not be retried on the spare")
	}
}

// Jev rounds probabilities to two decimals and may be unsure between
// candidates; neither is an outage (production fell back to rules ~1 in 5).
func TestJevRoundedAndUnsureAnswersAreKept(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answers := map[string]any{"selection": map[string]any{"type": "choice", "choice": "a", "confidence": .45, "probabilities": map[string]float64{"a": .45, "b": .54}}}
		for _, c := range []string{"c0", "c1"} {
			answers[c+"_fit"] = map[string]any{"type": "score", "score": 1.34, "confidence": .63, "probabilities": map[string]float64{"0": .01, "1": .67, "2": .27, "3": .04}}
			for _, s := range []string{"_conflict", "_verify", "_review"} {
				answers[c+s] = map[string]any{"type": "noul", "noul": .1}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers})
	}))
	defer server.Close()
	judge := &JevJudge{APIKey: "test", Endpoint: server.URL}
	got, e := judge.Judge(testCtx, "index", []SkillCandidate{{ID: "a", Name: "a"}, {ID: "b", Name: "b"}})
	if e != nil || got.SelectedID != "a" || !got.lowSelection || len(got.Judgments) != 2 {
		t.Fatalf("rounded, unsure answer rejected: %+v %v", got, e)
	}
	// Eight uniform candidates rounded to 0.13 each sum to 1.04.
	eight := map[string]float64{}
	keys := []string{}
	for i := range 8 {
		k := fmt.Sprint(i)
		eight[k], keys = .13, append(keys, k)
	}
	if !distribution(eight, keys) || distribution(map[string]float64{"a": .9, "b": .2}, []string{"a", "b"}) {
		t.Fatal("rounding tolerance must scale with the number of keys and still reject real errors")
	}

	f := newFixture(t)
	v := analyzedSkill(t, f)
	f.svc.Router = judgeFunc(func(context.Context, string, []SkillCandidate) (RouteResult, error) {
		return RouteResult{SelectedID: v.ID, Provider: "jev", Judgments: []SkillJudgment{{ID: v.ID, Suitability: 3, Confidence: .9}}, lowSelection: true}, nil
	})
	routed, e := f.svc.RouteSkills(testCtx, f.human, SearchInput{Scope: scopeA, Query: "index"})
	if e != nil || routed.SelectedID != "" || routed.Provider != "jev" || len(routed.Judgments) != 1 || !slices.Equal(routed.Degraded, []string{"jev_uncertain"}) {
		t.Fatalf("unsure selection must keep judgments and withhold the pick: %+v %v", routed, e)
	}
	// ROUTER sees the judge's unconfirmed best guess (SkillRoute has no query field).
	logged, e := f.svc.SkillRoutes(testCtx, f.human, scopeA)
	if e != nil || len(logged) != 1 || logged[0].Selected != v.ID || logged[0].Confirmed || logged[0].Provider != "jev" ||
		!slices.Equal(logged[0].Degraded, []string{"jev_uncertain"}) || logged[0].SelectedName != "index helper" || logged[0].Candidates != 1 || logged[0].At.IsZero() {
		t.Fatalf("skill decision not kept: %+v %v", logged, e)
	}
}

func TestRoutingUncertainDecisionDoesNotSelect(t *testing.T) {
	f := newFixture(t)
	v := analyzedSkill(t, f)
	f.svc.Router = judgeFunc(func(context.Context, string, []SkillCandidate) (RouteResult, error) {
		return RouteResult{SelectedID: v.ID, Provider: "jev", Judgments: []SkillJudgment{{ID: v.ID, Suitability: 0, Confidence: .9, Conflict: true}}}, nil
	})
	got, e := f.svc.RouteSkills(testCtx, f.human, SearchInput{Scope: scopeA, Query: "index"})
	if e != nil || got.SelectedID != "" || !got.Judgments[0].Conflict || !got.Judgments[0].NeedsHumanReview {
		t.Fatalf("uncertain choice: %+v %v", got, e)
	}
}

type unscannedAuth struct{ inner Authorizer }

func (a unscannedAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	d, e := a.inner.Authorize(ctx, token, in)
	d.ContentSafe = false
	return d, e
}
func TestExternalPermissionDoesNotImplySecretScan(t *testing.T) {
	f := newFixture(t)
	f.svc.Auth = unscannedAuth{f.auth}
	f.svc.Analyzer = &countingAnalyzer{}
	_, e := f.svc.AnalyzeSkill(testCtx, f.human, AnalyzeSkillInput{Scope: scopeA, SkillID: "test", Path: "/test/SKILL.md", Content: "safe summary"})
	if !errors.Is(e, ErrDenied) {
		t.Fatal("analysis without scan accepted")
	}
	got := f.search(f.human, SearchInput{Scope: scopeA, Query: "safe", Purpose: "knowledge"})
	if len(f.emb.calls) != 0 || len(got.Items) != 0 {
		t.Fatal("unscanned transfer")
	}
}

func TestUnscannedWorkerAndRoutingStayLocal(t *testing.T) {
	f := newFixture(t)
	v := analyzedSkill(t, f)
	f.svc.Auth = unscannedAuth{f.auth}
	f.svc.Router = judgeFunc(func(context.Context, string, []SkillCandidate) (RouteResult, error) {
		t.Fatal("unscanned Jev call")
		return RouteResult{}, nil
	})
	if e := f.svc.WorkOnce(testCtx, f.work); !errors.Is(e, ErrDenied) {
		t.Fatalf("worker: %v", e)
	}
	got, e := f.svc.RouteSkills(testCtx, f.human, SearchInput{Scope: scopeA, Query: "index"})
	if e != nil || got.Provider != "rules" || got.SelectedID != v.ID || len(f.emb.calls) != 0 {
		t.Fatalf("unsafe transfer: %+v %v", got, e)
	}
}

func TestSkillParentRevocation(t *testing.T) {
	for _, mode := range []string{"deleted_source", "expired_source", "inactive_parent", "expired_parent"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			src := f.source()
			// Seed a header created before this version, citing another source.
			if e := f.repo.Insert(testCtx, colSkills, Record{ID: "index-helper", Scope: scopeA, Kind: "skill", Status: "active", Revision: 1, Sources: refs(src)}); e != nil {
				t.Fatal(e)
			}
			v := analyzedSkill(t, f)
			if got := f.search(f.human, SearchInput{Scope: scopeA, Query: "index", Purpose: "skills"}); len(got.Items) != 1 {
				t.Fatal("valid independent parent source excluded")
			}
			revoke := func() {
				past := time.Now().Add(-time.Hour)
				switch mode {
				case "deleted_source":
					if e := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
						t.Fatal(e)
					}
				case "expired_source":
					src.ExpiresAt = &past
					if e := f.repo.Replace(testCtx, "sources", src, src.Revision); e != nil {
						t.Fatal(e)
					}
				default:
					p := f.get(f.human, colSkills, scopeA, v.SkillID)
					if mode == "inactive_parent" {
						p.Status = "rejected"
					} else {
						p.ExpiresAt = &past
					}
					if e := f.repo.Replace(testCtx, colSkills, p, p.Revision); e != nil {
						t.Fatal(e)
					}
				}
			}
			f.svc.Router = judgeFunc(func(context.Context, string, []SkillCandidate) (RouteResult, error) {
				revoke()
				return RouteResult{}, ErrUnavailable
			})
			got, e := f.svc.RouteSkills(testCtx, f.human, SearchInput{Scope: scopeA, Query: "index"})
			if !errors.Is(e, ErrConflict) || got.SelectedID != "" {
				t.Fatalf("revoked parent selected: %+v %v", got, e)
			}
			if got := f.search(f.human, SearchInput{Scope: scopeA, Query: "index", Purpose: "skills"}); len(got.Items) != 0 {
				t.Fatal("revoked parent searchable")
			}
			p, e := f.repo.Get(testCtx, colSkills, scopeA, v.SkillID)
			if e != nil {
				t.Fatal(e)
			}
			cleared, e := f.svc.ApplySkill(testCtx, f.human, v.SkillID, ChangeInput{Scope: scopeA, ExpectedRevision: p.Revision}, true)
			if e != nil || cleared.ActiveVersion != "" {
				t.Fatalf("cannot clear revoked skill: %v", e)
			}
			_, e = f.svc.ApplySkill(testCtx, f.human, v.SkillID, ChangeInput{Scope: scopeA, ExpectedRevision: cleared.Revision, VersionID: v.ID}, true)
			want := ErrConflict
			if mode == "deleted_source" || mode == "expired_source" {
				want = ErrDenied
			}
			if !errors.Is(e, want) {
				t.Fatalf("reactivation err = %v, want %v", e, want)
			}
			kept, e := f.repo.Get(testCtx, colSkills, scopeA, v.SkillID)
			if e != nil || kept.ActiveVersion != "" || kept.Revision != cleared.Revision {
				t.Fatal("failed apply changed parent")
			}
		})
	}
}

func TestSkillQueryVariantsIncludeKoreanBrandPrefix(t *testing.T) {
	got := skillQueryVariants("당근마켓에서 맥북에어 중고 가격 알려줘")
	if !strings.Contains(got, "당근") || !strings.Contains(got, "당근마켓") {
		t.Fatalf("variants = %q", got)
	}
}

func TestRoutingJudgesExtraMCPAndPluginCandidates(t *testing.T) {
	f := newFixture(t)
	v := analyzedSkill(t, f)
	var seen []SkillCandidate
	f.svc.Router = judgeFunc(func(_ context.Context, _ string, c []SkillCandidate) (RouteResult, error) {
		seen = c
		return RouteResult{SelectedID: "mcp-files", Provider: "jev", Judgments: []SkillJudgment{{ID: v.ID, Suitability: 1, Confidence: .9}, {ID: "mcp-files", Suitability: 3, Confidence: .9}}}, nil
	})
	extra := []SkillCandidate{{ID: "mcp-files", Kind: "mcp", Name: "files", Description: "file search", Tools: []string{"dropped"}}}
	got, e := f.svc.RouteSkills(testCtx, f.human, SearchInput{Scope: scopeA, Query: "index", ExtraCandidates: extra})
	if e != nil || got.SelectedID != "mcp-files" || len(seen) != 2 || seen[0].Kind != "skill" || seen[1].Kind != "mcp" || seen[1].Tools != nil {
		t.Fatalf("extra candidate: %+v %+v %v", got, seen, e)
	}
	// The host trims descriptions to 300 code points; Korean takes 3 bytes each.
	long := []SkillCandidate{{ID: "mcp-ko", Kind: "mcp", Description: strings.Repeat("가", 300)}}
	if _, e := f.svc.RouteSkills(testCtx, f.human, SearchInput{Scope: scopeA, Query: "index", ExtraCandidates: long}); e != nil {
		t.Fatalf("300-rune description refused: %v", e)
	}
	for _, bad := range [][]SkillCandidate{
		{{ID: "files", Kind: "mcp", Name: "files"}},
		{{ID: "mcp-a/b", Kind: "mcp"}},
		{{ID: "skill-x", Kind: "skill"}},
		{{ID: "mcp-long", Kind: "mcp", Description: strings.Repeat("가", 301)}},
		{{ID: "mcp-1", Kind: "mcp"}, {ID: "mcp-2", Kind: "mcp"}, {ID: "mcp-3", Kind: "mcp"}, {ID: "mcp-4", Kind: "mcp"}, {ID: "mcp-5", Kind: "mcp"}},
	} {
		if _, e := f.svc.RouteSkills(testCtx, f.human, SearchInput{Scope: scopeA, Query: "index", ExtraCandidates: bad}); !errors.Is(e, ErrInvalid) {
			t.Fatalf("accepted %+v: %v", bad, e)
		}
	}
}
