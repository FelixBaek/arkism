package capture

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

// Exact transcript locations for capture; never guess a source from similarity.

var evidenceHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

// evidencePost is capPost's shape: one Arkism API request with a timeout.
type evidencePost = func(path string, payload any, timeout time.Duration) (map[string]any, error)

// evidenceDict copies a JSON object (dict or ordered) into an ordered one, or reports false.
func evidenceDict(v any) (host.Obj, bool) {
	switch x := v.(type) {
	case host.Obj:
		return append(host.Obj{}, x...), true
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(host.Obj, 0, len(x))
		for _, k := range keys {
			out = append(out, host.KV{Key: k, Value: x[k]})
		}
		return out, true
	}
	return nil, false
}

// evidenceFileContext is the collector's identity of transcript under an explicitly
// configured local root, or nil.
func evidenceFileContext(transcript, root string, sid, device any, verifyFile bool, origin any) host.Obj {
	if (origin != "claude_code" && origin != "codex") || (device != "server" && device != "pc") {
		return nil
	}
	s, ok := sid.(string)
	if !ok || s == "" {
		return nil
	}
	path, base := absPath(transcript), absPath(root)
	// Reject symlink aliases: they can change both authority and logical ID.
	if !resolved(path) || !resolved(base) {
		return nil
	}
	logical, err := filepath.Rel(base, path)
	if err != nil || logical == ".." || strings.HasPrefix(logical, ".."+string(filepath.Separator)) {
		return nil
	}
	logical = filepath.ToSlash(logical)
	if verifyFile {
		if !isFile(path) {
			return nil
		}
		got, err := evidenceTranscriptSession(path, origin.(string))
		if err != nil || got != s {
			return nil
		}
	}
	return host.Obj{{Key: "path", Value: path}, {Key: "logical", Value: logical}, {Key: "device", Value: device},
		{Key: "origin", Value: origin}, {Key: "session_id", Value: s},
		{Key: "archive_id", Value: archiveIdentity(device.(string), origin.(string), s, logical)}}
}

// evidenceRowHint locates one complete user record by chunk position and content hash.
func evidenceRowHint(context any, raw []byte, offset int64, message any, format string) host.Obj {
	if format == "" {
		format = "jsonl"
	}
	if context == nil || autosaveGet(message, "role") != "user" || !strings.HasSuffix(string(raw), "\n") ||
		len(raw) > 8<<20 || !utf8.Valid(raw) {
		return nil
	}
	if marked, err := scanPrivate(strings.NewReader(string(raw))); marked || err != nil {
		return nil
	}
	rawHash := sha256Hex(raw[:len(raw)-1])
	digest := sha256Hex([]byte("arkism-message-v1\n" + format + "\nuser\n" + rawHash))
	return host.Obj{{Key: "archive_id", Value: autosaveItem(context, "archive_id")}, {Key: "chunk_index", Value: offset / archiveChunk},
		{Key: "chunk_offset", Value: offset % archiveChunk}, {Key: "message_sha256", Value: digest}}
}

// evidenceClient posts session-archive requests with the capture client's scope and
// turns an HTTP failure into the collector's APIError.
type evidenceClient struct {
	send  evidencePost
	scope map[string]any
}

func evidencePolicyCheck(policy *archivePolicy) error {
	if err := policy.check(); err != nil {
		return sessionWait("archive_capture_policy_wait")
	}
	return nil
}

// Evidence lookups can reuse an archive without calling collector.upload. They
// obey the original source and recorded project policy before every request too.
func evidencePolicyClient(post evidencePost, scope map[string]any, policy *archivePolicy) evidenceClient {
	return evidenceClient{func(path string, payload any, timeout time.Duration) (map[string]any, error) {
		if err := evidencePolicyCheck(policy); err != nil {
			return nil, err
		}
		return post(path, payload, timeout)
	}, scope}
}

func (c evidenceClient) post(endpoint string, payload map[string]any) (map[string]any, error) {
	body := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		body[k] = v
	}
	body["scope"] = c.scope
	reply, err := c.send("/v1/session-archives/"+endpoint, body, 60*time.Second)
	var h *host.HTTPError
	if !errors.As(err, &h) {
		return reply, err
	}
	code := any("http_error")
	if v, derr := loadsBytes(h.Body); derr == nil {
		if m, ok := v.(map[string]any); ok {
			if c, has := m["code"]; has {
				code = c
			} else if c, has := m["error"]; has {
				code = c
			}
			if cm, isMap := code.(map[string]any); isMap {
				code = "http_error"
				if c, has := cm["code"]; has {
					code = c
				}
			}
		}
	}
	return nil, &APIError{Status: h.Code, Code: str(code)}
}

func evidenceWithoutArchiveLinks(users []any) []any {
	for _, user := range users {
		if row, ok := user.(map[string]any); ok {
			delete(row, "archive_hint")
			delete(row, "archive_ref")
			delete(row, "archive_refs")
		}
	}
	return users
}

// evidencePrepareUsers returns users with validated archive refs, or fails before any
// claim is submitted.
func evidencePrepareUsers(context any, users []any, root string, post evidencePost, scope map[string]any) (out []any, err error) {
	defer recovered(&err)
	out = []any{}
	if !truthy(context) {
		return users, nil
	}
	expected := evidenceFileContext(sstr(autosaveGet(context, "path")), root, autosaveGet(context, "session_id"),
		autosaveGet(context, "device"), false, autosaveGet(context, "origin"))
	if expected == nil || !eq(expected, context) {
		return nil, runtimeErr("archive_context_invalid")
	}
	path := sstr(autosaveGet(context, "path"))
	policy := newArchivePolicy(path)
	if err := evidencePolicyCheck(policy); err != nil {
		return nil, err
	}
	marked, err := privateMarked(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// A removed local transcript cannot supply current archive evidence.
			return evidenceWithoutArchiveLinks(users), nil
		}
		return nil, sessionWait("archive_inspection_unavailable")
	}
	if marked { // never archived; the memories carry no source link
		return evidenceWithoutArchiveLinks(users), nil
	}
	if err := policy.inspect(path, "jsonl"); err != nil {
		return nil, err
	}
	if err := evidencePolicyCheck(policy); err != nil {
		return nil, err
	}
	at := func(k string) any { return autosaveItem(context, k) }
	client := evidencePolicyClient(post, scope, policy)
	refreshed := false
	refresh := func() error {
		if refreshed {
			return runtimeErr("archive_evidence_unavailable")
		}
		refreshed = true
		checked := evidenceFileContext(at("path").(string), root, at("session_id"), at("device"), true, at("origin"))
		if checked == nil || !eq(checked, context) {
			return runtimeErr("archive_source_unavailable")
		}
		c := newCollector(client, nil, true)
		if err := c.file(at("path").(string), at("device").(string), at("origin").(string), at("logical").(string),
			at("session_id").(string), -1); err != nil {
			return err
		}
		if c.counts.n["committed"] == 0 {
			if c.counts.n["capture_skipped"] > 0 {
				return sessionWait("archive_capture_policy_wait")
			}
			return runtimeErr("archive_snapshot_unavailable")
		}
		return nil
	}
	resolve := func(hint any) (any, error) {
		h, ok := evidenceDict(hint)
		idx, idxOK := host.Int(evidenceField(hint, "chunk_index"))
		off, offOK := host.Int(evidenceField(hint, "chunk_offset"))
		sum, _ := evidenceField(hint, "message_sha256").(string)
		if !ok || !eq(evidenceField(hint, "archive_id"), at("archive_id")) || !evidenceHash.MatchString(sum) ||
			!idxOK || idx < 0 || idx >= 2048 || !offOK || off < 0 || off >= archiveChunk || len(h) != 4 ||
			!evidenceKeys(h, "archive_id", "message_sha256", "chunk_index", "chunk_offset") {
			return nil, runtimeErr("archive_hint_invalid")
		}
		for attempt := 0; attempt < 2; attempt++ {
			var again bool
			actual, err := func() (any, error) {
				header, err := client.post("get", map[string]any{"id": at("archive_id")})
				if err != nil {
					return nil, err
				}
				if header["state"] != "active" {
					if attempt == 0 {
						again = true
						return nil, refresh()
					}
					return nil, runtimeErr("archive_not_active")
				}
				for _, k := range []string{"session_id", "origin", "device"} {
					if !eq(header[k], at(k)) {
						return nil, runtimeErr("archive_identity_mismatch")
					}
				}
				sha, has := header["sha256"]
				if !has {
					sha = ""
				}
				s, isStr := sha.(string)
				if !isStr || !evidenceHash.MatchString(s) {
					return nil, runtimeErr("archive_header_invalid")
				}
				ref := append(host.Obj{}, h...)
				ref.Set("sha256", s)
				message, err := client.post("message", map[string]any{"ref": ref})
				if err != nil {
					return nil, err
				}
				got, has := message["ref"]
				if !has {
					got = map[string]any{}
				}
				gotSHA, _ := autosaveGet(got, "sha256").(string)
				if message["role"] != "user" || message["authority"] != "data_only_untrusted" || !evidenceHas(got, h) ||
					!evidenceHash.MatchString(gotSHA) {
					return nil, runtimeErr("archive_message_mismatch")
				}
				if privatePayload(message["text"]) {
					return nil, sessionWait("session_archive_private_skipped")
				}
				return got, nil
			}()
			var api *APIError
			if errors.As(err, &api) {
				if !(attempt == 0 && (api.Status == 400 || api.Status == 404 || api.Status == 409)) {
					return nil, runtimeErr("archive_evidence_unavailable")
				}
				if err := refresh(); err != nil {
					return nil, err
				}
				continue
			}
			if err != nil {
				return nil, err
			}
			if again {
				continue
			}
			return actual, nil
		}
		return nil, runtimeErr("archive_evidence_unavailable")
	}
	for _, user := range users {
		value, ok := evidenceDict(user)
		if !ok {
			panic("user is not a dict")
		}
		if hint := autosaveGet(user, "archive_hint"); truthy(hint) {
			ref, err := resolve(hint)
			if err != nil {
				return nil, err
			}
			value.Set("archive_ref", ref)
		}
		out = append(out, value)
	}
	return out, nil
}

// evidenceField is v.get(key) that tolerates a non-dict (checked by the caller).
func evidenceField(v any, key string) any {
	x, _ := objGet(v, key)
	return x
}

// evidenceKeys reports that o has exactly the given keys.
func evidenceKeys(o host.Obj, keys ...string) bool {
	if len(o) != len(keys) {
		return false
	}
	for _, k := range keys {
		if _, ok := o.Get(k); !ok {
			return false
		}
	}
	return true
}

// evidenceHas is Python `not any(actual.get(k) != v for k, v in want.items())`.
func evidenceHas(actual any, want host.Obj) bool {
	for _, kv := range want {
		if !eq(autosaveGet(actual, kv.Key), kv.Value) {
			return false
		}
	}
	return true
}

// evidenceClaudeContext is the Claude Code transcript's file context.
func evidenceClaudeContext(transcript, root, sid, device string) host.Obj {
	return evidenceFileContext(transcript, root, sid, device, true, "claude_code")
}

// evidenceTranscriptSession reads the session id from the first 8 lines; unlike the
// collector's fallback, a locator requires explicit metadata.
func evidenceTranscriptSession(path, origin string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	for range 8 {
		raw, err := readLine(r, 2*archiveChunk+1)
		if err != nil {
			return "", err
		}
		if len(raw) == 0 || len(raw) > 2*archiveChunk {
			break
		}
		row, err := loadObjBytes(raw)
		if err != nil {
			continue
		}
		var sid any
		switch {
		case origin == "codex" && row["type"] == "session_meta":
			payload, has := row["payload"]
			if !has {
				payload = map[string]any{}
			}
			p, ok := payload.(map[string]any)
			if !ok {
				continue
			}
			sid = p["id"]
		case origin == "claude_code":
			sid = row["sessionId"]
		default:
			continue
		}
		if s, ok := sid.(string); ok && s != "" {
			return s, nil
		}
	}
	return "", nil
}

// evidencePromptLocator names a prompt by session and hash, for sources the hook cannot
// point at by byte offset.
func evidencePromptLocator(origin, device string, sid, prompt any) host.Obj {
	switch origin + "/" + device {
	case "codex/server", "codex/pc", "claude_code/pc", "zcode/server", "zcode/pc":
	default:
		return nil
	}
	s, ok := sid.(string)
	p, pok := prompt.(string)
	if !ok || s == "" || s == "unknown" || !pok || p == "" {
		return nil
	}
	if privatePayload([]any{s, p}) {
		return nil
	}
	return host.Obj{{Key: "origin", Value: origin}, {Key: "device", Value: device}, {Key: "session_id", Value: s},
		{Key: "prompt_sha256", Value: sha256Hex([]byte(p))}}
}

func evidenceLocatorRoot(locator host.Obj) (string, error) {
	var matches []string
	origin, _ := locator.Get("origin")
	device, _ := locator.Get("device")
	for _, r := range archiveRoots() {
		if origin == r.origin && device == r.device && r.glob == "*.jsonl" {
			matches = append(matches, r.root)
		}
	}
	if len(matches) != 1 {
		return "", runtimeErr("archive_locator_invalid")
	}
	return matches[0], nil
}

func evidenceValidateLocator(locator any) (host.Obj, error) {
	l, ok := evidenceDict(locator)
	if !ok || len(l) != 4 || !evidenceKeys(l, "origin", "device", "session_id", "prompt_sha256") {
		return nil, runtimeErr("archive_locator_invalid")
	}
	sum, _ := autosaveGet(l, "prompt_sha256").(string)
	origin, _ := autosaveGet(l, "origin").(string)
	device, _ := autosaveGet(l, "device").(string)
	if !evidenceHash.MatchString(sum) || evidencePromptLocator(origin, device, autosaveGet(l, "session_id"), "probe") == nil {
		return nil, runtimeErr("archive_locator_invalid")
	}
	return l, nil
}

// evidenceLocatePrompt finds the one transcript record whose user text hashes to the
// locator's prompt_sha256 and returns its file context, row hint and root.
func evidenceLocatePrompt(locator any) (context, hint host.Obj, root string, err error) {
	l, err := evidenceValidateLocator(locator)
	if err != nil {
		return nil, nil, "", err
	}
	if root, err = evidenceLocatorRoot(l); err != nil {
		return nil, nil, "", err
	}
	var contexts []host.Obj
	sid, _ := l.Get("session_id")
	device, _ := l.Get("device")
	origin, _ := l.Get("origin")
	for _, path := range globAll(root, "*.jsonl") {
		// Claude Code names a transcript <session>.jsonl and Codex rollout-<time>-<session>.jsonl,
		// so only files named for the session can hold it; skip reading the rest (a PC has hundreds).
		if s, ok := sid.(string); ok && !strings.Contains(filepath.Base(path), s) {
			continue
		}
		if c := evidenceFileContext(path, root, sid, device, true, origin); c != nil {
			contexts = append(contexts, c)
			if len(contexts) > 1 {
				return nil, nil, "", runtimeErr("archive_prompt_ambiguous")
			}
		}
	}
	if len(contexts) == 0 {
		return nil, nil, "", runtimeErr("archive_prompt_pending")
	}
	context = contexts[0]
	pathV, _ := context.Get("path")
	path := pathV.(string)
	marked, err := privateMarked(path)
	if err != nil {
		return nil, nil, "", sessionWait("archive_inspection_unavailable")
	}
	if marked {
		return nil, nil, "", sessionWait("session_archive_private_skipped")
	}
	before, err := archiveSignature(path)
	if err != nil {
		return nil, nil, "", err
	}
	want, _ := l.Get("prompt_sha256")
	key, kind := "message", "user"
	if origin == "codex" {
		key, kind = "payload", "response_item"
	}
	var matches []host.Obj
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, "", err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	var offset int64
	for {
		raw, err := readLine(r, (8<<20)+1)
		if err != nil {
			return nil, nil, "", err
		}
		start := offset
		offset += int64(len(raw))
		if len(raw) == 0 {
			break
		}
		if len(raw) > 8<<20 {
			return nil, nil, "", runtimeErr("archive_prompt_record_too_large")
		}
		if raw[len(raw)-1] != '\n' {
			break
		}
		if h := evidencePromptHint(context, raw, start, key, kind, want); h != nil {
			matches = append(matches, h)
		}
	}
	after, err := archiveSignature(path)
	if err != nil {
		return nil, nil, "", err
	}
	if !eq(after, before) {
		return nil, nil, "", runtimeErr("archive_prompt_pending")
	}
	if len(matches) > 1 {
		return nil, nil, "", runtimeErr("archive_prompt_ambiguous")
	}
	if len(matches) == 0 {
		return nil, nil, "", runtimeErr("archive_prompt_pending")
	}
	return context, matches[0], root, nil
}

// evidencePromptHint is one locate_prompt line; malformed rows are skipped.
func evidencePromptHint(context host.Obj, raw []byte, offset int64, key, kind string, want any) (hint host.Obj) {
	defer func() {
		if recover() != nil {
			hint = nil
		}
	}()
	row, err := loadObjBytes(raw)
	if err != nil || row["type"] != kind {
		return nil
	}
	message, has := row[key]
	if !has {
		message = map[string]any{}
	}
	if autosaveGet(message, "role") != "user" {
		return nil
	}
	content := autosaveGet(message, "content")
	if blocks, ok := content.([]any); ok {
		// Same joining as the server's archive message text (memory.archiveBlockSeparator):
		// Codex input_text blocks join with nothing, as its hook prompt does.
		var joined strings.Builder
		prev := ""
		for _, b := range blocks {
			m, ok := b.(map[string]any)
			if !ok {
				continue
			}
			text, isStr := m["text"].(string)
			kind, _ := m["type"].(string)
			if (kind == "text" || kind == "input_text") && isStr {
				if prev != "" && !(prev == "input_text" && kind == "input_text") {
					joined.WriteString("\n")
				}
				joined.WriteString(text)
				prev = kind
			}
		}
		content = joined.String()
	}
	s, ok := content.(string)
	if !ok || privatePayload(s) || sha256Hex([]byte(s)) != want {
		return nil
	}
	return evidenceRowHint(context, raw, offset, message, "")
}

// evidenceZcodeHints selects real-user parts from exactly the snapshot that will be uploaded.
func evidenceZcodeHints(path string, locator host.Obj) (out []any, err error) {
	defer recovered(&err)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > archiveMaxBytes {
		return nil, runtimeErr("archive_snapshot_too_large")
	}
	marked, err := privateMarked(path)
	if err != nil {
		return nil, sessionWait("archive_inspection_unavailable")
	}
	if marked {
		return nil, sessionWait("session_archive_private_skipped")
	}
	device, _ := locator.Get("device")
	sid, _ := locator.Get("session_id")
	want, _ := locator.Get("prompt_sha256")
	aid := archiveIdentity(device.(string), "zcode", sid.(string), "db-session")
	type part struct {
		sequence int64
		text     string
		hint     host.Obj
	}
	messages := map[string]bool{}
	seen := map[string]bool{}
	parts := map[string][]part{}
	var order []string
	visible := func(v any) bool {
		channel, has := objGet(v, "channel")
		if _, ok := v.(map[string]any); !ok {
			panic("not a dict")
		}
		if !has {
			channel = ""
		}
		return (channel == "" || channel == "final") && autosaveGet(v, "type") != "session_meta"
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	var offset int64
	for {
		raw, err := readLine(r, (8<<20)+1)
		if err != nil {
			return nil, err
		}
		start := offset
		offset += int64(len(raw))
		if len(raw) == 0 {
			break
		}
		if len(raw) > 8<<20 {
			return nil, runtimeErr("archive_prompt_record_too_large")
		}
		v, err := loadsBytes(raw)
		if err != nil {
			return nil, err
		}
		record, has := objGet(v, "record")
		if !has {
			record = map[string]any{}
		}
		switch autosaveGet(v, "table") {
		case "message":
			mid := canon(autosaveItem(record, "id"))
			if seen[mid] {
				return nil, runtimeErr("archive_prompt_ambiguous")
			}
			seen[mid] = true
			data, err := loads([]byte(autosaveItem(record, "data").(string)))
			if err != nil {
				return nil, err
			}
			semantics, has := objGet(data, "semantics")
			if !has {
				semantics = map[string]any{}
			}
			messages[mid] = visible(v) && visible(record) && visible(data) && autosaveGet(data, "role") == "user" &&
				autosaveGet(semantics, "origin") == "real_user"
		case "part":
			data, err := loads([]byte(autosaveItem(record, "data").(string)))
			if err != nil {
				return nil, err
			}
			text, isStr := autosaveGet(data, "text").(string)
			if !visible(v) || !visible(record) || !visible(data) || truthy(autosaveGet(data, "synthetic")) ||
				autosaveGet(data, "type") != "text" || !isStr {
				continue
			}
			hint := evidenceRowHint(host.Obj{{Key: "archive_id", Value: aid}}, raw, start, map[string]any{"role": "user"}, "reconstructed_zcode")
			if hint == nil {
				continue
			}
			if id, ok := autosaveGet(record, "id").(string); ok && id != "" {
				hint.Set("record_key", sha256Hex([]byte("arkism-zcode-part-v1\n"+id)))
			}
			seq, has := objGet(record, "sequence")
			if !has {
				seq = start
			}
			n, ok := host.Int(seq)
			if !ok {
				return nil, runtimeErr("archive_prompt_order_invalid")
			}
			mid := canon(autosaveItem(record, "message_id"))
			if _, known := parts[mid]; !known {
				order = append(order, mid)
			}
			parts[mid] = append(parts[mid], part{n, text, hint})
		}
	}
	var matches [][]any
	for _, mid := range order {
		if !messages[mid] {
			continue
		}
		values := parts[mid]
		sort.SliceStable(values, func(i, j int) bool { return values[i].sequence < values[j].sequence })
		texts := make([]string, len(values))
		hints := make([]any, len(values))
		for i, v := range values {
			if i > 0 && v.sequence == values[i-1].sequence {
				return nil, runtimeErr("archive_prompt_order_invalid")
			}
			texts[i], hints[i] = v.text, v.hint
		}
		if sha256Hex([]byte(strings.Join(texts, "\n"))) == want {
			matches = append(matches, hints)
		}
	}
	switch {
	case len(matches) > 1:
		return nil, runtimeErr("archive_prompt_ambiguous")
	case len(matches) == 0:
		return nil, runtimeErr("archive_prompt_pending")
	case len(matches[0]) > 8:
		return nil, runtimeErr("archive_prompt_too_many_parts")
	}
	return matches[0], nil
}

// evidencePrepareZcode uploads the exact Zcode snapshot the prompt came from (when the
// server does not hold it yet) and returns the single user with verified refs.
func evidencePrepareZcode(locator any, users []any, path string, post evidencePost, scope map[string]any) (out []any, err error) {
	defer recovered(&err)
	l, err := evidenceValidateLocator(locator)
	if err != nil {
		return nil, err
	}
	device, _ := l.Get("device")
	if origin, _ := l.Get("origin"); origin != "zcode" || (device != "server" && device != "pc") || len(users) != 1 {
		return nil, runtimeErr("archive_locator_invalid")
	}
	sid, _ := l.Get("session_id")
	source := path
	if device == "pc" {
		source = filepath.Join(path, sha256Hex([]byte(sid.(string)))+".jsonl")
	}
	policy := newArchivePolicy(source)
	if err := evidencePolicyCheck(policy); err != nil {
		return nil, err
	}
	client := evidencePolicyClient(post, scope, policy)
	var refs []any
	body := func(snapshot string, observed float64, revision any) error {
		if err := policy.inspect(snapshot, "reconstructed_zcode"); err != nil {
			return err
		}
		if err := evidencePolicyCheck(policy); err != nil {
			return err
		}
		hints, err := evidenceZcodeHints(snapshot, l)
		if err != nil {
			return err
		}
		aid, _ := hints[0].(host.Obj).Get("archive_id")
		f, err := os.Open(snapshot)
		if err != nil {
			return err
		}
		hasher := sha256.New()
		n, err := io.Copy(hasher, io.LimitReader(f, archiveMaxBytes+1))
		f.Close()
		if err != nil {
			return err
		}
		if n > archiveMaxBytes {
			return runtimeErr("archive_snapshot_too_large")
		}
		sha := hex.EncodeToString(hasher.Sum(nil))
		same := func(header map[string]any) bool {
			for _, k := range []string{"origin", "device", "session_id"} {
				if v, _ := l.Get(k); !eq(header[k], v) {
					return false
				}
			}
			return true
		}
		header, err := client.post("get", map[string]any{"id": aid})
		var api *APIError
		if errors.As(err, &api) && api.Status == 404 {
			header, err = nil, nil
		}
		if err != nil {
			return err
		}
		if header != nil && (header["state"] != "active" || !same(header)) {
			return runtimeErr("archive_identity_mismatch")
		}
		if header == nil || header["sha256"] != sha {
			c := newCollector(client, nil, true)
			if err := c.upload(snapshot, device.(string), "zcode", sid.(string), "db-session", observed, "reconstructed_zcode", revision, source); err != nil {
				return err
			}
			if c.counts.n["committed"] == 0 {
				if c.counts.n["capture_skipped"] > 0 {
					return sessionWait("archive_capture_policy_wait")
				}
				return runtimeErr("archive_snapshot_unavailable")
			}
			if header, err = client.post("get", map[string]any{"id": aid}); err != nil {
				return err
			}
		}
		if header["state"] != "active" || header["sha256"] != sha || !same(header) {
			return runtimeErr("archive_identity_mismatch")
		}
		for _, hint := range hints {
			ref := append(host.Obj{}, hint.(host.Obj)...)
			ref.Set("sha256", sha)
			message, err := client.post("message", map[string]any{"ref": ref})
			if err != nil {
				return err
			}
			if message["role"] != "user" || message["authority"] != "data_only_untrusted" || !eq(message["ref"], ref) {
				return runtimeErr("archive_message_mismatch")
			}
			if privatePayload(message["text"]) {
				return sessionWait("session_archive_private_skipped")
			}
			refs = append(refs, ref)
		}
		return nil
	}
	if device == "pc" {
		err = pcZcodeSnapshot(path, sid.(string), func(target string, observed float64, before []any) error {
			return body(target, observed, before)
		})
	} else {
		err = zcodeSnapshot(path, sid.(string), body)
	}
	var missing zcodeMissing
	if errors.As(err, &missing) {
		return nil, runtimeErr("archive_prompt_pending")
	}
	if err != nil {
		return nil, err
	}
	user, ok := evidenceDict(users[0])
	if !ok {
		panic("user is not a dict")
	}
	user.Set("archive_refs", refs)
	return []any{user}, nil
}
