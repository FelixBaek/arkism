package memory

import (
	"context"
	"errors"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Discord: bots the owner connects so a team, workflow or agent answers in a
// Discord server. The host process (`arkism discord`) holds the bot tokens in
// the OS keyring and talks to Discord; this store keeps only the bots'
// settings and what the host reports. A token the owner types is held in this
// process's memory until the host collects it once (within providerKeyTTL),
// exactly like provider keys: it never reaches the database or a log.
const (
	colDiscordBots = "discord_bots"
	// The host process's own permission: only it collects tokens and reports.
	actionDiscordHandoff = "handoff:discord"
	discordBotsMax       = 20
)

var discordStates = []string{"waiting_token", "connecting", "connected", "needs_permission", "offline", "token_invalid"}
var discordActionKinds = []string{"meeting", "workflow", "agent"}
var discordReplyTo = []string{"same_thread", "channel"}
var discordBotModes = []string{"shared", "per_target"}
var discordPermRe = regexp.MustCompile(`^[a-z_]{1,40}$`)

type DiscordAction struct {
	Kind        string `json:"kind"` // meeting (team id) | workflow | agent
	ID          string `json:"id"`
	TemplateKey string `json:"template_key,omitempty"`
}

type DiscordChannel struct {
	ChannelID string         `json:"channel_id"`
	Name      string         `json:"name,omitempty"`
	Label     string         `json:"label,omitempty"`
	Action    *DiscordAction `json:"action,omitempty"`
	AutoStart bool           `json:"auto_start,omitempty"`
}

type DiscordMissing struct {
	ChannelID string   `json:"channel_id"`
	Perms     []string `json:"perms"`
}

type DiscordAllowed struct {
	UserIDs []string `json:"user_ids"`
	RoleIDs []string `json:"role_ids"`
}

// DiscordKnownChannel is a channel the host saw in the bot's guild.
type DiscordKnownChannel struct {
	ChannelID string `json:"channel_id"`
	Name      string `json:"name"`
	Parent    string `json:"parent,omitempty"`
}

type DiscordGuild struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type DiscordBot struct {
	Name          string           `json:"name"`
	ApplicationID string           `json:"application_id,omitempty"`
	BotUserID     string           `json:"bot_user_id,omitempty"`
	TokenLast4    string           `json:"token_last4,omitempty"`
	GuildID       string           `json:"guild_id,omitempty"`
	GuildName     string           `json:"guild_name,omitempty"`
	State         string           `json:"state"`
	Missing       []DiscordMissing `json:"missing"`
	Channels      []DiscordChannel `json:"channels"`
	// Target is the one thing a per-target bot runs (팀마다 봇); channels may still override it.
	Target           *DiscordAction        `json:"target,omitempty"`
	Allowed          DiscordAllowed        `json:"allowed"`
	Guilds           []DiscordGuild        `json:"guilds,omitempty"`
	Known            []DiscordKnownChannel `json:"known_channels,omitempty"`
	CheckRequestedAt *time.Time            `json:"check_requested_at,omitempty"`
	LastSeenAt       *time.Time            `json:"last_seen_at,omitempty"`
}

type DiscordSettings struct {
	GuildID   string  `json:"guild_id,omitempty"`
	GuildName string  `json:"guild_name,omitempty"`
	ReplyTo   string  `json:"reply_to"`
	DailyUSD  float64 `json:"daily_usd"`
	BotMode   string  `json:"bot_mode"`
}

// DiscordBotInput is what the desktop sends; Token only travels in.
type DiscordBotInput struct {
	Name     string           `json:"name"`
	Token    string           `json:"token,omitempty"`
	Channels []DiscordChannel `json:"channels,omitempty"`
	Target   *DiscordAction   `json:"target,omitempty"`
	Allowed  *DiscordAllowed  `json:"allowed,omitempty"`
	GuildID  string           `json:"guild_id,omitempty"`
}

type DiscordBotCreate struct {
	Scope Scope           `json:"scope"`
	Bot   DiscordBotInput `json:"bot"`
}

type DiscordBotUpdate struct {
	Scope            Scope            `json:"scope"`
	ExpectedRevision int64            `json:"expected_revision"`
	Bot              *DiscordBotInput `json:"bot,omitempty"`
	Archive          bool             `json:"archive,omitempty"`
}

type DiscordSettingsInput struct {
	Scope            Scope           `json:"scope"`
	ExpectedRevision int64           `json:"expected_revision,omitempty"`
	Settings         DiscordSettings `json:"settings"`
}

type DiscordScopeInput struct {
	Scope Scope `json:"scope"`
}

type DiscordPosted struct {
	MeetingID     string `json:"meeting_id"`
	LastPostedSeq int    `json:"last_posted_seq"`
}

type DiscordReport struct {
	Scope         Scope                 `json:"scope"`
	BotID         string                `json:"bot_id"`
	State         string                `json:"state"`
	ApplicationID string                `json:"application_id,omitempty"`
	BotUserID     string                `json:"bot_user_id,omitempty"`
	TokenLast4    string                `json:"token_last4,omitempty"`
	Guilds        []DiscordGuild        `json:"guilds"`
	Channels      []DiscordKnownChannel `json:"channels"`
	Missing       []DiscordMissing      `json:"missing"`
	Posted        []DiscordPosted       `json:"posted,omitempty"`
}

type DiscordHandoffItem struct {
	Bot   Record `json:"bot"`
	Token string `json:"token,omitempty"`
}

type DiscordHandoffPage struct {
	Items    []DiscordHandoffItem `json:"items"`
	Settings Record               `json:"settings"`
}

type DiscordTarget struct {
	Kind  string  `json:"kind"`
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Calls int     `json:"calls"`
	Cost  float64 `json:"cost_usd"`
}

type DiscordStats struct {
	Bots        int             `json:"bots"`
	OpenThreads int             `json:"open_threads"`
	CallsWeek   int             `json:"calls_week"`
	TodayUSD    float64         `json:"today_usd"`
	ByTarget    []DiscordTarget `json:"by_target"`
}

type DiscordPage struct {
	Bots     []Record     `json:"bots"`
	Settings Record       `json:"settings"`
	Stats    DiscordStats `json:"stats"`
}

type DiscordChannelsPage struct {
	Items []DiscordKnownChannel `json:"items"`
}

func discordSettingsID(scope Scope) string {
	return "discord-settings:" + ContentHash(structuredText(scope))[:16]
}

func defaultDiscordSettings() DiscordSettings {
	return DiscordSettings{ReplyTo: "same_thread", BotMode: "shared"}
}

// validDiscordToken: three dot-separated parts, no space; the shape only, never logged.
func validDiscordToken(t string) bool {
	return len(t) >= 50 && len(t) <= 120 && strings.Count(t, ".") == 2 &&
		strings.IndexFunc(t, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func discordID(s string) bool { return s == "" || discordIDRe.MatchString(s) }

func validDiscordAction(a *DiscordAction) bool {
	return a == nil || slices.Contains(discordActionKinds, a.Kind) && validRunLabel(a.ID, 256) && (a.TemplateKey == "" || crewTemplateKeyRe.MatchString(a.TemplateKey))
}

func validDiscordIDs(ids []string) bool {
	if len(ids) > 100 {
		return false
	}
	for _, id := range ids {
		if !discordIDRe.MatchString(id) {
			return false
		}
	}
	return true
}

func validDiscordBotInput(b DiscordBotInput) bool {
	name := strings.TrimSpace(b.Name)
	if name == "" || runes(name) > 60 || len(b.Channels) > 50 || !validDiscordAction(b.Target) || !discordID(b.GuildID) ||
		b.Allowed != nil && (!validDiscordIDs(b.Allowed.UserIDs) || !validDiscordIDs(b.Allowed.RoleIDs)) {
		return false
	}
	seen := map[string]bool{}
	for _, c := range b.Channels {
		if !discordIDRe.MatchString(c.ChannelID) || seen[c.ChannelID] || runes(c.Name) > 100 || runes(c.Label) > 60 || !validDiscordAction(c.Action) {
			return false
		}
		seen[c.ChannelID] = true
	}
	return true
}

func validDiscordSettings(s DiscordSettings) bool {
	return discordID(s.GuildID) && runes(s.GuildName) <= 100 && slices.Contains(discordReplyTo, s.ReplyTo) &&
		s.DailyUSD >= 0 && s.DailyUSD <= 1e4 && !math.IsNaN(s.DailyUSD) && slices.Contains(discordBotModes, s.BotMode)
}

func validDiscordReport(in DiscordReport) bool {
	if !validScope(in.Scope) || !validRunLabel(in.BotID, 256) || !slices.Contains(discordStates, in.State) || !discordID(in.ApplicationID) ||
		!discordID(in.BotUserID) || len(in.TokenLast4) > 4 || len(in.Guilds) > 50 || len(in.Channels) > 500 || len(in.Missing) > 50 || len(in.Posted) > 200 {
		return false
	}
	for _, g := range in.Guilds {
		if !discordIDRe.MatchString(g.ID) || runes(g.Name) > 100 {
			return false
		}
	}
	for _, c := range in.Channels {
		if !discordIDRe.MatchString(c.ChannelID) || runes(c.Name) > 100 || !discordID(c.Parent) {
			return false
		}
	}
	for _, m := range in.Missing {
		if !discordIDRe.MatchString(m.ChannelID) || len(m.Perms) > 20 {
			return false
		}
		for _, p := range m.Perms {
			if !discordPermRe.MatchString(p) {
				return false
			}
		}
	}
	for _, p := range in.Posted {
		if !validRunLabel(p.MeetingID, 256) || p.LastPostedSeq < 0 {
			return false
		}
	}
	return true
}

func (s *Service) humanDiscord(ctx context.Context, token string, scope Scope, id string) (Decision, error) {
	if !validScope(scope) {
		return Decision{}, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "write:"+colDiscordBots, scope, id, "")
	if err != nil {
		return d, err
	}
	if d.Role != roleHuman {
		return d, ErrDenied
	}
	return d, nil
}

func (s *Service) liveDiscordBots(ctx context.Context, scope Scope) ([]Record, Record, error) {
	rows, err := s.Repo.ListRecords(ctx, scope, colDiscordBots, 1000)
	if err != nil {
		return nil, Record{}, err
	}
	bots := []Record{}
	settings := Record{ID: discordSettingsID(scope), Scope: scope, Kind: "settings", Status: statusActive, DiscordSettings: ptr(defaultDiscordSettings())}
	for _, r := range rows {
		switch {
		case r.DiscordSettings != nil && r.ID == discordSettingsID(scope):
			settings = r
		case r.DiscordBot != nil && r.Status == statusActive:
			bots = append(bots, r)
		}
	}
	return bots, settings, nil
}

func ptr[T any](v T) *T { return &v }

// CreateDiscordBot adds a bot; its token waits in memory for the host.
func (s *Service) CreateDiscordBot(ctx context.Context, token string, in DiscordBotCreate) (Record, error) {
	tok := strings.TrimSpace(in.Bot.Token)
	if !validScope(in.Scope) || !validDiscordBotInput(in.Bot) || !validDiscordToken(tok) {
		return Record{}, ErrInvalid
	}
	bot := discordBotFrom(DiscordBot{State: "waiting_token", Missing: []DiscordMissing{}}, in.Bot)
	bot.TokenLast4 = tok[len(tok)-4:]
	if err := noSecrets(bot); err != nil {
		return Record{}, err
	}
	d, err := s.humanDiscord(ctx, token, in.Scope, "")
	if err != nil {
		return Record{}, err
	}
	bots, _, err := s.liveDiscordBots(ctx, in.Scope)
	if err != nil {
		return Record{}, err
	}
	if len(bots) >= discordBotsMax {
		return Record{}, ErrConflict
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	r := Record{ID: ID(), Scope: in.Scope, Kind: "bot", Status: statusActive, Revision: 1, Text: bot.Name, DiscordBot: &bot,
		CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if err := tx.Insert(ctx, colDiscordBots, r); err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "create:"+colDiscordBots, r.ID)
	})
	if err == nil {
		s.discordTokens.put(r.ID, in.Scope, tok, time.Now())
	}
	return r, err
}

// discordBotFrom lays what the owner edits over a stored bot; what the host reports stays.
func discordBotFrom(b DiscordBot, in DiscordBotInput) DiscordBot {
	b.Name, b.Target = strings.TrimSpace(in.Name), in.Target
	b.Channels = slices.Clone(in.Channels)
	if b.Channels == nil {
		b.Channels = []DiscordChannel{}
	}
	if in.Allowed != nil {
		b.Allowed = *in.Allowed
	}
	if b.Allowed.UserIDs == nil {
		b.Allowed.UserIDs = []string{}
	}
	if b.Allowed.RoleIDs == nil {
		b.Allowed.RoleIDs = []string{}
	}
	if in.GuildID != "" {
		b.GuildID = in.GuildID
	}
	return b
}

// UpdateDiscordBot changes a bot's name, channels, target or allowed people,
// takes a new token (held like a new bot's), or archives it.
func (s *Service) UpdateDiscordBot(ctx context.Context, token, id string, in DiscordBotUpdate) (Record, error) {
	if !validRunLabel(id, 256) || in.ExpectedRevision < 1 || (in.Bot == nil) == !in.Archive || in.Bot != nil && !validDiscordBotInput(*in.Bot) {
		return Record{}, ErrInvalid
	}
	tok := ""
	if in.Bot != nil {
		if tok = strings.TrimSpace(in.Bot.Token); tok != "" && !validDiscordToken(tok) {
			return Record{}, ErrInvalid
		}
	}
	d, err := s.humanDiscord(ctx, token, in.Scope, id)
	if err != nil {
		return Record{}, err
	}
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, colDiscordBots, in.Scope, id)
		if err != nil {
			return err
		}
		if r.DiscordBot == nil {
			return ErrNotFound
		}
		if r.Revision != in.ExpectedRevision || r.Status != statusActive {
			return ErrConflict
		}
		action := "archive:" + colDiscordBots
		if in.Archive {
			r.Status = "archived"
		} else {
			action = "update:" + colDiscordBots
			b := discordBotFrom(*r.DiscordBot, *in.Bot)
			if tok != "" {
				b.TokenLast4, b.State = tok[len(tok)-4:], "waiting_token"
			}
			if err := noSecrets(b); err != nil {
				return err
			}
			r.DiscordBot, r.Text = &b, b.Name
		}
		prev := r.Revision
		r.Revision, r.UpdatedAt = r.Revision+1, time.Now().UTC().Truncate(time.Millisecond)
		if err := tx.Replace(ctx, colDiscordBots, r, prev); err != nil {
			return err
		}
		out = r
		return audit(ctx, tx, in.Scope, d.PrincipalID, action, id)
	})
	if err == nil && tok != "" {
		s.discordTokens.put(id, in.Scope, tok, time.Now())
	}
	return out, err
}

func (s *Service) discordBot(ctx context.Context, token string, scope Scope, id string) (Record, error) {
	if !validScope(scope) || !validRunLabel(id, 256) {
		return Record{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colDiscordBots, scope, id, ""); err != nil {
		return Record{}, err
	}
	r, err := s.Repo.Get(ctx, colDiscordBots, scope, id)
	if err != nil {
		return Record{}, err
	}
	if r.DiscordBot == nil || r.Status != statusActive {
		return Record{}, ErrNotFound
	}
	return r, nil
}

// DiscordBotChannels lists the channels the host last saw for a bot.
func (s *Service) DiscordBotChannels(ctx context.Context, token string, scope Scope, id string) (DiscordChannelsPage, error) {
	r, err := s.discordBot(ctx, token, scope, id)
	if err != nil {
		return DiscordChannelsPage{}, err
	}
	out := DiscordChannelsPage{Items: slices.Clone(r.DiscordBot.Known)}
	if out.Items == nil {
		out.Items = []DiscordKnownChannel{}
	}
	return out, nil
}

// CheckDiscordBot asks the host to check the bot's permissions now.
func (s *Service) CheckDiscordBot(ctx context.Context, token, id string, in DiscordScopeInput) (Record, error) {
	if !validRunLabel(id, 256) {
		return Record{}, ErrInvalid
	}
	d, err := s.humanDiscord(ctx, token, in.Scope, id)
	if err != nil {
		return Record{}, err
	}
	return s.changeBot(ctx, in.Scope, id, d.PrincipalID, "check", func(b *DiscordBot, now time.Time) { b.CheckRequestedAt = &now })
}

func (s *Service) changeBot(ctx context.Context, scope Scope, id, actor, action string, change func(*DiscordBot, time.Time)) (Record, error) {
	var out Record
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, colDiscordBots, scope, id)
		if err != nil {
			return err
		}
		if r.DiscordBot == nil || r.Status != statusActive {
			return ErrNotFound
		}
		b := *r.DiscordBot
		now := time.Now().UTC().Truncate(time.Millisecond)
		change(&b, now)
		if err := noSecrets(b); err != nil {
			return err
		}
		prev := r.Revision
		r.DiscordBot, r.Revision, r.UpdatedAt = &b, r.Revision+1, now
		if err := tx.Replace(ctx, colDiscordBots, r, prev); err != nil {
			return err
		}
		out = r
		if actor == "" {
			return nil
		}
		return audit(ctx, tx, scope, actor, action+":"+colDiscordBots, id)
	})
	return out, err
}

// SetDiscordSettings saves the workspace's Discord settings (one guild, reply place, daily budget, bot mode).
func (s *Service) SetDiscordSettings(ctx context.Context, token string, in DiscordSettingsInput) (Record, error) {
	if !validScope(in.Scope) || in.ExpectedRevision < 0 || !validDiscordSettings(in.Settings) {
		return Record{}, ErrInvalid
	}
	if err := noSecrets(in.Settings); err != nil {
		return Record{}, err
	}
	id := discordSettingsID(in.Scope)
	d, err := s.humanDiscord(ctx, token, in.Scope, id)
	if err != nil {
		return Record{}, err
	}
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		now := time.Now().UTC().Truncate(time.Millisecond)
		settings := in.Settings
		r, err := tx.Get(ctx, colDiscordBots, in.Scope, id)
		if errors.Is(err, ErrNotFound) {
			if in.ExpectedRevision > 0 {
				return ErrConflict
			}
			r = Record{ID: id, Scope: in.Scope, Kind: "settings", Status: statusActive, Revision: 1, Text: "settings", DiscordSettings: &settings,
				CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role}
			if err := tx.Insert(ctx, colDiscordBots, r); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			if in.ExpectedRevision != r.Revision {
				return ErrConflict
			}
			prev := r.Revision
			r.DiscordSettings, r.Revision, r.UpdatedAt = &settings, r.Revision+1, now
			if err := tx.Replace(ctx, colDiscordBots, r, prev); err != nil {
				return err
			}
		}
		out = r
		return audit(ctx, tx, in.Scope, d.PrincipalID, "settings:"+colDiscordBots, id)
	})
	return out, err
}

// Discord is the desktop's Discord screen: bots, settings and a week of use.
func (s *Service) Discord(ctx context.Context, token string, scope Scope) (DiscordPage, error) {
	if !validScope(scope) {
		return DiscordPage{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colDiscordBots, scope, "", ""); err != nil {
		return DiscordPage{}, err
	}
	bots, settings, err := s.liveDiscordBots(ctx, scope)
	if err != nil {
		return DiscordPage{}, err
	}
	stats, err := s.discordStats(ctx, scope)
	if err != nil {
		return DiscordPage{}, err
	}
	stats.Bots = len(bots)
	return DiscordPage{Bots: bots, Settings: settings, Stats: stats}, nil
}

func (s *Service) discordStats(ctx context.Context, scope Scope) (DiscordStats, error) {
	out := DiscordStats{ByTarget: []DiscordTarget{}}
	crew, err := s.liveCrew(ctx, scope)
	if err != nil {
		return out, err
	}
	meetings, err := s.Repo.ListRecords(ctx, scope, colCrewMeetings, 1000)
	if err != nil {
		return out, err
	}
	for _, r := range meetings {
		if m := r.CrewMeeting; m != nil && m.Discord != nil && m.Discord.ThreadID != "" && !terminalMeeting(m.Status) {
			out.OpenThreads++
		}
	}
	runs, err := s.Repo.ListRecords(ctx, scope, colCrewRuns, 1000)
	if err != nil {
		return out, err
	}
	for _, r := range runs {
		if run := r.CrewRun; run != nil && run.Trigger == "discord" && !terminalCrewRun(run.Status) {
			out.OpenThreads++
		}
	}
	calls, err := s.Repo.ListRecords(ctx, scope, colRoutingCalls, routingCallsRead)
	if err != nil {
		return out, err
	}
	now := time.Now()
	day := startOfDay(now)
	targets := map[string]*DiscordTarget{}
	for _, r := range calls {
		c := r.RoutingCall
		if c == nil || c.Origin == nil || c.Origin.Trigger != "discord" || now.Sub(r.CreatedAt) > 7*24*time.Hour {
			continue
		}
		cost, _ := callCost(*c)
		out.CallsWeek++
		if !r.CreatedAt.Before(day) {
			out.TodayUSD += cost
		}
		kind, id := "workflow", c.Origin.WorkflowID
		switch {
		case c.Origin.TeamID != "":
			kind, id = "meeting", c.Origin.TeamID
		case c.Origin.AgentID != "" && c.Origin.Kind != "meeting":
			kind, id = "agent", c.Origin.AgentID
		}
		if id == "" {
			continue
		}
		t := targets[kind+":"+id]
		if t == nil {
			name := id
			if it, ok := crew[id]; ok {
				name = it.Crew.Name
			}
			t = &DiscordTarget{Kind: kind, ID: id, Name: name}
			targets[kind+":"+id] = t
		}
		t.Calls++
		t.Cost = roundUSD(t.Cost + cost)
	}
	for _, t := range targets {
		out.ByTarget = append(out.ByTarget, *t)
	}
	slices.SortFunc(out.ByTarget, func(a, b DiscordTarget) int { return b.Calls - a.Calls })
	out.TodayUSD = roundUSD(out.TodayUSD)
	return out, nil
}

// DiscordHandoff gives the host every live bot, each token once, and the settings.
func (s *Service) DiscordHandoff(ctx context.Context, token string, scope Scope) (DiscordHandoffPage, error) {
	if !validScope(scope) {
		return DiscordHandoffPage{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, actionDiscordHandoff, scope, "", ""); err != nil {
		return DiscordHandoffPage{}, err
	}
	bots, settings, err := s.liveDiscordBots(ctx, scope)
	if err != nil {
		return DiscordHandoffPage{}, err
	}
	out := DiscordHandoffPage{Items: []DiscordHandoffItem{}, Settings: settings}
	now := time.Now()
	for _, b := range bots {
		item := DiscordHandoffItem{Bot: b}
		if tok, ok := s.discordTokens.take(b.ID, scope, now); ok {
			item.Token = tok
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// ReportDiscord records what the host sees of one bot and how far it mirrored meetings.
func (s *Service) ReportDiscord(ctx context.Context, token string, in DiscordReport) (Record, error) {
	if !validDiscordReport(in) {
		return Record{}, ErrInvalid
	}
	if err := noSecrets(in); err != nil {
		return Record{}, err
	}
	d, err := s.authorize(ctx, token, actionDiscordHandoff, in.Scope, in.BotID, "")
	if err != nil {
		return Record{}, err
	}
	out, err := s.changeBot(ctx, in.Scope, in.BotID, "", "report", func(b *DiscordBot, now time.Time) {
		b.State, b.LastSeenAt = in.State, &now
		if in.ApplicationID != "" {
			b.ApplicationID = in.ApplicationID
		}
		if in.BotUserID != "" {
			b.BotUserID = in.BotUserID
		}
		if len(in.TokenLast4) == 4 {
			b.TokenLast4 = in.TokenLast4
		}
		if in.State == "token_invalid" {
			return
		}
		b.Guilds, b.Known, b.Missing = slices.Clone(in.Guilds), slices.Clone(in.Channels), slices.Clone(in.Missing)
		if b.Missing == nil {
			b.Missing = []DiscordMissing{}
		}
		for _, g := range in.Guilds {
			if b.GuildID == "" || g.ID == b.GuildID {
				b.GuildID, b.GuildName = g.ID, g.Name
				break
			}
		}
	})
	if err != nil {
		return Record{}, err
	}
	for _, p := range in.Posted {
		s.markPosted(ctx, in.Scope, d.PrincipalID, in.BotID, p)
	}
	return out, nil
}

// markPosted moves a meeting's last_posted_seq forward (best effort: a
// meeting of another bot, or a seq past its turns, is left as it is).
func (s *Service) markPosted(ctx context.Context, scope Scope, actor, botID string, p DiscordPosted) {
	_, _ = s.changeMeeting(ctx, scope, p.MeetingID, Decision{PrincipalID: actor}, "posted", func(r *Record, m *CrewMeeting, now time.Time) error {
		if m.Discord == nil || m.Discord.BotID != botID || p.LastPostedSeq > len(m.Turns) {
			return ErrInvalid
		}
		if p.LastPostedSeq <= m.Discord.LastPostedSeq {
			return ErrConflict
		}
		dc := *m.Discord
		dc.LastPostedSeq = p.LastPostedSeq
		m.Discord = &dc
		return nil
	})
}

func discordRoutes(mux *http.ServeMux, s *Service) {
	mux.HandleFunc("GET /v1/discord", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.Discord(r.Context(), bearer(r), scopeQuery(r))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/discord/bots", func(w http.ResponseWriter, r *http.Request) {
		var in DiscordBotCreate
		if !decode(w, r, &in) {
			return
		}
		out, e := s.CreateDiscordBot(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	// Not "POST /v1/discord/bots/{id}": that would collide with the generic POST /v1/{collection}/{id}/transition.
	mux.HandleFunc("POST /v1/discord/bots/{id}/update", func(w http.ResponseWriter, r *http.Request) {
		var in DiscordBotUpdate
		if !decode(w, r, &in) {
			return
		}
		out, e := s.UpdateDiscordBot(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/discord/bots/{id}/channels", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.DiscordBotChannels(r.Context(), bearer(r), scopeQuery(r), r.PathValue("id"))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/discord/bots/{id}/check", func(w http.ResponseWriter, r *http.Request) {
		var in DiscordScopeInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.CheckDiscordBot(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/discord/settings", func(w http.ResponseWriter, r *http.Request) {
		var in DiscordSettingsInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.SetDiscordSettings(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/discord/handoff", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.DiscordHandoff(r.Context(), bearer(r), scopeQuery(r))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/discord/report", func(w http.ResponseWriter, r *http.Request) {
		var in DiscordReport
		if !decodeLimit(w, r, &in, 1<<20) {
			return
		}
		out, e := s.ReportDiscord(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}
