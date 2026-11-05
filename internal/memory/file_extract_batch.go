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
	"time"
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
}

func extractBatchID(i int) string { return "chunk-" + strconv.Itoa(i) }

// extractBatcher is the batch client for job, or nil when the job calls the model per chunk:
// batching is off, the mode calls no model, or only a request's worth of chunks is left.
func (s *Service) extractBatcher(job *FileExtractJob) BatchLLM {
	b, ok := s.Wiki.(BatchLLM)
	if !ok || !b.Batches() || job.Settings.Mode == spaceExtractChunks || job.Batch == nil && len(job.Chunks)-job.Next <= fileExtractSyncChunks {
		return nil
	}
	return b
}

// extractBatchAnswers submits the job's remaining chunks (errExtractBatchPending), or reads
// the ended batch: one ledger row per answer, each read as a direct answer would be.
func (s *Service) extractBatchAnswers(ctx context.Context, token string, d Decision, scope Scope, b BatchLLM, job *FileExtractJob) (map[int]ExtractPreviewResult, error) {
	if job.Batch == nil {
		return nil, s.submitExtractBatch(ctx, token, d, scope, b, job)
	}
	got, done, err := b.BatchResults(ctx, job.Batch.ID)
	if err != nil || !done {
		return nil, cmp.Or(err, errExtractBatchPending)
	}
	out := map[int]ExtractPreviewResult{}
	for i := job.Next; i < len(job.Chunks); i++ {
		ch, a := job.Chunks[i], got[extractBatchID(i)]
		text, usage, err := s.ledgerCall(ctx, modelCallSpec{scope: scope, operation: fileExtractOperation, kind: "complete", provider: wikiProvider, model: s.Wiki.Generator().Model}, func(context.Context) (string, ProviderUsage, error) {
			if a.Failed || a.Text == "" {
				return "", a.Usage, ErrUnavailable
			}
			return a.Text, a.Usage, nil
		})
		job.Calls, job.Tokens = job.Calls+1, job.Tokens+max(usage.TotalTokens, usage.InputTokens+usage.OutputTokens)
		if err != nil {
			out[i] = ExtractPreviewResult{Skipped: []ExtractSkip{{Locator: ch.Locator, Reason: "batch_failed"}}}
			continue
		}
		out[i] = readExtractAnswer(text, job, ch)
	}
	job.Batch = nil // filed: a later failure resumes per chunk instead of filing the answers twice
	return out, nil
}

// submitExtractBatch checks every chunk may leave the server and the day's call cap has
// room for all of them, then submits them as one batch.
func (s *Service) submitExtractBatch(ctx context.Context, token string, d Decision, scope Scope, b BatchLLM, job *FileExtractJob) error {
	prompts := []BatchPrompt{}
	for i := job.Next; i < len(job.Chunks); i++ {
		if err := s.extractTransfer(ctx, token, d, scope, job.Chunks[i]); err != nil {
			return err
		}
		system, user := extractPrompt(job.Chunks[i].Text, job)
		prompts = append(prompts, BatchPrompt{ID: extractBatchID(i), System: system, User: user})
	}
	calls, err := s.wikiDailyCalls(ctx, scope, time.Now())
	if err != nil {
		return err
	}
	if calls+len(prompts) > wikiDailyCallLimit {
		return ErrModelBudget
	}
	id, err := b.SubmitBatch(ctx, prompts, wikiMaxCallTokens)
	if err != nil {
		return err
	}
	job.Batch = &ExtractBatch{ID: id, SubmittedAt: time.Now().UTC()}
	return errExtractBatchPending
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
	body, status, err := postBody(ctx, providerClient(nil, 120*time.Second), c.Config.Endpoint+"/batches", c.Config.messagesHeader(), encoded, defaultResponseMax)
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
	body, status, err := sendBody(ctx, client, http.MethodGet, c.Config.Endpoint+"/batches/"+url.PathEscape(id), c.Config.messagesHeader(), nil, defaultResponseMax)
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
	body, status, err = sendBody(ctx, client, http.MethodGet, results.String(), c.Config.messagesHeader(), nil, extractBatchResultMax)
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
