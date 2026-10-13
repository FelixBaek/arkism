package capture

// Capture for agents without hooks: read their own chat logs instead. Gemini CLI writes
// ~/.gemini/tmp/<project>/chats/session-*.jsonl; every new user message that the agent already
// answered becomes the same spool job a Stop hook makes, and `capture drain` takes it from there.
// The built-in Arkism runs this every minute with ARKISM_ORIGIN=gemini.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// logsLookback is how far back the first run reaches: older conversations are history, not new
// memories, and reading them all would spend the user's model quota at once.
const logsLookback = 3 * time.Hour

// logsSeenLimit caps the message ids remembered per chat file.
const logsSeenLimit = 4000

type logsState struct {
	Since int64               `json:"since"` // unix seconds of the first run
	Seen  map[string][]string `json:"seen"`  // chat file -> user message ids already queued
}

type logsMessage struct {
	id, typ, at, text string
}

// geminiChat is one chat file: its session id, folder and messages in order (a message written
// again under the same id replaces the earlier one).
func geminiChat(path string) (sid, cwd string, msgs []logsMessage, err error) {
	byID := map[string]int{}
	take := func(d map[string]any) {
		if truthy(d["sessionId"]) && d["type"] == nil {
			sid = str(d["sessionId"])
			if dirs, ok := d["directories"].([]any); ok && len(dirs) > 0 {
				cwd = sstr(dirs[0])
			}
			return
		}
		id, typ := sstr(d["id"]), sstr(d["type"])
		if id == "" || (typ != "user" && typ != "gemini") {
			return
		}
		m := logsMessage{id: id, typ: typ, at: sstr(d["timestamp"]), text: geminiText(d["content"])}
		if i, ok := byID[id]; ok {
			msgs[i] = m
			return
		}
		byID[id] = len(msgs)
		msgs = append(msgs, m)
	}
	if strings.HasSuffix(path, ".jsonl") {
		err = usageLines(path, take)
	} else {
		var raw []byte
		if raw, err = os.ReadFile(path); err == nil {
			var doc map[string]any
			if json.Unmarshal(raw, &doc) == nil {
				list, _ := doc["messages"].([]any)
				delete(doc, "messages")
				take(doc)
				for _, m := range list {
					if d, ok := m.(map[string]any); ok {
						take(d)
					}
				}
			}
		}
	}
	if sid == "" {
		sid = usageStem(path)
	}
	return sid, cwd, msgs, err
}

// geminiText is the typed text of a message: a string, or the text parts of a part list.
func geminiText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, p := range c {
			if m, ok := p.(map[string]any); ok && truthy(m["text"]) {
				parts = append(parts, sstr(m["text"]))
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// logsCapture queues the new, answered user messages of every Gemini CLI chat.
func logsCapture(root string, now time.Time) (queued int, err error) {
	return logsCaptureFrom(root, "session-*.json*", geminiChat, now)
}

// logsCaptureFrom queues the new, answered user messages of every chat file under root matching
// pattern; read gives a file's session id, folder and messages in order ("user" is the person,
// any other type an answer).
func logsCaptureFrom(root, pattern string, read func(string) (string, string, []logsMessage, error), now time.Time) (queued int, err error) {
	if capAgentOff(capOrigin) {
		return 0, nil
	}
	if err := os.MkdirAll(sessionsDir(), 0o700); err != nil {
		return 0, err
	}
	statePath := filepath.Join(sessionsDir(), "logs-"+capOrigin+".json")
	state := logsState{Seen: map[string][]string{}}
	if raw, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(raw, &state)
	}
	if state.Seen == nil {
		state.Seen = map[string][]string{}
	}
	if state.Since == 0 {
		state.Since = now.Add(-logsLookback).Unix()
	}
	since := time.Unix(state.Since, 0)
	live := map[string]bool{}
	for _, path := range globAll(root, pattern) {
		live[path] = true
		fi, err := os.Stat(path)
		if err != nil || fi.ModTime().Before(since) {
			continue
		}
		sid, cwd, msgs, err := read(path)
		if err != nil || captureSkipped(cwd) {
			continue
		}
		seen := state.Seen[path]
		var users []any
		var ids []string
		for i, m := range msgs {
			if m.typ != "user" || slices.Contains(seen, m.id) {
				continue
			}
			// Only answered messages: the turn is over, as at a Stop hook.
			answered := slices.ContainsFunc(msgs[i+1:], func(x logsMessage) bool { return x.typ != "user" })
			at, perr := time.Parse(time.RFC3339Nano, m.at)
			if !answered || perr == nil && at.Before(since) {
				if answered {
					ids = append(ids, m.id) // older than the first run: remembered, not captured
				}
				continue
			}
			ids = append(ids, m.id)
			if text := clean(m.text); text != "" {
				users = append(users, map[string]any{"at": or(m.at, host.NowISO()), "text": head(text, 4000)})
			}
		}
		if len(users) > 0 {
			turns := int64(len(seen) + len(ids))
			job := map[string]any{"type": "turn_completed", "session_id": sid, "cwd": cwd, "users": users, "turns": turns,
				"event_id": fmt.Sprintf("%s:%s:turn_completed:%d", strings.ReplaceAll(capOrigin, "_", "-"), sid, turns), "queued_at": host.NowISO()}
			if err := enqueue(job); err != nil {
				return queued, err
			}
			queued += len(users)
		}
		if len(ids) > 0 {
			seen = append(seen, ids...)
			if len(seen) > logsSeenLimit {
				seen = seen[len(seen)-logsSeenLimit:]
			}
			state.Seen[path] = seen
		}
	}
	for path := range state.Seen {
		if !live[path] {
			delete(state.Seen, path) // the chat file is gone
		}
	}
	raw, _ := json.Marshal(state)
	return queued, atomicWrite(statePath, string(raw))
}

// logsMain is `arkism capture logs`: Gemini CLI's chats under ~/.gemini/tmp, or with
// ARKISM_ORIGIN=grok Grok's under ~/.grok/sessions.
func logsMain() error {
	// The caller drains the queue next (the built-in Arkism runs `capture drain` right after).
	var err error
	if capOrigin == "grok" {
		_, err = logsCaptureFrom(filepath.Join(host.Home, ".grok", "sessions"), grokChatFile, grokChat, time.Now())
	} else if capOrigin == "deepseek" {
		_, err = logsCaptureFrom(deepseekSessions(), deepseekChatFile, deepseekChat, time.Now())
	} else {
		_, err = logsCapture(filepath.Join(host.Home, ".gemini", "tmp"), time.Now())
	}
	return err
}
