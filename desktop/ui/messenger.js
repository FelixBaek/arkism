// 설정 › 메신저 (Figma 1012:60790): one bot per messenger. Discord uses the same /v1/discord API as before
// (token travels in once, the server keeps only the last four). Telegram (bot token + chat id) and Slack (webhook URL)
// use /v1/messengers, which edits the notify file the crew runner sends notices from; secrets never come back, only the last four.
window.ArkismMessenger = (ctx) => {
  "use strict";
  const { state, el, button, notice, request, render } = ctx;
  const raw = (n) => { n.setAttribute("data-no-i18n", ""); return n; };
  const S = { loaded: false, loading: false, bots: [], err: "", draft: {}, changing: {}, busy: false, dests: [], nerr: "", hooks: [], herr: "", hook: null, edit: null, logs: {} };
  const body = (extra) => JSON.stringify({ scope: state.scope, ...extra });
  const unwrap = (r) => (r?.discord_bot ? { ...r.discord_bot, id: r.id, revision: r.revision } : r);
  const ago = (iso) => { if (!iso) return ""; const m = Math.max(0, Math.round((Date.now() - new Date(iso)) / 60000)); return m < 1 ? "방금 확인" : m < 60 ? `${m}분 전 확인` : m < 2880 ? `${Math.round(m / 60)}시간 전 확인` : `${Math.floor(m / 1440)}일 전 확인`; }; // days past 48 hours (#190)
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
    try { S.hooks = (await request(`/v1/webhooks?${new URLSearchParams(state.scope)}`)).items || []; S.herr = ""; }
    catch (e) { S.herr = /HTTP 404|HTTP 501|not_found|not_implemented/.test(e.message) ? "unsupported" : e.message === "access_denied" ? "denied" : e.message; if (!quiet) S.hooks = []; }
    finally { S.loading = false; S.loaded = true; if (state.view === "settings" && state.settingsTab === "messenger") render(false); }
  }
  async function act(fn, ok, errs = {}) {
    if (S.busy) return;
    S.busy = true;
    try { await fn(); notice(ok); } catch (e) { notice(errs[e.message] || errText(e)); } finally { S.busy = false; }
    await load(true);
  }
  const fields = (b) => ({ name: b.name, channels: b.channels || [], ...(b.target ? { target: b.target } : {}), allowed: b.allowed || { user_ids: [], role_ids: [] }, ...(b.guild_id ? { guild_id: b.guild_id } : {}) });
  const upd = (b, extra) => request(`/v1/discord/bots/${encodeURIComponent(b.id)}/update`, { method: "POST", body: body({ expected_revision: b.revision, ...extra }) });
  const tokenOk = (t) => t.length >= 20;

  function card(name, st, right, ...rest) {
    const c = el("section", "mg-card");
    const head = el("div", "mg-head");
    const t = el("div", "mg-title");
    const logo = window.ArkismBrand?.mark(name, "mg-logo"); // 디스코드·텔레그램·슬랙 logo before the name
    t.append(...(logo ? [logo] : []), el("strong", "", name));
    if (st) t.append(el("span", `mg-state ${st[1]}`, st[0]));
    head.append(t);
    if (right) head.append(right);
    c.append(head, ...rest);
    return c;
  }
  function tokenRow(key, last4, placeholder, onSave, saveLabel, label = "봇 토큰") {
    const row = el("div", "mg-token");
    const lab = el("span", "mg-label", label);
    const input = el("input"); input.type = "password"; input.autocomplete = "off"; input.spellcheck = false; input.setAttribute("aria-label", label);
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
      const shown = el("div", "mg-masked"); shown.append(`${"•".repeat(24)}${last4}`, el("small", "", "저장됨"));
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
        el("p", "mg-note", "토큰은 Arkism에 암호화해 저장하고, 화면에는 끝 4자리만 보여요. 디스코드 개발자 포털에서 봇을 만들고 Message Content Intent를 켠 뒤 토큰을 넣어 주세요."));
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
    const invite = window.ArkismDiscordInvite?.(b.application_id || "");
    if (invite && /^\d{5,24}$/.test(b.application_id || "")) {
      const a = el("a", "btn", "서버에 초대하기"); a.href = invite; a.target = "_blank"; a.rel = "noreferrer";
      kids.push(a);
    }
    const off = button("연결 끊기", "ghost", null, async () => { if (await ask("디스코드 연결을 끊을까요? 저장된 토큰이 지워져요.")) act(() => upd(b, { archive: true }), "디스코드 연결을 끊었어요.").catch(() => {}); });
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
    const input = el("input"); input.autocomplete = "off"; input.spellcheck = false; input.setAttribute("aria-label", "채팅 ID");
    input.placeholder = "-1001234567890 또는 @채널이름"; input.value = S.draft.chat ?? d?.chat_id ?? "";
    input.addEventListener("input", () => { S.draft.chat = input.value; });
    const box = el("div", "mg-field"); box.append(input);
    row.append(el("span", "mg-label", "채팅 ID"), box);
    if (d) row.append(button("저장", "", null, () => act(() => saveDest("telegram", { chat_id: input.value.trim() }), "채팅 ID를 바꿨어요.")));
    return row;
  }
  function messengerCard(ch, name, d, note, kids) {
    if (S.nerr === "unsupported") return card(name, ["연결 안 됨", ""], null, el("p", "mg-note", "이 서버는 아직 이 연결을 지원하지 않아요. 서버를 새 버전으로 올려 주세요."));
    // A failed read is not "not connected": say so and offer a retry instead of showing a saved link as missing.
    if (S.nerr) return card(name, ["상태 모름", ""], button("다시 불러오기", "", null, () => void load()), el("p", "mg-note warn", `연결 상태를 불러오지 못했어요 (${S.nerr}).`));
    const right = el("div", "mg-actions");
    if (d) right.append(button("시험 보내기", "", null, () => testDest(ch)), button("연결 끊기", "ghost", null, async () => { if (await ask(OFF[ch][0])) void act(() => dropDest(ch), OFF[ch][1]); }));
    return card(name, d ? ["연결됨", "on"] : ["연결 안 됨", ""], d ? right : null, ...kids, note);
  }
  function telegramCard() {
    const d = dest("telegram");
    const row = tokenRow("telegram", d?.last4, "텔레그램 봇 토큰 (123456:ABC…)", (t) => {
      const chat = (S.draft.chat ?? d?.chat_id ?? "").trim();
      if (!chat) { notice("채팅 ID를 먼저 넣어 주세요."); return; }
      void act(() => saveDest("telegram", { bot_token: t, chat_id: chat }), "텔레그램을 연결했어요.");
    }, d ? "저장" : "저장하고 연결");
    return messengerCard("telegram", "텔레그램", d, el("p", "mg-note", "토큰은 Arkism에 저장하고, 화면에는 끝 4자리만 보여요. @BotFather로 만든 봇을 그룹이나 DM에 먼저 추가해 두세요."), [row, chatRow(d)]);
  }
  function slackCard() {
    const d = dest("slack");
    const row = tokenRow("slack", d?.last4, "https://hooks.slack.com/services/…", (t) => act(() => saveDest("slack", { webhook_url: t }), "슬랙을 연결했어요."), d ? "저장" : "저장하고 연결", "웹훅 주소");
    return messengerCard("slack", "슬랙", d, el("p", "mg-note", "웹훅 주소는 Arkism에 저장하고, 화면에는 끝 4자리만 보여요. 슬랙의 Incoming Webhooks에서 만들어요."), [row]);
  }

  // 웹훅: 기억이 바뀔 때 다른 서비스(https 주소)로 알림을 보내요. The server signs each delivery with a secret the app
  // makes here; it never comes back, so 자세히 shows it once while adding. Deleting asks first.
  const EVENTS = window.ArkismMessenger.WEBHOOK_EVENTS;
  const hookPath = (h, tail = "") => `/v1/webhooks/${encodeURIComponent(h.id)}${tail}`;
  const newSecret = () => Array.from(crypto.getRandomValues(new Uint8Array(24)), (b) => b.toString(16).padStart(2, "0")).join("");
  const HOOK_ERR = { invalid_request: "주소나 고른 알림을 확인해 주세요. 주소는 https://로 시작해야 해요.", access_denied: "웹훅은 사람 계정만 바꿀 수 있어요." };
  const hookAct = (fn, ok) => act(fn, ok, HOOK_ERR);
  async function openLog(h) {
    if (S.logs[h.id] && !S.logs[h.id].loading) { delete S.logs[h.id]; render(false); return; }
    S.logs[h.id] = { loading: true, items: [] }; render(false);
    try { S.logs[h.id] = { items: (await request(`${hookPath(h, "/deliveries")}?${new URLSearchParams({ ...state.scope, limit: "20" })}`)).items || [] }; }
    catch (e) { S.logs[h.id] = { items: [], error: errText(e) }; }
    render(false);
  }
  function logList(h) {
    const L = S.logs[h.id];
    if (!L) return null;
    const box = el("div", "mg-log");
    if (L.loading) box.append(el("p", "mg-note", "불러오는 중…"));
    else if (L.error) box.append(el("p", "mg-note warn", `보낸 기록을 불러오지 못했어요. ${L.error}`));
    else if (!L.items.length) box.append(el("p", "mg-note", "아직 보낸 알림이 없어요. '시험 보내기'로 확인해 보세요."));
    for (const d of L.items) {
      const st = window.ArkismMessenger.deliveryState(d), r = el("div", "mg-chan");
      const small = el("small", st.tone === "warn" ? "warn" : ""); small.append(el("span", "", st.label));
      if (st.detail) small.append(" · ", raw(el("span", "", st.detail)));
      r.append(el("strong", "", EVENTS[d.delivery?.event] || d.delivery?.event || "알림"), small);
      box.append(r);
    }
    return box;
  }
  // what the server sends with each delivery besides the body: method, wait time and the names of extra headers/query (values stay sealed)
  function shapeLine(h) {
    const parts = [h.method && h.method !== "POST" ? h.method : "", h.timeout_seconds ? `${h.timeout_seconds}초까지 기다림` : "",
      (h.header_names || []).length ? `헤더 ${h.header_names.join(", ")}` : "", (h.query_param_names || []).length ? `쿼리 ${h.query_param_names.join(", ")}` : ""].filter(Boolean);
    return parts.length ? raw(el("p", "mg-meta mono", parts.join(" · "))) : null;
  }
  function hookRow(h) {
    if (S.edit?.id === h.id) return hookForm(h);
    const right = el("div", "mg-actions");
    right.append(button("시험 보내기", "", null, () => hookAct(() => request(hookPath(h, "/test"), { method: "POST", body: body({}) }).then(() => { delete S.logs[h.id]; }), "시험 알림을 보냈어요. 결과는 '보낸 기록'에서 봐요.")),
      button(S.logs[h.id] ? "기록 닫기" : "보낸 기록", "", null, () => openLog(h)),
      button("고치기", "", null, () => { S.edit = draftOf(h); render(false); }),
      button(h.active ? "끄기" : "켜기", "ghost", null, () => hookAct(() => request(hookPath(h), { method: "PATCH", body: body({ expected_revision: h.revision, active: !h.active }) }), h.active ? "웹훅을 껐어요. 알림을 보내지 않아요." : "웹훅을 켰어요.")),
      button("지우기", "ghost", null, async () => { if (await ask("이 웹훅을 지울까요? 이 주소로 더는 알림을 보내지 않고, 되돌릴 수 없어요.")) void hookAct(() => request(`${hookPath(h)}?${new URLSearchParams(state.scope)}`, { method: "DELETE" }), "웹훅을 지웠어요."); }));
    const row = el("div", "mg-hook"), title = el("div", "mg-title");
    title.append(raw(el("strong", "mono", h.url)), el("span", `mg-state ${h.active ? "on" : ""}`, h.active ? "켜짐" : "꺼짐"));
    const ev = el("p", "mg-meta"); (h.events || []).forEach((e, i) => ev.append(...(i ? [" · "] : []), el("span", "", EVENTS[e] || e)));
    const shape = shapeLine(h);
    row.append(title, ev, ...(shape ? [shape] : []), right);
    const log = logList(h); if (log) row.append(log);
    return row;
  }
  // one draft for adding (no id) or changing a webhook; headers and query are typed fresh: stored values never come back
  const draftOf = (h) => (h ? { id: h.id, url: h.url, events: [...(h.events || [])], content: !!h.include_content, method: h.method || "POST", timeout: h.timeout_seconds ? String(h.timeout_seconds) : "", headers: "", query: "", clear: false, secret: "" }
    : { url: "", events: ["candidate.created", "memory.approved"], secret: newSecret(), content: false, method: "POST", timeout: "", headers: "", query: "" });
  const input = (f, key, label, attrs = {}) => {
    const n = el("input"); Object.assign(n, { autocomplete: "off", ...attrs, value: f[key] }); n.setAttribute("aria-label", label);
    n.addEventListener("input", () => { f[key] = n.value; });
    const box = el("label", "mg-field"); box.append(el("span", "mg-label", label), n); return box;
  };
  const area = (f, key, label, placeholder) => {
    const n = el("textarea", "mono"); n.value = f[key]; n.placeholder = placeholder; n.rows = 2; n.spellcheck = false; n.setAttribute("aria-label", label); raw(n);
    n.addEventListener("input", () => { f[key] = n.value; });
    const box = el("label", "mg-field"); box.append(el("span", "mg-label", label), n); return box;
  };
  function hookForm(h) {
    const editing = !!h, f = editing ? S.edit : (S.hook ||= draftOf());
    const box = el("div", "mg-hook-form");
    const url = input(f, "url", "받을 주소", { type: "url", placeholder: "https://example.com/arkism-hook" });
    const pick = el("fieldset", "mg-events"); pick.append(el("legend", "mg-label", "알림 보낼 때"));
    for (const [k, label] of Object.entries(EVENTS).filter(([k]) => k !== "webhook.test")) {
      const l = el("label", "mg-check"), c = el("input"); c.type = "checkbox"; c.checked = f.events.includes(k);
      c.addEventListener("change", () => { f.events = c.checked ? [...f.events, k] : f.events.filter((x) => x !== k); });
      l.append(c, el("span", "", label)); pick.append(l);
    }
    const more = el("details", "mg-more"), opts = el("div", "mg-more-body"); more.append(el("summary", "", "자세히"), opts); // a wrapper: flex on <details> itself does not lay out its content
    if (!editing) {
      const sec = el("input"); sec.readOnly = true; sec.value = f.secret; sec.setAttribute("aria-label", "서명 비밀값"); sec.className = "mono";
      const secField = el("label", "mg-field"); secField.append(el("span", "mg-label", "서명 비밀값 (받는 쪽에서 진짜 알림인지 확인할 때 써요. 지금만 보여요.)"), sec);
      opts.append(secField);
    } else if (f.secret) {
      const sec = el("input"); sec.readOnly = true; sec.value = f.secret; sec.setAttribute("aria-label", "새 서명 비밀값"); sec.className = "mono";
      const secField = el("label", "mg-field"); secField.append(el("span", "mg-label", "새 서명 비밀값 (저장하면 이 값으로 바뀌어요. 지금만 보여요.)"), sec);
      opts.append(secField);
    } else opts.append(button("서명 비밀값 새로 만들기", "small", null, () => { f.secret = newSecret(); render(false); }));
    const inc = el("label", "mg-check"), ic = el("input"); ic.type = "checkbox"; ic.checked = f.content; ic.addEventListener("change", () => { f.content = ic.checked; });
    inc.append(ic, el("span", "", "알림에 기억 내용도 넣기 (비공개·비밀 표시된 내용은 빼요)"));
    const method = el("select"); method.setAttribute("aria-label", "보내는 방식");
    for (const m of ["POST", "PUT", "PATCH"]) { const o = el("option", "", m); o.value = m; o.selected = m === f.method; method.append(o); }
    method.addEventListener("change", () => { f.method = method.value; });
    const mField = el("label", "mg-field"); mField.append(el("span", "mg-label", "보내는 방식"), method);
    const named = editing ? [...(h.header_names || []), ...(h.query_param_names || [])] : [];
    opts.append(inc, mField, input(f, "timeout", "답을 기다릴 시간 (초, 1~30, 비우면 기본)", { type: "number", min: "1", max: "30", inputMode: "numeric" }),
      area(f, "headers", "더 보낼 헤더 (한 줄에 하나, 이름: 값)", "Authorization: Bearer …"), area(f, "query", "주소에 붙일 쿼리 (한 줄에 하나, 이름=값)", "team=memory"),
      el("p", "mg-note", editing ? "헤더·쿼리 값은 봉인해 저장해서 다시 보여 주지 않아요. 비워 두면 지금 값을 그대로 둬요. 주소의 도메인을 바꾸면 저장한 헤더·쿼리는 지워지니 다시 넣어 주세요." : "헤더·쿼리 값은 봉인해 저장하고, 화면에는 이름만 보여요."));
    if (named.length) { const cl = el("label", "mg-check"), cc = el("input"); cc.type = "checkbox"; cc.checked = !!f.clear; cc.addEventListener("change", () => { f.clear = cc.checked; }); cl.append(cc, el("span", "", "저장한 헤더·쿼리 모두 지우기")); opts.append(cl); }
    if (editing && (f.method !== "POST" || f.timeout || named.length)) more.open = true;
    const save = button(editing ? "저장" : "웹훅 추가", "primary", null, () => {
      const u = f.url.trim(), shape = window.ArkismMessenger.hookShape(f);
      if (!/^https:\/\/\S+$/.test(u)) { notice("https://로 시작하는 주소를 넣어 주세요."); return; }
      if (!f.events.length) { notice("알림 보낼 때를 하나 이상 골라 주세요."); return; }
      if (shape.error) { notice(shape.error); return; }
      if (!editing) {
        void hookAct(() => request("/v1/webhooks", { method: "POST", body: body({ url: u, events: f.events, secret: f.secret, include_content: f.content, ...shape.create }) }).then(() => { S.hook = null; }), "웹훅을 추가했어요. '시험 보내기'로 확인해 보세요.");
        return;
      }
      const patch = window.ArkismMessenger.hookPatch(h, { ...f, url: u }, shape);
      if (!Object.keys(patch).length) { S.edit = null; render(false); return; }
      void hookAct(() => request(hookPath(h), { method: "PATCH", body: body({ expected_revision: h.revision, ...patch }) }).then(() => { S.edit = null; }), "웹훅을 고쳤어요.");
    });
    const foot = el("div", "mg-actions"); foot.append(save, ...(editing ? [button("취소", "ghost", null, () => { S.edit = null; render(false); })] : []));
    box.append(url, pick, more, foot);
    return box;
  }
  function webhookCard() {
    const title = "웹훅";
    if (S.herr === "unsupported") return card(title, ["연결 안 됨", ""], null, el("p", "mg-note", "이 서버는 아직 웹훅을 지원하지 않아요. 서버를 새 버전으로 올려 주세요."));
    if (S.herr === "denied") return card(title, ["권한 없음", "warn"], null, el("p", "mg-note", "웹훅은 사람 계정으로 로그인했을 때만 보고 바꿀 수 있어요."));
    if (S.herr) return card(title, ["상태 모름", ""], button("다시 불러오기", "", null, () => void load()), el("p", "mg-note warn", `웹훅을 불러오지 못했어요 (${S.herr}).`));
    const on = S.hooks.filter((h) => h.active).length;
    return card(title, S.hooks.length ? [`${on}개 켜짐`, on ? "on" : ""] : ["없음", ""], null,
      el("p", "mg-note", "기억 후보가 생기거나 기억이 승인·변경될 때 다른 서비스로 알림을 보내요."), ...S.hooks.map(hookRow), hookForm());
  }
  function channelsPanel() {
    const sec = el("section", "v4-side-sec");
    sec.append(el("h3", "", "연결된 채널"), el("p", "mg-note", "바꾸려면 팀·전문가 상세의 채널 연결에서 해요."));
    // one group per messenger, each under its logo; telegram and slack have one destination each
    const groups = [["디스코드", S.bots.flatMap((b) => (b.channels || []).map((c) => [`#${c.name || c.channel_id}`, c.label || "", (b.missing || []).some((m) => m.channel_id === c.channel_id)]))],
      ["텔레그램", dest("telegram") ? [[dest("telegram").chat_id || "채팅 ID 없음", "알림을 보내는 곳", false, true]] : []],
      ["슬랙", dest("slack") ? [[`웹훅 ····${dest("slack").last4 || ""}`, "알림을 보내는 곳", false, true]] : []]].filter(([, rows]) => rows.length);
    if (!groups.length) sec.append(el("p", "mg-note", "연결된 채널이 없어요."));
    for (const [name, rows] of groups) {
      const g = el("div", "mg-chan-group"), h = el("div", "mg-chan-head"), logo = window.ArkismBrand?.mark(name, "mg-logo");
      h.append(...(logo ? [logo] : []), el("strong", "", name), el("span", "mg-chan-n", `${rows.length}`)); g.append(h);
      for (const [t, sub, need, fixed] of rows) {
        const r = el("div", "mg-chan"); r.append(el("strong", "", t));
        const s2 = el("small", need ? "warn" : ""); s2.append(fixed ? el("span", "", sub) : raw(el("span", "", sub))); // only a channel label is user data
        if (need) s2.append(" · ", el("span", "", "권한 필요"));
        r.append(s2); g.append(r);
      }
      sec.append(g);
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
      wrap.append(discordCard(), telegramCard(), slackCard(), webhookCard());
    }
    return [wrap];
  }
  return { tab };
};
// 웹훅 알림 종류 (the server's webhookEvents) and one delivery's state in words: retry says when it goes again.
window.ArkismMessenger.WEBHOOK_EVENTS = { "candidate.created": "기억 후보가 생겼을 때", "memory.approved": "기억을 승인했을 때", "memory.rejected": "기억을 버렸을 때", "memory.updated": "기억이 바뀌었을 때", "memory.deleted": "기억을 지웠을 때", "consolidation.completed": "기억 정리가 끝났을 때", "ingest.completed": "파일 수집이 끝났을 때", "defense.triggered": "공간 방어 규칙이 작동했을 때", "webhook.test": "시험 알림" };
// a webhook form's method, wait and extra headers/query as the API wants them; error is a sentence for the person.
// Lines are "Name: value" (headers) and "name=value" (query); the server checks names and lengths again.
window.ArkismMessenger.hookShape = (f) => {
  const lines = (text, sep) => {
    const out = {};
    for (const line of String(text || "").split("\n").map((x) => x.trim()).filter(Boolean)) {
      const i = line.indexOf(sep), k = i > 0 ? line.slice(0, i).trim() : "", v = i > 0 ? line.slice(i + 1).trim() : "";
      if (!k || !v) return null;
      out[k] = v;
    }
    return out;
  };
  const headers = lines(f.headers, ":"), query = lines(f.query, "="), t = String(f.timeout ?? "").trim(), timeout = t ? Number(t) : 0;
  if (!headers) return { error: "헤더는 한 줄에 하나씩 '이름: 값'으로 적어 주세요." };
  if (!query) return { error: "쿼리는 한 줄에 하나씩 '이름=값'으로 적어 주세요." };
  if (!Number.isInteger(timeout) || timeout < 0 || timeout > 30) return { error: "기다릴 시간은 1~30초로 적어 주세요." };
  const create = { ...(f.method && f.method !== "POST" ? { method: f.method } : {}), ...(timeout ? { timeout_seconds: timeout } : {}),
    ...(Object.keys(headers).length ? { headers } : {}), ...(Object.keys(query).length ? { query_params: query } : {}) };
  return { headers, query, timeout, create };
};
// only what changed, for PATCH /v1/webhooks/{id}: typed headers/query replace the sealed set, 지우기 sends empty sets, blank keeps them
window.ArkismMessenger.hookPatch = (h, f, shape) => {
  const out = {}, same = (a, b) => { const key = (x) => [...x].sort((p, q) => p.localeCompare(q)).join("\n"); return key(a) === key(b); };
  if (f.url !== h.url) out.url = f.url;
  if (!same(f.events, h.events || [])) out.events = f.events;
  if (f.content !== !!h.include_content) out.include_content = f.content;
  if ((f.method || "POST") !== (h.method || "POST")) out.method = f.method;
  if (shape.timeout !== (h.timeout_seconds || 0)) out.timeout_seconds = shape.timeout;
  if (f.secret) out.secret = f.secret;
  if (Object.keys(shape.headers).length) out.headers = shape.headers; else if (f.clear) out.headers = {};
  if (Object.keys(shape.query).length) out.query_params = shape.query; else if (f.clear) out.query_params = {};
  return out;
};
window.ArkismMessenger.deliveryState = (v = {}) => {
  const d = v.delivery || {}, code = d.http_status ? `HTTP ${d.http_status}` : "", when = (t) => { const x = new Date(t); return Number.isNaN(x.getTime()) ? "" : x.toLocaleString(); };
  if (v.status === "done") return { label: "보냄", detail: [code, d.latency_ms ? `${d.latency_ms}ms` : ""].filter(Boolean).join(" · "), tone: "" };
  if (v.status === "retry") return { label: `다시 보낼 예정 (${v.attempts || 1}번 실패)`, detail: [code || d.error, v.next_attempt_at ? when(v.next_attempt_at) : ""].filter(Boolean).join(" · "), tone: "warn" };
  if (v.status === "failed") return { label: "보내지 못했어요", detail: code || d.error || "", tone: "warn" };
  if (v.status === "running") return { label: "보내는 중", detail: "", tone: "" };
  return { label: "보낼 차례 기다림", detail: "", tone: "" };
};
