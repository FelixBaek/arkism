package memory

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// fileExtractRequestBytes fits a 4 MiB file as base64 plus the JSON around it.
const fileExtractRequestBytes = 6 << 20

func registerFileextractCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/extract/preview", func(w http.ResponseWriter, r *http.Request) {
		var in ExtractPreviewInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.ExtractPreview(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/files/memories", func(w http.ResponseWriter, r *http.Request) {
		var in FileMemoriesInput
		if !decodeLimit(w, r, &in, fileExtractRequestBytes) {
			return
		}
		out, e := s.ExtractFileMemories(r.Context(), bearer(r), in)
		s.startExtractJob(r, in.Scope, out, e)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/memories/ingest", func(w http.ResponseWriter, r *http.Request) {
		var in IngestInput
		if !decodeLimit(w, r, &in, fileExtractRequestBytes) {
			return
		}
		out, e := s.Ingest(r.Context(), bearer(r), in)
		for _, item := range out.Items {
			s.startExtractJob(r, in.Scope, item, e)
		}
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/files/memories/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		scope, _, e := parseWikiQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.GetFileExtractJob(r.Context(), bearer(r), scope, r.PathValue("id"))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/files/memories/jobs/{id}/run", func(w http.ResponseWriter, r *http.Request) {
		var in WikiScopeInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.RunFileExtractJob(r.Context(), bearer(r), in.Scope, r.PathValue("id"))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/knowledge/sources/{id}/extract_memories", func(w http.ResponseWriter, r *http.Request) {
		var in KnowledgeExtractInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.ExtractKnowledgeMemories(r.Context(), bearer(r), r.PathValue("id"), in)
		s.startExtractJob(r, in.Scope, out, e)
		respond(w, out, e)
	})
}

// extractJobSlots bounds how many queued extraction jobs call the model at once.
var extractJobSlots = make(chan struct{}, 2)

// startExtractJob runs a queued job after the response; the caller polls it.
// A job stopped by a restart or the budget resumes through its run route.
func (s *Service) startExtractJob(r *http.Request, scope Scope, out FileMemoriesResult, err error) {
	if err != nil || out.JobID == "" || out.Status != JobStatusPending {
		return
	}
	s.followExtractJob(context.WithoutCancel(r.Context()), bearer(r), scope, out.JobID)
}

// extractFollowers names the jobs a loop of this process is running, so a job has one loop.
var extractFollowers sync.Map

// followExtractJob runs a job in the background under the token of the person who asked.
// A job waiting on a provider batch is run again every minute until the batch ends (a
// day at most); after a restart the owner's next poll of the job starts this again.
func (s *Service) followExtractJob(ctx context.Context, token string, scope Scope, id string) {
	if _, busy := extractFollowers.LoadOrStore(id, true); busy {
		return
	}
	go func() {
		defer extractFollowers.Delete(id)
		for start := time.Now(); ; time.Sleep(extractBatchPoll) {
			extractJobSlots <- struct{}{} // held per run, not while waiting on a batch
			view, err := s.RunFileExtractJob(ctx, token, scope, id)
			<-extractJobSlots
			if err != nil || !extractBatchWaiting(view) || time.Since(start) > extractBatchWaitMax {
				return
			}
		}
	}()
}

func extractBatchWaiting(v FileExtractJobView) bool {
	return v.Status == JobStatusRetry && v.Job.Error == extractBatchPending
}
