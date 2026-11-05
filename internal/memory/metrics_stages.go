package memory

import (
	"context"
	"sync"
	"time"
)

// Stage timings and job queue gauges for the Prometheus and OTLP exports.
// Stage labels are code constants (recall lanes, extraction stages); queue
// labels are job kinds. Both pass metricLabel and a series cap, so a label
// never carries user text and the set stays bounded.

const (
	stageRecallLane   = "recall_lane"
	stageExtract      = "extract_stage"
	stageMetricsMax   = 64
	jobQueueKindsMax  = 200
	jobQueueStatsWait = 2 * time.Second
)

type stageMetrics struct {
	mu     sync.Mutex
	series map[[2]string]*histogramCounts
}

var stageStats = &stageMetrics{series: map[[2]string]*histogramCounts{}}

// observeStage records one stage's wall time; a new label past the cap is dropped.
func observeStage(family, stage string, elapsed time.Duration) {
	stageStats.mu.Lock()
	defer stageStats.mu.Unlock()
	key := [2]string{family, metricLabel(stage)}
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
	for key, h := range stageStats.series {
		if key[0] == stageRecallLane {
			lanes.points = append(lanes.points, histogramPoint([][2]string{{"lane", key[1]}}, *h))
		} else {
			extract.points = append(extract.points, histogramPoint([][2]string{{"stage", key[1]}}, *h))
		}
	}
	return []metricFamily{lanes, extract}
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
