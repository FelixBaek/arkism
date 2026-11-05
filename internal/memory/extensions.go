package memory

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// Extensions let a program add its own checks and reactions without a fork: set
// the fields on Service before serving, either with Go code compiled in or with
// LoadProcessExtensions (ext_runtime.go), which runs separate programs over
// stdio or HTTP and fills every field from a config file. Nil hooks are skipped.
// Hooks run in the request's goroutine, so keep them fast.
//
// Hooks cannot approve anything: a capture still lands as a candidate a person
// reviews, and AfterApprove only observes an approval a person already made.
type Extensions struct {
	// OperationHook may refuse capture, recall, reflect, consolidate and mental
	// model refresh/update before they run, and hears how each ended.
	OperationHook OperationHook
	// JobHook may hold back a queued embedding or webhook job; the job is
	// retried later like a failed attempt. Erasure jobs never pass through it.
	JobHook JobHook
	// ApproveHook hears every committed candidate approval.
	ApproveHook ApproveHook
	// RequestHook may refuse any HTTP request before its route runs (middleware).
	// The MCP router's tool-call hook is skills.ToolCallHook (internal/host/skills).
	RequestHook RequestHook
	// TenantHook may refuse a principal a space its grants allow (it only narrows).
	TenantHook TenantHook
	// Endpoints adds HTTP routes under /v1/ext/{extension}/ and MCP tools named
	// ext_{extension}_{tool}; Arkism authorizes every call first (http_core_ext.go).
	Endpoints ExtensionEndpoints
}

// Operations an OperationHook sees.
const (
	OpCapture          = "capture"
	OpRecall           = "recall"
	OpReflect          = "reflect"
	OpConsolidate      = "consolidate"
	OpMentalModelFresh = "mental_model_refresh"
	OpMentalModelEdit  = "mental_model_update"
)

// OperationHook checks an operation after its permission check; an error
// refuses it. AfterOperation hears the outcome (err is the operation's error)
// and cannot change it; a panic in it is contained.
type OperationHook interface {
	BeforeOperation(ctx context.Context, actor Decision, op string, input any) error
	AfterOperation(ctx context.Context, actor Decision, op string, input, result any, err error)
}

// TenantHook runs after every allowed permission decision; an error turns it into a denial.
type TenantHook interface {
	CheckTenant(ctx context.Context, actor Decision, action string) error
}

// ExtensionRoute is one HTTP route an extension serves and the permission it needs.
type ExtensionRoute struct {
	Method string   `json:"method"`
	Path   string   `json:"path"`
	Action string   `json:"action"`
	Roles  []string `json:"roles,omitempty"`
	// Data: the call gets the extension's stored entries for the space and may answer changes to them (ext_data.go).
	Data bool `json:"data,omitempty"`
}

// ExtensionTool is one MCP tool an extension serves; Name is the full ext_{extension}_{tool} name.
type ExtensionTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Action      string          `json:"-"`
	Data        bool            `json:"-"`
}

// ExtensionCall is an authorized call forwarded to an extension.
type ExtensionCall struct {
	Actor     Decision        `json:"-"`
	Scope     Scope           `json:"scope"`
	Extension string          `json:"-"`
	Method    string          `json:"method,omitempty"`
	Path      string          `json:"path,omitempty"`
	Query     string          `json:"query,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	Body      json.RawMessage `json:"body,omitempty"`
	// Data is the extension's stored entries in the space, set only for a route or tool that declared data.
	Data map[string]json.RawMessage `json:"data,omitempty"`
}

// ExtensionAnswer is an extension's answer to a route (status and JSON body) or
// tool call (status 200 and the result in Body), with the data changes it asks for.
type ExtensionAnswer struct {
	Status int
	Body   json.RawMessage
	Data   []ExtDataOp
}

// ExtensionEndpoints serves extension routes and tools.
type ExtensionEndpoints interface {
	// Route finds the declared route of extension for method and path.
	Route(extension, method, path string) (ExtensionRoute, bool)
	// Tools lists every extension tool.
	Tools() []ExtensionTool
	// Serve answers a route or tool call.
	Serve(ctx context.Context, call ExtensionCall) (ExtensionAnswer, error)
}

// RequestHook sees a copy of each HTTP request (method, URL, headers without
// credentials; the body is not readable) before routing and authorization; an
// error refuses it with 403.
type RequestHook interface {
	BeforeRequest(r *http.Request) error
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

// operationActions is the permission each hooked operation already asks for, so
// the hook sees the same actor the operation does.
var operationActions = map[string]string{OpCapture: "read:memory_candidates", OpRecall: "search:memories", OpReflect: "search:memories",
	OpConsolidate: "write:observations", OpMentalModelFresh: "write:mental_models", OpMentalModelEdit: "write:mental_models"}

// withOperation runs OperationHook around run. Without a hook it is run() alone.
// A refusal answers like ErrInvalid (capture keeps its old answer) or ErrDenied.
func withOperation[T any](s *Service, ctx context.Context, token, op string, scope Scope, input any, run func() (T, error)) (T, error) {
	hook := s.Extensions.OperationHook
	if hook == nil {
		return run()
	}
	var zero T
	actor, err := s.authorize(ctx, token, operationActions[op], scope, "", "")
	if err != nil {
		return zero, err
	}
	if err := guard("operation_hook", func() error { return hook.BeforeOperation(ctx, actor, op, input) }); err != nil {
		slog.Info("operation refused by extension", "operation", op)
		if op == OpCapture {
			return zero, errors.Join(ErrInvalid, ErrExtensionRefused)
		}
		return zero, errors.Join(ErrDenied, ErrExtensionRefused)
	}
	out, err := run()
	func() {
		defer func() {
			if recover() != nil {
				slog.Error("operation hook panicked", "operation", op)
			}
		}()
		hook.AfterOperation(context.WithoutCancel(ctx), actor, op, input, out, err)
	}()
	return out, err
}

// checkTenant runs TenantHook on an allowed decision; a refusal or a panic denies.
func (x Extensions) checkTenant(ctx context.Context, d Decision, action string) error {
	if x.TenantHook == nil {
		return nil
	}
	if err := guard("tenant_hook", func() error { return x.TenantHook.CheckTenant(ctx, d, action) }); err != nil {
		return errors.Join(ErrDenied, ErrExtensionRefused)
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
	for _, h := range []string{"Authorization", "Proxy-Authorization", "Cookie", "X-Api-Key"} {
		view.Header.Del(h) // Clone copied the header map, so the route still sees them
	}
	if err := guard("request_hook", func() error { return x.RequestHook.BeforeRequest(view) }); err != nil {
		slog.Info("request refused by extension", "method", r.Method, "path", r.URL.Path)
		return errors.Join(ErrDenied, ErrExtensionRefused)
	}
	return nil
}
