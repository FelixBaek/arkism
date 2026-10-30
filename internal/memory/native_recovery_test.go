package memory

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func nativeRecoveryUnitSnapshot() NativeRecoverySnapshot {
	h := strings.Repeat("a", 64)
	return NativeRecoverySnapshot{nativeRecoveryProtocol, h, []NativeRecoveryScope{{scopeA, 1, NativeRecoveryFields{h, h, h, h, h, h}}}}
}

func TestDynamicsRecoverySixFieldsStayDistinctAndRequired(t *testing.T) {
	seed := nativeRecoveryUnitSnapshot()
	if !nativeRecoverySnapshotValid(seed) {
		t.Fatal("valid snapshot rejected")
	}
	fields := reflect.ValueOf(&seed.Scopes[0].Fields).Elem()
	if fields.NumField() != 6 {
		t.Fatal("six restoration domains changed")
	}
	for i := 0; i < fields.NumField(); i++ {
		name := fields.Type().Field(i).Name
		t.Run(name, func(t *testing.T) {
			snapshot := nativeRecoveryUnitSnapshot()
			target := reflect.ValueOf(&snapshot.Scopes[0].Fields).Elem().Field(i)
			for _, invalid := range []string{"", "unknown", strings.Repeat("0", 63)} {
				target.SetString(invalid)
				if nativeRecoverySnapshotValid(snapshot) {
					t.Fatal("missing domain accepted")
				}
			}
		})
	}
	empty := seed
	empty.Scopes = []NativeRecoveryScope{}
	if !nativeRecoverySnapshotValid(empty) {
		t.Fatal("known empty projection rejected")
	}
	empty.Scopes = nil
	if nativeRecoverySnapshotValid(empty) {
		t.Fatal("unknown projection treated as empty")
	}
	duplicate := nativeRecoveryUnitSnapshot()
	duplicate.Scopes = append(duplicate.Scopes, duplicate.Scopes[0])
	if nativeRecoverySnapshotValid(duplicate) {
		t.Fatal("duplicate scope accepted")
	}
}

func TestDynamicsLifecycleOrdinaryEvalReferencesRemainUnsupported(t *testing.T) {
	for _, collection := range []string{colEvalTasks, colEvalRuns, colEvalTrials, colJudgeCalibrations} {
		t.Run(collection, func(t *testing.T) {
			if nativeLifecycleProofSupported(Record{}, collection) {
				t.Fatal("ordinary evaluation metadata became native qualification")
			}
		})
	}
	for _, field := range []string{"spec", "trial", "calibration"} {
		t.Run(field, func(t *testing.T) {
			r := Record{Evaluation: &Evaluation{EvalType: "automated_check"}}
			ref := &Ref{Collection: colEvalTasks, ID: "synthetic", Revision: 1}
			switch field {
			case "spec":
				r.Evaluation.SpecRef = ref
			case "trial":
				r.Evaluation.TrialRef = ref
			case "calibration":
				r.Evaluation.CalibrationRef = ref
			}
			if nativeLifecycleProofSupported(r, "evaluations") {
				t.Fatal("unsupported extra edge silently omitted")
			}
		})
	}
	if !nativeLifecycleProofSupported(Record{Evaluation: &Evaluation{EvalType: "automated_check"}}, "evaluations") {
		t.Fatal("canonical nil refs rejected")
	}
}

type nativeRestoreGateFixture struct{ Repository }

func (nativeRestoreGateFixture) RequireRestoreReview(context.Context, string) error { return nil }
func (nativeRestoreGateFixture) CheckRestorePublication(context.Context) error      { return ErrUnavailable }

func TestDynamicsLifecycleRestoreGateProtectsDirectNativeCalls(t *testing.T) {
	f, in := nativeFixture(t)
	nativeActivate(t, f, in, "restore-gate")
	_, capture := nativeAccept(t, f, in)
	before := len(f.repo.rows(colJobs))
	f.svc.Repo = nativeRestoreGateFixture{f.repo}
	t.Run("read", func(t *testing.T) {
		out, err := f.svc.ReadNativeDynamicsSnapshot(testCtx, "native", NativeDynamicsQuery{in.Scope, in.Target, in.Dimension, "restore-gate", time.Now().UnixNano()})
		if !errors.Is(err, ErrUnavailable) || out.Value != nil {
			t.Fatal("direct read bypassed restore review", err)
		}
	})
	t.Run("apply", func(t *testing.T) {
		out, err := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", in.Scope, capture)
		if !errors.Is(err, ErrUnavailable) || out.AppliedNow {
			t.Fatal("direct apply bypassed restore review", err)
		}
	})
	t.Run("activation", func(t *testing.T) {
		out, err := f.svc.ActivateNativeMemoryDynamics(testCtx, "native", NativeDynamicsActivationInput{in.Scope, in.Target, in.Dimension, nativeTestConfig("restore-new"), "restore-new"})
		if !errors.Is(err, ErrUnavailable) || out.AppliedNow {
			t.Fatal("direct activation bypassed review", err)
		}
	})
	if len(f.repo.rows(colJobs)) != before {
		t.Fatal("blocked calls wrote native data")
	}
	t.Run("denied_actor_first", func(t *testing.T) {
		_, err := f.svc.ReadNativeDynamicsSnapshot(testCtx, f.agent, NativeDynamicsQuery{in.Scope, in.Target, in.Dimension, "restore-gate", time.Now().UnixNano()})
		if !errors.Is(err, ErrDenied) {
			t.Fatal("existing permission rejection order changed", err)
		}
	})
}
