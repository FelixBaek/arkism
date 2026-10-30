package memory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Agent tools: the MCP servers and plugins each agent on the operator's host has
// configured, and the owner's on/off switches for them. The host sync tool
// reports what it sees and applies pending switches
// with the agents' own CLIs; this store keeps one document per scope.
//
// The document never carries secrets: names, transport and the program or host
// only — never arguments, environment, headers or tokens.
const (
	colAgentTools = "agent_tools"
	agentToolsID  = "agent-tools"
)

var agentToolAgents = []string{"claude_code", "codex", "zcode", "gemini", "grok", "opencode", "cursor"}
var agentToolTransports = []string{"stdio", "http", "sse", "plugin"}

type AgentToolEntry struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Target    string            `json:"target,omitempty"`
	Agents    map[string]string `json:"agents"`         // agent -> on | off
	Note      string            `json:"note,omitempty"` // e.g. "로그인 필요"
	// Login is the agents whose copy of this server waits for an OAuth sign-in (agent -> "needed").
	Login map[string]string `json:"login,omitempty"`
	// What the host learned beyond the switches (12 · 12b): a server's tools and recent calls,
	// a plugin's version, description and what it bundles. Names and counts only.
	Tools    []AgentTool     `json:"tools,omitempty"`
	Calls    *AgentToolCalls `json:"calls,omitempty"`
	Version  string          `json:"version,omitempty"`
	Desc     string          `json:"desc,omitempty"`
	Contents []PluginItem    `json:"contents,omitempty"`
}

// AgentTool is one tool an MCP server lists; Risky when it changes or sends something
// (the server's destructive hint, or a write-like name when it gives none).
type AgentTool struct {
	Name  string `json:"name"`
	Desc  string `json:"desc,omitempty"`
	Risky bool   `json:"risky,omitempty"`
}

// AgentToolCalls counts an MCP server's tool calls in the agents' transcripts today, newest few first.
type AgentToolCalls struct {
	Today  int             `json:"today"`
	Recent []AgentToolCall `json:"recent,omitempty"`
}

type AgentToolCall struct {
	Tool string    `json:"tool"`
	OK   bool      `json:"ok"`
	At   time.Time `json:"at"`
}

// PluginItem is one thing a plugin bundles: a skill, an MCP server or a slash command.
type PluginItem struct {
	Kind string `json:"kind"` // skill | mcp | command
	Name string `json:"name"`
	Desc string `json:"desc,omitempty"`
}

// SkillUpdate is a skill whose source repository has newer commits upstream (01 할 일 › 업데이트 가능).
type SkillUpdate struct {
	Name    string `json:"name"`
	Repo    string `json:"repo"` // the repository folder on the host, ~/... or /...
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Commits int    `json:"commits"`
}

// SkillRepo is one skill repository the host tracks and when git last asked its upstream
// (the 스킬 화면's 업데이트 section): the public remote address, the commit the folder is on, and how far behind it is.
type SkillRepo struct {
	Repo      string    `json:"repo"`   // the repository folder on the host, ~/... or /...
	Remote    string    `json:"remote"` // readable address without credentials, e.g. github.com/owner/repo
	Head      string    `json:"head,omitempty"`
	Behind    int       `json:"behind"`
	CheckedAt time.Time `json:"checked_at"`
}

// PCLink is how one agent on the owner's PC last reached this server: the last time any of its
// hooks or skill calls arrived, whether its latest attempt failed, and how many writes wait to be retried.
// The app turns these into 연결됨 / 오래됨 / 실패 / 연결 안 됨 (a missing agent is 연결 안 됨).
type PCLink struct {
	Agent    string     `json:"agent"`
	LastSeen *time.Time `json:"last_seen,omitempty"`
	Failing  bool       `json:"failing,omitempty"`
	Pending  int        `json:"pending,omitempty"`
}

// AgentInfo is one coding agent the host found or the owner added (설정 › 에이전트):
// where it keeps skills and chats, and how many skills are there now.
type AgentInfo struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	SkillsDirs []string `json:"skills_dirs"`
	ChatsDir   string   `json:"chats_dir,omitempty"`
	Found      bool     `json:"found"`   // its folder exists on the host
	Enabled    bool     `json:"enabled"` // Arkism reads it
	Custom     bool     `json:"custom,omitempty"`
	Skills     int      `json:"skills"`
}

type AgentToolRequest struct {
	ID        string   `json:"id"`
	Agent     string   `json:"agent"`
	Kind      string   `json:"kind"` // mcp | plugin | agent (add or remove an agent) | shared (a folder several agents read) | skill_update | skill_check
	SkillsDir string   `json:"skills_dir,omitempty"`
	ChatsDir  string   `json:"chats_dir,omitempty"`
	Command   []string `json:"command,omitempty"` // mcp_add over stdio: program then arguments
	URL       string   `json:"url,omitempty"`     // mcp_add over http
	Targets   []string `json:"targets,omitempty"` // skill_install: the agents' skill folders to install into
	Update    bool     `json:"update,omitempty"`  // skill_install: replace a skill already there
	Zip       string   `json:"zip,omitempty"`     // skill_install from a zip file (base64); Name is the file name. Cleared once applied
	Name      string   `json:"name"`
	Enabled   bool     `json:"enabled"`
	Status    string   `json:"status"` // pending | waiting (mcp_login: auth_url is open) | done | failed
	Error     string   `json:"error,omitempty"`
	Detail    string   `json:"detail,omitempty"` // a host's short JSON answer: skill_install preview, mcp_add warnings
	// mcp_login: the agent CLI's sign-in address, and the address the browser came back to
	// (path and query only), which the host replays on its own loopback. Both are cleared when it ends.
	AuthURL     string     `json:"auth_url,omitempty"`
	Callback    string     `json:"callback,omitempty"`
	RequestedBy string     `json:"requested_by"`
	RequestedAt time.Time  `json:"requested_at"`
	DoneAt      *time.Time `json:"done_at,omitempty"`
}

// SkillCheck is one skill folder's result from the host's free, offline scanners:
// NVIDIA SkillEvaluator quality-check (0–100, A–F) and SkillSpector --no-llm
// (risk 0–100, SAFE | CAUTION | DO_NOT_INSTALL). Findings are category, pattern
// and severity only — never the matched text or a code snippet.
type SkillCheck struct {
	Agent        string    `json:"agent"`
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	QualityScore *float64  `json:"quality_score,omitempty"`
	QualityGrade string    `json:"quality_grade,omitempty"`
	RiskScore    *int      `json:"risk_score,omitempty"`
	Verdict      string    `json:"verdict,omitempty"`
	Findings     int       `json:"findings"`
	TopFindings  []string  `json:"top_findings,omitempty"`
	ScannedAt    time.Time `json:"scanned_at"`
	Error        string    `json:"error,omitempty"`
}

type AgentTools struct {
	CollectedAt *time.Time         `json:"collected_at,omitempty"`
	Servers     []AgentToolEntry   `json:"servers"`
	Plugins     []AgentToolEntry   `json:"plugins"`
	Requests    []AgentToolRequest `json:"requests"`
	SkillChecks []SkillCheck       `json:"skill_checks,omitempty"`
	// Scanner names and versions behind SkillChecks, e.g. "skillevaluator 0.4.0 · skillspector 2.12.0".
	SkillCheckTools string          `json:"skill_check_tools,omitempty"`
	Agents          []AgentInfo     `json:"agents,omitempty"`
	SharedDirs      []string        `json:"shared_dirs,omitempty"`
	MemoryUse       *MemoryUseStats `json:"memory_use,omitempty"`
	MemoryUse30     *MemoryUseStats `json:"memory_use_30,omitempty"`
	SkillUpdates    []SkillUpdate   `json:"skill_updates,omitempty"`
	PCLinks         []PCLink        `json:"pc_links,omitempty"`
	SkillRepos      []SkillRepo     `json:"skill_repos,omitempty"`
	AnalysisRun     *AnalysisRun    `json:"analysis_run,omitempty"`
	Extractor       *Extractor      `json:"extractor,omitempty"`
}

// Extractor is what the host runs for 기억 추출 when nothing is picked: the model, its agent CLI and
// whether that CLI is installed and signed in, with each known CLI's state (ready, login_needed, missing).
type Extractor struct {
	Model string            `json:"model"`
	CLI   string            `json:"cli"`
	State string            `json:"state"`
	CLIs  map[string]string `json:"clis,omitempty"`
}

// AnalysisRun is the host's last capture drain that touched a job: how many jobs it settled and how,
// with the most common waiting or failure code. LastDoneAt is the last time any job finished, kept
// across runs, so a stretch without candidates can be told from a stretch without processing (#449).
type AnalysisRun struct {
	At                 time.Time  `json:"at"`
	Jobs               int        `json:"jobs"`
	Done               int        `json:"done"`
	Candidates         int        `json:"candidates"`
	Waiting            int        `json:"waiting"`
	Dropped            int        `json:"dropped"`
	Parked             int        `json:"parked"`
	Failed             int        `json:"failed"`
	Reason             string     `json:"reason"`
	Error              string     `json:"error"`
	LastDoneAt         *time.Time `json:"last_done_at"`
	LastDoneCandidates *int       `json:"last_done_candidates"`
}

// MemoryUseStats counts injections and observed answer-text overlap over Days.
// Used is a legacy wire name for text overlap, not measured actual use. Older
// hosts do not report unread transcript pairs, so zero cannot prove irrelevance.
// Keep the wire shape compatible with those hosts. Ids and counts only.
type MemoryUseStats struct {
	Days       int             `json:"days"`
	Sessions   int             `json:"sessions"`
	Injections int             `json:"injections"`
	Used       int             `json:"used"`
	Items      []MemoryUseStat `json:"items"`
	// Text the hooks added (SessionStart index + prompt recall), in characters, over the
	// sessions that got any; absent from hosts that do not log it yet.
	InjectSessions int `json:"inject_sessions,omitempty"`
	InjectChars    int `json:"inject_chars,omitempty"`
	// Hosts that count record_memory_usage receipts as Used send the text overlap here,
	// and the number of unreceipted injections after which an item is marked Unused.
	Estimated   int `json:"estimated,omitempty"`
	UnusedAfter int `json:"unused_after,omitempty"`
}

type MemoryUseStat struct {
	ID          string `json:"id"`
	Injected    int    `json:"injected"`
	Used        int    `json:"used"`
	Estimated   int    `json:"estimated,omitempty"`
	Unreceipted int    `json:"unreceipted,omitempty"`
	Unused      bool   `json:"unused,omitempty"`
}

type AgentToolSwitch struct {
	Scope     Scope    `json:"scope"`
	Agent     string   `json:"agent"`
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	Enabled   bool     `json:"enabled"`
	SkillsDir string   `json:"skills_dir,omitempty"`
	ChatsDir  string   `json:"chats_dir,omitempty"`
	Command   []string `json:"command,omitempty"`
	URL       string   `json:"url,omitempty"`
	Targets   []string `json:"targets,omitempty"`
	Update    bool     `json:"update,omitempty"`
	Zip       string   `json:"zip,omitempty"`
}

type AgentToolResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type AgentToolsReport struct {
	Scope   Scope             `json:"scope"`
	Servers []AgentToolEntry  `json:"servers"`
	Plugins []AgentToolEntry  `json:"plugins"`
	Results []AgentToolResult `json:"results"`
	// Absent keeps the last skill checks: scans run on their own, slower schedule.
	SkillChecks     *[]SkillCheck `json:"skill_checks,omitempty"`
	SkillCheckTools string        `json:"skill_check_tools,omitempty"`
	// Absent keeps the last agent list and shared folders.
	Agents     *[]AgentInfo    `json:"agents,omitempty"`
	SharedDirs *[]string       `json:"shared_dirs,omitempty"`
	MemoryUse  *MemoryUseStats `json:"memory_use,omitempty"`
	// The same over 30 days; absent keeps the last one.
	MemoryUse30 *MemoryUseStats `json:"memory_use_30,omitempty"`
	// Absent keeps the last list: repositories are checked on a slower schedule.
	SkillUpdates *[]SkillUpdate `json:"skill_updates,omitempty"`
	// Absent keeps the last list; an empty one clears it (no PC has reached the server).
	PCLinks *[]PCLink `json:"pc_links,omitempty"`
	// Absent keeps the last list: repositories are checked on a slower schedule.
	SkillRepos *[]SkillRepo `json:"skill_repos,omitempty"`
	// Absent keeps the last run: an idle drain does not report.
	AnalysisRun *AnalysisRun `json:"analysis_run,omitempty"`
	// Absent (an older host) keeps the last one.
	Extractor *Extractor `json:"extractor,omitempty"`
}

const agentToolRequestsKept = 50

var agentIDRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)

// validAgentDir accepts a home-relative or absolute (POSIX or Windows) folder without "..".
func validAgentDir(d string, required bool) bool {
	if d == "" {
		return !required
	}
	return len(d) <= 200 && (strings.HasPrefix(d, "~/") || strings.HasPrefix(d, "/") || winDirRe.MatchString(d)) && !strings.Contains(d, "..") &&
		strings.IndexFunc(d, func(r rune) bool { return r < 0x20 }) < 0
}

// winDirRe: a Windows host's C:\ or C:/ folder, or a \\server\share one (desktop pages.js agentDirOk accepts the same).
var winDirRe = regexp.MustCompile(`^([A-Za-z]:[\\/]|\\\\[^\\])`)

func validAgentInfos(list []AgentInfo) bool {
	if len(list) > 40 {
		return false
	}
	for _, a := range list {
		if !agentIDRe.MatchString(a.ID) || strings.TrimSpace(a.Name) == "" || runes(a.Name) > 40 || len(a.SkillsDirs) > 4 || !validAgentDir(a.ChatsDir, false) || a.Skills < 0 {
			return false
		}
		for _, d := range a.SkillsDirs {
			if !validAgentDir(d, true) {
				return false
			}
		}
	}
	return true
}

const skillChecksMax = 2000

var skillVerdicts = []string{"SAFE", "CAUTION", "DO_NOT_INSTALL"}
var skillGrades = []string{"A", "B", "C", "D", "F"}

func validSkillChecks(checks []SkillCheck) bool {
	if len(checks) > skillChecksMax {
		return false
	}
	for _, c := range checks {
		if !validSkillCheck(c) {
			return false
		}
	}
	return true
}

func validSkillCheck(c SkillCheck) bool {
	if !slices.Contains(agentToolAgents, c.Agent) || !validRunLabel(c.Name, 128) || c.Path == "" || len(c.Path) > 512 || len(c.Error) > 200 || c.Findings < 0 || len(c.TopFindings) > 3 || c.ScannedAt.IsZero() {
		return false
	}
	if !validSkillCheckScores(c) {
		return false
	}
	for _, f := range c.TopFindings {
		if f == "" || len(f) > 160 {
			return false
		}
	}
	return true
}

func validSkillCheckScores(c SkillCheck) bool {
	if c.QualityScore != nil && (*c.QualityScore < 0 || *c.QualityScore > 100) || c.QualityGrade != "" && !slices.Contains(skillGrades, c.QualityGrade) {
		return false
	}
	return !(c.RiskScore != nil && (*c.RiskScore < 0 || *c.RiskScore > 100) || c.Verdict != "" && !slices.Contains(skillVerdicts, c.Verdict))
}

func validAgentToolEntries(entries []AgentToolEntry, plugin bool) bool {
	if len(entries) > 200 {
		return false
	}
	for _, e := range entries {
		if !validAgentToolEntry(e, plugin) {
			return false
		}
	}
	return true
}

func validAgentToolEntry(e AgentToolEntry, plugin bool) bool {
	if !validRunLabel(e.Name, 128) || len(e.Target) > 256 || len(e.Note) > 120 || len(e.Agents) == 0 || len(e.Agents) > len(agentToolAgents) {
		return false
	}
	for agent, v := range e.Login {
		if !slices.Contains(agentToolAgents, agent) || v != "needed" {
			return false
		}
	}
	if plugin != (e.Transport == "plugin") || !slices.Contains(agentToolTransports, e.Transport) || !validToolDetail(e) {
		return false
	}
	for agent, state := range e.Agents {
		if !slices.Contains(agentToolAgents, agent) || state != "on" && state != "off" {
			return false
		}
	}
	return true
}

func validToolDetail(e AgentToolEntry) bool {
	if len(e.Tools) > 300 || len(e.Contents) > 200 || len(e.Version) > 40 || runes(e.Desc) > 300 || !validToolCalls(e.Calls) {
		return false
	}
	for _, t := range e.Tools {
		if !validRunLabel(t.Name, 128) || runes(t.Desc) > 300 {
			return false
		}
	}
	for _, it := range e.Contents {
		if !slices.Contains([]string{"skill", "mcp", "command"}, it.Kind) || !validRunLabel(it.Name, 128) || runes(it.Desc) > 300 {
			return false
		}
	}
	return true
}

func validToolCalls(c *AgentToolCalls) bool {
	if c == nil {
		return true
	}
	if c.Today < 0 || len(c.Recent) > 20 {
		return false
	}
	for _, call := range c.Recent {
		if !validRunLabel(call.Tool, 128) {
			return false
		}
	}
	return true
}

func validSkillUpdates(list []SkillUpdate) bool {
	if len(list) > 500 {
		return false
	}
	for _, u := range list {
		if !validRunLabel(u.Name, 128) || !validAgentDir(u.Repo, true) || len(u.From) > 40 || len(u.To) > 40 || u.Commits < 0 {
			return false
		}
	}
	return true
}

func validSkillRepos(list []SkillRepo) bool {
	if len(list) > 200 {
		return false
	}
	for _, r := range list {
		if !validAgentDir(r.Repo, true) || len(r.Remote) > 200 || strings.ContainsAny(r.Remote, " \t\r\n@") || len(r.Head) > 40 || r.Behind < 0 || r.CheckedAt.IsZero() {
			return false
		}
	}
	return true
}

// validAnalysisRun accepts an absent run (the last one is kept).
func validAnalysisRun(a *AnalysisRun) bool {
	if a == nil {
		return true
	}
	for _, n := range []int{a.Jobs, a.Done, a.Candidates, a.Waiting, a.Dropped, a.Parked, a.Failed} {
		if n < 0 || n > 1_000_000 {
			return false
		}
	}
	code := func(c string) bool { return c == "" || validRunLabel(c, 80) }
	return !a.At.IsZero() && code(a.Reason) && code(a.Error) && (a.LastDoneCandidates == nil || *a.LastDoneCandidates >= 0)
}

var extractorStates = []string{"ready", "login_needed", "missing"}

// validExtractor accepts an absent report (the last one is kept).
func validExtractor(e *Extractor) bool {
	if e == nil {
		return true
	}
	if !validRunLabel(e.Model, 120) || !agentIDRe.MatchString(e.CLI) || !slices.Contains(extractorStates, e.State) || len(e.CLIs) > 8 {
		return false
	}
	for cli, state := range e.CLIs {
		if !agentIDRe.MatchString(cli) || !slices.Contains(extractorStates, state) {
			return false
		}
	}
	return true
}

func validPCLinks(list []PCLink) bool {
	if len(list) > 16 {
		return false
	}
	seen := map[string]bool{}
	for _, l := range list {
		if !agentIDRe.MatchString(l.Agent) || seen[l.Agent] || l.Pending < 0 || l.Pending > 100000 {
			return false
		}
		seen[l.Agent] = true
	}
	return true
}

func noSecrets(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return ErrInvalid
	}
	if containsSecret(string(raw)) {
		return ErrSecretDetected
	}
	return nil
}

// AgentTools returns the scope's document; an empty one before the first report.
func (s *Service) AgentTools(ctx context.Context, token string, scope Scope) (Record, error) {
	if !validScope(scope) {
		return Record{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colAgentTools, scope, agentToolsID, ""); err != nil {
		return Record{}, err
	}
	r, err := agentToolsDoc(ctx, s.Repo, scope)
	if errors.Is(err, ErrNotFound) {
		return Record{ID: scopedAgentToolsID(scope), Scope: scope, AgentTools: &AgentTools{Servers: []AgentToolEntry{}, Plugins: []AgentToolEntry{}, Requests: []AgentToolRequest{}}}, nil
	}
	return r, err
}

// The primary key is (collection,id), so each scope needs its own id. The one scope
// that already owns the legacy shared id keeps using it.
func scopedAgentToolsID(scope Scope) string {
	return agentToolsID + "-" + ContentHash(structuredText(scope))
}
func agentToolsDoc(ctx context.Context, repo interface {
	Get(context.Context, string, Scope, string) (Record, error)
}, scope Scope) (Record, error) {
	r, err := repo.Get(ctx, colAgentTools, scope, scopedAgentToolsID(scope))
	if errors.Is(err, ErrNotFound) {
		return repo.Get(ctx, colAgentTools, scope, agentToolsID)
	}
	return r, err
}

// updateAgentTools applies change to the scope's document inside one transaction.
// then runs in the same transaction after change, to record what changed.
func (s *Service) updateAgentTools(ctx context.Context, scope Scope, change func(*AgentTools, time.Time), then ...func(context.Context, Tx) error) (Record, error) {
	var out Record
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		now := time.Now().UTC().Truncate(time.Millisecond)
		r, err := agentToolsDoc(ctx, tx, scope)
		fresh := errors.Is(err, ErrNotFound)
		if err != nil && !fresh {
			return err
		}
		if fresh {
			r = Record{ID: scopedAgentToolsID(scope), Scope: scope, Kind: colAgentTools, Status: statusActive, Revision: 1, CreatedAt: now, Text: "agent tools"}
		}
		// Work on a copy: a record read earlier must not change under its reader.
		tools := AgentTools{}
		if r.AgentTools != nil {
			tools = *r.AgentTools
			tools.Requests = append([]AgentToolRequest(nil), tools.Requests...)
		}
		change(&tools, now)
		if err := runInTx(ctx, tx, then); err != nil {
			return err
		}
		r.AgentTools = &tools
		r.UpdatedAt = now
		if fresh {
			out = r
			return tx.Insert(ctx, colAgentTools, r)
		}
		prev := r.Revision
		r.Revision++
		out = r
		return tx.Replace(ctx, colAgentTools, r, prev)
	})
	return out, err
}

// skillZipMaxBytes caps a zip carried in the request: its base64 must fit the 256KB body.
const skillZipMaxBytes = 160 << 10

// skillInstallTargets are the skill folders skill_install may write: ~/.claude/skills, ~/.codex/skills, ~/.agents/skills, ~/.zcode/skills.
var (
	skillInstallTargets = []string{"claude_code", "codex", "agents", "zcode"}
	skillSourceRe       = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,39}/[A-Za-z0-9_.-]{1,100}(/[A-Za-z0-9_.-]+)*(@[A-Za-z0-9_./-]+)?$`)
)

// validSkillSource is owner/repo[/path][@ref] with no . or .. segment.
func validSkillSource(s string) bool {
	if len(s) > 400 || !skillSourceRe.MatchString(s) {
		return false
	}
	for _, seg := range strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == '@' }) {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// RequestAgentTool queues the owner's switch; the host tool applies it.
func (s *Service) RequestAgentTool(ctx context.Context, token string, in AgentToolSwitch) (Record, error) {
	if err := validAgentToolSwitch(in); err != nil {
		return Record{}, err
	}
	if !validScope(in.Scope) {
		return Record{}, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "write:"+colAgentTools, in.Scope, agentToolsID, "")
	if err != nil {
		return Record{}, err
	}
	if d.Role != roleHuman {
		return Record{}, ErrDenied
	}
	return s.updateAgentTools(ctx, in.Scope, func(t *AgentTools, now time.Time) {
		// A newer switch for the same thing replaces one still waiting.
		kept := make([]AgentToolRequest, 0, len(t.Requests)+1)
		for _, q := range t.Requests {
			// One zip waits at a time, so the record never holds more than one upload.
			if q.Status != "pending" || (q.Agent != in.Agent || q.Kind != in.Kind || q.Name != in.Name) && (q.Zip == "" || in.Zip == "") {
				kept = append(kept, q)
			}
		}
		t.Requests = append(kept, AgentToolRequest{ID: ID(), Agent: in.Agent, Kind: in.Kind, Name: in.Name, Enabled: in.Enabled,
			SkillsDir: in.SkillsDir, ChatsDir: in.ChatsDir, Command: in.Command, URL: in.URL, Targets: in.Targets, Update: in.Update, Zip: in.Zip, Status: "pending", RequestedBy: d.PrincipalID, RequestedAt: now})
		if over := len(t.Requests) - agentToolRequestsKept; over > 0 {
			t.Requests = t.Requests[over:]
		}
	}, func(ctx context.Context, tx Tx) error {
		// Only the newest 50 requests are kept; the count of every switch outlives them.
		kind := map[bool]string{true: "agent_tool_on", false: "agent_tool_off"}[in.Enabled]
		return recordDecision(ctx, tx, in.Scope, d.PrincipalID, kind, in.Agent, in.Kind, in.Name)
	})
}

func validAgentToolSwitch(in AgentToolSwitch) error {
	if in.Kind != "skill_install" && skillInstallExtras(in) {
		return ErrInvalid
	}
	switch in.Kind {
	case "mcp", "plugin", "plugin_remove", "mcp_login":
		if !slices.Contains(agentToolAgents, in.Agent) || !validRunLabel(in.Name, 128) || switchHasExtras(in) {
			return ErrInvalid
		}
	case "agent": // add (enabled) or remove an agent; Name is its display name
		return validAgentSwitch(in)
	case "mcp_add": // a new MCP server for one agent: a program to run, or an https address; no keys or headers
		return validMCPAddSwitch(in)
	case "skill_update", "skill_check": // update: pull one skill repository forward (git pull --ff-only); check: ask its upstream again now. Name is the repository folder
		if in.Agent != "shared" || !validAgentDir(in.Name, true) || switchHasExtras(in) {
			return ErrInvalid
		}
	case "skill_install": // a skill from GitHub: Name is owner/repo[/path][@ref]; not enabled = preview only, enabled = install into Targets
		return validSkillInstallSwitch(in)
	case "shared": // add (enabled) or remove a folder several agents read; Name is the folder
		if in.Agent != "shared" || !validAgentDir(in.Name, true) || in.SkillsDir != "" || in.ChatsDir != "" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func validAgentSwitch(in AgentToolSwitch) error {
	if !agentIDRe.MatchString(in.Agent) || strings.TrimSpace(in.Name) == "" || runes(in.Name) > 40 ||
		!validAgentDir(in.SkillsDir, in.Enabled) || !validAgentDir(in.ChatsDir, false) {
		return ErrInvalid
	}
	return nil
}

// skillInstallExtras reports fields only skill_install switches carry.
func skillInstallExtras(in AgentToolSwitch) bool {
	return len(in.Targets) > 0 || in.Update || in.Zip != ""
}

// switchHasExtras reports fields only agent, mcp_add and skill_install switches carry.
func switchHasExtras(in AgentToolSwitch) bool {
	return in.SkillsDir != "" || in.ChatsDir != "" || len(in.Command) > 0 || in.URL != ""
}

func validMCPAddSwitch(in AgentToolSwitch) error {
	if !slices.Contains(agentToolAgents, in.Agent) || !validRunLabel(in.Name, 128) || (len(in.Command) == 0) == (in.URL == "") || len(in.Command) > 32 ||
		in.URL != "" && (!strings.HasPrefix(in.URL, "https://") || len(in.URL) > 400) {
		return ErrInvalid
	}
	for _, a := range in.Command {
		if a == "" || len(a) > 400 || strings.IndexFunc(a, func(r rune) bool { return r < 0x20 }) >= 0 {
			return ErrInvalid
		}
	}
	return noSecrets(in)
}

func validSkillInstallSwitch(in AgentToolSwitch) error {
	source := validSkillSource(in.Name)
	if in.Zip != "" { // the host reads and checks the archive; here only its size and encoding
		raw, err := base64.StdEncoding.DecodeString(in.Zip)
		source = err == nil && len(raw) > 0 && len(raw) <= skillZipMaxBytes && validRunLabel(in.Name, 128)
	}
	if in.Agent != "shared" || !source || switchHasExtras(in) ||
		in.Enabled != (len(in.Targets) > 0) || len(in.Targets) > len(skillInstallTargets) {
		return ErrInvalid
	}
	for i, t := range in.Targets {
		if !slices.Contains(skillInstallTargets, t) || slices.Contains(in.Targets[:i], t) {
			return ErrInvalid
		}
	}
	return nil
}

func validAgentToolsReport(in AgentToolsReport) bool {
	if !validScope(in.Scope) || !validAgentToolEntries(in.Servers, false) || !validAgentToolEntries(in.Plugins, true) || len(in.Results) > agentToolRequestsKept || len(in.SkillCheckTools) > 120 {
		return false
	}
	if in.SkillChecks != nil && !validSkillChecks(*in.SkillChecks) || in.Agents != nil && !validAgentInfos(*in.Agents) || in.SkillUpdates != nil && !validSkillUpdates(*in.SkillUpdates) || in.PCLinks != nil && !validPCLinks(*in.PCLinks) || in.SkillRepos != nil && !validSkillRepos(*in.SkillRepos) {
		return false
	}
	if !validMemoryUse(in.MemoryUse) || !validMemoryUse(in.MemoryUse30) || !validAnalysisRun(in.AnalysisRun) || !validExtractor(in.Extractor) {
		return false
	}
	return validSharedDirs(in.SharedDirs) && validAgentToolResults(in.Results)
}

func validSharedDirs(dirs *[]string) bool {
	if dirs == nil {
		return true
	}
	if len(*dirs) > 8 {
		return false
	}
	for _, d := range *dirs {
		if !validAgentDir(d, true) {
			return false
		}
	}
	return true
}

func validAgentToolResults(results []AgentToolResult) bool {
	for _, res := range results {
		if res.ID == "" || res.Status != "done" && res.Status != "failed" || len(res.Error) > 500 || len(res.Detail) > 8000 {
			return false
		}
	}
	return true
}

// settle marks the switches the host tool applied; a switch no longer pending is left alone.
func (t *AgentTools) settle(results []AgentToolResult, now time.Time) {
	index := map[string]AgentToolResult{}
	for _, res := range results {
		index[res.ID] = res
	}
	for i, q := range t.Requests {
		if res, ok := index[q.ID]; ok && q.Status == "pending" {
			t.Requests[i].Status, t.Requests[i].Error, t.Requests[i].Detail, t.Requests[i].DoneAt = res.Status, res.Error, res.Detail, &now
			t.Requests[i].Zip = ""
		}
	}
}

// ReportAgentTools replaces the inventory the host tool saw and records the
// outcome of the switches it applied.
func (s *Service) ReportAgentTools(ctx context.Context, token string, in AgentToolsReport) (Record, error) {
	if !validAgentToolsReport(in) {
		return Record{}, ErrInvalid
	}
	if err := noSecrets(in); err != nil {
		return Record{}, err
	}
	if _, err := s.authorize(ctx, token, "write:"+colAgentTools, in.Scope, agentToolsID, ""); err != nil {
		return Record{}, err
	}
	return s.updateAgentTools(ctx, in.Scope, func(t *AgentTools, now time.Time) {
		t.Servers, t.Plugins, t.CollectedAt = in.Servers, in.Plugins, &now
		if in.SkillChecks != nil {
			t.SkillChecks, t.SkillCheckTools = *in.SkillChecks, in.SkillCheckTools
		}
		if in.Agents != nil {
			t.Agents = *in.Agents
		}
		if in.SharedDirs != nil {
			t.SharedDirs = *in.SharedDirs
		}
		if in.MemoryUse != nil {
			t.MemoryUse = in.MemoryUse
		}
		if in.MemoryUse30 != nil {
			t.MemoryUse30 = in.MemoryUse30
		}
		if in.SkillUpdates != nil {
			t.SkillUpdates = *in.SkillUpdates
		}
		if in.PCLinks != nil {
			t.PCLinks = *in.PCLinks
		}
		if in.SkillRepos != nil {
			t.SkillRepos = *in.SkillRepos
		}
		if in.AnalysisRun != nil {
			t.AnalysisRun = in.AnalysisRun
		}
		if in.Extractor != nil {
			t.Extractor = in.Extractor
		}
		t.settle(in.Results, now)
		t.expireLogins(now)
	})
}

func scopeQuery(r *http.Request) Scope {
	q := r.URL.Query()
	return Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")}
}

func agentToolsRoutes(mux routeRegistrar, s *Service) {
	agentToolLoginRoutes(mux, s)
	galleryArchiveRoutes(mux, s)
	mux.HandleFunc("GET /v1/agent_tools", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.AgentTools(r.Context(), bearer(r), scopeQuery(r))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/agent_tools", func(w http.ResponseWriter, r *http.Request) {
		var in AgentToolsReport
		if !decode(w, r, &in) {
			return
		}
		out, e := s.ReportAgentTools(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/agent_tools/requests", func(w http.ResponseWriter, r *http.Request) {
		var in AgentToolSwitch
		if !decode(w, r, &in) {
			return
		}
		out, e := s.RequestAgentTool(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/prompts", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.ListPrompts(r.Context(), bearer(r), scopeQuery(r))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/prompts", func(w http.ResponseWriter, r *http.Request) {
		var in PromptInput
		if !decodeLimit(w, r, &in, maxPromptBody) {
			return
		}
		out, e := s.CreatePrompt(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/prompts/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in PromptUpdate
		if !decodeLimit(w, r, &in, maxPromptBody) {
			return
		}
		out, e := s.UpdatePrompt(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/design_systems", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.ListDesignSystems(r.Context(), bearer(r), scopeQuery(r))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/design_systems", func(w http.ResponseWriter, r *http.Request) {
		var in DesignSystemInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.CreateDesignSystem(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/design_systems/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in DesignSystemUpdate
		if !decode(w, r, &in) {
			return
		}
		out, e := s.UpdateDesignSystem(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
}
