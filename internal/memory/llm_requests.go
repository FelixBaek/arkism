package memory

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Model call traces: every server-side model call goes through meteredCall,
// which reserves and settles its provider_usage row, opens a model span with
// gen_ai.* attributes, feeds the process metrics and files one llm_requests
// row in the caller's scope: sizes and hashes of the prompt and the answer,
// tokens, latency, outcome, the trace/span/parent ids that place the call in
// its request or job, and the memory ids it read and created. The text itself
// is kept only when the server runs with ARKISM_LLM_TRACE_CONTENT=1, redacted
// and truncated. Rows expire with the audit retention run
// (ARKISM_LLM_TRACE_RETENTION_DAYS) and are deleted with the source document
// they were made from; LLMTraceInclude/LLMTraceExclude limit which operations
// are recorded at all (the ledger row is written either way).

const (
	colLLMRequests    = "llm_requests"
	llmRequestKind    = "llm_request"
	llmTraceTextRunes = 4000
	llmStatsMaxRows   = 5000
	llmTraceMaxIDs    = 64
	llmStatsRuns      = 20
)

type LLMRequest struct {
	Operation      string     `json:"operation" bson:"operation"`
	Generator      *Generator `json:"generator,omitempty" bson:"generator,omitempty"`
	Provider       string     `json:"provider,omitempty" bson:"provider,omitempty"`
	PromptChars    int        `json:"prompt_chars" bson:"prompt_chars"`
	PromptSHA256   string     `json:"prompt_sha256" bson:"prompt_sha256"`
	ResponseChars  int        `json:"response_chars" bson:"response_chars"`
	ResponseSHA256 string     `json:"response_sha256,omitempty" bson:"response_sha256,omitempty"`
	InputTokens    int64      `json:"input_tokens" bson:"input_tokens"`
	OutputTokens   int64      `json:"output_tokens" bson:"output_tokens"`
	TotalTokens    int64      `json:"total_tokens" bson:"total_tokens"`
	// CachedInputTokens is the part of InputTokens served from the provider's prompt cache.
	CachedInputTokens int64 `json:"cached_input_tokens,omitempty" bson:"cached_input_tokens,omitempty"`
	// ReasoningTokens is the part of OutputTokens the model spent thinking, when the provider says.
	ReasoningTokens int64 `json:"reasoning_tokens,omitempty" bson:"reasoning_tokens,omitempty"`
	// CallKind is llm, embedding or rerank (llmCallKind); rows filed before it existed have none.
	CallKind  string `json:"call_kind,omitempty" bson:"call_kind,omitempty"`
	LatencyMS int64  `json:"latency_ms" bson:"latency_ms"`
	Status    string `json:"status" bson:"status"` // ok | error
	Error     string `json:"error,omitempty" bson:"error,omitempty"`
	Prompt    string `json:"prompt,omitempty" bson:"prompt,omitempty"`
	Response  string `json:"response,omitempty" bson:"response,omitempty"`
	// TraceID groups the calls of one request or job; ParentSpanID is the job's
	// (or, with tracing on, the enclosing) span. With tracing on they are the
	// exported span's own ids.
	TraceID      string `json:"trace_id,omitempty" bson:"trace_id,omitempty"`
	SpanID       string `json:"span_id,omitempty" bson:"span_id,omitempty"`
	ParentSpanID string `json:"parent_span_id,omitempty" bson:"parent_span_id,omitempty"`
	JobID        string `json:"job_id,omitempty" bson:"job_id,omitempty"`
	// DocumentID is the source document the call read; deleting it deletes the trace.
	DocumentID       string   `json:"document_id,omitempty" bson:"document_id,omitempty"`
	SourceMemoryIDs  []string `json:"source_memory_ids,omitempty" bson:"source_memory_ids,omitempty"`
	CreatedMemoryIDs []string `json:"created_memory_ids,omitempty" bson:"created_memory_ids,omitempty"`
}

// modelCallSpec names one metered model call. kind picks the span name
// (model.<kind>) and the gen_ai.operation.name; prompt is traced as a size and
// a hash, and as redacted text only with LLMTraceContent.
type modelCallSpec struct {
	scope           Scope
	operation, kind string
	provider, model string
	prompt          string
}

// meteredCall is the one shared ledger call: it reserves a provider_usage row,
// makes the call, files what it used and traces it. callErr is the provider's
// failure (filed as unsuccessful); err is a ledger failure, and with a ledger
// failure before the call the call is not made.
func meteredCall[T any](ctx context.Context, s *Service, m modelCallSpec, call func(context.Context) (T, ProviderUsage, error)) (out T, usage ProviderUsage, callErr, err error) {
	// An extension's refusal is a failed call that was never made, so callers take their fallbacks.
	if ctx, callErr = s.Extensions.beforeModelCall(ctx, &m); callErr != nil {
		return out, ProviderUsage{}, callErr, nil
	}
	rows, err := s.reserveProviderCalls(ctx, m.scope, m.operation, m.provider, m.model, 1, operationCap(m.operation))
	if err != nil {
		return out, ProviderUsage{}, nil, err
	}
	reserved := rows[0]
	ctx, sp := startSpan(ctx, "model."+m.kind)
	start := time.Now()
	out, usage, callErr = call(ctx)
	text, _ := any(out).(string)
	usage, err = s.fileModelCall(ctx, sp, m, reserved, text, usage, start, callErr)
	if err != nil && callErr == nil {
		return out, usage, nil, err
	}
	return out, usage, callErr, nil
}

// fileModelCall is the second half of every metered call, direct or answered later in a
// provider batch: it ends the call's span, counts and traces the call, and settles its
// reserved ledger row with what it used (even after the caller gave up on the call).
func (s *Service) fileModelCall(ctx context.Context, sp *span, m modelCallSpec, reserved Record, text string, usage ProviderUsage, start time.Time, callErr error) (ProviderUsage, error) {
	usage = filledUsage(usage, m.provider, m.model)
	setGenAI(sp, m, usage)
	sp.end(callErr)
	observeLLMCall(m.operation, usage.Provider, usage.Model, callErr == nil, time.Since(start), usage)
	s.Extensions.afterModelCall(ctx, m, usage, time.Since(start), callErr)
	s.recordLLMRequest(ctx, m, sp, text, usage, start, callErr)
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return usage, s.finishProviderCall(fctx, reserved, usage, callErr == nil)
}

func filledUsage(usage ProviderUsage, provider, model string) ProviderUsage {
	if usage.Provider == "" {
		usage.Provider = provider
	}
	if usage.Model == "" {
		usage.Model = model
	}
	if usage.Requests == 0 {
		usage.Requests = 1
	}
	return usage
}

// setGenAI sets the generative-AI semantic convention attributes of a model span.
func setGenAI(sp *span, m modelCallSpec, usage ProviderUsage) {
	op := "chat"
	switch m.kind {
	case "embed":
		op = "embeddings"
	case "rerank":
		op = "rerank"
	}
	sp.set("arkism.operation", m.operation)
	sp.set("gen_ai.operation.name", op)
	sp.set("gen_ai.provider.name", usage.Provider)
	sp.set("gen_ai.request.model", m.model)
	sp.set("gen_ai.response.model", usage.Model)
	sp.setInt("gen_ai.usage.input_tokens", usage.InputTokens)
	sp.setInt("gen_ai.usage.output_tokens", usage.OutputTokens)
}

type llmJobKey struct{}

type llmJob struct{ id, traceID, spanID string }

// startJobSpan opens the span of one background job. Model calls under the
// returned ctx share its trace id and name it as their parent, tracing on or off.
func startJobSpan(ctx context.Context, name, jobID string) (context.Context, *span) {
	ctx, sp := startSpan(ctx, name)
	traceID, spanID, _ := sp.ids()
	if sp == nil {
		traceID, spanID = ContentHash("job:" + jobID)[:32], randomHex(8)
	}
	return context.WithValue(ctx, llmJobKey{}, llmJob{jobID, traceID, spanID}), sp
}

// llmTraceIDs places one call: the span's own ids with tracing on; otherwise
// the job's trace, else the request's, else a fresh one.
func llmTraceIDs(ctx context.Context, sp *span) (traceID, spanID, parentID, jobID string) {
	job, _ := ctx.Value(llmJobKey{}).(llmJob)
	if sp != nil {
		traceID, spanID, parentID = sp.ids()
		return traceID, spanID, parentID, job.id
	}
	spanID = randomHex(8)
	if job.traceID != "" {
		return job.traceID, spanID, job.spanID, job.id
	}
	if id := boundAuditRequestID(ctx); id != "" {
		return ContentHash("request:" + id)[:32], spanID, "", ""
	}
	return randomHex(16), spanID, "", ""
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type llmNoteKey struct{}

// llmNote is what a caller tells the traces of the calls made under its ctx:
// the document and memories they read. recordLLMRequest adds the rows it wrote
// so attachLLMCreated can name the memories the calls produced.
type llmNote struct {
	document string
	sources  []string
	mu       sync.Mutex
	rows     []string
}

func withLLMNote(ctx context.Context, document string, sources []string) (context.Context, *llmNote) {
	n := &llmNote{document: document, sources: clipIDs(sources)}
	return context.WithValue(ctx, llmNoteKey{}, n), n
}

func clipIDs(ids []string) []string {
	if len(ids) > llmTraceMaxIDs {
		ids = ids[:llmTraceMaxIDs]
	}
	return slices.Clone(ids)
}

// attachLLMCreated names the memories created from the calls noted in n on
// their trace rows. Best effort, like the trace itself.
func (s *Service) attachLLMCreated(ctx context.Context, scope Scope, n *llmNote, created []string) {
	if n == nil || len(created) == 0 || s.Repo == nil {
		return
	}
	n.mu.Lock()
	rows := slices.Clone(n.rows)
	n.mu.Unlock()
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	for _, id := range rows {
		_ = s.Repo.Transaction(wctx, func(ctx context.Context, tx Tx) error {
			r, err := tx.Get(ctx, colLLMRequests, scope, id)
			if err != nil || r.LLMRequest == nil {
				return err
			}
			tr := *r.LLMRequest
			tr.CreatedMemoryIDs = clipIDs(append(tr.CreatedMemoryIDs, created...))
			r.LLMRequest, r.UpdatedAt = &tr, time.Now().UTC().Truncate(time.Millisecond)
			prev := r.Revision
			r.Revision++
			return tx.Replace(ctx, colLLMRequests, r, prev)
		})
	}
}

// deleteLLMTracesFor deletes, inside tx, the traces of calls that read the
// source document id. A repository without row listing keeps them until retention.
func deleteLLMTracesFor(ctx context.Context, tx Tx, scope Scope, id string) error {
	store, ok := tx.(scopedRowStore)
	if !ok || id == "" {
		return nil
	}
	// Pages by id so a row that row security hides from DELETE cannot loop the walk.
	before := ""
	for {
		rows, err := store.ListScopedRows(ctx, scopedRowQuery{Collection: colLLMRequests, Kind: llmRequestKind, Scope: scope,
			Path: []string{"llm_request", "document_id"}, Value: id, Before: before, Limit: 500})
		if err != nil {
			return err
		}
		for _, r := range rows {
			if err := tx.Delete(ctx, colLLMRequests, scope, r.ID, r.Revision); err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		if len(rows) < 500 {
			return nil
		}
		before = rows[len(rows)-1].ID
	}
}

// traceOperation reports whether calls of operation are recorded: listed by
// LLMTraceInclude (empty: all) and not by LLMTraceExclude. A pattern ending in
// * matches a prefix.
func (s *Service) traceOperation(operation string) bool {
	match := func(patterns []string) bool {
		return slices.ContainsFunc(patterns, func(p string) bool {
			if prefix, ok := strings.CutSuffix(p, "*"); ok {
				return strings.HasPrefix(operation, prefix)
			}
			return p == operation
		})
	}
	return (len(s.LLMTraceInclude) == 0 || match(s.LLMTraceInclude)) && !match(s.LLMTraceExclude)
}

// recordLLMRequest files one model call as an llm_requests row. It is best
// effort: tracing never changes or fails the call it describes. out is empty
// for calls that return no text.
func (s *Service) recordLLMRequest(ctx context.Context, m modelCallSpec, sp *span, out string, usage ProviderUsage, start time.Time, callErr error) {
	if s.Repo == nil || !validScope(m.scope) || !validRunLabel(m.operation, 128) || !s.traceOperation(m.operation) {
		return
	}
	defer func() { _ = recover() }()
	tr := LLMRequest{Operation: m.operation, Generator: &Generator{Model: usage.Model}, Provider: usage.Provider,
		PromptChars: utf8.RuneCountInString(m.prompt), PromptSHA256: ContentHash(m.prompt),
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, TotalTokens: max(usage.TotalTokens, usage.InputTokens+usage.OutputTokens),
		CachedInputTokens: usage.CachedInputTokens, ReasoningTokens: usage.ReasoningTokens, CallKind: llmCallKind(m.operation),
		LatencyMS: time.Since(start).Milliseconds(), Status: "ok"}
	tr.TraceID, tr.SpanID, tr.ParentSpanID, tr.JobID = llmTraceIDs(ctx, sp)
	note, _ := ctx.Value(llmNoteKey{}).(*llmNote)
	if note != nil {
		tr.DocumentID, tr.SourceMemoryIDs = note.document, note.sources
	}
	if callErr != nil {
		tr.Status, tr.Error = "error", captureErrorCode(callErr)
	} else if out != "" {
		tr.ResponseChars, tr.ResponseSHA256 = utf8.RuneCountInString(out), ContentHash(out)
	}
	if s.LLMTraceContent {
		tr.Prompt = traceText(m.prompt)
		if callErr == nil {
			tr.Response = traceText(out)
		}
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	r := Record{ID: timeOrderedID(now.UnixMilli()), Scope: m.scope, Kind: llmRequestKind, Status: tr.Status, Revision: 1,
		CreatedAt: now, UpdatedAt: now, CreatedBy: "server", CreatedByType: roleService, LLMRequest: &tr}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if s.Repo.Insert(wctx, colLLMRequests, r) == nil && note != nil {
		note.mu.Lock()
		if len(note.rows) < llmTraceMaxIDs {
			note.rows = append(note.rows, r.ID)
		}
		note.mu.Unlock()
	}
}

// traceText is stored model text: private spans masked, secrets redacted, clipped.
func traceText(s string) string {
	n := 0
	return runeCutString(redactCode(redactSessionPrivate(s), &n), llmTraceTextRunes)
}

type LLMRequestView struct {
	ID        string     `json:"id"`
	Scope     Scope      `json:"scope"`
	CreatedAt time.Time  `json:"created_at"`
	Request   LLMRequest `json:"request"`
}

type LLMRequestPage struct {
	Items      []LLMRequestView `json:"items"`
	NextBefore string           `json:"next_before,omitempty"`
}

type LLMRequestFilter struct {
	Operation string
	// CallKind is llm, embedding or rerank; with Operation set it must be that operation's kind.
	CallKind string
	Status   string
	Before   string
	Limit    int
	// Run is a trace_id: every call of one request or job.
	Run string
}

func (s *Service) humanLLMRequests(ctx context.Context, token string, scope Scope) (scopedRowStore, error) {
	if !validScope(scope) {
		return nil, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "read:"+colLLMRequests, scope, "", "")
	if err != nil {
		return nil, err
	}
	if d.Role != roleHuman {
		return nil, ErrDenied
	}
	return s.webhookStore()
}

func (s *Service) ListLLMRequests(ctx context.Context, token string, scope Scope, f LLMRequestFilter) (LLMRequestPage, error) {
	if !f.valid() {
		return LLMRequestPage{}, ErrInvalid
	}
	if f.Limit == 0 {
		f.Limit = 50
	}
	store, err := s.humanLLMRequests(ctx, token, scope)
	if err != nil {
		return LLMRequestPage{}, err
	}
	q := scopedRowQuery{Collection: colLLMRequests, Kind: llmRequestKind, Status: f.Status, Scope: scope, Before: f.Before, Limit: f.Limit + 1}
	switch {
	case f.Operation != "" && f.CallKind != "" && llmCallKind(f.Operation) != f.CallKind:
		return LLMRequestPage{Items: []LLMRequestView{}}, nil
	case f.Run != "":
		q.Path, q.Value = []string{"llm_request", "trace_id"}, f.Run
	case f.Operation != "":
		q.Path, q.Value = []string{"llm_request", "operation"}, f.Operation
	case f.CallKind != "":
		q.Path, q.Value = []string{"llm_request", "call_kind"}, f.CallKind
	}
	rows, err := store.ListScopedRows(ctx, q)
	if err != nil {
		return LLMRequestPage{}, err
	}
	out := LLMRequestPage{Items: []LLMRequestView{}}
	if len(rows) > f.Limit {
		rows = rows[:f.Limit]
		out.NextBefore = rows[f.Limit-1].ID
	}
	for _, r := range rows {
		if r.LLMRequest != nil && f.matchesRun(*r.LLMRequest) {
			out.Items = append(out.Items, LLMRequestView{ID: r.ID, Scope: r.Scope, CreatedAt: r.CreatedAt, Request: *r.LLMRequest})
		}
	}
	return out, nil
}

var llmCallKinds = []string{"llm", "embedding", "rerank"}

func (f LLMRequestFilter) valid() bool {
	return f.Limit >= 0 && f.Limit <= 200 && (f.Operation == "" || validRunLabel(f.Operation, 128)) && (f.CallKind == "" || slices.Contains(llmCallKinds, f.CallKind)) &&
		(f.Status == "" || f.Status == "ok" || f.Status == "error") && (f.Before == "" || validRunLabel(f.Before, 64)) && (f.Run == "" || validRunLabel(f.Run, 64))
}

// matchesRun applies operation and kind in Go when the store filtered by run.
// shortcut: such a page can hold fewer than limit items; one run has few calls, so add a second store path if runs grow large.
func (f LLMRequestFilter) matchesRun(q LLMRequest) bool {
	return f.Run == "" || (f.Operation == "" || q.Operation == f.Operation) && (f.CallKind == "" || llmCallKind(q.Operation) == f.CallKind)
}

// llmCallKind names a call's kind from its operation: the embedding and rerank
// operations are named so by meteredEmbed and meteredRerank (cost.go).
func llmCallKind(operation string) string {
	switch {
	case strings.HasPrefix(operation, "embed"):
		return "embedding"
	case operation == "rerank" || strings.HasSuffix(operation, "_rerank"):
		return "rerank"
	}
	return "llm"
}

// GetLLMRequest is one model call trace, read like the list (people only, the caller's scope).
func (s *Service) GetLLMRequest(ctx context.Context, token string, scope Scope, id string) (LLMRequestView, error) {
	if !validRunLabel(id, 64) {
		return LLMRequestView{}, ErrInvalid
	}
	if _, err := s.humanLLMRequests(ctx, token, scope); err != nil {
		return LLMRequestView{}, err
	}
	r, err := s.Repo.Get(ctx, colLLMRequests, scope, id)
	if err != nil {
		return LLMRequestView{}, err
	}
	if r.LLMRequest == nil || r.Kind != llmRequestKind {
		return LLMRequestView{}, ErrNotFound
	}
	return LLMRequestView{ID: r.ID, Scope: r.Scope, CreatedAt: r.CreatedAt, Request: *r.LLMRequest}, nil
}

type LLMRequestStat struct {
	Day          string `json:"day"` // UTC, YYYY-MM-DD
	Operation    string `json:"operation"`
	Calls        int64  `json:"calls"`
	Errors       int64  `json:"errors"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
	// CachedInputTokens is the part of InputTokens served from a provider prompt cache.
	CachedInputTokens int64 `json:"cached_input_tokens"`
}

// LLMRunStat is one run's calls in the window: a run is a trace (else a job) id.
type LLMRunStat struct {
	Run         string `json:"run"`
	Calls       int64  `json:"calls"`
	TotalTokens int64  `json:"total_tokens"`
}

type LLMRequestStats struct {
	Since time.Time        `json:"since"`
	Items []LLMRequestStat `json:"items"`
	// Runs are the runs with more than one call, busiest first (at most llmStatsRuns).
	Runs []LLMRunStat `json:"runs"`
	// Truncated: the window held more than llmStatsMaxRows calls; only the newest were counted.
	Truncated bool `json:"truncated,omitempty"`
}

// LLMRequestStatsFor counts the last days (1–31) of traces by operation and UTC day.
// shortcut: counts in Go over at most llmStatsMaxRows rows; move to SQL aggregation if a scope outgrows it.
func (s *Service) LLMRequestStatsFor(ctx context.Context, token string, scope Scope, days int) (LLMRequestStats, error) {
	if days == 0 {
		days = 7
	}
	if days < 1 || days > 31 {
		return LLMRequestStats{}, ErrInvalid
	}
	store, err := s.humanLLMRequests(ctx, token, scope)
	if err != nil {
		return LLMRequestStats{}, err
	}
	now := time.Now().UTC()
	since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1-days)
	out := LLMRequestStats{Since: since, Items: []LLMRequestStat{}, Runs: []LLMRunStat{}}
	acc := llmStatsAcc{days: map[[2]string]*LLMRequestStat{}, runs: map[string]*LLMRunStat{}}
	before, seen := "", 0
	for seen < llmStatsMaxRows {
		rows, err := store.ListScopedRows(ctx, scopedRowQuery{Collection: colLLMRequests, Kind: llmRequestKind, Scope: scope, Before: before, Limit: 500})
		if err != nil {
			return LLMRequestStats{}, err
		}
		for _, r := range rows {
			if r.CreatedAt.Before(since) {
				return acc.finish(out), nil
			}
			seen++
			acc.add(r)
		}
		if len(rows) < 500 {
			return acc.finish(out), nil
		}
		before = rows[len(rows)-1].ID
	}
	out.Truncated = true
	return acc.finish(out), nil
}

// llmStatsAcc folds trace rows by day and operation, and by run.
type llmStatsAcc struct {
	days map[[2]string]*LLMRequestStat
	runs map[string]*LLMRunStat
}

func (a llmStatsAcc) add(r Record) {
	q := r.LLMRequest
	if q == nil {
		return
	}
	key := [2]string{r.CreatedAt.UTC().Format(time.DateOnly), q.Operation}
	st := a.days[key]
	if st == nil {
		st = &LLMRequestStat{Day: key[0], Operation: key[1]}
		a.days[key] = st
	}
	st.Calls++
	if q.Status == "error" {
		st.Errors++
	}
	st.InputTokens += q.InputTokens
	st.OutputTokens += q.OutputTokens
	st.TotalTokens += q.TotalTokens
	st.CachedInputTokens += q.CachedInputTokens
	if run := cmp.Or(q.TraceID, q.JobID); run != "" {
		rs := a.runs[run]
		if rs == nil {
			rs = &LLMRunStat{Run: run}
			a.runs[run] = rs
		}
		rs.Calls++
		rs.TotalTokens += q.TotalTokens
	}
}

func (a llmStatsAcc) finish(out LLMRequestStats) LLMRequestStats {
	for _, st := range a.days {
		out.Items = append(out.Items, *st)
	}
	sort.Slice(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		return a.Day > b.Day || a.Day == b.Day && a.Operation < b.Operation
	})
	for _, rs := range a.runs {
		if rs.Calls > 1 {
			out.Runs = append(out.Runs, *rs)
		}
	}
	sort.Slice(out.Runs, func(i, j int) bool {
		a, b := out.Runs[i], out.Runs[j]
		return a.Calls > b.Calls || a.Calls == b.Calls && a.Run < b.Run
	})
	out.Runs = out.Runs[:min(len(out.Runs), llmStatsRuns)]
	return out
}
