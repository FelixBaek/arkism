package memory

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
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
	// RequestHook may refuse any HTTP request before its route runs (middleware).
	// The MCP router's tool-call hook is skills.ToolCallHook (internal/host/skills).
	RequestHook RequestHook
}

// RequestHook sees a copy of each HTTP request (method, URL, headers; the body is
// not readable) before routing and authorization; an error refuses it with 403.
type RequestHook interface {
	BeforeRequest(r *http.Request) error
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

// beforeRequest runs RequestHook; a refusal or a panic answers 403 (fail closed).
func (x Extensions) beforeRequest(r *http.Request) error {
	if x.RequestHook == nil {
		return nil
	}
	view := r.Clone(r.Context())
	view.Body = http.NoBody
	if err := guard("request_hook", func() error { return x.RequestHook.BeforeRequest(view) }); err != nil {
		slog.Info("request refused by extension", "method", r.Method, "path", r.URL.Path)
		return errors.Join(ErrDenied, ErrExtensionRefused)
	}
	return nil
}
