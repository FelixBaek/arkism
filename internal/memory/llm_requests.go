package memory

import (
	"context"
	"sort"
	"time"
	"unicode/utf8"
)

// Model call traces: every server-side WikiLLM.Complete call is recorded as one
// llm_requests row in the caller's scope: sizes and hashes of the prompt and the
// answer, tokens, latency and outcome. The text itself is kept only when the
// server runs with ARKISM_LLM_TRACE_CONTENT=1, redacted and truncated, and
// rows expire with the audit retention run (ARKISM_LLM_TRACE_RETENTION_DAYS).

const (
	colLLMRequests    = "llm_requests"
	llmRequestKind    = "llm_request"
	llmTraceTextRunes = 4000
	llmStatsMaxRows   = 5000
)

type LLMRequest struct {
	Operation      string     `json:"operation" bson:"operation"`
	Generator      *Generator `json:"generator,omitempty" bson:"generator,omitempty"`
	PromptChars    int        `json:"prompt_chars" bson:"prompt_chars"`
	PromptSHA256   string     `json:"prompt_sha256" bson:"prompt_sha256"`
	ResponseChars  int        `json:"response_chars" bson:"response_chars"`
	ResponseSHA256 string     `json:"response_sha256,omitempty" bson:"response_sha256,omitempty"`
	InputTokens    int64      `json:"input_tokens" bson:"input_tokens"`
	OutputTokens   int64      `json:"output_tokens" bson:"output_tokens"`
	TotalTokens    int64      `json:"total_tokens" bson:"total_tokens"`
	LatencyMS      int64      `json:"latency_ms" bson:"latency_ms"`
	Status         string     `json:"status" bson:"status"` // ok | error
	Error          string     `json:"error,omitempty" bson:"error,omitempty"`
	Prompt         string     `json:"prompt,omitempty" bson:"prompt,omitempty"`
	Response       string     `json:"response,omitempty" bson:"response,omitempty"`
}

type llmCallKey struct{}

type llmCall struct {
	operation string
	scope     Scope
}

// WithLLMOperation names the model calls made with ctx and the scope their
// traces are filed in. A call without it is not traced.
func WithLLMOperation(ctx context.Context, operation string, scope Scope) context.Context {
	return context.WithValue(ctx, llmCallKey{}, llmCall{operation, scope})
}

// tracedLLM decorates a WikiLLM; it changes nothing about the call itself.
type tracedLLM struct {
	inner        WikiLLM
	svc          *Service
	storeContent bool
}

// TraceLLM wraps inner so every Complete call is recorded; nil stays nil.
func TraceLLM(svc *Service, inner WikiLLM, storeContent bool) WikiLLM {
	if inner == nil {
		return nil
	}
	return &tracedLLM{inner: inner, svc: svc, storeContent: storeContent}
}

func (t *tracedLLM) Generator() Generator { return t.inner.Generator() }

func (t *tracedLLM) Complete(ctx context.Context, system, user string, maxTokens int) (string, ProviderUsage, error) {
	start := time.Now()
	out, usage, err := t.inner.Complete(ctx, system, user, maxTokens)
	t.record(ctx, start, system+"\n\n"+user, out, usage, err)
	return out, usage, err
}

// record is best effort: tracing never changes or fails the call it describes.
func (t *tracedLLM) record(ctx context.Context, start time.Time, prompt, out string, usage ProviderUsage, callErr error) {
	call, ok := ctx.Value(llmCallKey{}).(llmCall)
	if !ok || t.svc == nil || t.svc.Repo == nil || !validScope(call.scope) || !validRunLabel(call.operation, 128) {
		return
	}
	defer func() { _ = recover() }()
	gen := t.inner.Generator()
	tr := LLMRequest{Operation: call.operation, Generator: &gen, PromptChars: utf8.RuneCountInString(prompt), PromptSHA256: ContentHash(prompt),
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, TotalTokens: max(usage.TotalTokens, usage.InputTokens+usage.OutputTokens),
		LatencyMS: time.Since(start).Milliseconds(), Status: "ok"}
	if callErr != nil {
		tr.Status, tr.Error = "error", captureErrorCode(callErr)
	} else {
		tr.ResponseChars, tr.ResponseSHA256 = utf8.RuneCountInString(out), ContentHash(out)
	}
	if t.storeContent {
		tr.Prompt = traceText(prompt)
		if callErr == nil {
			tr.Response = traceText(out)
		}
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	r := Record{ID: timeOrderedID(now.UnixMilli()), Scope: call.scope, Kind: llmRequestKind, Status: tr.Status, Revision: 1,
		CreatedAt: now, UpdatedAt: now, CreatedBy: "server", CreatedByType: roleService, LLMRequest: &tr}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_ = t.svc.Repo.Insert(wctx, colLLMRequests, r)
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
	Status    string
	Before    string
	Limit     int
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
	if f.Limit < 0 || f.Limit > 200 || f.Operation != "" && !validRunLabel(f.Operation, 128) ||
		f.Status != "" && f.Status != "ok" && f.Status != "error" || f.Before != "" && !validRunLabel(f.Before, 64) {
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
	if f.Operation != "" {
		q.Path, q.Value = []string{"llm_request", "operation"}, f.Operation
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
		if r.LLMRequest != nil {
			out.Items = append(out.Items, LLMRequestView{ID: r.ID, Scope: r.Scope, CreatedAt: r.CreatedAt, Request: *r.LLMRequest})
		}
	}
	return out, nil
}

type LLMRequestStat struct {
	Day          string `json:"day"` // UTC, YYYY-MM-DD
	Operation    string `json:"operation"`
	Calls        int64  `json:"calls"`
	Errors       int64  `json:"errors"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
}

type LLMRequestStats struct {
	Since time.Time        `json:"since"`
	Items []LLMRequestStat `json:"items"`
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
	out := LLMRequestStats{Since: since, Items: []LLMRequestStat{}}
	stats := map[[2]string]*LLMRequestStat{}
	before, seen := "", 0
	for seen < llmStatsMaxRows {
		rows, err := store.ListScopedRows(ctx, scopedRowQuery{Collection: colLLMRequests, Kind: llmRequestKind, Scope: scope, Before: before, Limit: 500})
		if err != nil {
			return LLMRequestStats{}, err
		}
		for _, r := range rows {
			if r.CreatedAt.Before(since) {
				return finishLLMStats(out, stats), nil
			}
			seen++
			addLLMStat(stats, r)
		}
		if len(rows) < 500 {
			return finishLLMStats(out, stats), nil
		}
		before = rows[len(rows)-1].ID
	}
	out.Truncated = true
	return finishLLMStats(out, stats), nil
}

func addLLMStat(stats map[[2]string]*LLMRequestStat, r Record) {
	if r.LLMRequest == nil {
		return
	}
	key := [2]string{r.CreatedAt.UTC().Format(time.DateOnly), r.LLMRequest.Operation}
	st := stats[key]
	if st == nil {
		st = &LLMRequestStat{Day: key[0], Operation: key[1]}
		stats[key] = st
	}
	st.Calls++
	if r.LLMRequest.Status == "error" {
		st.Errors++
	}
	st.InputTokens += r.LLMRequest.InputTokens
	st.OutputTokens += r.LLMRequest.OutputTokens
	st.TotalTokens += r.LLMRequest.TotalTokens
}

func finishLLMStats(out LLMRequestStats, stats map[[2]string]*LLMRequestStat) LLMRequestStats {
	for _, st := range stats {
		out.Items = append(out.Items, *st)
	}
	sort.Slice(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		return a.Day > b.Day || a.Day == b.Day && a.Operation < b.Operation
	})
	return out
}
