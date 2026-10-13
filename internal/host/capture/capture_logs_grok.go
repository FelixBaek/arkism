package capture

// Grok CLI keeps one folder per session: ~/.grok/sessions/<url-encoded cwd>/<session id>/ with
//
//	summary.json        {"info":{"id","cwd"},…}
//	chat_history.jsonl  the model's conversation, one message per line:
//	                    {"type":"user","prompt_index":N,"content":[{"type":"text","text":"<user_query>\n…\n</user_query>"}]}
//	                    is what the person typed; a user line with "synthetic_reason" (system reminders)
//	                    or without prompt_index (the <user_info> preamble) is the CLI's own;
//	                    {"type":"assistant","content":"…","tool_calls":[…]} is the model, and one
//	                    without tool_calls is the answer that ends a turn.
//	updates.jsonl       the session/update stream; user_message_chunk carries _meta.promptIndex and
//	                    the time the prompt was sent.
//
// chat_history.jsonl has no per-message ids or times, so a prompt is named by its index and
// text, and its time comes from updates.jsonl.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const grokChatFile = "chat_history.jsonl"

// grokChat reads one Grok session folder's chat_history.jsonl as logs messages.
func grokChat(path string) (sid, cwd string, msgs []logsMessage, err error) {
	dir := filepath.Dir(path)
	sid = filepath.Base(dir)
	if raw, rerr := os.ReadFile(filepath.Join(dir, "summary.json")); rerr == nil && len(raw) <= 1<<20 {
		if doc, derr := loadObj(raw); derr == nil {
			info, _ := doc["info"].(map[string]any)
			if id := sstr(info["id"]); id != "" {
				sid = id
			}
			cwd = sstr(info["cwd"])
		}
	}
	if cwd == "" {
		if folder, uerr := url.PathUnescape(filepath.Base(filepath.Dir(dir))); uerr == nil && filepath.IsAbs(folder) {
			cwd = folder
		}
	}
	times := grokPromptTimes(filepath.Join(dir, "updates.jsonl"))
	err = usageLines(path, func(d map[string]any) {
		switch d["type"] {
		case "user":
			index, isInt := num(d["prompt_index"])
			if !isInt || truthy(d["synthetic_reason"]) {
				return
			}
			text := grokQuery(geminiText(d["content"]))
			sum := sha256.Sum256([]byte(text))
			msgs = append(msgs, logsMessage{id: fmt.Sprintf("p%d:%s", int64(index), hex.EncodeToString(sum[:])[:12]),
				typ: "user", at: times[int64(index)], text: text})
		case "assistant":
			// Only the answer that ends the turn: a message still calling tools is mid-turn.
			if calls, _ := d["tool_calls"].([]any); len(calls) == 0 && strings.TrimSpace(geminiText(d["content"])) != "" {
				msgs = append(msgs, logsMessage{typ: "assistant"})
			}
		}
	})
	return sid, cwd, msgs, err
}

// grokQuery is the typed text inside Grok's <user_query> wrapper (the whole text without one).
func grokQuery(text string) string {
	trimmed := strings.TrimSpace(text)
	if body, ok := strings.CutPrefix(trimmed, "<user_query>"); ok {
		if i := strings.LastIndex(body, "</user_query>"); i >= 0 {
			return body[:i]
		}
		return body
	}
	return text
}

// grokPromptTimes maps prompt index to when it was sent (RFC 3339), from updates.jsonl; the last
// send of an index wins (a rewound prompt is sent again).
func grokPromptTimes(path string) map[int64]string {
	times := map[int64]string{}
	if !exists(path) {
		return times
	}
	_ = usageLines(path, func(d map[string]any) {
		params, _ := d["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		if update["sessionUpdate"] != "user_message_chunk" {
			return
		}
		meta, _ := update["_meta"].(map[string]any)
		index, ok := num(meta["promptIndex"])
		if !ok {
			return
		}
		pmeta, _ := params["_meta"].(map[string]any)
		if ms, ok := num(pmeta["agentTimestampMs"]); ok && ms > 0 {
			times[int64(index)] = time.UnixMilli(int64(ms)).UTC().Format(time.RFC3339Nano)
		} else if sec, ok := num(d["timestamp"]); ok && sec > 0 {
			times[int64(index)] = time.Unix(int64(sec), 0).UTC().Format(time.RFC3339Nano)
		}
	})
	return times
}
