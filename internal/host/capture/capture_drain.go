package capture

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// ---------------------------------------------------------------- extraction

const extractPrompt = `You extract durable memory claims from a user's own messages to an AI coding agent.
Return JSON only: {"claims":[{"subject":"...","text":"...","kind":"user|decision|procedural|lesson","quote":"...","reason":"...","confidence":0.0,"supersedes_id":null,"risk":"high|medium|low","risk_reasons":["..."],"dup_hint":""}]}
Rules:
- Keep ONLY what the USER explicitly stated that should still hold in future sessions: lasting preferences, standing
  instructions ("앞으로/항상/하지 마"), project goals, decisions, facts about their own systems, corrections of facts.
- Ignore one-off task requests ("고쳐줘", "처리해", "해줘"), questions, complaints, chit-chat, temporary ideas, and anything
  only the assistant said. Do not infer habits or guess intent.
  Task-specific permissions, spending limits, temporary research goals and corrections of your current assignment
  are not standing rules. Quoted proposals and hypothetical examples are not adopted decisions.
  A pasted draft of a policy, world or character is reference material, not evidence of the user's adopted decision.
  Keep supported standing project rules from mixed turns and omit only their temporary task requests.
  A commit-size constraint is a procedural rule, not a spending permission. Keep explicit lasting dislikes and
  ongoing system/account facts, including a stated monthly API grant; retain any tentative wording in text.
- text: one standalone Korean sentence, faithful to the user's meaning, understandable without this chat. Add nothing.
  It must name what it is about (the product, project, screen or tool). A rule whose subject only this chat knows is not a claim.
  Retain the explicitly named project in every extracted rule; never turn a project-specific rule into a global one.
- Record the content itself, not the user's action: not "the user asked for X" but what X is.
- Only the user's words count: never extract something the assistant said, proposed or restated, even if the user agreed with "응".
- A change keeps both sides: "<what> uses Y instead of X". Turn relative dates (today, next week) into absolute ones using TODAY.
- Preserve every condition, exception, negation, uncertainty and stated change-effective time in text itself.
  Do not turn a conditional or tentative statement into an unconditional rule. Do not invent a date, domain or certainty.
- memory_context is optional and normally null. Supply it only for material applicability, uncertainty or a stated
  change-effective time. Its fields are domains (personal and/or work, only when supported), applicability,
  uncertainty, valid_from and semantic_kind (fact|preference|correction|lesson|failure_recovery).
  If the domain is unknown, use null and keep all qualifiers in text. A domain restriction (personal-only/work-only)
  is a real applicability condition: preserve it in text and applicability, not just in the domains label.
   applicability, uncertainty and valid_from must each be an exact contiguous quotation from the SAME user message,
   also contained in quote. Copy the original wording, spelling and spacing; do not paraphrase or normalize these
   fields, even dates (copy the user's date phrase). Use empty strings for unstated qualifiers.
   Include enough of that message in quote to cover every nonempty context field within quote's first 2000 characters.
   If one such quotation cannot support the context, use null and preserve all qualifiers in text.
   Classification labels alone do not need memory_context. Never treat future valid_from as already active.
  Never supply evidence_kind or observed_at: those are not model declarations.
- quote: an exact contiguous substring copied from the user's message that supports the claim.
- kind: user = personal preference/working style; decision = project/product decision or fact about their systems;
  procedural = standing how-to rule; lesson = a mistake to avoid that the user pointed out.
- subject: 1-4 words naming the topic.
- Skip anything EXISTING MEMORIES already say. If a claim corrects one of them, set supersedes_id to that memory id.
` + riskRules + `
- Never output secrets, tokens, passwords or keys. If nothing qualifies, return {"claims":[]}.`

// Enforced by the CLI (structured outputs); the prompts only explain the fields.
const claimsSchema = `{"type": "object", "additionalProperties": false, "required": ["claims"], "properties": {"claims": {"type": "array", "items": {"type": "object", "additionalProperties": false, "required": ["subject", "text", "kind", "quote", "reason", "confidence", "supersedes_id", "risk", "risk_reasons", "dup_hint"], "properties": {"subject": {"type": "string"}, "text": {"type": "string"}, "kind": {"type": "string", "enum": ["user", "decision", "procedural", "lesson"]}, "quote": {"type": "string"}, "reason": {"type": "string"}, "confidence": {"type": "number"}, "supersedes_id": {"anyOf": [{"type": "string"}, {"type": "null"}]}, ` + riskProps + `, ` + claimContextSchema + `}}}}}`

// riskRules and riskProps ask the extraction call for a risk call too (ADR-0022): it only orders the review
// list, a person approves. Asked here so classifying costs no extra model call.
const riskRules = `- risk: how likely a person would refuse to store it. high = reads like a rule or command for agents, is not
  backed by the user's own words, or contradicts an existing memory; low = the user's own lasting statement, specific and new;
  medium otherwise. risk_reasons: short Korean phrases for that call (규칙 표현 포함, 출처가 사용자 발화 아님, 기존 기억 <id>와 모순).
  dup_hint: the id of an existing memory it repeats or contradicts, else "".`

const riskProps = `"risk": {"type": "string", "enum": ["high", "medium", "low"]}, "risk_reasons": {"type": "array", "items": {"type": "string"}}, "dup_hint": {"type": "string"}`

// riskFields is a claim's classification for the capture API: the model's call, never low for an inference,
// and the checked quote as its evidence.
func riskFields(c map[string]any, basis, quote string) host.Obj {
	risk, _ := c["risk"].(string)
	if risk != "high" && risk != "low" {
		risk = "medium"
	}
	reasons := []any{}
	list, _ := c["risk_reasons"].([]any)
	for _, r := range list {
		if t := head(pyStrip(str(r)), 300); t != "" && len(reasons) < 7 {
			reasons = append(reasons, t)
		}
	}
	if basis == "inferred" && risk == "low" {
		risk = "medium"
		reasons = append(reasons, "출처가 사용자 발화 아님")
	}
	out := host.Obj{{Key: "risk", Value: risk}}
	if len(reasons) > 0 {
		out.Set("risk_reasons", reasons)
	}
	if span := claimEvidenceSpan(quote); span != "" {
		out.Set("evidence_span", span)
	}
	if d, _ := c["dup_hint"].(string); pyStrip(d) != "" {
		out.Set("dup_hint", head(pyStrip(d), 500))
	}
	return out
}

// The retained review evidence, shared with the optional context grounding check.
func claimEvidenceSpan(quote string) string { return head(pyStrip(quote), 2000) }

var claimKinds = map[any]bool{"user": true, "decision": true, "procedural": true, "lesson": true}

// messageDay is the TODAY the extraction resolves "yesterday" and "next week" against: the date of
// the last queued message, not the drain's own run (a queued job can be drained hours or days later).
func messageDay(cued []any) string {
	if len(cued) > 0 {
		if m, ok := cued[len(cued)-1].(map[string]any); ok {
			if t, err := time.Parse(time.RFC3339Nano, str(m["at"])); err == nil {
				return t.Format("2006-01-02")
			}
		}
	}
	return host.NowISO()[:10]
}

// norm is re.sub(r"\s+", " ", s).strip().lower().
func norm(s string) string { return strings.ToLower(strings.Join(pyFields(s), " ")) }

// asList is a JSON list, or TypeError as Python iteration would raise.
func asList(v any) ([]any, error) {
	l, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: not a list", errType)
	}
	return l, nil
}

// asDict is a JSON object, or AttributeError as Python .get would raise.
func asDict(v any) (map[string]any, error) {
	// Evidence preparation hands back ordered objects; read them like dicts.
	if o, ordered := v.(host.Obj); ordered {
		m := make(map[string]any, len(o))
		for _, kv := range o {
			m[kv.Key] = kv.Value
		}
		return m, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: has no attribute 'get'", errAttr)
	}
	return m, nil
}

// claimQuoteMatches keeps message boundaries and the original spelling intact.
// Older user-only rows omit role; an explicit non-user role is not evidence.
func claimQuoteMatches(quote string, users []any) ([]map[string]any, error) {
	var matches []map[string]any
	for _, u := range users {
		um, err := asDict(u)
		if err != nil {
			return nil, err
		}
		if role, present := um["role"]; present && role != "user" {
			continue
		}
		text, isString := um["text"].(string)
		if pyStrip(quote) != "" && isString && strings.Contains(text, quote) {
			matches = append(matches, um)
		}
	}
	return matches, nil
}

// buildClaims checks the model's claims: a claim whose quote is not the user's literal words is
// only an inference, and a unique literal quote links its one source record.
func buildClaims(raw []any, userText string, related []map[string]any, users []any) ([]any, error) {
	claims, _, err := buildClaimsWithReceipt(raw, userText, related, users)
	return claims, err
}

func buildClaim(c map[string]any, text, userText string, related []map[string]any, users []any) (map[string]any, error) {
	quote := str(or(c["quote"], ""))
	kind := c["kind"]
	if !claimKinds[hashable(kind)] {
		kind = "decision"
	}
	matches, err := claimQuoteMatches(quote, users)
	if err != nil {
		return nil, err
	}
	basis := "inferred"
	// Aggregate-only legacy callers cannot establish message boundaries.
	legacyExact := users == nil && pyStrip(quote) != "" && strings.Contains(userText, quote)
	if len(matches) > 0 || legacyExact {
		basis = "explicit_user"
	}
	claim := map[string]any{"subject": head(or(pyStrip(str(or(c["subject"], "general"))), "general").(string), 120),
		"text": head(text, 4000), "kind": kind, "basis": basis, "reason": head(str(or(c["reason"], "")), 500)}
	for _, f := range riskFields(c, basis, quote) {
		claim[f.Key] = f.Value
	}
	switch conf := c["confidence"].(type) {
	case bool:
		claim["confidence"] = map[bool]float64{true: 1, false: 0}[conf]
	case json.Number, float64, int, int64:
		f, _ := num(conf)
		claim["confidence"] = min(max(f, 0), 1)
	}
	var sup map[string]any
	for _, r := range related { // {r["id"]: r} keeps the last row per id
		if id := hashable(c["supersedes_id"]); id != nil && eq(r["id"], id) {
			sup = r
		}
	}
	if sup != nil {
		claim["supersedes"] = host.Obj{{Key: "collection", Value: "memories"}, {Key: "id", Value: sup["id"]}, {Key: "revision", Value: sup["revision"]}}
	}
	// A unique literal quote identifies one known input record; no similarity matching.
	if basis == "explicit_user" && len(matches) == 1 {
		if truthy(matches[0]["archive_refs"]) {
			claim["archive_messages"] = matches[0]["archive_refs"]
		} else if truthy(matches[0]["archive_ref"]) {
			claim["archive_messages"] = []any{matches[0]["archive_ref"]}
		}
	}
	for k, v := range claim {
		if v == nil {
			delete(claim, k)
		}
	}
	return claim, nil
}

// hashable is v when Python could use it as a dict key, else nil.
func hashable(v any) any {
	switch v.(type) {
	case map[string]any, []any, host.Obj:
		return nil
	}
	return v
}

// sendCapture posts the claims of one event and logs each item's outcome.
func sendCapture(eventType, eventID, sid string, occurredAt any, claims []any, origin string, started float64, gen host.Obj) ([]any, error) {
	if err := privateCapturePolicy("capture_policy_wait"); err != nil {
		return nil, err
	}
	if started == 0 {
		started = monotonic()
	}
	body := host.Obj{{Key: "scope", Value: capScope()}, {Key: "generator", Value: gen}, {Key: "claims", Value: claims}}
	if sessionProcessorOn() { // the server accepts automated capture only from a service role
		body = append(body, host.KV{Key: "automated", Value: true})
	}
	body = append(body, host.KV{Key: "event", Value: host.Obj{{Key: "id", Value: eventID}, {Key: "type", Value: eventType}, {Key: "origin_product", Value: origin},
		{Key: "session_id", Value: head(sid, 120)}, {Key: "occurred_at", Value: occurredAt}}})
	if privatePayload(body) {
		return nil, sessionWait("capture_private_content")
	}
	res, err := capPost("/v1/capture", body, 60*time.Second)
	if err != nil {
		return nil, err
	}
	raw, has := res["items"]
	if !has {
		return nil, fmt.Errorf("%w: 'items'", errKey)
	}
	items, err := asList(raw)
	if err != nil {
		return nil, err
	}
	for _, v := range items {
		item, err := asDict(v)
		if err != nil {
			return nil, err
		}
		metric(kv("event", eventType), kv("session", head(sid, 12)), kv("status", item["status"]), kv("reason", item["reason"]),
			kv("candidate_id", or(item["candidate_id"], item["memory_id"])), kv("latency_ms", int((monotonic()-started)*1000)))
	}
	return items, nil
}

var captureOrigins = map[string]bool{"claude_code": true, "codex": true, "zcode": true, "gemini": true, "grok": true, "deepseek": true, "langgraph": true}

// queuedOrigin: a shared spool must retain the producer, independent of its next drainer.
func queuedOrigin(job map[string]any) (string, error) {
	markers := []any{job["origin"]}
	for _, field := range []string{"archive_locator", "archive_context"} {
		if evidence, has := job[field]; has && evidence != nil {
			m, ok := evidence.(map[string]any)
			if !ok {
				return "", runtimeErr("capture_origin_invalid")
			}
			markers = append(markers, m["origin"])
		}
	}
	event, isStr := or(job["event_id"], "").(string)
	if v, has := job["event_id"]; has && v != nil {
		event, isStr = v.(string)
	}
	if !isStr {
		return "", runtimeErr("capture_origin_invalid")
	}
	prefix, _, _ := strings.Cut(event, ":")
	if prefix = strings.ReplaceAll(prefix, "-", "_"); captureOrigins[prefix] {
		markers = append(markers, prefix)
	}
	seen := map[string]bool{}
	var first string
	for _, v := range markers {
		if v == nil {
			continue
		}
		s, ok := v.(string)
		if !ok || !captureOrigins[s] {
			return "", runtimeErr("capture_origin_invalid")
		}
		if len(seen) == 0 {
			first = s
		}
		seen[s] = true
	}
	if len(seen) > 1 {
		return "", runtimeErr("capture_origin_mismatch")
	}
	if first == "" {
		return capOrigin, nil
	}
	return first, nil
}

var retryableReasons = map[any]bool{"dependency_unavailable": true, "revision_conflict": true, "internal_error": true}

// process extracts and sends one spooled job; persist checkpoints the job before each
// at-least-once step so a retry resends the exact request.
func process(job map[string]any, persist func() error) (err error) {
	defer recovered(&err)
	checkPolicy := func() error { return privateCapturePolicy("capture_policy_wait", str(or(job["cwd"], ""))) }
	if err := checkPolicy(); err != nil {
		return err
	}
	if persist != nil {
		checkpoint := persist
		persist = func() error {
			if err := checkPolicy(); err != nil {
				return err
			}
			return checkpoint()
		}
	}
	if privatePrepared(job, "prepared_capture") || privatePrepared(job, "session_prepared") {
		return sessionWait("capture_private_prepared")
	}
	origin, err := queuedOrigin(job)
	if err != nil {
		return err
	}
	rawUsers, has := job["users"]
	if !has {
		return fmt.Errorf("%w: 'users'", errKey)
	}
	users, err := asList(rawUsers)
	if err != nil {
		return err
	}
	allInternal, err := allCodexInternal(origin, users)
	if err != nil {
		return err
	}
	if allInternal {
		metric(kv("event", "capture_skipped"), kv("reason", "codex_ambient_prompt"))
		return nil
	}
	if sessionProcessorOn() {
		return sessionProcess(job, persist, origin)
	}
	if users, err = sanitizeLegacyUsers(job, users, persist); err != nil {
		return err
	}
	started := monotonic()
	sid := job["session_id"].(string) // KeyError/TypeError surface through recovered
	cued := cuedUsers(users)
	checkpoint := persist
	if checkpoint == nil {
		checkpoint = func() error { return nil }
	}
	if len(cued) > 0 && !truthy(job["captured"]) {
		if err := captureCued(job, cued, sid, origin, started, checkPolicy, checkpoint); err != nil {
			return err
		}
	}
	if len(cued) == 0 && len(users) > 0 {
		metric(kv("event", job["type"]), kv("session", head(sid, 12)), kv("status", "skipped_no_cue"), kv("turns", len(users)))
	}
	return nil
}

// allCodexInternal reports whether every queued turn is a Codex ambient prompt (false for no turns).
func allCodexInternal(origin string, users []any) (bool, error) {
	allInternal := len(users) > 0
	for _, u := range users {
		um, err := asDict(u)
		if err != nil {
			return false, err
		}
		if !internalCodexPrompt(origin, um["text"]) {
			allInternal = false
			break
		}
	}
	return allInternal, nil
}

// sanitizeLegacyUsers redacts the turns of an old spool file that may predate redaction.
// Sanitize before cue matching, searching or extraction, and never use hashes derived from private text.
func sanitizeLegacyUsers(job map[string]any, users []any, persist func() error) ([]any, error) {
	if !privatePayload(users) {
		return users, nil
	}
	users, _ = privateData(users).([]any)
	if privatePayload(users) {
		return nil, sessionWait("capture_private_content")
	}
	for _, user := range users {
		if row, ok := user.(map[string]any); ok {
			delete(row, "archive_hint")
			delete(row, "archive_ref")
			delete(row, "archive_refs")
		}
	}
	job["users"] = users
	delete(job, "archive_locator")
	if persist != nil {
		if err := persist(); err != nil {
			return nil, err
		}
	}
	return users, nil
}

// cuedUsers is the turns whose text carries a memory cue; a malformed turn panics into recovered.
func cuedUsers(users []any) []any {
	var cued []any
	for _, u := range users {
		if cueRE.MatchString(u.(map[string]any)["text"].(string)) {
			cued = append(cued, u)
		}
	}
	return cued
}

// captureCued prepares the cued turns' claims once, then delivers them at least once.
func captureCued(job map[string]any, cued []any, sid, origin string, started float64, checkPolicy, checkpoint func() error) error {
	prepared, _ := job["prepared_capture"].(map[string]any)
	if job["prepared_capture"] == nil {
		var err error
		if prepared, err = prepareCapture(job, cued, sid, origin, checkPolicy, checkpoint); err != nil {
			return err
		}
	}
	if err := retainClaimContextReceipt(job, prepared, cued); err != nil {
		return err
	}
	claims := prepared["claims"].([]any)
	eventType := prepared["event_type"].(string)
	if len(claims) > 0 {
		if err := checkPolicy(); err != nil {
			return err
		}
		gen, err := preparedClaimGenerator(prepared, strings.ReplaceAll(origin, "_", "-")+"-hook-v1")
		if err != nil {
			return err
		}
		items, err := sendCapture(eventType, job["event_id"].(string), sid, prepared["occurred_at"], claims, origin, started, gen)
		if err != nil {
			return err
		}
		if len(items) != len(claims) {
			return runtimeErr("capture_response_invalid")
		}
		merged, retry, err := mergeCaptureItems(job, items, claims)
		if err != nil {
			return err
		}
		job["capture_items"] = merged
		if err := checkpoint(); err != nil {
			return err
		}
		if retry {
			return runtimeErr("capture_retryable")
		}
		job["captured"], job["session_candidates"] = true, len(claims)
		if err := checkpoint(); err != nil {
			return err
		}
	} else {
		status := "no_claims"
		if prepared["context_rejections"] != nil {
			status = "context_withheld"
		}
		metric(kv("event", eventType), kv("session", head(sid, 12)), kv("status", status))
		if status == "context_withheld" {
			return runtimeErr("capture_context_all_withheld")
		}
	}
	job["captured"] = true
	return nil
}

// prepareCapture extracts the cued turns' claims and checkpoints the exact request before delivery.
func prepareCapture(job map[string]any, cued []any, sid, origin string, checkPolicy, checkpoint func() error) (map[string]any, error) {
	linked, err := linkedUsers(job, cued, sid, origin)
	if err != nil {
		return nil, err
	}
	texts := make([]string, len(cued))
	for i, u := range cued {
		texts[i] = u.(map[string]any)["text"].(string)
	}
	userText := strings.Join(texts, "\n")
	eventType := captureEventType(userText)
	if err := checkPolicy(); err != nil {
		return nil, err
	}
	found, _, err := search(head(userText, 2000), host.ProjectKey(str(or(job["cwd"], ""))), 5, 60)
	if err != nil {
		return nil, err
	}
	related, existing, err := relatedMemories(found)
	if err != nil {
		return nil, err
	}
	out, err := privateModel(redactPrivate(withGuidance(extractPrompt)), fmt.Sprintf("TODAY: %s\n\nEXISTING MEMORIES:\n%s\n\nUSER MESSAGES:\n%s", messageDay(cued), existing, userText),
		capModel, 120*time.Second, claimsSchema, "claims", str(or(job["cwd"], "")))
	if err != nil {
		return nil, err
	}
	raw, err := asList(out)
	if err != nil {
		return nil, err
	}
	claims, contextReceipt, err := buildClaimsWithReceipt(raw, userText, related, linked)
	if err != nil {
		return nil, err
	}
	tagClaimRepo(job, claims)
	prepared := map[string]any{"event_type": eventType, "occurred_at": cued[len(cued)-1].(map[string]any)["at"], "claims": claims, "generator": extractionGenerator(strings.ReplaceAll(origin, "_", "-") + "-hook-v1")}
	if contextReceipt != nil {
		prepared["context_rejections"], err = bindClaimContextReceipt(contextReceipt, job["event_id"], cued)
		if err != nil {
			return nil, err
		}
	}
	if privatePayload(prepared) {
		return nil, sessionWait("capture_private_prepared")
	}
	job["prepared_capture"] = prepared
	if err := checkpoint(); err != nil { // persist the exact request before at-least-once delivery
		return nil, err
	}
	return prepared, nil
}

// linkedUsers links the cued turns to their archive records, through the job's archive locator when it has one.
func linkedUsers(job map[string]any, cued []any, sid, origin string) ([]any, error) {
	var err error
	context, sourceUsers, root := job["archive_context"], cued, capProjects
	if locator, _ := job["archive_locator"].(map[string]any); truthy(job["archive_locator"]) {
		if len(cued) != 1 || locator["session_id"] != sid || locator["origin"] != origin || locator["device"] != client() {
			return nil, runtimeErr("archive_locator_invalid")
		}
		if locator["origin"] == "zcode" {
			source := capZcodeDB
			if locator["device"] == "pc" {
				source = filepath.Join(archivePC, "zcode-sessions")
			}
			if sourceUsers, err = evPrepareZcode(locator, cued, source); err != nil {
				return nil, err
			}
		} else {
			var hint any
			if context, hint, root, err = evLocatePrompt(locator); err != nil {
				return nil, err
			}
			first := map[string]any{}
			for k, v := range cued[0].(map[string]any) {
				first[k] = v
			}
			first["archive_hint"] = hint
			sourceUsers = []any{first}
		}
	}
	return evPrepareUsers(context, sourceUsers, root)
}

// captureEventType names the capture event the user's own words call for.
func captureEventType(userText string) string {
	if rememberRE.MatchString(userText) {
		return "explicit_remember"
	} else if correctionRE.MatchString(userText) {
		return "user_corrected_fact"
	}
	return "turn_completed"
}

// relatedMemories reads the search hits and renders them as the prompt's EXISTING MEMORIES.
func relatedMemories(found []any) ([]map[string]any, string, error) {
	related := make([]map[string]any, len(found))
	lines := make([]string, len(found))
	for i, r := range found {
		var err error
		if related[i], err = asDict(r); err != nil {
			return nil, "", err
		}
		lines[i] = fmt.Sprintf("%s: %s", str(related[i]["id"]), redactPrivate(str(or(related[i]["text"], ""))))
		if _, has := related[i]["text"]; !has {
			lines[i] = fmt.Sprintf("%s: ", str(related[i]["id"]))
		}
	}
	existing := strings.Join(lines, "\n")
	if existing == "" {
		existing = "(none)"
	}
	return related, existing, nil
}

// tagClaimRepo tags every non-user claim with the job's repository when repo tags are on.
func tagClaimRepo(job map[string]any, claims []any) {
	if repo := repoKey(str(or(job["cwd"], ""))); repo != "" && repoTagsOn() {
		for _, c := range claims {
			if m, ok := c.(map[string]any); ok && m["kind"] != "user" {
				m["repo"] = repo
			}
		}
	}
}

// mergeCaptureItems keeps a merged item's earlier candidate receipt and reports whether any rejection is retryable.
func mergeCaptureItems(job map[string]any, items, claims []any) ([]any, bool, error) {
	previous, hasPrev := job["capture_items"]
	if !hasPrev {
		blank := make([]any, len(claims))
		for i := range blank {
			blank[i] = map[string]any{}
		}
		previous = blank
	}
	prev, isList := previous.([]any)
	if !isList || len(prev) != len(claims) {
		return nil, false, runtimeErr("capture_receipt_invalid")
	}
	for _, p := range prev {
		if _, ok := p.(map[string]any); !ok {
			return nil, false, runtimeErr("capture_receipt_invalid")
		}
	}
	merged := make([]any, len(items))
	retry := false
	for i, v := range items {
		item, old := v.(map[string]any), prev[i].(map[string]any)
		if item["status"] == "merged" && truthy(old["candidate_id"]) {
			item = old
		}
		merged[i] = item
		if item["status"] == "rejected" && retryableReasons[hashable(item["reason"])] {
			retry = true
		}
	}
	return merged, retry, nil
}

// archiveTimestamp is session_archive.timestamp: epoch seconds (milliseconds scaled down) or an
// ISO time; ok false for anything else.
func archiveTimestamp(v any) (float64, bool) {
	switch x := v.(type) {
	case bool:
		return map[bool]float64{true: 1, false: 0}[x], true
	case json.Number, float64, int, int64:
		f, _ := num(x)
		for f > 1e11 {
			f /= 1000
		}
		return f, true
	case string:
		if x == "" {
			return 0, false
		}
		ts, _, ok := fromISO(x)
		return ts, ok
	}
	return 0, false
}

const archiveTTL = 7 * 86400

// drain processes the spool under drain.lock; a second drain returns at once.
// processJob is the drain's per-job step; tests replace it.
var processJob = process

func drain(allWorkOrigins bool) (err error) {
	if err := os.MkdirAll(capState, 0o700); err != nil {
		return err
	}
	lock, err := lockFile(filepath.Join(capState, "drain.lock"), false)
	if err != nil {
		if errors.Is(err, errBusy) {
			return nil // another drain is already running
		}
		return err
	}
	defer lock.Close()
	tally = &analysisRun{reasons: map[string]int{}}
	defer func() {
		run := tally
		tally = nil
		if saveErr := saveAnalysisRun(run, err); saveErr != nil { // a report, never a reason to fail the drain
			metric(kv("event", "analysis_run_failure"), kv("error", errorCode(saveErr)))
		}
	}()
	a, err := cleanupSpool(spoolDir(), "queued_at", -1)
	if err != nil {
		return err
	}
	b, err := cleanupSpool(failedDir(), "queued_at", -1)
	if err != nil {
		return err
	}
	if a+b > 0 {
		metric(kv("event", "capture_copies_expired"), kv("count", a+b))
	}
	paths, _ := filepath.Glob(filepath.Join(globEscape(spoolDir()), "*.json"))
	sortPaths(paths)
	park := func(path string) error {
		if err := os.MkdirAll(failedDir(), 0o700); err != nil {
			return err
		}
		return os.Rename(path, filepath.Join(failedDir(), filepath.Base(path)))
	}
	parkedEvents := parkedBatchEvents() // batch parents already in the failed spool
	for _, path := range paths {
		if err := drainJob(path, park, parkedEvents); err != nil {
			return err
		}
	}
	sessionTraces()
	if err := hoDrain(allWorkOrigins); err != nil {
		return err
	}
	return pcDrain(allWorkOrigins)
}

// parkedBatchEvents is the event ids of the jobs already in the failed spool (none without the session processor).
func parkedBatchEvents() map[string]bool {
	parkedEvents := map[string]bool{}
	if failed, _ := filepath.Glob(filepath.Join(globEscape(failedDir()), "*.json")); sessionProcessorOn() {
		for _, f := range failed {
			if job, err := readJob(f); err == nil {
				if id, ok := job["event_id"].(string); ok {
					parkedEvents[id] = true
				}
			}
		}
	}
	return parkedEvents
}

// drainJob runs one spool file through processJob and settles its failure; nil moves on to the next file.
func drainJob(path string, park func(string) error, parkedEvents map[string]bool) error {
	if isSymlink(path) || !exists(path) { // gone: parked earlier in this pass with its batch parent
		return nil
	}
	job, err := readJob(path)
	parent, _ := job["session_batch_parent"].(string) // a non-string parent never matches a parked event
	if err == nil && parent != "" && parkedEvents[parent] {
		// The parent holds this child's turns and was parked: follow it so the redrive keeps them together.
		if err := park(path); err != nil {
			return err
		}
		metric(kv("event", "session_processor_parked"), kv("session", head(str(job["session_id"]), 12)), kv("error", "session_batch_parent_parked"))
		return nil
	}
	if err != nil { // torn or vanished file: park it instead of blocking every later job
		if exists(path) {
			if err := park(path); err != nil {
				return err
			}
		}
		metric(kv("event", "writeback_failure"), kv("error", "unreadable_spool"))
		return nil
	}
	save := func() error { return atomicWrite(path, dumps(job)) }
	err = func() error {
		retryAfter, has := job["archive_retry_after"]
		if !has {
			retryAfter = 0
		}
		after, isNum := num(retryAfter)
		if _, isBool := retryAfter.(bool); !isNum && !isBool {
			return runtimeErr("archive_retry_invalid")
		}
		if after > timeNow() {
			return errSkip
		}
		if tally != nil {
			tally.jobs++
		}
		if err := processJob(job, save); err != nil {
			return err
		}
		return os.Remove(path)
	}()
	if err == errSkip {
		return nil
	}
	if err == nil {
		if tally != nil {
			n, _ := num(or(job["session_candidates"], 0))
			tally.candidates += int(n)
		}
		count("done", "")
		return nil
	}
	return settleJobFailure(job, path, err, park, save, parkedEvents)
}

// settleJobFailure pauses, drops, parks or retries a job whose step failed; nil moves on to the next file.
func settleJobFailure(job map[string]any, path string, err error, park func(string) error, save func() error, parkedEvents map[string]bool) error {
	code := errorCode(err)
	sid := head(str(job["session_id"]), 12)
	if capturePolicyPending(err) || privateCapturePolicy("capture_policy_wait", str(or(job["cwd"], "")), privateCheckpointRepository(job["session_prepared"], true)) != nil {
		// Pausing must preserve the last durable request and retry budget;
		// resuming retries it immediately without a synthetic failure/backoff.
		metric(kv("event", "capture_policy_pending"), kv("session", sid), kv("error", code))
		count("waiting", "capture_policy_pending")
		return nil
	}
	if sessionProcessorOn() && errors.Is(err, errSessionPending) && code == "session_archive_private_skipped" {
		// The user marked the transcript private, so it never reaches the archive: drop the batch, never retry.
		if err := dropBatch(job, path); err != nil {
			return err
		}
		metric(kv("event", "session_processor_dropped"), kv("session", sid), kv("error", code))
		count("dropped", code)
		return nil
	}
	if sessionProcessorOn() && errors.Is(err, errSessionPending) && (sessionModelDefect(code) || sessionInputDefect(code)) {
		// Re-running the same window repeats the model call and the same defect every minute: park the batch
		// (parent and merged children) instead of blocking every later turn of the session.
		if err := parkBatch(job, path, code, park, save); err != nil {
			return err
		}
		parkedEvents[str(job["event_id"])] = true
		metric(kv("event", "session_processor_parked"), kv("session", sid), kv("error", code))
		count("parked", code)
		return nil
	}
	return retryJob(job, path, err, code, sid, park, save)
}

// dropBatch removes a batch parent and its merged children from the spool.
func dropBatch(job map[string]any, path string) error {
	for _, name := range anyList(job["session_batch_children"]) {
		if child, err := sessionChildPath(name); err == nil {
			if err := os.Remove(child); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// parkBatch records the defect, then moves the batch's merged children and the parent to the failed spool.
func parkBatch(job map[string]any, path, code string, park func(string) error, save func() error) error {
	job["last_error"] = code
	if err := save(); err != nil {
		return err
	}
	for _, name := range anyList(job["session_batch_children"]) {
		if child, err := sessionChildPath(name); err == nil && exists(child) {
			if err := park(child); err != nil {
				return err
			}
		}
	}
	return park(path)
}

// retryJob schedules a job that is only waiting, else spends one of its attempts.
func retryJob(job map[string]any, path string, err error, code, sid string, park func(string) error, save func() error) error {
	// Only a real wait is free; any other pending code spends an attempt so it parks instead of re-calling the model forever.
	waiting := code == "429" || code == "session_batch_wait" || code == "session_model_budget_wait" || code == "session_batch_parent_pending"
	if sessionProcessorOn() && waiting {
		wait := 60.0
		if code == "429" {
			wait = 3600
		}
		job["archive_retry_after"] = timeNow() + wait
		job["last_error"] = code
		if err := save(); err != nil {
			return err
		}
		metric(kv("event", "session_processor_pending"), kv("session", sid), kv("error", code))
		count("waiting", code)
		return nil
	}
	if code == "archive_prompt_pending" {
		if queued, ok := archiveTimestamp(job["queued_at"]); ok && queued != 0 && queued+archiveTTL > timeNow() {
			job["archive_retry_after"] = timeNow() + 60
			job["last_error"] = code
			if err := save(); err != nil {
				return err
			}
			metric(kv("event", "archive_wait"), kv("session", sid))
			count("waiting", code)
			return nil
		}
		code = "archive_prompt_expired"
	}
	return spendAttempt(job, path, err, code, sid, park, save)
}

// spendAttempt counts a failed attempt and parks the job once it has used them all.
func spendAttempt(job map[string]any, path string, err error, code, sid string, park func(string) error, save func() error) error {
	attempts, _ := num(or(job["attempts"], 0))
	job["attempts"] = int(attempts) + 1
	job["last_error"] = code
	if sessionProcessorOn() && errors.Is(err, errSessionPending) {
		job["archive_retry_after"] = timeNow() + 60
	}
	metric(kv("event", "writeback_failure"), kv("session", sid), kv("error", code), kv("attempts", job["attempts"]))
	count("failed", code)
	if int(attempts)+1 >= capMaxAttempts {
		if err := park(path); err != nil {
			return err
		}
		if err := atomicWrite(filepath.Join(failedDir(), filepath.Base(path)), dumps(job)); err != nil {
			return err
		}
	} else if err := save(); err != nil {
		return err
	}
	return nil
}

var errSkip = errors.New("skip")

// sessionModelDefect reports a validation failure of an already-dispatched model output.
func sessionModelDefect(code string) bool {
	for _, p := range []string{"schema_", "evidence_", "coverage_", "candidate_", "user_provenance_",
		"tool_provenance_", "structured_output_", "checkpoint_", "memory_context_"} {
		if strings.HasPrefix(code, p) {
			return true
		}
	}
	return false
}

// sessionInputDefect reports an archive window or record that rereading the same transcript cannot change.
func sessionInputDefect(code string) bool {
	return code == "session_archive_window_incomplete" || code == "oversized_record_requires_artifact_reference" ||
		code == "oversized_tool_group_requires_reference" || code == "input_chunk_required"
}

// readJob loads one spool file, stamping a legacy job's queue time from its mtime.
func readJob(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	job, err := loadObjBytes(raw)
	if err != nil {
		return nil, err
	}
	if _, has := job["queued_at"]; !has {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		at, err := queuedAt(mtime(info), -1)
		if err != nil {
			return nil, err
		}
		job["queued_at"] = spoolISO(at)
		if err := atomicWrite(path, dumps(job)); err != nil {
			return nil, err
		}
	}
	if _, err := queuedAt(job["queued_at"], -1); err != nil {
		return nil, err
	}
	return job, nil
}
