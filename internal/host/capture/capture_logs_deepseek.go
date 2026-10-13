package capture

// DeepSeek CLI (dsh, github.com/deepseek-ai/deepseek-harness) keeps one folder per session:
// ~/.dsh/sessions/--<project path>--/<escaped session id>/session.vN.jsonl, zstd-compressed as
// session.vN.jsonl.zstd by default. An upgrade keeps older generations beside the new one and
// dsh reads only the highest; so does capture. One JSON row per event:
//
//	{"type":"session","version":4,"id","createdAt","cwd","delegationDepth",…}  the header (first row)
//	{"type":"user/message","seq","time","data":{"role":"user","id","source":{"kind":"user"},"content":[{"type":"text","text"}]}}
//	                    source kind "user" is what the person typed; other kinds (runtime-context,
//	                    reminders, relays from other agents) are the harness's own
//	{"type":"assistant/message",…}  the model, also mid-turn while it still calls tools
//	{"type":"turn/end","data":{"turn","reason":{"kind":"completed"}}}  the turn is answered
//
// time is unix milliseconds; message ids survive dsh's format migrations.

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/capture/dshzstd"
)

const deepseekChatFile = "session*.jsonl*"

// deepseekLogName is a canonical log name: generation 0 has no .vN, compression adds .zstd.
var deepseekLogName = regexp.MustCompile(`^session(?:\.v([1-9][0-9]*))?\.jsonl(\.zstd)?$`)

func deepseekSessions() string { return filepath.Join(host.Home, ".dsh", "sessions") }

// deepseekGeneration is a log file's format generation, or -1 for a name dsh does not read.
func deepseekGeneration(name string) int {
	m := deepseekLogName.FindStringSubmatch(name)
	if m == nil {
		return -1
	}
	if m[1] == "" {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return -1
	}
	return n
}

// deepseekChat reads one dsh session log as logs messages.
func deepseekChat(path string) (sid, cwd string, msgs []logsMessage, err error) {
	gen := deepseekGeneration(filepath.Base(path))
	if gen < 0 {
		return "", "", nil, os.ErrNotExist
	}
	dir := filepath.Dir(path)
	siblings, _ := filepath.Glob(filepath.Join(globEscape(dir), deepseekChatFile))
	for _, s := range siblings {
		if deepseekGeneration(filepath.Base(s)) > gen {
			return "", "", nil, os.ErrNotExist // superseded: dsh reads the newer generation
		}
	}
	sid = filepath.Base(dir)
	take := func(d map[string]any) {
		data, _ := d["data"].(map[string]any)
		switch d["type"] {
		case "session":
			if id := sstr(d["id"]); id != "" {
				sid = id
			}
			cwd = sstr(d["cwd"])
		case "user/message":
			source, _ := data["source"].(map[string]any)
			id := sstr(data["id"])
			if data["role"] != "user" || source["kind"] != "user" || id == "" {
				return
			}
			var at string
			if ms, ok := num(d["time"]); ok && ms > 0 {
				at = time.UnixMilli(int64(ms)).UTC().Format(time.RFC3339Nano)
			}
			msgs = append(msgs, logsMessage{id: id, typ: "user", at: at, text: geminiText(data["content"])})
		case "turn/end":
			if reason, _ := data["reason"].(map[string]any); reason["kind"] == "completed" {
				msgs = append(msgs, logsMessage{typ: "assistant"})
			}
		}
	}
	if filepath.Ext(path) != ".zstd" {
		err = usageLines(path, take)
		return sid, cwd, msgs, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return sid, cwd, msgs, err
	}
	// A frame still being written decodes as far as it goes; its partial last line is dropped.
	var out bytes.Buffer
	_, derr := out.ReadFrom(dshzstd.NewReader(bytes.NewReader(raw)))
	data := out.Bytes()
	if derr != nil {
		data = data[:bytes.LastIndexByte(data, '\n')+1]
	}
	for line := range bytes.Lines(data) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if v, jerr := loads(line); jerr == nil {
			if d, ok := v.(map[string]any); ok {
				take(d)
			}
		}
	}
	return sid, cwd, msgs, nil
}
