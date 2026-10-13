package main

import (
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/FelixBaek/arkism/internal/host/capture"
)

const pipelineVersion = "locomo-bench-v1"

func httpClient() *http.Client {
	// Like the capture client: no proxy, the server is local.
	return &http.Client{Timeout: 90 * time.Second, Transport: &http.Transport{Proxy: nil}}
}

// ingestAll feeds every session through extraction and POST /v1/capture. Sessions of one
// conversation run in order (each extraction sees the memories the earlier ones left, as the
// drain does); conversations run in parallel.
func (b *bench) ingestAll(convs []Conversation) error {
	sem := make(chan struct{}, b.cfg.Concurrency)
	var wg sync.WaitGroup
	errs := make([]error, len(convs))
	for i, conv := range convs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			for _, s := range conv.Sessions {
				if err := b.ingestSession(conv, s); err != nil {
					errs[i] = fmt.Errorf("%s session %d: %w", conv.SampleID, s.N, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (b *bench) ingestSession(conv Conversation, s Session) error {
	key := ingestKey(conv.SampleID, s.N)
	if _, ok := b.ck.done(key); ok {
		return nil
	}
	t0 := time.Now()
	sc := b.scope(conv)
	text := SessionText(s)
	if b.cfg.CueFilter {
		var kept []string
		for _, line := range strings.Split(text, "\n") {
			if capture.CueMatch(line) {
				kept = append(kept, line)
			}
		}
		text = strings.Join(kept, "\n")
	}
	row := Row{Type: "ingest", Key: key, Conv: conv.SampleID, Session: s.N, Date: s.Date.Format("2006-01-02"),
		Calls: map[string]int{}, Statuses: map[string]int{}}
	if strings.TrimSpace(text) != "" {
		// EXISTING MEMORIES, as the drain finds them.
		var related []hit
		if err := b.retry(func() (err error) { related, err = b.api.search(sc, text, 5); return }); err != nil {
			return fmt.Errorf("search: %w", err)
		}
		rel := make([]map[string]any, len(related))
		for i, h := range related {
			rel[i] = map[string]any{"id": h.ID, "text": h.Text, "revision": h.Revision}
		}
		var ex capture.BenchExtraction
		if err := b.retry(func() (err error) {
			row.Calls["extract"]++
			ex, err = capture.ExtractClaims(text, s.Date.Format("2006-01-02"), rel, b.cfg.Guidance, pipelineVersion)
			return
		}); err != nil {
			return fmt.Errorf("extract: %w", err)
		}
		row.Claims = len(ex.Claims)
		if len(ex.Claims) > 0 {
			payload := map[string]any{"scope": sc, "generator": ex.Generator, "claims": ex.Claims, "event": map[string]any{
				"id": "locomo-" + b.cfg.RunID + "-" + conv.SampleID + "-s" + itoa(s.N), "type": ex.EventType,
				"origin_product": "locomo-bench", "session_id": conv.SampleID + "-s" + itoa(s.N),
				"occurred_at": s.Date.Format(time.RFC3339)}}
			var items []captureItem
			if err := b.retry(func() (err error) { items, err = b.api.capture(payload); return }); err != nil {
				return fmt.Errorf("capture: %w", err)
			}
			for _, it := range items {
				row.Statuses[it.Status+":"+it.Reason]++
				if b.cfg.Variant != "approve-all" || it.CandidateID == "" || it.Status == "accepted" {
					continue
				}
				var st string
				if err := b.retry(func() (err error) { st, err = b.api.approve(sc, it.CandidateID); return }); err != nil {
					// One candidate that cannot be approved must not stop a run of hours: count it and go on.
					row.Statuses["approve_failed"]++
					continue
				}
				if st == "approved" {
					row.Approved++
				}
			}
		}
	}
	row.Seconds = time.Since(t0).Seconds()
	fmt.Printf("ingest %s s%d: %d claim(s) %v approved=%d (%.0fs)\n", conv.SampleID, s.N, row.Claims, row.Statuses, row.Approved, row.Seconds)
	return b.ck.add(row)
}

type qaJob struct {
	conv   Conversation
	q      QA
	system string
}

// answerAll asks every selected question of every system; failures are counted, not fatal,
// so a resume can retry them.
func (b *bench) answerAll(convs []Conversation) int {
	systems := []string{"arkism"}
	if b.cfg.NoMemory {
		systems = append(systems, "no-memory")
	}
	if b.cfg.FullContext {
		systems = append(systems, "full-context")
	}
	jobs := make(chan qaJob)
	var failed int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < b.cfg.Concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if err := b.answer(j); err != nil {
					mu.Lock()
					failed++
					mu.Unlock()
					fmt.Printf("qa %s q%d %s failed: %v\n", j.conv.SampleID, j.q.Index, j.system, err)
				}
			}
		}()
	}
	for _, conv := range convs {
		for _, q := range SelectQuestions(conv.QA, b.cfg.Sessions, b.cfg.QuestionsPerConv) {
			for _, s := range systems {
				if _, ok := b.ck.done(qaKey(conv.SampleID, q.Index, s)); !ok {
					jobs <- qaJob{conv, q, s}
				}
			}
		}
	}
	close(jobs)
	wg.Wait()
	return failed
}

func estTokens(s string) int { return int(math.Ceil(float64(len([]rune(s))) / 4)) }

func (b *bench) answer(j qaJob) error {
	t0 := time.Now()
	row := Row{Type: "qa", Key: qaKey(j.conv.SampleID, j.q.Index, j.system), Conv: j.conv.SampleID, QIndex: j.q.Index,
		Category: j.q.Category, CategoryName: categoryNames[j.q.Category], System: j.system, Question: j.q.Question,
		Gold: j.q.Answer, Calls: map[string]int{}}
	system, context := answerSystem, ""
	switch j.system {
	case "arkism":
		var hits []hit
		if err := b.retry(func() (err error) { hits, err = b.api.search(b.scope(j.conv), j.q.Question, b.cfg.SearchLimit); return }); err != nil {
			return fmt.Errorf("search: %w", err)
		}
		var lines []string
		for _, h := range hits {
			line := "- " + strings.TrimSpace(h.Text)
			if d := b.hitDate(j.conv, h); d != "" {
				line = "- [" + d + "] " + strings.TrimSpace(h.Text)
			}
			lines = append(lines, line)
			row.Memories = append(row.Memories, line[2:])
		}
		row.Retrieved = len(hits)
		context = strings.Join(lines, "\n")
	case "full-context":
		system = fullContextSystem
		var parts []string
		for _, s := range j.conv.Sessions {
			parts = append(parts, fmt.Sprintf("=== Session %d (%s) ===\n%s", s.N, s.DateRaw, SessionText(s)))
		}
		context = strings.Join(parts, "\n\n")
	}
	row.ContextChars = len([]rune(context))
	row.ContextTokensEst = estTokens(context)
	var user string
	if j.system == "full-context" {
		user = "CONVERSATION:\n" + context + "\n\nQUESTION: " + j.q.Question
	} else {
		if context == "" {
			context = "(none)"
		}
		user = "MEMORIES:\n" + context + "\n\nQUESTION: " + j.q.Question
	}
	ans, err := b.cli.call(b.cfg.AnswerModel, system, user, "")
	row.Calls["answer"] += ans.Attempts
	if err != nil {
		return fmt.Errorf("answer: %w", err)
	}
	row.Answer, row.AnswerInputTokens, row.CostUSD = ans.Text, ans.InputTokens, ans.CostUSD
	var score int
	jr, err := b.cli.call(b.cfg.JudgeModel, judgeSystem, judgeUser(j.q.Question, j.q.Answer, ans.Text), judgeSchema)
	row.Calls["judge"] += jr.Attempts
	if err == nil {
		score, err = ParseJudge(jr.Structured, jr.Text)
	}
	if err != nil {
		return fmt.Errorf("judge: %w", err)
	}
	row.Score, row.CostUSD = score, row.CostUSD+jr.CostUSD
	if r, ok := jr.Structured["reasoning"].(string); ok {
		row.JudgeReason = r
	}
	row.Seconds = time.Since(t0).Seconds()
	fmt.Printf("qa %s q%d [%s] %-12s %d  %q -> %q\n", j.conv.SampleID, j.q.Index, row.CategoryName, j.system, score, j.q.Answer, ans.Text)
	return b.ck.add(row)
}

// hitDate is when the memory's source event happened (its source's collected_at), if known.
func (b *bench) hitDate(conv Conversation, h hit) string {
	if h.CollectedAt != nil {
		return h.CollectedAt.UTC().Format("2006-01-02")
	}
	if len(h.Sources) == 0 {
		return ""
	}
	id := h.Sources[0].SourceID
	b.srcMu.Lock()
	d, ok := b.sources[id]
	b.srcMu.Unlock()
	if ok {
		return d
	}
	if t, err := b.api.sourceTime(b.scope(conv), id); err == nil && t != nil {
		d = t.UTC().Format("2006-01-02")
	}
	b.srcMu.Lock()
	b.sources[id] = d
	b.srcMu.Unlock()
	return d
}
