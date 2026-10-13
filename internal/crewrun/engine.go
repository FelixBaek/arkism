package crewrun

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Engine runs graphs. Progress lives in the runs API after every node change,
// so a restart resumes waiting runs and settles interrupted ones.
//
// Scheduling rule: a pending node is decided once every source of its
// incoming edges is finished (done, skipped or failed). An edge is followed
// when its source is done and the edge has no port, or its port equals the
// branch the source took ("yes"/"no" for condition, "approve"/"reject" for
// approval). A decided node runs if at least one incoming edge is followed,
// otherwise it is skipped; nodes without incoming edges are roots and get the
// trigger input. A node with one followed edge gets that source's output; with
// several (and always for merge) it gets an object keyed by source node id.
// Nodes whose config "access" is "read" run together with other ready read
// nodes; every other node runs alone.
//
// Back edges (max > 0) are left out of that rule: when a node ends on a back
// edge's port, the edge's target and every node after it go back to pending,
// at most max times per edge (see followBack).
type Engine struct {
	Store Store
	Now   func() time.Time
	Loc   *time.Location
	Log   *slog.Logger
	// DefaultModel runs AI nodes when neither the node nor the workflow names a model.
	DefaultModel string
	// Runner is the AI node command prefix; the engine appends
	// "run-node --json" and writes the node spec as JSON on stdin.
	Runner      []string
	Sandbox     []string
	GH          string
	KeysFile    string
	NotifyFile  string
	HooksFile   string
	PublicURL   string
	TelegramAPI string
	HTTP        *http.Client
	CodeTimeout time.Duration

	// Meetings drives team meetings when set (FromEnv sets the Arkism client).
	Meetings MeetingStore
	// MeetingAI replaces the run-node call of a meeting turn (tests).
	MeetingAI func(context.Context, aiSpec) (aiAnswer, error)

	lease  string // this process's meeting lease token
	mu     sync.Mutex
	crew   map[string]CrewItem
	active map[string]bool
	fired  map[string]time.Time
	crons  map[string]*Cron
	wg     sync.WaitGroup
}

var errStopped = errors.New("run stopped elsewhere")

const maxStored = 20000

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

func (e *Engine) httpClient() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

// httpClientFor is httpClient with its overall timeout raised to at least d, so a
// node's own timeout (http `timeout`, n8n `sync_timeout`) is not cut short by the
// daemon client's 60 s.
func (e *Engine) httpClientFor(d time.Duration) *http.Client {
	c := e.httpClient()
	if c.Timeout == 0 || c.Timeout >= d {
		return c
	}
	cp := *c
	cp.Timeout = d
	return &cp
}

// LoadCrew refreshes the crew items from Arkism.
func (e *Engine) LoadCrew(ctx context.Context) error {
	items, err := e.Store.ListCrew(ctx)
	if err != nil {
		return err
	}
	m := make(map[string]CrewItem, len(items))
	for _, it := range items {
		m[it.ID] = it
	}
	e.mu.Lock()
	e.crew = m
	e.mu.Unlock()
	return nil
}

func (e *Engine) item(id string) (CrewItem, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	it, ok := e.crew[id]
	return it, ok
}

func crewKey(h string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(strings.TrimSpace(h), "$"), " ", ""))
}

// workflow is the live workflow called handle.
func (e *Engine) workflow(handle string) (CrewItem, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, it := range e.crew {
		if it.Crew.Type == "workflow" && crewKey(it.Crew.Handle) == crewKey(handle) {
			return it, true
		}
	}
	return CrewItem{}, false
}

// graphFor is the graph a workflow runs.
func graphFor(it CrewItem) (*Graph, error) {
	if it.Crew.Type != "workflow" {
		return nil, fmt.Errorf("%s is not runnable", it.Crew.Type)
	}
	if it.Crew.Graph == nil || len(it.Crew.Graph.Nodes) == 0 {
		return nil, fmt.Errorf("workflow %s has no graph", it.ID)
	}
	return it.Crew.Graph, nil
}

// StartRun creates a run of a workflow; Drive executes it.
func (e *Engine) StartRun(ctx context.Context, crewID, trigger string, input any) (Run, error) {
	it, ok := e.item(crewID)
	if !ok {
		return Run{}, fmt.Errorf("no live crew item %s", crewID)
	}
	g, err := graphFor(it)
	if err != nil {
		return Run{}, err
	}
	nodes := make(map[string]*NodeState, len(g.Nodes))
	for _, n := range g.Nodes {
		nodes[n.ID] = &NodeState{Status: "pending"}
	}
	if input == nil {
		input = map[string]any{}
	}
	return e.Store.CreateRun(ctx, RunState{CrewID: it.ID, CrewRevision: it.Revision, Trigger: trigger, Status: "running",
		Input: encodeStored(input), Nodes: nodes})
}

func (e *Engine) claim(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active == nil {
		e.active = map[string]bool{}
	}
	if e.active[id] {
		return false
	}
	e.active[id] = true
	return true
}

func (e *Engine) release(id string) {
	e.mu.Lock()
	delete(e.active, id)
	e.mu.Unlock()
}

// Go drives a run in the background; Wait waits for all of them.
func (e *Engine) Go(ctx context.Context, id string) {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		if err := e.Drive(ctx, id); err != nil && ctx.Err() == nil {
			e.log().Warn("crew run", "run_id", id, "error", err.Error())
		}
	}()
}

func (e *Engine) Wait() { e.wg.Wait() }

// Drive advances one run until it finishes, waits or the context ends.
func (e *Engine) Drive(ctx context.Context, id string) error {
	if !e.claim(id) {
		return nil
	}
	defer e.release(id)
	run, err := e.Store.GetRun(ctx, id)
	if err != nil {
		return err
	}
	if terminal(run.State.Status) {
		return nil
	}
	x := &runExec{e: e, run: run}
	it, ok := e.item(run.State.CrewID)
	if !ok || it.Revision != run.State.CrewRevision {
		return x.finish(ctx, "failed", "crew_changed: the definition changed or was archived after the run started", "")
	}
	g, err := graphFor(it)
	if err != nil {
		return x.finish(ctx, "failed", "no_graph: "+err.Error(), "")
	}
	x.item = it
	x.index(g)
	// The deadline counts from the first drive; it is stored with the next save.
	if l := it.Crew.Limits; l != nil && l.MaxMinutes > 0 && x.run.State.Deadline == nil {
		t := e.now().UTC().Add(time.Duration(l.MaxMinutes) * time.Minute).Truncate(time.Millisecond)
		x.run.State.Deadline = &t
	}
	if err := x.settleInterrupted(ctx); err != nil {
		return err
	}
	if err := x.loop(ctx); !errors.Is(err, errStopped) {
		return err
	}
	return nil
}

func terminal(s string) bool { return s == "succeeded" || s == "failed" || s == "stopped" }

// runExec is one run being driven.
type runExec struct {
	e     *Engine
	mu    sync.Mutex
	run   Run
	item  CrewItem
	nodes map[string]Node
	order []string
	in    map[string][]Edge // forward edges only
	out   map[string][]Edge // forward edges only
	back  map[string][]Edge // back edges (max > 0) by source
}

func (x *runExec) index(g *Graph) {
	x.nodes, x.in, x.out, x.back = map[string]Node{}, map[string][]Edge{}, map[string][]Edge{}, map[string][]Edge{}
	deg := map[string]int{}
	for _, n := range g.Nodes {
		x.nodes[n.ID] = n
		deg[n.ID] = 0
		if x.run.State.Nodes == nil {
			x.run.State.Nodes = map[string]*NodeState{}
		}
		if x.run.State.Nodes[n.ID] == nil {
			x.run.State.Nodes[n.ID] = &NodeState{Status: "pending"}
		}
	}
	for _, ed := range g.Edges {
		if ed.Max > 0 {
			x.back[ed.From] = append(x.back[ed.From], ed)
			continue
		}
		x.in[ed.To] = append(x.in[ed.To], ed)
		x.out[ed.From] = append(x.out[ed.From], ed)
		deg[ed.To]++
	}
	// Kahn's order, ties in graph order, so runs are deterministic.
	for len(x.order) < len(g.Nodes) {
		progressed := false
		for _, n := range g.Nodes {
			if deg[n.ID] == 0 && !slices.Contains(x.order, n.ID) {
				x.order = append(x.order, n.ID)
				for _, ed := range x.out[n.ID] {
					deg[ed.To]--
				}
				progressed = true
			}
		}
		if !progressed { // cycle: the server refuses these; stop rather than spin
			break
		}
	}
}

func (x *runExec) state(id string) *NodeState { return x.run.State.Nodes[id] }

// settleInterrupted handles nodes left "running" by a stopped engine: read
// nodes run again, anything else fails the run instead of repeating a side effect.
func (x *runExec) settleInterrupted(ctx context.Context) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	changed, failed := false, false
	for id, st := range x.run.State.Nodes {
		if st.Status != "running" {
			continue
		}
		changed = true
		if x.nodes[id].Config["access"] == "read" {
			st.Status, st.StartedAt = "pending", nil
			continue
		}
		st.Status, st.Error = "failed", "interrupted: the engine stopped while this step ran; not repeated"
		x.stamp(st)
		failed = true
	}
	if failed {
		x.closeRun("failed", "")
	}
	if !changed {
		return nil
	}
	return x.saveLocked(ctx, "")
}

func (x *runExec) stamp(st *NodeState) {
	t := x.e.now().UTC().Truncate(time.Millisecond)
	if st.Status == "running" {
		st.StartedAt = &t
	} else {
		st.FinishedAt = &t
	}
}

// closeRun ends the run and skips whatever never ran.
func (x *runExec) closeRun(status, summary string) {
	x.run.State.Status, x.run.State.Waiting = status, nil
	if summary != "" {
		x.run.State.Summary = clip(summary, 4000)
	}
	for _, st := range x.run.State.Nodes {
		if st.Status == "pending" || st.Status == "waiting" {
			st.Status = "skipped"
		}
	}
}

func (x *runExec) finish(ctx context.Context, status, errMsg, summary string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if errMsg != "" && summary == "" {
		summary = errMsg
	}
	x.closeRun(status, summary)
	return x.saveLocked(ctx, "")
}

// saveLocked persists the run. On a revision conflict it re-reads: a run
// finished elsewhere (a person stopped it) ends the drive; otherwise the
// engine's progress wins on the fresh revision. A refused output (looks like
// a credential) is withheld and the node fails.
func (x *runExec) saveLocked(ctx context.Context, node string) error {
	wait := time.Second
	for attempt := 0; attempt < 8; attempt++ {
		r, err := x.e.Store.UpdateRun(ctx, x.run)
		if err == nil {
			x.run.Revision, x.run.State.Decisions = r.Revision, r.State.Decisions
			return nil
		}
		switch apiCode(err) {
		case "revision_conflict":
			fresh, gerr := x.e.Store.GetRun(ctx, x.run.ID)
			if gerr != nil {
				return gerr
			}
			if terminal(fresh.State.Status) {
				return errStopped
			}
			x.run.Revision, x.run.State.Decisions = fresh.Revision, fresh.State.Decisions
		case "secret_detected":
			// The credential may sit in any node's output or error, or in the
			// summary copied from one, so none of them is kept.
			x.dropOutputs(node, true, "secret_detected: the output looked like a credential and was not stored",
				"secret_detected: an output looked like a credential, so outputs were not stored")
		case "invalid_request":
			// The server refused the progress (outputs too large for the run,
			// say). End the run rather than leave it "running" with nothing
			// driving it.
			x.dropOutputs(node, false, "store_refused: the result could not be stored",
				"store_refused: the run's progress could not be stored, so outputs were dropped")
		default:
			// A network error or a 5xx: wait and try again, so a short server
			// outage does not leave the run "running" with nothing driving it.
			// shortcut: about 4 minutes in all; a longer outage needs an engine restart.
			var ae *APIError
			if errors.As(err, &ae) && ae.Status < 500 {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			wait = min(2*wait, time.Minute)
		}
	}
	return fmt.Errorf("save run %s: gave up", x.run.ID)
}

// dropOutputs clears stored outputs (and errors too, when they may hold what
// was refused), fails the node being saved and ends the run.
func (x *runExec) dropOutputs(node string, errorsToo bool, nodeErr, summary string) {
	for id, st := range x.run.State.Nodes {
		// A decided approval's output is the person's {approved, note}, never a
		// credential; the server refuses a done approval without it.
		if n, ok := x.nodes[id]; ok && n.Type == "approval" && st.Status == "done" {
			continue
		}
		st.Output = ""
		if errorsToo {
			st.Error = ""
		}
	}
	if st := x.state(node); st != nil {
		st.Status, st.Error = "failed", nodeErr
	}
	x.run.State.Summary = ""
	x.closeRun("failed", summary)
}

func (x *runExec) decided(id string) (ready, followed bool, sources []string) {
	ready = true
	for _, ed := range x.in[id] {
		src := x.state(ed.From)
		switch src.Status {
		case "done":
			if ed.Port == "" || ed.Port == src.Port {
				followed = true
				sources = append(sources, ed.From)
			}
		case "skipped", "failed":
		default:
			ready = false
		}
	}
	if len(x.in[id]) == 0 {
		followed = true
	}
	return ready, followed, sources
}

// inputFor builds a node's input from the outputs it follows.
// next splits the pending nodes into ready ones and ones to skip. A one-node run (only) readies that node and
// skips every other one.
func (x *runExec) next(only string) (ready, skip []string) {
	for _, id := range x.order {
		if x.state(id).Status != "pending" {
			continue
		}
		if only != "" {
			if id == only {
				ready = append(ready, id)
			} else {
				skip = append(skip, id)
			}
			continue
		}
		ok, followed, _ := x.decided(id)
		switch {
		case ok && followed:
			ready = append(ready, id)
		case ok:
			skip = append(skip, id)
		}
	}
	return ready, skip
}

func (x *runExec) inputFor(id string) any {
	if x.run.State.Only == id {
		return decodeStored(x.run.State.Input)
	}
	_, _, sources := x.decided(id)
	if len(x.in[id]) == 0 {
		return decodeStored(x.run.State.Input)
	}
	if len(sources) == 1 && x.nodes[id].Type != "merge" {
		return decodeStored(x.state(sources[0]).Output)
	}
	m := map[string]any{}
	for _, s := range sources {
		m[s] = decodeStored(x.state(s).Output)
	}
	return m
}

func (x *runExec) loop(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		x.mu.Lock()
		st := x.run.State
		if terminal(st.Status) {
			x.mu.Unlock()
			return nil
		}
		if st.Status == "waiting" {
			resumed, err := x.resumeLocked(ctx)
			x.mu.Unlock()
			if err != nil || !resumed {
				return err
			}
			continue
		}
		ready, skip := x.next(st.Only)
		if len(skip) > 0 {
			for _, id := range skip {
				x.state(id).Status = "skipped"
			}
			err := x.saveLocked(ctx, "")
			x.mu.Unlock()
			if err != nil {
				return err
			}
			continue
		}
		if len(ready) == 0 {
			x.closeRun("succeeded", x.lastSummary())
			err := x.saveLocked(ctx, "")
			x.mu.Unlock()
			return err
		}
		batch := ready[:1]
		if x.concurrent(ready[0]) {
			batch = nil
			for _, id := range ready {
				if x.concurrent(id) {
					batch = append(batch, id)
				}
			}
		}
		if msg := x.overLimit(len(batch)); msg != "" {
			x.closeRun("failed", msg)
			err := x.saveLocked(ctx, "")
			x.mu.Unlock()
			return err
		}
		inputs := map[string]any{}
		for _, id := range batch {
			inputs[id] = x.inputFor(id)
			s := x.state(id)
			s.Status, s.Error = "running", ""
			s.Runs++
			x.stamp(s)
		}
		err := x.saveLocked(ctx, "")
		x.mu.Unlock()
		if err != nil {
			return err
		}
		var wg sync.WaitGroup
		errs := make([]error, len(batch))
		for i, id := range batch {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = x.runNode(ctx, id, inputs[id])
			}()
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return err
		}
	}
}

// concurrent: read nodes may share a turn, except kinds that wait, repeat
// another node or end the run.
func (x *runExec) concurrent(id string) bool {
	n := x.nodes[id]
	switch n.Type {
	case "approval", "wait", "n8n", "loop", "end":
		return false
	}
	return n.Config["access"] == "read"
}

// overLimit names the limit that starting n more nodes would break, or "".
func (x *runExec) overLimit(n int) string {
	if d := x.run.State.Deadline; d != nil && x.e.now().After(*d) {
		return "time_limit: 정해 둔 실행 시간이 지났어요"
	}
	l := x.item.Crew.Limits
	if l == nil || l.MaxSteps == 0 {
		return ""
	}
	total := n
	for _, st := range x.run.State.Nodes {
		total += st.Runs
	}
	if total > l.MaxSteps {
		return fmt.Sprintf("step_limit: 단계 한도 %d번을 넘었어요", l.MaxSteps)
	}
	return ""
}

func (x *runExec) title(id string) string { return cmp.Or(x.nodes[id].Title, id) }

// followBack takes the node's back edge for the port it ended on (no port: an
// edge without one). Under its max the edge's target and every node after it
// (forward edges only) go back to pending; other finished nodes stay. At the
// max the run fails.
func (x *runExec) followBack(id string) {
	if x.run.State.Only != "" {
		return
	}
	port := x.state(id).Port
	for _, ed := range x.back[id] {
		if ed.Port != port {
			continue
		}
		key := ed.From + ">" + ed.To + ":" + ed.Port
		if x.run.State.Loops[key] >= ed.Max {
			x.closeRun("failed", fmt.Sprintf("loop_limit: %s → %s 최대 %d번", x.title(ed.From), x.title(ed.To), ed.Max))
			return
		}
		if x.run.State.Loops == nil {
			x.run.State.Loops = map[string]int{}
		}
		x.run.State.Loops[key]++
		todo, seen := []string{ed.To}, map[string]bool{}
		for len(todo) > 0 {
			n := todo[0]
			todo = todo[1:]
			if seen[n] {
				continue
			}
			seen[n] = true
			st := x.state(n)
			st.Status, st.Port, st.Output, st.Error, st.StartedAt, st.FinishedAt = "pending", "", "", "", nil, nil
			delete(x.run.State.Decisions, n) // an approval asks again; the server drops it too
			for _, next := range x.out[n] {
				todo = append(todo, next.To)
			}
		}
		return
	}
}

// lastSummary is used when a graph has no end node: the last finished output.
func (x *runExec) lastSummary() string {
	if x.run.State.Summary != "" {
		return ""
	}
	for i := len(x.order) - 1; i >= 0; i-- {
		if st := x.state(x.order[i]); st.Status == "done" && st.Output != "" {
			return summaryOf(decodeStored(st.Output))
		}
	}
	return ""
}

// result is what an executor hands back.
type result struct {
	out     any
	port    string
	wait    *Waiting
	end     bool
	summary string
	also    map[string]any // other nodes finished by this one (loop body)
}

type nodeError struct{ code, msg string }

func (n *nodeError) Error() string { return n.code + ": " + n.msg }

func failf(code, format string, a ...any) error { return &nodeError{code, fmt.Sprintf(format, a...)} }

func (x *runExec) runNode(ctx context.Context, id string, input any) error {
	n := x.nodes[id]
	res, err := x.e.execute(ctx, x, n, input)
	if ctx.Err() != nil {
		return ctx.Err() // leave it running; the next start settles it
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	st := x.state(id)
	// A sibling read node may have ended the run meanwhile; this node's own
	// outcome is still recorded, but it changes the run no further.
	open := !terminal(x.run.State.Status)
	switch {
	case err != nil:
		st.Status, st.Error = "failed", clip(errText(err), 500)
		x.stamp(st)
		if open {
			x.closeRun("failed", strings.TrimSpace(n.Title+" "+st.Error))
		}
	case res.wait != nil && open:
		st.Status = "waiting"
		res.wait.NodeID = id
		x.run.State.Status, x.run.State.Waiting = "waiting", res.wait
	case res.wait != nil:
		st.Status = "skipped"
	default:
		st.Status, st.Port, st.Output = "done", res.port, encodeStored(res.out)
		x.stamp(st)
		for other, out := range res.also {
			o := x.state(other)
			o.Status, o.Output, o.StartedAt = "done", encodeStored(out), st.StartedAt
			x.stamp(o)
		}
		if res.end && open {
			x.closeRun("succeeded", res.summary)
		} else if open {
			x.followBack(id)
		}
	}
	return x.saveLocked(ctx, id)
}

func errText(err error) string {
	var ne *nodeError
	if errors.As(err, &ne) {
		return ne.Error()
	}
	return "error: " + err.Error()
}

// resumeLocked continues a waiting run when its condition is met.
func (x *runExec) resumeLocked(ctx context.Context) (bool, error) {
	w := x.run.State.Waiting
	if w == nil {
		x.run.State.Status = "running"
		return true, x.saveLocked(ctx, "")
	}
	st := x.state(w.NodeID)
	if st == nil {
		x.closeRun("failed", "waiting on an unknown node")
		return false, x.saveLocked(ctx, "")
	}
	now := x.e.now()
	switch w.Kind {
	case "approval":
		d, ok := x.run.State.Decisions[w.NodeID]
		if !ok {
			return false, nil
		}
		port := "reject"
		if d.Approve {
			port = "approve"
		}
		st.Status, st.Port, st.Output = "done", port, encodeStored(map[string]any{"approved": d.Approve, "note": d.Note})
	case "budget":
		// A person let the step go over budget (it runs now) or stopped the run.
		d, ok := x.run.State.Decisions[w.NodeID]
		if !ok {
			return false, nil
		}
		x.run.State.Waiting = nil
		if !d.Approve {
			st.Status, st.Error = "skipped", "budget: 예산을 넘어 멈췄어요"
			x.closeRun("stopped", "예산을 넘어 사람이 멈췄어요")
			return false, x.saveLocked(ctx, w.NodeID)
		}
		st.Status, x.run.State.Status = "pending", "running"
		return true, x.saveLocked(ctx, w.NodeID)
	case "wait":
		if w.ResumeAt != nil && now.Before(*w.ResumeAt) {
			return false, nil
		}
		st.Status, st.Output = "done", encodeStored(x.inputFor(w.NodeID))
	case "callback":
		if w.ResumeAt == nil || now.Before(*w.ResumeAt) {
			return false, nil
		}
		st.Status, st.Error = "failed", "callback_timeout: no result arrived in time"
		x.stamp(st)
		x.closeRun("failed", st.Error)
		return false, x.saveLocked(ctx, w.NodeID)
	}
	x.stamp(st)
	x.run.State.Status, x.run.State.Waiting = "running", nil
	x.followBack(w.NodeID)
	return !terminal(x.run.State.Status), x.saveLocked(ctx, w.NodeID)
}

// readyToResume tells the poller whether a waiting run can move.
func readyToResume(r Run, now time.Time) bool {
	w := r.State.Waiting
	if r.State.Status != "waiting" || w == nil {
		return false
	}
	switch w.Kind {
	case "approval", "budget":
		_, ok := r.State.Decisions[w.NodeID]
		return ok
	default:
		return w.ResumeAt != nil && !now.Before(*w.ResumeAt)
	}
}

// encodeStored turns a value into the stored output text (≤ 20000 chars).
// Longer values become {"truncated": true, "text": as many first chars as fit}.
func encodeStored(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(fmt.Sprint(v))
	}
	s := string(b)
	if utf8.RuneCountInString(s) <= maxStored {
		return s
	}
	// Re-encoding escapes quotes, backslashes and <>& again, so shrink the
	// kept text until the result fits.
	for n := 19000; ; {
		t, _ := json.Marshal(map[string]any{"truncated": true, "text": clip(s, n)})
		if size := utf8.RuneCount(t); size <= maxStored {
			return string(t)
		} else {
			n = n*maxStored/size - 100
		}
	}
}

func decodeStored(s string) any {
	if s == "" {
		return nil
	}
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	return v
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// summaryOf is a value as summary text: an AI node's answer, else textOf.
func summaryOf(v any) string {
	if m, ok := v.(map[string]any); ok {
		if s, ok := m["output"].(string); ok {
			return s
		}
	}
	return textOf(v)
}

// textOf is a value as text: a string as is, anything else as JSON.
func textOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}
