// Package crewrun runs workflow graphs on the operator's host.
// It reads crew definitions from Arkism, keeps run progress in the
// Arkism runs API and never stores keys or tokens there: provider
// keys, hook secrets and notify destinations stay in 0600 files under
// ~/.config/arkism. Standard library only; no import of internal/memory.
package crewrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Graph mirrors the crew graph JSON.
type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

type Node struct {
	ID     string            `json:"id"`
	Type   string            `json:"type"`
	Title  string            `json:"title,omitempty"`
	Config map[string]string `json:"config,omitempty"`
}

// Edge with Max > 0 goes back: it may be followed Max times per run.
type Edge struct {
	From string `json:"from"`
	Port string `json:"port,omitempty"`
	To   string `json:"to"`
	Max  int    `json:"max,omitempty"`
}

type Limits struct {
	MaxSteps   int `json:"max_steps,omitempty"`
	MaxMinutes int `json:"max_minutes,omitempty"`
}

type Trigger struct {
	Kind string `json:"kind"`
	Cron string `json:"cron,omitempty"`
}

// Crew is the part of a crew item the engine needs: a workflow, or a persona
// whose instructions an AI node may take.
type Crew struct {
	Type         string      `json:"type"`
	Name         string      `json:"name"`
	Handle       string      `json:"handle,omitempty"`
	Model        string      `json:"model,omitempty"`
	Instructions string      `json:"instructions,omitempty"`
	Trigger      *Trigger    `json:"trigger,omitempty"`
	Enabled      bool        `json:"enabled,omitempty"`
	Limits       *Limits     `json:"limits,omitempty"`
	Graph        *Graph      `json:"graph,omitempty"`
	Notify       *CrewNotify `json:"notify,omitempty"` // 진행 알림 (progress.go)
	// work agents (type agent, or template saved before agents) and teams, for meetings
	Role            string            `json:"role,omitempty"`
	Effort          string            `json:"effort,omitempty"`
	ModelMode       string            `json:"model_mode,omitempty"`
	FallbackModel   string            `json:"fallback_model,omitempty"`
	MaxOutputTokens int               `json:"max_output_tokens,omitempty"`
	OutputFormat    string            `json:"output_format,omitempty"`
	Budget          *AgentBudget      `json:"budget,omitempty"`
	Members         []TeamMember      `json:"members,omitempty"`
	Mode            string            `json:"mode,omitempty"`
	Ending          *TeamEnding       `json:"ending,omitempty"`
	Context         []TeamContext     `json:"context,omitempty"`
	Templates       []MeetingTemplate `json:"templates,omitempty"`
}

type CrewItem struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Crew     Crew   `json:"crew"`
}

type NodeState struct {
	Status     string     `json:"status"`
	Port       string     `json:"port,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Output     string     `json:"output,omitempty"`
	Error      string     `json:"error,omitempty"`
	Runs       int        `json:"runs,omitempty"`   // starts of this node in the run
	Tokens     int64      `json:"tokens,omitempty"` // AI tokens (input + output) of its last finish
}

type Waiting struct {
	NodeID       string     `json:"node_id"`
	Kind         string     `json:"kind"`
	ResumeAt     *time.Time `json:"resume_at,omitempty"`
	Question     string     `json:"question,omitempty"`
	CallbackHash string     `json:"callback_hash,omitempty"`
}

type Decision struct {
	Approve   bool      `json:"approve"`
	Note      string    `json:"note,omitempty"`
	DecidedBy string    `json:"decided_by"`
	DecidedAt time.Time `json:"decided_at"`
}

// RunState is the crew_run body of a runs API record.
type RunState struct {
	CrewID       string                `json:"crew_id"`
	CrewRevision int64                 `json:"crew_revision"`
	Trigger      string                `json:"trigger"`
	Status       string                `json:"status"`
	Input        string                `json:"input,omitempty"`
	Nodes        map[string]*NodeState `json:"nodes"`
	Waiting      *Waiting              `json:"waiting,omitempty"`
	Decisions    map[string]Decision   `json:"decisions,omitempty"`
	Summary      string                `json:"summary,omitempty"`
	Only         string                `json:"only,omitempty"`  // run this one node, skip the rest
	Loops        map[string]int        `json:"loops,omitempty"` // back edge uses, keyed "from>to:port"
	Deadline     *time.Time            `json:"deadline,omitempty"`
}

type Run struct {
	ID       string   `json:"id"`
	Revision int64    `json:"revision"`
	State    RunState `json:"crew_run"`
	// CreatedByType tells a run a person started from the desktop (시험 실행·지금 실행) apart from the engine's own.
	CreatedByType string `json:"created_by_type,omitempty"`
}

// Store is the Arkism side the engine needs. Client implements it over HTTP.
type Store interface {
	ListCrew(ctx context.Context) ([]CrewItem, error)
	ListRuns(ctx context.Context, status string, limit int) ([]Run, error)
	GetRun(ctx context.Context, id string) (Run, error)
	CreateRun(ctx context.Context, st RunState) (Run, error)
	UpdateRun(ctx context.Context, r Run) (Run, error)
}

// APIError is a non-200 answer; Code is Arkism's error code.
type APIError struct {
	Status int
	Code   string
}

func (e *APIError) Error() string { return fmt.Sprintf("arkism %d %s", e.Status, e.Code) }

func apiCode(err error) string {
	var a *APIError
	if errors.As(err, &a) {
		return a.Code
	}
	return ""
}

type Scope struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	UserID      string `json:"user_id,omitempty"`
	AgentID     string `json:"agent_id,omitempty"`
}

// Client talks to Arkism with a bearer token read from a file on each call,
// so a rotated token needs no restart. The token is never logged.
type Client struct {
	BaseURL   string
	TokenFile string
	Scope     Scope
	HTTP      *http.Client
}

func (c *Client) query(extra url.Values) string {
	q := url.Values{"workspace_id": {c.Scope.WorkspaceID}, "project_id": {c.Scope.ProjectID}}
	if c.Scope.UserID != "" {
		q.Set("user_id", c.Scope.UserID)
	}
	if c.Scope.AgentID != "" {
		q.Set("agent_id", c.Scope.AgentID)
	}
	for k, v := range extra {
		q[k] = v
	}
	return q.Encode()
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	raw, err := readSecretFile(c.TokenFile)
	if err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := h.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 32 MiB fits a brief page of 1000 runs of 100 nodes (about 10 MB).
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		var e struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(data, &e)
		return &APIError{Status: resp.StatusCode, Code: e.Code}
	}
	return json.Unmarshal(data, out)
}

func (c *Client) ListCrew(ctx context.Context) ([]CrewItem, error) {
	var page struct {
		Items []CrewItem `json:"items"`
	}
	err := c.do(ctx, "GET", "/v1/crew?"+c.query(nil), nil, &page)
	return page.Items, err
}

func (c *Client) ListRuns(ctx context.Context, status string, limit int) ([]Run, error) {
	var page struct {
		Items []Run `json:"items"`
	}
	q := url.Values{"limit": {strconv.Itoa(limit)}, "brief": {"1"}}
	if status != "" {
		q.Set("status", status)
	}
	err := c.do(ctx, "GET", "/v1/crew/runs?"+c.query(q), nil, &page)
	return page.Items, err
}

func (c *Client) GetRun(ctx context.Context, id string) (Run, error) {
	var r Run
	err := c.do(ctx, "GET", "/v1/crew/runs/"+url.PathEscape(id)+"?"+c.query(nil), nil, &r)
	return r, err
}

func (c *Client) CreateRun(ctx context.Context, st RunState) (Run, error) {
	var r Run
	err := c.do(ctx, "POST", "/v1/crew/runs", map[string]any{"scope": c.Scope, "run": st}, &r)
	return r, err
}

func (c *Client) UpdateRun(ctx context.Context, run Run) (Run, error) {
	var r Run
	st := run.State
	st.Decisions = nil // only the decision API writes them
	err := c.do(ctx, "POST", "/v1/crew/runs/"+url.PathEscape(run.ID)+"/progress",
		map[string]any{"scope": c.Scope, "expected_revision": run.Revision, "run": st}, &r)
	return r, err
}
