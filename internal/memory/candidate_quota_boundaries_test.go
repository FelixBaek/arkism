package memory

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

// 기존 메모리 저장소 대역에 장애를 주입하며 실제 DB에는 접속하지 않는다.
type quotaFaultTx struct {
	fakeTx
	op  string
	err error
}

func (tx quotaFaultTx) Get(ctx context.Context, col string, scope Scope, id string) (Record, error) {
	if tx.op == "get" {
		return Record{}, tx.err
	}
	return tx.fakeTx.Get(ctx, col, scope, id)
}

func (tx quotaFaultTx) Insert(ctx context.Context, col string, row Record) error {
	if tx.op == "insert" {
		return tx.err
	}
	return tx.fakeTx.Insert(ctx, col, row)
}

func (tx quotaFaultTx) Replace(ctx context.Context, col string, row Record, revision int64) error {
	if tx.op == "replace" {
		return tx.err
	}
	return tx.fakeTx.Replace(ctx, col, row, revision)
}

// 저장소별 원자적 할당 구현에는 원래 문맥·범위·시각·한도를 그대로 전달한다.
type quotaDelegateTx struct {
	Tx
	reserve func(context.Context, Scope, time.Time, int) error
}

func (tx quotaDelegateTx) ReserveCandidateQuota(ctx context.Context, scope Scope, at time.Time, limit int) error {
	return tx.reserve(ctx, scope, at, limit)
}

func TestCandidateQuotaDelegatesAndPropagatesError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for _, want := range []error{nil, ErrCandidateQuota, ErrUnavailable} {
		calls := 0
		tx := quotaDelegateTx{reserve: func(gotCtx context.Context, scope Scope, stamp time.Time, limit int) error {
			calls++
			if gotCtx != ctx || scope != scopeA || !stamp.Equal(at) || limit != 7 {
				t.Fatal("delegated arguments changed")
			}
			return want
		}}
		if err := reserveCandidate(ctx, tx, scopeA, at, 7); !errors.Is(err, want) || calls != 1 {
			t.Fatalf("error %v, want %v, calls %d", err, want, calls)
		}
	}
}

// 한도 밖 값은 DB 조회 이전에 거부하고, 1과 100000은 정확히 허용한다.
func TestCandidateQuotaLimitBoundaries(t *testing.T) {
	for _, limit := range []int{math.MinInt, -1, 0, 100001, math.MaxInt} {
		if err := reserveCandidate(testCtx, nil, scopeA, time.Time{}, limit); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
	for _, limit := range []int{1, 100000} {
		tx := fakeTx{store: map[string]map[string]Record{}}
		if err := reserveCandidate(testCtx, tx, scopeA, time.Time{}, limit); err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		row, err := tx.Get(testCtx, "jobs", candidateQuotaScope, "candidate-quota-0001-01-01")
		if err != nil || row.QuotaUsed != 1 || row.Revision != 1 {
			t.Fatalf("initial quota: %+v, %v", row, err)
		}
	}
}

// 조회·삽입·갱신 실패는 원인을 유지하며 저장된 사용량을 바꾸지 않는다.
func TestCandidateQuotaStorageFailures(t *testing.T) {
	at := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	for _, op := range []string{"get", "insert", "replace"} {
		t.Run(op, func(t *testing.T) {
			base := fakeTx{store: map[string]map[string]Record{}}
			if op == "replace" {
				if err := reserveCandidate(testCtx, base, scopeA, at, 2); err != nil {
					t.Fatal(err)
				}
			}
			before := structuredText(base.store)
			failure := errors.New("injected " + op + " failure")
			if err := reserveCandidate(testCtx, quotaFaultTx{base, op, failure}, scopeA, at, 2); !errors.Is(err, failure) {
				t.Fatalf("error %v, want %v", err, failure)
			}
			if structuredText(base.store) != before {
				t.Fatal("failed operation changed stored quota")
			}
		})
	}
}

// 손상된 할당 레코드를 정상 사용량으로 해석하거나 덮어쓰지 않는다.
func TestCandidateQuotaCorruptRecord(t *testing.T) {
	at := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*Record){
		"wrong kind":        func(r *Record) { r.Kind = "other" },
		"wrong status":      func(r *Record) { r.Status = "pending" },
		"zero used":         func(r *Record) { r.QuotaUsed = 0 },
		"negative used":     func(r *Record) { r.QuotaUsed = -1 },
		"zero revision":     func(r *Record) { r.Revision = 0 },
		"negative revision": func(r *Record) { r.Revision = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			row := Record{ID: "candidate-quota-2026-10-11", Scope: candidateQuotaScope, Kind: "candidate_quota", Status: "done", Revision: 1, QuotaUsed: 1}
			mutate(&row)
			tx := fakeTx{store: map[string]map[string]Record{"jobs": {recKey(candidateQuotaScope, row.ID): row}}}
			if err := reserveCandidate(testCtx, tx, scopeA, at, 2); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("error %v", err)
			}
			got, err := tx.Get(testCtx, "jobs", candidateQuotaScope, row.ID)
			if err != nil || !reflect.DeepEqual(got, row) {
				t.Fatalf("corrupt row overwritten: %+v, %v", got, err)
			}
		})
	}
}
