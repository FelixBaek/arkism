package memory

// Task: SYS-02. Isolated service tests; no checker, HTTP, database or model calls.
import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func dynamicsOutcomeAccepted(t *testing.T, passed bool) (*fixture, Ref, MemoryOutcomeInput) {
	t.Helper()
	f, _, in := outcomeFixture(t)
	in.Passed = passed
	ref, err := f.svc.RecordMemoryOutcome(testCtx, "checker", in)
	if err != nil {
		t.Fatal("accept fixture", err)
	}
	f.auth.actors["consumer"] = actor("read-consumer", roleService, false, "read")
	return f, ref, in
}

func dynamicsOutcomeZero(t *testing.T, value NativeOutcomeIntent, err error) {
	t.Helper()
	if err == nil || !reflect.DeepEqual(value, NativeOutcomeIntent{}) {
		t.Fatal("failure returned an intent or no error")
	}
}

func dynamicsOutcomeMutate(t *testing.T, f *fixture, col string, ref Ref, fn func(*Record)) {
	t.Helper()
	r, err := f.repo.Get(testCtx, col, scopeA, ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	fn(&r)
	// Deliberate store corruption/changes are refusal fixtures, never positive authentication evidence.
	f.repo.mu.Lock()
	f.repo.store[col][recKey(scopeA, ref.ID)] = r
	f.repo.mu.Unlock()
}

func TestDynamicsOutcomeAcceptedPassAndFailureIntent(t *testing.T) {
	for _, passed := range []bool{false, true} {
		t.Run(map[bool]string{false: "false", true: "true"}[passed], func(t *testing.T) {
			f, ref, in := dynamicsOutcomeAccepted(t, passed)
			r, _ := f.repo.Get(testCtx, "evaluations", scopeA, ref.ID)
			out, err := f.svc.PrepareMemoryOutcomeIntent(testCtx, "consumer", ref, in)
			if err != nil {
				t.Fatal(err)
			}
			want := "observed_false"
			if passed {
				want = "observed_true"
			}
			if out.Protocol != "arkism-native-outcome-intent-v1" || out.Phase != "prepared_uncommitted" || out.TaskSuccess != want || out.AcceptedEvaluation != ref || out.EvaluatorPrincipal != "checker" || !out.ServerCreatedAt.Equal(r.CreatedAt) || out.NativeRequestSHA256 != r.RequestHash {
				t.Fatal("incorrect accepted metadata")
			}
			if out.ServerReturn != "observed_true" || out.ActualUse != "unknown" || out.ContextDelivery != "unknown" || out.Attribution != "unattributable" || out.CheckerHostBytesStatus != "not_observed_by_server" || out.Authority != AuthorityDataOnly {
				t.Fatal("observation promoted beyond available evidence")
			}
			if out.Strength != nil || out.TaskRef != nil || out.TrialRef != nil || out.ProcessingSeq != nil || out.LogicalConsumptionKey != nil || out.LearningQualification != "unsupported_native" || out.SourceObservedAt.State != "not_recorded" || out.SourceObservedAt.UnixNS != nil {
				t.Fatal("invented native learning evidence")
			}
		})
	}
}

func TestDynamicsOutcomeRejectsGenericCreateKindLookalike(t *testing.T) {
	f, ref, in := dynamicsOutcomeAccepted(t, false)
	passed := false
	lookalike, err := f.svc.Create(testCtx, "checker", "evaluations", CreateInput{Scope: scopeA, Kind: "memory_outcome", Text: "Independent pinned memory checker " + in.CheckerSHA256, Target: &in.Target, Evaluation: &Evaluation{EvalType: "automated_check", Passed: &passed}, Sources: f.repo.rows("evaluations")[0].Sources, Relations: []Ref{in.Target, in.Trace}})
	if err != nil {
		t.Fatal("generic kind alone is permitted", err)
	}
	if lookalike.ID == ref.ID || lookalike.RequestHash != "" {
		t.Fatal("fixture is not generic")
	}
	out, err := f.svc.PrepareMemoryOutcomeIntent(testCtx, "consumer", Ref{Collection: "evaluations", ID: lookalike.ID, Revision: 1}, in)
	dynamicsOutcomeZero(t, out, err)
	_, err = f.svc.Create(testCtx, "checker", "evaluations", CreateInput{Scope: scopeA, Kind: "memory_outcome", Text: "not acceptance", Tags: []string{"dimension:work_task"}, Target: &in.Target, Evaluation: &Evaluation{EvalType: "automated_check", Passed: &passed}})
	if err == nil {
		t.Fatal("generic dimension tags unexpectedly accepted")
	}
}

func TestDynamicsOutcomeRejectsCanonicalRecordTampering(t *testing.T) {
	changes := []struct {
		name   string
		change func(*Record)
	}{
		{"kind", func(r *Record) { r.Kind = "generic" }},
		{"id", func(r *Record) { r.ID = "memory-outcome-" + strings.Repeat("b", 64) }},
		{"hash", func(r *Record) { r.RequestHash = strings.Repeat("f", 64) }},
		{"empty_hash", func(r *Record) { r.RequestHash = "" }},
		{"role", func(r *Record) { r.CreatedByType = "pipeline" }},
		{"evaluator", func(r *Record) { r.CreatedBy = "other-evaluator" }},
		{"evaltype", func(r *Record) { r.Evaluation = &Evaluation{EvalType: "model_self", Passed: r.Evaluation.Passed} }},
		{"nil_evaluation", func(r *Record) { r.Evaluation = nil }},
		{"nil_passed", func(r *Record) { r.Evaluation = &Evaluation{EvalType: "automated_check"} }},
		{"passed", func(r *Record) { r.Passed = true }},
		{"eval_passed", func(r *Record) { v := true; r.Evaluation = &Evaluation{EvalType: "automated_check", Passed: &v} }},
		{"score", func(r *Record) {
			v, m := 0.0, 1.0
			old := *r.Evaluation
			old.Score = &v
			old.ScoreMax = &m
			r.Evaluation = &old
		}},
		{"text", func(r *Record) { r.Text = "Independent pinned memory checker " + strings.Repeat("b", 64) }},
		{"tag", func(r *Record) { r.Tags = []string{"dimension:personal_compliance"} }},
		{"extra_tag", func(r *Record) { r.Tags = append(slices.Clone(r.Tags), "extra") }},
		{"target", func(r *Record) { other := *r.Target; other.Revision++; r.Target = &other }},
		{"relations", func(r *Record) {
			r.Relations = slices.Clone(r.Relations)
			r.Relations[0], r.Relations[1] = r.Relations[1], r.Relations[0]
		}},
		{"sources", func(r *Record) { r.Sources = slices.Clone(r.Sources); r.Sources[0].Locator = "other" }},
		{"created_time", func(r *Record) { r.CreatedAt = r.CreatedAt.Add(time.Second) }},
		{"zero_time", func(r *Record) { r.CreatedAt = time.Time{}; r.UpdatedAt = time.Time{} }},
		{"scope", func(r *Record) { r.Scope = scopeB }},
		{"revision", func(r *Record) { r.Revision++ }},
		{"quarantine", func(r *Record) { r.PrivacyQuarantined = true }},
		{"erased", func(r *Record) { r.ContentErased = true }},
		{"inactive", func(r *Record) { r.Status = "deleted" }},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			f, ref, in := dynamicsOutcomeAccepted(t, false)
			dynamicsOutcomeMutate(t, f, "evaluations", ref, change.change)
			out, err := f.svc.PrepareMemoryOutcomeIntent(testCtx, "consumer", ref, in)
			dynamicsOutcomeZero(t, out, err)
		})
	}
}

func TestDynamicsOutcomeBindsOriginalRequestAndRef(t *testing.T) {
	for _, mode := range []string{"event", "passed", "checker", "dimension", "target", "trace", "evidence", "scope", "evaluation", "revision", "collection", "missing"} {
		t.Run(mode, func(t *testing.T) {
			f, ref, in := dynamicsOutcomeAccepted(t, false)
			switch mode {
			case "event":
				in.EventID = "changed"
			case "passed":
				in.Passed = true
			case "checker":
				in.CheckerSHA256 = strings.Repeat("b", 64)
			case "dimension":
				in.Dimension = "personal_compliance"
			case "target":
				in.Target.Revision++
			case "trace":
				in.Trace.Revision++
			case "evidence":
				in.Evidence = slices.Clone(in.Evidence)
				in.Evidence[0].Revision++
			case "scope":
				in.Scope = scopeB
			case "evaluation":
				ref.ID = "other"
			case "revision":
				ref.Revision = 2
			case "collection":
				ref.Collection = "sources"
			case "missing":
				in.EventID = ""
			}
			out, err := f.svc.PrepareMemoryOutcomeIntent(testCtx, "consumer", ref, in)
			dynamicsOutcomeZero(t, out, err)
		})
	}
}

func TestDynamicsOutcomeRechecksSourceTargetAndTrace(t *testing.T) {
	for _, col := range []string{"memories", "execution_traces", "sources"} {
		for _, mode := range []string{"revision", "deleted", "erased", "quarantined", "missing"} {
			t.Run(col+"/"+mode, func(t *testing.T) {
				f, ref, in := dynamicsOutcomeAccepted(t, false)
				if _, err := f.svc.PrepareMemoryOutcomeIntent(testCtx, "consumer", ref, in); err != nil {
					t.Fatal(err)
				}
				target := in.Target
				if col == "execution_traces" {
					target = in.Trace
				}
				if col == "sources" {
					target = in.Evidence[0]
				}
				if mode == "missing" {
					if err := f.repo.Delete(testCtx, col, scopeA, target.ID, target.Revision); err != nil {
						t.Fatal(err)
					}
				} else {
					dynamicsOutcomeMutate(t, f, col, target, func(r *Record) {
						switch mode {
						case "revision":
							r.Revision++
						case "deleted":
							r.Status = "deleted"
						case "erased":
							r.ContentErased = true
						case "quarantined":
							r.PrivacyQuarantined = true
						}
					})
				}
				out, err := f.svc.PrepareMemoryOutcomeIntent(testCtx, "consumer", ref, in)
				dynamicsOutcomeZero(t, out, err)
			})
		}
	}
}

func TestDynamicsOutcomeIndependentEvaluatorAndTraceGuards(t *testing.T) {
	for _, mode := range []string{"creator", "approver", "trace_creator", "source_creator", "crew", "nonterminal", "unfinished", "run_revision", "run_missing", "source_trace", "source_event", "source_bytes", "trace_membership"} {
		t.Run(mode, func(t *testing.T) {
			f, ref, in := dynamicsOutcomeAccepted(t, false)
			col, target := "memories", in.Target
			switch mode {
			case "trace_creator", "trace_membership":
				col, target = "execution_traces", in.Trace
			case "source_creator", "source_trace", "source_event", "source_bytes":
				col, target = "sources", in.Evidence[0]
			case "crew", "nonterminal", "unfinished", "run_revision", "run_missing":
				col, target = "agent_runs", Ref{Collection: "agent_runs", ID: "outcome-run", Revision: 1}
			}
			if mode == "run_missing" {
				if err := f.repo.Delete(testCtx, col, scopeA, target.ID, 1); err != nil {
					t.Fatal(err)
				}
			} else {
				dynamicsOutcomeMutate(t, f, col, target, func(r *Record) {
					switch mode {
					case "creator", "trace_creator", "source_creator":
						r.CreatedBy = "checker"
					case "approver":
						r.ApprovedBy = "checker"
					case "crew":
						run := *r.Run
						run.TaskType = "crew_run"
						r.Run = &run
					case "nonterminal":
						run := *r.Run
						run.Outcome = "running"
						r.Run = &run
					case "unfinished":
						run := *r.Run
						run.FinishedAt = nil
						r.Run = &run
					case "run_revision":
						r.Revision++
					case "source_trace":
						r.TraceID = "wrong"
					case "source_event":
						r.SourceEventID = ""
					case "source_bytes":
						r.Text = `{"memory_context":{"items":[]}}`
					case "trace_membership":
						r.Sources = nil
					}
				})
			}
			out, err := f.svc.PrepareMemoryOutcomeIntent(testCtx, "consumer", ref, in)
			dynamicsOutcomeZero(t, out, err)
		})
	}
}

func TestDynamicsOutcomeCurrentConsumerAuthorization(t *testing.T) {
	for _, mode := range []string{"human", "pipeline", "unknown", "collection", "runs", "evaluation", "target", "trace", "source", "principal_change", "role_change", "scope_change", "provider_error"} {
		t.Run(mode, func(t *testing.T) {
			f, ref, in := dynamicsOutcomeAccepted(t, false)
			base := f.auth
			call := 0
			f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, req Authorization) (Decision, error) {
				call++
				decision, err := base.Authorize(ctx, token, req)
				if err != nil {
					return decision, err
				}
				switch mode {
				case "human":
					decision.Role = "human"
				case "pipeline":
					decision.Role = "pipeline"
				case "unknown":
					return Decision{}, ErrDenied
				case "collection":
					if req.Action == "read:evaluations" && req.ResourceID == "" {
						decision.Allowed = false
					}
				case "runs":
					if req.Action == "read:agent_runs" {
						decision.Allowed = false
					}
				case "evaluation":
					if req.Action == "read:evaluations" && req.ResourceID == ref.ID {
						decision.Allowed = false
					}
				case "target":
					if req.Action == "read:memories" {
						decision.Allowed = false
					}
				case "trace":
					if req.Action == "read:execution_traces" {
						decision.Allowed = false
					}
				case "source":
					if req.Action == "read:sources" {
						decision.Allowed = false
					}
				case "principal_change":
					if call > 1 {
						decision.PrincipalID = "other"
					}
				case "role_change":
					if call > 1 {
						decision.Role = "human"
					}
				case "scope_change":
					if call > 1 {
						decision.Scope = scopeB
					}
				case "provider_error":
					return Decision{}, errors.New("policy provider unavailable")
				}
				return decision, nil
			})
			out, err := f.svc.PrepareMemoryOutcomeIntent(testCtx, "consumer", ref, in)
			dynamicsOutcomeZero(t, out, err)
		})
	}
}

func TestDynamicsOutcomeNoWritesOwnsReferencesAndPreservesRetryTime(t *testing.T) {
	f, ref, in := dynamicsOutcomeAccepted(t, false)
	before := f.repo.snapshot()
	input := in
	input.Evidence = slices.Clone(in.Evidence)
	r := &dynamicsOutcomeRepo{Repository: f.repo}
	f.svc.Repo = r
	first, err := f.svc.PrepareMemoryOutcomeIntent(testCtx, "consumer", ref, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.svc.PrepareMemoryOutcomeIntent(testCtx, "consumer", ref, in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(in, input) || !reflect.DeepEqual(before, f.repo.snapshot()) || r.domainWrites != 0 {
		t.Fatal("read mutated state or time")
	}
	first.Evidence[0].ID = "changed-output"
	first.AcceptedClock.UnixNS = new(int64)
	third, err := f.svc.PrepareMemoryOutcomeIntent(testCtx, "consumer", ref, in)
	if err != nil || !reflect.DeepEqual(third, second) {
		t.Fatal("output aliases state")
	}
}

type dynamicsOutcomeRepo struct {
	Repository
	mode          string
	err           error
	getCollection string
	cancel        context.CancelFunc
	domainWrites  int
}

func (r *dynamicsOutcomeRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	if r.mode == "before" {
		return r.err
	}
	invoke := func(ctx context.Context, tx Tx) error { return fn(ctx, dynamicsOutcomeTx{Tx: tx, owner: r}) }
	if err := r.Repository.Transaction(ctx, invoke); err != nil {
		return err
	}
	if r.mode == "commit" {
		return r.err
	}
	if r.mode == "retry" {
		r.getCollection = "evaluations"
		return r.Repository.Transaction(ctx, invoke)
	}
	if r.mode == "cancel" {
		r.cancel()
	}
	return nil
}

type dynamicsOutcomeTx struct {
	Tx
	owner *dynamicsOutcomeRepo
}

func (x dynamicsOutcomeTx) Get(ctx context.Context, col string, scope Scope, id string) (Record, error) {
	if col == x.owner.getCollection {
		return Record{}, x.owner.err
	}
	return x.Tx.Get(ctx, col, scope, id)
}
func (x dynamicsOutcomeTx) Insert(ctx context.Context, col string, r Record) error {
	if col != "audit_events" {
		x.owner.domainWrites++
	}
	return x.Tx.Insert(ctx, col, r)
}
func (x dynamicsOutcomeTx) Replace(ctx context.Context, col string, r Record, n int64) error {
	x.owner.domainWrites++
	return x.Tx.Replace(ctx, col, r, n)
}
func (x dynamicsOutcomeTx) Delete(ctx context.Context, col string, s Scope, id string, n int64) error {
	x.owner.domainWrites++
	return x.Tx.Delete(ctx, col, s, id, n)
}
func (x dynamicsOutcomeTx) FenceSource(context.Context, Scope, string) error {
	x.owner.domainWrites++
	return ErrUnavailable
}
func (x dynamicsOutcomeTx) FenceRelationMember(context.Context, Scope, string, string) error {
	x.owner.domainWrites++
	return ErrUnavailable
}

func TestDynamicsOutcomeStoreCommitRetryAndCancellationFailures(t *testing.T) {
	failure := errors.New("isolated store error")
	for _, mode := range []string{"before", "commit", "retry", "get", "cancel", "pre_cancel", "nil_repo"} {
		t.Run(mode, func(t *testing.T) {
			f, ref, in := dynamicsOutcomeAccepted(t, false)
			ctx, cancel := context.WithCancel(testCtx)
			defer cancel()
			r := &dynamicsOutcomeRepo{Repository: f.repo, mode: mode, err: failure, cancel: cancel}
			if mode == "get" {
				r.getCollection = "sources"
			}
			f.svc.Repo = r
			want := failure
			if mode == "pre_cancel" {
				cancel()
				want = context.Canceled
			}
			if mode == "cancel" {
				want = context.Canceled
			}
			if mode == "nil_repo" {
				f.svc.Repo = nil
				want = ErrUnavailable
			}
			out, err := f.svc.PrepareMemoryOutcomeIntent(ctx, "consumer", ref, in)
			dynamicsOutcomeZero(t, out, err)
			if !errors.Is(err, want) {
				t.Fatal("lost error identity", mode, err)
			}
		})
	}
}

func TestDynamicsOutcomeNativeClockRepresentability(t *testing.T) {
	min := time.Unix(0, -1<<63).UTC()
	max := time.Unix(0, 1<<63-1).UTC()
	for _, item := range []struct {
		name  string
		at    time.Time
		known bool
	}{{"min", min, true}, {"max", max, true}, {"before", min.Add(-time.Nanosecond), false}, {"after", max.Add(time.Nanosecond), false}, {"epoch", time.Unix(0, 0).UTC(), true}} {
		t.Run(item.name, func(t *testing.T) {
			f, ref, in := dynamicsOutcomeAccepted(t, false)
			dynamicsOutcomeMutate(t, f, "evaluations", ref, func(r *Record) { r.CreatedAt = item.at; r.UpdatedAt = item.at })
			out, err := f.svc.PrepareMemoryOutcomeIntent(testCtx, "consumer", ref, in)
			if err != nil {
				t.Fatal(err)
			}
			if !out.ServerCreatedAt.Equal(item.at) {
				t.Fatal("server clock changed")
			}
			if item.known {
				if out.AcceptedClock.State != "known" || out.AcceptedClock.UnixNS == nil || *out.AcceptedClock.UnixNS != item.at.UnixNano() {
					t.Fatal("incorrect exact NS")
				}
			} else if out.AcceptedClock.State != "outside_int64_ns" || out.AcceptedClock.UnixNS != nil {
				t.Fatal("outside clock overflowed")
			}
		})
	}
	if c := nativeOutcomeClock(time.Time{}); c.State != "native_zero" || c.UnixNS != nil {
		t.Fatal("zero clock changed")
	}
}

func TestDynamicsOutcomeOutputContainsNoBodiesOrCheckerAuthority(t *testing.T) {
	f, ref, in := dynamicsOutcomeAccepted(t, false)
	out, err := f.svc.PrepareMemoryOutcomeIntent(testCtx, "consumer", ref, in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"actual_test_exit", "Independent pinned memory checker", "retrieved", "synthetic_independent"} {
		if strings.Contains(string(b), body) {
			t.Fatal("body or unsupported authority emitted")
		}
	}
	if out.ProcessingSeq != nil || out.CheckerHostBytesStatus != "not_observed_by_server" {
		t.Fatal("invented evidence")
	}
}

func TestDynamicsOutcomeDenialAuditIsNotSuppressed(t *testing.T) {
	for _, failAudit := range []bool{false, true} {
		t.Run(map[bool]string{false: "stored", true: "failure"}[failAudit], func(t *testing.T) {
			f, ref, in := dynamicsOutcomeAccepted(t, false)
			before := len(f.repo.rows("audit_events"))
			f.auth.actors["consumer"] = actor("read-consumer", roleService, false)
			f.repo.failAudit = failAudit
			out, err := f.svc.PrepareMemoryOutcomeIntent(testCtx, "consumer", ref, in)
			dynamicsOutcomeZero(t, out, err)
			if failAudit {
				if !errors.Is(err, ErrUnavailable) {
					t.Fatal("audit failure suppressed")
				}
			} else if len(f.repo.rows("audit_events")) <= before {
				t.Fatal("authorization denial auditing suppressed")
			}
		})
	}
}

func TestDynamicsOutcomeConcurrentReadSnapshotsOwnOutputs(t *testing.T) {
	f, ref, in := dynamicsOutcomeAccepted(t, false)
	type result struct {
		out NativeOutcomeIntent
		err error
	}
	channel := make(chan result, 16)
	for range 16 {
		go func() {
			out, err := f.svc.PrepareMemoryOutcomeIntent(testCtx, "consumer", ref, in)
			channel <- result{out, err}
		}()
	}
	rows := make([]NativeOutcomeIntent, 0, 16)
	for range 16 {
		r := <-channel
		if r.err != nil {
			t.Fatal(r.err)
		}
		rows = append(rows, r.out)
	}
	for _, r := range rows[1:] {
		if !reflect.DeepEqual(rows[0], r) {
			t.Fatal("parallel snapshot differs")
		}
	}
	rows[0].Evidence[0].ID = "mutated"
	for _, r := range rows[1:] {
		if r.Evidence[0] != in.Evidence[0] {
			t.Fatal("parallel outputs alias")
		}
	}
}

func TestDynamicsOutcomePureProjectionOwnsPrivateSnapshot(t *testing.T) {
	f, ref, in := dynamicsOutcomeAccepted(t, false)
	snapshot, err := f.svc.loadNativeOutcomeSnapshot(testCtx, "consumer", ref, in)
	if err != nil {
		t.Fatal(err)
	}
	first := nativeOutcomeIntent(snapshot)
	first.Evidence[0].ID = "changed"
	second := nativeOutcomeIntent(snapshot)
	if second.Evidence[0] != in.Evidence[0] || snapshot.original.Evidence[0] != in.Evidence[0] {
		t.Fatal("projection mutated validated snapshot")
	}
	in.Evidence[0].ID = "changed-input"
	if second.Evidence[0].ID == in.Evidence[0].ID || snapshot.original.Evidence[0].ID == in.Evidence[0].ID {
		t.Fatal("snapshot aliases caller")
	}
}
