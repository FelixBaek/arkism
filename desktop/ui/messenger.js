// 설정 › 메신저 (Figma 1012:60790): one bot per messenger. Discord uses the same /v1/discord API as before
// (token travels in once, the server keeps only the last four). Telegram (bot token + chat id) and Slack (webhook URL)
// use /v1/messengers, which edits the notify file the crew runner sends notices from; secrets never come back, only the last four.
window.ArkismMessenger = (ctx) => {
  "use strict";
  const { state, el, button, notice, request, render } = ctx;
  const raw = (n) => { n.setAttribute("data-no-i18n", ""); return n; };
  const S = { loaded: false, loading: false, bots: [], err: "", draft: {}, changing: {}, busy: false, dests: [], nerr: "" };
  const body = (extra) => JSON.stringify({ scope: state.scope, ...extra });
  const unwrap = (r) => (r?.discord_bot ? { ...r.discord_bot, id: r.id, revision: r.revision } : r);
  const ago = (iso) => { if (!iso) return ""; const m = Math.max(0, Math.round((Date.now() - new Date(iso)) / 60000)); return m < 1 ? "방금 확인" : m < 60 ? `${m}분 전 확인` : `${Math.round(m / 60)}시간 전 확인`; };
  const STATE = { connected: ["연결됨", "on"], connecting: ["연결 중", ""], waiting_token: ["연결 중", ""], needs_permission: ["권한 필요", "warn"], offline: ["꺼짐", ""], token_invalid: ["토큰 오류", "warn"] };
  const ERR = { secret_detected: "키나 토큰처럼 보이는 값이 있어 저장하지 않았어요.", invalid_request: "토큰 형식을 확인해 주세요.", revision_conflict: "다른 곳에서 먼저 고쳤어요. 다시 불러올게요." };
  const errText = (e) => ERR[e.message] || `처리하지 못했어요 (${e.message}).`;

  async function load(quiet) {
    if (S.loading) return;
    S.loading = true;
    try { const d = await request(`/v1/discord?${new URLSearchParams(state.scope)}`); S.bots = (d.bots || []).map(unwrap); S.err = ""; }
    catch (e) { S.err = /HTTP 404|HTTP 501|not_found|not_implemented/.test(e.message) ? "unsupported" : e.message; if (!quiet) S.bots = []; }
    try { S.dests = (await request(`/v1/messengers?${new URLSearchParams(state.scope)}`)).destinations || []; S.nerr = ""; }
    catch (e) { S.nerr = /HTTP 404|HTTP 501|not_found|not_implemented/.test(e.message) ? "unsupported" : e.message; if (!quiet) S.dests = []; }
    finally { S.loading = false; S.loaded = true; if (state.view === "settings" && state.settingsTab === "messenger") render(false); }
  }
  async function act(fn, ok) {
    if (S.busy) return;
    S.busy = true;
    try { await fn(); notice(ok); } catch (e) { notice(errText(e)); } finally { S.busy = false; }
    await load(true);
  }
  const fields = (b) => ({ name: b.name, channels: b.channels || [], ...(b.target ? { target: b.target } : {}), allowed: b.allowed || { user_ids: [], role_ids: [] }, ...(b.guild_id ? { guild_id: b.guild_id } : {}) });
  const upd = (b, extra) => request(`/v1/discord/bots/${encodeURIComponent(b.id)}/update`, { method: "POST", body: body({ expected_revision: b.revision, ...extra }) });
  const tokenOk = (t) => t.length >= 20;

  function card(name, st, right, ...rest) {
    const c = el("section", "mg-card");
    const head = el("div", "mg-head");
    const t = el("div", "mg-title");
    t.append(el("strong", "", name));
    if (st) t.append(el("span", `mg-state ${st[1]}`, st[0]));
    head.append(t);
    if (right) head.append(right);
    c.append(head, ...rest);
    return c;
  }
  function tokenRow(key, last4, placeholder, onSave, saveLabel, label = "봇 토큰") {
    const row = el("div", "mg-token");
    const lab = el("span", "mg-label", label);
    const input = el("input", "mono"); input.type = "password"; input.autocomplete = "off"; input.spellcheck = false; input.setAttribute("aria-label", label);
    const box = el("div", "mg-field");
    const editing = !last4 || S.changing[key];
    if (editing) {
      input.placeholder = placeholder; input.value = S.draft[key] || "";
      const go = button(saveLabel, "primary", null, () => { const t = input.value.trim(); if (!tokenOk(t)) { notice("값을 끝까지 붙여 넣어 주세요."); return; } S.draft[key] = ""; S.changing[key] = false; onSave(t); });
      box.append(input);
      row.append(lab, el("div", "mg-line"), go);
      row.children[1].append(box);
      if (last4) row.append(button("취소", "ghost", null, () => { S.changing[key] = false; render(false); }));
    } else {
      const shown = el("div", "mg-masked mono"); shown.append(`${"•".repeat(24)}${last4}`, el("small", "", "저장됨"));
      box.append(shown);
      const line = el("div", "mg-line"); line.append(box);
      row.append(lab, line, button("바꾸기", "", null, () => { S.changing[key] = true; render(false); }));
    }
    return row;
  }
  // Each part is its own node so the i18n patterns match; the bot name is user text.
  function metaLine(name, parts) {
    const p = el("p", "mg-meta");
    p.append(raw(el("span", "", name)));
    for (const t of parts) p.append(" · ", el("span", "", t));
    return p;
  }
  function discordCard() {
    const b = S.bots.find((x) => !x.archived) || null;
    if (S.err === "unsupported") return card("디스코드", ["연결 안 됨", ""], null, el("p", "mg-note", "이 서버는 아직 디스코드 연결을 지원하지 않아요. 서버를 새 버전으로 올려 주세요."));
    if (!b) {
      return card("디스코드", ["연결 안 됨", ""], null,
        tokenRow("discord", "", "디스코드 봇 토큰을 붙여 넣으세요", (t) => act(() => request("/v1/discord/bots", { method: "POST", body: body({ bot: { name: "Arkism", token: t } }) }), "토큰을 넘겼어요. 서버가 디스코드에 붙는 중이에요."), "저장하고 연결"),
        el("p", "mg-note", "토큰은 서버에 암호화해 저장하고, 화면에는 끝 4자리만 보여요. 디스코드 개발자 포털에서 Message Content Intent를 켜 주세요. 봇 만드는 법은 CREW › 디스코드에 있어요."));
    }
    const st = STATE[b.state] || [b.state, ""];
    const parts = [b.guild_name && `서버 ${b.guild_name}`, b.channels?.length ? `채널 ${b.channels.length}개` : "", ago(b.last_seen_at)].filter(Boolean);
    const kids = [tokenRow("discord", b.token_last4 || "····", "", (t) => act(() => upd(b, { bot: { ...fields(b), token: t } }), "새 토큰을 넘겼어요."), "저장"),
      metaLine(b.name, parts)];
    if (b.missing?.length || b.state === "needs_permission") {
      const w = el("div", "mg-warn");
      const names = (b.missing || []).map((m) => (b.channels || []).find((c) => c.channel_id === m.channel_id)?.name).filter(Boolean);
      w.append(el("span", "", names.length ? `#${names.join(", #")} 채널에 필요한 권한이 빠졌어요. 서버 관리자가 역할을 다시 줘야 해요.` : "필요한 권한이 빠졌어요. 서버 관리자가 역할을 다시 줘야 해요."),
        button("권한 다시 확인", "", null, () => act(() => request(`/v1/discord/bots/${encodeURIComponent(b.id)}/check`, { method: "POST", body: body({}) }), "권한을 다시 확인하는 중이에요.")));
      kids.push(w);
    }
    const off = button("연결 끊기", "ghost", null, () => { if (confirm("디스코드 연결을 끊을까요? 저장된 토큰이 지워져요.")) act(() => upd(b, { archive: true }), "디스코드 연결을 끊었어요.").catch(() => {}); });
    return card("디스코드", st, off, ...kids);
  }
  const OFF = { telegram: ["텔레그램 연결을 끊을까요? 저장된 값이 지워져요.", "텔레그램 연결을 끊었어요."], slack: ["슬랙 연결을 끊을까요? 저장된 값이 지워져요.", "슬랙 연결을 끊었어요."] };
  const dest = (ch) => S.dests.find((d) => d.channel === ch && d.name === "default");
  const saveDest = (ch, f) => request("/v1/messengers", { method: "POST", body: body({ channel: ch, ...f }) });
  const dropDest = (ch) => request("/v1/messengers/remove", { method: "POST", body: body({ channel: ch }) });
  async function testDest(ch) {
    if (S.busy) return;
    S.busy = true;
    try { const r = await request("/v1/messengers/test", { method: "POST", body: body({ channel: ch }) }); notice(r.ok ? "시험 메시지를 보냈어요." : `보내지 못했어요 (${r.error}).`); }
    catch (e) { notice(errText(e)); } finally { S.busy = false; }
  }
  // Telegram needs a chat id besides the token; a saved destination can change the chat without the token.
  function chatRow(d) {
    const row = el("div", "mg-token");
    const input = el("input", "mono"); input.autocomplete = "off"; input.spellcheck = false; input.setAttribute("aria-label", "채팅 ID");
    input.placeholder = "-1001234567890 또는 @채널이름"; input.value = S.draft.chat ?? d?.chat_id ?? "";
    input.addEventListener("input", () => { S.draft.chat = input.value; });
    const box = el("div", "mg-field"); box.append(input);
    row.append(el("span", "mg-label", "채팅 ID"), box);
    if (d) row.append(button("저장", "", null, () => act(() => saveDest("telegram", { chat_id: input.value.trim() }), "채팅 ID를 바꿨어요.")));
    return row;
  }
  function messengerCard(ch, name, d, note, kids) {
    if (S.nerr === "unsupported") return card(name, ["연결 안 됨", ""], null, el("p", "mg-note", "이 서버는 아직 이 연결을 지원하지 않아요. 서버를 새 버전으로 올려 주세요."));
    const right = el("div", "mg-actions");
    if (d) right.append(button("시험 보내기", "", null, () => testDest(ch)), button("연결 끊기", "ghost", null, () => { if (confirm(OFF[ch][0])) act(() => dropDest(ch), OFF[ch][1]); }));
    return card(name, d ? ["연결됨", "on"] : ["연결 안 됨", ""], d ? right : null, ...kids, note);
  }
  function telegramCard() {
    const d = dest("telegram");
    const row = tokenRow("telegram", d?.last4, "텔레그램 봇 토큰 (123456:ABC…)", (t) => {
      const chat = (S.draft.chat ?? d?.chat_id ?? "").trim();
      if (!chat) { notice("채팅 ID를 먼저 넣어 주세요."); return; }
      act(() => saveDest("telegram", { bot_token: t, chat_id: chat }), "텔레그램을 연결했어요.");
    }, d ? "저장" : "저장하고 연결");
    return messengerCard("telegram", "텔레그램", d, el("p", "mg-note", "토큰은 이 PC에 저장하고, 화면에는 끝 4자리만 보여요. @BotFather로 만든 봇을 그룹이나 DM에 먼저 추가해 두세요."), [row, chatRow(d)]);
  }
  function slackCard() {
    const d = dest("slack");
    const row = tokenRow("slack", d?.last4, "https://hooks.slack.com/services/…", (t) => act(() => saveDest("slack", { webhook_url: t }), "슬랙을 연결했어요."), d ? "저장" : "저장하고 연결", "웹훅 주소");
    return messengerCard("slack", "슬랙", d, el("p", "mg-note", "웹훅 주소는 이 PC에 저장하고, 화면에는 끝 4자리만 보여요. 슬랙의 Incoming Webhooks에서 만들어요."), [row]);
  }
  function channelsPanel() {
    const sec = el("section", "v4-side-sec");
    sec.append(el("h3", "", "연결된 채널"), el("p", "mg-note", "바꾸려면 팀·전문가 상세의 채널 연결에서 해요."));
    const rows = [];
    for (const b of S.bots) for (const c of b.channels || []) {
      const need = (b.missing || []).some((m) => m.channel_id === c.channel_id);
      rows.push([`디스코드 #${c.name || c.channel_id}`, c.label || "", need]);
    }
    if (!rows.length) sec.append(el("p", "mg-note", "연결된 채널이 없어요."));
    for (const [t, sub, need] of rows) {
      const r = el("div", "mg-chan"); r.append(el("strong", "", t));
      const s = el("small", need ? "warn" : ""); s.append(raw(el("span", "", sub))); if (need) s.append(" · ", el("span", "", "권한 필요"));
      r.append(s); sec.append(r);
    }
    return sec;
  }
  function tab(kit) {
    if (!S.loaded) void load();
    kit.side(channelsPanel());
    const wrap = el("div", "mg-list");
    if (!S.loaded) wrap.append(el("p", "pg-hint", "불러오는 중…"));
    else {
      if (S.err && S.err !== "unsupported") wrap.append(el("p", "mg-note warn", `디스코드 상태를 불러오지 못했어요 (${S.err}).`));
      wrap.append(discordCard(), telegramCard(), slackCard());
    }
    return [wrap];
  }
  return { tab };
};
