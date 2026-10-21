package memory

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// HTTP now rejects absent scopes before dispatch. Workers and other direct
// callers still need to reject empty direct requests. Exercise every exported
// scoped/token API with available and missing dependencies, including additions.
func TestServiceAPIsRefuseEmptyScopedRequests(t *testing.T) {
	scopeType := reflect.TypeOf(Scope{})
	contextType := reflect.TypeOf((*context.Context)(nil)).Elem()
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	serviceType := reflect.TypeOf((*Service)(nil))
	covered := 0
	for i := 0; i < serviceType.NumMethod(); i++ {
		method := serviceType.Method(i)
		mt := method.Type
		if mt.NumIn() < 3 || mt.In(1) != contextType || mt.In(2).Kind() != reflect.String || mt.NumOut() == 0 || mt.Out(mt.NumOut()-1) != errorType {
			continue
		}
		scoped := false
		for j := 3; j < mt.NumIn(); j++ {
			scoped = scoped || carriesServiceScope(mt.In(j), scopeType, 3)
		}
		if !scoped {
			continue
		}
		covered++
		for _, repository := range []string{"available", "missing"} {
			t.Run(method.Name+"/"+repository, func(t *testing.T) {
				f := newFixture(t)
				// Workers may call the service without HTTP preflight. Available
				// dependencies must not turn an empty request into an operation.
				if repository == "missing" {
					f.svc.Repo = nil
				}
				ctx, cancel := context.WithTimeout(testCtx, time.Second)
				defer cancel()
				args := []reflect.Value{reflect.ValueOf(f.svc), reflect.ValueOf(ctx), reflect.ValueOf(f.human)}
				for j := 3; j < mt.NumIn(); j++ {
					args = append(args, reflect.Zero(mt.In(j)))
				}
				var out []reflect.Value
				if mt.IsVariadic() {
					out = method.Func.CallSlice(args)
				} else {
					out = method.Func.Call(args)
				}
				if out[len(out)-1].IsNil() {
					t.Fatal("missing scope succeeded")
				}
				if len(f.emb.calls) != 0 {
					t.Fatal("missing scope reached embedding provider")
				}
			})
		}
	}
	t.Logf("Checked %d scoped/token APIs with both dependency states", covered)
	if covered < 100 {
		t.Fatalf("scoped API inventory unexpectedly small: %d", covered)
	}
}

// A successful HTTP preflight is not a cached grant: a worker call or a later
// service check must still stop on a denial or policy outage. Only recording
// that denial is allowed; every operational repository method is a trap.
func TestServiceAPIsFailClosedWhenPolicyDeniesOrFails(t *testing.T) {
	scopeType := reflect.TypeOf(Scope{})
	contextType := reflect.TypeOf((*context.Context)(nil)).Elem()
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	serviceType := reflect.TypeOf((*Service)(nil))
	for i := 0; i < serviceType.NumMethod(); i++ {
		method := serviceType.Method(i)
		mt := method.Type
		if mt.NumIn() < 3 || mt.In(1) != contextType || mt.In(2).Kind() != reflect.String || mt.NumOut() == 0 || mt.Out(mt.NumOut()-1) != errorType {
			continue
		}
		scoped := false
		for j := 3; j < mt.NumIn(); j++ {
			scoped = scoped || carriesServiceScope(mt.In(j), scopeType, 3)
		}
		if !scoped {
			continue
		}
		for _, mode := range []string{"denied", "unavailable"} {
			t.Run(method.Name+"/"+mode, func(t *testing.T) {
				f := newFixture(t)
				f.svc.Repo = denialBoundaryRepo{}
				var calls atomic.Int64
				f.svc.Auth = authorizerFunc(func(context.Context, string, Authorization) (Decision, error) {
					calls.Add(1)
					if mode == "unavailable" {
						return Decision{}, ErrUnavailable
					}
					return Decision{}, ErrDenied
				})
				ctx, cancel := context.WithTimeout(testCtx, time.Second)
				defer cancel()
				args := []reflect.Value{reflect.ValueOf(f.svc), reflect.ValueOf(ctx), reflect.ValueOf("boundary-token")}
				for j := 3; j < mt.NumIn(); j++ {
					input := reflect.New(mt.In(j)).Elem()
					if input.Kind() == reflect.String {
						input.SetString("missing")
					}
					setBoundaryScope(input, scopeType, 3)
					args = append(args, input)
				}
				var out []reflect.Value
				if mt.IsVariadic() {
					out = method.Func.CallSlice(args)
				} else {
					out = method.Func.Call(args)
				}
				if calls.Load() > 0 && out[len(out)-1].IsNil() {
					t.Fatal("policy failure turned into success")
				}
				if len(f.emb.calls) != 0 {
					t.Fatal("policy failure reached embedding provider")
				}
			})
		}
	}
}

func setBoundaryScope(input reflect.Value, scope reflect.Type, depth int) {
	if input.Type() == scope {
		input.Set(reflect.ValueOf(scopeA))
		return
	}
	if depth == 0 || input.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < input.NumField(); i++ {
		field := input.Field(i)
		if field.CanSet() {
			setBoundaryScope(field, scope, depth-1)
		}
	}
}

type denialBoundaryRepo struct{ Repository }

func (denialBoundaryRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return fn(ctx, denialBoundaryTx{})
}

type denialBoundaryTx struct{ Tx }

func (denialBoundaryTx) Get(_ context.Context, collection string, _ Scope, _ string) (Record, error) {
	if collection != "audit_events" {
		panic("policy failure reached operational data read")
	}
	return Record{}, ErrNotFound
}
func (denialBoundaryTx) Insert(_ context.Context, collection string, record Record) error {
	if collection != "audit_events" || record.Audit == nil || record.Audit.Decision != "deny" {
		panic("policy failure reached operational data write")
	}
	return nil
}

func carriesServiceScope(input, scope reflect.Type, depth int) bool {
	if input == scope {
		return true
	}
	if depth == 0 {
		return false
	}
	if input.Kind() == reflect.Pointer || input.Kind() == reflect.Slice {
		return carriesServiceScope(input.Elem(), scope, depth-1)
	}
	if input.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < input.NumField(); i++ {
		if carriesServiceScope(input.Field(i).Type, scope, depth-1) {
			return true
		}
	}
	return false
}
