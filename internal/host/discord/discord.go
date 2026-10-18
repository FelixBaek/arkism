// Package discord is `arkism discord`: the host process that connects the workspace's Discord bots.
//
// It collects bot tokens once from memoryd (GET /v1/discord/handoff, perm handoff:discord) and keeps them in the
// OS credential store (service "arkism-discord"; a 0600 file on a host without one), connects each bot to the
// Discord gateway, reports state, guilds, channels and missing permissions (POST /v1/discord/report) every five
// minutes and whenever a check is requested, and turns channel posts into work:
//
//   - a post in an auto-start channel opens a thread and starts the channel's target (team meeting, workflow run
//     or one agent answer), trigger "discord";
//   - "@이름 질문" as plain text, or /물어보기, gets one read-only answer from that work agent (a team holds a
//     meeting instead; an agent that works by a workflow runs that workflow);
//   - meeting turns are mirrored into the thread (last_posted_seq), replies there become notes for the next round,
//     and /회의 멈춤 stops the meeting;
//   - when the daily budget is reached the bot asks in the thread (계속 / 멈춤) instead of starting or going on.
//
// Only allowed users (default: the guild owner) are heard; other bots are ignored; the bot never asks for
// Administrator; everything it starts is read-only. Channel text reaches models only as fenced data.
package discord

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/FelixBaek/arkism/internal/host"
)

func init() { host.Register("discord", Main) }

const usage = `usage:
  arkism discord [run]        connect every bot and keep working (the service)
  arkism discord once         one round: collect tokens, connect, check, mirror, then exit
  arkism discord invite APP   print the invite link for application id APP`

// Main is the subcommand.
func Main(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	cmd := "run"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "run":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		_ = NewManager(nil, nil, stderr).Run(ctx)
		return 0
	case "once":
		m := NewManager(nil, nil, stderr)
		m.Tick()
		m.Shutdown()
		return 0
	case "invite":
		if len(args) < 2 || args[1] == "" {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		fmt.Fprintln(stdout, InviteURL(args[1]))
		return 0
	}
	fmt.Fprintln(stderr, usage)
	return 2
}

// InviteURL is the bot invite: scopes bot + applications.commands and only the permissions the bot needs.
func InviteURL(appID string) string {
	q := url.Values{}
	q.Set("client_id", appID)
	q.Set("scope", "bot applications.commands")
	q.Set("permissions", fmt.Sprint(invitePerms()))
	return "https://discord.com/oauth2/authorize?" + q.Encode()
}
