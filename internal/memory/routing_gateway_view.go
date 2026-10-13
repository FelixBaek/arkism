package memory

import (
	"sort"
	"time"
)

// The routing overview's Claude Code part (calls through the :8099 gateway): main conversation
// calls and side calls (fork, query, compaction, auxiliary, side_reply) counted apart, and the
// latest agent turns (session_id + turn_seq) with each call of the turn. Ids and numbers only.

const gatewayTurnsShown = 20

type gatewayCallStats struct {
	Requests     int            `json:"requests"`
	Failures     int            `json:"failures"`
	InputTokens  int64          `json:"input_tokens"`
	OutputTokens int64          `json:"output_tokens"`
	CostUSD      float64        `json:"cost_usd"`
	Estimated    bool           `json:"estimated,omitempty"`
	BySide       map[string]int `json:"by_side,omitempty"`
}

func (s *gatewayCallStats) add(c RoutingCall) {
	s.Requests++
	if !c.OK {
		s.Failures++
	}
	s.InputTokens += c.InputTokens
	s.OutputTokens += c.OutputTokens
	if c.CostUSD != nil {
		s.CostUSD += *c.CostUSD
	}
	s.Estimated = s.Estimated || c.Estimated
	if c.Origin.Side != "" {
		if s.BySide == nil {
			s.BySide = map[string]int{}
		}
		s.BySide[c.Origin.Side]++
	}
}

type gatewayTurnCall struct {
	At           time.Time `json:"at"`
	Model        string    `json:"model"`
	OK           bool      `json:"ok"`
	LatencyMS    int64     `json:"latency_ms"`
	InputTokens  int64     `json:"input_tokens,omitempty"`
	OutputTokens int64     `json:"output_tokens,omitempty"`
	CostUSD      *float64  `json:"cost_usd,omitempty"`
	Side         string    `json:"side,omitempty"`
	Error        string    `json:"error,omitempty"`
}

type gatewayTurn struct {
	SessionID string            `json:"session_id"`
	TurnSeq   int64             `json:"turn_seq"`
	At        time.Time         `json:"at"`
	Main      int               `json:"main_calls"`
	Side      int               `json:"side_calls"`
	Calls     []gatewayTurnCall `json:"calls"`
}

type gatewayOverview struct {
	Main  gatewayCallStats `json:"main"`
	Side  gatewayCallStats `json:"side"`
	Turns []gatewayTurn    `json:"turns"`
}

// gatewayView reads the overview's rows (newest first) from `from` on.
func gatewayView(rows []Record, from time.Time) gatewayOverview {
	out := gatewayOverview{Turns: []gatewayTurn{}}
	turns := map[[2]any]*gatewayTurn{}
	order := []*gatewayTurn{}
	for _, r := range rows {
		c := r.RoutingCall
		if c == nil || c.Origin == nil || (c.Origin.Kind != "gateway" && c.Origin.Kind != "side") || r.CreatedAt.Before(from) {
			continue
		}
		side := c.Origin.Kind == "side"
		if side {
			out.Side.add(*c)
		} else {
			out.Main.add(*c)
		}
		if c.Origin.SessionID == "" || c.Origin.TurnSeq < 1 {
			continue
		}
		key := [2]any{c.Origin.SessionID, c.Origin.TurnSeq}
		t := turns[key]
		if t == nil {
			if len(order) == gatewayTurnsShown {
				continue // older turns than the ones shown
			}
			t = &gatewayTurn{SessionID: c.Origin.SessionID, TurnSeq: c.Origin.TurnSeq, At: r.CreatedAt}
			turns[key] = t
			order = append(order, t)
		}
		if side {
			t.Side++
		} else {
			t.Main++
		}
		t.Calls = append(t.Calls, gatewayTurnCall{At: r.CreatedAt, Model: c.Model, OK: c.OK, LatencyMS: c.LatencyMS,
			InputTokens: c.InputTokens, OutputTokens: c.OutputTokens, CostUSD: c.CostUSD, Side: c.Origin.Side, Error: c.Error})
	}
	for _, t := range order {
		sort.SliceStable(t.Calls, func(i, j int) bool { return t.Calls[i].At.Before(t.Calls[j].At) }) // the turn as it ran
		out.Turns = append(out.Turns, *t)
	}
	return out
}
