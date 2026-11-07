// memory-maintenance advances explicitly acknowledged global maintenance one
// durable step at a time. It never initializes schemas or offers a force-unlock.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/FelixBaek/arkism/internal/memory"
)

type maintenanceInput struct {
	privacy   *memory.PrivacyDeletionRequest
	supersede *memory.SupersedeInput
	token     string
}
type backend interface {
	Execute(context.Context, options, maintenanceInput) (any, error)
	Close()
}
type backendFactory func(context.Context, string) (backend, error)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if e := runArgs(ctx, os.Args[1:], os.Getenv, os.Stdout, openBackend); e != nil {
		fmt.Fprintln(os.Stderr, "memory maintenance failed:", errorCode(e))
		os.Exit(1)
	}
}
func errorCode(e error) string {
	switch {
	case errors.Is(e, errInput), errors.Is(e, memory.ErrInvalid):
		return "invalid_input"
	case errors.Is(e, memory.ErrDenied):
		return "permission_denied"
	case errors.Is(e, memory.ErrConflict):
		return "revision_or_state_conflict"
	case errors.Is(e, context.Canceled):
		return "cancelled"
	case errors.Is(e, context.DeadlineExceeded):
		return "timeout"
	default:
		return "unavailable"
	}
}
func runArgs(ctx context.Context, args []string, getenv func(string) string, out io.Writer, open backendFactory) error {
	o, help, e := parseOptions(args)
	if e != nil {
		return e
	}
	if help {
		_, e = io.WriteString(out, usage)
		return e
	}
	in := maintenanceInput{}
	if o.command == "begin-privacy" || o.command == "begin-supersede" {
		in.token, e = loadToken(o, getenv)
		if e != nil {
			return e
		}
		if o.command == "begin-privacy" {
			in.privacy = &memory.PrivacyDeletionRequest{}
			e = decodeRequest(o.request, in.privacy)
		} else {
			in.supersede = &memory.SupersedeInput{}
			e = decodeRequest(o.request, in.supersede)
		}
		if e != nil {
			return e
		}
	}
	dsn := getenv("MEMORY_DATABASE_URL")
	if dsn == "" {
		return errInput
	}
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	b, e := open(ctx, dsn)
	if e != nil {
		return e
	}
	defer b.Close()
	result, e := b.Execute(ctx, o, in)
	if e != nil {
		return e
	}
	return json.NewEncoder(out).Encode(result)
}
