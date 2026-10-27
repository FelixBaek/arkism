package discord

// Workflow result deliveries are owned by memoryd. The local bot state is not
// their source of truth: losing it must not lose a final answer or rerun work.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/FelixBaek/arkism/internal/flowresult"
	"github.com/FelixBaek/arkism/internal/host"
)

func (m *Manager) followDeliveries() {
	for _, id := range m.botIDs() {
		b := m.bots[id]
		if b.sess == nil {
			continue
		}
		m.followDeliveryState(b, "waiting_run")
		m.followDeliveryState(b, "")
	}
}

func (m *Manager) followDeliveryState(b *bot, status string) {
	id := b.cfg.ID
	cursor := ""
	for page := 0; page < 10; page++ {
		items, err := m.api.Get("/v1/crew/deliveries?" + scopeQuery("connector", "discord", "bot_id", id, "limit", "200", "after", cursor, "status", status))
		if err != nil {
			// An older server has no durable deliveries; legacy jobs still work.
			if host.HTTPCode(err) != 404 {
				m.logf("bot %s: delivery listing: %s", id, errText(err))
			}
			break
		}
		for _, item := range list(items["items"]) {
			if status == "waiting_run" {
				m.followFlowWait(b, obj(item))
			} else {
				m.deliverResult(b, obj(item))
			}
		}
		next := str(items["next_cursor"])
		if next == "" || next == cursor {
			break
		}
		cursor = next
	}
}

// Waiting jobs live on the server, so an approval notice is recoverable even
// when this host has never seen the invocation. Local markers only suppress
// repeated notices; final answers always remain owned by the durable outbox.
func (m *Manager) followFlowWait(b *bot, record map[string]any) {
	delivery := body(record, "delivery")
	origin := obj(delivery["origin"])
	id := str(delivery["run_id"])
	if id == "" || str(origin["connector"]) != "discord" || str(origin["bot_id"]) != b.cfg.ID {
		return
	}
	response, err := m.api.Get("/v1/crew/runs/" + url.PathEscape(id) + "?" + scopeQuery())
	if err != nil {
		return
	}
	run := body(response, "crew_run")
	waiting := obj(run["waiting"])
	kind, node := flowWaitKind(waiting), str(waiting["node_id"])
	index := m.flowWaitIndex(b.cfg.ID, id)
	if str(run["status"]) != "waiting" {
		if index >= 0 {
			m.st.Jobs[index].Asked = ""
		}
		return
	}
	if node == "" || (kind != "approval" && kind != "budget") || obj(run["decisions"])[node] != nil {
		return
	}
	revision := int64(num(response["revision"]))
	marker := flowWaitMarker(run, revision)
	if index >= 0 && m.st.Jobs[index].Asked == marker {
		return
	}
	if action, _ := m.deliveryAllowed(b, origin); action != "" {
		return
	}
	question := str(waiting["question"])
	if question == "" {
		question = "확인이 필요해요."
	}
	j := job{Kind: "flow_wait", ID: id, BotID: b.cfg.ID, ChannelID: str(origin["thread_id"]), Since: m.now()}
	if index >= 0 {
		j = m.st.Jobs[index]
	}
	out := Out{Content: "**FLOW** · " + question + "\n실행 ID: `" + id + "`"}
	if kind == "budget" {
		out.Buttons = budgetButtons(&j, node, revision)
	} else {
		out.Content += "\nArkism 앱의 FLOW에서 이 실행을 열어 승인해 주세요. 승인 후 이 대화에 최종 결과를 올립니다."
	}
	if !m.send(b, str(origin["thread_id"]), out) {
		return
	}
	if index < 0 {
		m.st.Jobs = append(m.st.Jobs, j)
		index = len(m.st.Jobs) - 1
	}
	j.Asked = marker
	m.st.Jobs[index] = j
}

func (m *Manager) flowWaitIndex(botID, runID string) int {
	for i, j := range m.st.Jobs {
		if j.Kind == "flow_wait" && j.ID == runID && j.BotID == botID {
			return i
		}
	}
	return -1
}

func (m *Manager) forgetFlowWait(botID, runID string) {
	kept := m.st.Jobs[:0]
	for _, j := range m.st.Jobs {
		if j.Kind != "flow_wait" || j.ID != runID || j.BotID != botID {
			kept = append(kept, j)
		}
	}
	m.st.Jobs = kept
}

// resultMessageV1 stays stable across retries: terminal run summaries are
// immutable and the title is not resolved from a mutable workflow definition.
func resultMessageV1(run map[string]any) (string, bool) {
	labels := map[string]string{"succeeded": "완료", "partial": "일부 완료", "failed": "실패", "stopped": "중단",
		"cancelled": "취소", "unknown": "결과 확인 필요"}
	label, ok := labels[str(run["status"])]
	if !ok {
		return "", false
	}
	raw, _ := json.Marshal(run)
	return "**워크플로** · " + label + "\n" + flowresult.Text(raw), true
}

func (m *Manager) deliverResult(b *bot, record map[string]any) {
	delivery := body(record, "delivery")
	origin := obj(delivery["origin"])
	if str(origin["connector"]) != "discord" || str(origin["bot_id"]) != b.cfg.ID || str(delivery["run_id"]) == "" {
		return
	}
	m.forgetFlowWait(b.cfg.ID, str(delivery["run_id"]))
	run, err := m.api.Get("/v1/crew/runs/" + url.PathEscape(str(delivery["run_id"])) + "?" + scopeQuery())
	if err != nil || deliveryRunRevision(run) != num(delivery["result_revision"]) {
		return
	}
	text, terminal := resultMessageV1(body(run, "crew_run"))
	if !terminal {
		return
	}
	token := randHex(16)
	claimFields := map[string]any{"result_hash": deliveryResultHash(text)}
	claimed, err := m.changeDelivery(record, token, "claim", claimFields)
	if err != nil {
		return
	}
	if str(body(claimed, "delivery")["status"]) != "sending" {
		return
	}
	record, delivery = claimed, body(claimed, "delivery")
	origin = obj(delivery["origin"])
	parts := deliveryParts(text)
	next := int(num(delivery["part"]))
	if next < 0 || next > len(parts) {
		_, _ = m.changeDelivery(record, token, "unknown", map[string]any{"error_code": "invalid_part"})
		return
	}
	for i := next; i < len(parts); i++ {
		if i != next {
			// An acknowledged part is pending again. Claim the next effect
			// separately so a restart between parts resumes without guessing.
			claimed, err = m.changeDelivery(record, token, "claim", claimFields)
			if err != nil || str(body(claimed, "delivery")["status"]) != "sending" {
				return
			}
			record = claimed
		}
		ack, ok := m.sendDeliveryPart(b, record, origin, token, parts[i], i)
		if !ok {
			return
		}
		record = ack
	}
	if _, err := m.changeDelivery(record, token, "complete", nil); err != nil {
		_, _ = m.changeDelivery(record, token, "complete", nil)
	}
}

func deliveryRunRevision(record map[string]any) float64 {
	if revision := num(body(record, "crew_run")["result_revision"]); revision > 0 {
		return revision
	}
	return num(record["revision"])
}

func (m *Manager) sendDeliveryPart(b *bot, record, origin map[string]any, token, text string, part int) (map[string]any, bool) {
	if action, code := m.deliveryAllowed(b, origin); action != "" {
		_, _ = m.changeDelivery(record, token, action, map[string]any{"error_code": code})
		return nil, false
	}
	delivery := body(record, "delivery")
	nonce := deliveryNonce(deliveryNonceIdentity(str(record["id"]), delivery), int64(num(delivery["result_revision"])), part)
	deadline, parseErr := time.Parse(time.RFC3339Nano, str(delivery["lease_until"]))
	if parseErr != nil || !deadline.After(time.Now()) {
		_, _ = m.changeDelivery(record, token, "unknown", map[string]any{"error_code": "send_lease_expired"})
		return nil, false
	}
	messageID, sendErr := b.sess.Send(str(origin["thread_id"]), Out{Content: text, Nonce: nonce, Deadline: deadline})
	if sendErr != nil || messageID == "" {
		action, code := deliveryFailure(sendErr)
		_, _ = m.changeDelivery(record, token, action, map[string]any{"error_code": code})
		return nil, false
	}
	fields := map[string]any{"part": part, "message_id": messageID}
	ack, ackErr := m.changeDelivery(record, token, "ack_part", fields)
	if ackErr != nil {
		// Acknowledgements are idempotent for this exact token/part/message.
		ack, ackErr = m.changeDelivery(record, token, "ack_part", fields)
	}
	if ackErr != nil {
		// No next part is sent. Expired sending becomes unknown on the server.
		return nil, false
	}
	delivery = body(ack, "delivery")
	// An expired lease or changed receipt is not authority to send another part.
	return ack, str(delivery["status"]) == "pending" && int(num(delivery["part"])) == part+1
}

func (m *Manager) changeDelivery(record map[string]any, token, action string, extra map[string]any) (map[string]any, error) {
	payload := map[string]any{"scope": host.Scope(), "expected_revision": int64(num(record["revision"])),
		"action": action, "lease_token": token}
	for key, value := range extra {
		payload[key] = value
	}
	return m.api.Post("/v1/crew/deliveries/"+url.PathEscape(str(record["id"]))+"/change", payload)
}

func deliveryNonce(id string, revision int64, part int) string {
	sum := sha256.Sum256([]byte(id + "/" + strconv.FormatInt(revision, 10) + "/" + strconv.Itoa(part)))
	return hex.EncodeToString(sum[:12]) // Discord accepts at most 25 characters.
}

// Only a human redirect creates a new send identity. An uncertain retry keeps
// the nonce so the connector can still suppress a recently accepted duplicate.
func deliveryNonceIdentity(id string, delivery map[string]any) string {
	generation := 0
	for i, item := range list(delivery["recovery_history"]) {
		if str(obj(item)["action"]) == "redirect" {
			generation = i + 1
		}
	}
	if generation == 0 {
		return id
	}
	origin := obj(delivery["origin"])
	raw, _ := json.Marshal([]any{id, "redirect", generation, origin["bot_id"], origin["guild_id"], origin["channel_id"], origin["thread_id"]})
	return string(raw)
}

func deliveryResultHash(text string) string {
	// The receipt index is meaningful only for this exact formatter and split
	// strategy. A host upgrade must not resume with a different part layout.
	sum := sha256.Sum256([]byte("discord-result-v1:utf16:1900\n" + text))
	return hex.EncodeToString(sum[:])
}

func deliveryParts(text string) []string {
	// Count supplementary characters as two units so emoji-heavy output also
	// stays under Discord's limit. Every character is preserved between parts.
	var out []string
	start, units := 0, 0
	for at, r := range text {
		size := utf16.RuneLen(r)
		if units+size > 1900 {
			out = append(out, text[start:at])
			start, units = at, 0
		}
		units += size
	}
	return append(out, text[start:])
}

func deliveryFailure(err error) (string, string) {
	switch code := restCode(err); {
	case code == 429:
		return "retry", "rate_limited"
	case code >= 400 && code < 500:
		return "failed", fmt.Sprintf("discord_%d", code)
	default:
		return "unknown", "send_uncertain"
	}
}

// A saved destination is not a permanent grant. Recheck channel configuration,
// guild membership and the bot allow-list before each result part.
func (m *Manager) deliveryAllowed(b *bot, origin map[string]any) (string, string) {
	guildID, channelID, userID := str(origin["guild_id"]), str(origin["thread_id"]), str(origin["user_id"])
	if guildID == "" || channelID == "" || userID == "" || !m.inGuild(guildID) {
		return "failed", "destination_unavailable"
	}
	ch, err := b.sess.Channel(channelID)
	if err != nil {
		return destinationFailure(err)
	}
	base := ch.ID
	if ch.Thread {
		base = ch.ParentID
	}
	configured := false
	for _, cfg := range b.cfg.Channels {
		configured = configured || cfg.ChannelID == base
	}
	if !configured || ch.GuildID != guildID || base != str(origin["channel_id"]) || strings.TrimSpace(channelID) != channelID {
		return "failed", "destination_unavailable"
	}
	roles, err := b.sess.MemberRoles(guildID, userID)
	if err != nil {
		return destinationFailure(err)
	}
	if !m.allowed(b, guildID, userID, roles) {
		return "failed", "requester_not_allowed"
	}
	return "", ""
}

func destinationFailure(err error) (string, string) {
	if code := restCode(err); code >= 400 && code < 500 && code != 429 {
		return "failed", "destination_unavailable"
	}
	// These are reads before the send boundary, so a transient lookup failure
	// can be retried without an uncertain external write.
	return "retry", "destination_check_unavailable"
}
