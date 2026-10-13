package memory

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func boolValue(v bool) *bool        { return &v }
func scoreValue(v float64) *float64 { return &v }

func TestEvaluationValidationAndRoles(t *testing.T) {
	for _, x := range []*Evaluation{nil, {}, {EvalType: "other"}, {EvalType: "user_rating", Score: scoreValue(1)}, {EvalType: "user_rating", ScoreMax: scoreValue(5)}, {EvalType: "user_rating", Score: scoreValue(-1), ScoreMax: scoreValue(5)}, {EvalType: "user_rating", Score: scoreValue(6), ScoreMax: scoreValue(5)}, {EvalType: "user_rating", Score: scoreValue(0), ScoreMax: scoreValue(0)}, {EvalType: "user_rating", Score: scoreValue(math.NaN()), ScoreMax: scoreValue(5)}, {EvalType: "user_rating", Score: scoreValue(1), ScoreMax: scoreValue(math.Inf(1))}} {
		if validEvaluation(x) {
			t.Fatalf("accepted invalid evaluation: %+v", x)
		}
	}
	for _, kind := range []string{"user_rating", "automated_check", "model_self"} {
		for _, role := range []string{"human", "agent", "service", "pipeline"} {
			t.Run(kind+"/"+role, func(t *testing.T) {
				f := newFixture(t)
				src := f.source()
				f.auth.actors["evaluator"] = actor("evaluator", role, true, allVerbs...)
				owner := f.agent
				if kind == "model_self" {
					owner = "evaluator"
				}
				run := f.create(owner, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "task", Sources: refs(src)}))
				in := CreateInput{Scope: scopeA, Text: "evaluation", Sources: refs(src), Target: &Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}, Evaluation: &Evaluation{EvalType: kind, Passed: boolValue(true)}}
				got, e := f.svc.Create(testCtx, "evaluator", "evaluations", in)
				allowed := kind == "user_rating" && role == "human" || kind == "automated_check" && (role == "service" || role == "pipeline") || kind == "model_self" && role != "human"
				if allowed {
					if e != nil || !got.Passed {
						t.Fatalf("create: %+v %v", got, e)
					}
				} else if !errors.Is(e, ErrDenied) {
					t.Fatalf("role bypass: %v", e)
				}
			})
		}
	}
}

func TestEvaluationCreationGuards(t *testing.T) {
	for _, name := range []string{"missing", "mismatch", "running", "wrong_owner", "target_denied", "principal_changed", "foreign_scope"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			src := f.source()
			rin := completedRunInput(CreateInput{Scope: scopeA, Text: "task", Sources: refs(src)})
			if name == "running" {
				rin.Run.Outcome = "running"
				rin.Run.FinishedAt = nil
			}
			run := f.create(f.agent, "agent_runs", rin)
			in := CreateInput{Scope: scopeA, Text: "review", Sources: refs(src), Target: &Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}, Evaluation: &Evaluation{EvalType: "user_rating", Passed: boolValue(true)}}
			want := ErrInvalid
			switch name {
			case "missing":
				in.Evaluation = nil
			case "mismatch":
				in.Passed = true
				in.Evaluation.Passed = nil
			case "running":
				want = ErrConflict
			case "wrong_owner":
				in.Evaluation.EvalType = "model_self"
				want = ErrDenied
			case "target_denied":
				f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: run.ID}
				want = ErrDenied
			case "principal_changed":
				f.svc.Auth = usagePrincipalAuth{Authorizer: f.auth, action: "read:agent_runs"}
				want = ErrDenied
			case "foreign_scope":
				in.Target.ID = "foreign"
				want = ErrNotFound
			}
			got, e := f.svc.Create(testCtx, f.human, "evaluations", in)
			if !errors.Is(e, want) || got.ID != "" || len(f.repo.rows("evaluations")) != 0 {
				t.Fatalf("guard: %+v %v want %v", got, e, want)
			}
		})
	}
}

func TestEvaluationSummaryPriorityAndIsolation(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	run := f.create(f.agent, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "private task", Sources: refs(src)}))
	target := Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}
	in := EvaluationSummaryInput{Scope: scopeA, Target: target}
	add := func(token, kind string, passed *bool, score, max *float64) Record {
		return f.create(token, "evaluations", CreateInput{Scope: scopeA, Text: "private evaluation", Sources: refs(src), Target: &target, Evaluation: &Evaluation{EvalType: kind, Passed: passed, Score: score, ScoreMax: max}})
	}
	add(f.agent, "model_self", boolValue(true), scoreValue(100), scoreValue(100))
	out, e := f.svc.SummarizeEvaluations(testCtx, f.human, in)
	if e != nil || out.Passed == nil || !*out.Passed || out.Effective.Count != 1 || out.PreferredTypes[0] != "model_self" {
		t.Fatalf("self-only: %+v %v", out, e)
	}
	user := add(f.human, "user_rating", boolValue(false), scoreValue(2), scoreValue(5))
	f.auth.actors["auto"] = actor("auto-1", "pipeline", true, allVerbs...)
	add("auto", "automated_check", boolValue(true), scoreValue(8), scoreValue(10))
	add(f.human2, "user_rating", nil, nil, nil)
	legacy := user
	legacy.ID = ID()
	legacy.Evaluation = nil
	if e = f.repo.Insert(testCtx, "evaluations", legacy); e != nil {
		t.Fatal(e)
	}
	for _, scope := range []Scope{{WorkspaceID: "other", ProjectID: scopeA.ProjectID}, scopeB, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, AgentID: "other"}} {
		row := user
		row.ID = ID()
		row.Scope = scope
		if e = f.repo.Insert(testCtx, "evaluations", row); e != nil {
			t.Fatal(e)
		}
	}
	for _, kind := range []string{"expired", "inactive", "revision", "target"} {
		row := user
		row.ID = ID()
		ref := target
		row.Target = &ref
		switch kind {
		case "expired":
			past := time.Now().Add(-time.Hour)
			row.ExpiresAt = &past
		case "inactive":
			row.Status = "deleted"
		case "revision":
			ref.Revision++
		case "target":
			ref.ID = "other"
		}
		if e = f.repo.Insert(testCtx, "evaluations", row); e != nil {
			t.Fatal(e)
		}
	}
	out, e = f.svc.SummarizeEvaluations(testCtx, f.human, in)
	if e != nil || len(out.EvaluationRefs) != 5 || out.LegacyCount != 1 || out.Effective.Count != 3 || out.Effective.Unrated != 1 || out.Effective.Passed != 1 || out.Effective.Failed != 1 || out.Passed == nil || *out.Passed || out.Effective.MeanScore == nil || math.Abs(*out.Effective.MeanScore-.6) > 1e-9 || len(out.PreferredTypes) != 2 {
		t.Fatalf("priority: %+v %v", out, e)
	}
	for _, ref := range out.EvaluationRefs {
		r, err := f.repo.Get(testCtx, ref.Collection, scopeA, ref.ID)
		if err != nil || ref.Collection != "evaluations" || ref.Revision != r.Revision || r.Scope != scopeA || r.Target == nil || *r.Target != target || !live(r) {
			t.Fatalf("unsafe evaluation reference: %+v %v", ref, err)
		}
	}
	raw, _ := json.Marshal(in)
	req := httptest.NewRequest(http.MethodPost, "/v1/evaluations/summary", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 200 || strings.Contains(w.Body.String(), "private") || !strings.Contains(w.Body.String(), AuthorityDataOnly) {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: user.ID}
	if out, e = f.svc.SummarizeEvaluations(testCtx, f.human, in); !errors.Is(e, ErrDenied) || out.Groups != nil || out.EvaluationRefs != nil {
		t.Fatalf("row policy: %+v %v", out, e)
	}
	f.svc.Auth = f.auth
	f.repo.failAudit = true
	if out, e = f.svc.SummarizeEvaluations(testCtx, f.human, in); !errors.Is(e, ErrUnavailable) || out.Groups != nil || out.EvaluationRefs != nil {
		t.Fatalf("audit failure: %+v %v", out, e)
	}
}

func TestEvaluationSelfCannotVerify(t *testing.T) {
	f := newFixture(t)
	r := verifiedReflection(t, f)
	ev, e := f.repo.Get(testCtx, "evaluations", scopeA, r.Reflection.EvaluationID)
	if e != nil {
		t.Fatal(e)
	}
	ev.Evaluation = &Evaluation{EvalType: "model_self", Passed: boolValue(true)}
	if independentEvaluation(ev) {
		t.Fatal("self passes independent gate")
	}
	f.repo.store["evaluations"][recKey(scopeA, ev.ID)] = ev
	if e = checkReflection(testCtx, f.repo.view(), r); e == nil {
		t.Fatal("self verifies reflection")
	}
	skill := Record{ID: "skill", Scope: scopeA, CreatedBy: "someone", Revision: 1}
	ev.Target = &Ref{Collection: "skill_versions", ID: skill.ID, Revision: 1}
	f.repo.store["evaluations"][recKey(scopeA, ev.ID)] = ev
	if e = validation(testCtx, f.repo.view(), skill, ev.ID, true); e == nil {
		t.Fatal("self verifies skill")
	}
}

type evaluationChangeRepo struct {
	Repository
	change func()
}

func (r evaluationChangeRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	r.change()
	return r.Repository.Transaction(ctx, fn)
}
func TestEvaluationSummaryFailClosed(t *testing.T) {
	for _, name := range []string{"set_changed", "target_changed", "source_deleted", "overflow", "principal", "unrated"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			src := f.source()
			run := f.create(f.agent, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "task", Sources: refs(src)}))
			target := Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}
			ev := f.create(f.human, "evaluations", CreateInput{Scope: scopeA, Text: "review", Sources: refs(src), Target: &target, Evaluation: &Evaluation{EvalType: "user_rating"}})
			want := ErrConflict
			switch name {
			case "set_changed":
				f.svc.Repo = evaluationChangeRepo{Repository: f.repo, change: func() {
					r := ev
					r.ID = ID()
					if e := f.repo.Insert(testCtx, "evaluations", r); e != nil {
						t.Fatal(e)
					}
				}}
			case "target_changed":
				f.svc.Repo = evaluationChangeRepo{Repository: f.repo, change: func() {
					updated := run
					updated.Revision++
					if e := f.repo.Replace(testCtx, "agent_runs", updated, run.Revision); e != nil {
						t.Fatal(e)
					}
				}}
			case "source_deleted":
				r := src
				r.Status = "deleted"
				if e := f.repo.Replace(testCtx, "sources", r, r.Revision); e != nil {
					t.Fatal(e)
				}
			case "overflow":
				want = ErrUnavailable
				for i := 0; i < evaluationSummaryLimit; i++ {
					r := ev
					r.ID = ID()
					if e := f.repo.Insert(testCtx, "evaluations", r); e != nil {
						t.Fatal(e)
					}
				}
			case "principal":
				want = ErrDenied
				f.svc.Auth = usagePrincipalAuth{Authorizer: f.auth, action: "read:evaluations"}
			case "unrated":
				want = nil
			}
			out, e := f.svc.SummarizeEvaluations(testCtx, f.human, EvaluationSummaryInput{Scope: scopeA, Target: target})
			if name == "source_deleted" {
				if e == nil || out.Groups != nil || out.EvaluationRefs != nil {
					t.Fatalf("source: %+v %v", out, e)
				}
				return
			}
			if !errors.Is(e, want) {
				t.Fatalf("%v want %v", e, want)
			}
			if want != nil && (out.Groups != nil || out.EvaluationRefs != nil) {
				t.Fatalf("partial result: %+v", out)
			}
			if want == nil && (out.Passed != nil || out.Effective.Unrated != 1) {
				t.Fatalf("unrated: %+v", out)
			}
		})
	}
}
