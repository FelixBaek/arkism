package memory

import (
	"context"
	"errors"
	dyn "github.com/FelixBaek/arkism/internal/memoryharness/dynamicsv2"
	"testing"
)

type searchPublicationGateRepo struct {
	Repository
	gate          error
	checks, reads int
}

func (r *searchPublicationGateRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return fn(ctx, &searchPublicationGateTx{repo: r})
}

type searchPublicationGateTx struct {
	Tx
	repo *searchPublicationGateRepo
}

func (t *searchPublicationGateTx) RequireRestoreReview(context.Context, string) error { return nil }
func (t *searchPublicationGateTx) CheckRestorePublication(context.Context) error {
	t.repo.checks++
	return t.repo.gate
}
func (t *searchPublicationGateTx) Get(context.Context, string, Scope, string) (Record, error) {
	t.repo.reads++
	return Record{}, ErrNotFound
}
func TestNativeSearchPublicationChecksAuthoritativeRestoreGate(t *testing.T) {
	for _, held := range []bool{true, false} {
		name := "open"
		if held {
			name = "held"
		}
		t.Run(name, func(t *testing.T) {
			repo := &searchPublicationGateRepo{}
			if held {
				repo.gate = ErrUnavailable
			}
			svc := &Service{Repo: repo}
			values, bindings, err := svc.nativeSearchReadValues(context.Background(), SearchInput{}, []nativeSearchSourcePreview{{row: Record{ID: "existing-state"}}}, dyn.PolicyEntry{}, nil)
			want := error(ErrNotFound)
			reads := 1
			if held {
				want = ErrUnavailable
				reads = 0
			}
			if !errors.Is(err, want) || repo.checks != 1 || repo.reads != reads || len(values) != 0 || len(bindings) != 0 {
				t.Fatalf("gate held=%v error=%v checks=%d reads=%d values=%d bindings=%d", held, err, repo.checks, repo.reads, len(values), len(bindings))
			}
		})
	}
}
