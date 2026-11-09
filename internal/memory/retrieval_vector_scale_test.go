//go:build linux

package memory

import (
	"context"
	"fmt"
	"math"
	"os"
	"runtime"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"
)

// Opt-in measurement rather than a CI latency threshold: the corpus is fully
// synthetic and provider-free. Latency depends on machine load and cache state.
func TestRetrievalVectorScaleSynthetic(t *testing.T) {
	if os.Getenv("ARKISM_VECTOR_SCALE_AUDIT") != "1" {
		t.Skip("set ARKISM_VECTOR_SCALE_AUDIT=1 for isolated synthetic scale measurement")
	}
	p := isolatedPostgres(t)
	p.SetVectorMaxDistance(0.35)
	f := newFixture(t)
	f.svc.Repo = p
	src := f.source()
	query := make([]float64, VectorDimension)
	query[0] = 1
	now := time.Now().UTC().Truncate(time.Millisecond)
	gold := []string{"scale-00000", "scale-00001", "scale-00002"}
	inserted := 0
	for _, size := range []int{128, 1024, 4096} {
		for ; inserted < size; inserted++ {
			angle := 0.4 + float64(inserted%1000)/1000
			if inserted < 3 {
				angle = float64(inserted) * 0.05
			}
			vec := make([]float64, VectorDimension)
			vec[0], vec[1] = math.Cos(angle), math.Sin(angle)
			r := Record{ID: fmt.Sprintf("scale-%05d", inserted), Scope: scopeA, Kind: "fact", Status: statusActive, Revision: 1, ApprovedBy: "human", Text: fmt.Sprintf("synthetic independent fact %d", inserted), Sources: refs(src), CreatedAt: now, UpdatedAt: now}
			r.Embedding = &Embedding{Space: f.emb.Space(), Dimension: VectorDimension, Vector: vec, ContentHash: ContentHash(recordContent(r))}
			insertPGRecords(t, p, colMemories, r)
		}
		if _, err := p.pool.Exec(testCtx, "ANALYZE memory_records"); err != nil {
			t.Fatal(err)
		}
		// Full service checks still authorize/re-read/fence the selected source facts.
		out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "synthetic recall", Modes: []string{"vector"}, Limit: 3})
		if err != nil || !slices.Equal(ids(out.Items), gold) {
			t.Fatal("service exact-vector answer loss", size, ids(out.Items), err)
		}
		for _, workers := range []int{1, 4} {
			type sample struct {
				elapsed time.Duration
				err     error
			}
			samples := make(chan sample, 32)
			var wg sync.WaitGroup
			for worker := 0; worker < workers; worker++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for n := 0; n < 32/workers; n++ {
						ctx, cancel := context.WithTimeout(testCtx, 10*time.Second)
						start := time.Now()
						lanes, e := p.Search(ctx, scopeA, "synthetic recall", query, f.emb.Space(), 3, "vector")
						elapsed := time.Since(start)
						cancel()
						if e == nil && (len(lanes) != 3 || !slices.Equal(ids(lanes[2]), gold)) {
							e = fmt.Errorf("synthetic expected top three lost")
						}
						samples <- sample{elapsed, e}
					}
				}()
			}
			wg.Wait()
			close(samples)
			times := []time.Duration{}
			for s := range samples {
				if s.err != nil {
					t.Fatal(size, workers, s.err)
				}
				times = append(times, s.elapsed)
			}
			sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
			t.Logf("rows=%d concurrency=%d samples=%d p50_ms=%.3f p95_ms=%.3f expected_top3=32/32 runtime_procs=%d", size, workers, len(times), float64(times[len(times)/2])/float64(time.Millisecond), float64(times[(len(times)*95+99)/100-1])/float64(time.Millisecond), runtime.GOMAXPROCS(0))
		}
	}
	// Even a perfect-distance distractor in another scope cannot displace a fact.
	other := Record{ID: "foreign-perfect", Scope: scopeB, Kind: "fact", Status: statusActive, Revision: 1, ApprovedBy: "human", Text: "foreign", CreatedAt: now, UpdatedAt: now}
	other.Embedding = &Embedding{Space: f.emb.Space(), Dimension: VectorDimension, Vector: query, ContentHash: ContentHash(recordContent(other))}
	insertPGRecords(t, p, colMemories, other)
	lanes, err := p.Search(testCtx, scopeA, "synthetic recall", query, f.emb.Space(), 3, "vector")
	if err != nil || !slices.Equal(ids(lanes[2]), gold) {
		t.Fatal("cross-scope vector contamination", err)
	}
}
