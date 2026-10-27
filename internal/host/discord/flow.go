package discord

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

func (m *Manager) requireFlowCapability(id string) error {
	capability, e := m.api.Get("/v1/flow/capabilities/" + url.PathEscape(id) + "?" + scopeQuery())
	if e != nil {
		return e
	}
	if capability["supported"] == true {
		return nil
	}
	var fields []string
	for _, v := range list(capability["unsupported"]) {
		if name := str(v); name != "" {
			fields = append(fields, name)
		}
	}
	if len(fields) > 0 {
		return errors.New("이 FLOW 실행 경로에서 지원하지 않는 설정: " + strings.Join(fields, ", "))
	}
	return errors.New("이 작업은 아직 공통 FLOW 실행을 지원하지 않아요")
}

func flowWaitKind(waiting map[string]any) string {
	kind := str(waiting["kind"])
	if kind == "child" {
		return str(waiting["decision_kind"])
	}
	return kind
}

func flowWaitMarker(run map[string]any, revision int64) string {
	w := obj(run["waiting"])
	if flowWaitKind(w) != "budget" {
		revision = 0
	}
	raw, _ := json.Marshal([]any{w, obj(run["nodes"])[str(w["node_id"])], revision})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Compact IDs stay below Discord's 100-character ceiling even for 69-character
// run IDs. The local receipt binds the displayed parent revision and node.
func budgetButtons(j *job, node string, revision int64) []Button {
	if revision < 1 || node == "" {
		return nil
	}
	raw, _ := json.Marshal([]any{j.BotID, j.ChannelID, j.ID, node, revision})
	sum := sha256.Sum256(raw)
	j.DecisionKey = hex.EncodeToString(sum[:16])
	j.DecisionNode, j.DecisionRevision = node, revision
	prefix := "ark:b:" + j.DecisionKey + ":" + strconv.FormatInt(revision, 10)
	return []Button{{ID: prefix + ":y", Label: "계속"}, {ID: prefix + ":n", Label: "멈춤", Danger: true}}
}

func (m *Manager) pressBudget(botID, channelID, key, rawRevision string, approve bool) error {
	b := m.bots[botID]
	if b == nil || b.sess == nil {
		return errors.New("연결된 봇을 찾지 못했어요")
	}
	if _, cfg := m.where(b, channelID); cfg == nil {
		return errors.New("이 채널은 더 이상 Arkism에 연결되어 있지 않아요")
	}
	revision, e := strconv.ParseInt(rawRevision, 10, 64)
	if e != nil || revision < 1 {
		return errors.New("오래된 승인 요청이에요")
	}
	for i := range m.st.Jobs {
		j := &m.st.Jobs[i]
		if j.DecisionKey != key || j.DecisionRevision != revision || j.BotID != botID || j.ChannelID != channelID {
			continue
		}
		if j.Kind != "run" && j.Kind != "flow_wait" {
			continue
		}
		if e = m.decideRun(j.ID, j.DecisionNode, revision, approve); e != nil {
			return e
		}
		j.DecisionKey = ""
		return nil
	}
	return errors.New("이 대화의 승인 요청이 아니거나 이미 처리했어요")
}

func (m *Manager) followRunWait(b *bot, j *job, rec map[string]any) {
	run := body(rec, "crew_run")
	w := obj(run["waiting"])
	kind, node := flowWaitKind(w), str(w["node_id"])
	if node == "" || (kind != "approval" && kind != "budget") || obj(run["decisions"])[node] != nil {
		return
	}
	revision := int64(num(rec["revision"]))
	marker := flowWaitMarker(run, revision)
	if j.Asked == marker {
		return
	}
	question := str(w["question"])
	if question == "" {
		question = "확인이 필요해요."
	}
	out := Out{Content: "**" + j.Who + "** · " + question}
	pending := *j
	if kind == "budget" {
		out.Buttons = budgetButtons(&pending, node, revision)
	} else {
		out.Content += "\nArkism 앱의 FLOW에서 해당 실행을 열어 승인해 주세요."
	}
	if m.send(b, j.ChannelID, out) {
		pending.Asked = marker
		*j = pending
	}
}
