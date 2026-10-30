package memory

// Task: SYS-02. Accepted native observations are prepared metadata, not learning events.
import (
	"context"
	"slices"
	"time"
)

// NativeOutcomeClock preserves time representability without a lossy UnixNano conversion.
type NativeOutcomeClock struct {
	State  string `json:"state"`
	UnixNS *int64 `json:"unix_ns"`
}

// NativeOutcomeIntent is an authorized read snapshot, never a committed dynamics event.
// Currentness and permission must be checked again at any future durable commit.
type NativeOutcomeIntent struct {
	Protocol               string             `json:"protocol"`
	Phase                  string             `json:"phase"`
	Scope                  Scope              `json:"scope"`
	AcceptedEvaluation     Ref                `json:"accepted_evaluation"`
	NativeAPIEventID       string             `json:"native_api_event_id"`
	NativeRequestSHA256    string             `json:"native_request_sha256"`
	Target                 Ref                `json:"target"`
	Trace                  Ref                `json:"trace"`
	Evidence               []Ref              `json:"evidence"`
	EvaluatorPrincipal     string             `json:"evaluator_principal"`
	Dimension              string             `json:"dimension"`
	CheckerSHA256          string             `json:"checker_sha256"`
	CheckerHostBytesStatus string             `json:"checker_host_bytes_status"`
	ServerCreatedAt        time.Time          `json:"server_created_at"`
	AcceptedClock          NativeOutcomeClock `json:"accepted_clock"`
	SourceObservedAt       NativeOutcomeClock `json:"source_observed_at"`
	ServerReturn           string             `json:"server_return"`
	ContextDelivery        string             `json:"context_delivery"`
	ActualUse              string             `json:"actual_use"`
	TaskSuccess            string             `json:"task_success"`
	Attribution            string             `json:"attribution"`
	Strength               *float64           `json:"strength"`
	TaskRef                *Ref               `json:"task_ref"`
	TrialRef               *Ref               `json:"trial_ref"`
	ProcessingSeq          *int64             `json:"processing_seq"`
	LearningQualification  string             `json:"learning_qualification"`
	LogicalConsumptionKey  *string            `json:"logical_consumption_key"`
	Authority              string             `json:"authority"`
}

// acceptedNativeOutcome is constructed only after the service resolves current store evidence.
// It carries no source bodies and no caller-supplied authentication boolean.
type acceptedNativeOutcome struct {
	evaluation Ref
	original   MemoryOutcomeInput
	evaluator  string
	createdAt  time.Time
	requestSHA string
}

// PrepareMemoryOutcomeIntent resolves an exact accepted evaluation and original request.
// It does not create evaluations, jobs, state, event sequences or evidence consumption.
func (s *Service) PrepareMemoryOutcomeIntent(ctx context.Context, token string, evaluation Ref, original MemoryOutcomeInput) (NativeOutcomeIntent, error) {
	snapshot, err := s.loadNativeOutcomeSnapshot(ctx, token, evaluation, original)
	if err != nil {
		return NativeOutcomeIntent{}, err
	}
	return nativeOutcomeIntent(snapshot), nil
}

func (s *Service) loadNativeOutcomeSnapshot(ctx context.Context, token string, evaluation Ref, original MemoryOutcomeInput) (acceptedNativeOutcome, error) {
	if err := ctx.Err(); err != nil {
		return acceptedNativeOutcome{}, err
	}
	if evaluation.Collection != "evaluations" || evaluation.Revision != 1 || !validWorkCheckpointRef(evaluation) || !validMemoryOutcomeInput(original) {
		return acceptedNativeOutcome{}, ErrInvalid
	}
	if s == nil || s.Repo == nil {
		return acceptedNativeOutcome{}, ErrUnavailable
	}
	original.Evidence = slices.Clone(original.Evidence)
	consumer, err := s.authorize(ctx, token, "read:evaluations", original.Scope, "", "")
	if err != nil {
		return acceptedNativeOutcome{}, err
	}
	if consumer.Role != roleService {
		return acceptedNativeOutcome{}, ErrDenied
	}
	ctx = bindRequestActor(ctx, consumer)
	if _, err = s.authorize(ctx, token, "read:agent_runs", original.Scope, "", ""); err != nil {
		return acceptedNativeOutcome{}, err
	}
	refs := append(slices.Clone(original.Evidence), evaluation, original.Target, original.Trace)
	for _, ref := range refs {
		if !validWorkCheckpointRef(ref) || ref != evaluation && ref != original.Target && ref != original.Trace && ref.Collection != "sources" {
			return acceptedNativeOutcome{}, ErrInvalid
		}
		if _, err = s.authorize(ctx, token, "read:"+ref.Collection, original.Scope, ref.ID, ""); err != nil {
			return acceptedNativeOutcome{}, err
		}
	}
	var staged acceptedNativeOutcome
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		staged = acceptedNativeOutcome{}
		var callbackErr error
		staged, callbackErr = resolveAcceptedNativeOutcomeTx(ctx, tx, evaluation, original)
		return callbackErr
	})
	if err != nil {
		return acceptedNativeOutcome{}, err
	}
	if err := ctx.Err(); err != nil {
		return acceptedNativeOutcome{}, err
	}
	return staged, nil
}

func canonicalNativeOutcome(r Record, ref Ref, in MemoryOutcomeInput) bool {
	wantID := "memory-outcome-" + ContentHash(structuredText(in.Scope)+"\n"+r.CreatedBy+"\n"+in.EventID)
	return r.ID == ref.ID && r.ID == wantID && r.Status == "active" && r.Kind == "memory_outcome" &&
		r.CreatedBy != "" && r.CreatedByType == roleService && r.RequestHash == ContentHash(structuredText(in)) &&
		!r.CreatedAt.IsZero() && r.CreatedAt.Equal(r.UpdatedAt) && r.Target != nil && *r.Target == in.Target &&
		r.Text == "Independent pinned memory checker "+in.CheckerSHA256 && slices.Equal(r.Tags, []string{"dimension:" + in.Dimension}) &&
		nativeOutcomeEvaluation(r, in.Passed)
}

func nativeOutcomeEvaluation(r Record, passed bool) bool {
	return len(evaluationRefs(r.Evaluation)) == 0 && validEvaluation(r.Evaluation) && r.Evaluation.EvalType == "automated_check" && r.Evaluation.Passed != nil &&
		*r.Evaluation.Passed == passed && r.Passed == passed && r.Evaluation.Score == nil &&
		r.Evaluation.ScoreMax == nil && r.Evaluation.ComparisonReport == nil
}

func nativeOutcomeClock(at time.Time) NativeOutcomeClock {
	if at.IsZero() {
		return NativeOutcomeClock{State: "native_zero"}
	}
	minimumClock := time.Unix(0, -1<<63).UTC()
	maximumClock := time.Unix(0, 1<<63-1).UTC()
	if at.Before(minimumClock) || at.After(maximumClock) {
		return NativeOutcomeClock{State: "outside_int64_ns"}
	}
	ns := at.UnixNano()
	return NativeOutcomeClock{State: "known", UnixNS: &ns}
}

func nativeOutcomeIntent(s acceptedNativeOutcome) NativeOutcomeIntent {
	outcome := "observed_false"
	if s.original.Passed {
		outcome = "observed_true"
	}
	return NativeOutcomeIntent{
		Protocol: "arkism-native-outcome-intent-v1", Phase: "prepared_uncommitted", Scope: s.original.Scope,
		AcceptedEvaluation: s.evaluation, NativeAPIEventID: s.original.EventID, NativeRequestSHA256: s.requestSHA,
		Target: s.original.Target, Trace: s.original.Trace, Evidence: slices.Clone(s.original.Evidence),
		EvaluatorPrincipal: s.evaluator, Dimension: s.original.Dimension, CheckerSHA256: s.original.CheckerSHA256,
		CheckerHostBytesStatus: "not_observed_by_server", ServerCreatedAt: s.createdAt, AcceptedClock: nativeOutcomeClock(s.createdAt),
		SourceObservedAt: NativeOutcomeClock{State: "not_recorded"}, ServerReturn: "observed_true", ContextDelivery: "unknown",
		ActualUse: "unknown", TaskSuccess: outcome, Attribution: "unattributable", LearningQualification: "unsupported_native", Authority: AuthorityDataOnly,
	}
}
