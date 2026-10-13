# Discord bots

`arkism discord` connects the Discord bots you add in **AGENT › 디스코드 (Discord)** and turns channel posts into work.

- It collects each bot token once from Arkism and keeps it in the OS keyring (service `arkism-discord`), or in a `0600` file on a machine without one. It never sends the token back. The app keeps only the last four characters.
- Only allowed users are heard (by default, the Discord server owner). Other bots are ignored.
- Everything it starts is read-only. Channel text reaches models only as quoted data.

## 1. Create the bot

1. Create an application at <https://discord.com/developers/applications>.
2. Open **Bot** and press **Reset Token**. Paste the token into Arkism (**AGENT › 디스코드 › 봇 추가 (Add bot)**).
   You can use one bot for every channel, or one bot per team or workflow.
3. On the same **Bot** page, turn on **Message Content Intent**. Without it the bot sees empty messages, so `@name` questions and auto-start channels do nothing.

## 2. Invite it

```bash
arkism discord invite APPLICATION_ID
```

This prints an invite link with the scopes `bot applications.commands` and only these permissions:

- View Channels
- Read Message History
- Send Messages
- Send Messages in Threads
- Create Public Threads
- Use Application Commands

**Do not give the bot Administrator.** The permission check reports `no_administrator` until you remove it.

## 3. Run it

Run `arkism discord` on the machine where Arkism runs.

On a server with systemd:

1. Create a service token with `memory-principal --role service` that has `handoff:discord` and write access to crew meetings, runs and assists.
2. Save it as `/srv/arkism/config/discord.token`, the path the unit sets in `ARKISM_DISCORD_TOKEN_FILE` (the default outside the unit is `~/.config/arkism/discord.token`). Without a token file, the owner token is used.
3. Write a systemd unit named `arkism-discord` that runs `arkism discord run` with `ARKISM_DISCORD_TOKEN_FILE` set, then run `systemctl enable --now arkism-discord`.

Other commands:

```bash
arkism discord run      # connect every bot and keep working (what the service runs)
arkism discord once     # one round: collect tokens, connect, check, then exit
```

## Using it

In a connected channel:

- A post in an **auto-start** channel opens a thread and starts that channel's team meeting, workflow or agent.
- `@name question` (plain text) or `/물어보기` gets one answer from that work agent. A team holds a meeting instead.
- Replies in a meeting thread become notes for the next round. `/회의 멈춤` stops the meeting.
- When the daily Discord budget is reached, the bot asks in the thread (계속 / 멈춤, continue / stop) before going on.

Permissions are checked every five minutes, and when you press **다시 확인 (Check again)** in the app.
