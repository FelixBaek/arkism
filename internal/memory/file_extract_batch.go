package memory

// Provider batch mode for queued extraction jobs (opt-in: MEMORY_EXTRACT_BATCH=1 with
// the messages protocol). A job's remaining chunks go to the provider as one batch,
// answered within hours at a lower price; the job waits as retry/batch_pending and is
// polled until the batch ends, then every answer is filed in the usage ledger and read
// exactly like a direct answer. Every answer still ends as candidates.

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	extractBatchPending   = "batch_pending"
	extractBatchResultMax = 16 << 20
	extractBatchPoll      = time.Minute
	extractBatchWaitMax   = 24 * time.Hour
)

var errExtractBatchPending = errors.New("extraction batch pending")

// BatchLLM is a server model that can also take many prompts as one provider batch.
type BatchLLM interface {
	Batches() bool
	SubmitBatch(ctx context.Context, prompts []BatchPrompt, maxTokens int) (string, error)
	// BatchResults returns the answers by prompt id once the batch ended; done is false until then.
	BatchResults(ctx context.Context, id string) (map[string]BatchAnswer, bool, error)
}

type BatchPrompt struct{ ID, System, User string }

// BatchAnswer is one prompt's answer; Failed marks a prompt the provider did not answer.
type BatchAnswer struct {
	Text   string
	Usage  ProviderUsage
	Failed bool
}

// ExtractBatch is the provider batch a job's remaining chunks were sent as.
type ExtractBatch struct {
	ID          string    `json:"id" bson:"id"`
	SubmittedAt time.Time `json:"submitted_at" bson:"submitted_at"`
	// Reserved maps each prompt id to the ledger row reserved for it at submit.
	Reserved map[string]string `json:"-" bson:"reserved"`
}

func extractBatchID(i int) string { return "chunk-" + strconv.Itoa(i) }

// extractBatcher is the batch client for job, or nil when the job calls the model per chunk:
// batching is off, the mode calls no model, or only a request's worth of chunks is left.
// A batch submitted before batching was turned off is dropped and the job goes on per chunk.
func (s *Service) extractBatcher(ctx context.Context, scope Scope, job *FileExtractJob) BatchLLM {
	b, ok := s.Wiki.(BatchLLM)
	if !ok || !b.Batches() || job.Settings.Mode == spaceExtractChunks || job.Batch == nil && len(job.Chunks)-job.Next <= fileExtractSyncChunks {
		if job.Batch != nil {
			s.dropExtractBatch(ctx, scope, job)
		}
		return nil
	}
	return b
}

// dropExtractBatch forgets a batch whose answers will not be read and closes its reserved rows.
func (s *Service) dropExtractBatch(ctx context.Context, scope Scope, job *FileExtractJob) {
	s.releaseExtractCalls(ctx, scope, job.Batch.Reserved)
	job.Batch = nil
}

// extractBatchAnswers submits the job's remaining chunks (errExtractBatchPending), or reads
// the ended batch: each answer settles the ledger row reserved for it at submit and is read
// as a direct answer would be.
func (s *Service) extractBatchAnswers(ctx context.Context, token string, d Decision, scope Scope, b BatchLLM, job *FileExtractJob) (map[int]ExtractPreviewResult, error) {
	if job.Batch == nil {
		return nil, s.submitExtractBatch(ctx, token, d, scope, b, job)
	}
	got, done, err := b.BatchResults(ctx, job.Batch.ID)
	if err != nil || !done {
		if time.Since(job.Batch.SubmittedAt) <= extractBatchWaitMax {
			return nil, errExtractBatchPending // a failed poll is tried again until the batch's day is over
		}
		s.dropExtractBatch(ctx, scope, job)
		return nil, cmp.Or(err, ErrUnavailable)
	}
	out := map[int]ExtractPreviewResult{}
	for i := job.Next; i < len(job.Chunks); i++ {
		ch, a := job.Chunks[i], got[extractBatchID(i)]
		ok := !a.Failed && a.Text != "" && len(a.Text) <= 256<<10 && utf8.ValidString(a.Text)
		if err = s.fileBatchAnswer(ctx, scope, job, i, a, ok); err != nil {
			return nil, err
		}
		job.Calls, job.Tokens = job.Calls+1, job.Tokens+max(a.Usage.TotalTokens, a.Usage.InputTokens+a.Usage.OutputTokens)
		if !ok {
			out[i] = ExtractPreviewResult{Skipped: []ExtractSkip{{Locator: ch.Locator, Reason: "batch_failed"}}}
			continue
		}
		out[i] = readExtractAnswer(a.Text, job, ch)
	}
	job.Batch = nil // filed: a later failure resumes per chunk instead of filing the answers twice
	return out, nil
}

// fileBatchAnswer meters one batch answer like a direct call: its span, counters, trace
// and the ledger row reserved for it at submit.
func (s *Service) fileBatchAnswer(ctx context.Context, scope Scope, job *FileExtractJob, i int, a BatchAnswer, ok bool) error {
	system, user := extractPrompt(job.Chunks[i].Text, job)
	m := modelCallSpec{scope: scope, operation: fileExtractOperation, kind: "complete", provider: wikiProvider, model: s.Wiki.Generator().Model, prompt: system + "\n\n" + user}
	var callErr error
	if !ok {
		callErr = ErrUnavailable
	}
	_, sp := startSpan(ctx, "model.batch")
	_, err := s.fileModelCall(ctx, sp, m, reservedCallRow(scope, job.Batch.Reserved[extractBatchID(i)]), a.Text, a.Usage, job.Batch.SubmittedAt, callErr)
	return err
}

// submitExtractBatch checks every chunk may leave the server, reserves a ledger row per
// chunk within the day's call cap, then submits them as one batch.
func (s *Service) submitExtractBatch(ctx context.Context, token string, d Decision, scope Scope, b BatchLLM, job *FileExtractJob) error {
	prompts, ids := []BatchPrompt{}, []string{}
	for i := job.Next; i < len(job.Chunks); i++ {
		if err := s.extractTransfer(ctx, token, d, scope, job.Chunks[i]); err != nil {
			return err
		}
		system, user := extractPrompt(job.Chunks[i].Text, job)
		prompts, ids = append(prompts, BatchPrompt{ID: extractBatchID(i), System: system, User: user}), append(ids, extractBatchID(i))
	}
	reserved, err := s.reserveExtractCalls(ctx, scope, ids)
	if err != nil {
		return err
	}
	id, err := b.SubmitBatch(ctx, prompts, wikiMaxCallTokens)
	if err != nil {
		s.releaseExtractCalls(ctx, scope, reserved)
		return err
	}
	job.Batch = &ExtractBatch{ID: id, SubmittedAt: time.Now().UTC(), Reserved: reserved}
	return errExtractBatchPending
}

// extractBudgetMu makes counting the day's calls and reserving a batch's rows one step, so
// two batches submitted at once cannot both pass the cap.
// shortcut: guards one server process; several processes on one database would need a database lock.
var extractBudgetMu sync.Mutex

// reserveExtractCalls reserves one pending ledger row per prompt id, all or none, within
// the day's call cap; the rows count against the cap from now on.
func (s *Service) reserveExtractCalls(ctx context.Context, scope Scope, ids []string) (map[string]string, error) {
	extractBudgetMu.Lock()
	defer extractBudgetMu.Unlock()
	calls, err := s.wikiDailyCalls(ctx, scope, time.Now())
	if err != nil {
		return nil, err
	}
	if calls+len(ids) > wikiDailyCallLimit {
		return nil, ErrModelBudget
	}
	out := map[string]string{}
	for _, id := range ids {
		r, err := s.beginProviderCall(ctx, scope, fileExtractOperation, wikiProvider, s.Wiki.Generator().Model)
		if err != nil {
			s.releaseExtractCalls(ctx, scope, out)
			return nil, err
		}
		out[id] = r.ID
	}
	return out, nil
}

// releaseExtractCalls closes reserved rows whose prompts were never answered as failed calls.
func (s *Service) releaseExtractCalls(ctx context.Context, scope Scope, reserved map[string]string) {
	for _, id := range reserved {
		_ = s.settleExtractCall(ctx, scope, id, ProviderUsage{}, false)
	}
}

// settleExtractCall closes a reserved row whose prompt was never answered as a failed call.
func (s *Service) settleExtractCall(ctx context.Context, scope Scope, id string, usage ProviderUsage, ok bool) error {
	model := "unknown"
	if s.Wiki != nil {
		model = s.Wiki.Generator().Model
	}
	usage.Provider, usage.Model, usage.Requests = cmp.Or(usage.Provider, wikiProvider), cmp.Or(usage.Model, model), max(usage.Requests, 1)
	return s.finishProviderCall(ctx, reservedCallRow(scope, id), usage, ok)
}

// reservedCallRow is the pending ledger row reserved under id, as finishProviderCall checks it.
func reservedCallRow(scope Scope, id string) Record {
	return Record{ID: id, Scope: scope, Revision: 1, Cost: &ProviderCost{}}
}

// Batches reports whether the operator turned batch mode on for a messages-protocol model.
func (c *ChatWikiLLM) Batches() bool { return c.Batch && c.Config.Protocol == llmProtocolMessages }

// SubmitBatch posts the prompts to {endpoint}/batches and returns the batch id.
func (c *ChatWikiLLM) SubmitBatch(ctx context.Context, prompts []BatchPrompt, maxTokens int) (string, error) {
	if !c.Batches() || validateEndpoint(c.Config.Endpoint) != nil {
		return "", ErrModelNotConfigured
	}
	requests := []map[string]any{}
	for _, p := range prompts {
		requests = append(requests, map[string]any{"custom_id": p.ID, "params": map[string]any{"model": c.Config.Model, "temperature": 0, "max_tokens": maxTokens,
			"system": p.System, "messages": []map[string]any{{"role": "user", "content": p.User}}}})
	}
	encoded, err := json.Marshal(map[string]any{"requests": requests})
	if err != nil || len(encoded) > defaultRequestMax {
		return "", ErrInvalid
	}
	body, status, err := postBody(ctx, providerClient(nil, 120*time.Second), c.Config.Endpoint+"/batches", c.Config.header(), encoded, defaultResponseMax)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", dependencyStatusError("wiki llm batch", status)
	}
	var out struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(body, &out) != nil || !validRunLabel(out.ID, 256) {
		return "", ErrUnavailable
	}
	return out.ID, nil
}

// BatchResults polls the batch and, once it ended, reads its results file. The results
// file must be on the endpoint's own host: the key is sent there.
func (c *ChatWikiLLM) BatchResults(ctx context.Context, id string) (map[string]BatchAnswer, bool, error) {
	if !c.Batches() || validateEndpoint(c.Config.Endpoint) != nil {
		return nil, false, ErrModelNotConfigured
	}
	client := providerClient(nil, 120*time.Second)
	body, status, err := sendBody(ctx, client, http.MethodGet, c.Config.Endpoint+"/batches/"+url.PathEscape(id), c.Config.header(), nil, defaultResponseMax)
	if err != nil {
		return nil, false, err
	}
	if status != http.StatusOK {
		return nil, false, dependencyStatusError("wiki llm batch", status)
	}
	var state struct {
		Status     string `json:"processing_status"`
		ResultsURL string `json:"results_url"`
	}
	if json.Unmarshal(body, &state) != nil {
		return nil, false, ErrUnavailable
	}
	if state.Status != "ended" {
		return nil, false, nil
	}
	base, _ := url.Parse(c.Config.Endpoint)
	results, err := url.Parse(state.ResultsURL)
	if err != nil || results.Scheme != base.Scheme || results.Host != base.Host {
		return nil, false, ErrUnavailable
	}
	body, status, err = sendBody(ctx, client, http.MethodGet, results.String(), c.Config.header(), nil, extractBatchResultMax)
	if err != nil {
		return nil, false, err
	}
	if status != http.StatusOK {
		return nil, false, dependencyStatusError("wiki llm batch", status)
	}
	return c.batchAnswers(body)
}

// batchAnswers reads the results file: one JSON line per prompt.
func (c *ChatWikiLLM) batchAnswers(body []byte) (map[string]BatchAnswer, bool, error) {
	out := map[string]BatchAnswer{}
	lines := bufio.NewScanner(bytes.NewReader(body))
	lines.Buffer(nil, extractBatchResultMax)
	for lines.Scan() {
		var line struct {
			CustomID string `json:"custom_id"`
			Result   struct {
				Type    string          `json:"type"`
				Message json.RawMessage `json:"message"`
			} `json:"result"`
		}
		if len(bytes.TrimSpace(lines.Bytes())) == 0 {
			continue
		}
		if json.Unmarshal(lines.Bytes(), &line) != nil || !validRunLabel(line.CustomID, 64) {
			return nil, false, ErrUnavailable
		}
		usage, _, text, ok := parseDraftResponse(line.Result.Message, "")
		usage.Provider, usage.Model = wikiProvider, c.Config.Model
		out[line.CustomID] = BatchAnswer{Text: text, Usage: usage, Failed: line.Result.Type != "succeeded" || !ok}
	}
	if lines.Err() != nil {
		return nil, false, fmt.Errorf("read batch results: %w", ErrUnavailable)
	}
	return out, true, nil
}
