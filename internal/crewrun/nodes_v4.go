package crewrun

// Node kinds of the Figma v4 graph editor.
//
//	expert  A saved expert (crew record id in "expert", type agent/persona/template) does the step. Works like ai,
//	        with the expert's model, effort and instructions as defaults; the node's own model, effort and
//	        instructions win (instructions add to the expert's).
//	team    A team meeting on a topic: team (team record id), topic (template, default "{{input}}"). Starts a
//	        meeting (trigger "workflow"), drives it here and outputs its conclusion
//	        {summary, decisions, todos, open_questions, meeting_id}. A meeting that waits for a person fails the node,
//	        and so does one that has no conclusion within timeout (default 1h).
//	n8n     Sends the input to an n8n webhook: url, mode (sync: the answer is the output; async: the request carries
//	        callback_url and the node waits until n8n posts the result there, at most timeout, default 30m, which
//	        needs ARKISM_CREW public URL), message (body template, default {"input": …}).

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

func (e *Engine) runExpert(ctx context.Context, x *runExec, n Node, input any) (result, error) {
	c := maps.Clone(n.Config)
	if c == nil {
		c = map[string]string{}
	}
	it, ok := e.item(strings.TrimSpace(c["expert"]))
	if !ok || it.Crew.Type != "agent" && it.Crew.Type != "persona" && it.Crew.Type != "template" {
		return result{}, failf("bad_config", "전문가 %s를 찾지 못했어요", c["expert"])
	}
	c["model"] = cmp.Or(c["model"], it.Crew.Model)
	c["effort"] = cmp.Or(c["effort"], it.Crew.Effort)
	c["instructions"] = joinText(it.Crew.Instructions, c["instructions"])
	delete(c, "persona")
	n.Config = c
	return e.runAI(ctx, x, n, input)
}

// MeetingCreator is the optional MeetingStore part that asks for a new meeting (Client implements it).
type MeetingCreator interface {
	CreateMeeting(ctx context.Context, teamID, title, topic string) (MeetingRecord, error)
}

// teamPoll is how often runTeam looks at a meeting the poller took over.
var teamPoll = 2 * time.Second

func (e *Engine) runTeam(ctx context.Context, c map[string]string, input any) (any, error) {
	mc, ok := e.Meetings.(MeetingCreator)
	if !ok {
		return nil, failf("bad_config", "이 실행기는 팀 회의를 만들 수 없어요")
	}
	team, ok := e.item(strings.TrimSpace(c["team"]))
	if !ok || team.Crew.Type != "team" {
		return nil, failf("bad_config", "팀 %s를 찾지 못했어요", c["team"])
	}
	topic := strings.TrimSpace(render(cmp.Or(c["topic"], "{{input}}"), input))
	if topic == "" {
		return nil, failf("bad_config", "회의 주제가 비었어요")
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, durationOr(c["timeout"], time.Hour))
	defer cancel()
	rec, err := mc.CreateMeeting(ctx, team.ID, clip(team.Crew.Name, 100), clip(topic, 20000))
	if err != nil {
		return nil, failf("team", "회의를 만들지 못했어요")
	}
	if err := e.DriveMeeting(ctx, rec.ID); err != nil {
		return nil, failf("team", "회의를 진행하지 못했어요: %s", clip(err.Error(), 200))
	}
	for {
		got, err := e.Meetings.GetMeeting(ctx, rec.ID)
		if err != nil {
			return nil, failf("team", "회의를 읽지 못했어요")
		}
		m := got.Meeting
		switch {
		case m.Status == "concluded" && m.Conclusion != nil:
			return map[string]any{"summary": m.Conclusion.Summary, "decisions": m.Conclusion.Decisions, "todos": m.Conclusion.Todos,
				"open_questions": m.Conclusion.OpenQuestions, "meeting_id": rec.ID}, nil
		case m.Status == "waiting":
			return nil, failf("team_waiting", "%s 회의가 사람의 결정을 기다리며 멈췄어요", team.Crew.Name)
		case terminalMeeting(m.Status):
			return nil, failf("team_failed", "%s 회의가 결론 없이 끝났어요 (%s)", team.Crew.Name, cmp.Or(m.EndedReason, m.Status))
		}
		select {
		case <-ctx.Done():
			if parent.Err() != nil {
				return nil, parent.Err()
			}
			return nil, failf("team_timeout", "%s 회의가 정해진 시간 안에 끝나지 않았어요", team.Crew.Name)
		case <-time.After(teamPoll):
		}
	}
}

// CreateMeeting asks Arkism for a meeting started by a workflow; the engine drives it.
func (c *Client) CreateMeeting(ctx context.Context, teamID, title, topic string) (MeetingRecord, error) {
	var out MeetingRecord
	err := c.do(ctx, "POST", "/v1/crew/meetings", map[string]any{"scope": c.Scope,
		"meeting": map[string]any{"team_id": teamID, "title": title, "topic": topic, "trigger": "workflow"}}, &out)
	return out, err
}

// n8nWaits holds the async n8n nodes by the SHA-256 of their one-time token until n8n posts the result to
// /callbacks/{token}; keying by the hash keeps the lookup from leaking the token through timing.
// shortcut: in memory only, so a restart settles the node as interrupted; keep it until n8n runs are long and common.
var n8nWaits sync.Map // sha256(token) → chan any

func (e *Engine) runN8N(ctx context.Context, c map[string]string, input any) (any, error) {
	u, err := url.Parse(strings.TrimSpace(render(c["url"], input)))
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, failf("bad_config", "n8n 웹훅 주소는 http(s)여야 해요")
	}
	async := c["mode"] == "async"
	limit := durationOr(c["timeout"], 30*time.Minute)
	body := map[string]any{"input": input}
	if t := strings.TrimSpace(c["message"]); t != "" {
		body = map[string]any{"input": render(t, input)}
	}
	var wait chan any
	if async {
		if e.PublicURL == "" {
			return nil, failf("bad_config", "결과를 받으려면 실행기의 공개 주소(PublicURL)가 필요해요")
		}
		b := make([]byte, 24)
		_, _ = rand.Read(b)
		token := hex.EncodeToString(b)
		wait = make(chan any, 1)
		key := tokenKey(token)
		n8nWaits.Store(key, wait)
		defer n8nWaits.Delete(key)
		body["callback_url"] = strings.TrimRight(e.PublicURL, "/") + "/callbacks/" + token
	}
	raw, _ := json.Marshal(body)
	rctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, "POST", u.String(), bytes.NewReader(raw))
	if err != nil {
		return nil, failf("bad_config", "bad url")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.httpClientFor(limit).Do(req)
	if err != nil {
		return nil, failf("n8n", "request to %s failed", u.Host)
	}
	defer resp.Body.Close()
	b, err := readLimited(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, failf("n8n_status", "%s answered %d", u.Host, resp.StatusCode)
	}
	if !async {
		return parseOutput(b), nil
	}
	select {
	case v := <-wait:
		return v, nil
	case <-rctx.Done():
		return nil, failf("n8n_timeout", "n8n이 정해진 시간 안에 결과를 보내지 않았어요")
	}
}

func tokenKey(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// n8nCallback delivers an async n8n result; the token is the secret.
func n8nCallback(w http.ResponseWriter, r *http.Request) {
	v, ok := n8nWaits.Load(tokenKey(r.PathValue("token")))
	if !ok {
		writeJSON(w, 404, map[string]string{"code": "not_found"})
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	select {
	case v.(chan any) <- body:
		writeJSON(w, 202, map[string]string{"ok": "true"})
	default:
		writeJSON(w, 409, map[string]string{"code": "conflict"})
	}
}
