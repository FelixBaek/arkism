package capture

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/prices"
)

// Usage collector: reads agent session logs and reports one
// count-only row per session to POST /v1/usage/sessions. Tokens, cost, tool/skill counts and
// timing only, never a prompt, an output or file content. Unchanged files are skipped.

const usageHelp = `usage: arkism usage-index [-h] [--execute] [--all]

사용량 수집기: reads agent session logs and reports one count-only row per
session to POST /v1/usage/sessions. Tokens, cost, tool/skill counts and timing
only — never a prompt, an output or file content. Plans by default; --execute
sends. Unchanged files are skipped.

options:
  -h, --help  show this help message and exit
  --execute
  --all       send every session, not only changed ones
`

const usageIdle = 300 // a gap longer than this (seconds) between messages is not active time

var (
	usageState      = or(os.Getenv("ARKISM_USAGE_STATE"), filepath.Join(host.Home, ".local/state/arkism-usage-index/state.json")).(string)
	usageCodexIndex = filepath.Join(host.Home, ".codex/session_index.jsonl")
	usageRoots      = usageSessionRoots
	usageSleep      = time.Sleep
	usageKST        = time.FixedZone("KST", 9*3600)
)

type usageTok struct{ Input, Output, CacheRead, CacheWrite, Requests int64 }

func (t usageTok) sum() int64 { return t.Input + t.Output + t.CacheRead + t.CacheWrite + t.Requests }

// usagePrice is the cost in dollars rounded like Python's round(x, 6), at the shared list
// prices (internal/prices). Unknown models stay unpriced instead of guessed.
func usagePrice(model string, t usageTok) (float64, bool) {
	p, ok := prices.Lookup(model)
	if !ok {
		return 0, false
	}
	x := (float64(t.Input)*p.In + float64(t.Output)*p.Out + float64(t.CacheWrite)*p.CacheWrite + float64(t.CacheRead)*p.CacheRead) / 1e6
	r, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', 6, 64), 64)
	return r, true
}

// usageTS is parse_ts as exact UTC microseconds.
func usageTS(v any) *int64 {
	ts, _, ok := fromISO(str(v))
	if !ok {
		return nil
	}
	us := int64(math.Round(ts * 1e6))
	return &us
}

// usageSeconds is datetime.timestamp() of microseconds: exactly us / 1e6 as a float.
func usageSeconds(us int64) float64 { return float64(us) / 1e6 }

type usageModel struct {
	Model string
	T     usageTok
}

type usageDay struct {
	Day    string
	Models []usageModel
}

type usagePair struct {
	K string
	N int64
}

// usageSession keeps every map as an ordered list: rows are sent in Python's dict order,
// and per-file results are kept in the JSON cache.
type usageSession struct {
	Agent, Device, Sid, Title, Project string
	Days                               []usageDay
	Stamps                             []int64 // microseconds
	Tools, Skills                      []usagePair
	Messages                           int64
}

func usageAdd(list *[]usagePair, k string, n int64) {
	for i := range *list {
		if (*list)[i].K == k {
			(*list)[i].N += n
			return
		}
	}
	*list = append(*list, usagePair{k, n})
}

func (s *usageSession) model(day, model string) *usageTok {
	var d *usageDay
	for i := range s.Days {
		if s.Days[i].Day == day {
			d = &s.Days[i]
		}
	}
	if d == nil {
		s.Days = append(s.Days, usageDay{Day: day})
		d = &s.Days[len(s.Days)-1]
	}
	for i := range d.Models {
		if d.Models[i].Model == model {
			return &d.Models[i].T
		}
	}
	d.Models = append(d.Models, usageModel{Model: model})
	return &d.Models[len(d.Models)-1].T
}

func (s *usageSession) tokens(when int64, model string, t usageTok) {
	if model == "" {
		model = "unknown"
	}
	d := s.model(time.UnixMicro(when).In(usageKST).Format("2006-01-02"), model)
	d.Input += max(0, t.Input)
	d.Output += max(0, t.Output)
	d.CacheRead += max(0, t.CacheRead)
	d.CacheWrite += max(0, t.CacheWrite)
	d.Requests += max(0, t.Requests)
}

func (s *usageSession) tool(name, skill string) {
	usageAdd(&s.Tools, name, 1)
	if skill != "" {
		usageAdd(&s.Skills, skill, 1)
	}
}

func (s *usageSession) merge(o *usageSession) {
	if s.Title == "" {
		s.Title = o.Title
	}
	if s.Project == "" {
		s.Project = o.Project
	}
	s.Stamps = append(s.Stamps, o.Stamps...)
	s.Messages += o.Messages
	for _, p := range o.Tools {
		usageAdd(&s.Tools, p.K, p.N)
	}
	for _, p := range o.Skills {
		usageAdd(&s.Skills, p.K, p.N)
	}
	for _, d := range o.Days {
		for _, m := range d.Models {
			t := s.model(d.Day, m.Model)
			t.Input += m.T.Input
			t.Output += m.T.Output
			t.CacheRead += m.T.CacheRead
			t.CacheWrite += m.T.CacheWrite
			t.Requests += m.T.Requests
		}
	}
}

func (s *usageSession) stamp(when *int64) {
	if when != nil {
		s.Stamps = append(s.Stamps, *when)
	}
}

func (s *usageSession) message(when *int64) {
	s.stamp(when)
	s.Messages++
}

// usageActive is Python's sum(min(b - a, IDLE) ...): ints add exactly until the first float,
// then every item is added with Neumaier compensation (CPython 3.12+ sum).
func usageActive(stamps []int64) int64 {
	var isum int64
	var f, c float64
	floating := false
	for i := 1; i < len(stamps); i++ {
		gap := usageSeconds(stamps[i]) - usageSeconds(stamps[i-1])
		isInt := gap > usageIdle
		x := gap
		if isInt {
			x = usageIdle
		}
		switch {
		case !floating && isInt:
			isum += usageIdle
		case !floating:
			f, floating = float64(isum)+x, true
		default:
			t := f + x
			if math.Abs(f) >= math.Abs(x) {
				c += (f - t) + x
			} else {
				c += (x - t) + f
			}
			f = t
		}
	}
	if !floating {
		return isum
	}
	if c != 0 && !math.IsInf(c, 0) && !math.IsNaN(c) {
		f += c
	}
	return int64(f)
}

// row is the session's count-only report, or nil when nobody answered (nothing to count).
func (s *usageSession) row() host.Obj {
	counted := false
	for _, d := range s.Days {
		for _, m := range d.Models {
			counted = counted || m.T.sum() != 0
		}
	}
	if len(s.Stamps) == 0 || !counted {
		return nil
	}
	stamps := append([]int64(nil), s.Stamps...)
	sort.Slice(stamps, func(i, j int) bool { return stamps[i] < stamps[j] })
	hours := make([]any, 168)
	counts := make([]int64, 168)
	for _, us := range stamps {
		k := time.UnixMicro(us).In(usageKST)
		counts[(int(k.Weekday())+6)%7*24+k.Hour()]++
	}
	for i, n := range counts {
		hours[i] = n
	}
	days := host.Obj{}
	for _, d := range s.Days {
		models := host.Obj{}
		for _, m := range d.Models {
			t := host.Obj{{Key: "input", Value: m.T.Input}, {Key: "output", Value: m.T.Output}, {Key: "cache_read", Value: m.T.CacheRead},
				{Key: "cache_write", Value: m.T.CacheWrite}, {Key: "requests", Value: m.T.Requests}}
			if c, ok := usagePrice(m.Model, m.T); ok {
				t = append(t, host.KV{Key: "cost_usd", Value: c})
			}
			models = append(models, host.KV{Key: m.Model, Value: t})
		}
		days = append(days, host.KV{Key: d.Day, Value: models})
	}
	pairs := func(list []usagePair) host.Obj {
		o := host.Obj{}
		for _, p := range list {
			o = append(o, host.KV{Key: p.K, Value: p.N})
		}
		return o
	}
	return host.Obj{{Key: "agent", Value: s.Agent}, {Key: "device", Value: s.Device}, {Key: "session_id", Value: s.Sid},
		{Key: "title", Value: head(s.Title, 300)}, {Key: "project", Value: head(s.Project, 200)},
		{Key: "started_at", Value: isoformat(time.UnixMicro(stamps[0]).UTC())},
		{Key: "ended_at", Value: isoformat(time.UnixMicro(stamps[len(stamps)-1]).UTC())},
		{Key: "messages", Value: s.Messages}, {Key: "active_seconds", Value: usageActive(stamps)}, {Key: "days", Value: days},
		{Key: "hours", Value: hours}, {Key: "tools", Value: pairs(s.Tools)}, {Key: "skills", Value: pairs(s.Skills)}}
}

var usageSep = regexp.MustCompile(`[/\\]`)

func usageProject(cwd string) string {
	cwd = strings.TrimRight(cwd, `/\`)
	if cwd == "" {
		return ""
	}
	parts := usageSep.Split(cwd, -1)
	return parts[len(parts)-1]
}

// usageStem is Path.stem.
func usageStem(path string) string {
	name := filepath.Base(path)
	if i := strings.LastIndex(name, "."); i > 0 && i < len(name)-1 {
		return name[:i]
	}
	return name
}

// usageText is `v or def` for a value used as text.
func usageText(v any, def string) string {
	if !truthy(v) {
		return def
	}
	return str(v)
}

// usageInt is int(v or 0) for a token count.
func usageInt(v any) int64 {
	if b, ok := v.(bool); ok && b {
		return 1
	}
	if n, ok := host.Int(v); ok {
		return n
	}
	if f, ok := num(v); ok {
		return int64(f)
	}
	return 0
}

// usageLines yields each JSON object line; other lines are skipped.
func usageLines(path string, fn func(d map[string]any)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if v, jerr := loads(line); jerr == nil {
				if d, ok := v.(map[string]any); ok {
					fn(d)
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// usageSeen is one Claude message's usage. Content blocks repeat a message's usage, and a
// resumed session copies earlier messages into its new file, so it is counted once by Key.
type usageSeen struct {
	Key   string
	Sid   string
	When  *int64
	Model string
	U     [4]int64 // input, output, cache read, cache write
}

// usageFile is one file's sessions and Claude message usage, before any cross-file merge.
type usageFile struct {
	Sig            [2]int64 // size, mtime ns
	Origin, Device string
	Sessions       []*usageSession
	Seen           []usageSeen
}

type usageParser struct {
	f     *usageFile
	index map[string]*usageSession
	seen  map[string]int
}

func (p *usageParser) get(sid string) *usageSession {
	if s, ok := p.index[sid]; ok {
		return s
	}
	s := &usageSession{Agent: p.f.Origin, Device: p.f.Device, Sid: sid}
	p.index[sid] = s
	p.f.Sessions = append(p.f.Sessions, s)
	return s
}

func (p *usageParser) claude(path string) error {
	return usageLines(path, func(d map[string]any) {
		sid := usageText(d["sessionId"], usageStem(path))
		s := p.get(sid)
		t := d["type"]
		if t == "custom-title" || t == "agent-name" {
			s.Title = usageText(or(d["customTitle"], d["agentName"]), s.Title)
			return
		}
		if t != "user" && t != "assistant" {
			return
		}
		when := usageTS(d["timestamp"])
		if truthy(d["cwd"]) && s.Project == "" {
			s.Project = usageProject(sstr(d["cwd"]))
		}
		m := obj(d["message"])
		if t == "user" {
			human := false
			switch c := m["content"].(type) {
			case string:
				human = true
			case []any:
				human = true
				for _, b := range c {
					if bm, ok := b.(map[string]any); ok && bm["type"] == "tool_result" {
						human = false
					}
				}
			}
			if human && !truthy(d["isMeta"]) && !truthy(d["isSidechain"]) {
				s.message(when)
			} else {
				s.stamp(when)
			}
			return
		}
		blocks, _ := m["content"].([]any)
		for _, b := range blocks {
			bm, ok := b.(map[string]any)
			if !ok || bm["type"] != "tool_use" {
				continue
			}
			name := usageText(bm["name"], "?")
			skill := ""
			if in, ok := bm["input"].(map[string]any); ok && name == "Skill" && truthy(in["skill"]) {
				skill = head(str(in["skill"]), 200)
			}
			s.tool(name, skill)
		}
		model := m["model"]
		if model == "<synthetic>" || !truthy(m["usage"]) {
			return
		}
		key := host.MustDumps(or(m["id"], d["uuid"]), host.DumpOpts{})
		i, dup := p.seen[key]
		if !dup || p.f.Seen[i].Sid == sid {
			u := obj(m["usage"])
			e := usageSeen{Key: key, Sid: sid, When: when, Model: usageText(model, ""), U: [4]int64{usageInt(u["input_tokens"]),
				usageInt(u["output_tokens"]), usageInt(u["cache_read_input_tokens"]), usageInt(u["cache_creation_input_tokens"])}}
			if dup {
				p.f.Seen[i] = e
			} else {
				p.seen[key] = len(p.f.Seen)
				p.f.Seen = append(p.f.Seen, e)
			}
		}
		s.stamp(when)
	})
}

func (p *usageParser) codex(path string) error {
	var s *usageSession
	model := ""
	var last *[3]int64
	err := usageLines(path, func(d map[string]any) {
		pl := obj(d["payload"])
		t, pt := d["type"], pl["type"]
		when := usageTS(d["timestamp"])
		if t == "session_meta" {
			s = p.get(usageText(pl["id"], usageStem(path)))
			if s.Project == "" {
				s.Project = usageProject(sstr(pl["cwd"]))
			}
			return
		}
		if s == nil {
			s = p.get(usageStem(path))
		}
		switch {
		case t == "turn_context" && truthy(pl["model"]):
			model = str(pl["model"])
		case pt == "thread_settings_applied":
			model = usageText(obj(pl["thread_settings"])["model"], model)
		case t == "event_msg" && pt == "user_message":
			s.message(when)
		case t == "response_item" && (pt == "function_call" || pt == "custom_tool_call"):
			s.tool(usageText(pl["name"], "?"), "")
			s.stamp(when)
		case t == "event_msg" && pt == "token_count" && when != nil:
			total := obj(obj(pl["info"])["total_token_usage"])
			if len(total) == 0 {
				return
			}
			cur := [3]int64{usageInt(total["input_tokens"]), usageInt(total["cached_input_tokens"]), usageInt(total["output_tokens"])}
			prev := [3]int64{}
			if last != nil {
				prev = *last
			}
			if cur[0] < prev[0] { // counter restarted (compaction or new thread): count from zero
				prev = [3]int64{}
			}
			di, dc, do := max(0, cur[0]-prev[0]), max(0, cur[1]-prev[1]), max(0, cur[2]-prev[2])
			last = &cur
			if di != 0 || do != 0 {
				s.tokens(*when, model, usageTok{Input: di - min(dc, di), CacheRead: min(dc, di), Output: do, Requests: 1})
			}
			s.stamp(when)
		}
	})
	if s != nil && s.Messages == 0 && len(s.Stamps) > 0 {
		s.Messages = 1
	}
	return err
}

// usageSessionRoots are the archive's session folders plus Gemini CLI's chats, which only the
// session list reads (Gemini sessions are not archived).
func usageSessionRoots() []archiveRoot {
	device := "server"
	if os.Getenv("ARKISM_PC_LOCAL") == "1" {
		device = "pc"
	}
	return append(archiveRoots(), archiveRoot{"gemini", device, filepath.Join(host.Home, ".gemini/tmp"), "session-*.json*"},
		archiveRoot{"zcode", device, filepath.Join(host.Home, ".zcode/cli/db"), "db.sqlite"},
		archiveRoot{"opencode", device, opencodeDataDir(), "opencode*.d[b]"}) // "[b]": a "*.db" glob is skipped here
}

// zcodeDB reads ZCode's own database (~/.zcode/cli/db/db.sqlite, read-only): each session's title,
// folder, user messages, tool calls and per-model tokens.
func (p *usageParser) zcodeDB(path string) error {
	db, err := openRO(path)
	if err != nil {
		return err
	}
	defer db.Close()
	sessions := map[string]*usageSession{}
	err = eachRow(db, "SELECT id, title, directory FROM session WHERE time_archived IS NULL ORDER BY time_created", nil, func(_ []string, v []any) error {
		s := p.get(str(v[0]))
		s.Title = usageText(v[1], "")
		s.Project = usageProject(sstr(v[2]))
		sessions[str(v[0])] = s
		return nil
	})
	if err != nil {
		return err
	}
	err = eachRow(db, "SELECT session_id, data FROM message ORDER BY session_id, time_created", nil, func(_ []string, v []any) error {
		s := sessions[str(v[0])]
		d, _ := loadsMap(sstr(v[1]))
		if s == nil || d == nil {
			return nil
		}
		var when *int64
		if ms := usageInt(obj(d["time"])["created"]); ms > 0 {
			us := ms * 1000
			when = &us
		}
		switch d["role"] {
		case "user":
			if truthy(d["synthetic"]) {
				s.stamp(when)
			} else {
				s.message(when)
			}
		case "assistant":
			t := obj(d["tokens"])
			cache := obj(t["cache"])
			tok := usageTok{Input: usageInt(t["input"]), Output: usageInt(t["output"]) + usageInt(t["reasoning"]),
				CacheRead: usageInt(cache["read"]), CacheWrite: usageInt(cache["write"]), Requests: 1}
			if when != nil && tok.sum() > 1 {
				s.tokens(*when, usageText(or(d["modelId"], d["modelID"]), ""), tok)
			}
			s.stamp(when)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return eachRow(db, "SELECT session_id, data FROM part WHERE json_extract(data, '$.type') = 'tool'", nil, func(_ []string, v []any) error {
		if s, d := sessions[str(v[0])], func() map[string]any { m, _ := loadsMap(sstr(v[1])); return m }(); s != nil && d != nil {
			s.tool(usageText(d["tool"], "?"), "")
		}
		return nil
	})
}

// loadsMap parses a JSON object; anything else is nil.
func loadsMap(text string) (map[string]any, error) {
	v, err := loads([]byte(text))
	if err != nil {
		return nil, err
	}
	m, _ := v.(map[string]any)
	return m, nil
}

// gemini reads a Gemini CLI chat: ~/.gemini/tmp/<project>/chats/session-*.jsonl, a metadata line
// ({sessionId, projectHash, startTime, directories …}) then one line per message, a message written
// again under the same id replacing the earlier one; $set/$patch/$rewindTo lines are bookkeeping.
// Older versions wrote the same record as one JSON document (.json).
func (p *usageParser) gemini(path string) error {
	var s *usageSession
	type msg struct {
		d    map[string]any
		when *int64
	}
	var order []string
	byID := map[string]msg{}
	take := func(d map[string]any) {
		if truthy(d["sessionId"]) && d["type"] == nil {
			s = p.get(str(d["sessionId"]))
			if dirs, ok := d["directories"].([]any); ok && len(dirs) > 0 && s.Project == "" {
				s.Project = usageProject(sstr(dirs[0]))
			}
			return
		}
		id, typ := str(or(d["id"], "")), d["type"]
		if id == "" || (typ != "user" && typ != "gemini") {
			return
		}
		if _, seen := byID[id]; !seen {
			order = append(order, id)
		}
		byID[id] = msg{d, usageTS(d["timestamp"])}
	}
	var err error
	if strings.HasSuffix(path, ".jsonl") {
		err = usageLines(path, take)
	} else {
		var raw []byte
		if raw, err = os.ReadFile(path); err == nil {
			var doc map[string]any
			if json.Unmarshal(raw, &doc) == nil {
				msgs, _ := doc["messages"].([]any)
				delete(doc, "messages")
				take(doc)
				for _, m := range msgs {
					if d, ok := m.(map[string]any); ok {
						take(d)
					}
				}
			}
		}
	}
	if s == nil {
		s = p.get(usageStem(path))
	}
	for _, id := range order {
		m := byID[id]
		if m.d["type"] == "user" {
			s.message(m.when)
			continue
		}
		if calls, ok := m.d["toolCalls"].([]any); ok {
			for _, c := range calls {
				s.tool(usageText(obj(c)["name"], "?"), "")
			}
		}
		if t := obj(m.d["tokens"]); len(t) > 0 && m.when != nil {
			// Gemini's input count includes the cached part; thinking is billed as output.
			input, cached := usageInt(t["input"]), usageInt(t["cached"])
			s.tokens(*m.when, usageText(m.d["model"], ""), usageTok{Input: max(0, input-cached), CacheRead: min(cached, input),
				Output: usageInt(t["output"]) + usageInt(t["thoughts"]), Requests: 1})
		}
		s.stamp(m.when)
	}
	return err
}

// generic reads other agents (Grok, Kiro, Qwen …): session time and message count only.
func (p *usageParser) generic(path string) error {
	sid := usageStem(path)
	if name := filepath.Base(path); name == "messages.jsonl" || name == "summary.json" {
		sid = filepath.Base(filepath.Dir(path))
	}
	s := p.get(sid)
	return usageLines(path, func(d map[string]any) {
		when := usageTS(or(d["timestamp"], d["created_at"], d["time"]))
		if role, typ := d["role"], d["type"]; role == "user" || role == "human" || typ == "user" || typ == "human" {
			s.message(when)
		} else {
			s.stamp(when)
		}
	})
}

func usageParse(origin, device, path string) (*usageFile, error) {
	p := &usageParser{f: &usageFile{Origin: origin, Device: device, Sessions: []*usageSession{}, Seen: []usageSeen{}},
		index: map[string]*usageSession{}, seen: map[string]int{}}
	var err error
	switch origin {
	case "claude_code":
		err = p.claude(path)
	case "codex":
		err = p.codex(path)
	case "gemini":
		err = p.gemini(path)
	case "zcode", "opencode": // OpenCode keeps the same session/message/part tables
		err = p.zcodeDB(path)
	default:
		err = p.generic(path)
	}
	return p.f, err
}

func usageCodexTitles() map[string]string {
	titles := map[string]string{}
	if exists(usageCodexIndex) {
		usageLines(usageCodexIndex, func(d map[string]any) {
			if id, ok := d["id"].(string); ok && id != "" && truthy(d["thread_name"]) {
				titles[id] = str(d["thread_name"])
			}
		})
	}
	return titles
}

type usageRow struct {
	key string
	row host.Obj
}

// usageCollect re-reads only files whose size or mtime changed and merges every file's
// cached result; cache is updated in place.
func usageCollect(state host.Obj, onlyChanged bool, cache map[string]*usageFile) (rows, changed []usageRow) {
	live := map[string]*usageFile{}
	for _, r := range usageRoots() {
		if !exists(r.root) || strings.HasSuffix(r.glob, ".db") || r.glob == "summary.json" {
			continue
		}
		paths := globAll(r.root, r.glob)
		if r.origin == "opencode" {
			// opencode.db sits at the top; the folders below hold file snapshots that can be huge.
			paths, _ = filepath.Glob(filepath.Join(globEscape(r.root), r.glob))
		}
		for _, path := range paths {
			fi, err := os.Stat(path)
			if err != nil {
				continue
			}
			sig := [2]int64{fi.Size(), fi.ModTime().UnixNano()}
			if wal, err := os.Stat(path + "-wal"); err == nil { // SQLite keeps recent writes beside the file
				sig = [2]int64{sig[0] + wal.Size(), max(sig[1], wal.ModTime().UnixNano())}
			}
			hit := cache[path]
			if hit == nil || hit.Sig != sig {
				f, err := usageParse(r.origin, r.device, path)
				if err != nil {
					continue
				}
				f.Sig = sig
				hit = f
			}
			live[path] = hit
		}
	}
	clear(cache)
	keys := make([]string, 0, len(live))
	for k, v := range live {
		cache[k] = v
		keys = append(keys, k)
	}
	sort.Strings(keys)

	type skey struct{ device, origin, sid string }
	index := map[skey]*usageSession{}
	var order []skey
	get := func(device, origin, sid string) *usageSession {
		k := skey{device, origin, sid}
		if s, ok := index[k]; ok {
			return s
		}
		s := &usageSession{Agent: origin, Device: device, Sid: sid}
		index[k] = s
		order = append(order, k)
		return s
	}
	type entry struct {
		device, origin string
		e              usageSeen
	}
	var seen []entry
	seenAt := map[string]bool{}
	for _, key := range keys { // first file wins a duplicated Claude message
		f := live[key]
		for _, part := range f.Sessions {
			get(f.Device, f.Origin, part.Sid).merge(part)
		}
		for _, e := range f.Seen {
			if !seenAt[e.Key] {
				seenAt[e.Key] = true
				seen = append(seen, entry{f.Device, f.Origin, e})
			}
		}
	}
	for _, x := range seen {
		if x.e.When != nil {
			u := x.e.U
			get(x.device, x.origin, x.e.Sid).tokens(*x.e.When, x.e.Model, usageTok{Input: u[0], Output: u[1], CacheRead: u[2], CacheWrite: u[3], Requests: 1})
		}
	}
	titles := usageCodexTitles()
	var sent host.Obj
	if v, ok := state.Get("sent"); ok {
		sent, _ = v.(host.Obj)
	}
	for _, k := range order {
		s := index[k]
		if s.Agent == "codex" {
			if t, ok := titles[s.Sid]; ok {
				s.Title = t
			}
		}
		r := s.row()
		if r == nil {
			continue
		}
		row := usageRow{k.device + "|" + k.origin + "|" + k.sid, r}
		rows = append(rows, row)
		if !onlyChanged {
			changed = append(changed, row)
			continue
		}
		if prev, _ := sent.Get(row.key); prev != usageDigest(r) {
			changed = append(changed, row)
		}
	}
	return rows, changed
}

func usageDigest(row host.Obj) string {
	return sha256Hex([]byte(host.MustDumps(row, host.DumpOpts{SortKeys: true})))[:16]
}

var usagePostRows = func(items []any) error {
	if _, err := os.ReadFile(host.TokenFile); err != nil {
		return err
	}
	s := host.Scope()
	scope := host.Obj{{Key: "workspace_id", Value: s["workspace_id"]}, {Key: "project_id", Value: s["project_id"]},
		{Key: "user_id", Value: s["user_id"]}, {Key: "agent_id", Value: s["agent_id"]}}
	payload := host.Obj{{Key: "scope", Value: scope}, {Key: "items", Value: items}}
	for attempt := 0; ; attempt++ {
		_, err := host.Post("/v1/usage/sessions", payload, 120*time.Second)
		if err == nil {
			return nil
		}
		if code := host.HTTPCode(err); code != 0 && code != 409 && code != 503 || attempt == 2 {
			return err
		}
		usageSleep(time.Duration(attempt+1) * 500 * time.Millisecond)
	}
}

func usageWrite(path, tmp string, data []byte) error {
	if err := os.WriteFile(tmp, data, 0o666); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func usageMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	execute, all := false, false
	var unknown []string
	for _, a := range args {
		switch {
		case a == "-h" || a == "--help":
			fmt.Fprint(stdout, usageHelp)
			return 0
		case len(a) > 2 && strings.HasPrefix("--execute", a): // argparse accepts unique prefixes
			execute = true
		case len(a) > 2 && strings.HasPrefix("--all", a):
			all = true
		default:
			unknown = append(unknown, a)
		}
	}
	if len(unknown) > 0 {
		fmt.Fprintf(stderr, "usage: arkism usage-index [-h] [--execute] [--all]\narkism usage-index: error: unrecognized arguments: %s\n", strings.Join(unknown, " "))
		return 2
	}
	dir := filepath.Dir(usageState)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintln(stderr, pyType(err)+":", err)
		return 1
	}
	lock, err := lockFile(filepath.Join(dir, "lock"), false)
	if err != nil {
		fmt.Fprintln(stderr, "BlockingIOError:", err)
		return 1
	}
	defer lock.Close()
	state := host.Obj{}
	if raw, err := os.ReadFile(usageState); err == nil {
		v, err := loadsOrdered(raw)
		if err != nil {
			fmt.Fprintln(stderr, "JSONDecodeError:", err)
			return 1
		}
		if o, ok := v.(host.Obj); ok {
			state = o
		}
	}
	// A JSON cache beside the state (the Python tool kept a pickle, files.pickle).
	cachePath := filepath.Join(dir, "files.json")
	cache := map[string]*usageFile{}
	if raw, err := os.ReadFile(cachePath); err == nil && json.Unmarshal(raw, &cache) != nil {
		cache = map[string]*usageFile{}
	}
	rows, changed := usageCollect(state, !all, cache)
	raw, err := json.Marshal(cache)
	if err == nil {
		err = usageWrite(cachePath, filepath.Join(dir, "files.tmp"), raw)
	}
	if err != nil {
		fmt.Fprintln(stderr, pyType(err)+":", err)
		return 1
	}
	out := host.Obj{{Key: "sessions", Value: len(rows)}, {Key: "changed", Value: len(changed)}, {Key: "sent", Value: 0}, {Key: "failed", Value: 0}}
	nsent, nfailed := 0, 0
	if execute {
		v, ok := state.Get("sent")
		sent, isObj := v.(host.Obj)
		if !ok || !isObj {
			sent = host.Obj{}
		}
		for i := 0; i < len(changed); i += 100 {
			batch := changed[i:min(i+100, len(changed))]
			items := make([]any, len(batch))
			for j, r := range batch {
				items[j] = r.row
			}
			if err := usagePostRows(items); err != nil {
				nfailed += len(batch)
				var code any = "unavailable"
				if c := host.HTTPCode(err); c != 0 {
					code = c
				}
				out.Set("error", code)
				continue
			}
			for _, r := range batch {
				sent.Set(r.key, usageDigest(r.row))
			}
			nsent += len(batch)
		}
		state.Set("sent", sent)
		if err := usageWrite(usageState, strings.TrimSuffix(usageState, filepath.Ext(usageState))+".tmp", []byte(host.MustDumps(state, host.DumpOpts{}))); err != nil {
			fmt.Fprintln(stderr, pyType(err)+":", err)
			return 1
		}
	}
	out.Set("sent", nsent)
	out.Set("failed", nfailed)
	fmt.Fprintln(stdout, host.MustDumps(out, host.DumpOpts{}))
	if nfailed != 0 {
		return 1
	}
	return 0
}

func init() { host.Register("usage-index", usageMain) }
