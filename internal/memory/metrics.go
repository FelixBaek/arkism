package memory

import (
	"sync"
	"time"
)

type QueueMetric struct {
	Count            int64     `json:"count" bson:"count"`
	Oldest           time.Time `json:"-" bson:"oldest"`
	OldestAgeSeconds float64   `json:"oldest_age_seconds" bson:"-"`
	Status           string    `json:"-" bson:"_id"`
}
type StoreMetrics struct {
	Candidates              QueueMetric            `json:"candidates"`
	EmbeddingJobs           map[string]QueueMetric `json:"embedding_jobs"`
	SourceMemoryErasureJobs map[string]QueueMetric `json:"source_memory_erasure_jobs"`
	Cost                    CostMetrics            `json:"-" bson:"-"`
}

type CostMetrics struct {
	Available            bool  `json:"available" bson:"-"`
	Requests             int64 `json:"requests" bson:"requests"`
	CompletedRequests    int64 `json:"completed_requests" bson:"completed_requests"`
	FailedRequests       int64 `json:"failed_requests" bson:"failed_requests"`
	PendingRequests      int64 `json:"pending_requests" bson:"pending_requests"`
	UnpricedRequests     int64 `json:"unpriced_requests" bson:"unpriced_requests"`
	InputTokens          int64 `json:"input_tokens" bson:"input_tokens"`
	OutputTokens         int64 `json:"output_tokens" bson:"output_tokens"`
	TotalTokens          int64 `json:"total_tokens" bson:"total_tokens"`
	EstimatedCostPicoUSD int64 `json:"estimated_cost_pico_usd" bson:"estimated_cost_pico_usd"`
}

type DeletionWorkflowMetrics struct {
	Available           bool   `json:"available"`
	FullErasureVerified bool   `json:"full_erasure_verified"`
	Coverage            string `json:"coverage"`
}

// Fixed buckets and ServeMux patterns bound cardinality independently of user
// paths, queries, identities and headers. A process restart resets these counts.
var latencyBounds = [...]float64{.05, .1, .25, .5, .8, 1, 2, 5, 10, 30, 60}

type RequestMetric struct {
	Count              uint64                     `json:"count"`
	StatusCodes        map[int]uint64             `json:"status_codes"`
	DurationSecondsSum float64                    `json:"duration_seconds_sum"`
	LatencyLeCounts    [len(latencyBounds)]uint64 `json:"latency_le_counts"`
}
type requestMetrics struct {
	mu     sync.Mutex
	routes map[string]RequestMetric
}

func (m *requestMetrics) observe(route string, status int, elapsed time.Duration) {
	if route == "" {
		route = "unmatched"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.routes == nil {
		m.routes = map[string]RequestMetric{}
	}
	row := m.routes[route]
	if row.StatusCodes == nil {
		row.StatusCodes = map[int]uint64{}
	}
	row.Count++
	row.StatusCodes[status]++
	seconds := elapsed.Seconds()
	row.DurationSecondsSum += seconds
	for i, bound := range latencyBounds {
		if seconds <= bound {
			row.LatencyLeCounts[i]++
		}
	}
	m.routes[route] = row
}
func (m *requestMetrics) snapshot() map[string]RequestMetric {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]RequestMetric{}
	for route, row := range m.routes {
		clone := row
		clone.StatusCodes = map[int]uint64{}
		for k, v := range row.StatusCodes {
			clone.StatusCodes[k] = v
		}
		out[route] = clone
	}
	return out
}
