package memory

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Crew assists: one-off AI help the desktop asks for while you edit (AI로
// 채우기·다듬기, 대화에서 추천, 카테고리 분류, 써보기). The person asks here;
// the host runner picks the request up, runs it with
// a fill persona or the expert itself, and posts the answer back. Arkism
// makes no model call of its own.
const (
	colCrewAssists = "crew_assists"
	crewAssistTTL  = 10 * time.Minute
	// An assist an external agent runner accepted (status running) waits this long after its last report.
	crewAssistRunningTTL = 24 * time.Hour
	crewAssistPrompt     = 20000
)

// Kinds: fill (a whole definition from a description), polish (rewrite one
// text), recommend (ideas from what you asked lately), classify (pick a
// category), try (run the expert on a task).
// explain (plain-words answer) and draft (a SKILL.md or a merged one) serve the skills screens.
// suggest proposes new skills from recent memories; install_skill has the runner write a SKILL.md
// (prompt is {"name","content","agents"} JSON, no model call) into the agents' skill folders.
var crewAssistKinds = []string{"fill", "polish", "recommend", "classify", "try", "explain", "draft", "suggest", "install_skill", "fix", "comfy", "review", "build", "title", "json"}
var crewAssistStates = []string{"pending", "running", "done", "failed"}

// crewTraceStates are an external runner's task states as the host runner reports them (backends.go).
var crewTraceStates = []string{"", "starting", "accepted", "running", "waiting_approval", "succeeded", "failed", "cancel_requested", "cancelled", "unknown"}

type CrewAssist struct {
	Kind    string `json:"kind"`
	Persona string `json:"persona,omitempty"` // the fill persona's instructions
	Expert  string `json:"expert,omitempty"`  // try: the work agent's handle ("@리뷰어" or "리뷰어")
	Prompt  string `json:"prompt"`
	Lang    string `json:"lang,omitempty"` // reply language: "" or "ko" = Korean, "en" = English
	Status  string `json:"status"`
	Output  string `json:"output,omitempty"`
	Model   string `json:"model,omitempty"`
	Error   string `json:"error,omitempty"`
	// Trace: set by the host runner when an external agent runner did the work (try with an expert's backend).
	Trace *CrewAssistTrace `json:"trace,omitempty"`
}

// CrewAssistTrace says which external runner did a try and what it did and did not apply; ids and names only.
type CrewAssistTrace struct {
	Backend        string   `json:"backend"`
	TaskID         string   `json:"task_id,omitempty"`
	State          string   `json:"state,omitempty"`
	RequestedModel string   `json:"requested_model,omitempty"`
	Model          string   `json:"model,omitempty"` // the model the runner reported; empty when it did not say
	NotApplied     []string `json:"not_applied,omitempty"`
	Context        []string `json:"context,omitempty"` // what was sent, by name: instructions, skill:<id>
	Usage          string   `json:"usage,omitempty"`   // reported | unknown
	Fallback       bool     `json:"fallback"`          // always false: an external task is never re-run elsewhere
}

var crewTraceLabel = regexp.MustCompile(`^[A-Za-z0-9._:@/ -]{1,128}$`)

func validCrewAssistTrace(t *CrewAssistTrace) bool {
	if t == nil {
		return true
	}
	ok := crewTraceLabel.MatchString(t.Backend) && (t.TaskID == "" || crewTraceLabel.MatchString(t.TaskID)) && slices.Contains(crewTraceStates, t.State) &&
		(t.RequestedModel == "" || crewTraceLabel.MatchString(t.RequestedModel)) && (t.Model == "" || crewTraceLabel.MatchString(t.Model)) &&
		(t.Usage == "" || t.Usage == "reported" || t.Usage == "unknown") && len(t.NotApplied) <= 16 && len(t.Context) <= 32
	for _, x := range append(slices.Clone(t.NotApplied), t.Context...) {
		ok = ok && crewTraceLabel.MatchString(x)
	}
	return ok
}

var crewAssistRequestID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func crewAssistRecordID(scope Scope, principal, requestID string) string {
	return "assist-" + ContentHash(structuredText([]any{"crew-assist-request-v1", scope, principal, requestID}))
}

type CrewAssistInput struct {
	RequestID string     `json:"request_id,omitempty"`
	Scope     Scope      `json:"scope"`
	Assist    CrewAssist `json:"assist"`
}

type CrewAssistResult struct {
	Scope  Scope  `json:"scope"`
	Status string `json:"status"`
	Output string `json:"output,omitempty"`
	Model  string `json:"model,omitempty"`
	Error  string `json:"error,omitempty"`
	// Trace and status running: an external runner accepted the work and the answer comes later.
	Trace *CrewAssistTrace `json:"trace,omitempty"`
}

func validCrewAssist(a CrewAssist) bool {
	limit := crewAssistPrompt
	if a.Kind == "install_skill" {
		limit = 120512 // A 20,000-character body plus JSON escaping and target names.
	}
	return slices.Contains(crewAssistKinds, a.Kind) && strings.TrimSpace(a.Prompt) != "" && runes(a.Prompt) <= limit &&
		runes(a.Persona) <= 8000 && (a.Lang == "" || a.Lang == "ko" || a.Lang == "en") && (a.Kind == "try") == (a.Expert != "") && (a.Expert == "" || crewHandleRe.MatchString(strings.TrimPrefix(a.Expert, "@")))
}

// expired reads a request nobody answered within the TTL as failed, so the
// screen stops waiting when the runner is off. An accepted (running) one waits a day from its last report.
func expireAssist(r Record, now time.Time) Record {
	if a := r.CrewAssist; a != nil && a.Status == "pending" && now.Sub(r.CreatedAt) > crewAssistTTL {
		c := *a
		c.Status, c.Error = "failed", "expired: 서버의 모델 설정 실행기(arkism-providers)가 받지 않았어요"
		r.CrewAssist = &c
	} else if a != nil && a.Status == "running" && now.Sub(r.UpdatedAt) > crewAssistRunningTTL {
		c := *a
		c.Status, c.Error = "failed", "expired: 외부 실행기의 결과를 하루 동안 받지 못했어요 (작업은 그쪽에서 계속됐을 수 있어요)"
		r.CrewAssist = &c
	}
	return r
}

func (s *Service) CreateCrewAssist(ctx context.Context, token string, in CrewAssistInput) (Record, error) {
	// Model on create asks for that model (the runner falls back when it cannot run it); the runner reports the one that answered.
	in.Assist.Status, in.Assist.Output, in.Assist.Error, in.Assist.Trace = "pending", "", "", nil
	if !validScope(in.Scope) || in.RequestID != "" && !crewAssistRequestID.MatchString(in.RequestID) || !validCrewAssist(in.Assist) || in.Assist.Model != "" && !crewModelRe.MatchString(in.Assist.Model) {
		return Record{}, ErrInvalid
	}
	if err := noSecrets(in.Assist); err != nil {
		return Record{}, err
	}
	d, err := s.authorize(ctx, token, "write:"+colCrew, in.Scope, "", "")
	if err != nil {
		return Record{}, err
	}
	// The Discord host (service token) and a harness calling @expert (agent token) ask agents one read-only question
	// (try); every other kind is a person's.
	if d.Role != roleHuman && !((d.Role == roleService || d.Role == roleAgent) && in.Assist.Kind == "try") {
		return Record{}, ErrDenied
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	a := in.Assist
	r := Record{ID: ID(), Scope: in.Scope, Kind: colCrewAssists, Status: statusActive, Revision: 1, Text: a.Kind, CrewAssist: &a,
		CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role}
	if in.RequestID != "" {
		r.ID = crewAssistRecordID(in.Scope, d.PrincipalID, in.RequestID)
	}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if err := tx.Insert(ctx, colCrewAssists, r); err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "create:"+colCrewAssists, r.ID)
	})
	return r, err
}

func (s *Service) GetCrewAssist(ctx context.Context, token string, scope Scope, id string) (Record, error) {
	if !validScope(scope) || !validRunLabel(id, 256) {
		return Record{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colCrew, scope, id, ""); err != nil {
		return Record{}, err
	}
	r, err := s.Repo.Get(ctx, colCrewAssists, scope, id)
	if err != nil {
		return Record{}, err
	}
	if r.CrewAssist == nil {
		return Record{}, ErrNotFound
	}
	return expireAssist(r, time.Now()), nil
}

// Lookup is scoped to the authenticated creator; it never searches other people's inputs.
func (s *Service) GetCrewAssistByRequest(ctx context.Context, token string, scope Scope, requestID string) (Record, error) {
	if !validScope(scope) || !crewAssistRequestID.MatchString(requestID) {
		return Record{}, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "read:"+colCrew, scope, "", "")
	if err != nil {
		return Record{}, err
	}
	if d.Role != roleHuman {
		return Record{}, ErrDenied
	}
	r, err := s.Repo.Get(ctx, colCrewAssists, scope, crewAssistRecordID(scope, d.PrincipalID, requestID))
	if err != nil {
		return Record{}, err
	}
	if r.CrewAssist == nil || r.CreatedBy != d.PrincipalID || r.CreatedByType != roleHuman {
		return Record{}, ErrNotFound
	}
	return expireAssist(r, time.Now()), nil
}

// PendingCrewAssists is the runner's queue, oldest first; withRunning adds the ones an external runner accepted,
// so a restarted host runner asks that runner again instead of starting the work twice.
func (s *Service) PendingCrewAssists(ctx context.Context, token string, scope Scope, withRunning bool) (CrewRunPage, error) {
	if !validScope(scope) {
		return CrewRunPage{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colCrew, scope, "", ""); err != nil {
		return CrewRunPage{}, err
	}
	rows, err := s.Repo.ListRecords(ctx, scope, colCrewAssists, 200)
	if err != nil {
		return CrewRunPage{}, err
	}
	now := time.Now()
	out := CrewRunPage{Items: []Record{}}
	for i := len(rows) - 1; i >= 0; i-- {
		if r := expireAssist(rows[i], now); r.CrewAssist != nil && (r.CrewAssist.Status == "pending" || withRunning && r.CrewAssist.Status == "running") {
			out.Items = append(out.Items, r)
		}
	}
	return out, nil
}

// FinishCrewAssist records the runner's answer once. Status running (with a trace) is a progress report an external
// runner's accepted work may send any number of times before the answer.
func (s *Service) FinishCrewAssist(ctx context.Context, token, id string, in CrewAssistResult) (Record, error) {
	if !validScope(in.Scope) || !validRunLabel(id, 256) || in.Status == "pending" || !slices.Contains(crewAssistStates, in.Status) ||
		runes(in.Output) > 20000 || runes(in.Error) > 500 || in.Model != "" && !crewModelRe.MatchString(in.Model) ||
		!validCrewAssistTrace(in.Trace) || in.Status == "running" && (in.Trace == nil || in.Output != "") {
		return Record{}, ErrInvalid
	}
	if err := noSecrets(in); err != nil {
		return Record{}, err
	}
	d, err := s.authorize(ctx, token, "write:"+colCrewRuns, in.Scope, id, "")
	if err != nil {
		return Record{}, err
	}
	if d.Role != roleService && d.Role != roleAgent {
		return Record{}, ErrDenied
	}
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, colCrewAssists, in.Scope, id)
		if err != nil {
			return err
		}
		if r.CrewAssist == nil {
			return ErrNotFound
		}
		if st := expireAssist(r, time.Now()).CrewAssist.Status; st != "pending" && st != "running" || r.CrewAssist.Kind != "try" && in.Status == "running" {
			return ErrConflict
		}
		a := *r.CrewAssist
		a.Status, a.Output, a.Model, a.Error = in.Status, in.Output, in.Model, in.Error
		if in.Trace != nil {
			a.Trace = in.Trace
		}
		prev := r.Revision
		r.CrewAssist, r.Revision, r.UpdatedAt = &a, r.Revision+1, time.Now().UTC().Truncate(time.Millisecond)
		if err := tx.Replace(ctx, colCrewAssists, r, prev); err != nil {
			return err
		}
		out = r
		return audit(ctx, tx, in.Scope, d.PrincipalID, "finish:"+colCrewAssists, id)
	})
	return out, err
}

func crewAssistRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/crew/assists", func(w http.ResponseWriter, r *http.Request) {
		var in CrewAssistInput
		if !decodeLimit(w, r, &in, 1<<20) {
			return
		}
		out, e := s.CreateCrewAssist(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/crew/assists", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.PendingCrewAssists(r.Context(), bearer(r), scopeQuery(r), r.URL.Query().Get("include") == "running")
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/crew/assists/by-request/{request_id}", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.GetCrewAssistByRequest(r.Context(), bearer(r), scopeQuery(r), r.PathValue("request_id"))
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/crew/assists/{id}", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.GetCrewAssist(r.Context(), bearer(r), scopeQuery(r), r.PathValue("id"))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/crew/assists/{id}/result", func(w http.ResponseWriter, r *http.Request) {
		var in CrewAssistResult
		if !decodeLimit(w, r, &in, 128<<10) {
			return
		}
		out, e := s.FinishCrewAssist(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
}
