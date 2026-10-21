package memory

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Crew: the operator's workflows, templates and fill personas. A workflow is a
// step graph called as "$handle", from the desktop, or by an optional
// schedule or webhook trigger. One record per item; archiving hides it.
// Execution happens on the operator's host (cmd/arkism-crew); this store
// only keeps the definitions, so it never holds keys or tokens.
const colCrew = "crew"

// template: a reusable expert definition by category (05a 템플릿에서);
// persona: a fill persona the AI helpers speak as (13 채우기 페르소나).
// agent: a work agent called as "@handle" (the successor of template: every
// template field, plus model, budget and output settings); team: agents that
// meet (crew_meetings). Agents and teams share the @-namespace, workflows keep $.
var crewTypes = []string{"workflow", "template", "persona", "agent", "team"}
var crewEfforts = []string{"low", "medium", "high", "max"}
var crewRunModes = []string{"fixed", "caller"}
var crewAgents = []string{"claude_code", "codex", "gemini", "zcode"}
var crewTriggers = []string{"schedule", "webhook"}

// Where a work agent may answer: the agent CLIs, Discord and the desktop app.
var crewAgentTargets = append(slices.Clone(crewAgents), "discord", "app")
var crewModelModes = []string{"fixed", "auto"}
var crewOutputFormats = []string{"text", "markdown", "json"}
var crewFileEdits = []string{"ask", "auto", "never"}
var crewMemberRoles = []string{"moderator", "worker", "reviewer", "scribe"}
var crewMeetingModes = []string{"round_robin", "moderator_picks"}
var crewContextShares = []string{"team", "shared"}
var crewTemplateKeyRe = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)

const crewAgentInstructions = 4000

// Where an expert is installed as a file (Gemini CLI takes skills only).
var crewInstallTargets = []string{"claude_code", "codex", "zcode"}
var crewSkillRuleFields = []string{"category", "name"}

// Memory kinds an expert may take in, and how widely.
var crewMemoryKinds = []string{"user", "procedural", "decision", "lesson"}
var crewMemoryScopes = []string{"all", "category", "expert", "off"}
var crewScriptLangs = []string{"bash", "sh", "python", "go"}
var crewFileRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
var crewAvatarBgs = []string{"sand", "tan", "sage", "sky", "lilac", "rose", "cream", "mint"}

// Node kinds a graph may use. Everything here runs on Arkism itself.
var crewNodeTypes = []string{"start", "schedule", "webhook", "ai", "ai_once", "code", "http", "github", "approval",
	"condition", "merge", "loop", "wait", "notify", "end", "subflow", "expert", "team", "n8n"}

var crewHandleRe = regexp.MustCompile(`^[\p{L}\p{N}_-]{1,40}$`)
var crewModelRe = regexp.MustCompile(`^[a-zA-Z0-9._ -]{1,80}$`)

type CrewAvatar struct {
	Art string `json:"art"`
	Bg  string `json:"bg"`
}

type CrewTrigger struct {
	Kind  string `json:"kind"`
	Cron  string `json:"cron,omitempty"`
	Label string `json:"label,omitempty"`
}

type CrewNode struct {
	ID     string            `json:"id"`
	Type   string            `json:"type"`
	Title  string            `json:"title,omitempty"`
	X      int               `json:"x"`
	Y      int               `json:"y"`
	Config map[string]string `json:"config,omitempty"`
}

// CrewEdge with Max > 0 goes back (되돌아가는 선): the engine may follow it
// at most Max times per run. The other edges form a DAG.
type CrewEdge struct {
	From string `json:"from"`
	Port string `json:"port,omitempty"`
	To   string `json:"to"`
	Max  int    `json:"max,omitempty"`
}

// CrewLimits stops a workflow run after so many node starts or minutes.
type CrewLimits struct {
	MaxSteps   int `json:"max_steps,omitempty"`
	MaxMinutes int `json:"max_minutes,omitempty"`
}

// CrewSkillRule pulls in every skill whose category is Value, or whose name
// matches Value ("release-*"); skills added later join when they match.
type CrewSkillRule struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// CrewScript is a script the expert may run; the agent asks before running it.
type CrewScript struct {
	Name string `json:"name"`
	Desc string `json:"desc,omitempty"`
	Lang string `json:"lang"`
	Code string `json:"code"`
}

// CrewBudget: MonthlyUSD is the agent's estimated spend a month before a run
// waits for a person (budget waiting); AskOverUSD asks before one run or
// meeting spends more than that. 0 means no limit.
type CrewBudget struct {
	MonthlyUSD float64 `json:"monthly_usd"`
	AskOverUSD float64 `json:"ask_over_usd,omitempty"`
}

// CrewMember is one agent in a team, in speaking order.
type CrewMember struct {
	AgentID       string `json:"agent_id"`
	Order         int    `json:"order"`
	Role          string `json:"role"` // moderator | worker | reviewer | scribe
	Focus         string `json:"focus,omitempty"`
	ModelOverride string `json:"model_override,omitempty"`
}

// CrewEnding: a meeting ends after MaxRounds, at TokenCap (0 none), when every
// reviewer approves (EndOnAllApproved) or the moderator says END (EndOnModerator).
type CrewEnding struct {
	MaxRounds        int   `json:"max_rounds"`
	TokenCap         int64 `json:"token_cap,omitempty"`
	EndOnAllApproved bool  `json:"end_on_all_approved,omitempty"`
	EndOnModerator   bool  `json:"end_on_moderator,omitempty"`
}

// CrewContextRef is a record a team reads before it meets; share "shared" lets
// Discord-started meetings read it too.
type CrewContextRef struct {
	Collection string `json:"collection"`
	ID         string `json:"id"`
	Title      string `json:"title,omitempty"`
	Share      string `json:"share"`
}

type CrewTemplateOverrides struct {
	Mode   string      `json:"mode,omitempty"`
	Ending *CrewEnding `json:"ending,omitempty"`
}

// CrewMeetingTemplate is a ready meeting prompt of a team (e.g. "코드 리뷰").
type CrewMeetingTemplate struct {
	Key       string                 `json:"key"`
	Name      string                 `json:"name"`
	Prompt    string                 `json:"prompt,omitempty"`
	Overrides *CrewTemplateOverrides `json:"overrides,omitempty"`
}

type CrewGraph struct {
	Nodes []CrewNode `json:"nodes"`
	Edges []CrewEdge `json:"edges"`
}

type Crew struct {
	Type     string      `json:"type"`
	Name     string      `json:"name"`
	Handle   string      `json:"handle,omitempty"`
	Role     string      `json:"role,omitempty"`
	Category string      `json:"category,omitempty"`
	Avatar   *CrewAvatar `json:"avatar,omitempty"`
	// template and persona (model: also a workflow's default worker model)
	Instructions string   `json:"instructions,omitempty"`
	Model        string   `json:"model,omitempty"`
	Effort       string   `json:"effort,omitempty"`
	RunMode      string   `json:"run_mode,omitempty"`
	Skills       []string `json:"skills,omitempty"`
	Tools        []string `json:"tools,omitempty"`
	Agents       []string `json:"agents,omitempty"`
	// template: 스킬 묶음, 설치, 참고하는 기억, 스크립트
	SkillRules []CrewSkillRule   `json:"skill_rules,omitempty"`
	SkillsOff  []string          `json:"skills_off,omitempty"`
	FileName   string            `json:"file_name,omitempty"`
	Install    []string          `json:"install,omitempty"`
	Memory     map[string]string `json:"memory,omitempty"`
	Scripts    []CrewScript      `json:"scripts,omitempty"`
	Template   string            `json:"template,omitempty"` // "category/name@version" it came from
	// workflow
	Trigger *CrewTrigger `json:"trigger,omitempty"`
	Enabled bool         `json:"enabled,omitempty"`
	Limits  *CrewLimits  `json:"limits,omitempty"`
	Graph   *CrewGraph   `json:"graph,omitempty"`
	Cover   *CrewCover   `json:"cover,omitempty"`  // 카드 배경 사진 (Unsplash)
	Goal    *CrewGoal    `json:"goal,omitempty"`   // 목표 (crew_goal.go)
	Notify  *CrewNotify  `json:"notify,omitempty"` // 진행 알림 (crew_goal.go)
	// agent
	ModelMode       string      `json:"model_mode,omitempty"` // fixed | auto (Clef picks)
	FallbackModel   string      `json:"fallback_model,omitempty"`
	ImageModel      string      `json:"image_model,omitempty"`
	Temperature     *float64    `json:"temperature,omitempty"`
	MaxOutputTokens int         `json:"max_output_tokens,omitempty"`
	OutputFormat    string      `json:"output_format,omitempty"`
	FileEdits       string      `json:"file_edits,omitempty"`
	Workflow        string      `json:"workflow,omitempty"` // handle of the workflow ($) this agent runs as its way of working
	Budget          *CrewBudget `json:"budget,omitempty"`
	// agent and team: records always read first (crew_pins.go)
	Pinned []CrewPin `json:"pinned,omitempty"`
	// team
	Members       []CrewMember          `json:"members,omitempty"`
	Mode          string                `json:"mode,omitempty"`
	Ending        *CrewEnding           `json:"ending,omitempty"`
	Context       []CrewContextRef      `json:"context,omitempty"`
	Templates     []CrewMeetingTemplate `json:"templates,omitempty"`
	DiscordMirror bool                  `json:"discord_mirror,omitempty"`
}

// CrewCover is an Unsplash photo hotlinked from images.unsplash.com, with the
// credit Unsplash requires next to it.
type CrewCover struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Thumb     string `json:"thumb"`
	Author    string `json:"author"`
	AuthorURL string `json:"author_url"`
}

type CrewInput struct {
	Scope Scope `json:"scope"`
	Crew  Crew  `json:"crew"`
}

type CrewUpdate struct {
	Scope            Scope `json:"scope"`
	ExpectedRevision int64 `json:"expected_revision"`
	Crew             *Crew `json:"crew,omitempty"`
	Archive          bool  `json:"archive,omitempty"`
}

type CrewPage struct {
	Items []Record `json:"items"`
}

// CrewResolution is what a hook needs to call "$handle". Members and
// LeaderModels are always empty (kept for older readers).
type CrewResolution struct {
	Item         Record   `json:"item"`
	Members      []Record `json:"members"`
	LeaderModels []string `json:"leader_models"`
}

func runes(s string) int { return utf8.RuneCountInString(s) }

func validCrewText(c Crew) bool {
	name := runes(strings.TrimSpace(c.Name))
	return name > 0 && name <= 60 && runes(c.Role) <= 200 && runes(c.Category) <= 40 &&
		runes(c.Instructions) <= 20000 &&
		shortList(c.Skills, 64, 128) && shortList(c.Tools, 32, 128)
}

func validCrewAvatar(a *CrewAvatar) bool {
	return a == nil || len(a.Art) > 0 && len(a.Art) <= 16 && slices.Contains(crewAvatarBgs, a.Bg)
}

// validCrewKit checks a template's fields beyond the basics.
func validCrewKit(c Crew) bool { return validCrewKitFor(c, crewAgents) }

func validCrewKitFor(c Crew, agents []string) bool {
	if len(c.SkillRules) > 16 || !shortList(c.SkillsOff, 64, 128) || !subset(c.Install, crewInstallTargets) || len(c.Install) > 4 ||
		c.FileName != "" && !crewFileRe.MatchString(c.FileName) || len(c.Memory) > 4 || len(c.Scripts) > 8 || runes(c.Template) > 120 ||
		!subset(c.Agents, agents) || len(c.Agents) > len(agents) {
		return false
	}
	return validSkillRules(c.SkillRules) && validCrewMemory(c.Memory) && validCrewScripts(c.Scripts) &&
		c.Trigger == nil && c.Graph == nil && c.Limits == nil
}

func validSkillRules(rules []CrewSkillRule) bool {
	for _, r := range rules {
		if !slices.Contains(crewSkillRuleFields, r.Field) || strings.TrimSpace(r.Value) == "" || runes(r.Value) > 64 {
			return false
		}
	}
	return true
}

func validCrewMemory(m map[string]string) bool {
	for k, v := range m {
		if !slices.Contains(crewMemoryKinds, k) || !slices.Contains(crewMemoryScopes, v) {
			return false
		}
	}
	return true
}

func validCrewScripts(scripts []CrewScript) bool {
	for _, s := range scripts {
		base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(s.Name, ".sh"), ".py"), ".go")
		if !crewFileRe.MatchString(base) || !slices.Contains(crewScriptLangs, s.Lang) || runes(s.Desc) > 200 || runes(s.Code) > 20000 {
			return false
		}
	}
	return true
}

// A template needs a category and may leave the model open.
func validCrewTemplate(c Crew) bool {
	return strings.TrimSpace(c.Category) != "" && (c.Model == "" || crewModelRe.MatchString(c.Model)) &&
		(c.Effort == "" || slices.Contains(crewEfforts, c.Effort)) && (c.RunMode == "" || slices.Contains(crewRunModes, c.RunMode)) && validCrewKit(c) &&
		noAgentFields(c) && noTeamFields(c)
}

func noAgentFields(c Crew) bool {
	return c.ModelMode == "" && c.FallbackModel == "" && c.ImageModel == "" && c.Temperature == nil && c.MaxOutputTokens == 0 &&
		c.OutputFormat == "" && c.FileEdits == "" && c.Workflow == "" && c.Budget == nil
}

func noTeamFields(c Crew) bool {
	return len(c.Members)+len(c.Context)+len(c.Templates) == 0 && c.Mode == "" && c.Ending == nil && !c.DiscordMirror
}

func optionalModel(m string) bool { return m == "" || crewModelRe.MatchString(m) }

func validUSD(v float64) bool { return v >= 0 && v <= 1e5 } // NaN fails both comparisons

// validCrewAgent: a template's fields (category optional) plus the agent's own.
func validCrewAgent(c Crew) bool {
	if runes(c.Instructions) > crewAgentInstructions || c.ModelMode != "" && !slices.Contains(crewModelModes, c.ModelMode) ||
		!optionalModel(c.Model) || c.ModelMode == "auto" && c.Model != "" && c.Model != "auto" ||
		!optionalModel(c.FallbackModel) || !optionalModel(c.ImageModel) ||
		c.Effort != "" && !slices.Contains(crewEfforts, c.Effort) || c.RunMode != "" && !slices.Contains(crewRunModes, c.RunMode) ||
		c.Temperature != nil && !(*c.Temperature >= 0 && *c.Temperature <= 1) || c.MaxOutputTokens < 0 || c.MaxOutputTokens > 200000 ||
		c.OutputFormat != "" && !slices.Contains(crewOutputFormats, c.OutputFormat) || c.FileEdits != "" && !slices.Contains(crewFileEdits, c.FileEdits) ||
		c.Workflow != "" && !crewHandleRe.MatchString(c.Workflow) ||
		c.Budget != nil && (!validUSD(c.Budget.MonthlyUSD) || !validUSD(c.Budget.AskOverUSD)) {
		return false
	}
	return validCrewKitFor(c, crewAgentTargets) && noTeamFields(c)
}

func validCrewEnding(e *CrewEnding) bool {
	return e == nil || e.MaxRounds >= 1 && e.MaxRounds <= 10 && e.TokenCap >= 0 && e.TokenCap <= 5_000_000
}

func validCrewTeam(c Crew) bool {
	if len(c.Members) == 0 || len(c.Members) > 12 || !slices.Contains(crewMeetingModes, c.Mode) || !validCrewEnding(c.Ending) ||
		len(c.Context) > 20 || len(c.Templates) > 10 || c.Instructions != "" || c.Model != "" || c.Effort != "" || c.RunMode != "" ||
		len(c.Skills)+len(c.Tools)+len(c.Agents)+len(c.SkillRules)+len(c.SkillsOff)+len(c.Install)+len(c.Memory)+len(c.Scripts) != 0 ||
		c.FileName != "" || c.Template != "" || !noAgentFields(c) {
		return false
	}
	seen, moderators := map[string]bool{}, 0
	for _, m := range c.Members {
		if !validRunLabel(m.AgentID, 256) || seen[m.AgentID] || !slices.Contains(crewMemberRoles, m.Role) || m.Order < 0 || m.Order > 100 ||
			runes(m.Focus) > 200 || !optionalModel(m.ModelOverride) {
			return false
		}
		seen[m.AgentID] = true
		if m.Role == "moderator" {
			moderators++
		}
	}
	if c.Mode == "moderator_picks" && moderators == 0 || moderators > 1 {
		return false
	}
	for _, r := range c.Context {
		if !knownCollection(r.Collection) || !validRunLabel(r.ID, 256) || runes(r.Title) > 120 || !slices.Contains(crewContextShares, r.Share) {
			return false
		}
	}
	keys := map[string]bool{}
	for _, t := range c.Templates {
		if !crewTemplateKeyRe.MatchString(t.Key) || keys[t.Key] || strings.TrimSpace(t.Name) == "" || runes(t.Name) > 60 || runes(t.Prompt) > 4000 {
			return false
		}
		keys[t.Key] = true
		if o := t.Overrides; o != nil && (o.Mode != "" && !slices.Contains(crewMeetingModes, o.Mode) || !validCrewEnding(o.Ending)) {
			return false
		}
	}
	return true
}

func validCrewTrigger(t *CrewTrigger) bool {
	if t == nil || !slices.Contains(crewTriggers, t.Kind) || runes(t.Label) > 60 {
		return false
	}
	if t.Kind == "schedule" {
		return len(strings.Fields(t.Cron)) == 5 && len(t.Cron) <= 64
	}
	return t.Cron == ""
}

// A photo the person uploaded: the desktop shrinks it to a small JPEG and sends it as a data URL (id "mine").
const crewCoverUploadMax = 150_000

func validCrewCover(c *CrewCover) bool {
	if c == nil {
		return true
	}
	if c.ID == "mine" {
		// The whole URL must decode as standard base64 to a JPEG (FF D8 FF), so it is safe in src and CSS url().
		b64, ok := strings.CutPrefix(c.URL, "data:image/jpeg;base64,")
		raw, err := base64.StdEncoding.DecodeString(b64)
		return ok && err == nil && bytes.HasPrefix(raw, []byte{0xff, 0xd8, 0xff}) && len(c.URL) <= crewCoverUploadMax &&
			c.Thumb == "" && strings.TrimSpace(c.Author) != "" && runes(c.Author) <= 80 && c.AuthorURL == ""
	}
	img := func(u string) bool { return len(u) <= 600 && strings.HasPrefix(u, "https://images.unsplash.com/") }
	return unsplashIDRe.MatchString(c.ID) && img(c.URL) && img(c.Thumb) && strings.TrimSpace(c.Author) != "" && runes(c.Author) <= 80 &&
		len(c.AuthorURL) <= 300 && strings.HasPrefix(c.AuthorURL, "https://unsplash.com/")
}

func validCrewLimits(l *CrewLimits) bool {
	return l == nil || l.MaxSteps >= 0 && l.MaxSteps <= 500 && l.MaxMinutes >= 0 && l.MaxMinutes <= 1440
}

// A workflow is a graph with a handle; the template-only fields stay empty.
func validCrewWorkflow(c Crew) bool {
	return c.Graph != nil && len(c.Graph.Nodes) > 0 && validCrewGraph(c.Graph) && (c.Trigger == nil || validCrewTrigger(c.Trigger)) &&
		(c.Model == "" || crewModelRe.MatchString(c.Model)) && validCrewLimits(c.Limits) &&
		c.Instructions == "" && c.Effort == "" && c.RunMode == "" && len(c.Skills)+len(c.Tools)+len(c.Agents)+len(c.SkillRules)+len(c.SkillsOff)+
		len(c.Install)+len(c.Memory)+len(c.Scripts) == 0 && c.FileName == "" && c.Template == "" && noAgentFields(c) && noTeamFields(c)
}

func validCrew(c Crew) bool {
	if !slices.Contains(crewTypes, c.Type) || !validCrewText(c) || !validCrewAvatar(c.Avatar) || !validCrewCover(c.Cover) ||
		!validCrewGoalNotify(c) || !validCrewPins(c.Pinned) || len(c.Pinned) > 0 && c.Type != "agent" && c.Type != "team" {
		return false
	}
	switch c.Type {
	case "workflow", "team":
		if !crewHandleRe.MatchString(c.Handle) {
			return false
		}
	case "agent":
		// Optional: a template turned agent has none until someone names it.
		if c.Handle != "" && !crewHandleRe.MatchString(c.Handle) {
			return false
		}
	default:
		if c.Handle != "" {
			return false
		}
	}
	if c.Type != "workflow" && c.Type != "team" && c.Cover != nil || c.Type != "workflow" && (c.Limits != nil || c.Trigger != nil || c.Graph != nil) {
		return false
	}
	switch c.Type {
	case "workflow":
		return validCrewWorkflow(c)
	case "template":
		return validCrewTemplate(c)
	case "agent":
		return validCrewAgent(c)
	case "team":
		return validCrewTeam(c)
	default: // persona
		return strings.TrimSpace(c.Instructions) != "" && c.Model == "" && len(c.Skills)+len(c.Tools)+len(c.Agents) == 0 &&
			c.Trigger == nil && c.Graph == nil && noAgentFields(c) && noTeamFields(c)
	}
}

// isAgent: a work agent, including a template saved before agents existed.
func isAgent(c *Crew) bool { return c != nil && (c.Type == "agent" || c.Type == "template") }

// crewNamespace is "$" for workflows and "@" for agents and teams; other types are never called by name.
func crewNamespace(typ string) string {
	switch typ {
	case "workflow":
		return "$"
	case "agent", "team":
		return "@"
	}
	return ""
}

func validCrewNode(n CrewNode) bool {
	if !validRunLabel(n.ID, 40) || !slices.Contains(crewNodeTypes, n.Type) || runes(n.Title) > 80 ||
		n.X < -20000 || n.X > 20000 || n.Y < -20000 || n.Y > 20000 || len(n.Config) > 20 {
		return false
	}
	for k, v := range n.Config {
		if k == "" || len(k) > 40 || runes(v) > 20000 {
			return false
		}
	}
	return true
}

// validCrewGraph accepts an absent graph, or nodes joined by edges whose
// forward edges (max 0) have no cycle; a back edge joins two different nodes.
func validCrewGraph(g *CrewGraph) bool {
	if g == nil {
		return true
	}
	if len(g.Nodes) > 100 || len(g.Edges) > 200 {
		return false
	}
	next := map[string][]string{}
	for _, n := range g.Nodes {
		if !validCrewNode(n) {
			return false
		}
		if _, dup := next[n.ID]; dup {
			return false
		}
		next[n.ID] = []string{}
	}
	for _, e := range g.Edges {
		_, okFrom := next[e.From]
		_, okTo := next[e.To]
		if !okFrom || !okTo || e.From == e.To || len(e.Port) > 20 || e.Max < 0 || e.Max > 10 {
			return false
		}
		if e.Max == 0 {
			next[e.From] = append(next[e.From], e.To)
		}
	}
	return !hasCycle(next)
}

func hasCycle(next map[string][]string) bool {
	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	var visit func(string) bool
	visit = func(id string) bool {
		switch state[id] {
		case visiting:
			return true
		case done:
			return false
		}
		state[id] = visiting
		for _, n := range next[id] {
			if visit(n) {
				return true
			}
		}
		state[id] = done
		return false
	}
	for id := range next {
		if visit(id) {
			return true
		}
	}
	return false
}

// crewKey folds a handle so "$릴리스 매니저" and "$릴리스매니저" meet ("@리뷰어" the same way).
func crewKey(handle string) string {
	h := strings.TrimSpace(handle)
	h = strings.TrimPrefix(strings.TrimPrefix(h, "$"), "@")
	return strings.ToLower(strings.ReplaceAll(h, " ", ""))
}

// checkCrewRefs keeps handles unique within their namespace among live items
// ($ workflows; @ agents and teams together) and a team's members live agents.
// self is the record being changed, if any.
// shortcut: checked from a read before the write transaction; one operator, so no race guard.
func (s *Service) checkCrewRefs(ctx context.Context, scope Scope, c Crew, self string) error {
	rows, err := s.Repo.ListRecords(ctx, scope, colCrew, 1000)
	if err != nil {
		return err
	}
	agents := map[string]bool{}
	flows := map[string]bool{}  // live workflow key -> it has a schedule or webhook on
	owners := map[string]bool{} // workflow keys another live agent works by
	var prev *Crew
	ns := crewNamespace(c.Type)
	for _, r := range rows {
		if r.ID == self {
			prev = r.Crew
		}
		if r.Status != statusActive || r.Crew == nil || r.ID == self {
			continue
		}
		if isAgent(r.Crew) {
			agents[r.ID] = true
		}
		if r.Crew.Type == "agent" && r.Crew.Workflow != "" {
			owners[crewKey(r.Crew.Workflow)] = true
		}
		if r.Crew.Type == "workflow" {
			flows[crewKey(r.Crew.Handle)] = r.Crew.Trigger != nil && r.Crew.Enabled
		}
		if c.Handle != "" && r.Crew.Handle != "" && crewNamespace(r.Crew.Type) == ns && crewKey(r.Crew.Handle) == crewKey(c.Handle) {
			return ErrHandleTaken
		}
	}
	for _, m := range c.Members {
		if !agents[m.AgentID] {
			return ErrInvalid
		}
	}
	// An owned workflow runs only through its one agent (CreateCrewRun), so it must exist, be free and have no live trigger.
	// A reference kept unchanged on save passes, so an agent whose workflow went away can still be edited.
	if k := crewKey(c.Workflow); c.Type == "agent" && k != "" && (prev == nil || crewKey(prev.Workflow) != k) {
		if on, live := flows[k]; !live || on || owners[k] {
			return ErrInvalid
		}
	}
	if c.Type == "workflow" && c.Trigger != nil && c.Enabled && owners[crewKey(c.Handle)] {
		return ErrInvalid
	}
	return nil
}

func (s *Service) ListCrew(ctx context.Context, token string, scope Scope) (CrewPage, error) {
	if !validScope(scope) {
		return CrewPage{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colCrew, scope, "", ""); err != nil {
		return CrewPage{}, err
	}
	// shortcut: one page of 1000; page with a cursor if the crew outgrows it.
	rows, err := s.Repo.ListRecords(ctx, scope, colCrew, 1000)
	if err != nil {
		return CrewPage{}, err
	}
	out := CrewPage{Items: []Record{}}
	for _, r := range rows {
		if r.Status == statusActive && r.Crew != nil {
			out.Items = append(out.Items, r)
		}
	}
	return out, nil
}

func (s *Service) CreateCrew(ctx context.Context, token string, in CrewInput) (Record, error) {
	if !validScope(in.Scope) || !validCrew(in.Crew) {
		return Record{}, ErrInvalid
	}
	if err := noSecrets(in.Crew); err != nil {
		return Record{}, err
	}
	d, err := s.authorize(ctx, token, "write:"+colCrew, in.Scope, "", "")
	if err != nil {
		return Record{}, err
	}
	if d.Role != roleHuman {
		return Record{}, ErrDenied
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	c := in.Crew
	r := Record{ID: ID(), Scope: in.Scope, Kind: colCrew, Status: statusActive, Revision: 1, Text: c.Name, Crew: &c,
		CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role}
	if err := s.checkCrewRefs(ctx, in.Scope, c, ""); err != nil {
		return Record{}, err
	}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkCrewPins(ctx, tx, in.Scope, c, nil); err != nil {
			return err
		}
		if err := tx.Insert(ctx, colCrew, r); err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "create:"+colCrew, r.ID)
	})
	return r, err
}

// UpdateCrew replaces an item's definition, or archives it. The type never changes.
func (s *Service) UpdateCrew(ctx context.Context, token, id string, in CrewUpdate) (Record, error) {
	if !validScope(in.Scope) || !validRunLabel(id, 256) || in.ExpectedRevision < 1 || (in.Crew == nil) == !in.Archive || in.Crew != nil && !validCrew(*in.Crew) {
		return Record{}, ErrInvalid
	}
	if in.Crew != nil {
		if err := noSecrets(*in.Crew); err != nil {
			return Record{}, err
		}
	}
	d, err := s.authorize(ctx, token, "write:"+colCrew, in.Scope, id, "")
	if err != nil {
		return Record{}, err
	}
	if d.Role != roleHuman {
		return Record{}, ErrDenied
	}
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, colCrew, in.Scope, id)
		if err != nil {
			return err
		}
		if r.Revision != in.ExpectedRevision || r.Status != statusActive || r.Crew == nil {
			return ErrConflict
		}
		if in.Crew != nil {
			// The type never changes, except that a template becomes an agent (once, on its next save).
			if in.Crew.Type != r.Crew.Type && !(r.Crew.Type == "template" && in.Crew.Type == "agent") {
				return ErrInvalid
			}
			if err := s.checkCrewRefs(ctx, in.Scope, *in.Crew, id); err != nil {
				return err
			}
			if err := checkCrewPins(ctx, tx, in.Scope, *in.Crew, r.Crew); err != nil {
				return err
			}
		}
		prev := r.Revision
		if in.Archive {
			r.Status = "archived"
		} else {
			if err := keepCrewVersion(ctx, tx, r); err != nil {
				return err
			}
			c := *in.Crew
			r.Crew, r.Text = &c, c.Name
		}
		r.Revision++
		r.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
		if err := tx.Replace(ctx, colCrew, r, prev); err != nil {
			return err
		}
		out = r
		action := "update:" + colCrew
		if in.Archive {
			action = "archive:" + colCrew
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, action, id)
	})
	return out, err
}

// ResolveCrew finds the live workflow called "$handle" (or a bare handle), or
// the live agent or team called "@handle".
func (s *Service) ResolveCrew(ctx context.Context, token string, scope Scope, handle string) (CrewResolution, error) {
	ns := "$"
	if strings.HasPrefix(strings.TrimSpace(handle), "@") {
		ns = "@"
	}
	key := crewKey(handle)
	if !validScope(scope) || !crewHandleRe.MatchString(key) {
		return CrewResolution{}, ErrInvalid
	}
	page, err := s.ListCrew(ctx, token, scope)
	if err != nil {
		return CrewResolution{}, err
	}
	for _, r := range page.Items {
		if crewNamespace(r.Crew.Type) == ns && r.Crew.Handle != "" && crewKey(r.Crew.Handle) == key {
			return CrewResolution{Item: r, Members: []Record{}, LeaderModels: []string{}}, nil
		}
	}
	return CrewResolution{}, ErrNotFound
}

func crewRoutes(mux routeRegistrar, s *Service) {
	unsplashRoutes(mux, s)
	crewVersionRoutes(mux, s)
	mux.HandleFunc("GET /v1/crew", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.ListCrew(r.Context(), bearer(r), scopeQuery(r))
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/crew/resolve", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.ResolveCrew(r.Context(), bearer(r), scopeQuery(r), r.URL.Query().Get("handle"))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/crew", func(w http.ResponseWriter, r *http.Request) {
		var in CrewInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.CreateCrew(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/crew/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in CrewUpdate
		if !decode(w, r, &in) {
			return
		}
		out, e := s.UpdateCrew(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
}
