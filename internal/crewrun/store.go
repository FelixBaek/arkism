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
	Research     *CrewResearch `json:"research,omitempty"`
	Type         string        `json:"type"`
	Name         string        `json:"name"`
	Handle       string        `json:"handle,omitempty"`
	Model        string        `json:"model,omitempty"`
	Instructions string        `json:"instructions,omitempty"`
	Skills       []string      `json:"skills,omitempty"`
	Trigger      *Trigger      `json:"trigger,omitempty"`
	Enabled      bool          `json:"enabled,omitempty"`
	Limits       *Limits       `json:"limits,omitempty"`
	Graph        *Graph        `json:"graph,omitempty"`
	Notify       *CrewNotify   `json:"notify,omitempty"` // 진행 알림 (progress.go)
	// work agents (type agent, or template saved before agents) and teams, for meetings
	Role            string            `json:"role,omitempty"`
	Effort          string            `json:"effort,omitempty"`
	ModelMode       string            `json:"model_mode,omitempty"`
	FallbackModel   string            `json:"fallback_model,omitempty"`
	MaxOutputTokens int               `json:"max_output_tokens,omitempty"`
	OutputFormat    string            `json:"output_format,omitempty"`
	Backend         string            `json:"backend,omitempty"` // the host runner's agent-runner connection (expert)
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
	// Backend is the agent-runner trace of a step sent to a connection (names and ids only, never text).
	Backend map[string]any `json:"backend,omitempty"`
}

type Waiting struct {
	EffectID      string     `json:"effect_id,omitempty"`
	EffectHash    string     `json:"effect_hash,omitempty"`
	ChildRunID    string     `json:"child_run_id,omitempty"`
	ChildKind     string     `json:"child_kind,omitempty"`
	ChildRevision int64      `json:"child_revision,omitempty"`
	DecisionKind  string     `json:"decision_kind,omitempty"`
	NodeID        string     `json:"node_id"`
	Kind          string     `json:"kind"`
	ResumeAt      *time.Time `json:"resume_at,omitempty"`
	Question      string     `json:"question,omitempty"`
	CallbackHash  string     `json:"callback_hash,omitempty"`
}

type Decision struct {
	Approve   bool      `json:"approve"`
	Note      string    `json:"note,omitempty"`
	DecidedBy string    `json:"decided_by"`
	DecidedAt time.Time `json:"decided_at"`
}

// RunState is the crew_run body of a runs API record.
type RunState struct {
	Effects         map[string]string     `json:"effects,omitempty"`
	SourceType      string                `json:"source_type,omitempty"`
	RootID          string                `json:"root_id,omitempty"`
	ParentID        string                `json:"parent_id,omitempty"`
	ParentNodeID    string                `json:"parent_node_id,omitempty"`
	Depth           int                   `json:"depth,omitempty"`
	Children        map[string]FlowChild  `json:"children,omitempty"`
	Definitions     map[string]CrewItem   `json:"definitions,omitempty"`
	Policy          *FlowPolicy           `json:"policy,omitempty"`
	ResearchMode    string                `json:"research_mode,omitempty"`
	InvocationMode  string                `json:"invocation_mode,omitempty"`
	Definition      *Crew                 `json:"definition,omitempty"`
	Origin          map[string]string     `json:"origin,omitempty"`
	CancelRequested bool                  `json:"cancel_requested,omitempty"`
	CrewID          string                `json:"crew_id"`
	CrewRevision    int64                 `json:"crew_revision"`
	Trigger         string                `json:"trigger"`
	Status          string                `json:"status"`
	Input           string                `json:"input,omitempty"`
	Nodes           map[string]*NodeState `json:"nodes"`
	Waiting         *Waiting              `json:"waiting,omitempty"`
	Decisions       map[string]Decision   `json:"decisions,omitempty"`
	Summary         string                `json:"summary,omitempty"`
	Only            string                `json:"only,omitempty"`  // run this one node, skip the rest
	Loops           map[string]int        `json:"loops,omitempty"` // back edge uses, keyed "from>to:port"
	Deadline        *time.Time            `json:"deadline,omitempty"`
}

type Run struct {
	LeaseToken string   `json:"-"`
	ID         string   `json:"id"`
	Revision   int64    `json:"revision"`
	State      RunState `json:"crew_run"`
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
	body := map[string]any{"scope": c.Scope, "expected_revision": run.Revision, "run": st}
	if run.State.InvocationMode == "flow" {
		if run.LeaseToken == "" {
			return Run{}, errors.New("flow progress requires an execution lease")
		}
		body["lease_token"] = run.LeaseToken
	}
	err := c.do(ctx, "POST", "/v1/crew/runs/"+url.PathEscape(run.ID)+"/progress", body, &r)
	return r, err
}

func (c *Client) LeaseRun(ctx context.Context, id, action, token string) (FlowLease, error) {
	var lease FlowLease
	err := c.do(ctx, "POST", "/v1/crew/runs/"+url.PathEscape(id)+"/lease",
		map[string]any{"scope": c.Scope, "action": action, "lease_token": token}, &lease)
	return lease, err
}

// The actor is supplied by the actual meeting caller, never inferred from a node
// label. This optional binding has no effect on legacy ContextText reads.
type contextReceiptActorKey struct{}

func withContextReceiptActor(ctx context.Context, actorID string) context.Context {
	return context.WithValue(ctx, contextReceiptActorKey{}, actorID)
}

type contextNativeRecord struct {
	fields   map[string]json.RawMessage
	scope    ObservationScope
	id       string
	revision int64
	text     string
	bound    bool
}

// ContextReceipt reads the selected body and then independently checks the
// current team's sharing policy. A readable historical body is not current-use
// authorization; policy diagnostics do not erase a successful source read.
func (c *Client) ContextReceipt(ctx context.Context, requested TeamContext) (ContextReceiptRead, error) {
	result := ContextReceiptRead{Observation: SourceObservation{Scope: ObservationScope(c.Scope), Collection: requested.Collection, ID: requested.ID, ProvenanceStatus: "not_observed", ReadAuthorization: "not_recorded", Sharing: "not_recorded", ObservationReason: "metadata_not_recorded", Authority: "data_only_untrusted"}}
	if requested.Collection == "" || requested.ID == "" {
		return result, observationError("invalid_value", "$.source")
	}
	record, err := c.contextRecord(ctx, requested.Collection, requested.ID)
	if err != nil {
		return result, err
	}
	now := time.Now()
	stamp := now.UnixNano()
	result.Text = record.text
	result.Facts = ContextUseFacts{Source: ContextSourceKey{ObservationScope(c.Scope), requested.Collection, requested.ID}, ReadState: "confirmed", Lifecycle: "unknown", Sharing: "unknown", PolicyState: "unknown", CheckedAtNS: &stamp}
	result.Observation.ReadAtNS = &stamp
	result.Observation.ObservedAtNS = &stamp
	if record.bound {
		digest, hashErr := ContextTextSHA256(record.text)
		if hashErr != nil {
			return result, hashErr
		}
		result.Observation.Revision = &record.revision
		result.Observation.ContentSHA256 = &digest
		result.Observation.ProvenanceStatus = "pinned"
		result.Observation.ReadAuthorization = "confirmed"
		result.Observation.ObservationReason = ""
		result.Facts.Ref = ObservationRef{Collection: requested.Collection, ID: record.id, Revision: record.revision}
		result.Facts.ContentSHA256 = digest
		result.Facts.Lifecycle = contextNativeLifecycle(requested.Collection, record.fields, now)
		result.Facts.GuardSHA256 = contextNativeGuard(record.fields)
	}
	c.contextPolicy(ctx, requested, &result)
	return result, nil
}

func (c *Client) contextRecord(ctx context.Context, collection, id string) (contextNativeRecord, error) {
	var raw json.RawMessage
	if err := c.do(ctx, "GET", "/v1/"+url.PathEscape(collection)+"/"+url.PathEscape(id)+"?"+c.query(nil), nil, &raw); err != nil {
		return contextNativeRecord{}, contextReceiptReadError(err)
	}
	if err := checkObservationJSONUnicode(raw); err != nil {
		return contextNativeRecord{}, observationError("invalid_json", "$")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkObservationJSON(decoder, 0); err != nil {
		return contextNativeRecord{}, observationError("invalid_json", "$")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return contextNativeRecord{}, observationError("invalid_json", "$")
	}
	return contextBoundRecord(fields, ObservationScope(c.Scope), collection, id)
}
func contextBoundRecord(fields map[string]json.RawMessage, expected ObservationScope, collection, id string) (contextNativeRecord, error) {
	text, known, err := contextSelectedBody(fields)
	if err != nil {
		return contextNativeRecord{}, err
	}
	record := contextNativeRecord{fields: fields, text: text}
	record.id, _ = contextNativeString(fields, "id")
	if record.id != "" && record.id != id {
		return record, observationError("source_binding", "$.id")
	}
	if value, ok := fields["collection"]; ok {
		var got string
		if json.Unmarshal(value, &got) != nil || got != collection {
			return record, observationError("source_binding", "$.collection")
		}
	}
	scope, scopeOK := contextNativeScope(fields["scope"])
	record.scope = scope
	if scopeOK && scope != expected {
		return record, observationError("scope_mismatch", "$.scope")
	}
	revision, revisionOK := contextNativeInt(fields["revision"])
	record.revision = revision
	record.bound = known && record.id == id && scopeOK && revisionOK && revision > 0
	return record, nil
}
func contextSelectedBody(fields map[string]json.RawMessage) (string, bool, error) {
	text, textOK := contextNativeString(fields, "text")
	summary, summaryOK := contextNativeString(fields, "summary")
	if !textOK && fields["text"] != nil || !summaryOK && fields["summary"] != nil {
		return "", false, observationError("invalid_type", "$.text")
	}
	if text != "" {
		return text, true, nil
	}
	return summary, textOK || summaryOK, nil
}
func contextNativeString(fields map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := fields[key]
	if !ok || len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var result string
	err := json.Unmarshal(raw, &result)
	return result, err == nil
}
func contextNativeInt(raw []byte) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	for i, ch := range raw {
		if ch < '0' || ch > '9' {
			if i != 0 || ch != '-' {
				return 0, false
			}
		}
	}
	result, err := strconv.ParseInt(string(raw), 10, 64)
	return result, err == nil
}
func contextNativeScope(raw []byte) (ObservationScope, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return ObservationScope{}, false
	}
	workspace, w := contextNativeString(fields, "workspace_id")
	project, p := contextNativeString(fields, "project_id")
	user, u := contextNativeString(fields, "user_id")
	agent, a := contextNativeString(fields, "agent_id")
	if fields["user_id"] == nil {
		u = true
	}
	if fields["agent_id"] == nil {
		a = true
	}
	return ObservationScope{WorkspaceID: workspace, ProjectID: project, UserID: user, AgentID: agent}, w && p && u && a && workspace != "" && project != ""
}
func contextNativeFlag(fields map[string]json.RawMessage, key string) (bool, bool) {
	raw, ok := fields[key]
	if !ok {
		return false, true
	}
	if bytes.Equal(raw, []byte("true")) {
		return true, true
	}
	return false, bytes.Equal(raw, []byte("false"))
}
func contextNativeTime(fields map[string]json.RawMessage, key string) (*time.Time, bool) {
	raw, ok := fields[key]
	if !ok || bytes.Equal(raw, []byte("null")) {
		return nil, true
	}
	value, valid := contextNativeString(fields, key)
	if !valid {
		return nil, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return &parsed, err == nil
}
func contextNativeLifecycle(collection string, fields map[string]json.RawMessage, now time.Time) string {
	if !contextNativeHeader(fields) {
		return "unknown"
	}
	erased, eOK := contextNativeFlag(fields, "content_erased")
	quarantined, qOK := contextNativeFlag(fields, "privacy_quarantined")
	if !eOK || !qOK {
		return "unknown"
	}
	if erased || quarantined {
		return "deleted"
	}
	status, ok := contextNativeString(fields, "status")
	if !ok {
		return "unknown"
	}
	if status != "active" {
		return "inactive"
	}
	effective, eOK := contextNativeTime(fields, "effective_at")
	expiry, xOK := contextNativeTime(fields, "expires_at")
	if !eOK || !xOK {
		return "unknown"
	}
	if effective != nil && effective.After(now) || expiry != nil && !expiry.After(now) {
		return "inactive"
	}
	if !contextNativeCollection(collection) {
		return "unknown"
	}
	if collection == "memories" && !contextNativeMemoryApproved(fields) {
		return "unknown"
	}
	return "active"
}
func contextNativeCollection(collection string) bool {
	switch collection {
	case "sources", "memories", "skills", "skill_versions", "prompts", "design_systems", "crew":
		return true
	}
	return false
}
func contextNativeMemoryApproved(fields map[string]json.RawMessage) bool {
	if value, ok := fields["stale_mark"]; ok && !bytes.Equal(value, []byte("null")) {
		return false
	}
	if value, ok := fields["superseded_by"]; ok && !bytes.Equal(value, []byte("null")) {
		return false
	}
	approved, ok := contextNativeString(fields, "approved_by")
	return ok && strings.TrimSpace(approved) != ""

}

// Guard commits the native non-display facts, independently of the exact
// selected text digest. Record CAS and display timestamps are not premises.
func contextNativeGuard(fields map[string]json.RawMessage) string {
	copyFields := map[string]json.RawMessage{}
	for k, v := range fields {
		switch k {
		case "revision", "created_at", "updated_at", "title", "name":
			continue
		}
		copyFields[k] = v
	}
	// Native omitempty false flags have the same semantics as explicit false.
	for _, key := range []string{"content_erased", "privacy_quarantined"} {
		if copyFields[key] == nil {
			copyFields[key] = json.RawMessage("false")
		}
	}
	if scope, ok := contextNativeScope(fields["scope"]); ok {
		copyFields["scope"], _ = json.Marshal(scope)
	}
	raw, err := json.Marshal(copyFields)
	if err != nil {
		return ""
	}
	var canonical any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&canonical) != nil {
		return ""
	}
	normalized, err := json.Marshal(canonical)
	if err != nil {
		return ""
	}
	return observationBytesSHA(normalized)
}
func (c *Client) contextPolicy(ctx context.Context, requested TeamContext, result *ContextReceiptRead) {
	actual, actor, ok := c.contextPolicyActor(ctx)
	if !ok {
		return
	}
	policy, err := c.contextRecord(ctx, "crew", actual.Run.Definition.Ref.ID)
	if err != nil {
		if apiCode(err) == "permission_denied" || apiCode(err) == "forbidden" || apiCode(err) == "access_denied" {
			result.Facts.PolicyState = "denied"
		}
		return
	}
	if !policy.bound || policy.revision < actual.Run.Definition.Ref.Revision || contextNativeLifecycle("crew", policy.fields, time.Now()) != "active" {
		return
	}
	var team struct {
		Type    string        `json:"type"`
		Members []TeamMember  `json:"members"`
		Context []TeamContext `json:"context"`
	}
	if json.Unmarshal(policy.fields["crew"], &team) != nil || team.Type != "team" {
		return
	}
	member := false
	for _, m := range team.Members {
		if m.AgentID == actor {
			member = true
		}
	}
	if !member {
		result.Facts.PolicyState = "denied"
		return
	}
	sharing := ""
	for _, ref := range team.Context {
		if ref.Collection == requested.Collection && ref.ID == requested.ID {
			if sharing != "" {
				return
			}
			sharing = ref.Share
		}
	}
	if sharing != "team" && sharing != "shared" {
		return
	}
	now := time.Now().UnixNano()
	ref := ObservationRef{Collection: "crew", ID: policy.id, Revision: policy.revision}
	result.Facts.PolicyRef = &ref
	result.Facts.PolicyScope = policy.scope
	result.Facts.PolicyReadAtNS = &now
	result.Facts.PolicyState = "confirmed"
	result.Facts.GuardSHA256 = contextCombinedGuard(result.Facts.GuardSHA256, policy.fields)
	result.Facts.Sharing = sharing
	result.Observation.Sharing = sharing
}

func contextCombinedGuard(source string, policy map[string]json.RawMessage) string {
	if source == "" {
		return ""
	}
	var team map[string]json.RawMessage
	if json.Unmarshal(policy["crew"], &team) != nil {
		return ""
	}
	for _, key := range []string{"name", "avatar", "cover"} {
		delete(team, key)
	}
	var refs []map[string]json.RawMessage
	if json.Unmarshal(team["context"], &refs) == nil {
		for _, ref := range refs {
			delete(ref, "title")
		}
		team["context"], _ = json.Marshal(refs)
	}
	cleaned := map[string]json.RawMessage{}
	for k, v := range policy {
		cleaned[k] = v
	}
	cleaned["crew"], _ = json.Marshal(team)
	guard := contextNativeGuard(cleaned)
	if guard == "" {
		return ""
	}
	return observationBytesSHA([]byte(source + "\n" + guard))
}

func (c *Client) contextPolicyActor(ctx context.Context) (AttemptContext, string, bool) {
	actual, ok := AttemptContextFrom(ctx)
	actor, _ := ctx.Value(contextReceiptActorKey{}).(string)
	valid := ok && validateObservation(actual.Run.RunRef) == nil && validateObservation(actual.Run.Definition.Ref) == nil && actual.Run.RunRef.Collection == "crew_meetings" && actual.Run.Definition.SourceType == "team" && actual.Run.Definition.Ref.Collection == "crew" && actual.Run.Scope == ObservationScope(c.Scope) && actor != ""
	return actual, actor, valid
}

func contextNativeHeader(fields map[string]json.RawMessage) bool {
	kind, ok := contextNativeString(fields, "kind")
	if !ok || kind == "" {
		return false
	}
	if value, exists := fields["content_hash"]; exists {
		var digest string
		return json.Unmarshal(value, &digest) == nil && validContextSHA(digest)
	}
	return true
}

func contextReceiptReadError(err error) error {
	var api *APIError
	if errors.As(err, &api) {
		code := "source_read_failed"
		switch api.Code {
		case "access_denied", "permission_denied", "forbidden", "not_found", "invalid_request", "revision_conflict", "invalid_token", "secret_detected":
			code = api.Code
		}
		return &APIError{Status: api.Status, Code: code}
	}
	return observationError("source_read_failed", "$")
}
