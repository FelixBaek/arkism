package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"time"
)

// Crew runs: one record per graph run of a workflow. The host
// engine (cmd/arkism-crew) creates and advances runs with a service token;
// a person records approval decisions. Outputs are size-capped and refused
// when they look like credentials, so a run never stores keys or tokens.
const colCrewRuns = "crew_runs"

// "agent": started by the agent whose way of working the workflow is; the only live trigger a bot-owned workflow takes.
// test: a person's 시험 실행; the engine fakes every step that reaches outside.
var crewRunTriggers = []string{"manual", "schedule", "webhook", "call", "discord", "agent", "test"}
var crewRunStatuses = []string{"running", "waiting", "succeeded", "failed", "stopped"}
var crewNodeStatuses = []string{"pending", "running", "waiting", "done", "failed", "skipped"}

// budget: a step would go over an agent's or Discord's budget; a person decides
// (POST …/decision) whether the run goes on, so a run never overspends silently.
var crewWaitKinds = []string{"approval", "wait", "callback", "budget", "child", "capacity"}
var crewHashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

const crewRunMaxBytes = 2 << 20

type CrewRunNode struct {
	Status     string     `json:"status" bson:"status"`
	Port       string     `json:"port,omitempty" bson:"port,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty" bson:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty" bson:"finished_at,omitempty"`
	Output     string     `json:"output,omitempty" bson:"output,omitempty"`
	Error      string     `json:"error,omitempty" bson:"error,omitempty"`
	// Runs counts the node's starts; a back edge makes it 2, 3, …
	Runs int `json:"runs,omitempty" bson:"runs,omitempty"`
	// Tokens is the AI token use (input + output) of the node's last finish.
	Tokens int64 `json:"tokens,omitempty" bson:"tokens,omitempty"`
	// Backend: the step ran on an agent-runner connection (ST-840); names and ids only.
	Backend *CrewAssistTrace `json:"backend,omitempty" bson:"backend,omitempty"`
}

type CrewRunWaiting struct {
	EffectID      string     `json:"effect_id,omitempty" bson:"effect_id,omitempty"`
	EffectHash    string     `json:"effect_hash,omitempty" bson:"effect_hash,omitempty"`
	ChildRunID    string     `json:"child_run_id,omitempty" bson:"child_run_id,omitempty"`
	ChildKind     string     `json:"child_kind,omitempty" bson:"child_kind,omitempty"`
	DecisionKind  string     `json:"decision_kind,omitempty" bson:"decision_kind,omitempty"`
	ChildRevision int64      `json:"child_revision,omitempty" bson:"child_revision,omitempty"`
	NodeID        string     `json:"node_id" bson:"node_id"`
	Kind          string     `json:"kind" bson:"kind"`
	ResumeAt      *time.Time `json:"resume_at,omitempty" bson:"resume_at,omitempty"`
	Question      string     `json:"question,omitempty" bson:"question,omitempty"`
	// CallbackHash is the SHA-256 (hex) of a one-time callback token; the token
	// itself stays with the engine.
	CallbackHash string `json:"callback_hash,omitempty" bson:"callback_hash,omitempty"`
}

type CrewRunDecision struct {
	Approve   bool      `json:"approve" bson:"approve"`
	Note      string    `json:"note,omitempty" bson:"note,omitempty"`
	DecidedBy string    `json:"decided_by" bson:"decided_by"`
	DecidedAt time.Time `json:"decided_at" bson:"decided_at"`
}

type CrewRun struct {
	ResultRevision  int64                      `json:"result_revision,omitempty" bson:"result_revision,omitempty"`
	Effects         map[string]string          `json:"effects,omitempty" bson:"effects,omitempty"`
	SourceType      string                     `json:"source_type,omitempty" bson:"source_type,omitempty"`
	RootID          string                     `json:"root_id,omitempty" bson:"root_id,omitempty"`
	ParentID        string                     `json:"parent_id,omitempty" bson:"parent_id,omitempty"`
	ParentNodeID    string                     `json:"parent_node_id,omitempty" bson:"parent_node_id,omitempty"`
	Depth           int                        `json:"depth,omitempty" bson:"depth,omitempty"`
	Children        map[string]FlowChildRef    `json:"children,omitempty" bson:"children,omitempty"`
	Definitions     map[string]FlowDefinition  `json:"definitions,omitempty" bson:"definitions,omitempty"`
	Policy          *FlowPolicy                `json:"policy,omitempty" bson:"policy,omitempty"`
	ResearchMode    string                     `json:"research_mode,omitempty" bson:"research_mode,omitempty"`
	CancelRequested bool                       `json:"cancel_requested,omitempty" bson:"cancel_requested,omitempty"`
	InvocationMode  string                     `json:"invocation_mode,omitempty" bson:"invocation_mode,omitempty"`
	Definition      *Crew                      `json:"definition,omitempty" bson:"definition,omitempty"`
	CrewID          string                     `json:"crew_id" bson:"crew_id"`
	CrewRevision    int64                      `json:"crew_revision" bson:"crew_revision"`
	Trigger         string                     `json:"trigger" bson:"trigger"`
	Status          string                     `json:"status" bson:"status"`
	Input           string                     `json:"input,omitempty" bson:"input,omitempty"`
	Nodes           map[string]CrewRunNode     `json:"nodes" bson:"nodes"`
	Waiting         *CrewRunWaiting            `json:"waiting,omitempty" bson:"waiting,omitempty"`
	Decisions       map[string]CrewRunDecision `json:"decisions,omitempty" bson:"decisions,omitempty"`
	Summary         string                     `json:"summary,omitempty" bson:"summary,omitempty"`
	// Only runs one node (이 노드만 실행): every other node is skipped and the node gets Input.
	Only string `json:"only,omitempty" bson:"only,omitempty"`
	// Loops counts how often each back edge was followed, keyed "from>to:port".
	Loops map[string]int `json:"loops,omitempty" bson:"loops,omitempty"`
	// Deadline is when the run fails with time_limit (the workflow's limits.max_minutes).
	Deadline *time.Time `json:"deadline,omitempty" bson:"deadline,omitempty"`
	// Origin is the Discord thread a discord run came from.
	Origin *CrewRunOrigin `json:"origin,omitempty" bson:"origin,omitempty"`
}

type CrewRunOrigin struct {
	Connector string `json:"connector,omitempty" bson:"connector,omitempty"`
	GuildID   string `json:"guild_id,omitempty" bson:"guild_id,omitempty"`
	ChannelID string `json:"channel_id,omitempty" bson:"channel_id,omitempty"`
	UserID    string `json:"user_id,omitempty" bson:"user_id,omitempty"`
	EventID   string `json:"event_id,omitempty" bson:"event_id,omitempty"`
	BotID     string `json:"bot_id,omitempty" bson:"bot_id,omitempty"`
	ThreadID  string `json:"thread_id,omitempty" bson:"thread_id,omitempty"`
}

func validCrewRunOrigin(o *CrewRunOrigin, trigger string) bool {
	if o == nil {
		return true
	}
	if trigger != "discord" || (o.Connector != "" && o.Connector != "discord") || (o.BotID != "" && !validRunLabel(o.BotID, 128)) {
		return false
	}
	for _, id := range []string{o.ThreadID, o.GuildID, o.ChannelID, o.UserID, o.EventID} {
		if id != "" && !discordIDRe.MatchString(id) {
			return false
		}
	}
	return true
}

type CrewRunInput struct {
	RequestID string  `json:"request_id,omitempty"`
	Scope     Scope   `json:"scope"`
	Run       CrewRun `json:"run"`
}

type CrewRunUpdate struct {
	LeaseToken       string  `json:"lease_token,omitempty"`
	Scope            Scope   `json:"scope"`
	ExpectedRevision int64   `json:"expected_revision"`
	Run              CrewRun `json:"run"`
}

type CrewRunDecisionInput struct {
	Scope            Scope  `json:"scope"`
	ExpectedRevision int64  `json:"expected_revision"`
	NodeID           string `json:"node_id"`
	Approve          bool   `json:"approve"`
	Note             string `json:"note,omitempty"`
}

type CrewRunPage struct {
	Items []Record `json:"items"`
}

func terminalCrewRun(status string) bool {
	return status == "succeeded" || status == "failed" || status == "stopped"
}

func validCrewRunNode(n CrewRunNode) bool {
	return slices.Contains(crewNodeStatuses, n.Status) && len(n.Port) <= 20 && runes(n.Output) <= 20000 && runes(n.Error) <= 500 &&
		n.Runs >= 0 && n.Runs <= 10000 && n.Tokens >= 0 && validCrewAssistTrace(n.Backend)
}

func validCrewRunLoops(loops map[string]int) bool {
	if len(loops) > 200 {
		return false
	}
	for k, v := range loops {
		if k == "" || len(k) > 200 || v < 0 || v > 10000 {
			return false
		}
	}
	return true
}

func validCrewRunWaiting(w *CrewRunWaiting, nodes map[string]CrewRunNode) bool {
	if w == nil {
		return true
	}
	n, ok := nodes[w.NodeID]
	return ok && n.Status == "waiting" && slices.Contains(crewWaitKinds, w.Kind) && runes(w.Question) <= 2000 &&
		(w.CallbackHash == "" || w.Kind == "callback" && crewHashRe.MatchString(w.CallbackHash))
}

// validCrewRun checks shape and size; decisions are not checked here because
// only the decision endpoint writes them.
func validCrewRun(c CrewRun) bool {
	if !slices.Contains([]string{"", "auto", "deep"}, c.ResearchMode) || !slices.Contains([]string{"", "flow"}, c.InvocationMode) {
		return false
	}
	if !validRunLabel(c.CrewID, 256) || c.CrewRevision < 1 || !slices.Contains(crewRunTriggers, c.Trigger) ||
		!slices.Contains(crewRunStatuses, c.Status) || runes(c.Input) > 20000 || runes(c.Summary) > 4000 || len(c.Nodes) > 100 ||
		!validCrewRunLoops(c.Loops) || !validCrewRunOrigin(c.Origin, c.Trigger) {
		return false
	}
	for id, n := range c.Nodes {
		if !validRunLabel(id, 40) || !validCrewRunNode(n) {
			return false
		}
	}
	if (c.Status == "waiting") != (c.Waiting != nil) || !validCrewRunWaiting(c.Waiting, c.Nodes) {
		return false
	}
	if _, ok := c.Nodes[c.Only]; c.Only != "" && !ok {
		return false
	}
	raw, err := json.Marshal(c)
	return err == nil && len(raw) <= crewRunMaxBytes
}

// ListCrewRuns returns runs newest first, optionally of one crew item and status.
func (s *Service) ListCrewRuns(ctx context.Context, token string, scope Scope, crewID, status string, limit int) (CrewRunPage, error) {
	if !validCrewRunQuery(scope, crewID, status, limit) {
		return CrewRunPage{}, ErrInvalid
	}
	if limit == 0 {
		limit = 50
	}
	if _, err := s.authorize(ctx, token, "read:"+colCrew, scope, "", ""); err != nil {
		return CrewRunPage{}, err
	}
	// A status filter is a query, so the engine always sees every waiting or
	// running run. shortcut: without one, filter over the newest 1000.
	var rows []Record
	var err error
	if status != "" {
		rows, err = s.Repo.ListCrewRunsByStatus(ctx, scope, status, 1000)
	} else {
		rows, err = s.Repo.ListRecords(ctx, scope, colCrewRuns, 1000)
	}
	if err != nil {
		return CrewRunPage{}, err
	}
	out := CrewRunPage{Items: []Record{}}
	for _, r := range rows {
		if !readableCrewRun(r) || crewID != "" && r.CrewRun.CrewID != crewID || status != "" && r.CrewRun.Status != status {
			continue
		}
		if err := s.authorizeFlowSources(ctx, token, r); err != nil {
			if errors.Is(err, ErrDenied) || errors.Is(err, ErrNotFound) {
				continue
			}
			return CrewRunPage{}, err
		}
		out.Items = append(out.Items, r)
		if len(out.Items) == limit {
			break
		}
	}
	return out, nil
}

func validCrewRunQuery(scope Scope, crewID, status string, limit int) bool {
	return !(!validScope(scope) || crewID != "" && !validRunLabel(crewID, 256) || status != "" && !slices.Contains(crewRunStatuses, status) || limit < 0 || limit > 200 && status == "" || limit > 1000)
}

func (s *Service) GetCrewRun(ctx context.Context, token string, scope Scope, id string) (Record, error) {
	if !validScope(scope) || !validRunLabel(id, 256) {
		return Record{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colCrew, scope, id, ""); err != nil {
		return Record{}, err
	}
	r, err := s.Repo.Get(ctx, colCrewRuns, scope, id)
	if err != nil {
		return Record{}, err
	}
	if !readableCrewRun(r) {
		return Record{}, ErrNotFound
	}
	if err := s.authorizeFlowSources(ctx, token, r); err != nil {
		return Record{}, err
	}
	return r, nil
}

func readableCrewRun(r Record) bool {
	return r.Status == statusActive && !r.ContentErased && !r.PrivacyQuarantined && r.CrewRun != nil
}

// crewRunWriter allows people and the host engine (service or agent token);
// pipeline principals never drive runs.
func crewRunWriter(role string) bool {
	return role == roleHuman || role == roleService || role == roleAgent
}

// CreateCrewRun starts a run of a live workflow. A run may start with nodes
// already done or skipped (실패한 곳부터 다시: the desktop copies the finished
// nodes of a failed run); only a person may ask for that, and an approval node
// copied as done carries that person's decision.
func (s *Service) CreateCrewRun(ctx context.Context, token string, in CrewRunInput) (Record, error) {
	resumed, err := validateCrewRunStart(in)
	if err != nil {
		return Record{}, err
	}
	if err := noSecrets(in.Run); err != nil {
		return Record{}, err
	}
	d, err := s.authorize(ctx, token, "write:"+colCrewRuns, in.Scope, "", "")
	if err != nil {
		return Record{}, err
	}
	// A one-node run skips every step before it, approvals too, and a resumed one starts past them: only a person may ask.
	if !crewRunWriter(d.Role) || (in.Run.Only != "" || resumed || in.Run.Trigger == "test") && d.Role != roleHuman {
		return Record{}, ErrDenied
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	run := in.Run
	if run.Nodes == nil {
		run.Nodes = map[string]CrewRunNode{}
	}
	r := Record{ID: ID(), Scope: in.Scope, Kind: colCrewRuns, Status: statusActive, Revision: 1, Text: run.Trigger, CrewRun: &run,
		CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role}
	if in.RequestID != "" {
		r.ID = flowRequestID(in.Scope, d.PrincipalID, in.RequestID)
		r.RequestHash = flowRequestHash(in.Run)
	}
	var definitions map[string]FlowDefinition
	if run.InvocationMode == "flow" {
		definitions, err = s.flowDefinitions(ctx, token, in.Scope, run.CrewID)
		if err != nil {
			return Record{}, err
		}
		run.RootID = r.ID
	}
	owners, err := s.workflowOwners(ctx, in.Scope)
	if err != nil {
		return Record{}, err
	}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if in.RequestID != "" {
			if old, e := tx.Get(ctx, colCrewRuns, in.Scope, r.ID); e == nil {
				if !readableCrewRun(old) || old.RequestHash != r.RequestHash {
					return ErrConflict
				}
				if e := s.authorizeFlowSourcesFrom(ctx, token, old, tx); e != nil {
					return e
				}
				r = old
				return nil
			} else if !errors.Is(e, ErrNotFound) {
				return e
			}
		}
		crew, err := tx.Get(ctx, colCrew, in.Scope, run.CrewID)
		if err != nil {
			return err
		}
		if err := checkCrewRunTarget(crew, &run, owners, d.Role); err != nil {
			return err
		}
		if err := prepareFlowRun(&run, crew, in.RequestID, resumed, definitions); err != nil {
			return err
		}
		if err := checkFlowDefinitions(ctx, tx, in.Scope, definitions); err != nil {
			return err
		}
		approvals := map[string]bool{}
		addApprovals(approvals, crew.Crew)
		decisions, ok := copiedDecisions(run.Nodes, approvals, d.PrincipalID, now)
		if !ok {
			return ErrInvalid
		}
		run.Decisions = decisions
		if err := tx.Insert(ctx, colCrewRuns, r); err != nil {
			return err
		}
		if err := enqueueFlowDelivery(ctx, tx, r); err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "create:"+colCrewRuns, r.ID)
	})
	if errors.Is(err, ErrConflict) && in.RequestID != "" {
		if old, e := s.Repo.Get(ctx, colCrewRuns, in.Scope, flowRequestID(in.Scope, d.PrincipalID, in.RequestID)); e == nil && readableCrewRun(old) && old.RequestHash == flowRequestHash(in.Run) {
			if e = s.authorizeFlowSources(ctx, token, old); e != nil {
				return Record{}, e
			}
			return old, nil
		}
	}
	return r, err
}

// checkCrewRunTarget checks that the crew item may start this run.
func checkCrewRunTarget(crew Record, run *CrewRun, owners map[string]string, role string) error {
	if !liveFlowTarget(crew) || run.InvocationMode != "flow" && crew.Crew.Type != "workflow" {
		return ErrInvalid
	}
	// A workflow an agent works by runs only when that agent is called (@), never alone by $, schedule or webhook; a 시험 실행 (test) is the one exception.
	if _, owned := owners[crew.Crew.Handle]; owned && run.Trigger != "agent" && run.Trigger != "test" {
		return ErrInvalid
	}
	if run.Trigger == "agent" && !crewRunWriter(role) {
		return ErrDenied
	}
	return nil
}

// UpdateCrewRun replaces a run's progress. The crew, revision and trigger are
// fixed at creation, a finished run never changes, and decisions are kept
// from the stored record whatever the caller sends.
func (s *Service) UpdateCrewRun(ctx context.Context, token, id string, in CrewRunUpdate) (Record, error) {
	if !validScope(in.Scope) || !validRunLabel(id, 256) || in.ExpectedRevision < 1 || !validCrewRun(in.Run) {
		return Record{}, ErrInvalid
	}
	if err := noSecrets(in.Run); err != nil {
		return Record{}, err
	}
	d, err := s.authorize(ctx, token, "write:"+colCrewRuns, in.Scope, id, "")
	if err != nil {
		return Record{}, err
	}
	if !crewRunWriter(d.Role) {
		return Record{}, ErrDenied
	}
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, colCrewRuns, in.Scope, id)
		if err != nil {
			return err
		}
		if err := checkCrewRunProgress(r, in); err != nil {
			return err
		}
		if err := s.authorizeFlowSourcesFrom(ctx, token, r, tx); err != nil {
			return err
		}
		old := r.CrewRun
		if err := checkFlowEffectProgress(ctx, tx, r, in.Run); err != nil {
			return err
		}
		if err := checkFlowChildrenTerminal(ctx, tx, r, in.Run.Status); err != nil {
			return err
		}
		approvals, err := s.runApprovals(ctx, tx, in.Scope, old)
		if err != nil {
			return err
		}
		if !keepsApprovals(old, in.Run, approvals) {
			return ErrInvalid
		}
		run := in.Run
		if run.InvocationMode != old.InvocationMode {
			return ErrInvalid
		}
		keepCrewRunFields(&run, old, r.Revision)
		if run.Nodes == nil {
			run.Nodes = map[string]CrewRunNode{}
		}
		prev := r.Revision
		r.CrewRun = &run
		r.Revision++
		r.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
		if err := tx.Replace(ctx, colCrewRuns, r, prev); err != nil {
			return err
		}
		if err := readyFlowDelivery(ctx, tx, r); err != nil {
			return err
		}
		out = r
		return audit(ctx, tx, in.Scope, d.PrincipalID, "update:"+colCrewRuns, id)
	})
	return out, err
}

// runApprovals are the approval nodes of the graph a run follows: a flow run's
// pinned definition, else the crew item's current graph.
func (s *Service) runApprovals(ctx context.Context, tx Tx, scope Scope, old *CrewRun) (map[string]bool, error) {
	if old.InvocationMode == "flow" && old.Definition != nil {
		approvals := map[string]bool{}
		addApprovals(approvals, old.Definition)
		return approvals, nil
	}
	return s.approvalNodes(ctx, tx, scope, old.CrewID)
}

// keepCrewRunFields copies onto a progress update the fields only the store
// sets, and keeps the decisions still in force.
func keepCrewRunFields(run, old *CrewRun, revision int64) {
	run.CancelRequested = old.CancelRequested
	if old.InvocationMode == "flow" {
		run.Deadline = old.Deadline
	}
	run.Definition = old.Definition
	run.RootID, run.ParentID, run.ParentNodeID, run.Depth = old.RootID, old.ParentID, old.ParentNodeID, old.Depth
	run.Definitions, run.Children, run.Policy, run.SourceType = old.Definitions, old.Children, old.Policy, old.SourceType
	run.Effects = old.Effects
	run.ResultRevision = old.ResultRevision
	if run.InvocationMode == "flow" && terminalCrewRun(run.Status) {
		run.ResultRevision = revision + 1
	}
	// A back edge that sends an approval node to pending again needs a new decision.
	run.Decisions = nil
	for id, dec := range old.Decisions {
		if n, ok := run.Nodes[id]; !ok || n.Status != "pending" {
			if run.Decisions == nil {
				run.Decisions = map[string]CrewRunDecision{}
			}
			run.Decisions[id] = dec
		}
	}
}

func validateCrewRunStart(in CrewRunInput) (bool, error) {
	if !validScope(in.Scope) || !validCrewRun(in.Run) || !clientFlowStart(in.Run) || in.Run.Status != "running" || in.Run.Waiting != nil || len(in.Run.Decisions) > 0 || in.RequestID != "" && !validRunLabel(in.RequestID, 200) {
		return false, ErrInvalid
	}
	resumed := false
	for _, n := range in.Run.Nodes {
		switch n.Status {
		case "pending":
		case "done", "skipped":
			resumed = true
		default:
			return false, ErrInvalid
		}
	}
	return resumed, nil
}

func checkCrewRunProgress(r Record, in CrewRunUpdate) error {
	if !readableCrewRun(r) {
		return ErrNotFound
	}
	old := r.CrewRun
	if r.Revision != in.ExpectedRevision || terminalCrewRun(old.Status) || old.CancelRequested && in.Run.Status != "stopped" {
		return ErrConflict
	}
	if in.Run.CrewID != old.CrewID || in.Run.CrewRevision != old.CrewRevision || in.Run.Trigger != old.Trigger || in.Run.Only != old.Only || in.Run.ResearchMode != old.ResearchMode || structuredText(in.Run.Origin) != structuredText(old.Origin) {
		return ErrInvalid
	}
	if old.InvocationMode == "flow" {
		if old.Deadline != nil && !old.Deadline.After(time.Now()) && in.Run.Status == "succeeded" {
			return ErrConflict
		}
		if in.Run.Input != old.Input {
			return ErrInvalid
		}
		if in.LeaseToken == "" || r.LeaseToken != in.LeaseToken || !r.LeaseUntil.After(time.Now()) {
			return ErrConflict
		}
	}
	return nil
}

// approvalNodes are the approval node IDs of a crew item's graph.
func (s *Service) approvalNodes(ctx context.Context, tx Tx, scope Scope, crewID string) (map[string]bool, error) {
	out := map[string]bool{}
	it, err := tx.Get(ctx, colCrew, scope, crewID)
	if err != nil {
		return nil, err
	}
	addApprovals(out, it.Crew)
	return out, nil
}

func addApprovals(out map[string]bool, c *Crew) {
	if c == nil || c.Graph == nil {
		return
	}
	for _, n := range c.Graph.Nodes {
		if n.Type == "approval" {
			out[n.ID] = true
		}
	}
}

// copiedDecisions records the creating person's decision for each approval
// node a new run starts with as done; its port and output {approved, note}
// must agree, as keepsApprovals later checks.
func copiedDecisions(nodes map[string]CrewRunNode, approvals map[string]bool, by string, at time.Time) (map[string]CrewRunDecision, bool) {
	var out map[string]CrewRunDecision
	for id := range approvals {
		n := nodes[id]
		if n.Status != "done" {
			continue
		}
		var o struct {
			Approved *bool  `json:"approved"`
			Note     string `json:"note"`
		}
		if json.Unmarshal([]byte(n.Output), &o) != nil || o.Approved == nil || runes(o.Note) > 500 ||
			n.Port != map[bool]string{true: "approve", false: "reject"}[*o.Approved] {
			return nil, false
		}
		if out == nil {
			out = map[string]CrewRunDecision{}
		}
		out[id] = CrewRunDecision{Approve: *o.Approved, Note: o.Note, DecidedBy: by, DecidedAt: at}
	}
	return out, true
}

// keepsApprovals holds a person's say over approval nodes: progress may wait
// on one only as an approval, and may finish one only on the port of the
// decision a person recorded.
func keepsApprovals(old *CrewRun, run CrewRun, approvals map[string]bool) bool {
	if w := run.Waiting; w != nil && approvals[w.NodeID] && w.Kind != "approval" {
		return false
	}
	for id := range approvals {
		n := run.Nodes[id]
		if n.Status != "done" && n.Port == "" {
			continue
		}
		dec, ok := old.Decisions[id]
		if !ok || n.Port != map[bool]string{true: "approve", false: "reject"}[dec.Approve] {
			return false
		}
		// Later steps read {approved, note}; it must say what the person said.
		var out struct {
			Approved *bool  `json:"approved"`
			Note     string `json:"note"`
		}
		if n.Status == "done" && (json.Unmarshal([]byte(n.Output), &out) != nil || out.Approved == nil || *out.Approved != dec.Approve || out.Note != dec.Note) {
			return false
		}
	}
	return true
}

// DecideCrewRun records a person's answer to the approval node the run waits
// on, or to a budget wait (go on over the budget, or stop). The engine sees
// the decision and resumes the run.
func (s *Service) DecideCrewRun(ctx context.Context, token, id string, in CrewRunDecisionInput) (Record, error) {
	if !validScope(in.Scope) || !validRunLabel(id, 256) || in.ExpectedRevision < 1 || !validRunLabel(in.NodeID, 40) || runes(in.Note) > 500 {
		return Record{}, ErrInvalid
	}
	if err := noSecrets(in.Note); err != nil {
		return Record{}, err
	}
	d, err := s.authorize(ctx, token, "write:"+colCrewRuns, in.Scope, id, "")
	if err != nil {
		return Record{}, err
	}
	if d.Role != roleHuman && d.Role != roleService {
		return Record{}, ErrDenied
	}
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, colCrewRuns, in.Scope, id)
		if err != nil {
			return err
		}
		if !readableCrewRun(r) {
			return ErrNotFound
		}
		if err := s.authorizeFlowSourcesFrom(ctx, token, r, tx); err != nil {
			return err
		}
		if r.CrewRun.Waiting != nil {
			if err := recordDecision(ctx, tx, in.Scope, d.PrincipalID, map[bool]string{true: "flow_approved", false: "flow_rejected"}[in.Approve], id, r.CrewRun.Waiting.Kind, in.Note); err != nil {
				return err
			}
		}
		if r.CrewRun.InvocationMode == "flow" && r.CrewRun.Waiting != nil && r.CrewRun.Waiting.Kind == "child" {
			out, err = s.decideFlowChild(ctx, tx, token, r, in, d, 0, r.CrewRun.Trigger == "discord")
			return err
		}
		run := *r.CrewRun
		if err := checkCrewRunDecision(r, in, d.Role); err != nil {
			return err
		}
		if err := s.decideFlowEffect(ctx, tx, token, r, in.Approve, d); err != nil {
			return err
		}
		decisions := make(map[string]CrewRunDecision, len(run.Decisions)+1)
		for k, v := range run.Decisions {
			decisions[k] = v
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		decisions[in.NodeID] = CrewRunDecision{Approve: in.Approve, Note: in.Note, DecidedBy: d.PrincipalID, DecidedAt: now}
		run.Decisions = decisions
		prev := r.Revision
		r.CrewRun = &run
		r.Revision++
		r.UpdatedAt = now
		if err := tx.Replace(ctx, colCrewRuns, r, prev); err != nil {
			return err
		}
		out = r
		return audit(ctx, tx, in.Scope, d.PrincipalID, "decide:"+colCrewRuns, id)
	})
	return out, err
}

// checkCrewRunDecision checks that the run waits on the approval or budget
// node the decision answers.
func checkCrewRunDecision(r Record, in CrewRunDecisionInput, role string) error {
	run := r.CrewRun
	w := run.Waiting
	// A service (the Discord host, on an allowed person's button) answers budget waits only; approvals stay people's.
	if role == roleService && (w == nil || w.Kind != "budget" || run.Trigger != "discord") {
		return ErrDenied
	}
	if r.Revision != in.ExpectedRevision || run.Status != "waiting" || w == nil || w.Kind != "approval" && w.Kind != "budget" || w.NodeID != in.NodeID {
		return ErrConflict
	}
	if _, done := run.Decisions[in.NodeID]; done {
		return ErrConflict
	}
	return nil
}

func crewRunRoutes(mux routeRegistrar, s *Service) {
	flowRoutes(mux, s)
	mux.HandleFunc("GET /v1/crew/runs", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit := 0
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				respond(w, nil, ErrInvalid)
				return
			}
			limit = n
		}
		out, e := s.ListCrewRuns(r.Context(), bearer(r), scopeQuery(r), q.Get("crew_id"), q.Get("status"), limit)
		if q.Get("brief") == "1" {
			// The engine only needs ids, node states and waits: leaving out the
			// stored text keeps a page of 1000 runs small.
			for i, rec := range out.Items {
				run := *rec.CrewRun
				run.Input, run.Summary = "", ""
				run.Definition = nil
				nodes := make(map[string]CrewRunNode, len(run.Nodes))
				for id, n := range run.Nodes {
					n.Output, n.Error = "", ""
					nodes[id] = n
				}
				run.Nodes = nodes
				out.Items[i].CrewRun = &run
			}
		}
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/crew/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.GetCrewRun(r.Context(), bearer(r), scopeQuery(r), r.PathValue("id"))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/crew/runs", func(w http.ResponseWriter, r *http.Request) {
		var in CrewRunInput
		if !decodeLimit(w, r, &in, crewRunMaxBytes+(64<<10)) {
			return
		}
		out, e := s.CreateCrewRun(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/crew/runs/{id}/progress", func(w http.ResponseWriter, r *http.Request) {
		var in CrewRunUpdate
		if !decodeLimit(w, r, &in, crewRunMaxBytes+(64<<10)) {
			return
		}
		out, e := s.UpdateCrewRun(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/crew/runs/{id}/decision", func(w http.ResponseWriter, r *http.Request) {
		var in CrewRunDecisionInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.DecideCrewRun(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
}

// workflowOwners maps a workflow handle to the handle of the live agent that works by it.
func (s *Service) workflowOwners(ctx context.Context, scope Scope) (map[string]string, error) {
	live, err := s.liveCrew(ctx, scope)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range live {
		if r.Crew.Type == "agent" && r.Crew.Workflow != "" {
			out[r.Crew.Workflow] = r.Crew.Handle
		}
	}
	return out, nil
}
