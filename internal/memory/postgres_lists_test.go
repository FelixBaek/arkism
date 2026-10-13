package memory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"
)

func openPostgresListsTest(t *testing.T) (*Postgres, Scope) {
	t.Helper()
	unique := ContentHash(fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano()))[:16]
	return isolatedPostgres(t), Scope{WorkspaceID: "pg-lists-" + unique, ProjectID: "project-" + unique}
}

func insertPGRecords(t *testing.T, p *Postgres, collection string, records ...Record) {
	t.Helper()
	for _, record := range records {
		if err := p.Insert(context.Background(), collection, record); err != nil {
			t.Fatalf("insert %s/%s: %v", collection, record.ID, err)
		}
	}
}

func TestPostgresListsLiteralLiveAndCursors(t *testing.T) {
	p, scope := openPostgresListsTest(t)
	ctx := context.Background()
	x := scope.ProjectID + "-"
	now := time.Now().UTC().Truncate(time.Millisecond)
	old, newer, future, expired := now.Add(-2*time.Hour), now.Add(-time.Hour), now.Add(time.Hour), now.Add(-time.Minute)
	sources := []Record{
		{ID: x + "source-new", Scope: scope, Kind: "note", Status: statusActive, Revision: 1, Text: "Literal_% match", CollectedAt: &newer, CreatedAt: newer, UpdatedAt: newer},
		{ID: x + "source-old", Scope: scope, Kind: "note", Status: statusActive, Revision: 1, Text: "Literal_% second", CollectedAt: &old, CreatedAt: old, UpdatedAt: old},
		{ID: x + "source-case", Scope: scope, Kind: "note", Status: statusActive, Revision: 1, Text: "literal_% match", CollectedAt: &old, CreatedAt: old, UpdatedAt: old},
		{ID: x + "source-expired", Scope: scope, Kind: "note", Status: statusActive, Revision: 1, Text: "Literal_% match", CollectedAt: &old, ExpiresAt: &expired, CreatedAt: old, UpdatedAt: old},
		{ID: x + "source-future", Scope: scope, Kind: "note", Status: statusActive, Revision: 1, Text: "Literal_% match", CollectedAt: &old, EffectiveAt: &future, CreatedAt: old, UpdatedAt: old},
	}
	insertPGRecords(t, p, "sources", sources...)
	in := SourceListInput{Scope: scope, Query: "Literal_%", Limit: 1}
	page, err := p.ListSources(ctx, in)
	if err != nil || len(page) != 2 || page[0].ID != x+"source-new" || page[1].ID != x+"source-old" {
		t.Fatalf("source page: ids=%v err=%v", recordIDs(page), err)
	}
	in.Cursor, err = encodeSourceCursor(in, page[0])
	if err != nil {
		t.Fatal(err)
	}
	next, err := p.ListSources(ctx, in)
	if err != nil || len(next) != 1 || next[0].ID != x+"source-old" {
		t.Fatalf("source cursor: ids=%v err=%v", recordIDs(next), err)
	}

	memories := []Record{
		{ID: x + "memory-live", Scope: scope, Kind: "fact", Status: statusActive, Revision: 1, Text: "100%_literal", CreatedAt: old, UpdatedAt: old},
		{ID: x + "memory-case", Scope: scope, Kind: "fact", Status: statusActive, Revision: 1, Text: "100%_LITERAL", CreatedAt: old, UpdatedAt: old},
		{ID: x + "memory-erased", Scope: scope, Kind: "fact", Status: statusActive, Revision: 1, Text: "100%_literal", ContentErased: true, CreatedAt: old, UpdatedAt: old},
		{ID: x + "memory-expired", Scope: scope, Kind: "fact", Status: statusActive, Revision: 1, Text: "100%_literal", ExpiresAt: &expired, CreatedAt: old, UpdatedAt: old},
	}
	insertPGRecords(t, p, "memories", memories...)
	listed, err := p.ListMemories(ctx, MemoryListInput{Scope: scope, Query: "100%_literal", Limit: 10})
	if err != nil || len(listed) != 1 || listed[0].ID != x+"memory-live" {
		t.Fatalf("memory list: ids=%v err=%v", recordIDs(listed), err)
	}
	artifactIDs := []string{"artifact-" + ContentHash(x+"artifact-a"), "artifact-" + ContentHash(x+"artifact-b")}
	slices.Sort(artifactIDs)
	for _, id := range artifactIDs {
		insertPGRecords(t, p, colArtifacts, Record{ID: id, Scope: scope, Revision: 1, Sources: []SourceRef{{SourceID: sources[0].ID}}, CreatedAt: old, UpdatedAt: old})
	}
	artifacts, err := p.ListSourceArtifacts(ctx, sources[0].ID, ArtifactListInput{Scope: scope, Limit: 1})
	if err != nil || len(artifacts) != 2 || artifacts[0].ID != artifactIDs[0] {
		t.Fatalf("artifact list: ids=%v err=%v", recordIDs(artifacts), err)
	}
	artifacts, err = p.ListSourceArtifacts(ctx, sources[0].ID, ArtifactListInput{Scope: scope, Limit: 1, Cursor: artifacts[0].ID})
	if err != nil || len(artifacts) != 1 || artifacts[0].ID != artifactIDs[1] {
		t.Fatalf("artifact cursor: ids=%v err=%v", recordIDs(artifacts), err)
	}
	derivedIDs := []string{x + "derived-a", x + "derived-b"}
	for _, id := range derivedIDs {
		insertPGRecords(t, p, "memory_candidates", Record{ID: id, Scope: scope, Revision: 1, Sources: []SourceRef{{SourceID: sources[0].ID}}, CreatedAt: old, UpdatedAt: old})
	}
	derived, err := p.ListSourceDerived(ctx, sources[0].ID, DerivedListInput{Scope: scope, Collection: "memory_candidates", Limit: 1, Cursor: derivedIDs[0]})
	if err != nil || len(derived) != 1 || derived[0].ID != derivedIDs[1] {
		t.Fatalf("derived cursor: ids=%v err=%v", recordIDs(derived), err)
	}
}

func TestPostgresReviewOrderingMetricsAndUsage(t *testing.T) {
	p, scope := openPostgresListsTest(t)
	ctx := context.Background()
	x := scope.ProjectID + "-"
	now := time.Now().UTC().Truncate(time.Millisecond)
	created := now.Add(-2 * time.Minute)
	candidates := []Record{
		{ID: x + "candidate-two", Scope: scope, Kind: "fact", Status: "candidate", Revision: 1, Trust: .8, Sources: []SourceRef{{SourceID: "a"}, {SourceID: "b"}, {SourceID: "a"}}, CreatedAt: created, UpdatedAt: created},
		{ID: x + "candidate-one", Scope: scope, Kind: "fact", Status: "candidate", Revision: 1, Trust: .8, Sources: []SourceRef{{SourceID: "a"}}, CreatedAt: created.Add(time.Second), UpdatedAt: created},
		{ID: x + "candidate-low", Scope: scope, Kind: "fact", Status: "candidate", Revision: 1, Trust: .7, CreatedAt: created.Add(time.Second), UpdatedAt: created},
	}
	expiredAt := now.Add(-time.Second)
	candidates = append(candidates, Record{ID: x + "candidate-expired", Scope: scope, Kind: "fact", Status: "candidate", Revision: 1, ExpiresAt: &expiredAt, CreatedAt: created, UpdatedAt: created})
	insertPGRecords(t, p, "memory_candidates", candidates...)
	in := ReviewInput{Scope: scope, Limit: 1}
	rows, err := p.ListCandidates(ctx, in)
	if err != nil || len(rows) != 2 || rows[0].ID != x+"candidate-two" || rows[1].ID != x+"candidate-one" {
		t.Fatalf("review order: ids=%v err=%v", recordIDs(rows), err)
	}
	in, _, err = normalizeReview(in)
	if err != nil {
		t.Fatal(err)
	}
	position := reviewPosition(rows[0], reviewBinding(in))
	raw, _ := json.Marshal(position)
	in.Cursor = base64.RawURLEncoding.EncodeToString(raw)
	next, err := p.ListCandidates(ctx, in)
	if err != nil || len(next) != 2 || next[0].ID != x+"candidate-one" || next[1].ID != x+"candidate-low" {
		t.Fatalf("review cursor: ids=%v err=%v", recordIDs(next), err)
	}

	usageOld := Record{ID: x + "usage-old", Scope: scope, Kind: "context_usage", Status: statusActive, Revision: 1, Usage: &ContextUsage{Items: []MemoryUse{{Memory: Ref{Collection: "memories", ID: x + "m", Revision: 1}}}}, CreatedAt: created, UpdatedAt: created}
	usageNew := usageOld
	usageNew.ID, usageNew.CreatedAt = x+"usage-new", created.Add(time.Minute)
	insertPGRecords(t, p, "jobs", usageOld, usageNew,
		Record{ID: x + "embedding-pending", Scope: scope, Kind: "embedding", Status: JobStatusPending, Revision: 1, CreatedAt: created, UpdatedAt: created},
		Record{ID: x + "erasure-failed", Scope: scope, Kind: sourceMemoryErasureKind, Status: JobStatusFailed, Revision: 1, CreatedAt: created, UpdatedAt: created})
	usage, err := p.MemoryUsage(ctx, scope, x+"m")
	if err != nil || len(usage) != 2 || usage[0].ID != x+"usage-new" {
		t.Fatalf("usage order: ids=%v err=%v", recordIDs(usage), err)
	}
	insertPGRecords(t, p, colProviderUsage,
		Record{ID: x + "cost-done", Scope: scope, Kind: providerUsageKind, Status: JobStatusDone, Revision: 1, Cost: &ProviderCost{Requests: 2, InputTokens: 3, OutputTokens: 4, TotalTokens: 7, EstimatedCostPicoUSD: 11, PricingAvailable: true}, CreatedAt: created, UpdatedAt: created},
		Record{ID: x + "cost-failed", Scope: scope, Kind: providerUsageKind, Status: JobStatusFailed, Revision: 1, Cost: &ProviderCost{Requests: 1, TotalTokens: 5}, CreatedAt: created, UpdatedAt: created})
	metrics, err := p.OperationalMetrics(ctx, scope)
	if err != nil || metrics.Candidates.Count != 3 || metrics.EmbeddingJobs[JobStatusPending].Count != 1 || metrics.SourceMemoryErasureJobs[JobStatusFailed].Count != 1 || metrics.Cost.Requests != 3 || metrics.Cost.CompletedRequests != 2 || metrics.Cost.FailedRequests != 1 || metrics.Cost.UnpricedRequests != 0 || metrics.Cost.TotalTokens != 12 || !metrics.Cost.Available || metrics.Candidates.OldestAgeSeconds <= 0 {
		t.Fatalf("metrics: %+v err=%v", metrics, err)
	}
}

func TestPostgresDeletionAndErasurePredicates(t *testing.T) {
	p, scope := openPostgresListsTest(t)
	ctx := context.Background()
	x := scope.ProjectID + "-"
	now := time.Now().UTC()
	target := Ref{Collection: "memories", ID: x + "same-id", Revision: 1}
	matching := []Record{
		{ID: x + "conflict-missing", Scope: scope, Revision: 1, Conflict: &ConflictDetails{Missing: []Ref{target}}, CreatedAt: now, UpdatedAt: now},
		{ID: x + "conflict-replacement", Scope: scope, Revision: 1, Conflict: &ConflictDetails{Replacement: &target}, CreatedAt: now, UpdatedAt: now},
		{ID: x + "usage-item", Scope: scope, Revision: 1, Usage: &ContextUsage{Items: []MemoryUse{{Memory: target}}}, CreatedAt: now, UpdatedAt: now},
		{ID: x + "usage-run", Scope: scope, Revision: 1, Usage: &ContextUsage{Run: &target}, CreatedAt: now, UpdatedAt: now},
	}
	notMatching := []Record{
		{ID: x + "source-only", Scope: scope, Revision: 1, Sources: []SourceRef{{SourceID: target.ID}}, CreatedAt: now, UpdatedAt: now},
		{ID: x + "wrong-relation", Scope: scope, Revision: 1, Relations: []Ref{{Collection: "sources", ID: target.ID, Revision: 1}}, CreatedAt: now, UpdatedAt: now},
	}
	insertPGRecords(t, p, "memory_relations", append(matching, notMatching...)...)
	refs, err := p.DeletionReferences(ctx, "memory_relations", scope, target, 100)
	if err != nil || len(refs) != len(matching) {
		t.Fatalf("deletion refs: %+v err=%v", refs, err)
	}
	for _, record := range matching {
		if !slices.ContainsFunc(refs, func(ref Ref) bool { return ref.ID == record.ID }) {
			t.Fatalf("missing deletion reference %s: %+v", record.ID, refs)
		}
	}
	insertPGRecords(t, p, "memories",
		Record{ID: target.ID, Scope: scope, Revision: 1, Relations: []Ref{target}, CreatedAt: now, UpdatedAt: now},
		Record{ID: x + "memory-referrer", Scope: scope, Revision: 1, Relations: []Ref{target}, CreatedAt: now, UpdatedAt: now})
	refs, err = p.DeletionReferences(ctx, "memories", scope, target, 100)
	if err != nil || len(refs) != 1 || refs[0].ID != x+"memory-referrer" {
		t.Fatalf("self exclusion: %+v err=%v", refs, err)
	}
	erasure, err := p.MemoryErasureBatch(ctx, scope, target.ID, false, "", memoryRelationErasureKind)
	if err != nil || len(erasure) != 0 {
		t.Fatalf("wrong collection relation matched: %+v err=%v", erasure, err)
	}
	insertPGRecords(t, p, "evaluations",
		Record{ID: x + "evaluation-match", Scope: scope, Revision: 1, Target: &target, CreatedAt: now, UpdatedAt: now},
		Record{ID: x + "evaluation-wrong", Scope: scope, Revision: 1, Target: &Ref{Collection: "sources", ID: target.ID, Revision: 1}, CreatedAt: now, UpdatedAt: now})
	erasure, err = p.MemoryErasureBatch(ctx, scope, target.ID, false, "", memoryEvaluationErasureKind)
	if err != nil || len(erasure) != 1 || erasure[0].ID != x+"evaluation-match" {
		t.Fatalf("evaluation erasure target: %+v err=%v", erasure, err)
	}

	sourceID := x + "source-skill"
	header := Record{ID: x + "skill-header", Scope: scope, Kind: "skill", Status: statusActive, Revision: 1, Sources: []SourceRef{{SourceID: sourceID}}, CreatedAt: now, UpdatedAt: now}
	child := Record{ID: x + "skill-child", Scope: scope, Kind: "skill_version", Status: statusActive, Revision: 2, SkillID: header.ID, CreatedAt: now, UpdatedAt: now}
	insertPGRecords(t, p, colSkills, header)
	insertPGRecords(t, p, colSkillVersions, child)
	batch, err := p.SourceErasureBatch(ctx, scope, sourceID, sourceSkillErasureKind)
	if err != nil || !slices.Contains(batch, Ref{Collection: colSkillVersions, ID: child.ID, Revision: child.Revision}) || slices.Contains(batch, Ref{Collection: colSkills, ID: header.ID, Revision: header.Revision}) {
		t.Fatalf("skill erasure order: %+v err=%v", batch, err)
	}
}

func TestPostgresBlobIdentityReferencesAndGC(t *testing.T) {
	p, scope := openPostgresListsTest(t)
	ctx := context.Background()
	x := scope.ProjectID + "-"
	first, err := p.BlobStorageIdentity(ctx)
	if err != nil || !sourceHashPattern.MatchString(first) {
		t.Fatalf("identity: %q %v", first, err)
	}
	if err = p.Init(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := p.BlobStorageIdentity(ctx)
	if err != nil || first != second {
		t.Fatalf("unstable identity: %q %q %v", first, second, err)
	}
	digest := ContentHash(fmt.Sprintf("blob-%s-%d", t.Name(), time.Now().UnixNano()))
	now := time.Now().UTC()
	sourceID := x + "blob-source"
	artifact := Record{ID: artifactID(scope, sourceID, digest), Scope: scope, Kind: "tool_output", Status: statusActive, Revision: 1, Artifact: &artifactMetadata{Digest: digest, MIME: "text/plain", Bytes: 1}, ArtifactGeneration: ContentHash(x + "generation"), Target: &Ref{Collection: "sources", ID: sourceID, Revision: 1}, Sources: []SourceRef{{SourceID: sourceID, Locator: "original"}}, CreatedAt: now, UpdatedAt: now}
	insertPGRecords(t, p, colArtifacts, artifact)
	insertPGRecords(t, p, colBlobs, Record{ID: digest, Scope: blobScope(), Kind: "blob", Status: statusActive, Revision: 1, Blob: &blobMetadata{Bytes: 1, References: 1}, CreatedAt: now, UpdatedAt: now})
	if count, err := p.BlobReferences(ctx, digest); err != nil || count != 1 {
		t.Fatalf("blob references=%d err=%v", count, err)
	}
	gcID := "000000000000" + digest[12:]
	insertPGRecords(t, p, colBlobs, Record{ID: gcID, Scope: blobScope(), Kind: "blob", Status: JobStatusPending, Revision: 1, Blob: &blobMetadata{}, CreatedAt: now, UpdatedAt: now})
	batch, err := p.BlobGCBatch(ctx, "")
	if err != nil || !slices.Contains(batch, gcID) {
		t.Fatalf("gc batch missing %s: %v err=%v", gcID, batch, err)
	}
	if err = p.ValidateBlobRestore(ctx, map[string]int64{digest: 1}); err != nil {
		t.Fatalf("validate blob restore: %v", err)
	}
}

func recordIDs(records []Record) []string {
	ids := make([]string, len(records))
	for i := range records {
		ids[i] = records[i].ID
	}
	return ids
}
