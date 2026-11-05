package memory

import (
	"context"
	"sync"
	"time"
)

// Stage timings, finished background jobs, ingested documents and job queue
// gauges for the Prometheus and OTLP exports. Stage labels are code constants
// (recall lanes, extraction stages); job labels are job kinds and an outcome;
// document labels a source kind. All pass metricLabel and a series cap, so a
// label never carries user text and the set stays bounded.

const (
	stageRecallLane   = "recall_lane"
	stageExtract      = "extract_stage"
	stageOperation    = "operation"
	stageDocument     = "document"
	stageMetricsMax   = 256
	jobQueueKindsMax  = 200
	jobQueueStatsWait = 2 * time.Second
)

type stageMetrics struct {
	mu     sync.Mutex
	series map[[3]string]*histogramCounts
}

var stageStats = &stageMetrics{series: map[[3]string]*histogramCounts{}}

// observeStage records one stage's wall time; a new label past the cap is dropped.
func observeStage(family, stage string, elapsed time.Duration) {
	observeStageOutcome(family, stage, "", elapsed)
}

// observeOperation records one finished background job by kind and outcome (ok or error).
func observeOperation(kind string, err error, elapsed time.Duration) {
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	observeStageOutcome(stageOperation, kind, outcome, elapsed)
}

// observeDocument counts one document handed to extraction: text, file or knowledge, and reprocess for a new round.
func observeDocument(source string) { observeStageOutcome(stageDocument, source, "", 0) }

func observeStageOutcome(family, stage, outcome string, elapsed time.Duration) {
	stageStats.mu.Lock()
	defer stageStats.mu.Unlock()
	key := [3]string{family, metricLabel(stage), outcome}
	h := stageStats.series[key]
	if h == nil {
		if len(stageStats.series) >= stageMetricsMax {
			return
		}
		h = &histogramCounts{}
		stageStats.series[key] = h
	}
	h.observe(latencyBounds[:], elapsed.Seconds())
}

// timeStage is defer timeStage(family, stage, time.Now()).
func timeStage(family, stage string, start time.Time) { observeStage(family, stage, time.Since(start)) }

func stageFamilies() []metricFamily {
	stageStats.mu.Lock()
	defer stageStats.mu.Unlock()
	lanes := metricFamily{name: "arkism_recall_lane_duration_seconds", help: "Recall lane wall time per run.", kind: "histogram", bounds: latencyBounds[:]}
	extract := metricFamily{name: "arkism_extract_stage_duration_seconds", help: "File extraction stage wall time (parse, model, capture).", kind: "histogram", bounds: latencyBounds[:]}
	ops := metricFamily{name: "arkism_operations_total", help: "Background jobs finished by kind and outcome.", kind: "counter"}
	opDur := metricFamily{name: "arkism_operation_duration_seconds", help: "Background job run time by kind and outcome.", kind: "histogram", bounds: latencyBounds[:]}
	docs := metricFamily{name: "arkism_documents_ingested_total", help: "Documents handed to memory extraction by source (text, file, knowledge, reprocess).", kind: "counter"}
	for key, h := range stageStats.series {
		switch key[0] {
		case stageRecallLane:
			lanes.points = append(lanes.points, histogramPoint([][2]string{{"lane", key[1]}}, *h))
		case stageOperation:
			l := [][2]string{{"kind", key[1]}, {"outcome", key[2]}}
			ops.points = append(ops.points, metricPoint{labels: l, value: float64(h.count)})
			opDur.points = append(opDur.points, histogramPoint(l, *h))
		case stageDocument:
			docs.points = append(docs.points, metricPoint{labels: [][2]string{{"source", key[1]}}, value: float64(h.count)})
		default:
			extract.points = append(extract.points, histogramPoint([][2]string{{"stage", key[1]}}, *h))
		}
	}
	return []metricFamily{lanes, extract, ops, opDur, docs}
}

// JobQueueStat is one job kind's open work across every space this server's worker serves.
type JobQueueStat struct {
	Kind             string
	Backlog, Running int64
	Failed           int64
	OldestAgeSeconds float64
}

// jobQueueStatser is a store that can count the job queue across scopes (Postgres).
type jobQueueStatser interface {
	JobQueueStats(ctx context.Context) ([]JobQueueStat, error)
}

// queueFamilies counts with the server's own store binding, not the scraper's
// request: the gauges show what the job worker claims, by kind only.
func (s *Service) queueFamilies() []metricFamily {
	q, ok := s.Repo.(jobQueueStatser)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), jobQueueStatsWait)
	defer cancel()
	stats, err := q.JobQueueStats(ctx)
	up := 1.0
	if err != nil {
		up, stats = 0, nil
	}
	backlog := metricFamily{name: "arkism_job_queue_backlog", help: "Jobs waiting to run (pending or retry) by kind.", kind: "gauge"}
	running := metricFamily{name: "arkism_job_queue_running", help: "Jobs running by kind.", kind: "gauge"}
	failed := metricFamily{name: "arkism_job_queue_failed", help: "Jobs that failed and wait for a person or a retry by kind.", kind: "gauge"}
	oldest := metricFamily{name: "arkism_job_queue_oldest_age_seconds", help: "Age of the oldest waiting job by kind.", kind: "gauge"}
	for i, st := range stats {
		if i == jobQueueKindsMax {
			break
		}
		l := [][2]string{{"kind", metricLabel(st.Kind)}}
		backlog.points = append(backlog.points, metricPoint{labels: l, value: float64(st.Backlog)})
		running.points = append(running.points, metricPoint{labels: l, value: float64(st.Running)})
		failed.points = append(failed.points, metricPoint{labels: l, value: float64(st.Failed)})
		oldest.points = append(oldest.points, metricPoint{labels: l, value: st.OldestAgeSeconds})
	}
	return []metricFamily{backlog, running, failed, oldest, gauge("arkism_job_queue_stats_up", "1 when the job queue could be counted on this scrape.", up)}
}
