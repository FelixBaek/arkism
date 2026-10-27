package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Crew meetings (회의): a team's agents talk a topic over in rounds on the
// operator's host. A person (or the Discord bot, or a $call) asks for one
// here; the host engine (arkism-crew) leases it, copies the team into
// team_snapshot, runs turns and writes each one back with a service token.
// The engine may add turns and consume human notes, never delete them.
// The conclusion's decisions and lessons become memory candidates through
// /v1/capture (source = the meeting id); the full transcript stays here only.
const (
	colCrewMeetings     = "crew_meetings"
	crewMeetingMaxBytes = 2 << 20
	// crewMeetingLease is how long a progress write keeps other engines off.
	crewMeetingLease = 2 * time.Minute
	crewMeetingTurns = 400
	crewMeetingNotes = 50
)

var crewMeetingStatuses = []string{"pending", "running", "stopping", "waiting", "concluded", "no_conclusion", "stopped", "failed"}
var crewMeetingTriggers = []string{"manual", "discord", "call", "workflow"}
var crewTurnKinds = []string{"brief", "turn", "human", "conclusion"}
var crewStances = []string{"agree", "disagree", "opinion", "approve"}
var crewEndedReasons = []string{"max_rounds", "token_cap", "all_approved", "moderator", "human_stop", "budget", "error"}
var crewLeaseTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)
var discordIDRe = regexp.MustCompile(`^[0-9]{5,25}$`)

type MeetingTurn struct {
	Seq          int        `json:"seq"`
	Round        int        `json:"round"`
	AgentID      string     `json:"agent_id,omitempty"`
	Speaker      string     `json:"speaker"`
	Model        string     `json:"model,omitempty"`
	Kind         string     `json:"kind"`
	Stance       string     `json:"stance,omitempty"`
	Text         string     `json:"text"`
	InputTokens  int64      `json:"input_tokens,omitempty"`
	OutputTokens int64      `json:"output_tokens,omitempty"`
	CostUSD      float64    `json:"cost_usd,omitempty"`
	Estimated    bool       `json:"estimated,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	OK           bool       `json:"ok"`
	Error        string     `json:"error,omitempty"`
}

type MeetingNote struct {
	By   string    `json:"by"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

type MeetingConclusion struct {
	Summary       string   `json:"summary"`
	Decisions     []string `json:"decisions"`
	Todos         []string `json:"todos"`
	OpenQuestions []string `json:"open_questions"`
}

type MeetingWaiting struct {
	Kind   string    `json:"kind"` // budget
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

type MeetingSaved struct {
	CaptureEventID string   `json:"capture_event_id,omitempty"`
	CandidateIDs   []string `json:"candidate_ids,omitempty"`
}

type MeetingDiscord struct {
	BotID         string `json:"bot_id,omitempty"`
	GuildID       string `json:"guild_id,omitempty"`
	ChannelID     string `json:"channel_id,omitempty"`
	ThreadID      string `json:"thread_id,omitempty"`
	ThreadURL     string `json:"thread_url,omitempty"`
	LastPostedSeq int    `json:"last_posted_seq,omitempty"` // highest turn seq posted; 0 none (seqs start at 1)
}

// MeetingSnapshot is the team and its agents as the meeting started.
type MeetingSnapshot struct {
	Team   Crew            `json:"team"`
	Agents map[string]Crew `json:"agents"`
}

// MeetingApproval records the person who let a meeting go over its budget.
type MeetingApproval struct {
	By string    `json:"by"`
	At time.Time `json:"at"`
}

type CrewMeeting struct {
	RootID          string             `json:"root_id,omitempty"`
	ParentID        string             `json:"parent_id,omitempty"`
	ParentNodeID    string             `json:"parent_node_id,omitempty"`
	Status          string             `json:"status"`
	TeamID          string             `json:"team_id"`
	TeamRevision    int64              `json:"team_revision"`
	TeamSnapshot    *MeetingSnapshot   `json:"team_snapshot,omitempty"`
	Title           string             `json:"title"`
	Topic           string             `json:"topic"`
	TemplateKey     string             `json:"template_key,omitempty"`
	Trigger         string             `json:"trigger"`
	Round           int                `json:"round"`
	MaxRounds       int                `json:"max_rounds"`
	Speaking        string             `json:"speaking,omitempty"`
	Turns           []MeetingTurn      `json:"turns"`
	NotesPending    []MeetingNote      `json:"notes_pending"`
	TokensUsed      int64              `json:"tokens_used"`
	TokenCap        int64              `json:"token_cap,omitempty"`
	CostUSD         float64            `json:"cost_usd"`
	CostEstimateUSD float64            `json:"cost_estimate_usd,omitempty"`
	Conclusion      *MeetingConclusion `json:"conclusion,omitempty"`
	EndedReason     string             `json:"ended_reason,omitempty"`
	Waiting         *MeetingWaiting    `json:"waiting,omitempty"`
	Saved           *MeetingSaved      `json:"saved,omitempty"`
	Discord         *MeetingDiscord    `json:"discord,omitempty"`
	// BudgetApproved: a person let this meeting go on over budget; the engine asks no more.
	BudgetApproved *MeetingApproval `json:"budget_approved,omitempty"`
	// LeaseUntil is set by the server on every progress write: until then only that engine writes.
	LeaseUntil *time.Time `json:"lease_until,omitempty"`
}

type CrewMeetingInput struct {
	Scope   Scope       `json:"scope"`
	Meeting CrewMeeting `json:"meeting"`
}

type CrewMeetingUpdate struct {
	Scope            Scope       `json:"scope"`
	ExpectedRevision int64       `json:"expected_revision"`
	LeaseToken       string      `json:"lease_token"`
	Meeting          CrewMeeting `json:"meeting"`
}

type CrewMeetingNoteInput struct {
	Scope            Scope  `json:"scope"`
	ExpectedRevision int64  `json:"expected_revision"`
	Text             string `json:"text"`
	By               string `json:"by,omitempty"` // service callers (Discord) name the person
}

type CrewMeetingStopInput struct {
	Scope Scope `json:"scope"`
}

type CrewMeetingDecisionInput struct {
	Scope            Scope `json:"scope"`
	ExpectedRevision int64 `json:"expected_revision"`
	Approve          bool  `json:"approve"`
}

type CrewMeetingDiscordInput struct {
	Scope            Scope          `json:"scope"`
	ExpectedRevision int64          `json:"expected_revision"`
	Discord          MeetingDiscord `json:"discord"`
}

type CrewMeetingPage struct {
	Items  []Record       `json:"items"`
	Counts map[string]int `json:"counts"`
}

func terminalMeeting(status string) bool {
	return status == "concluded" || status == "no_conclusion" || status == "stopped" || status == "failed"
}

func shortTexts(list []string, n, maxN int) bool {
	if len(list) > n {
		return false
	}
	for _, t := range list {
		if runes(t) > maxN {
			return false
		}
	}
	return true
}

func validMeetingTurn(t MeetingTurn, seq int) bool {
	return t.Seq == seq && t.Round >= 0 && t.Round <= 11 && (t.AgentID == "" || validRunLabel(t.AgentID, 256)) && runes(t.Speaker) <= 60 &&
		optionalModel(t.Model) && slices.Contains(crewTurnKinds, t.Kind) && (t.Stance == "" || slices.Contains(crewStances, t.Stance)) &&
		runes(t.Text) <= 8000 && t.InputTokens >= 0 && t.OutputTokens >= 0 && t.InputTokens <= 1e8 && t.OutputTokens <= 1e8 &&
		t.CostUSD >= 0 && t.CostUSD <= 1e4 && runes(t.Error) <= 500
}

func validMeetingDiscord(d *MeetingDiscord) bool {
	if d == nil {
		return true
	}
	id := func(s string) bool { return s == "" || discordIDRe.MatchString(s) }
	return (d.BotID == "" || validRunLabel(d.BotID, 128)) && id(d.GuildID) && id(d.ChannelID) && id(d.ThreadID) &&
		(d.ThreadURL == "" || len(d.ThreadURL) <= 300 && strings.HasPrefix(d.ThreadURL, "https://discord.com/")) && d.LastPostedSeq >= 0
}

func validCrewMeeting(m CrewMeeting) bool {
	if !validCrewMeetingHeader(m) || !validCrewMeetingProgress(m) || !validCrewMeetingContent(m) || !validCrewMeetingState(m) {
		return false
	}
	raw, err := json.Marshal(m)
	return err == nil && len(raw) <= crewMeetingMaxBytes
}

func validCrewMeetingHeader(m CrewMeeting) bool {
	return !(!slices.Contains(crewMeetingStatuses, m.Status) || !validRunLabel(m.TeamID, 256) || m.TeamRevision < 1 ||
		strings.TrimSpace(m.Title) == "" || runes(m.Title) > 120 || strings.TrimSpace(m.Topic) == "" || runes(m.Topic) > 20000 ||
		m.TemplateKey != "" && !crewTemplateKeyRe.MatchString(m.TemplateKey) || !slices.Contains(crewMeetingTriggers, m.Trigger))
}

func validCrewMeetingProgress(m CrewMeeting) bool {
	return !(m.Round < 0 || m.Round > 10 || m.MaxRounds < 1 || m.MaxRounds > 10 || m.Speaking != "" && !validRunLabel(m.Speaking, 256) ||
		len(m.Turns) > crewMeetingTurns || len(m.NotesPending) > crewMeetingNotes || m.TokensUsed < 0 || m.TokenCap < 0 || m.TokenCap > 5_000_000 ||
		!(m.CostUSD >= 0 && m.CostUSD <= 1e5) || !(m.CostEstimateUSD >= 0 && m.CostEstimateUSD <= 1e5) || !validMeetingDiscord(m.Discord))
}

func validCrewMeetingContent(m CrewMeeting) bool {
	for i, t := range m.Turns {
		if !validMeetingTurn(t, i+1) {
			return false
		}
	}
	for _, n := range m.NotesPending {
		if strings.TrimSpace(n.Text) == "" || runes(n.Text) > 4000 || !validRunLabel(n.By, 120) {
			return false
		}
	}
	c := m.Conclusion
	return !(c != nil && (runes(c.Summary) > 4000 || !shortTexts(c.Decisions, 30, 1000) || !shortTexts(c.Todos, 30, 1000) ||
		!shortTexts(c.OpenQuestions, 30, 1000)))
}

func validCrewMeetingState(m CrewMeeting) bool {
	if terminalMeeting(m.Status) != (m.EndedReason != "") || m.EndedReason != "" && !slices.Contains(crewEndedReasons, m.EndedReason) {
		return false
	}
	if (m.Status == "waiting") != (m.Waiting != nil) || m.Waiting != nil && (m.Waiting.Kind != "budget" || runes(m.Waiting.Reason) > 500) {
		return false
	}
	if sv := m.Saved; sv != nil && (sv.CaptureEventID != "" && !validRunLabel(sv.CaptureEventID, 256) || len(sv.CandidateIDs) > 32) {
		return false
	} else if sv != nil {
		for _, id := range sv.CandidateIDs {
			if !validRunLabel(id, 256) {
				return false
			}
		}
	}
	sn := m.TeamSnapshot
	return !(sn != nil && (sn.Team.Type != "team" || len(sn.Agents) > 12))
}

// meetingEnding is the team's ending, with the template's overrides laid over it.
func meetingEnding(team *Crew, templateKey string) (CrewEnding, bool) {
	end := CrewEnding{MaxRounds: 3}
	if team.Ending != nil {
		end = *team.Ending
	}
	if templateKey == "" {
		return end, true
	}
	for _, t := range team.Templates {
		if t.Key == templateKey {
			if t.Overrides != nil && t.Overrides.Ending != nil {
				end = *t.Overrides.Ending
			}
			return end, true
		}
	}
	return end, false
}

// CreateCrewMeeting asks for a meeting of a live team; the engine starts it.
func (s *Service) CreateCrewMeeting(ctx context.Context, token string, in CrewMeetingInput) (Record, error) {
	m := in.Meeting
	if m.Trigger == "" {
		m.Trigger = "manual"
	}
	if !validScope(in.Scope) || !validRunLabel(m.TeamID, 256) || strings.TrimSpace(m.Title) == "" || runes(m.Title) > 120 ||
		strings.TrimSpace(m.Topic) == "" || runes(m.Topic) > 20000 || m.TemplateKey != "" && !crewTemplateKeyRe.MatchString(m.TemplateKey) ||
		!slices.Contains(crewMeetingTriggers, m.Trigger) || !validMeetingDiscord(m.Discord) {
		return Record{}, ErrInvalid
	}
	if err := noSecrets(m); err != nil {
		return Record{}, err
	}
	d, err := s.authorize(ctx, token, "write:"+colCrewMeetings, in.Scope, "", "")
	if err != nil {
		return Record{}, err
	}
	if !crewRunWriter(d.Role) {
		return Record{}, ErrDenied
	}
	agents, err := s.liveCrew(ctx, in.Scope)
	if err != nil {
		return Record{}, err
	}
	team, ok := agents[m.TeamID]
	if !ok || team.Crew.Type != "team" {
		return Record{}, ErrNotFound
	}
	end, ok := meetingEnding(team.Crew, m.TemplateKey)
	if !ok {
		return Record{}, ErrInvalid
	}
	est := estimateTeam(*team.Crew, agents)
	now := time.Now().UTC().Truncate(time.Millisecond)
	meeting := CrewMeeting{Status: "pending", TeamID: team.ID, TeamRevision: team.Revision, Title: strings.TrimSpace(m.Title), Topic: m.Topic,
		TemplateKey: m.TemplateKey, Trigger: m.Trigger, MaxRounds: end.MaxRounds, TokenCap: end.TokenCap, Turns: []MeetingTurn{},
		NotesPending: []MeetingNote{}, CostEstimateUSD: est.CostUSD, Discord: m.Discord}
	if meeting.Discord != nil {
		meeting.Discord.LastPostedSeq = 0
	}
	r := Record{ID: ID(), Scope: in.Scope, Kind: colCrewMeetings, Status: statusActive, Revision: 1, Text: meeting.Title, CrewMeeting: &meeting,
		CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if err := tx.Insert(ctx, colCrewMeetings, r); err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "create:"+colCrewMeetings, r.ID)
	})
	return r, err
}

// liveCrew is every live crew item of the scope by id.
func (s *Service) liveCrew(ctx context.Context, scope Scope) (map[string]Record, error) {
	rows, err := s.Repo.ListRecords(ctx, scope, colCrew, 1000)
	if err != nil {
		return nil, err
	}
	out := map[string]Record{}
	for _, r := range rows {
		if r.Status == statusActive && r.Crew != nil {
			out[r.ID] = r
		}
	}
	return out, nil
}

// meetingStatusMatch: running is every open meeting (pending, running, stopping, waiting); ended every finished one.
func meetingStatusMatch(filter, status string) bool {
	switch filter {
	case "", "all":
		return true
	case "running":
		return !terminalMeeting(status)
	case "ended":
		return terminalMeeting(status)
	}
	return filter == status
}

func briefMeeting(r Record) Record {
	m := *r.CrewMeeting
	m.TeamSnapshot = nil
	turns := make([]MeetingTurn, len(m.Turns))
	for i, t := range m.Turns {
		t.Text = clipRunes(t.Text, 0)
		turns[i] = t
	}
	m.Turns, m.Topic = turns, clipRunes(m.Topic, 300)
	r.CrewMeeting = &m
	return r
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// ListCrewMeetings returns meetings newest first with counts; brief leaves out texts and snapshots.
func (s *Service) ListCrewMeetings(ctx context.Context, token string, scope Scope, status, teamID string, limit int, brief bool) (CrewMeetingPage, error) {
	if !validCrewMeetingQuery(scope, status, teamID, limit) {
		return CrewMeetingPage{}, ErrInvalid
	}
	if limit == 0 {
		limit = 50
	}
	if _, err := s.authorize(ctx, token, "read:"+colCrew, scope, "", ""); err != nil {
		return CrewMeetingPage{}, err
	}
	// shortcut: the newest 1000 meetings; page with a cursor if a scope outgrows it.
	rows, err := s.Repo.ListRecords(ctx, scope, colCrewMeetings, 1000)
	if err != nil {
		return CrewMeetingPage{}, err
	}
	out := CrewMeetingPage{Items: []Record{}, Counts: map[string]int{"running": 0, "ended": 0, "all": 0}}
	for _, r := range rows {
		m := r.CrewMeeting
		if m == nil || teamID != "" && m.TeamID != teamID {
			continue
		}
		if err := s.authorizeLinkedMeetingSources(ctx, token, r, s.Repo); err != nil {
			if errors.Is(err, ErrDenied) || errors.Is(err, ErrNotFound) {
				continue
			}
			return CrewMeetingPage{}, err
		}
		out.Counts["all"]++
		if terminalMeeting(m.Status) {
			out.Counts["ended"]++
		} else {
			out.Counts["running"]++
		}
		if meetingStatusMatch(status, m.Status) && len(out.Items) < limit {
			if brief {
				r = briefMeeting(r)
			}
			out.Items = append(out.Items, r)
		}
	}
	return out, nil
}

func validCrewMeetingQuery(scope Scope, status, teamID string, limit int) bool {
	return !(!validScope(scope) || status != "" && status != "all" && status != "ended" && !slices.Contains(crewMeetingStatuses, status) ||
		teamID != "" && !validRunLabel(teamID, 256) || limit < 0 || limit > 200)
}

func (s *Service) GetCrewMeeting(ctx context.Context, token string, scope Scope, id string) (Record, error) {
	if !validScope(scope) || !validRunLabel(id, 256) {
		return Record{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colCrew, scope, id, ""); err != nil {
		return Record{}, err
	}
	r, err := s.Repo.Get(ctx, colCrewMeetings, scope, id)
	if err != nil {
		return Record{}, err
	}
	if err := s.authorizeLinkedMeetingSources(ctx, token, r, s.Repo); err != nil {
		return Record{}, err
	}
	return r, nil
}

// changeMeeting runs change on the stored meeting in one transaction and saves it.
func (s *Service) changeMeeting(ctx context.Context, token string, scope Scope, id string, d Decision, action string, change func(r *Record, m *CrewMeeting, now time.Time) error) (Record, error) {
	var out Record
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, colCrewMeetings, scope, id)
		if err != nil {
			return err
		}
		if err := s.authorizeLinkedMeetingSources(ctx, token, r, tx); err != nil {
			return err
		}
		m := *r.CrewMeeting
		now := time.Now().UTC().Truncate(time.Millisecond)
		if err := change(&r, &m, now); err != nil {
			return err
		}
		if !validCrewMeeting(m) {
			return ErrInvalid
		}
		prev := r.Revision
		r.CrewMeeting, r.Revision, r.UpdatedAt = &m, r.Revision+1, now
		if err := tx.Replace(ctx, colCrewMeetings, r, prev); err != nil {
			return err
		}
		out = r
		return audit(ctx, tx, scope, d.PrincipalID, action+":"+colCrewMeetings, id)
	})
	return out, err
}

func (s *Service) meetingWriter(ctx context.Context, token string, scope Scope, id string, ok func(role string) bool) (Decision, error) {
	if !validScope(scope) || !validRunLabel(id, 256) {
		return Decision{}, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "write:"+colCrewMeetings, scope, id, "")
	if err != nil {
		return d, err
	}
	if !ok(d.Role) {
		return d, ErrDenied
	}
	return d, nil
}

func humanOrService(role string) bool { return role == roleHuman || role == roleService }

// NoteCrewMeeting adds a person's note; the engine reads it into the next round.
func (s *Service) NoteCrewMeeting(ctx context.Context, token, id string, in CrewMeetingNoteInput) (Record, error) {
	text := strings.TrimSpace(in.Text)
	if in.ExpectedRevision < 1 || text == "" || runes(text) > 4000 || in.By != "" && !validRunLabel(in.By, 120) {
		return Record{}, ErrInvalid
	}
	if err := noSecrets(in.Text + " " + in.By); err != nil {
		return Record{}, err
	}
	d, err := s.meetingWriter(ctx, token, in.Scope, id, humanOrService)
	if err != nil {
		return Record{}, err
	}
	by := d.PrincipalID
	if d.Role == roleService {
		by = "discord" // the bot writes the person's name into the text
		if in.By != "" {
			by = in.By
		}
	}
	return s.changeMeeting(ctx, token, in.Scope, id, d, "note", func(r *Record, m *CrewMeeting, now time.Time) error {
		if r.Revision != in.ExpectedRevision || terminalMeeting(m.Status) || m.Status == "stopping" {
			return ErrConflict
		}
		if len(m.NotesPending) >= crewMeetingNotes {
			return ErrConflict
		}
		m.NotesPending = append(slices.Clone(m.NotesPending), MeetingNote{By: by, Text: text, At: now})
		return nil
	})
}

// StopCrewMeeting ends a meeting at a person's word: one not started or
// waiting stops at once; a running one goes to stopping and the engine closes it.
func (s *Service) StopCrewMeeting(ctx context.Context, token, id string, in CrewMeetingStopInput) (Record, error) {
	d, err := s.meetingWriter(ctx, token, in.Scope, id, humanOrService)
	if err != nil {
		return Record{}, err
	}
	return s.changeMeeting(ctx, token, in.Scope, id, d, "stop", func(r *Record, m *CrewMeeting, now time.Time) error {
		switch m.Status {
		case "pending", "waiting":
			m.Status, m.EndedReason, m.Waiting, m.Speaking, m.LeaseUntil = "stopped", "human_stop", nil, "", nil
			r.LeaseToken, r.LeaseUntil = "", time.Time{}
		case "running":
			m.Status = "stopping"
		case "stopping":
		default:
			return ErrConflict
		}
		return nil
	})
}

// DecideCrewMeeting answers a budget wait: go on over the budget, or stop.
func (s *Service) DecideCrewMeeting(ctx context.Context, token, id string, in CrewMeetingDecisionInput) (Record, error) {
	if in.ExpectedRevision < 1 {
		return Record{}, ErrInvalid
	}
	d, err := s.meetingWriter(ctx, token, in.Scope, id, humanOrService)
	if err != nil {
		return Record{}, err
	}
	return s.changeMeeting(ctx, token, in.Scope, id, d, "decide", func(r *Record, m *CrewMeeting, now time.Time) error {
		if r.Revision != in.ExpectedRevision || m.Status != "waiting" || m.Waiting == nil || m.Waiting.Kind != "budget" {
			return ErrConflict
		}
		m.Waiting, m.LeaseUntil = nil, nil
		r.LeaseToken, r.LeaseUntil = "", time.Time{}
		if in.Approve {
			m.Status, m.BudgetApproved = "running", &MeetingApproval{By: d.PrincipalID, At: now}
		} else {
			m.Status, m.EndedReason, m.Speaking = "stopped", "budget", ""
		}
		return nil
	})
}

// sameTurns: the stored turns are an unchanged prefix of the new ones.
func sameTurns(old, next []MeetingTurn) bool {
	if len(next) < len(old) {
		return false
	}
	for i := range old {
		if structuredText(old[i]) != structuredText(next[i]) {
			return false
		}
	}
	return true
}

// keepsNotes: every stored note is still pending or became one new human
// turn with its words, and the engine wrote no human turn of its own.
func keepsNotes(old, next CrewMeeting) bool {
	pending := map[string]int{}
	key := func(by, text string) string { return by + "\x00" + text }
	for _, n := range next.NotesPending {
		pending[structuredText(n)]++
	}
	consumed := map[string]int{}
	for _, n := range old.NotesPending {
		if k := structuredText(n); pending[k] > 0 {
			pending[k]--
			continue
		}
		consumed[key(n.By, n.Text)]++
	}
	for _, left := range pending {
		if left > 0 {
			return false // a note the engine made up
		}
	}
	for _, t := range next.Turns[len(old.Turns):] {
		if t.Kind != "human" {
			continue
		}
		k := key(t.Speaker, t.Text)
		if consumed[k] == 0 {
			return false
		}
		consumed[k]--
	}
	for _, left := range consumed {
		if left > 0 {
			return false // a note that vanished
		}
	}
	return true
}

func engineRole(role string) bool { return role == roleService || role == roleAgent }

// UpdateCrewMeeting is the engine's progress write. It holds a lease: while
// another lease token is live the write is refused, and each write renews it.
func (s *Service) UpdateCrewMeeting(ctx context.Context, token, id string, in CrewMeetingUpdate) (Record, error) {
	if in.ExpectedRevision < 1 || !crewLeaseTokenRe.MatchString(in.LeaseToken) || !validCrewMeeting(in.Meeting) ||
		in.Meeting.Status == "pending" {
		return Record{}, ErrInvalid
	}
	if err := noSecrets(in.Meeting); err != nil {
		return Record{}, err
	}
	d, err := s.meetingWriter(ctx, token, in.Scope, id, engineRole)
	if err != nil {
		return Record{}, err
	}
	return s.changeMeeting(ctx, token, in.Scope, id, d, "update", func(r *Record, m *CrewMeeting, now time.Time) error {
		old := *m
		if r.Revision != in.ExpectedRevision || terminalMeeting(old.Status) {
			return ErrConflict
		}
		if r.LeaseToken != "" && r.LeaseToken != in.LeaseToken && r.LeaseUntil.After(now) {
			return ErrConflict
		}
		next := in.Meeting
		// Linked snapshots are server-owned. The worker wire model is a projection.
		if old.ParentID != "" {
			next.TeamSnapshot = old.TeamSnapshot
		}
		if err := meetingUpdateKeeps(old, next); err != nil {
			return err
		}
		if err := meetingStatusMove(old, next); err != nil {
			return err
		}
		// What only people and the Discord bot write stays as stored.
		next.Discord, next.BudgetApproved = old.Discord, old.BudgetApproved
		next.RootID, next.ParentID, next.ParentNodeID = old.RootID, old.ParentID, old.ParentNodeID
		if terminalMeeting(next.Status) {
			next.LeaseUntil, next.Speaking = nil, ""
			r.LeaseToken, r.LeaseUntil = "", time.Time{}
		} else {
			until := now.Add(crewMeetingLease)
			next.LeaseUntil = &until
			r.LeaseToken, r.LeaseUntil = in.LeaseToken, until
		}
		*m = next
		return nil
	})
}

// meetingUpdateKeeps refuses an engine write that changes what the meeting was
// asked as, or rewrites its turns or notes.
func meetingUpdateKeeps(old, next CrewMeeting) error {
	if next.TeamID != old.TeamID || next.TeamRevision != old.TeamRevision || next.Title != old.Title || next.Topic != old.Topic ||
		next.TemplateKey != old.TemplateKey || next.Trigger != old.Trigger || next.MaxRounds != old.MaxRounds || next.TokenCap != old.TokenCap ||
		next.CostEstimateUSD != old.CostEstimateUSD || old.TeamSnapshot != nil && structuredText(next.TeamSnapshot) != structuredText(old.TeamSnapshot) {
		return ErrInvalid
	}
	if !sameTurns(old.Turns, next.Turns) || !keepsNotes(old, next) {
		return ErrInvalid
	}
	return nil
}

func meetingStatusMove(old, next CrewMeeting) error {
	switch old.Status {
	case "stopping":
		// Only closing remains; a stop is never undone by the engine.
		if next.Status != "stopping" && next.Status != "stopped" && next.Status != "failed" {
			return ErrConflict
		}
	case "waiting":
		// Only the decision endpoint moves a budget wait on.
		if next.Status != "waiting" && next.Status != "failed" {
			return ErrConflict
		}
	default:
		if next.Status == "stopping" || next.Status == "stopped" {
			return ErrConflict
		}
	}
	if next.Status == "waiting" && old.BudgetApproved != nil {
		return ErrInvalid // approved over budget: the engine asks no more
	}
	return nil
}

// UpdateMeetingDiscord records the thread a meeting posts to and how far it
// has posted (the Discord bot, service token). Finished meetings take it too.
func (s *Service) UpdateMeetingDiscord(ctx context.Context, token, id string, in CrewMeetingDiscordInput) (Record, error) {
	if in.ExpectedRevision < 1 || !validMeetingDiscord(&in.Discord) {
		return Record{}, ErrInvalid
	}
	d, err := s.meetingWriter(ctx, token, in.Scope, id, func(role string) bool { return role == roleService })
	if err != nil {
		return Record{}, err
	}
	return s.changeMeeting(ctx, token, in.Scope, id, d, "discord", func(r *Record, m *CrewMeeting, now time.Time) error {
		if r.Revision != in.ExpectedRevision {
			return ErrConflict
		}
		old, next := MeetingDiscord{}, in.Discord
		if m.Discord != nil {
			old = *m.Discord
		}
		fixed := func(a, b string) bool { return a == "" || a == b }
		if !fixed(old.BotID, next.BotID) || !fixed(old.GuildID, next.GuildID) || !fixed(old.ChannelID, next.ChannelID) ||
			!fixed(old.ThreadID, next.ThreadID) || !fixed(old.ThreadURL, next.ThreadURL) || next.LastPostedSeq < old.LastPostedSeq ||
			next.LastPostedSeq > len(m.Turns) {
			return ErrInvalid
		}
		m.Discord = &next
		return nil
	})
}

func crewMeetingRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/crew/meetings", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit := 0
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				respond(w, nil, ErrInvalid)
				return
			}
			limit = n
		}
		out, e := s.ListCrewMeetings(r.Context(), bearer(r), scopeQuery(r), q.Get("status"), q.Get("team_id"), limit, q.Get("brief") == "1")
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/crew/meetings", func(w http.ResponseWriter, r *http.Request) {
		var in CrewMeetingInput
		if !decodeLimit(w, r, &in, 256<<10) {
			return
		}
		out, e := s.CreateCrewMeeting(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/crew/meetings/{id}", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.GetCrewMeeting(r.Context(), bearer(r), scopeQuery(r), r.PathValue("id"))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/crew/meetings/{id}/notes", func(w http.ResponseWriter, r *http.Request) {
		var in CrewMeetingNoteInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.NoteCrewMeeting(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/crew/meetings/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		var in CrewMeetingStopInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.StopCrewMeeting(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/crew/meetings/{id}/decision", func(w http.ResponseWriter, r *http.Request) {
		var in CrewMeetingDecisionInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.DecideCrewMeeting(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/crew/meetings/{id}/progress", func(w http.ResponseWriter, r *http.Request) {
		var in CrewMeetingUpdate
		if !decodeLimit(w, r, &in, crewMeetingMaxBytes+(64<<10)) {
			return
		}
		out, e := s.UpdateCrewMeeting(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/crew/meetings/{id}/discord", func(w http.ResponseWriter, r *http.Request) {
		var in CrewMeetingDiscordInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.UpdateMeetingDiscord(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
}
