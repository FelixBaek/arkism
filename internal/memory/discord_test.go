package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// A made-up token of Discord's shape (three dot-separated parts).
// Split so secret scanners do not read the source as a real token.
const fakeBotToken = "MTIzNDU2Nzg5MDEyMzQ1Njc4OQ" + "." + "GaBcDe" + "." + "abcdefghijklmnopqrstuvwxyz0123456789ABCD"

func TestDiscordBotTokenHandoffOnceAndNeverStored(t *testing.T) {
	f := newMeetingFixture(t)
	in := DiscordBotCreate{Scope: scopeA, Bot: DiscordBotInput{Name: "회의봇", Token: fakeBotToken,
		Channels: []DiscordChannel{{ChannelID: "234567", Label: "리뷰", Action: &DiscordAction{Kind: "meeting", ID: f.team.ID}, AutoStart: true}}}}
	if _, err := f.svc.CreateDiscordBot(testCtx, f.agent, in); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent added a bot", err)
	}
	bad := in
	bad.Bot.Token = "short"
	if _, err := f.svc.CreateDiscordBot(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad token shape", err)
	}
	bot, err := f.svc.CreateDiscordBot(testCtx, f.human, in)
	if err != nil || bot.DiscordBot.State != "waiting_token" || bot.DiscordBot.TokenLast4 != "ABCD" {
		t.Fatal("create", err)
	}
	for _, col := range []string{colDiscordBots, "audit_events"} {
		raw, _ := json.Marshal(f.repo.rows(col))
		if strings.Contains(string(raw), fakeBotToken) || strings.Contains(fmt.Sprintf("%+v", f.repo.rows(col)), fakeBotToken) {
			t.Fatal("token stored in", col)
		}
	}
	if _, err := f.svc.DiscordHandoff(testCtx, f.human, scopeA); !errors.Is(err, ErrDenied) {
		t.Fatal("a person without handoff took tokens", err)
	}
	page, err := f.svc.DiscordHandoff(testCtx, tokHost, scopeA)
	if err != nil || len(page.Items) != 1 || page.Items[0].Token != fakeBotToken || page.Items[0].Bot.ID != bot.ID || page.Settings.DiscordSettings.BotMode != "shared" {
		t.Fatal("handoff", err)
	}
	if page, _ = f.svc.DiscordHandoff(testCtx, tokHost, scopeA); len(page.Items) != 1 || page.Items[0].Token != "" {
		t.Fatal("token handed out twice")
	}
	if page, _ = f.svc.DiscordHandoff(testCtx, tokHost, scopeB); len(page.Items) != 0 {
		t.Fatal("another scope saw the bot")
	}
	// a new token on update is held again
	up, err := f.svc.UpdateDiscordBot(testCtx, f.human, bot.ID, DiscordBotUpdate{Scope: scopeA, ExpectedRevision: 1,
		Bot: &DiscordBotInput{Name: "회의봇2", Token: strings.Replace(fakeBotToken, "ABCD", "WXYZ", 1), Channels: in.Bot.Channels,
			Allowed: &DiscordAllowed{UserIDs: []string{"111111"}}}})
	if err != nil || up.DiscordBot.TokenLast4 != "WXYZ" || up.DiscordBot.Name != "회의봇2" || up.DiscordBot.Allowed.UserIDs[0] != "111111" {
		t.Fatal("update", err)
	}
	if page, _ = f.svc.DiscordHandoff(testCtx, tokHost, scopeA); !strings.HasSuffix(page.Items[0].Token, "WXYZ") {
		t.Fatal("new token not handed out")
	}
	if _, err := f.svc.UpdateDiscordBot(testCtx, f.human, bot.ID, DiscordBotUpdate{Scope: scopeA, ExpectedRevision: 1, Archive: true}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision", err)
	}
}

func TestDiscordReportSettingsAndPosted(t *testing.T) {
	f := newMeetingFixture(t)
	bot, _ := f.svc.CreateDiscordBot(testCtx, f.human, DiscordBotCreate{Scope: scopeA, Bot: DiscordBotInput{Name: "봇", Token: fakeBotToken}})
	m, _ := f.svc.CreateCrewMeeting(testCtx, tokHost, CrewMeetingInput{Scope: scopeA, Meeting: CrewMeeting{TeamID: f.team.ID, Title: "디코", Topic: "z",
		Trigger: "discord", Discord: &MeetingDiscord{BotID: bot.ID, ThreadID: "345678"}}})
	m, _ = f.progress(m, leaseA, func(x *CrewMeeting) { x.Status, x.Turns = "running", []MeetingTurn{turn(1, 0, "brief", "요약")} })
	rep := DiscordReport{Scope: scopeA, BotID: bot.ID, State: "needs_permission", ApplicationID: "999999", BotUserID: "888888", TokenLast4: "ABCD",
		Guilds: []DiscordGuild{{ID: "123456", Name: "우리 서버"}}, Channels: []DiscordKnownChannel{{ChannelID: "234567", Name: "리뷰", Parent: "222222"}},
		Missing: []DiscordMissing{{ChannelID: "234567", Perms: []string{"send_messages_in_threads"}}}, Posted: []DiscordPosted{{MeetingID: m.ID, LastPostedSeq: 1}}}
	if _, err := f.svc.ReportDiscord(testCtx, f.human, rep); !errors.Is(err, ErrDenied) {
		t.Fatal("a person reported", err)
	}
	out, err := f.svc.ReportDiscord(testCtx, tokHost, rep)
	if err != nil || out.DiscordBot.State != "needs_permission" || out.DiscordBot.GuildID != "123456" || len(out.DiscordBot.Missing) != 1 || out.DiscordBot.LastSeenAt == nil {
		t.Fatal("report", err)
	}
	if got, _ := f.svc.GetCrewMeeting(testCtx, f.human, scopeA, m.ID); got.CrewMeeting.Discord.LastPostedSeq != 1 {
		t.Fatal("posted not recorded", got.CrewMeeting.Discord)
	}
	ch, err := f.svc.DiscordBotChannels(testCtx, f.human, scopeA, bot.ID)
	if err != nil || len(ch.Items) != 1 || ch.Items[0].Parent != "222222" {
		t.Fatal("channels", ch, err)
	}
	if c, err := f.svc.CheckDiscordBot(testCtx, f.human, bot.ID, DiscordScopeInput{Scope: scopeA}); err != nil || c.DiscordBot.CheckRequestedAt == nil {
		t.Fatal("check", err)
	}
	leak := rep
	leak.Channels = []DiscordKnownChannel{{ChannelID: "234567", Name: "Bot " + fakeBotToken}}
	if _, err := f.svc.ReportDiscord(testCtx, tokHost, leak); err == nil {
		t.Fatal("a report carrying a token-sized name was taken")
	}
	s, err := f.svc.SetDiscordSettings(testCtx, f.human, DiscordSettingsInput{Scope: scopeA, Settings: DiscordSettings{GuildID: "123456", ReplyTo: "same_thread", DailyUSD: 2, BotMode: "per_target"}})
	if err != nil || s.Revision != 1 {
		t.Fatal("settings", err)
	}
	if _, err := f.svc.SetDiscordSettings(testCtx, f.human, DiscordSettingsInput{Scope: scopeA, Settings: DiscordSettings{ReplyTo: "channel", BotMode: "shared"}}); !errors.Is(err, ErrConflict) {
		t.Fatal("settings without expected_revision overwrote", err)
	}
	page, err := f.svc.Discord(testCtx, f.human, scopeA)
	if err != nil || len(page.Bots) != 1 || page.Settings.DiscordSettings.DailyUSD != 2 || page.Stats.Bots != 1 || page.Stats.OpenThreads != 1 {
		t.Fatal("discord page", page.Stats, err)
	}
	h := Handler(f.svc)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/v1/discord?workspace_id=ws-a&project_id=proj-a", ""},
		{"POST", "/v1/discord/bots/" + bot.ID + "/update", `{"scope":{"workspace_id":"ws-a","project_id":"proj-a"},"expected_revision":` + fmt.Sprint(out.Revision+1) + `,"archive":true}`},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		req.Header.Set("Authorization", "Bearer "+f.human)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatal(c.path, w.Code, w.Body.String())
		}
	}
	if page, _ = f.svc.Discord(testCtx, f.human, scopeA); len(page.Bots) != 0 {
		t.Fatal("archived bot listed")
	}
}
