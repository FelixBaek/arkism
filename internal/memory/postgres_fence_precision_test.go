package memory

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func fenceStorageSnapshot(t *testing.T, p *Postgres, collection, id string) (string, map[string]json.RawMessage) {
	t.Helper()
	var columns, doc string
	var payload []byte
	err := p.q().QueryRow(context.Background(), `SELECT jsonb_build_object('revision',revision,'status',status,'kind',kind,'updated_at',updated_at,'run_at',run_at,'lease_until',lease_until,'lease_token',lease_token,'attempts',attempts)::text,payload,doc::text FROM memory_records WHERE collection=$1 AND id=$2`, collection, id).Scan(&columns, &payload, &doc)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	var a, b any
	if json.Unmarshal(payload, &fields) != nil || json.Unmarshal(payload, &a) != nil || json.Unmarshal([]byte(doc), &b) != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("payload/doc mismatch")
	}
	return columns, fields
}

func TestPostgresReadFencePreservesStorageTimestamps(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	// Insert keeps these microseconds in indexed SQL columns, while the storage
	// codec represents dates in milliseconds. A fence must not rewrite either.
	at := time.Date(2026, 11, 8, 1, 2, 3, 123456000, time.UTC)
	runAt, leaseUntil := at.Add(time.Hour+111*time.Microsecond), at.Add(2*time.Hour+222*time.Microsecond)
	for _, tc := range []struct {
		name, col string
		fence     func(string, int64) error
		run       bool
	}{
		{"source", "sources", func(id string, _ int64) error { return p.FenceSource(ctx, scopeA, id) }, false},
		{"search", colMemories, func(id string, rev int64) error { return p.FenceSearchRead(ctx, scopeA, colMemories, id, rev) }, false},
		{"relation", colMemories, func(id string, _ int64) error { return p.FenceRelationMember(ctx, scopeA, colMemories, id) }, false},
		{"run", "agent_runs", func(id string, rev int64) error { return p.FenceRunCall(ctx, scopeA, id, rev) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "precision-" + tc.name
			r := Record{ID: id, Scope: scopeA, Kind: tc.name, Status: statusActive, Revision: 7, Text: "Copper pump inspection", CreatedAt: at, UpdatedAt: at, RunAt: &runAt, LeaseUntil: leaseUntil, LeaseToken: "existing-lease", Attempts: 4, MemoryReferenceFence: 3, RunCallFence: 2}
			if err := p.Insert(ctx, tc.col, r); err != nil {
				t.Fatal(err)
			}
			_, fields := fenceStorageSnapshot(t, p, tc.col, id)
			fields["future_payload_field"] = json.RawMessage(`{"nested":["retain",19]}`)
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = p.q().Exec(ctx, `UPDATE memory_records SET payload=$1,doc=$2 WHERE collection=$3 AND id=$4`, raw, json.RawMessage(raw), tc.col, id); err != nil {
				t.Fatal(err)
			}
			before, original := fenceStorageSnapshot(t, p, tc.col, id)
			for n := 1; n <= 2; n++ {
				if err := tc.fence(id, 7); err != nil {
					t.Fatal(err)
				}
				after, current := fenceStorageSnapshot(t, p, tc.col, id)
				if before != after {
					t.Fatalf("read fence changed indexed state: before=%s after=%s", before, after)
				}
				got, err := p.Get(ctx, tc.col, scopeA, id)
				if err != nil {
					t.Fatal(err)
				}
				wantRun := int64(2)
				if tc.run {
					wantRun += int64(n)
				}
				if got.Revision != 7 || got.MemoryReferenceFence != 3+int64(n) || got.RunCallFence != wantRun {
					t.Fatal("counter or revision contract", got.Revision, got.MemoryReferenceFence, got.RunCallFence)
				}
				delete(current, "memory_reference_fence")
				delete(current, "run_call_fence")
				old := map[string]json.RawMessage{}
				for k, v := range original {
					if k != "memory_reference_fence" && k != "run_call_fence" {
						old[k] = v
					}
				}
				if !reflect.DeepEqual(old, current) {
					t.Fatal("fence rewrote unrelated payload fields")
				}
			}
			if tc.name == "search" || tc.run {
				if err := tc.fence(id, 6); !errors.Is(err, ErrConflict) {
					t.Fatal("stale revision accepted", err)
				}
				after, _ := fenceStorageSnapshot(t, p, tc.col, id)
				if before != after {
					t.Fatal("failed fence changed columns")
				}
			}
		})
	}
}

func TestPostgresReadFenceKeepsFTSOrder(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 8, 1, 2, 3, 123456000, time.UTC)
	for _, id := range []string{"precision-a", "precision-b"} {
		r := Record{ID: id, Scope: scopeA, Kind: "preference", Status: statusActive, Revision: 1, ApprovedBy: "reviewer", Text: "Copper pump inspection", CreatedAt: at, UpdatedAt: at}
		if err := p.Insert(ctx, colMemories, r); err != nil {
			t.Fatal(err)
		}
	}
	read := func() []string {
		rows, err := p.SearchCollection(ctx, colMemories, SearchInput{Scope: scopeA, Query: "copper pump inspection", Modes: []string{"fts"}, Limit: 10}, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, lane := range rows {
			for _, h := range lane {
				order = append(order, h.ID)
			}
		}
		return order
	}
	before := read()
	if !reflect.DeepEqual(before, []string{"precision-a", "precision-b"}) {
		t.Fatal("fixture order", before)
	}
	for range 3 {
		if err := p.FenceSearchRead(ctx, scopeA, colMemories, "precision-a", 1); err != nil {
			t.Fatal(err)
		}
		if after := read(); !reflect.DeepEqual(before, after) {
			t.Fatal("read changed FTS ranking", before, after)
		}
	}
}
