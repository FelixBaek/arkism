package memory

import (
	"context"
	"errors"
	"log/slog"
)

// Extensions let a program that embeds this package add its own checks and
// reactions without a fork: set the fields on Service before serving. There is
// no plugin loader; each hook is plain Go code compiled into that program.
// Nil hooks are skipped. Hooks run in the request's goroutine, so keep them fast.
//
// Hooks cannot approve anything: a capture still lands as a candidate a person
// reviews, and AfterApprove only observes an approval a person already made.
type Extensions struct {
	// CaptureValidator may refuse a capture after its built-in checks pass
	// (scope, privacy and secret filters still run first).
	CaptureValidator CaptureValidator
	// JobHook may hold back a queued embedding or webhook job; the job is
	// retried later like a failed attempt. Erasure jobs never pass through it.
	JobHook JobHook
	// ApproveHook hears every committed candidate approval.
	ApproveHook ApproveHook
}

// CaptureValidator checks a capture request; an error refuses the whole request.
type CaptureValidator interface {
	ValidateCapture(ctx context.Context, actor Decision, in CaptureInput) error
}

// JobHook runs before the worker starts a claimed job; an error defers the job.
type JobHook interface {
	BeforeJob(ctx context.Context, job Record) error
}

// ApproveHook is told about an approval after it is committed. It cannot undo
// the approval; a panic in it is contained and logged.
type ApproveHook interface {
	AfterApprove(ctx context.Context, approved Record)
}

// ErrExtensionRefused wraps a hook's refusal; it answers like ErrInvalid.
var ErrExtensionRefused = errors.New("refused by extension")

// guard runs a refusing hook; a panic counts as a refusal (fail closed).
func guard(name string, hook func() error) (err error) {
	defer func() {
		if recover() != nil {
			slog.Error("extension hook panicked", "hook", name)
			err = ErrExtensionRefused
		}
	}()
	return hook()
}

func (x Extensions) validateCapture(ctx context.Context, actor Decision, in CaptureInput) error {
	if x.CaptureValidator == nil {
		return nil
	}
	if err := guard("capture_validator", func() error { return x.CaptureValidator.ValidateCapture(ctx, actor, in) }); err != nil {
		slog.Info("capture refused by extension", "error", captureErrorCode(err))
		return errors.Join(ErrInvalid, ErrExtensionRefused)
	}
	return nil
}

func (x Extensions) beforeJob(ctx context.Context, job Record) error {
	if x.JobHook == nil {
		return nil
	}
	if err := guard("job_hook", func() error { return x.JobHook.BeforeJob(ctx, job) }); err != nil {
		return errors.Join(ErrUnavailable, ErrExtensionRefused)
	}
	return nil
}

// afterTransition tells ApproveHook about a committed candidate approval.
func (x Extensions) afterTransition(ctx context.Context, col, status string, approved Record) {
	if x.ApproveHook == nil || col != "memory_candidates" || status != "approved" {
		return
	}
	defer func() {
		if recover() != nil {
			slog.Error("approve hook panicked", "id", approved.ID)
		}
	}()
	x.ApproveHook.AfterApprove(context.WithoutCancel(ctx), approved)
}
