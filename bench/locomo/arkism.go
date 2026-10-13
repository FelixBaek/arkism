package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// apiClient talks to a running Arkism server with the owner token. The token is only ever
// placed in the Authorization header; errors carry the status and the server's reply.
type apiClient struct {
	base  string
	token string
	http  *http.Client
}

type httpError struct {
	Code int
	Body string
}

func (e *httpError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Code, e.Body) }

func (a apiClient) do(method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode/100 != 2 {
		if len(data) > 300 {
			data = data[:300]
		}
		return &httpError{Code: resp.StatusCode, Body: string(data)}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

type scope struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	UserID      string `json:"user_id,omitempty"`
}

type hit struct {
	ID          string     `json:"id"`
	Collection  string     `json:"collection"`
	Text        string     `json:"text"`
	Revision    int64      `json:"revision"`
	Kind        string     `json:"kind"`
	CreatedAt   *time.Time `json:"created_at"`
	EffectiveAt *time.Time `json:"effective_at"`
	CollectedAt *time.Time `json:"collected_at"`
	Sources     []struct {
		SourceID string `json:"source_id"`
	} `json:"sources"`
}

// search is the capture client's recall request (searchMemories): exact+fts+vector, falling
// back to the lexical modes when the embedder is unavailable.
func (a apiClient) search(sc scope, query string, limit int) ([]hit, error) {
	if r := []rune(query); len(r) > 1000 {
		query = string(r[:1000])
	}
	body := map[string]any{"scope": sc, "query": query, "modes": []string{"exact", "fts", "vector"},
		"purpose": "answer", "destination": "internal", "limit": limit, "max_chars": 6000}
	var out struct {
		Items []hit `json:"items"`
	}
	err := a.do("POST", "/v1/search", body, &out)
	var he *httpError
	if errors.As(err, &he) && he.Code == 503 {
		body["modes"] = []string{"exact", "fts"}
		err = a.do("POST", "/v1/search", body, &out)
	}
	if err != nil {
		return nil, err
	}
	var mem []hit
	for _, h := range out.Items {
		if h.Collection == "" || h.Collection == "memories" {
			mem = append(mem, h)
		}
	}
	return mem, nil
}

// sourceTime is a source record's collected_at: for a captured claim, the event's occurred_at.
func (a apiClient) sourceTime(sc scope, id string) (*time.Time, error) {
	q := url.Values{"workspace_id": {sc.WorkspaceID}, "project_id": {sc.ProjectID}}
	if sc.UserID != "" {
		q.Set("user_id", sc.UserID)
	}
	var src struct {
		CollectedAt *time.Time `json:"collected_at"`
	}
	if err := a.do("GET", "/v1/sources/"+url.PathEscape(id)+"?"+q.Encode(), nil, &src); err != nil {
		return nil, err
	}
	return src.CollectedAt, nil
}

type captureItem struct {
	Status      string `json:"status"`
	Reason      string `json:"reason"`
	CandidateID string `json:"candidate_id"`
	MemoryID    string `json:"memory_id"`
}

func (a apiClient) capture(payload any) ([]captureItem, error) {
	var out struct {
		Items []captureItem `json:"items"`
	}
	if err := a.do("POST", "/v1/capture", payload, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// approve reads the candidate's revision and approves it through the review API.
// It returns the candidate's resulting status ("approved" when it was already approved).
func (a apiClient) approve(sc scope, id string) (string, error) {
	q := url.Values{"workspace_id": {sc.WorkspaceID}, "project_id": {sc.ProjectID}}
	if sc.UserID != "" {
		q.Set("user_id", sc.UserID)
	}
	// The worker may touch a fresh candidate (dedupe, conflict check) between our read and the review:
	// read the revision again and retry a few times.
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		var cand struct {
			Status   string `json:"status"`
			Revision int64  `json:"revision"`
		}
		if err = a.do("GET", "/v1/memory_candidates/"+url.PathEscape(id)+"?"+q.Encode(), nil, &cand); err != nil {
			return "", err
		}
		if cand.Status != "candidate" && cand.Status != "conflicted" {
			return cand.Status, nil
		}
		var out struct {
			Status string `json:"status"`
		}
		err = a.do("POST", "/v1/memory_candidates/"+url.PathEscape(id)+"/review",
			map[string]any{"scope": sc, "expected_revision": cand.Revision, "action": "approve"}, &out)
		if err == nil || !strings.Contains(err.Error(), "409") {
			return out.Status, err
		}
		time.Sleep(time.Duration(attempt+1) * 300 * time.Millisecond)
	}
	return "", err
}
