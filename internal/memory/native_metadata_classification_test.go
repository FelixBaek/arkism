package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNativeMetadataClassificationConservative(t *testing.T) {
	now := time.Now().UTC()
	old := Record{ID: "m", Scope: scopeA, Revision: 1, Status: "active", Text: "rule", MemoryContext: &MemoryContext{Domains: []string{"work"}}}
	cases := []struct {
		name     string
		metadata bool
		edit     func(*Record)
	}{
		{"timestamp", true, func(r *Record) { r.UpdatedAt = now }},
		{"review schedule", true, func(r *Record) { r.ReviewDueAt = &now }},
		{"observed timestamp", true, func(r *Record) {
			r.MemoryContext = copyMemoryContext(r.MemoryContext)
			r.MemoryContext.ObservedAt = &now
		}},
		{"content", false, func(r *Record) { r.Text = "new" }},
		{"rule meaning", false, func(r *Record) { r.Tags = []string{"rule:never"} }},
		{"privacy", false, func(r *Record) { r.PrivacyQuarantined = true }},
		{"scope", false, func(r *Record) { r.Scope = scopeB }},
		{"source", false, func(r *Record) { r.Sources = []SourceRef{{SourceID: "new"}} }},
		{"reach", false, func(r *Record) { r.Reach = "personal" }},
	}
	counts := map[bool]int{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next := old
			next.Revision++
			tc.edit(&next)
			got := nativeMetadataOnlyRevision(old, next)
			counts[got]++
			if got != tc.metadata {
				t.Fatal(got)
			}
		})
	}
	t.Logf("synthetic mutation classification only: metadata=%d content_or_safety=%d; no operational frequency measured", counts[true], counts[false])
}
func TestNativeMetadataPurgeReasonPreservesExactFencePG(t *testing.T) {
	p := nativePGSetup(t)
	p.activate(t, "v1")
	_, capture := p.accept(t)
	p.process(t, capture)
	old, e := p.admin.Get(testCtx, colMemories, p.in.Scope, p.in.Target.ID)
	if e != nil {
		t.Fatal(e)
	}
	next := old
	next.Revision++
	now := time.Now().UTC().Add(time.Hour)
	next.ReviewDueAt = &now
	if e = p.admin.Replace(testCtx, colMemories, next, old.Revision); e != nil {
		t.Fatal(e)
	}
	purge, e := p.admin.Get(testCtx, colJobs, p.in.Scope, nativePurgeID(p.in.Scope, p.in.Target))
	if e != nil || purge.NativeDynamics == nil || purge.NativeDynamics.Reason != "dependency_metadata_revision_changed" {
		t.Fatal(purge, e)
	}
	e = p.admin.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		return nativeRejectInvalidatedTx(ctx, tx, p.in.Scope, p.in.Target)
	})
	if !errors.Is(e, ErrConflict) {
		t.Fatal("metadata bypassed exact fence", e)
	}
	if v, e := p.worker.ReadNativeDynamicsSnapshot(testCtx, p.workerToken, NativeDynamicsQuery{p.in.Scope, p.in.Target, p.in.Dimension, "v1", time.Now().UnixNano()}); e == nil && v.Value != nil {
		t.Fatal("old evidence was reused")
	}
}
