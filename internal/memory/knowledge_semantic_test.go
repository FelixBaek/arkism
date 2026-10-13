//go:build linux

package memory

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func knowledgeSemanticFixture(t *testing.T) (*fixture, KnowledgeIngestResult, KnowledgeRetrieveInput) {
	t.Helper()
	f, _, ing, b, req := knowledgeRetrievalFixture(t)
	f.emb.semantic = true
	knowledgeActivate(t, f, b)
	return f, ing, req
}

func knowledgeIndex(t *testing.T, f *fixture, req KnowledgeRetrieveInput, source string) KnowledgeIndexResult {
	t.Helper()
	out, e := f.svc.IndexKnowledgeEmbeddings(testCtx, f.human, KnowledgeIndexInput{Scope: req.Scope, KnowledgeSourceID: source})
	if e != nil {
		t.Fatal(e)
	}
	return out
}

func TestKnowledgeIndexIsIdempotentPerContentAndSpace(t *testing.T) {
	f, ing, req := knowledgeSemanticFixture(t)
	first := knowledgeIndex(t, f, req, ing.Source.ID)
	if first.Embedded != len(ing.Chunks) || first.Reused != 0 || first.Failed != 0 {
		t.Fatalf("first index: %#v", first)
	}
	if first.EmbeddingSpaceID != f.emb.Space() || first.EmbeddingDimension != VectorDimension || first.IndexedAt.IsZero() {
		t.Fatalf("missing embedding pins: %#v", first)
	}
	calls := len(f.emb.calls)
	second := knowledgeIndex(t, f, req, ing.Source.ID)
	if second.Embedded != 0 || second.Reused != len(ing.Chunks) || len(f.emb.calls) != calls {
		t.Fatalf("re-embedded unchanged content: %#v", second)
	}
	chunk, e := f.repo.Get(testCtx, colKnowledgeChunks, req.Scope, ing.Chunks[0].ID)
	if e != nil || chunk.Embedding == nil || chunk.Embedding.ContentHash != chunk.ContentHash || chunk.Knowledge.IndexStatus != "indexed" {
		t.Fatalf("chunk vector provenance: %v %#v", e, chunk.Embedding)
	}
}

func TestKnowledgeSemanticRequiresIndexedChunks(t *testing.T) {
	f, ing, req := knowledgeSemanticFixture(t)
	req.Mode = knowledgeModeSemantic
	before, e := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if e != nil || len(before.Context.Items) != 0 {
		t.Fatalf("unindexed corpus must not be retrievable: %v %#v", e, before)
	}
	log, e := f.svc.GetKnowledgeRetrievalLog(testCtx, f.work, before.RetrievalLogID, req.Scope)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, exclusion := range log.KnowledgeRetrieval.Exclusions {
		found = found || exclusion.Reason == "embedding_unavailable"
	}
	if !found || log.KnowledgeRetrieval.RetrievalMode != knowledgeModeSemantic || log.KnowledgeRetrieval.EmbeddingSpaceID != f.emb.Space() {
		t.Fatalf("missing semantic exclusion or model pin: %#v", log.KnowledgeRetrieval)
	}
	knowledgeIndex(t, f, req, ing.Source.ID)
	after, e := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if e != nil || len(after.Context.Items) == 0 {
		t.Fatalf("indexed retrieval: %v %#v", e, after)
	}
	item := after.Context.Items[0]
	if item.Rank != 1 || item.VectorDistance == nil || item.Authority != "data_only_untrusted" {
		t.Fatalf("missing rank or score: %#v", item)
	}
	stable, e := f.svc.RevalidateKnowledge(testCtx, f.work, knowledgeValidateRequest(after))
	if e != nil || !reflect.DeepEqual(stable, after) {
		t.Fatalf("semantic receipt is not reproducible: %v", e)
	}
}

func TestKnowledgeSemanticRunsAfterBindingAndScopeFilters(t *testing.T) {
	f, _, ing, b, req := knowledgeRetrievalFixture(t)
	f.emb.semantic = true
	b = knowledgeActivate(t, f, b)
	knowledgeIndex(t, f, req, ing.Source.ID)
	req.Mode = knowledgeModeSemantic
	if out, e := f.svc.RetrieveKnowledge(testCtx, f.work, req); e != nil || len(out.Context.Items) == 0 {
		t.Fatalf("baseline semantic retrieval: %v", e)
	}
	if _, e := f.svc.SetKnowledgeState(testCtx, f.human, colKnowledgeBindings, b.ID, KnowledgeStateInput{Scope: req.Scope, ExpectedRevision: b.Revision, Status: "suspended"}); e != nil {
		t.Fatal(e)
	}
	revoked, e := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if e != nil || len(revoked.Context.Items) != 0 {
		t.Fatalf("revoked binding still reachable by vector search: %v %#v", e, revoked)
	}
	foreign := req
	foreign.Scope.WorkspaceID = "other-workspace"
	if _, e := f.svc.RetrieveKnowledge(testCtx, f.work, foreign); e == nil {
		t.Fatal("cross-scope semantic retrieval was allowed")
	}
}

func TestKnowledgeFusionKeepsBothLanes(t *testing.T) {
	f, ing, req := knowledgeSemanticFixture(t)
	knowledgeIndex(t, f, req, ing.Source.ID)
	deterministic, e := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if e != nil {
		t.Fatal(e)
	}
	if deterministic.RetrievalMode != "" || deterministic.Context.Items[0].Rank != 0 {
		t.Fatalf("deterministic receipt changed shape: %#v", deterministic)
	}
	req.Mode = knowledgeModeFusion
	fused, e := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if e != nil || len(fused.Context.Items) == 0 {
		t.Fatalf("fusion retrieval: %v", e)
	}
	item := fused.Context.Items[0]
	if item.FusionScore == nil || *item.FusionScore <= 0 || item.Rank != 1 {
		t.Fatalf("missing fusion score: %#v", item)
	}
	if fused.RetrievalMode != knowledgeModeFusion || fused.EmbeddingModel == "" {
		t.Fatalf("missing fusion pins: %#v", fused)
	}
}

func TestKnowledgeSemanticFallsBackWhenEmbedderIsDown(t *testing.T) {
	f, ing, req := knowledgeSemanticFixture(t)
	knowledgeIndex(t, f, req, ing.Source.ID)
	f.emb.err = ErrUnavailable
	req.Mode = knowledgeModeSemantic
	out, e := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if e != nil || len(out.Context.Items) == 0 {
		t.Fatalf("fallback must still serve deterministic results: %v %#v", e, out)
	}
	if out.RetrievalMode != knowledgeModeDeterministic || out.RequestedMode != knowledgeModeSemantic || out.ModeFallback != "embedder_unavailable" {
		t.Fatalf("fallback not recorded: %#v", out)
	}
	if !out.FallbackUsed || out.FallbackPolicy != knowledgeFallbackAllowed {
		t.Fatalf("explicit fallback fields missing: %#v", out)
	}
	if out.Context.Items[0].VectorDistance != nil {
		t.Fatal("fallback context carries a vector score")
	}
	log, e := f.svc.GetKnowledgeRetrievalLog(testCtx, f.work, out.RetrievalLogID, req.Scope)
	if e != nil {
		t.Fatal(e)
	}
	stored := log.KnowledgeRetrieval
	if stored.RetrievalMode != knowledgeModeDeterministic || stored.RequestedMode != knowledgeModeSemantic || stored.ModeFallback != "embedder_unavailable" || !stored.FallbackUsed || stored.FallbackPolicy != knowledgeFallbackAllowed {
		t.Fatalf("fallback not recorded in the retrieval log: %#v", stored)
	}
}

func TestKnowledgeStrictSemanticNeverServesDeterministicResults(t *testing.T) {
	f, ing, req := knowledgeSemanticFixture(t)
	knowledgeIndex(t, f, req, ing.Source.ID)
	f.emb.err = ErrUnavailable
	req.Mode, req.Fallback = knowledgeModeSemantic, knowledgeFallbackStrict
	out, e := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if !errors.Is(e, ErrSemanticLaneDown) || !errors.Is(e, ErrUnavailable) {
		t.Fatalf("strict semantic must fail typed, got %v", e)
	}
	if len(out.Context.Items) != 0 || out.RetrievalMode != "" || out.RequestedMode != "" {
		t.Fatalf("strict failure must not carry a receipt: %#v", out)
	}
	// The failure is on the audit trail as a fixed reason code, and no
	// retrieval log row exists that could misreport an effective mode.
	found := false
	for _, event := range f.repo.rows("audit_events") {
		if event.Kind == "retrieve:knowledge_failed" && event.Target != nil && event.Target.ID == "embedder_unavailable" {
			found = true
		}
	}
	if !found {
		t.Fatal("strict failure missing from the audit trail")
	}
	for _, entry := range f.repo.rows(colRetrievalLogs) {
		if entry.KnowledgeRetrieval != nil {
			t.Fatalf("strict failure wrote a retrieval log: %#v", entry.KnowledgeRetrieval)
		}
	}
	// Recovering the embedder restores strict semantic retrieval unchanged.
	f.emb.err = nil
	req.Fallback = knowledgeFallbackStrict
	recovered, e := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if e != nil || recovered.RetrievalMode != knowledgeModeSemantic || recovered.RequestedMode != knowledgeModeSemantic || recovered.FallbackUsed || recovered.ModeFallback != "" {
		t.Fatalf("strict semantic after recovery: %v %#v", e, recovered)
	}
	if recovered.FallbackPolicy != knowledgeFallbackStrict {
		t.Fatalf("strict policy not recorded: %#v", recovered)
	}
}

func TestKnowledgeStrictFusionRequiresBothLanes(t *testing.T) {
	f, ing, req := knowledgeSemanticFixture(t)
	knowledgeIndex(t, f, req, ing.Source.ID)
	// A fusion_strict request whose embedder is down fails typed instead of
	// returning a deterministic answer labelled fusion.
	f.emb.err = ErrUnavailable
	req.Mode, req.Fallback = knowledgeModeFusion, knowledgeFallbackStrict
	if _, e := f.svc.RetrieveKnowledge(testCtx, f.work, req); !errors.Is(e, ErrSemanticLaneDown) {
		t.Fatalf("fusion_strict with a down embedder must fail typed, got %v", e)
	}
	// A semantic lane that ranks nothing (no indexed vectors) is also a
	// failure under strict: an overlap-only ranking must not pose as fusion.
	f.emb.err = nil
	second := knowledgeFixtureInput()
	second.Scope = req.Scope
	second.SourceKey = "unindexed"
	second.Content = "Synthetic printable specification sample without vectors."
	unindexed, e := f.svc.IngestKnowledge(testCtx, f.human, second)
	if e != nil {
		t.Fatal(e)
	}
	head, e := f.svc.SetKnowledgeState(testCtx, f.human, colKnowledgeSources, unindexed.Source.ID, KnowledgeStateInput{Scope: req.Scope, ExpectedRevision: unindexed.Source.Revision, Status: "active", TrustLevel: "reviewed"})
	if e != nil {
		t.Fatal(e)
	}
	binding, e := f.svc.CreateKnowledgeBinding(testCtx, f.human, KnowledgeBindingInput{Scope: req.Scope, BindingKey: "unindexed", KnowledgeSourceID: head.ID, SpecialistProfileID: "illustrator", SpecialistProfileVersionID: "v1", SkillID: "illustrator.v1", TrustRequirement: "reviewed", AccessMode: "synthetic_retrieval"})
	if e != nil {
		t.Fatal(e)
	}
	binding = knowledgeActivate(t, f, binding)
	req.Fallback = knowledgeFallbackStrict
	req.BindingIDs = []string{binding.ID}
	if _, e := f.svc.RetrieveKnowledge(testCtx, f.work, req); !errors.Is(e, ErrSemanticLaneDown) {
		t.Fatalf("fusion_strict with an empty vector lane must fail typed, got %v", e)
	}
	emptyLaneAudited := false
	for _, event := range f.repo.rows("audit_events") {
		if event.Kind == "retrieve:knowledge_failed" && event.Target != nil && event.Target.ID == "semantic_lane_empty" {
			emptyLaneAudited = true
		}
	}
	if !emptyLaneAudited {
		t.Fatal("empty semantic lane failure missing from the audit trail")
	}
	// The same request under fallback_allowed stays the explicit degraded path.
	req.Fallback = knowledgeFallbackAllowed
	degraded, e := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if e != nil || degraded.RetrievalMode != knowledgeModeFusion || degraded.RequestedMode != knowledgeModeFusion {
		t.Fatalf("non-strict fusion on an empty vector lane: %v %#v", e, degraded)
	}
}

type batchFailEmbedder struct {
	inner     Embedder
	failOn    int
	calls     int
	embedding []string
}

func (b *batchFailEmbedder) Embed(ctx context.Context, texts []string, purpose string) ([]Embedding, error) {
	b.calls++
	if b.calls == b.failOn {
		return nil, ErrUnavailable
	}
	b.embedding = append(b.embedding, strconv.Itoa(len(texts)))
	return b.inner.Embed(ctx, texts, purpose)
}

func (b *batchFailEmbedder) Space() string { return b.inner.Space() }

func TestKnowledgeIndexPartialBatchFailureIsDurable(t *testing.T) {
	f, _, req := knowledgeSemanticFixture(t)
	in := knowledgeFixtureInput()
	in.Scope = req.Scope
	in.SourceKey = "batching"
	in.Content = strings.Repeat("Synthetic printable specification batch sample for partial failure durability. ", 34)
	ing, e := f.svc.IngestKnowledge(testCtx, f.human, in)
	if e != nil {
		t.Fatal(e)
	}
	if len(ing.Chunks) < knowledgeIndexBatch+1 {
		t.Fatalf("fixture needs more than one batch, got %d chunks", len(ing.Chunks))
	}
	head, e := f.svc.SetKnowledgeState(testCtx, f.human, colKnowledgeSources, ing.Source.ID, KnowledgeStateInput{Scope: req.Scope, ExpectedRevision: ing.Source.Revision, Status: "active", TrustLevel: "reviewed"})
	if e != nil {
		t.Fatal(e)
	}
	// The second batch fails after the first one succeeded: completed vectors
	// are committed, only the remainder is durably marked failed.
	wrapper := &batchFailEmbedder{inner: f.emb, failOn: 2}
	f.svc.Embedder = wrapper
	out, e := f.svc.IndexKnowledgeEmbeddings(testCtx, f.human, KnowledgeIndexInput{Scope: req.Scope, KnowledgeSourceID: head.ID})
	if !errors.Is(e, ErrUnavailable) {
		t.Fatalf("partial batch job must fail with the embedder error, got %v", e)
	}
	if out.Embedded != knowledgeIndexBatch || out.Failed != len(ing.Chunks)-knowledgeIndexBatch {
		t.Fatalf("partial counts: %#v", out)
	}
	first, e := f.repo.Get(testCtx, colKnowledgeChunks, req.Scope, ing.Chunks[0].ID)
	if e != nil || first.Knowledge.IndexStatus != "indexed" || first.Embedding == nil {
		t.Fatalf("completed batch not durable: %v %#v", e, first.Knowledge)
	}
	last, e := f.repo.Get(testCtx, colKnowledgeChunks, req.Scope, ing.Chunks[len(ing.Chunks)-1].ID)
	if e != nil || last.Knowledge.IndexStatus != "failed" || last.Knowledge.IndexError != "embedder_unavailable" || last.Embedding != nil {
		t.Fatalf("remainder not durably failed: %v %#v", e, last.Knowledge)
	}
	// The retry re-embeds only the failed remainder.
	f.svc.Embedder = f.emb
	before := len(f.emb.calls)
	retried, e := f.svc.IndexKnowledgeEmbeddings(testCtx, f.human, KnowledgeIndexInput{Scope: req.Scope, KnowledgeSourceID: head.ID})
	if e != nil || retried.Embedded != len(ing.Chunks)-knowledgeIndexBatch || retried.Reused != knowledgeIndexBatch {
		t.Fatalf("retry after partial failure: %v %#v", e, retried)
	}
	if embedded := len(f.emb.calls) - before; embedded != len(ing.Chunks)-knowledgeIndexBatch {
		t.Fatalf("retry re-embedded %d texts, want %d", embedded, len(ing.Chunks)-knowledgeIndexBatch)
	}
}

func TestKnowledgeIndexJobIsCancelledByMidFlightSourceDelete(t *testing.T) {
	f, ing, req := knowledgeSemanticFixture(t)
	// The embedder call erases one chunk's content mid-job, the way a privacy
	// deletion or source erase races an index run.
	f.emb.onEmbed = func() {
		chunk, e := f.repo.Get(testCtx, colKnowledgeChunks, req.Scope, ing.Chunks[0].ID)
		if e != nil {
			t.Error(e)
			return
		}
		chunk.ContentErased = true
		chunk.Revision++
		if e := f.repo.Replace(testCtx, colKnowledgeChunks, chunk, chunk.Revision-1); e != nil {
			t.Error(e)
		}
	}
	_, e := f.svc.IndexKnowledgeEmbeddings(testCtx, f.human, KnowledgeIndexInput{Scope: req.Scope, KnowledgeSourceID: ing.Source.ID})
	if !errors.Is(e, ErrNotFound) {
		t.Fatalf("mid-flight erase must cancel the job with a typed error, got %v", e)
	}
	chunk, e := f.repo.Get(testCtx, colKnowledgeChunks, req.Scope, ing.Chunks[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	if chunk.Embedding != nil {
		t.Fatal("cancelled job stored a vector for an erased chunk")
	}
}

func TestEmbedderFailureKindClassification(t *testing.T) {
	timeout := fmt.Errorf("call dependency: %w: %w", context.DeadlineExceeded, ErrUnavailable)
	if kind := embedderFailureKind(timeout); kind != "embedder_timeout" {
		t.Fatalf("timeout classified as %s", kind)
	}
	refused := fmt.Errorf("call dependency: dial tcp 127.0.0.1:8099: connect: %w: %w", syscall.ECONNREFUSED, ErrUnavailable)
	if kind := embedderFailureKind(refused); kind != "embedder_connect_failed" {
		t.Fatalf("connection refused classified as %s", kind)
	}
	if kind := embedderFailureKind(ErrUnavailable); kind != "embedder_unavailable" {
		t.Fatalf("generic outage classified as %s", kind)
	}
	if reason := knowledgeFallbackReason(fmt.Errorf("wrap: %w", ErrInvalid)); reason != "embedder_rejected_query" {
		t.Fatalf("rejected query classified as %s", reason)
	}
}

func TestKnowledgeIndexFailureIsDurable(t *testing.T) {
	f, ing, req := knowledgeSemanticFixture(t)
	f.emb.err = ErrUnavailable
	out, e := f.svc.IndexKnowledgeEmbeddings(testCtx, f.human, KnowledgeIndexInput{Scope: req.Scope, KnowledgeSourceID: ing.Source.ID})
	if !errors.Is(e, ErrUnavailable) || out.Failed != len(ing.Chunks) {
		t.Fatalf("index failure: %v %#v", e, out)
	}
	chunk, e := f.repo.Get(testCtx, colKnowledgeChunks, req.Scope, ing.Chunks[0].ID)
	if e != nil || chunk.Knowledge.IndexStatus != "failed" || chunk.Knowledge.IndexError != "embedder_unavailable" || chunk.Embedding != nil {
		t.Fatalf("failure not durable: %v %#v", e, chunk.Knowledge)
	}
	f.emb.err = nil
	retried := knowledgeIndex(t, f, req, ing.Source.ID)
	if retried.Embedded != len(ing.Chunks) {
		t.Fatalf("retry after failure: %#v", retried)
	}
}

func TestKnowledgeNewVersionNeedsNewVectors(t *testing.T) {
	f, _, ing, b, req := knowledgeRetrievalFixture(t)
	f.emb.semantic = true
	knowledgeActivate(t, f, b)
	knowledgeIndex(t, f, req, ing.Source.ID)
	req.Mode = knowledgeModeSemantic
	in := knowledgeFixtureInput()
	in.Scope = req.Scope
	in.Content = "Synthetic printable specification revision two: 600 dpi sample."
	in.ExpectedRevision = ing.Source.Revision
	update, e := f.svc.IngestKnowledge(testCtx, f.human, in)
	if e != nil {
		t.Fatal(e)
	}
	head, e := f.svc.SetKnowledgeState(testCtx, f.human, colKnowledgeSources, update.Source.ID, KnowledgeStateInput{Scope: req.Scope, ExpectedRevision: update.Source.Revision, Status: "active", TrustLevel: "reviewed"})
	if e != nil {
		t.Fatal(e)
	}
	stale, e := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if e != nil || len(stale.Context.Items) != 0 {
		t.Fatalf("new version served old vectors: %v %#v", e, stale)
	}
	indexed := knowledgeIndex(t, f, req, head.ID)
	if indexed.Embedded != len(update.Chunks) || indexed.DocumentID != update.Document.ID {
		t.Fatalf("reindex targeted the wrong document: %#v", indexed)
	}
	fresh, e := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if e != nil || len(fresh.Context.Items) == 0 {
		t.Fatalf("reindexed retrieval: %v", e)
	}
	for _, item := range fresh.Context.Items {
		if item.DocumentID != update.Document.ID {
			t.Fatalf("superseded document reachable by vector search: %#v", item)
		}
	}
}
