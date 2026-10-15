package capture

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	sqlite "github.com/ncruces/go-sqlite3"

	"github.com/FelixBaek/arkism/internal/host"
)

// Scoped Arkism archive collector. Plans by default; --execute writes via API only.

const archiveChunk = 1 << 20

var (
	archivePC    = host.Env("ARKISM_PC_SESSIONS", "/srv/arkism/session-inbox/pc-sessions")
	archiveState = or(os.Getenv("ARKISM_SESSION_STATE"), filepath.Join(host.Home, ".local/state/arkism-session-archive/state.json")).(string)
	archiveSleep = time.Sleep
)

type archiveRoot struct{ origin, device, root, glob string }

// archiveRoots lists the session folders; "server"/"pc" are device labels stored in archive ids.
func archiveRoots() []archiveRoot {
	h := host.Home
	pcClaude, pcCodex := filepath.Join(archivePC, "claude-projects"), filepath.Join(archivePC, "codex-sessions")
	if os.Getenv("ARKISM_PC_LOCAL") == "1" {
		// The hook runs on the PC itself against the app's built-in Arkism: the transcripts are
		// the agents' own folders, not a server's synced copy.
		pcClaude, pcCodex = capProjects, capCodexSessions
	}
	roots := []archiveRoot{
		{"claude_code", "server", capProjects, "*.jsonl"},
		{"codex", "server", capCodexSessions, "*.jsonl"},
		{"grok", "server", filepath.Join(h, ".grok/sessions"), "*.jsonl"},
		{"grok", "server", filepath.Join(h, ".grok/sessions"), "summary.json"},
		{"kiro", "server", filepath.Join(h, ".kiro/sessions"), "messages.jsonl"},
		{"qwen", "server", filepath.Join(h, ".qwen/projects"), "*.jsonl"},
		{"antigravity-cli", "server", filepath.Join(h, ".gemini/antigravity-cli/conversations"), "*.db"},
		{"claude_code", "pc", pcClaude, "*.jsonl"},
		{"codex", "pc", pcCodex, "*.jsonl"},
	}
	if os.Getenv("ARKISM_PC_LOCAL") == "1" {
		// On the PC the "server" Claude Code and Codex folders are the same folders as "pc";
		// keep one label so a session is not counted twice.
		kept := roots[:0]
		for _, r := range roots {
			if r.device == "server" && (r.origin == "claude_code" || r.origin == "codex") {
				continue
			}
			kept = append(kept, r)
		}
		roots = kept
	}
	return roots
}

func archiveIdentity(device, origin, sid, logical string) string {
	return sha256Hex([]byte(strings.Join([]string{device, origin, sid, logical}, "\x00")))
}

// archiveISO is isoformat(timespec='milliseconds') in UTC.
func archiveISO(at float64) string {
	return utcFromTimestamp(at).Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000") + "+00:00"
}

// APIError is a session-archive API failure: HTTP status (0 when unreachable) and code.
type APIError struct {
	Status int
	Code   string
}

func (e *APIError) Error() string { return fmt.Sprintf("(%d, %q)", e.Status, e.Code) }

// archivePoster sends one session-archive request; tests fake it.
type archivePoster interface {
	post(endpoint string, payload map[string]any) (map[string]any, error)
}

type archiveClient struct{ beforePost func() error }

func (c archiveClient) post(endpoint string, payload map[string]any) (map[string]any, error) {
	body := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		body[k] = v
	}
	body["scope"] = host.Scope()
	for attempt := 0; ; attempt++ {
		if c.beforePost != nil {
			if err := c.beforePost(); err != nil {
				return nil, err
			}
		}
		reply, err := host.Post("/v1/session-archives/"+endpoint, body, 120*time.Second)
		if err == nil {
			return reply, nil
		}
		var h *host.HTTPError
		var syn *json.SyntaxError
		switch {
		case errors.As(err, &h):
			code := any("http_error")
			if failure, ok := host.Loads(string(h.Body)); ok == nil {
				if m, isMap := failure.(map[string]any); isMap {
					if c, has := m["code"]; has {
						code = c
					} else if c, has := m["error"]; has {
						code = c
					}
				}
			}
			if m, isMap := code.(map[string]any); isMap {
				code = "http_error"
				if c, has := m["code"]; has {
					code = c
				}
			}
			if (h.Code != 409 && h.Code != 503) || attempt == 2 {
				return nil, &APIError{Status: h.Code, Code: str(code)}
			}
		case errors.As(err, &syn), errors.Is(err, io.ErrUnexpectedEOF), strings.HasPrefix(err.Error(), "json:"):
			return nil, valueErr(err.Error())
		default:
			if attempt == 2 {
				return nil, &APIError{Status: 0, Code: "unavailable"}
			}
		}
		archiveSleep(time.Duration(200*(attempt+1)) * time.Millisecond)
	}
}

// archiveSignature is [st_ino, st_size, st_mtime_ns].
var archiveSignature = func(path string) ([]any, error) {
	st, err := pathStamp(path, true)
	if err != nil {
		return nil, err
	}
	return []any{int64(st.Ino), st.Size, st.MtimeNs}, nil
}

// readLine is file.readline(limit): up to limit bytes, ending at the first newline.
func readLine(r *bufio.Reader, limit int) ([]byte, error) {
	var out []byte
	for len(out) < limit {
		if _, err := r.Peek(1); err != nil {
			if err == io.EOF {
				return out, nil
			}
			return out, err
		}
		buf, _ := r.Peek(r.Buffered())
		n := min(len(buf), limit-len(out))
		if i := bytes.IndexByte(buf[:n], '\n'); i >= 0 {
			out = append(out, buf[:i+1]...)
			_, err := r.Discard(i + 1)
			return out, err
		}
		out = append(out, buf[:n]...)
		if _, err := r.Discard(n); err != nil {
			return out, err
		}
	}
	return out, nil
}

// eachJSONLine reads path the way the collector does: lines longer than 2 MiB are skipped
// whole; fn gets every other line (with its newline, if any).
func eachJSONLine(path string, fn func(line []byte) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	for {
		line, err := readLine(r, 2*archiveChunk+1)
		if err != nil {
			return err
		}
		if len(line) == 0 {
			return nil
		}
		if len(line) > 2*archiveChunk {
			for len(line) > 0 && line[len(line)-1] != '\n' {
				if line, err = readLine(r, 2*archiveChunk+1); err != nil {
					return err
				}
			}
			continue
		}
		if !fn(line) {
			return nil
		}
	}
}

// textOf is the user/assistant text of one transcript record; tool, system and thinking
// objects are never walked.
func textOf(record map[string]any) string {
	if record["table"] == "tool_calls" {
		return ""
	}
	if t := record["table"]; t == "messages" || t == "message" {
		inner := obj(record["record"])
		if data, has := inner["data"]; has {
			s, isStr := data.(string)
			if !isStr {
				return ""
			}
			v, err := loads([]byte(s))
			if err != nil {
				return ""
			}
			m, isMap := v.(map[string]any)
			if !isMap {
				return ""
			}
			inner = m
		}
		record = inner
	}
	if record["type"] == "session_meta" {
		return ""
	}
	data := or(record["message"], record["payload"], record)
	if s, isStr := data.(string); isStr {
		v, err := loads([]byte(s))
		if err != nil {
			return ""
		}
		data = v
	}
	d, isMap := data.(map[string]any)
	if !isMap {
		return ""
	}
	if role := d["role"]; role != "user" && role != "assistant" {
		return ""
	}
	if ch := d["channel"]; ch == "analysis" || ch == "commentary" {
		return ""
	}
	content, has := d["content"]
	if !has {
		return ""
	}
	if s, isStr := content.(string); isStr {
		return s
	}
	list, isList := content.([]any)
	if !isList {
		return ""
	}
	var parts []string
	for _, x := range list {
		m, isMap := x.(map[string]any)
		if !isMap {
			continue
		}
		if t := m["type"]; t != "text" && t != "input_text" && t != "output_text" {
			continue
		}
		if s, isStr := m["text"].(string); isStr {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

// projection is the searchable text of a transcript: its last 60000 bytes of message text.
func projection(path string) (string, error) {
	var output []byte
	err := eachJSONLine(path, func(line []byte) bool {
		v, err := loadsBytes(line)
		if err != nil {
			return true
		}
		if row, isMap := v.(map[string]any); isMap {
			output = append(append(output, '\n'), textOf(row)...)
			if len(output) > 60000 {
				output = append([]byte(nil), output[len(output)-60000:]...)
			}
		}
		return true
	})
	return strings.TrimSpace(strings.ToValidUTF8(string(output), "")), err
}

var codexKinds = map[string]bool{"session_meta": true, "response_item": true, "event_msg": true,
	"turn_context": true, "token_usage_record": true, "world_state": true}

// codexObservedAt uses producer timestamps: open Windows writers may keep mtime unchanged.
func codexObservedAt(path string, fallback float64) (float64, error) {
	latest, found := 0.0, false
	err := eachJSONLine(path, func(line []byte) bool {
		if line[len(line)-1] != '\n' {
			return true
		}
		row, err := loadObjBytes(line)
		if err != nil {
			return true
		}
		kind, _ := row["type"].(string)
		stamp, isStr := row["timestamp"].(string)
		if !codexKinds[kind] || !isStr {
			return true
		}
		ts, aware, ok := fromISO(stamp)
		if !ok || !aware {
			return true
		}
		if !found || ts > latest {
			latest, found = ts, true
		}
		return true
	})
	if !found {
		return fallback, err
	}
	return latest, err
}

var uuidRE = regexp.MustCompile(`[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}`)

// archiveSessionID reads the session id from the first lines, else the file name.
func archiveSessionID(path, origin string) (string, error) {
	if origin == "grok" || origin == "kiro" {
		return filepath.Base(filepath.Dir(path)), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	for range 8 {
		line, err := readLine(r, 2*archiveChunk+1)
		if err != nil {
			return "", err
		}
		if len(line) > 2*archiveChunk {
			break
		}
		row, err := loadObjBytes(line)
		if err != nil {
			continue
		}
		if origin == "codex" && row["type"] == "session_meta" {
			if p, isMap := row["payload"].(map[string]any); isMap {
				if id, has := p["id"]; has {
					return str(id), nil
				}
			}
			continue
		}
		if origin == "claude_code" && truthy(row["sessionId"]) {
			return str(row["sessionId"]), nil
		}
	}
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if m := uuidRE.FindString(stem); m != "" {
		return m, nil
	}
	return stem, nil
}

// zcodeExportMetadata validates a PC Zcode export's header and file name.
func zcodeExportMetadata(path string) (string, float64, error) {
	path = absPath(path)
	if !resolved(path) || !isFile(path) {
		return "", 0, valueErr("zcode_export_path_invalid")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	raw, err := readLine(bufio.NewReaderSize(f, 1<<16), (8<<20)+1)
	if err != nil {
		return "", 0, err
	}
	if len(raw) > 8<<20 || len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return "", 0, valueErr("zcode_export_header_invalid")
	}
	head, err := loadObjBytes(raw)
	if err != nil {
		return "", 0, valueErr("zcode_export_header_invalid")
	}
	session := obj(head["session"])
	sid, _ := session["id"].(string)
	observed, ok := sessionTimestamp(session["time_updated"])
	if sid == "" || !ok {
		return "", 0, valueErr("zcode_export_header_invalid")
	}
	if filepath.Base(path) != sha256Hex([]byte(sid))+".jsonl" {
		return "", 0, valueErr("zcode_export_identity_mismatch")
	}
	return sid, observed, nil
}

// pcZcodeSnapshot copies a stable PC Zcode export of sid under root and calls fn with the
// copy, its observed time and the source signature.
func pcZcodeSnapshot(root, sid string, fn func(target string, observed float64, before []any) error) error {
	root = absPath(root)
	path := filepath.Join(root, sha256Hex([]byte(sid))+".jsonl")
	if !resolved(root) || !resolved(path) {
		return valueErr("zcode_export_path_invalid")
	}
	if !isFile(path) {
		return zcodeMissing("zcode_export_missing")
	}
	before, err := archiveSignature(path)
	if err != nil {
		return err
	}
	actual, observed, err := zcodeExportMetadata(path)
	if err != nil {
		return err
	}
	if actual != sid {
		return valueErr("zcode_export_identity_mismatch")
	}
	temp, err := os.MkdirTemp("", "arkism-session-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	target := filepath.Join(temp, "session.jsonl")
	if err := copyFile(path, target); err != nil {
		return err
	}
	after, err := archiveSignature(path)
	if err != nil {
		return err
	}
	if !eq(after, before) {
		return zcodeMissing("zcode_export_changing")
	}
	return fn(target, observed, before)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(&archiveBoundedWriter{writer: out, remaining: archiveMaxBytes}, io.LimitReader(in, archiveMaxBytes+1)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// counter is collections.Counter with insertion order for printing.
type counter struct {
	keys []string
	n    map[string]int
}

func newCounter() *counter { return &counter{n: map[string]int{}} }

func (c *counter) add(key string, by int) {
	if _, seen := c.n[key]; !seen {
		c.keys = append(c.keys, key)
	}
	c.n[key] += by
}

func (c *counter) inc(key string) { c.add(key, 1) }

func (c *counter) obj() host.Obj {
	o := host.Obj{}
	for _, k := range c.keys {
		o = append(o, host.KV{Key: k, Value: c.n[k]})
	}
	return o
}

// collector uploads recent session files; state maps archive id -> last upload.
type collector struct {
	client  archivePoster
	state   *host.Obj
	execute bool
	counts  *counter
}

func newCollector(client archivePoster, state *host.Obj, execute bool) *collector {
	if state == nil {
		state = &host.Obj{}
	}
	return &collector{client: client, state: state, execute: execute, counts: newCounter()}
}

// upload sends path in 1 MiB chunks then commits it. observed < 0 means "from the file".
func (c *collector) upload(path, device, origin, sid, logical string, observed float64, format string, statKey any, sourcePaths ...string) (err error) {
	policy := newArchivePolicy(append(sourcePaths, path)...)
	defer func() {
		switch {
		case errors.Is(err, errArchivePolicySkipped):
			c.counts.inc("capture_skipped")
			err = nil
		case errors.Is(err, errArchivePrivate):
			c.counts.inc("private_skipped")
			err = nil
		}
	}()
	if err = policy.check(); err != nil {
		return err
	}
	before, err := archiveSignature(path)
	if err != nil {
		return err
	}
	if observed < 0 {
		observed = float64(before[2].(int64)) / 1e9
		if origin == "codex" && format == "jsonl" {
			if observed, err = codexObservedAt(path, observed); err != nil {
				return err
			}
		}
	}
	aid := archiveIdentity(device, origin, sid, logical)
	cacheKey := statKey
	if cacheKey == nil {
		cacheKey = before
	}
	now := timeNow()
	if observed > now+60 {
		return valueErr("future_time")
	}
	if observed+spoolTTL <= now {
		c.counts.inc("older_than_7d")
		return nil
	}
	if prev, ok := c.state.Get(aid); ok {
		if sig, has := objGet(prev, "signature"); has && eq(sig, cacheKey) {
			c.counts.inc("unchanged")
			return nil
		}
	}
	if !c.execute {
		if err := inspectPrivateArchiveFile(path, format); err != nil {
			return err
		}
		if err := policy.inspect(path, format); err != nil {
			return err
		}
		if err := policy.check(); err != nil {
			return err
		}
		c.counts.inc("planned")
		return nil
	}
	snapshot, cleanup, err := archiveSnapshot(path, format)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := policy.inspect(snapshot, format); err != nil {
		return err
	}
	if err := policy.check(); err != nil {
		return err
	}
	f, err := os.Open(snapshot)
	if err != nil {
		return err
	}
	defer f.Close()
	full := sha256.New()
	chunks := []any{}
	buf := make([]byte, archiveChunk)
	for {
		n, err := io.ReadFull(f, buf)
		if n > 0 {
			part := buf[:n]
			full.Write(part)
			chunk, err := c.archivePost(policy, "chunks", map[string]any{"parent_id": aid, "sha256": sha256Hex(part),
				"data": base64.StdEncoding.EncodeToString(part)})
			if err != nil {
				return err
			}
			id, has := chunk["id"]
			if !has {
				return fmt.Errorf("%w: 'id'", errKey)
			}
			chunks = append(chunks, id)
			if len(chunks) > 2048 {
				return valueErr("too_large")
			}
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if len(chunks) == 0 {
		c.counts.inc("empty")
		return nil
	}
	text := ""
	if format != "sqlite_snapshot" {
		if text, err = projection(snapshot); err != nil {
			return err
		}
	}
	after, err := archiveSignature(path)
	if err != nil {
		return err
	}
	if !eq(after, before) {
		c.counts.inc("changed_during_read")
		return nil
	}
	sum := hex.EncodeToString(full.Sum(nil))
	payload := map[string]any{"id": aid, "session_id": sid, "device": device, "origin": origin, "format": format,
		"observed_at": archiveISO(observed), "sha256": sum, "chunks": chunks, "text": text}
	reply, err := c.archivePost(policy, "commit", payload)
	if err != nil {
		var api *APIError
		if !errors.As(err, &api) || !strings.Contains(api.Code, "secret") {
			return err
		}
		payload["text"] = ""
		if reply, err = c.archivePost(policy, "commit", payload); err != nil {
			return err
		}
	}
	c.state.Set(aid, host.Obj{{Key: "signature", Value: cacheKey}, {Key: "sha256", Value: sum}, {Key: "id", Value: aid},
		{Key: "chunks", Value: chunks}, {Key: "observed_at", Value: archiveISO(observed)}, {Key: "state", Value: reply["state"]}})
	c.counts.inc("committed")
	return nil
}

func objGet(v any, key string) (any, bool) {
	switch x := v.(type) {
	case host.Obj:
		return x.Get(key)
	case map[string]any:
		e, ok := x[key]
		return e, ok
	}
	return nil, false
}

// file uploads one transcript, or a consistent backup of an SQLite session database.
func (c *collector) file(path, device, origin, logical, sid string, observed float64) error {
	if c.archiveCaptureSkipped(path) {
		return nil
	}
	if isSymlink(path) {
		c.counts.inc("symlink_skipped")
		return nil
	}
	if ext := filepath.Ext(path); ext == ".db" || ext == ".sqlite" {
		mark, err := archiveSignature(path)
		if err != nil {
			return err
		}
		walmark := []any{}
		latest := mark[2].(int64)
		if exists(path + "-wal") {
			if walmark, err = archiveSignature(path + "-wal"); err != nil {
				return err
			}
			latest = max(latest, walmark[2].(int64))
		}
		if observed <= 0 {
			observed = float64(latest) / 1e9
		}
		if observed+spoolTTL <= timeNow() {
			c.counts.inc("older_than_7d")
			return nil
		}
		temp, err := os.MkdirTemp("", "arkism-session-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(temp)
		snapshot := filepath.Join(temp, "session.db")
		resolved, err := filepath.EvalSymlinks(absPath(path))
		if err != nil {
			return err
		}
		db, err := openRO(resolved)
		if err != nil {
			return err
		}
		err = archiveSQLiteBackup(db, snapshot)
		db.Close()
		if err != nil {
			return err
		}
		if sid == "" {
			sid = strings.TrimSuffix(filepath.Base(path), ext)
		}
		return c.upload(snapshot, device, origin, sid, logical, observed, "sqlite_snapshot", []any{mark, walmark}, path)
	}
	if sid == "" {
		var err error
		if sid, err = archiveSessionID(path, origin); err != nil {
			return err
		}
	}
	return c.upload(path, device, origin, sid, logical, observed, "jsonl", nil)
}

// attempt counts a failure instead of stopping the collection.
func (c *collector) attempt(fn func() error) {
	err := func() (err error) {
		defer recovered(&err)
		return fn()
	}()
	if err == nil {
		return
	}
	c.counts.inc("failed")
	var api *APIError
	if errors.As(err, &api) {
		c.counts.inc(fmt.Sprintf("http_%d", api.Status))
	}
}

// globAll is sorted(root.rglob(pattern)): a symlinked root is followed, symlinked
// folders below it are not.
func globAll(root, pattern string) []string {
	var out []string
	// The trailing separator makes WalkDir resolve a symlinked root.
	start := strings.TrimRight(root, string(filepath.Separator)) + string(filepath.Separator)
	filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == start {
			return nil
		}
		if ok, _ := filepath.Match(pattern, d.Name()); ok {
			out = append(out, p)
		}
		return nil
	})
	sortPaths(out)
	return out
}

// sortPaths orders paths like Python's sorted(Path, ...): part by part.
func sortPaths(paths []string) {
	sort.SliceStable(paths, func(i, j int) bool {
		a := strings.Split(filepath.ToSlash(paths[i]), "/")
		b := strings.Split(filepath.ToSlash(paths[j]), "/")
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})
}

func (c *collector) collect() {
	if c.archiveCaptureSkipped() {
		return
	}
	for _, r := range archiveRoots() {
		if !exists(r.root) {
			continue
		}
		for _, path := range globAll(r.root, r.glob) {
			if !isFile(path) {
				continue
			}
			rel, _ := filepath.Rel(r.root, path)
			c.attempt(func() error { return c.file(path, r.device, r.origin, rel, "", -1) })
		}
	}
	if zcode := filepath.Join(host.Home, ".zcode/cli/db/db.sqlite"); exists(zcode) {
		c.attempt(func() error { return c.zcode(zcode) })
	}
	pcZcode := filepath.Join(archivePC, "zcode-sessions")
	if exists(pcZcode) {
		paths, _ := filepath.Glob(filepath.Join(globEscape(pcZcode), "*.jsonl"))
		sortPaths(paths)
		for _, path := range paths {
			c.attempt(func() error { return c.zcodeExport(path) })
		}
	}
}

func (c *collector) zcodeExport(path string) error {
	if c.archiveCaptureSkipped(path) {
		return nil
	}
	sid, observed, err := zcodeExportMetadata(path)
	if err != nil {
		return err
	}
	return c.upload(path, "pc", "zcode", sid, "db-session", observed, "reconstructed_zcode", nil)
}

func (c *collector) zcode(path string) error {
	if c.archiveCaptureSkipped(path) {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(absPath(path))
	if err != nil {
		return err
	}
	db, err := openRO(resolved)
	if err != nil {
		return err
	}
	defer db.Close()
	var sids []string
	err = eachRow(db, "SELECT * FROM session WHERE time_updated>? ORDER BY id", []any{(timeNow() - spoolTTL) * 1000},
		func(cols []string, vals []any) error {
			sids = append(sids, str(rowObjGet(cols, vals, "id")))
			return nil
		})
	if err != nil {
		return err
	}
	for _, sid := range sids {
		if !c.execute {
			c.counts.inc("planned")
			continue
		}
		c.attempt(func() error {
			return zcodeSnapshot(path, sid, func(target string, observed float64, revision any) error {
				return c.upload(target, "server", "zcode", sid, "db-session", observed, "reconstructed_zcode", revision, path)
			})
		})
	}
	return nil
}

func rowObjGet(cols []string, vals []any, key string) any {
	for i, c := range cols {
		if c == key {
			return vals[i]
		}
	}
	return nil
}

// migrate uploads recent sessions from an AgentsView database.
func (c *collector) migrate(path string) error {
	if c.archiveCaptureSkipped(path) {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(absPath(path))
	if err != nil {
		return err
	}
	db, err := openRO(resolved)
	if err != nil {
		return err
	}
	defer db.Close()
	type row struct {
		cols []string
		vals []any
	}
	var sessions []row
	err = eachRow(db, "SELECT * FROM sessions WHERE deleted_at IS NULL ORDER BY id", nil, func(cols []string, vals []any) error {
		sessions = append(sessions, row{cols, vals})
		return nil
	})
	if err != nil {
		return err
	}
	for _, s := range sessions {
		get := func(k string) any { return rowObjGet(s.cols, s.vals, k) }
		observed, ok := sessionTimestamp(get("ended_at"))
		if !ok || observed == 0 {
			observed, ok = sessionTimestamp(get("file_mtime"))
		}
		if !ok {
			c.counts.inc("unknown_time")
			continue
		}
		if observed+spoolTTL <= timeNow() {
			c.counts.inc("older_than_7d")
			continue
		}
		if !c.execute {
			c.counts.inc("planned")
			continue
		}
		c.attempt(func() error {
			agent, id := str(get("agent")), str(get("id"))
			origin := agent
			if agent == "claude" {
				origin = "claude_code"
			}
			sid := get("source_session_id")
			if !truthy(sid) {
				sid = strings.TrimPrefix(id, agent+":")
			}
			device := "server"
			if strings.HasPrefix(str(get("file_path")), "/pc/") {
				device = "pc"
			}
			temp, err := os.MkdirTemp("", "arkism-session-")
			if err != nil {
				return err
			}
			defer os.RemoveAll(temp)
			target := filepath.Join(temp, "session.jsonl")
			if err := writeAgentsView(db, rowObj(s.cols, s.vals), get("id"), target); err != nil {
				return err
			}
			// Many subagent rows share source_session_id: keep the AgentsView row identity too.
			return c.upload(target, device, origin, str(sid), "agentsview-db-export:"+id, observed,
				"reconstructed_agentsview", []any{get("message_count"), get("ended_at"), get("file_mtime")}, path)
		})
	}
	return nil
}

func writeAgentsView(db *sqlite.Conn, session host.Obj, id any, target string) error {
	return writeAgentsViewLimit(db, session, id, target, archiveMaxBytes)
}

func writeAgentsViewLimit(db *sqlite.Conn, session host.Obj, id any, target string, maxBytes int64) error {
	db.Limit(sqlite.LIMIT_LENGTH, archiveSQLiteValueBytes)
	if err := db.Exec("BEGIN"); err != nil {
		return err
	}
	defer db.Exec("ROLLBACK")
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	defer f.Close()
	out := bufio.NewWriter(&archiveBoundedWriter{writer: f, remaining: maxBytes})
	if _, err := out.WriteString(host.MustDumps(host.Obj{{Key: "session", Value: session}}, host.DumpOpts{}) + "\n"); err != nil {
		return err
	}
	for _, t := range [][2]string{{"messages", "ordinal,id"}, {"tool_calls", "call_index,id"}} {
		err := eachRow(db, "SELECT * FROM "+t[0]+" WHERE session_id=? ORDER BY "+t[1], []any{id},
			func(cols []string, vals []any) error {
				_, err := out.WriteString(host.MustDumps(host.Obj{{Key: "table", Value: t[0]}, {Key: "record", Value: rowObj(cols, vals)}}, host.DumpOpts{}) + "\n")
				return err
			})
		if err != nil {
			return err
		}
	}
	if err := out.Flush(); err != nil {
		return err
	}
	return f.Close()
}

// saveArchiveState writes state atomically next to the state file.
func saveArchiveState(state host.Obj) error {
	f, err := os.CreateTemp(filepath.Dir(archiveState), "tmp")
	if err != nil {
		return err
	}
	if _, err := f.WriteString(host.MustDumps(state, host.DumpOpts{})); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	f.Close()
	return os.Rename(f.Name(), archiveState)
}

func sessionArchiveMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	const usage = "usage: arkism session-archive [-h] [--database DATABASE] [--execute] {collect,migrate,expire}\n"
	var command, database string
	execute := false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-h" || a == "--help":
			fmt.Fprint(stdout, usage)
			return 0
		case a == "--execute":
			execute = true
		case a == "--database":
			if i+1 >= len(args) {
				fmt.Fprint(stderr, usage+"arkism session-archive: error: argument --database: expected one argument\n")
				return 2
			}
			i++
			database = args[i]
		case strings.HasPrefix(a, "--database="):
			database = strings.TrimPrefix(a, "--database=")
		case command == "" && (a == "collect" || a == "migrate" || a == "expire"):
			command = a
		case command == "" && !strings.HasPrefix(a, "-"):
			fmt.Fprintf(stderr, "%sarkism session-archive: error: argument command: invalid choice: '%s' (choose from collect, migrate, expire)\n", usage, a)
			return 2
		default:
			fmt.Fprintf(stderr, "%sarkism session-archive: error: unrecognized arguments: %s\n", usage, a)
			return 2
		}
	}
	if command == "" {
		fmt.Fprint(stderr, usage+"arkism session-archive: error: the following arguments are required: command\n")
		return 2
	}
	dir := filepath.Dir(archiveState)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	lock, err := lockFile(filepath.Join(dir, "collector.lock"), false)
	if err != nil {
		fmt.Fprintln(stderr, "BlockingIOError:", err)
		return 1
	}
	defer lock.Close()
	state := host.Obj{}
	if raw, err := os.ReadFile(archiveState); err == nil {
		v, err := loadsOrdered(raw)
		if err != nil {
			fmt.Fprintln(stderr, "JSONDecodeError:", err)
			return 1
		}
		if o, isObj := v.(host.Obj); isObj {
			state = o
		}
	}
	var client archivePoster
	if execute {
		if _, err := os.ReadFile(host.TokenFile); err != nil {
			fmt.Fprintln(stderr, "FileNotFoundError:", err)
			return 1
		}
		client = archiveClient{}
	}
	c := newCollector(client, &state, execute)
	switch command {
	case "expire":
		if execute {
			for range 100 {
				result, err := client.post("expire", map[string]any{"limit": 50})
				if err != nil {
					fmt.Fprintln(stderr, "APIError:", err)
					return 1
				}
				expired, _ := result["expired"].([]any)
				c.counts.add("expired", len(expired))
				orphans, _ := host.Int(result["orphans"])
				c.counts.add("orphans", int(orphans))
				if len(expired) == 0 && orphans == 0 {
					break
				}
			}
		}
	case "migrate":
		if database == "" {
			fmt.Fprint(stderr, usage+"arkism session-archive: error: --database is required\n")
			return 2
		}
		if err := c.migrate(database); err != nil {
			fmt.Fprintln(stderr, pyType(err)+":", err)
			return 1
		}
	default:
		c.collect()
	}
	if execute {
		if err := saveArchiveState(state); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	fmt.Fprintln(stdout, host.MustDumps(c.counts.obj(), host.DumpOpts{}))
	if c.counts.n["failed"] != 0 {
		return 1
	}
	return 0
}

func init() { host.Register("session-archive", sessionArchiveMain) }
