package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sync"
)

// Row is one finished unit of work: an ingested session or one system's answer to a question.
// Rows are appended to checkpoint.jsonl as they finish; a resumed run skips every key it finds.
type Row struct {
	Type string `json:"type"` // "ingest" | "qa"
	Key  string `json:"key"`
	Conv string `json:"conv"`

	// ingest
	Session  int            `json:"session,omitempty"`
	Date     string         `json:"date,omitempty"`
	Claims   int            `json:"claims,omitempty"`
	Statuses map[string]int `json:"statuses,omitempty"`
	Approved int            `json:"approved,omitempty"`

	// qa
	QIndex            int      `json:"q_index"`
	Category          int      `json:"category,omitempty"`
	CategoryName      string   `json:"category_name,omitempty"`
	System            string   `json:"system,omitempty"`
	Question          string   `json:"question,omitempty"`
	Gold              string   `json:"gold,omitempty"`
	Answer            string   `json:"answer,omitempty"`
	Score             int      `json:"score"`
	JudgeReason       string   `json:"judge_reason,omitempty"`
	Retrieved         int      `json:"retrieved,omitempty"`
	Memories          []string `json:"memories,omitempty"`
	ContextChars      int      `json:"context_chars,omitempty"`
	ContextTokensEst  int      `json:"context_tokens_est,omitempty"`
	AnswerInputTokens int      `json:"answer_input_tokens,omitempty"`

	// both
	Calls   map[string]int `json:"calls,omitempty"` // CLI runs by role: extract, answer, judge
	CostUSD float64        `json:"cost_usd,omitempty"`
	Seconds float64        `json:"seconds"`
}

type checkpoint struct {
	mu   sync.Mutex
	path string
	rows map[string]Row
	keys []string
}

func qaKey(conv string, q int, system string) string { return conv + "|q" + itoa(q) + "|" + system }
func ingestKey(conv string, session int) string      { return conv + "|s" + itoa(session) }

// openCheckpoint loads path (missing is empty). A torn last line from a kill is ignored.
func openCheckpoint(path string) (*checkpoint, error) {
	c := &checkpoint{path: path, rows: map[string]Row{}}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var r Row
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.Key == "" {
			continue
		}
		if _, seen := c.rows[r.Key]; !seen {
			c.keys = append(c.keys, r.Key)
		}
		c.rows[r.Key] = r
	}
	return c, sc.Err()
}

func (c *checkpoint) done(key string) (Row, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.rows[key]
	return r, ok
}

func (c *checkpoint) add(r Row) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := os.OpenFile(c.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if _, seen := c.rows[r.Key]; !seen {
		c.keys = append(c.keys, r.Key)
	}
	c.rows[r.Key] = r
	return f.Close()
}

func (c *checkpoint) all() []Row {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Row, 0, len(c.keys))
	for _, k := range c.keys {
		out = append(out, c.rows[k])
	}
	return out
}
