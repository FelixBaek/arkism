package memory

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"time"
)

// AgentCall is one call a work agent answered directly (not as a step of a
// workflow run): the 기록 tab of an expert lists these next to its runs.
type AgentCall struct {
	At           time.Time `json:"at"`
	Model        string    `json:"model"`
	OK           bool      `json:"ok"`
	LatencyMS    int64     `json:"latency_ms,omitempty"`
	InputTokens  int64     `json:"input_tokens,omitempty"`
	OutputTokens int64     `json:"output_tokens,omitempty"`
	CostUSD      float64   `json:"cost_usd"`
	Estimated    bool      `json:"estimated,omitempty"`
	Error        string    `json:"error,omitempty"`
	Trigger      string    `json:"trigger,omitempty"`
}

type AgentCalls struct {
	Items []AgentCall `json:"items"`
}

// CrewAgentCalls lists an agent's direct calls, newest first (at most limit, 1-100).
func (s *Service) CrewAgentCalls(ctx context.Context, token string, scope Scope, agentID string, limit int) (AgentCalls, error) {
	if !validScope(scope) || !validRunLabel(agentID, 256) || limit < 1 || limit > 100 {
		return AgentCalls{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colCrew, scope, "", ""); err != nil {
		return AgentCalls{}, err
	}
	rows, err := s.Repo.ListRecords(ctx, scope, colRoutingCalls, routingCallsRead)
	if err != nil {
		return AgentCalls{}, err
	}
	out := AgentCalls{Items: []AgentCall{}}
	for _, r := range rows {
		c := r.RoutingCall
		if c == nil || c.Origin == nil || c.Origin.AgentID != agentID || c.Origin.RunID != "" || callKind(c.Origin) != "agent" {
			continue
		}
		cost, est := callCost(*c)
		out.Items = append(out.Items, AgentCall{At: r.CreatedAt, Model: c.Model, OK: c.OK, LatencyMS: c.LatencyMS, InputTokens: c.InputTokens,
			OutputTokens: c.OutputTokens, CostUSD: roundUSD(cost), Estimated: est, Error: c.Error, Trigger: c.Origin.Trigger})
	}
	slices.SortStableFunc(out.Items, func(a, b AgentCall) int { return b.At.Compare(a.At) })
	out.Items = out.Items[:min(len(out.Items), limit)]
	return out, nil
}

func crewAgentCallRoutes(mux *http.ServeMux, s *Service) {
	mux.HandleFunc("GET /v1/crew/agents/{id}/calls", func(w http.ResponseWriter, r *http.Request) {
		limit := 30
		if v := r.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				respond(w, nil, ErrInvalid)
				return
			}
			limit = n
		}
		out, e := s.CrewAgentCalls(r.Context(), bearer(r), scopeQuery(r), r.PathValue("id"), limit)
		respond(w, out, e)
	})
}
