package host

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Per-session context state shared by the capture hooks and the MCP skill router: the
// session's stable injected block and the skill versions the session is pinned to. It sits
// with the other capture session state, so an MCP restart inside one session keeps the pins.
// Every entry is create-only (first writer wins), which is what makes a pin a pin.

// SessionStore keeps the state under Root (one folder per session id hash).
type SessionStore struct{ Root string }

// DefaultSessionStore is CaptureState/sessions/context.
func DefaultSessionStore() SessionStore {
	return SessionStore{Root: filepath.Join(CaptureState, "sessions", "context")}
}

// SessionStateTTL bounds how long a session keeps its stable block and pins: a session resumed
// after this rebuilds both at its next SessionStart.
const SessionStateTTL = 24 * time.Hour

// SkillPin is the approved skill version a session uses. Version is the 1-based history
// number when known; VersionID is the immutable version record id when known.
type SkillPin struct {
	SkillID   string `json:"skill_id"`
	VersionID string `json:"version_id,omitempty"`
	Version   int    `json:"version,omitempty"`
}

// StableBlock is the cached session-fixed injection and the memory ids it shows.
type StableBlock struct {
	Full         string            `json:"full"`
	Meta         string            `json:"meta"`
	IDs          []string          `json:"ids"`
	Memories     []MemoryDelivery  `json:"memories,omitempty"`
	DeliveryForm string            `json:"delivery_form,omitempty"`
	Scope        map[string]string `json:"scope,omitempty"`
}

// MemoryDelivery records an observed hook output, never confirmed behavioral use.
// Full means the hook's full representation, which may itself be an index or summary.
// Scope is retained to prevent replay under a different account from relabeling the receipt.
type MemoryDelivery struct {
	Collection string            `json:"collection"`
	ID         string            `json:"id"`
	Revision   int64             `json:"revision"`
	Scope      map[string]string `json:"scope"`
	Form       string            `json:"form"`
}

func hashName(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:32]
}

func (s SessionStore) dir(sid string) string { return filepath.Join(s.Root, hashName(sid)) }

// createOnly writes data to path unless it exists; it reports whether this call created it.
func createOnly(path string, data []byte) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// Stable is the session's cached stable block, if one was saved.
func (s SessionStore) Stable(sid string) (StableBlock, bool) {
	var b StableBlock
	if sid == "" || readJSON(filepath.Join(s.dir(sid), "stable.json"), &b) != nil {
		return StableBlock{}, false
	}
	return b, true
}

// SaveStable stores b unless the session already has a stable block; it returns the block in
// effect, so concurrent first hooks agree on one.
func (s SessionStore) SaveStable(sid string, b StableBlock) (StableBlock, error) {
	if sid == "" {
		return b, errors.New("session id required")
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return b, err
	}
	created, err := createOnly(filepath.Join(s.dir(sid), "stable.json"), raw)
	if err != nil || created {
		return b, err
	}
	if kept, ok := s.Stable(sid); ok {
		return kept, nil
	}
	return b, errors.New("stable block unreadable")
}

// Delivered is the memory revisions per-turn recall already showed in the session (id -> the
// highest revision shown).
func (s SessionStore) Delivered(sid string) map[string]int64 {
	out := map[string]int64{}
	if sid == "" {
		return out
	}
	raw, _ := os.ReadFile(filepath.Join(s.dir(sid), "delivered.txt"))
	for _, line := range strings.Split(string(raw), "\n") {
		id, rev, ok := strings.Cut(line, " ")
		n, err := strconv.ParseInt(rev, 10, 64)
		if ok && err == nil && id != "" && n >= out[id] {
			out[id] = n
		}
	}
	return out
}

// MarkDelivered records memory revisions per-turn recall showed in the session.
func (s SessionStore) MarkDelivered(sid string, revs map[string]int64) error {
	if sid == "" || len(revs) == 0 {
		return nil
	}
	var b strings.Builder
	for id, rev := range revs {
		if id != "" && !strings.ContainsAny(id, " \n") {
			b.WriteString(id + " " + strconv.FormatInt(rev, 10) + "\n")
		}
	}
	if err := os.MkdirAll(s.dir(sid), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.dir(sid), "delivered.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(b.String())
	return errors.Join(err, f.Close())
}

// ResetDelivered forgets what per-turn recall showed, e.g. after the transcript was compacted.
func (s SessionStore) ResetDelivered(sid string) error {
	if sid == "" {
		return nil
	}
	for _, name := range []string{"delivered.txt", "delivered-memories.jsonl"} {
		if err := os.Remove(filepath.Join(s.dir(sid), name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// DeliveredMemories is the exact receipt for the latest delivered revision, when recorded.
// Old delivered.txt files still suppress repetition but cannot invent scope or form metadata.
func (s SessionStore) DeliveredMemories(sid string) map[string]MemoryDelivery {
	out := map[string]MemoryDelivery{}
	if sid == "" {
		return out
	}
	raw, _ := os.ReadFile(filepath.Join(s.dir(sid), "delivered-memories.jsonl"))
	for _, line := range strings.Split(string(raw), "\n") {
		var item MemoryDelivery
		if json.Unmarshal([]byte(line), &item) != nil || item.ID == "" || item.Revision < 1 || item.Collection != "memories" || (item.Form != "full" && item.Form != "meta") {
			continue
		}
		if previous, ok := out[item.ID]; !ok || item.Revision >= previous.Revision {
			out[item.ID] = item
		}
	}
	return out
}

func (s SessionStore) MarkMemoryDelivered(sid string, items []MemoryDelivery) error {
	if sid == "" || len(items) == 0 {
		return nil
	}
	var data []byte
	for _, item := range items {
		raw, err := json.Marshal(item)
		if err != nil {
			return err
		}
		data = append(append(data, raw...), '\n')
	}
	if err := os.MkdirAll(s.dir(sid), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.dir(sid), "delivered-memories.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return errors.Join(err, f.Close())
}

// Expire drops a session's state once its stable block is older than ttl, and other sessions'
// state older than a week. It never touches a younger session.
func (s SessionStore) Expire(sid string, ttl time.Duration, now time.Time) {
	if sid != "" {
		if st, err := os.Stat(filepath.Join(s.dir(sid), "stable.json")); err == nil && now.Sub(st.ModTime()) > ttl {
			os.RemoveAll(s.dir(sid))
		}
	}
	week := 7 * 24 * time.Hour
	links, _ := os.ReadDir(filepath.Join(s.Root, "agents"))
	for _, e := range links {
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > week {
			os.Remove(filepath.Join(s.Root, "agents", e.Name()))
		}
	}
	entries, _ := os.ReadDir(s.Root)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && e.Name() != "agents" && now.Sub(info.ModTime()) > week {
			os.RemoveAll(filepath.Join(s.Root, e.Name()))
		}
	}
}

func pinPath(dir, skillID string) string {
	return filepath.Join(dir, "skills", hashName(skillID)+".json")
}

// Pin records p for the session unless the skill is already pinned, and returns the pin in effect.
func (s SessionStore) Pin(sid string, p SkillPin) (SkillPin, error) {
	if sid == "" || p.SkillID == "" || (p.VersionID == "" && p.Version < 1) {
		return p, errors.New("invalid skill pin")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	path := pinPath(s.dir(sid), p.SkillID)
	created, err := createOnly(path, raw)
	if err != nil || created {
		return p, err
	}
	var kept SkillPin
	if err := readJSON(path, &kept); err != nil || kept.SkillID != p.SkillID {
		return p, errors.New("skill pin unreadable")
	}
	return kept, nil
}

// SkillPin is the session's pin for skillID, if any.
func (s SessionStore) SkillPin(sid, skillID string) (SkillPin, bool) {
	var p SkillPin
	if sid == "" || readJSON(pinPath(s.dir(sid), skillID), &p) != nil || p.SkillID != skillID {
		return SkillPin{}, false
	}
	return p, true
}

// Agent session links: the hook knows the session id, the MCP server does not. Both are
// children of the agent program, so the hook records "agent process -> session id" for its
// nearest ancestors and the MCP server looks up its own. Linux only (/proc); elsewhere the
// router falls back to its own process.

const linkDepth = 3

// procParent is pid's parent and start time from /proc.
func procParent(pid int) (int, string, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, "", false
	}
	s := string(raw)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, "", false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return 0, "", false
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return 0, "", false
	}
	return ppid, f[19], true
}

// ancestors is up to linkDepth "pid-start" keys from the parent of this process upward.
func ancestors() []string {
	var out []string
	pid := os.Getppid()
	for len(out) < linkDepth && pid > 1 {
		ppid, start, ok := procParent(pid)
		if !ok {
			break
		}
		out = append(out, strconv.Itoa(pid)+"-"+start)
		pid = ppid
	}
	return out
}

// LinkAgent records sid for this hook's ancestor processes.
func (s SessionStore) LinkAgent(sid string) error {
	if sid == "" {
		return nil
	}
	dir := filepath.Join(s.Root, "agents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, key := range ancestors() {
		tmp, err := os.CreateTemp(dir, ".tmp-*")
		if err != nil {
			return err
		}
		_, werr := tmp.WriteString(sid)
		cerr := tmp.Close()
		if werr == nil && cerr == nil {
			werr = os.Rename(tmp.Name(), filepath.Join(dir, key))
		}
		if werr != nil || cerr != nil {
			os.Remove(tmp.Name())
			return errors.Join(werr, cerr)
		}
	}
	return nil
}

// AgentSession is the session id of the agent this process runs under: ARKISM_SESSION_ID, else
// the nearest ancestor a hook linked; "" when unknown.
func (s SessionStore) AgentSession() string {
	if v := strings.TrimSpace(os.Getenv("ARKISM_SESSION_ID")); v != "" {
		return v
	}
	for _, key := range ancestors() {
		if raw, err := os.ReadFile(filepath.Join(s.Root, "agents", key)); err == nil && len(raw) > 0 {
			return string(raw)
		}
	}
	return ""
}
