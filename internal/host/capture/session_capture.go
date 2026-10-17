package capture

// Session processor delivery through the existing archive, capture and checkpoint APIs. The capture
// spool is the durable retry envelope, not a second canonical store: a prepared model output
// survives API retries, and the job's chunk cursor advances only after BOTH the candidates and the
// WorkCheckpoint are acknowledged.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// sessionProcessorOn: ARKISM_SESSION_PROCESSOR=1 replaces cue extraction and reflect with the processor.
func sessionProcessorOn() bool { return os.Getenv("ARKISM_SESSION_PROCESSOR") == "1" }

// sessionTokenFile points the capture client at the harness service credential: the processor's
// automated capture must never run as the owner. With automated false (a command a person types)
// the person's credential stays and only the shared audience partition is selected.
func sessionTokenFile(automated bool) error {
	if automated {
		file := os.Getenv("ARKISM_HARNESS_TOKEN_FILE")
		if file == "" {
			return runtimeErr("harness_service_credential_required")
		}
		host.TokenFile = file
	}
	if os.Getenv("ARKISM_SHARED_AUDIENCE_SCOPE") != "" {
		scope, err := sessionScope()
		if err != nil {
			return err
		}
		capScope = func() map[string]any { return scope }
	}
	return nil
}

func dedupeRefs(refs []any) []any {
	out, seen := []any{}, map[string]bool{}
	for _, r := range refs {
		if k := sdigest(r); !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	return out
}

func capRequest(path string, body any) (map[string]any, error) {
	return capPost(path, body, 60*time.Second)
}

var sessionLinked = sessionLinkedUsers // test hook

// sessionLinkedUsers turns the job's archived messages into records with exact archive refs. A job
// with a byte window reads the whole user/assistant dialogue in it; otherwise the user turns only.
func sessionLinkedUsers(job map[string]any, origin string) (records []any, refs map[string][]any, err error) {
	defer recovered(&err)
	users := job["users"].([]any)
	context, root := job["archive_context"], capProjects
	if locator, _ := job["archive_locator"].(map[string]any); truthy(job["archive_locator"]) {
		if len(users) != 1 || locator["session_id"] != job["session_id"] || locator["origin"] != origin {
			return nil, nil, sessionWait("session_archive_locator_invalid")
		}
		if origin == "zcode" {
			source := capZcodeDB
			if locator["device"] == "pc" {
				source = filepath.Join(archivePC, "zcode-sessions")
			}
			if users, err = evPrepareZcode(locator, users, source); err != nil {
				return nil, nil, err
			}
		} else {
			var hint any
			if context, hint, root, err = evLocatePrompt(locator); err != nil {
				return nil, nil, err
			}
			first := map[string]any{}
			for k, v := range users[0].(map[string]any) {
				first[k] = v
			}
			first["archive_hint"] = hint
			users = []any{first}
		}
	} else if autosaveGet(or(context, map[string]any{}), "origin") == "codex" {
		root = capCodexSessions
	}
	_, hasStart := job["source_start"]
	_, hasEnd := job["source_end"]
	window := truthy(context) && hasStart && hasEnd
	var snapshot any
	if window {
		start, okStart := host.Int(job["source_start"])
		end, okEnd := host.Int(job["source_end"])
		if !okStart || !okEnd || start < 0 || start >= end {
			return nil, nil, sessionWait("session_archive_window_invalid")
		}
		at := func(k string) string { return str(autosaveItem(context, k)) }
		actual := evidenceFileContext(at("path"), root, at("session_id"), at("device"), true, at("origin"))
		if actual == nil || !eq(actual, context) {
			return nil, nil, sessionWait("session_archive_context_changed")
		}
		collector := newCollector(evidenceClient{capPost, capScope()}, nil, true)
		if err := collector.file(at("path"), at("device"), at("origin"), at("logical"), at("session_id"), -1); err != nil {
			return nil, nil, err
		}
		if collector.counts.n["private_skipped"] > 0 {
			// A <private marker keeps the whole transcript out of the archive for good: waiting never fills the window.
			return nil, nil, sessionWait("session_archive_private_skipped")
		}
		saved, has := collector.state.Get(at("archive_id"))
		if !has || !truthy(saved) || collector.counts.n["committed"] == 0 {
			return nil, nil, sessionWait("session_archive_window_unavailable")
		}
		if covered, _ := host.Int(autosaveItem(saved, "signature").([]any)[1]); covered < end {
			return nil, nil, sessionWait("session_archive_window_unavailable")
		}
		snapshot = saved
	}
	if users, err = evPrepareUsers(context, users, root); err != nil {
		return nil, nil, err
	}
	records, refs = []any{}, map[string][]any{}
	for _, u := range users {
		// evPrepareUsers returns host.Obj users (archive_ref added), the job holds plain maps.
		user := map[string]any{"archive_refs": autosaveGet(u, "archive_refs"), "archive_ref": autosaveGet(u, "archive_ref"), "text": autosaveGet(u, "text")}
		archiveRefs, _ := user["archive_refs"].([]any)
		if len(archiveRefs) == 0 && truthy(user["archive_ref"]) {
			archiveRefs = []any{user["archive_ref"]}
		}
		if len(archiveRefs) == 0 {
			return nil, nil, sessionWait("session_archive_evidence_required")
		}
		var texts []string
		for _, ref := range archiveRefs {
			message, err := capRequest("/v1/session-archives/message", map[string]any{"scope": capScope(), "ref": ref})
			if err != nil {
				return nil, nil, err
			}
			if message["authority"] != "data_only_untrusted" || message["role"] != "user" || !eq(message["ref"], ref) || message["next_text_offset"] != nil {
				return nil, nil, sessionWait("session_archive_message_unavailable")
			}
			texts = append(texts, str(message["text"]))
		}
		if !strings.Contains(strings.Join(texts, "\n"), str(user["text"])) {
			return nil, nil, sessionWait("session_archive_text_mismatch")
		}
		// Revision 1 belongs to this immutable sha-bound evidence envelope, never a canonical sources revision.
		id := "archive-" + sdigest(archiveRefs)
		records = append(records, map[string]any{"source_id": id, "revision": 1, "role": "user", "text": user["text"]})
		refs[id] = archiveRefs
	}
	if window {
		archiveID := autosaveItem(context, "archive_id")
		header, err := capRequest("/v1/session-archives/get", map[string]any{"scope": capScope(), "id": archiveID})
		if err != nil {
			return nil, nil, err
		}
		if header["state"] != "active" || !eq(header["sha256"], autosaveItem(snapshot, "sha256")) {
			return nil, nil, sessionWait("session_archive_snapshot_changed")
		}
		messages, excluded, err := autosaveDialogue(map[string]any{"archive_id": archiveID, "sha256": header["sha256"],
			"start_byte": job["source_start"], "stop_byte": job["source_end"]}, capRequest, capScope())
		if err != nil {
			return nil, nil, err
		}
		if excluded > 0 {
			return nil, nil, sessionWait("session_archive_window_incomplete")
		}
		if len(messages) == 0 {
			// A window of only metadata lines (system, cost-state, last-prompt) has nothing to learn from: done.
			return []any{}, map[string][]any{}, nil
		}
		direct := map[string]bool{}
		for _, values := range refs {
			for _, ref := range values {
				direct[sdigest(ref)] = true
			}
		}
		records, refs = []any{}, map[string][]any{}
		for _, m := range messages {
			ref := autosaveItem(m, "ref")
			id, role := "archive-"+sdigest([]any{ref}), autosaveItem(m, "role")
			if role == "user" && !direct[sdigest(ref)] {
				role = "assistant" // relayed or meta user envelopes never gain direct authorship
			}
			records = append(records, map[string]any{"source_id": id, "revision": 1, "role": role, "text": autosaveItem(m, "text")})
			refs[id] = []any{ref}
		}
	}
	return records, refs, nil
}

func sessionCurrentCheckpoint(id string) (map[string]any, error) {
	result, err := capRequest("/v1/work-checkpoints/get", map[string]any{"scope": capScope(), "id": id})
	if host.HTTPCode(err) == 404 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if result["authority"] != "data_only_untrusted" || result["id"] != id {
		return nil, sessionWait("session_checkpoint_invalid")
	}
	return result, nil
}

func sessionChildPath(name any) (string, error) {
	s, ok := name.(string)
	path := filepath.Join(spoolDir(), s)
	if !ok || filepath.Base(s) != s || isSymlink(path) {
		return "", sessionWait("session_batch_path_invalid")
	}
	return path, nil
}

// sessionCoalesce merges pending user batches of the same session and transcript into job; the
// caller holds drain.lock.
func sessionCoalesce(job map[string]any, persist func() error) error {
	if children, has := job["session_batch_children"]; has && children != nil {
		for _, name := range anyList(children) {
			path, err := sessionChildPath(name)
			if err != nil {
				return err
			}
			if exists(path) {
				child, err := readJob(path)
				if err != nil {
					return err
				}
				child["session_batch_parent"] = job["event_id"]
				if err := atomicWrite(path, dumps(child)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	// Prompt locators each name a separate archive message; only a shared transcript context merges.
	if truthy(job["session_prepared"]) || !truthy(job["archive_context"]) || truthy(job["archive_locator"]) {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(globEscape(spoolDir()), "*.json"))
	sortPaths(paths)
	names, children := []any{}, []map[string]any{}
	paths2 := []string{}
	for _, path := range paths {
		if isSymlink(path) {
			continue
		}
		child, err := readJob(path)
		if err != nil {
			continue
		}
		if eq(child["event_id"], job["event_id"]) || child["session_id"] != job["session_id"] || !eq(child["archive_context"], job["archive_context"]) ||
			truthy(child["archive_locator"]) || truthy(child["captured"]) || truthy(child["prepared_capture"]) || truthy(child["session_prepared"]) ||
			truthy(child["session_batch_parent"]) || child["session_batch_children"] != nil {
			continue
		}
		users := anyList(job["users"])
		if len(users)+len(anyList(child["users"])) > 100 {
			break
		}
		job["users"] = append(users, anyList(child["users"])...)
		_, childWindow := child["source_start"]
		_, jobWindow := job["source_start"]
		if childWindow && jobWindow {
			cs, _ := num(child["source_start"])
			ce, _ := num(child["source_end"])
			js, _ := num(job["source_start"])
			je, _ := num(job["source_end"])
			job["source_start"], job["source_end"] = int64(min(cs, js)), int64(max(ce, je))
		}
		names, children, paths2 = append(names, filepath.Base(path)), append(children, child), append(paths2, path)
	}
	job["session_batch_children"] = names
	if err := persist(); err != nil { // parent first: no source is orphaned if interrupted during child marks
		return err
	}
	for i, child := range children {
		child["session_batch_parent"] = job["event_id"]
		if err := atomicWrite(paths2[i], dumps(child)); err != nil {
			return err
		}
	}
	return nil
}

func sessionFinishChildren(job map[string]any) error {
	for _, name := range anyList(job["session_batch_children"]) {
		path, err := sessionChildPath(name)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

var sessionClaimKinds = map[any]string{"preference": "user", "lesson": "lesson", "failure_recovery": "lesson"}

// sessionClaims turns validated candidates into capture claims bound to their archive messages.
// canonical maps a canonical trace source id to its sources ref: such evidence travels as
// source_refs, and an agent statement over a canonical trace is only an inference.
func sessionClaims(candidates []any, refs map[string][]any, canonical map[string]any) ([]any, error) {
	claims := []any{}
	for _, c := range candidates {
		candidate := c.(map[string]any)
		var candidateRefs []any
		sourceRefs := []any{}
		evidenceKind := candidate["evidence_kind"]
		for _, evidence := range candidate["source_refs"].([]any) {
			id := str(autosaveGet(evidence, "source_id"))
			candidateRefs = append(candidateRefs, refs[id]...)
			if ref, has := canonical[id]; has {
				sourceRefs = append(sourceRefs, ref)
				if evidenceKind == "agent_statement" {
					evidenceKind = "inference"
				}
			}
		}
		context := map[string]any{"evidence_kind": evidenceKind, "semantic_kind": candidate["kind"]}
		for _, k := range []string{"domains", "applicability", "uncertainty", "valid_from"} {
			context[k] = candidate[k]
		}
		for k, limit := range map[string]int{"applicability": 2000, "uncertainty": 512, "valid_from": 256} {
			if len(str(context[k])) > limit {
				return nil, sessionWait("memory_context_byte_limit")
			}
		}
		domains := []string{}
		for _, d := range candidate["domains"].([]any) {
			domains = append(domains, str(d))
		}
		text := str(candidate["statement"])
		if applicability := str(candidate["applicability"]); applicability != "" {
			text += " (적용 조건: " + applicability + ")"
		}
		kind, known := sessionClaimKinds[candidate["kind"]]
		if !known {
			kind = "decision"
		}
		basis := "inferred"
		if candidate["evidence_kind"] == "user_statement" {
			basis = "explicit_user"
		}
		reason := str(candidate["uncertainty"])
		if reason == "" {
			reason = "session_processor source-backed candidate"
		}
		claims = append(claims, map[string]any{"memory_context": context, "subject": "session-" + strings.Join(domains, "-"), "text": text,
			"kind": kind, "basis": basis, "reason": reason, "archive_messages": dedupeRefs(candidateRefs), "source_refs": sourceRefs})
	}
	return claims, nil
}

// sessionProcess is process() under ARKISM_SESSION_PROCESSOR=1.
func sessionProcess(job map[string]any, persist func() error, origin string) (err error) {
	defer recovered(&err)
	scope := capScope()
	if v, has := job["session_scope"]; has && v != nil && !eq(v, scope) {
		return sessionWait("session_scope_changed")
	}
	if truthy(job["session_batch_parent"]) {
		return sessionWait("session_batch_parent_pending")
	}
	if truthy(job["session_processor_complete"]) {
		return sessionFinishChildren(job)
	}
	if len(anyList(job["users"])) == 0 && !truthy(job["execution_receipt"]) && !truthy(job["archive_context"]) {
		return nil
	}
	userText := func() string {
		var texts []string
		for _, u := range anyList(job["users"]) {
			texts = append(texts, str(autosaveGet(u, "text")))
		}
		return strings.Join(texts, "\n")
	}
	explicit := func(fallback string) string {
		switch text := userText(); {
		case correctionRE.MatchString(text):
			return "explicit_correction"
		case rememberRE.MatchString(text):
			return "explicit_remember_request"
		}
		return fallback
	}
	event := ""
	if t := job["type"]; t == "session_end" || t == "session_ended" || t == "task_finished" {
		event = "task_finished"
	}
	event = explicit(event)
	now := timeNow()
	queued, ok := archiveTimestamp(job["queued_at"])
	if !ok || queued == 0 {
		queued = now
	}
	latest := queued
	for _, u := range anyList(job["users"]) {
		if at, ok := archiveTimestamp(autosaveGet(u, "at")); ok && at != 0 {
			latest = max(latest, at)
		}
	}
	pendingRecords := anyList(job["users"])
	if len(pendingRecords) == 0 {
		pendingRecords = []any{map[string]any{"text": "canonical trace pending"}}
	}
	if !truthy(job["session_prepared"]) && !sessionDue(pendingRecords, event, max(0, now-latest), max(0, now-queued)) {
		return sessionWait("session_batch_wait")
	}
	if err := sessionCoalesce(job, persist); err != nil {
		return err
	}
	event = explicit(event)
	read := sessionLinked
	if truthy(job["execution_receipt"]) {
		read = func(job map[string]any, _ string) ([]any, map[string][]any, error) { return sessionCanonical(job) }
	}
	records, refs, err := read(job, origin)
	if err != nil {
		return err
	}
	canonical := map[string]any{}
	for _, ref := range anyList(autosaveGet(or(job["execution_receipt"], map[string]any{}), "source_events")) {
		canonical[str(autosaveGet(ref, "id"))] = ref
	}
	chunkSources := func(chunk []any) []any {
		out := []any{}
		for _, row := range chunk {
			if ref, has := canonical[str(autosaveGet(row, "source_id"))]; has {
				out = append(out, ref)
			}
		}
		return out
	}
	groups, err := chunkRecords(records, 32000)
	if err != nil {
		return err
	}
	for _, group := range groups {
		for _, row := range group {
			if split := autosaveGet(row, "split_of"); split != nil {
				id, whole := str(autosaveGet(row, "source_id")), str(split)
				refs[id] = refs[whole]
				if ref, has := canonical[whole]; has {
					canonical[id] = ref
				}
			}
		}
	}
	var chunks [][]any
	for _, group := range groups {
		for i := 0; i < len(group); i += 8 {
			chunks = append(chunks, group[i:min(i+8, len(group))])
		}
	}
	cursor, _ := host.Int(or(job["session_cursor"], 0))
	checkpointID := "session-" + sdigest([]any{scope, origin, job["session_id"]})
	sid := str(job["session_id"])
	var start int64
	for index, chunk := range chunks {
		end := start + int64(len(chunk))
		chunkStart := start
		start = end
		if int64(index) < cursor {
			continue
		}
		prepared, _ := job["session_prepared"].(map[string]any)
		if prepared == nil {
			current, err := sessionCurrentCheckpoint(checkpointID)
			if err != nil {
				return err
			}
			var previous any
			if current != nil {
				previous = current["package"]
				known, knownSources := map[string]bool{}, map[string]bool{}
				for _, ref := range anyList(current["archive_messages"]) {
					known[sdigest(ref)] = true
				}
				for _, ref := range anyList(autosaveGet(or(current["package"], map[string]any{}), "source_refs")) {
					knownSources[sdigest(ref)] = true
				}
				delivered := true
				for _, row := range chunk {
					// A later part shares its record's refs with the window just saved, so refs cannot prove it delivered.
					if whole := autosaveGet(row, "split_of"); whole != nil && autosaveGet(row, "source_id") != str(whole)+"-p1" {
						delivered = false
					}
					if ref, has := canonical[str(autosaveGet(row, "source_id"))]; has {
						delivered = delivered && knownSources[sdigest(ref)]
						continue
					}
					for _, ref := range refs[str(autosaveGet(row, "source_id"))] {
						delivered = delivered && known[sdigest(ref)]
					}
				}
				if delivered { // the checkpoint follows the capture ACK: this source window was delivered
					job["session_cursor"] = index + 1
					if err := persist(); err != nil {
						return err
					}
					continue
				}
			}
			var chunkRefs []any
			for _, row := range chunk {
				chunkRefs = append(chunkRefs, refs[str(autosaveGet(row, "source_id"))]...)
			}
			transferRefs := dedupeRefs(chunkRefs)
			if len(transferRefs) > 8 {
				return sessionWait("session_transfer_evidence_limit")
			}
			authorize := func(content string) error {
				payload := map[string]any{"scope": scope, "refs": transferRefs, "source_refs": chunkSources(chunk), "destination": "external:anthropic", "content": content}
				if current != nil {
					payload["work"] = map[string]any{"collection": "work_checkpoints", "id": checkpointID, "revision": current["revision"]}
				}
				permission, err := capRequest("/v1/session-archives/authorize-transfer", payload)
				if err != nil {
					return err
				}
				if permission["authorized"] != true || permission["content_hash"] != sha256Hex([]byte(content)) {
					return sessionWait("external_transfer_denied")
				}
				return nil
			}
			expected := int64(0)
			if current != nil {
				expected, _ = host.Int(current["revision"])
			}
			// Automatic Haiku qualification stays conservative: an explicit correction is the only category identified here.
			category := ""
			if event == "explicit_correction" {
				category = "korean_correction"
			}
			if prepared, err = sessionPrepare(scope, sid, chunk, chunkStart, end, expected, previous, nil, category, authorize); err != nil {
				return err
			}
			pkg := sessionCheckpointPackage(prepared, sid, origin, str(or(job["cwd"], "")))
			pkg["source_refs"] = dedupeRefs(append(chunkSources(chunk), anyList(autosaveGet(or(previous, map[string]any{}), "source_refs"))...))
			// Only this window's refs: the server takes at most 8, all from the current archive snapshot,
			// so a previous window's refs (older snapshot) would make every later save a 409.
			archiveRefs := transferRefs
			claims, err := sessionClaims(prepared["output"].(map[string]any)["memory_candidates"].([]any), refs, canonical)
			if err != nil {
				return err
			}
			if err := validateCheckpointPackage(pkg); err != nil {
				return err
			}
			prepared["claims"] = claims
			prepared["checkpoint_request"] = map[string]any{"scope": scope, "id": checkpointID, "event_id": prepared["operation_id"],
				"expected_revision": expected, "package": pkg, "archive_messages": archiveRefs}
			job["session_prepared"] = prepared
			if err := persist(); err != nil {
				return err
			}
		}
		request := prepared["checkpoint_request"].(map[string]any)
		if !eq(request["scope"], scope) {
			return sessionWait("session_scope_changed")
		}
		if !truthy(prepared["capture_ack"]) {
			if claims := anyList(prepared["claims"]); len(claims) > 0 {
				eventType := "turn_completed"
				switch event {
				case "explicit_correction":
					eventType = "user_corrected_fact"
				case "explicit_remember_request":
					eventType = "explicit_remember"
				}
				occurred := or(job["occurred_at"], job["queued_at"])
				if users := anyList(job["users"]); len(users) > 0 {
					occurred = autosaveGet(users[len(users)-1], "at")
				}
				items, err := sendCapture(eventType, str(prepared["operation_id"]), sid, occurred, claims, origin, 0, host.Obj{
					{Key: "model", Value: autosaveGet(prepared["metadata"], "model_id")}, {Key: "prompt_version", Value: sessionPromptVersion},
					{Key: "pipeline_version", Value: "session-processor-v1"}})
				if err != nil {
					return err
				}
				acknowledged := len(items) == len(claims)
				for _, item := range items {
					acknowledged = acknowledged && autosaveGet(item, "status") != "rejected"
				}
				if !acknowledged {
					return sessionWait("session_capture_not_acknowledged")
				}
			}
			prepared["capture_ack"] = true
			if err := persist(); err != nil {
				return err
			}
		}
		receipt, err := capRequest("/v1/work-checkpoints/save", request)
		if err != nil {
			return err
		}
		revision, _ := host.Int(receipt["revision"])
		expected, _ := host.Int(request["expected_revision"])
		if receipt["id"] != checkpointID || revision != expected+1 {
			return sessionWait("session_checkpoint_not_acknowledged")
		}
		job["session_cursor"] = index + 1
		delete(job, "session_prepared")
		if err := persist(); err != nil {
			return err
		}
	}
	job["session_processor_complete"], job["captured"] = true, true
	if err := persist(); err != nil {
		return err
	}
	return sessionFinishChildren(job)
}
