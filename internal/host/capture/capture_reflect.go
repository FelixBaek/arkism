package capture

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// ---------------------------------------------------------------- reflection
// Periodic self-check, after ReasoningBank (lessons from failures and the fixes that worked) and Hindsight
// (consolidate against what is already stored instead of piling up siblings). A lesson is approved on
// evidence, never on the model's own confidence: the user objected in the log, or it recurred.

const reflectPrompt = `You review a log of a session between a user and an AI coding agent and extract LESSONS for future sessions.
First think about where the work went wrong and what finally fixed it: the user objected or corrected the agent, a check
or tool failed and the approach had to change, work had to be redone, a claim turned out wrong.
A lesson is the reusable rule that would have prevented that mistake, or the recovery strategy that worked.
Rules:
- Only what this log shows, and only rules that will matter again in other tasks. Skip one-off facts, task progress,
  things that went fine the first time, and tool mechanics the tools already enforce (read before edit, exact paths).
- text: one standalone Korean sentence: <when it applies>, <what to do> (<what went wrong>). Understandable without this log.
- quote: an exact contiguous substring of the log that shows the mistake; prefer the user's own objection.
- action, checked against EXISTING LESSONS (A = in use, C = waiting for more evidence):
  new = no existing lesson covers it;
  confirm = the same situation and rule as target_id, seen again in this log;
  replace = target_id is an A lesson that is wrong or too vague, or the mistake happened again despite it; text is the fix.
  A difference in a condition, a negation or a number makes it a different lesson: new.
- When unsure the mistake was real, leave it out: a false lesson steers every future session.
` + riskRules + `
- At most 3. Never output secrets, tokens, passwords or keys. If nothing qualifies, return {"claims":[]}.`

const reflectSchema = `{"type": "object", "additionalProperties": false, "required": ["claims"], "properties": {"claims": {"type": "array", "items": {"type": "object", "additionalProperties": false, "required": ["subject", "text", "quote", "action", "target_id", "reason", "risk", "risk_reasons", "dup_hint"], "properties": {"subject": {"type": "string"}, "text": {"type": "string"}, "quote": {"type": "string"}, "action": {"type": "string", "enum": ["new", "confirm", "replace"]}, "target_id": {"anyOf": [{"type": "string"}, {"type": "null"}]}, "reason": {"type": "string"}, ` + riskProps + `}}}}}`

var (
	reflectModel = host.Env("ARKISM_REFLECT_MODEL", "sonnet")
	// The review model must not default to a third-party wrapper's model name.
	reviewModel = host.Env("ARKISM_REVIEW_MODEL", "sonnet")
)

const (
	reflectWindow = 3 * 3600 // the timer period; a session not touched within it is not read on first sight
	// shortcut: only the last 120k chars of a session window are read; chunk the log if early lessons get missed.
	reflectMaxChars = 120_000
	answerAll       = -1 // read_dialogue(answer_chars=None)
)

func reflectGenerator(origin string) host.Obj {
	return host.Obj{{Key: "model", Value: "claude-" + reflectModel}, {Key: "prompt_version", Value: "session-reflect-v2"},
		{Key: "pipeline_version", Value: strings.ReplaceAll(origin, "_", "-") + "-reflect-v1"}}
}

func cut(s string, n int) string {
	if n < 0 {
		return s
	}
	return head(s, n)
}

func toolError(block map[string]any) string {
	var c string
	switch x := block["content"].(type) {
	case string:
		c = x
	case []any:
		var parts []string
		for _, b := range x {
			if m, ok := b.(map[string]any); ok {
				parts = append(parts, str(or(m["text"], "")))
			}
		}
		c = strings.Join(parts, " ")
	}
	return "도구 실패: " + head(pyStrip(c), 300)
}

// recordLines are the log lines of one transcript record and how many of them the user typed.
func recordLines(d map[string]any, answerChars int) ([]string, int, error) {
	content, err := dget(or(d["message"], map[string]any{}), "content")
	if err != nil {
		return nil, 0, err
	}
	if truthy(d["isSidechain"]) || truthy(d["isMeta"]) {
		return nil, 0, nil
	}
	if d["type"] == "user" && !humanTyped(d) {
		return nil, 0, nil // a compact summary or relayed agent text is not the user objecting
	}
	if list, isList := content.([]any); d["type"] == "assistant" && isList {
		text, err := textBlocks(list)
		if err != nil {
			return nil, 0, err
		}
		if text = pyStrip(text); text != "" {
			return []string{"AI: " + cut(text, answerChars)}, 0, nil
		}
		return nil, 0, nil
	}
	if d["type"] == "user" {
		return userLines(content)
	}
	return nil, 0, nil
}

func userLines(content any) ([]string, int, error) {
	var blocks []any
	switch x := content.(type) {
	case string:
		blocks = []any{map[string]any{"type": "text", "text": x}}
	case []any:
		blocks = x
	case nil:
	default:
		return nil, 0, fmt.Errorf("%w: not iterable", errType)
	}
	var lines []string
	users := 0
	for _, v := range blocks {
		b, err := asDict(v)
		if err != nil {
			return nil, 0, err
		}
		if b["type"] == "text" {
			t, _ := or(b["text"], "").(string)
			if text := clean(t); text != "" {
				lines = append(lines, "사용자: "+head(text, 2000))
				users++
				continue
			}
		}
		if b["type"] == "tool_result" && truthy(b["is_error"]) {
			lines = append(lines, toolError(b))
		}
	}
	return lines, users, nil
}

// readDialogue is user turns, assistant text and failed tool results after offset, as log lines.
func readDialogue(path string, offset int64, answerChars int) ([]string, int, int64, string, error) {
	var lines []string
	users, entry := 0, ""
	end, err := eachLine(path, offset, func(raw []byte, _ int64) error {
		v, err := loadsBytes(raw)
		if err != nil {
			return nil
		}
		d, err := asDict(v)
		if err != nil {
			return err
		}
		if entry == "" {
			entry = str(or(d["entrypoint"], ""))
		}
		got, n, err := recordLines(d, answerChars)
		lines, users = append(lines, got...), users+n
		return err
	})
	return lines, users, end, entry, err
}

type readResult struct {
	lines    []string
	users    int
	end      int64
	headless bool
}

func readClaude(path string, start int64) (readResult, error) {
	lines, users, end, entry, err := readDialogue(path, start, 1500)
	return readResult{lines, users, end, strings.HasPrefix(entry, "sdk")}, err // headless `claude -p` runs
}

// codexItemLines are the log lines of one finished Codex item and how many the user typed.
func codexItemLines(it map[string]any, answerChars int) ([]string, int) {
	kind := it["type"]
	var parts []string
	if blocks, _ := it["content"].([]any); blocks != nil {
		for _, b := range blocks {
			if m, ok := b.(map[string]any); ok {
				parts = append(parts, str(or(m["text"], "")))
			}
		}
	}
	text := pyStrip(strings.Join(parts, " "))
	switch kind {
	case "UserMessage":
		// Codex Desktop puts attached files first and the typed request after this heading.
		// Private spans go first so the split cannot cut one open.
		pieces := strings.Split(redactPrivate(text), "## My request for Codex:")
		text = clean(pieces[len(pieces)-1])
		// A message that is one tag block (<in-app-browser…>, <send_user_message…>) is put there by the app.
		if text != "" && !strings.HasPrefix(text, "<") {
			return []string{"사용자: " + head(text, 2000)}, 1
		}
		return nil, 0
	case "AgentMessage":
		if text != "" {
			return []string{"AI: " + cut(text, answerChars)}, 0
		}
		return nil, 0
	case "CommandExecution", "McpToolCall":
		if it["status"] == "failed" {
			return []string{"도구 실패: " + head(pyStrip(str(or(it["aggregated_output"], it["error"], it["result"], ""))), 300)}, 0
		}
	}
	return nil, 0
}

// readCodex is readDialogue for a Codex rollout; `codex exec` and subagent threads are headless.
func readCodex(path string, offset int64) (readResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return readResult{}, err
	}
	first, err := bufio.NewReader(f).ReadBytes('\n')
	f.Close()
	if err != nil && err != io.EOF {
		return readResult{}, err
	}
	var headObj map[string]any
	if v, err := loadsBytes(first); err == nil {
		d, err := asDict(v)
		if err != nil {
			return readResult{}, err
		}
		headObj, _ = or(d["payload"], map[string]any{}).(map[string]any)
	}
	if _, nested := headObj["source"].(map[string]any); headObj["originator"] == "codex_exec" || nested {
		info, err := os.Stat(path)
		if err != nil {
			return readResult{}, err
		}
		return readResult{nil, 0, info.Size(), true}, nil
	}
	var lines []string
	users := 0
	end, err := eachLine(path, offset, func(raw []byte, _ int64) error {
		v, err := loadsBytes(raw)
		if err != nil {
			return nil
		}
		d, err := asDict(v)
		if err != nil {
			return err
		}
		p, _ := or(d["payload"], map[string]any{}).(map[string]any)
		if d["type"] == "event_msg" && p["type"] == "item_completed" {
			it, _ := or(p["item"], map[string]any{}).(map[string]any)
			got, n := codexItemLines(it, 1500)
			lines, users = append(lines, got...), users+n
		}
		return nil
	})
	return readResult{lines, users, end, false}, err
}

// readZcode is readDialogue for a ZCode session in its database; offsets are message sequence
// numbers. Only what the person typed counts as theirs (semantics.origin real_user).
func readZcode(sid string, start int64) (readResult, error) {
	res := readResult{end: start}
	db, err := openRO(capZcodeDB)
	if err != nil {
		return res, err
	}
	defer db.Close()
	type msg struct {
		id   any
		seq  int64
		data string
	}
	var msgs []msg
	err = eachRow(db, "SELECT id, sequence, data FROM message WHERE session_id = ? AND sequence >= ? ORDER BY sequence",
		[]any{sid, start}, func(_ []string, v []any) error {
			seq, _ := v[1].(int64)
			msgs = append(msgs, msg{v[0], seq, str(v[2])})
			return nil
		})
	if err != nil {
		return res, err
	}
	for _, m := range msgs {
		data, err := loadObj([]byte(m.data))
		if err != nil {
			return res, err
		}
		tm, _ := or(data["time"], map[string]any{}).(map[string]any)
		if data["role"] == "assistant" && !truthy(tm["completed"]) {
			break // still being written; the next run reads it whole
		}
		var parts []map[string]any
		id, _ := m.id.(string)
		if err := eachRow(db, "SELECT data FROM part WHERE message_id = ? ORDER BY sequence", []any{id}, func(_ []string, v []any) error {
			p, err := loadObj([]byte(str(v[0])))
			parts = append(parts, p)
			return err
		}); err != nil {
			return res, err
		}
		zcodeLines(&res, data, parts)
		res.end = m.seq + 1
	}
	return res, nil
}

// zcodeLines adds one ZCode message's dialogue lines to res: what the person typed, the
// assistant's text, and its failed tool calls.
func zcodeLines(res *readResult, data map[string]any, parts []map[string]any) {
	sem, _ := or(data["semantics"], map[string]any{}).(map[string]any)
	if data["role"] == "user" && sem["origin"] == "real_user" {
		for _, p := range parts {
			if p["type"] == "text" && !truthy(p["synthetic"]) {
				t, _ := or(p["text"], "").(string)
				if t = clean(t); t != "" {
					res.lines = append(res.lines, "사용자: "+head(t, 2000))
					res.users++
				}
			}
		}
	} else if data["role"] == "assistant" {
		var texts []string
		for _, p := range parts {
			if p["type"] == "text" {
				texts = append(texts, str(or(p["text"], "")))
			}
		}
		if text := pyStrip(strings.Join(texts, " ")); text != "" {
			res.lines = append(res.lines, "AI: "+head(text, 1500))
		}
		for _, p := range parts {
			st, _ := or(p["state"], map[string]any{}).(map[string]any)
			if p["type"] == "tool" && st["status"] == "error" {
				res.lines = append(res.lines, "도구 실패: "+head(pyStrip(str(or(st["error"], ""))), 300))
			}
		}
	}
}

// chat is one conversation reflect can read: key in reflect.json, which agent, session id, how
// far it goes (bytes, or messages for ZCode), when it last changed, and how to read it.
type chat struct {
	key, origin, sid string
	size             int64
	mtime            float64
	read             func(start int64) (readResult, error)
}

func stem(p string) string { return strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)) }

// chats are the conversations of every agent whose reflection is on.
func chats() ([]chat, error) {
	var out []chat
	add := func(pattern, origin string, read func(string, int64) (readResult, error)) error {
		paths, _ := filepath.Glob(pattern)
		sortPaths(paths)
		for _, p := range paths {
			info, err := os.Stat(p)
			if err != nil {
				return err
			}
			out = append(out, chat{p, origin, stem(p), info.Size(), mtime(info), func(start int64) (readResult, error) { return read(p, start) }})
		}
		return nil
	}
	if !capAgentOff("claude_code") {
		if err := add(filepath.Join(globEscape(capProjects), "*", "*.jsonl"), "claude_code", readClaude); err != nil {
			return nil, err
		}
	}
	if !capAgentOff("codex") {
		if err := add(filepath.Join(globEscape(capCodexSessions), "*", "*", "*", "*.jsonl"), "codex", readCodex); err != nil {
			return nil, err
		}
	}
	if !capAgentOff("zcode") && exists(capZcodeDB) {
		db, err := openRO(capZcodeDB)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		err = eachRow(db, "SELECT s.id, s.time_updated, MAX(m.sequence) FROM session s JOIN message m ON m.session_id = s.id "+
			"WHERE s.task_type = 'interactive' AND s.parent_id IS NULL GROUP BY s.id", nil, func(_ []string, v []any) error {
			sid := str(v[0])
			updated, _ := num(v[1])
			last, _ := v[2].(int64)
			out = append(out, chat{"zcode:" + sid, "zcode", sid, last + 1, updated / 1000, func(start int64) (readResult, error) { return readZcode(sid, start) }})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// tail is the last whole lines that fit in limit chars.
func tail(lines []string, limit int) []string {
	size, i := 0, len(lines)
	for i > 0 {
		size += host.Len(lines[i-1]) + 1
		if size > limit {
			break
		}
		i--
	}
	return lines[i:]
}

// lessons are active lessons (A) or ones waiting for evidence (C).
func lessons(status string) ([]map[string]any, error) {
	path := "/v1/memories"
	if status != "active" {
		path = "/v1/memory_candidates"
	}
	res, err := capGet(path+"?"+scopeQuery(true, "status", status, "kind", "lesson", "limit", "100"), 30*time.Second)
	if err != nil {
		return nil, err
	}
	return dicts(listOf(or(res["items"], []any{}))), nil
}

type checkedLesson struct {
	claim    host.Obj
	action   string
	target   map[string]any
	fromUser bool
}

// reflectionClaims are the checked lessons: a quote not in the log is the model's guess and is
// dropped; an unknown or mismatched target makes the lesson new.
func reflectionClaims(raw []any, lines []string, active, waiting []map[string]any) ([]checkedLesson, error) {
	log := norm(strings.Join(lines, "\n"))
	users := personLines(lines)
	activeIDs := map[any]bool{}
	for _, r := range active {
		activeIDs[hashable(r["id"])] = true
	}
	known := map[any]map[string]any{}
	for _, r := range append(append([]map[string]any{}, active...), waiting...) {
		known[hashable(r["id"])] = r
	}
	var out []checkedLesson
	for _, v := range raw {
		if len(out) == 3 {
			break
		}
		c, err := asDict(v)
		if err != nil {
			return nil, err
		}
		text, quote := pyStrip(str(or(c["text"], ""))), norm(str(or(c["quote"], "")))
		action, _ := c["action"].(string)
		if text == "" || quote == "" || !strings.Contains(log, quote) {
			continue
		}
		var target map[string]any
		if id := hashable(c["target_id"]); id != nil {
			target = known[id]
		}
		if action != "confirm" && action != "replace" || target == nil || action == "replace" && !activeIDs[hashable(target["id"])] {
			action, target = "new", nil
		}
		claim := host.Obj{{Key: "subject", Value: head(or(pyStrip(str(or(c["subject"], "general"))), "general").(string), 120)},
			{Key: "text", Value: head(text, 4000)}, {Key: "kind", Value: "lesson"}, {Key: "basis", Value: "inferred"},
			{Key: "reason", Value: head(str(or(c["reason"], "")), 500)}}
		risk := riskFields(c, "inferred", quote)
		if _, has := risk.Get("dup_hint"); target != nil && !has { // a confirm or replace names its lesson
			risk.Set("dup_hint", str(target["id"]))
		}
		claim = append(claim, risk...)
		if action == "replace" && target["revision"] != nil { // a suggestion: the old lesson stays in use until a person replaces it
			claim = append(claim, host.KV{Key: "supersedes", Value: host.Obj{{Key: "collection", Value: "memories"}, {Key: "id", Value: target["id"]}, {Key: "revision", Value: target["revision"]}}})
		}
		out = append(out, checkedLesson{claim, action, target, quotedByUser(users, quote)})
	}
	return out, nil
}

// personLines are the person's lines of the log, normalized.
func personLines(lines []string) []string {
	var users []string
	for _, l := range lines {
		if strings.HasPrefix(l, "사용자: ") {
			users = append(users, norm(l))
		}
	}
	return users
}

// quotedByUser says the quote comes from something the person said.
func quotedByUser(users []string, quote string) bool {
	fromUser := false
	for _, u := range users {
		if strings.Contains(u, quote) {
			fromUser = true
		}
	}
	return fromUser
}

func candidatePath(cid any) string {
	return fmt.Sprintf("/v1/memory_candidates/%s?%s", str(cid), scopeQuery(false))
}
func memoryPath(mid any) string {
	return fmt.Sprintf("/v1/memories/%s?%s", str(mid), scopeQuery(false))
}

// ---------------------------------------------------------------- trial
// Memories an earlier version approved on trial: prompt search marks them "시험 중", and one injected in trialRetireAt
// sessions without any answer using it leaves the trial as unused, unless keptOnTrial; it stays active for a person to remove
// from the app's 안 쓰임 list. After trialDays it is an ordinary memory.
// Nothing new goes on trial: a person approves every candidate (ADR-0022).

const (
	trialDays     = 30
	trialRetireAt = 5 // sessions, not injections: one session repeating it is one chance to be used
)

// restraintRE finds a rule about what not to do or what is preferred: following one leaves no words in the answer.
// 말고도, 피해 범위, 빠짐없이, 마지막, 뿐·만이 아니라, 안 됐다고, whenever and preferences are not restraints.
var restraintRE = regexp.MustCompile(`않(는다|게|도록|고|아야|으며|기로)|말(고(?:[^도]|$)|\s?것|아야|라)|금지|피(한다|하고|해야|해서)|` +
	`지\s?마(?:라|세요|십시오|\s|[,.]|$)|안\s?된다|(?:^|\s)안\s\S*[^았었였했됐렸]다(?:[\s,.]|$)|선호|대신|(?:[^만]이|가)\s아니라|\s없이|(?:것|뒤에|끼리|로)만\s|보다\s.*먼저|` +
	`(?i:\b(?:never|don['’]?t|do not|avoid(?:s|ing)?|must not|prefer(?:s|red)?)\b)`)

// keptOnTrial says a trial memory is never retired as unused, only graduated: standing rules (user, procedural)
// and prohibitions or preferences, which word overlap cannot see being followed.
// shortcut: a phrase match on the rule; tag restraints at reflection if it misses real ones.
func keptOnTrial(m map[string]any) bool {
	if k := m["kind"]; k == "user" || k == "procedural" {
		return true
	}
	rule := strings.TrimRight(str(or(m["text"], "")), ". ")
	if i := strings.LastIndex(rule, " ("); i >= 0 && strings.HasSuffix(rule, ")") && pastTense(rule[i+2:len(rule)-1]) {
		rule = rule[:i] // a trailing note in the past tense tells what went wrong; its negations are not the rule
	}
	return restraintRE.MatchString(rule)
}

// pastTense is a Korean sentence ending in a ㅆ-final syllable before 다 (했다, 됐다, 막혔다): an incident, not a directive.
func pastTense(s string) bool {
	r := []rune(strings.TrimRight(s, ". "))
	n := len(r)
	return n >= 2 && r[n-1] == '다' && r[n-2] >= '가' && r[n-2] <= '힣' && (r[n-2]-'가')%28 == 20
}

func trialState() host.Obj {
	raw, err := os.ReadFile(filepath.Join(capState, "trial.json"))
	if err != nil {
		return host.Obj{}
	}
	v, err := loadsOrdered(raw)
	if err != nil {
		return host.Obj{}
	}
	o, ok := v.(host.Obj)
	if !ok {
		return host.Obj{} // shortcut: Python would fail later on a non-dict trial.json; treat it as empty
	}
	return o
}

func trialUpdate(change func(st *host.Obj)) error {
	lock, err := lockFile(filepath.Join(capState, "trial.lock"), true)
	if err != nil {
		return err
	}
	defer lock.Close()
	st := trialState()
	change(&st)
	return atomicWrite(filepath.Join(capState, "trial.json"), host.MustDumps(st, host.DumpOpts{}))
}

func trialPop(cid string) error {
	return trialUpdate(func(st *host.Obj) {
		for i, kv := range *st {
			if kv.Key == cid {
				*st = append((*st)[:i:i], (*st)[i+1:]...)
				return
			}
		}
	})
}

// trialVerdicts is {id: "retire" | "graduate"} for trial memories that are decided. keep is asked only about
// one due for retirement; a kept one waits to graduate instead.
func trialVerdicts(state host.Obj, counts map[string][3]int, today time.Time, keep func(string) bool) (host.Obj, error) {
	out := host.Obj{}
	for _, e := range state {
		n := counts[e.Key]
		if n[0] >= trialRetireAt && n[1] == 0 && n[2] == 0 && !keep(e.Key) { // another agent may have used it: never retire blind
			out.Set(e.Key, "retire")
			continue
		}
		since, isStr := e.Value.(string)
		ts, aware, ok := fromISO(since)
		if !isStr || !ok {
			return nil, valueErr("Invalid isoformat string")
		}
		if !aware {
			return nil, fmt.Errorf("%w: can't subtract offset-naive and offset-aware datetimes", errType)
		}
		if math.Floor((float64(today.UnixNano())/1e9-ts)/86400) >= trialDays {
			out.Set(e.Key, "graduate")
		}
	}
	return out, nil
}

// trialReview drains the legacy trial list: it reports unused memories and graduates old ones, writing nothing.
// shortcut: `used` is word overlap, so a lesson followed without being restated can read as unused;
// trialRetireAt is high for that reason; add an LLM judge if it bites.
func trialReview() error {
	for _, e := range trialState() {
		cid := e.Key
		err := func() error {
			cand, err := capGet(candidatePath(cid), 8*time.Second)
			if err != nil {
				return err
			}
			if cand["status"] != "approved" { // one still waiting is a person's to decide (ADR-0022)
				return trialPop(cid)
			}
			return nil
		}()
		if err != nil {
			if !networkish(err) {
				return err
			}
			metric(kv("event", "trial_failure"), kv("memory_id", cid), kv("error", errorCode(err)))
		}
	}
	state := trialState()
	if len(state) == 0 {
		return nil
	}
	since := ""
	for i, e := range state {
		if s := str(e.Value); i == 0 || s < since {
			since = s
		}
	}
	counts, _, err := host.UtilityCounts(since)
	if err != nil {
		return err
	}
	unread := map[string]bool{}
	verdicts, err := trialVerdicts(state, counts, time.Now().UTC(), func(mid string) bool {
		m, err := capGet(memoryPath(mid), 8*time.Second)
		if err != nil {
			if host.HTTPCode(err) == 404 {
				return false // deleted: leaves the trial below
			}
			unread[mid] = true
			metric(kv("event", "trial_failure"), kv("memory_id", mid), kv("error", errorCode(err)))
			return true
		}
		return m["status"] == "active" && keptOnTrial(m) // an expired or superseded one leaves the trial as before
	})
	if err != nil {
		return err
	}
	for _, v := range verdicts {
		mid, verdict := v.Key, v.Value.(string)
		if unread[mid] {
			continue // neither retired nor graduated on a guess; decided on a later run
		}
		if verdict == "retire" {
			verdict = "unused" // a person removes it from the 안 쓰임 list; the AI never expires a memory (ADR-0022)
		}
		metric(kv("event", "trial_"+verdict), kv("memory_id", mid), kv("injected", counts[mid][0]))
		if err := trialPop(mid); err != nil {
			return err
		}
	}
	return nil
}

// networkish is Python's (urllib.error.URLError, OSError, RuntimeError): anything but a
// programming or data-shape error.
func networkish(err error) bool {
	switch pyType(err) {
	case "KeyError", "TypeError", "AttributeError", "ValueError", "JSONDecodeError", "TimeoutExpired":
		return false
	}
	return true
}

// applyLessons stores new and replacing lessons as candidates with their risk call; a person approves them
// (ADR-0022). A confirm only counts the recurrence on the lesson (proof_count): approving on it would be the AI deciding.
func applyLessons(checked []checkedLesson, eventID, sid, origin string) error {
	claims := []any{}
	for _, c := range checked {
		if c.action == "confirm" {
			metric(kv("event", "reflection_confirmed"), kv("target_id", c.target["id"]), kv("same_session", containsAny(listOf(c.target["tags"]), "session:"+sid)))
			confirmLesson(c.target)
			continue
		}
		claims = append(claims, c.claim)
	}
	if len(claims) == 0 {
		return nil
	}
	_, err := sendCapture("session_end", eventID, sid, host.NowISO(), claims, origin, 0, reflectGenerator(origin))
	return err
}

// confirmLesson raises the lesson's proof count on the server; a failure is a metric line, never a stopped reflection.
func confirmLesson(target map[string]any) {
	col := "memory_candidates"
	if target["status"] == "active" {
		col = "memories"
	}
	if _, err := capPost(fmt.Sprintf("/v1/%s/%s/confirm", col, str(target["id"])), host.Obj{{Key: "scope", Value: capScope()}}, 30*time.Second); err != nil {
		metric(kv("event", "reflection_confirm_failure"), kv("target_id", target["id"]), kv("error", errorCode(err)))
	}
}

type existingLessons struct {
	active, waiting []map[string]any
	known           string
}

// reflectSession reflects on one conversation from start; ok false means reread later.
func reflectSession(c chat, start int64, existing existingLessons, dryRun bool, stdout io.Writer) (end int64, ok bool, err error) {
	defer recovered(&err)
	r, err := c.read(start)
	if err != nil {
		return 0, false, err
	}
	if r.headless {
		return r.end, true, nil // `claude -p`, `codex exec` and subagent threads are pipelines, not conversations
	}
	if r.users < 2 {
		return 0, false, nil // too little yet; read again with what follows
	}
	lines, _ := privateData(r.lines).([]string)
	kept := tail(lines, reflectMaxChars)
	out, err := privateModel(redactPrivate(withGuidance(reflectPrompt)), "EXISTING LESSONS:\n"+redactPrivate(existing.known)+"\n\nSESSION LOG:\n"+strings.Join(kept, "\n"), reflectModel,
		600*time.Second, reflectSchema, "claims")
	if err != nil {
		return 0, false, err
	}
	raw, err := asList(out)
	if err != nil {
		return 0, false, err
	}
	checked, err := reflectionClaims(raw, kept, existing.active, existing.waiting)
	if err != nil {
		return 0, false, err
	}
	metric(kv("event", "reflection"), kv("origin", c.origin), kv("session", head(c.sid, 12)), kv("lessons", len(checked)))
	if dryRun {
		list := []any{}
		for _, l := range checked {
			var target any
			if l.target != nil {
				target = l.target["id"]
			}
			list = append(list, host.Obj{{Key: "claim", Value: l.claim}, {Key: "action", Value: l.action}, {Key: "target", Value: target}, {Key: "from_user", Value: l.fromUser}})
		}
		fmt.Fprintln(stdout, dumpsIndent(host.Obj{{Key: "origin", Value: c.origin}, {Key: "session", Value: c.sid}, {Key: "lessons", Value: list}}, true))
		return 0, false, nil
	}
	err = applyLessons(checked, fmt.Sprintf("%s:%s:reflect:%d", strings.ReplaceAll(c.origin, "_", "-"), c.sid, r.end), c.sid, c.origin)
	return r.end, err == nil, err
}

func reflectRun(dryRun bool, stdout io.Writer) error {
	if err := os.MkdirAll(capState, 0o700); err != nil {
		return err
	}
	stateFile := filepath.Join(capState, "reflect.json")
	lock, err := lockFile(filepath.Join(capState, "reflect.lock"), false)
	if errors.Is(err, errBusy) {
		return nil
	} else if err != nil {
		return err
	}
	defer lock.Close()
	state, err := loadReflectState(stateFile)
	if err != nil {
		return err
	}
	active, err := lessons("active")
	if err != nil {
		return err
	}
	waiting, err := lessons("candidate")
	if err != nil {
		return err
	}
	existing := existingLessons{active, waiting, knownLessons(active, waiting)}
	all, err := chats()
	if err != nil {
		return err
	}
	for _, c := range all {
		seenV, seen := state.Get(c.key)
		seenAt, _ := num(seenV)
		if float64(c.size) <= seenAt || !seen && c.mtime < timeNow()-reflectWindow {
			continue
		}
		end, ok, err := reflectSession(c, int64(seenAt), existing, dryRun, stdout)
		if err != nil { // one bad session must not stop the rest; it is retried next run
			metric(kv("event", "reflection_failure"), kv("origin", c.origin), kv("session", head(c.sid, 12)), kv("error", errorCode(err)))
			continue
		}
		if dryRun {
			continue
		}
		if !ok {
			if !seen {
				state.Set(c.key, 0) // known now, so it is reread when it grows even after the window
			}
			v, _ := state.Get(c.key)
			n, _ := num(v)
			end = int64(n)
		}
		state.Set(c.key, end)
		tmp := strings.TrimSuffix(stateFile, ".json") + ".tmp" // a kill mid-write must not leave a torn state file
		if err := os.WriteFile(tmp, []byte(host.MustDumps(state, host.DumpOpts{})), 0o666); err != nil {
			return err
		}
		if err := os.Rename(tmp, stateFile); err != nil {
			return err
		}
	}
	if dryRun {
		return nil
	}
	if err := trialReview(); err != nil {
		return err
	}
	return classifyWaiting(stdout)
}

// loadReflectState reads reflect.json: how far each chat was reflected on (empty when absent).
func loadReflectState(stateFile string) (host.Obj, error) {
	state := host.Obj{}
	if raw, err := os.ReadFile(stateFile); err == nil {
		v, err := loadsOrdered(raw)
		if err != nil {
			return nil, err
		}
		if o, isObj := v.(host.Obj); isObj {
			state = o
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return state, nil
}

// knownLessons lists the active (A) and waiting (C) lessons for the reflection prompt.
func knownLessons(active, waiting []map[string]any) string {
	var known []string
	for _, g := range []struct {
		tag  string
		rows []map[string]any
	}{{"A", active}, {"C", waiting}} {
		for _, r := range g.rows {
			known = append(known, fmt.Sprintf("%s %s: %s", g.tag, str(r["id"]), str(or(r["text"], ""))))
			if _, has := r["text"]; !has {
				known[len(known)-1] = fmt.Sprintf("%s %s: ", g.tag, str(r["id"]))
			}
		}
	}
	return or(strings.Join(known, "\n"), "(none)").(string)
}

// classifyWaiting gives waiting candidates the AI advice the review screen shows; the next run retries a failure.
func classifyWaiting(stdout io.Writer) error {
	if err := aiReview(false, stdout); err != nil {
		if !networkish(err) && !errors.Is(err, errReviewerCredential) {
			return err
		}
		metric(kv("event", "ai_review_failure"), kv("error", errorCode(err)))
	}
	return nil
}

// ---------------------------------------------------------------- backfill / stats

// backfill imports plan rows {path, action, kind, subject, text}: rows with text are sent (IMPORT,
// or the non-duplicate remainder of a MERGE); the others are only reported.
func backfill(planPath string, stdout io.Writer) (err error) {
	defer recovered(&err)
	raw, err := os.ReadFile(planPath)
	if err != nil {
		return err
	}
	v, err := loadsOrdered(raw)
	if err != nil {
		return err
	}
	plan, err := asList(v)
	if err != nil {
		return err
	}
	trace := "claude-memory-backfill-" + time.Now().UTC().Format("20060102T150405Z")
	report := []any{}
	for _, r := range plan {
		row := r.(host.Obj)
		get := func(k string) any { v, _ := row.Get(k); return v }
		action := get("action")
		if _, has := row.Get("action"); !has {
			return fmt.Errorf("%w: 'action'", errKey)
		}
		if action != "IMPORT" && action != "MERGE" || !truthy(get("text")) {
			report = append(report, host.Obj{{Key: "path", Value: get("path")}, {Key: "action", Value: action}})
			continue
		}
		p := get("path").(string)
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		digest := sha256Hex(body)
		at := spoolMillis(mtime(info))
		claim := host.Obj{{Key: "subject", Value: get("subject")}, {Key: "text", Value: get("text")}, {Key: "kind", Value: get("kind")},
			{Key: "basis", Value: "explicit_user"}, {Key: "reason", Value: fmt.Sprintf("source=claude_memory path=%s sha256=%s trace=%s", filepath.Base(p), digest[:16], trace)}}
		res, err := capPost("/v1/capture", host.Obj{{Key: "scope", Value: capScope()}, {Key: "generator", Value: generator("claude-memory-backfill-v1")},
			{Key: "claims", Value: []any{claim}}, {Key: "event", Value: host.Obj{{Key: "id", Value: fmt.Sprintf("claude-memory:%s@%s", filepath.Base(p), digest[:16])},
				{Key: "type", Value: "backfill"}, {Key: "origin_product", Value: "claude_memory"}, {Key: "trace_id", Value: trace}, {Key: "occurred_at", Value: at}}}}, 60*time.Second)
		if err != nil {
			return err
		}
		item := listOf(res["items"])[0].(map[string]any)
		metric(kv("event", "backfill"), kv("status", item["status"]), kv("reason", item["reason"]), kv("candidate_id", item["candidate_id"]))
		out := host.Obj{{Key: "path", Value: get("path")}, {Key: "action", Value: action}, {Key: "sha256", Value: digest}, {Key: "occurred_at", Value: at}}
		keys := make([]string, 0, len(item))
		for k := range item {
			keys = append(keys, k)
		}
		sort.Strings(keys) // shortcut: the server's key order is lost in a Go map; sorted keys are stable
		for _, k := range keys {
			out.Set(k, item[k])
		}
		report = append(report, out)
	}
	fmt.Fprintln(stdout, dumpsIndent(report, true))
	return nil
}

// spoolMillis is datetime.fromtimestamp(at, utc).isoformat(timespec="milliseconds") with a Z.
func spoolMillis(at float64) string {
	return strings.Replace(archiveISO(at), "+00:00", "Z", 1)
}

func metricRows() ([]map[string]any, error) {
	f, err := os.Open(metricsFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	defer f.Close()
	var rows []map[string]any
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			row, lerr := loadObj(line)
			if lerr != nil {
				return nil, lerr
			}
			rows = append(rows, row)
		}
		if err == io.EOF {
			return rows, nil
		} else if err != nil {
			return nil, err
		}
	}
}

func countJSON(dir string) int {
	paths, _ := filepath.Glob(filepath.Join(globEscape(dir), "*.json"))
	return len(paths)
}

func stats(stdout io.Writer) error {
	rows, err := metricRows()
	if err != nil {
		return err
	}
	out := host.Obj{}
	for _, k := range []string{"capture_events", "candidates", "accepted", "merged", "rejected", "privacy_rejected", "skipped_no_cue",
		"no_claims", "writeback_failures", "hook_errors", "recalls", "recall_hits_from_capture", "transport_copies_expired", "expiry_receipt_failures", "session_validation_withheld"} {
		out.Set(k, int64(0))
	}
	out.Set("spool_pending", countJSON(spoolDir()))
	out.Set("failed_jobs", countJSON(failedDir()))
	bump := func(k string, by int64) {
		v, _ := out.Get(k)
		n, _ := v.(int64)
		out.Set(k, n+by)
	}
	var lat []float64
	latRaw := map[float64]any{}
	for _, r := range rows {
		st, ev := r["status"], r["event"]
		switch {
		case ev == "capture_copies_expired":
			n, _ := host.Int(r["count"])
			if n > 0 {
				bump("transport_copies_expired", n)
			}
		case ev == "capture_expiry_receipt_failed":
			bump("expiry_receipt_failures", 1)
		case ev == "session_validation_withheld":
			bump("session_validation_withheld", 1)
		case ev == "recall":
			bump("recalls", 1)
			n, _ := host.Int(or(r["captured"], 0))
			bump("recall_hits_from_capture", n)
		case ev == "writeback_failure":
			bump("writeback_failures", 1)
		case ev == "hook_error":
			bump("hook_errors", 1)
		case st == "skipped_no_cue" || st == "no_claims":
			bump(st.(string), 1)
		case truthy(st):
			bump("capture_events", 1)
			key := str(st)
			if key == "candidate" {
				key = "candidates"
			}
			if _, has := out.Get(key); has {
				bump(key, 1)
			}
			if r["reason"] == "secret_detected" {
				bump("privacy_rejected", 1)
			}
			if v := r["latency_ms"]; v != nil {
				f, _ := num(v)
				lat = append(lat, f)
				latRaw[f] = v
			}
		}
	}
	var p50 any
	if len(lat) > 0 {
		sort.Float64s(lat)
		p50 = latRaw[lat[len(lat)/2]]
	}
	out.Set("capture_latency_ms_p50", p50)
	fmt.Fprintln(stdout, dumpsIndent(out, false))
	return nil
}

var wordRE = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9._/-]{2,}|[가-힣]{2,}`)

// answerTextOverlap is a transcript heuristic, not evidence of actual memory use.
// An answer can repeat a rule it ignored, or follow one without repeating it.
// Standing SessionStart rules are outside this prompt-recall report.
func answerTextOverlap(memoryText, answer string) bool {
	words := map[string]bool{}
	for _, w := range wordRE.FindAllString(memoryText, -1) {
		words[w] = true
	}
	hits := 0
	for w := range words {
		r := []rune(w)
		if strings.Contains(answer, w) || len(r) >= 3 && r[0] >= '가' && r[0] <= '힣' && strings.Contains(answer, string(r[:len(r)-1])) {
			hits++
		}
	}
	return hits >= 2 && float64(hits) >= 0.4*float64(len(words))
}

// recalledBySession is the memory ids injected by prompt search per session, from the local metrics.
func recalledBySession(since string) (map[string][]string, []string, error) {
	rows, err := metricRows()
	if err != nil {
		return nil, nil, err
	}
	out := map[string][]string{}
	var order []string
	seen := map[string]map[string]bool{}
	for _, r := range rows {
		if since != "" && str(or(r["ts"], "")) < since {
			continue
		}
		ids, _ := r["ids"].([]any)
		if r["event"] == "recall" && r["hook"] == "UserPromptSubmit" && truthy(r["session"]) && len(ids) > 0 {
			sid := str(r["session"])
			if seen[sid] == nil {
				seen[sid] = map[string]bool{}
				order = append(order, sid)
			}
			for _, id := range ids {
				if s := str(id); !seen[sid][s] {
					seen[sid][s] = true
					out[sid] = append(out[sid], s)
				}
			}
		}
	}
	return out, order, nil
}

// memoryText is the memory's text, "" when it is gone, ok false when it could not be read.
func memoryText(mid string) (string, bool) {
	m, err := capGet(memoryPath(mid), 8*time.Second)
	if err != nil {
		if host.HTTPCode(err) == 404 {
			return "", true
		}
		return "", false
	}
	return str(or(m["text"], "")), true
}

const settled = 3600 // a session quiet this long has written its answers; a busier one may still use what it recalled

// utilityCounts is {memory id: [injected, answer text overlap, unread]} over settled sessions whose prompts
// recalled it (since an ISO time), and the session count. unread counts sessions whose answers
// this client cannot read (Codex, ZCode share the memories).
func utilityCounts(since string) (map[string][3]int, int, error) {
	bySession, order, err := recalledBySession(since)
	if err != nil {
		return nil, 0, err
	}
	type text struct {
		s  string
		ok bool
	}
	texts := map[string]text{}
	counts := map[string][3]int{}
	for _, sid := range order {
		ids := bySession[sid]
		matches, _ := filepath.Glob(filepath.Join(globEscape(capProjects), "*", globEscape(sid)+".jsonl"))
		if len(matches) == 0 {
			for _, mid := range ids {
				n := counts[mid]
				n[2]++
				counts[mid] = n
			}
			continue
		}
		info, err := os.Stat(matches[0])
		if err != nil {
			return nil, 0, err
		}
		if mtime(info) > timeNow()-settled {
			continue
		}
		lines, _, _, _, err := readDialogue(matches[0], 0, answerAll)
		if err != nil {
			return nil, 0, err
		}
		var answers []string
		for _, l := range lines {
			if strings.HasPrefix(l, "AI: ") {
				answers = append(answers, l)
			}
		}
		answer := norm(strings.Join(answers, " "))
		for _, mid := range ids {
			t, has := texts[mid]
			if !has {
				s, ok := memoryText(mid)
				t = text{s, ok}
				texts[mid] = t
			}
			if !t.ok {
				continue // unreadable now: counted as neither injected nor unused, so an outage never retires it
			}
			n := counts[mid]
			n[0]++
			if t.s != "" && answerTextOverlap(norm(t.s), answer) {
				n[1]++
			}
			counts[mid] = n
		}
	}
	return counts, len(bySession), nil
}

// utility reports transcript text overlap. The legacy used/use_rate/review_unused
// fields are compatibility aliases; neither overlap nor its absence measures use.
// Review candidates are informational and are never automatically demoted.
func utility(minInjected int, stdout io.Writer) error {
	counts, sessions, err := utilityCounts("")
	if err != nil {
		return err
	}
	injected, hit, unread := 0, 0, 0
	type row struct {
		id string
		n  int
	}
	var unused []row
	for mid, n := range counts {
		injected += n[0]
		hit += n[1]
		unread += n[2]
		if n[0] >= minInjected && n[1] == 0 {
			unused = append(unused, row{mid, n[0]})
		}
	}
	// shortcut: Python kept first-seen order among ties; ties here are ordered by id.
	sort.Slice(unused, func(i, j int) bool {
		if unused[i].n != unused[j].n {
			return unused[i].n > unused[j].n
		}
		return unused[i].id < unused[j].id
	})
	var rate any
	if injected > 0 {
		rate = math.Round(float64(hit)/float64(injected)*1000) / 1000
	}
	review := []any{}
	for _, u := range unused {
		t, _ := memoryText(u.id)
		review = append(review, host.Obj{{Key: "id", Value: u.id}, {Key: "injected", Value: u.n}, {Key: "text", Value: head(t, 80)}})
	}
	fmt.Fprintln(stdout, dumpsIndent(host.Obj{{Key: "measurement_basis", Value: "answer_text_overlap"},
		{Key: "actual_use_status", Value: "not_measured"}, {Key: "actual_used", Value: nil},
		{Key: "sessions", Value: sessions}, {Key: "memory_injections", Value: injected},
		{Key: "unread_session_pairs", Value: unread}, {Key: "text_overlap_count", Value: hit},
		{Key: "text_overlap_rate", Value: rate}, {Key: "review_without_text_overlap", Value: review},
		{Key: "used", Value: hit}, {Key: "use_rate", Value: rate}, {Key: "review_unused", Value: review}}, true))
	return nil
}

// dumpsIndent is json.dumps(v, indent=1), with ensure_ascii=False when noASCII.
func dumpsIndent(v any, noASCII bool) string {
	var b strings.Builder
	writeIndent(&b, v, host.DumpOpts{NoASCII: noASCII}, 0)
	return b.String()
}

func writeIndent(b *strings.Builder, v any, opt host.DumpOpts, depth int) {
	pad := "\n" + strings.Repeat(" ", depth+1)
	switch x := v.(type) {
	case host.Obj:
		if len(x) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{")
		for i, e := range x {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(pad + host.MustDumps(e.Key, opt) + ": ")
			writeIndent(b, e.Value, opt, depth+1)
		}
		b.WriteString("\n" + strings.Repeat(" ", depth) + "}")
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		o := make(host.Obj, len(keys))
		for i, k := range keys {
			o[i] = host.KV{Key: k, Value: x[k]}
		}
		writeIndent(b, o, opt, depth)
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[")
		for i, e := range x {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(pad)
			writeIndent(b, e, opt, depth+1)
		}
		b.WriteString("\n" + strings.Repeat(" ", depth) + "]")
	default:
		b.WriteString(host.MustDumps(v, opt))
	}
}
