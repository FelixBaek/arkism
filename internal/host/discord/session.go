package discord

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Session is the small slice of a Discord bot connection the host uses. The real one wraps discordgo; tests use a
// fake. Every method is safe to call from event handlers.
type Session interface {
	// Open checks the token and connects to the gateway; handlers start firing after it returns.
	Open(h Handlers) error
	Close() error
	// Self is the bot's application id and user id (known after Open).
	Self() (appID, userID string)
	Guilds() []Guild
	Channels(guildID string) ([]Channel, error)
	// Permissions is the bot's effective permission bits in a channel.
	Permissions(channelID string) (int64, error)
	// Channel looks one channel (or thread) up.
	Channel(channelID string) (Channel, error)
	// MemberRoles fetches current guild membership for delivery authorization.
	MemberRoles(guildID, userID string) ([]string, error)
	Send(channelID string, msg Out) (messageID string, err error)
	// StartThread opens a public thread on messageID (or a bare thread when messageID is "").
	StartThread(channelID, messageID, name string) (threadID string, err error)
	RegisterCommands(guildID string) error
	// Respond answers an interaction (a slash command or a button press).
	Respond(interactionID string, content string, private bool) error
}

// Handlers receive gateway events, already reduced to what the host needs.
type Handlers struct {
	Message func(Message)
	Command func(Command)
	Button  func(Press)
}

// Guild is one server the bot is in.
type Guild struct{ ID, Name, OwnerID string }

// Channel is a text channel or a thread.
type Channel struct {
	ID, GuildID, Name, ParentID string
	Thread                      bool
}

// Out is one message to post; Buttons render as one row.
type Out struct {
	Content string
	Buttons []Button
	// Nonce is a stable delivery-part identity. Discord only deduplicates it
	// for a few minutes; persisted ambiguous sends must not rely on it forever.
	Nonce string
	// Deadline prevents a rate-limit wait from posting after the lease expires.
	Deadline time.Time
}

// Button is a clickable button; ID comes back in a Press.
type Button struct {
	ID, Label string
	Danger    bool
}

// Message is a new channel or thread message.
type Message struct {
	ID, ChannelID, GuildID string
	AuthorID               string
	AuthorName             string
	AuthorBot              bool
	RoleIDs                []string
	Content                string
}

// Command is a slash command; Sub is the subcommand name ("멈춤") when there is one.
type Command struct {
	InteractionID      string
	Name, Sub          string
	Options            map[string]string
	ChannelID, GuildID string
	UserID, UserName   string
	RoleIDs            []string
}

// Press is a button press.
type Press struct {
	InteractionID      string
	CustomID           string
	ChannelID, GuildID string
	UserID, UserName   string
	RoleIDs            []string
}

// Permission bits the bot needs, with the names reported as missing.
var neededPerms = []struct {
	bit  int64
	name string
}{
	{discordgo.PermissionViewChannel, "view_channel"},
	{discordgo.PermissionReadMessageHistory, "read_message_history"},
	{discordgo.PermissionSendMessages, "send_messages"},
	{discordgo.PermissionSendMessagesInThreads, "send_messages_in_threads"},
	{discordgo.PermissionCreatePublicThreads, "create_public_threads"},
	{discordgo.PermissionUseApplicationCommands, "use_application_commands"},
}

const permAdministrator = discordgo.PermissionAdministrator

// invitePerms is the permission integer the invite link asks for (never Administrator).
func invitePerms() int64 {
	var p int64
	for _, n := range neededPerms {
		p |= n.bit
	}
	return p
}

// ErrTokenInvalid means Discord refused the bot token.
var ErrTokenInvalid = errors.New("discord: token refused")

// commands are the guild slash commands: /물어보기 이름: 질문: and /회의 멈춤.
func commands() []*discordgo.ApplicationCommand {
	en := func(s string) *map[discordgo.Locale]string {
		m := map[discordgo.Locale]string{discordgo.EnglishUS: s}
		return &m
	}
	return []*discordgo.ApplicationCommand{
		{Name: cmdAsk, NameLocalizations: en("ask"), Description: "워크플로·전문가·팀에 작업을 요청해요",
			Options: []*discordgo.ApplicationCommandOption{
				{Type: discordgo.ApplicationCommandOptionString, Name: optName, Description: "워크플로·전문가·팀 이름 (@ 없이)", Required: true},
				{Type: discordgo.ApplicationCommandOptionString, Name: optQuestion, Description: "물어볼 내용", Required: true},
			}},
		{Name: cmdMeeting, NameLocalizations: en("meeting"), Description: "이 스레드의 팀 회의",
			Options: []*discordgo.ApplicationCommandOption{
				{Type: discordgo.ApplicationCommandOptionSubCommand, Name: subStop, Description: "회의를 멈춰요"},
			}},
	}
}

const (
	cmdAsk      = "물어보기"
	cmdMeeting  = "회의"
	subStop     = "멈춤"
	optName     = "이름"
	optQuestion = "질문"
)

// dgSession is the real Session over discordgo.
type dgSession struct {
	s     *discordgo.Session
	mu    sync.Mutex
	app   string
	user  string
	inter map[string]*discordgo.Interaction // pending interactions by id (answered once)
}

func newDiscordgo(token string) (Session, error) {
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}
	s.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentMessageContent
	s.StateEnabled = true
	s.LogLevel = discordgo.LogError
	return &dgSession{s: s, inter: map[string]*discordgo.Interaction{}}, nil
}

func restCode(err error) int {
	var rate *discordgo.RateLimitError
	if errors.As(err, &rate) {
		return 429
	}
	var re *discordgo.RESTError
	if errors.As(err, &re) && re.Response != nil {
		return re.Response.StatusCode
	}
	return 0
}

func (d *dgSession) Open(h Handlers) error {
	me, err := d.s.User("@me")
	if err != nil {
		if restCode(err) == 401 {
			return ErrTokenInvalid
		}
		return err
	}
	d.mu.Lock()
	d.user = me.ID
	d.mu.Unlock()
	if app, err := d.s.Application("@me"); err == nil {
		d.mu.Lock()
		d.app = app.ID
		d.mu.Unlock()
	}
	d.s.AddHandler(func(_ *discordgo.Session, m *discordgo.MessageCreate) {
		if h.Message == nil || m.Author == nil {
			return
		}
		msg := Message{ID: m.ID, ChannelID: m.ChannelID, GuildID: m.GuildID, AuthorID: m.Author.ID, AuthorName: m.Author.Username,
			AuthorBot: m.Author.Bot || m.WebhookID != "", Content: m.Content}
		if m.Member != nil {
			msg.RoleIDs = m.Member.Roles
			if m.Member.Nick != "" {
				msg.AuthorName = m.Member.Nick
			}
		}
		h.Message(msg)
	})
	d.s.AddHandler(func(_ *discordgo.Session, ic *discordgo.InteractionCreate) {
		it := ic.Interaction
		var uid, uname string
		var roles []string
		if it.Member != nil && it.Member.User != nil {
			uid, uname, roles = it.Member.User.ID, it.Member.User.Username, it.Member.Roles
		} else if it.User != nil {
			uid, uname = it.User.ID, it.User.Username
		}
		d.mu.Lock()
		d.inter[it.ID] = it
		d.mu.Unlock()
		switch it.Type {
		case discordgo.InteractionApplicationCommand:
			if h.Command == nil {
				return
			}
			data := it.ApplicationCommandData()
			c := Command{InteractionID: it.ID, Name: data.Name, Options: map[string]string{}, ChannelID: it.ChannelID, GuildID: it.GuildID,
				UserID: uid, UserName: uname, RoleIDs: roles}
			opts := data.Options
			if len(opts) == 1 && opts[0].Type == discordgo.ApplicationCommandOptionSubCommand {
				c.Sub, opts = opts[0].Name, opts[0].Options
			}
			for _, o := range opts {
				if o.Type == discordgo.ApplicationCommandOptionString {
					c.Options[o.Name] = o.StringValue()
				}
			}
			h.Command(c)
		case discordgo.InteractionMessageComponent:
			if h.Button == nil {
				return
			}
			h.Button(Press{InteractionID: it.ID, CustomID: it.MessageComponentData().CustomID, ChannelID: it.ChannelID, GuildID: it.GuildID,
				UserID: uid, UserName: uname, RoleIDs: roles})
		}
	})
	if err := d.s.Open(); err != nil {
		if strings.Contains(err.Error(), "4004") {
			return ErrTokenInvalid
		}
		return err
	}
	return nil
}

func (d *dgSession) Close() error { return d.s.Close() }

func (d *dgSession) Self() (string, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.app, d.user
}

func (d *dgSession) Guilds() []Guild {
	out := []Guild{}
	if d.s.State == nil {
		return out
	}
	d.s.State.RLock()
	defer d.s.State.RUnlock()
	for _, g := range d.s.State.Guilds {
		out = append(out, Guild{ID: g.ID, Name: g.Name, OwnerID: g.OwnerID})
	}
	return out
}

func isText(t discordgo.ChannelType) bool {
	return t == discordgo.ChannelTypeGuildText || t == discordgo.ChannelTypeGuildNews
}

func isThread(t discordgo.ChannelType) bool {
	return t == discordgo.ChannelTypeGuildPublicThread || t == discordgo.ChannelTypeGuildPrivateThread || t == discordgo.ChannelTypeGuildNewsThread
}

func (d *dgSession) Channels(guildID string) ([]Channel, error) {
	list, err := d.s.GuildChannels(guildID)
	if err != nil {
		return nil, err
	}
	out := []Channel{}
	for _, c := range list {
		if isText(c.Type) {
			out = append(out, Channel{ID: c.ID, GuildID: c.GuildID, Name: c.Name, ParentID: c.ParentID})
		}
	}
	return out, nil
}

func (d *dgSession) Permissions(channelID string) (int64, error) {
	_, user := d.Self()
	if p, err := d.s.State.UserChannelPermissions(user, channelID); err == nil {
		return p, nil
	}
	return d.s.UserChannelPermissions(user, channelID)
}

func (d *dgSession) Channel(channelID string) (Channel, error) {
	c, err := d.s.State.Channel(channelID)
	if err != nil {
		if c, err = d.s.Channel(channelID); err != nil {
			return Channel{}, err
		}
	}
	return Channel{ID: c.ID, GuildID: c.GuildID, Name: c.Name, ParentID: c.ParentID, Thread: isThread(c.Type)}, nil
}

func (d *dgSession) MemberRoles(guildID, userID string) ([]string, error) {
	member, err := d.s.GuildMember(guildID, userID)
	if err != nil {
		return nil, err
	}
	return member.Roles, nil
}

func components(buttons []Button) []discordgo.MessageComponent {
	if len(buttons) == 0 {
		return nil
	}
	row := discordgo.ActionsRow{}
	for _, b := range buttons {
		style := discordgo.PrimaryButton
		if b.Danger {
			style = discordgo.DangerButton
		}
		row.Components = append(row.Components, discordgo.Button{Label: b.Label, Style: style, CustomID: b.ID})
	}
	return []discordgo.MessageComponent{row}
}

func (d *dgSession) Send(channelID string, msg Out) (string, error) {
	if msg.Nonce != "" {
		if len(msg.Nonce) > 25 {
			return "", errors.New("discord: delivery nonce too long")
		}
		endpoint := discordgo.EndpointChannelMessages(channelID)
		ctx := context.Background()
		cancel := func() {}
		if !msg.Deadline.IsZero() {
			ctx, cancel = context.WithDeadline(ctx, msg.Deadline)
		}
		defer cancel()
		raw, err := d.s.RequestWithBucketID(http.MethodPost, endpoint, map[string]any{
			"content": msg.Content, "components": components(msg.Buttons),
			"allowed_mentions": &discordgo.MessageAllowedMentions{},
			"nonce":            msg.Nonce, "enforce_nonce": true,
		}, endpoint, discordgo.WithRestRetries(0), discordgo.WithRetryOnRatelimit(false), discordgo.WithContext(ctx))
		if err != nil {
			return "", err
		}
		var reply struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &reply) != nil || reply.ID == "" {
			return "", errors.New("discord: delivery receipt missing")
		}
		return reply.ID, nil
	}
	m, err := d.s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{Content: msg.Content, Components: components(msg.Buttons),
		AllowedMentions: &discordgo.MessageAllowedMentions{}}) // never ping anyone
	if err != nil {
		return "", err
	}
	return m.ID, nil
}

func (d *dgSession) StartThread(channelID, messageID, name string) (string, error) {
	var c *discordgo.Channel
	var err error
	if messageID != "" {
		c, err = d.s.MessageThreadStart(channelID, messageID, name, 1440)
	} else {
		c, err = d.s.ThreadStart(channelID, name, discordgo.ChannelTypeGuildPublicThread, 1440)
	}
	if err != nil {
		// Discord uses the source message ID as its thread ID. A repeated
		// gateway event can safely recover the already-created reply thread.
		var rest *discordgo.RESTError
		if messageID != "" && errors.As(err, &rest) && rest.Message != nil && rest.Message.Code == 160004 {
			existing, getErr := d.s.Channel(messageID)
			if getErr == nil && isThread(existing.Type) && existing.ParentID == channelID {
				return existing.ID, nil
			}
		}
		return "", err
	}
	return c.ID, nil
}

func (d *dgSession) RegisterCommands(guildID string) error {
	app, _ := d.Self()
	if app == "" {
		return errors.New("discord: application id unknown")
	}
	_, err := d.s.ApplicationCommandBulkOverwrite(app, guildID, commands())
	return err
}

func (d *dgSession) Respond(interactionID, content string, private bool) error {
	d.mu.Lock()
	it := d.inter[interactionID]
	delete(d.inter, interactionID)
	d.mu.Unlock()
	if it == nil {
		return errors.New("discord: interaction already answered")
	}
	data := &discordgo.InteractionResponseData{Content: content, AllowedMentions: &discordgo.MessageAllowedMentions{}}
	if private {
		data.Flags = discordgo.MessageFlagsEphemeral
	}
	return d.s.InteractionRespond(it, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseChannelMessageWithSource, Data: data})
}
