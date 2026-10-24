// CREW › 팀 · 회의 · 디스코드 · 사용량 (Figma v4: 208:18425 팀, 310:19981 팀 — 새로 만들기, 208:19103 회의,
// 208:19677 디스코드, 325:37545 사용량). Interface: the team/meeting/discord contract (2026-10-13).
// A team is a crew record of type "team" (called @이름, same namespace as work agents; workflows keep $이름).
// Meetings live in /v1/crew/meetings (the host engine runs the turns; this screen polls every 2 s while one runs),
// Discord bots in /v1/discord (tokens go to the host keyring; the record keeps only the last four characters),
// and CREW › 사용량 reads /v1/crew/usage. Every cost here is an estimate from public list prices (marked 추정).
window.ArkismTeams = (ctx) => {
  "use strict";
  const { state, $, el, icon, button, go, render, request, query, notice, ago } = ctx;
  const raw = (n) => { n.setAttribute("data-no-i18n", ""); return n; };
  // 화면 문구가 데이터와 한 줄에 섞여 raw()로 감쌀 때: 만들 때 번역한다 (한국어 화면이면 그대로).
  const tr = (s) => (window.ArkismI18n ? window.ArkismI18n.translate(s) : s);
  const pins = window.ArkismPins?.(ctx); // 항상 넣는 자료 (pins.js)

  // ---------------------------------------------------------------- vocabulary (mirrors the contract)
  const ROLES = [["moderator", "진행자"], ["worker", "작업자"], ["reviewer", "리뷰어"], ["scribe", "정리 담당"]];
  const MODES = [
    ["round_robin", "정해진 순서대로", "적힌 순서로 한 사람씩 말하고, 다 돌면 처음부터 다시 돌아요."],
    ["moderator_picks", "진행자가 다음 사람 지명", "진행자가 매번 다음에 말할 사람을 골라요. 필요한 사람만 다시 부를 수 있어요."]
  ];
  const STANCE = { agree: "찬성", disagree: "반대", opinion: "의견", approve: "승인" };
  const MEETING_STATUS = {
    pending: ["시작 전", "line"], running: ["진행 중", "warn"], stopping: ["멈추는 중", "warn"], waiting: ["확인 필요", "warn"],
    concluded: ["결론 남", "line"], no_conclusion: ["결론 없음", "line"], stopped: ["멈춤", "line"], failed: ["실패", "warn"]
  };
  const LIVE = new Set(["pending", "running", "stopping", "waiting"]);
  const ENDED = {
    max_rounds: "최대 라운드까지 했어요", token_cap: "토큰 상한에 닿았어요", all_approved: "리뷰어가 모두 승인했어요", moderator: "진행자가 끝냈어요",
    human_stop: "직접 멈췄어요", budget: "예산 확인에서 멈췄어요", error: "오류로 멈췄어요"
  };
  const BOT_STATE = {
    waiting_token: ["토큰 기다림", "line"], connecting: ["연결 중", "line"], connected: ["연결됨", "line"],
    needs_permission: ["권한 필요", "warn"], offline: ["꺼짐", "line"], token_invalid: ["토큰 오류", "warn"]
  };
  const BOT_MODES = [
    ["shared", "봇 하나로 나눠 쓰기", "봇 하나를 서버에 초대하고, 채널마다 부를 팀·워크플로우를 정해요. 설정이 가장 간단해요."],
    ["per_target", "팀마다 봇", "팀이나 워크플로우마다 봇을 따로 만들어요. 디스코드에서 이름과 프로필이 따로 보여요."]
  ];
  // Invite: scopes bot + applications.commands; never Administrator (contract › Discord behaviour).
  const PERMS = [["View Channels", "채널 보기", 10], ["Read Message History", "메시지 기록 보기", 16], ["Send Messages", "메시지 보내기", 11],
    ["Send Messages in Threads", "스레드에서 메시지 보내기", 38], ["Create Public Threads", "공개 스레드 만들기", 35], ["Use Application Commands", "앱 명령어 사용", 31]];
  const PERM_BITS = PERMS.reduce((s, [, , bit]) => s + (1n << BigInt(bit)), 0n).toString(); // 311385197568
  const inviteURL = (appId) => `https://discord.com/oauth2/authorize?client_id=${encodeURIComponent(appId)}&scope=bot%20applications.commands&permissions=${PERM_BITS}`;
  window.ArkismDiscordInvite = inviteURL; // 설정 › 메신저 uses the same invite link
  const RANGES = [["today", "오늘"], ["7d", "7일"], ["30d", "30일"], ["90d", "90일"], ["all", "전체"]];
  const ERRORS = {
    secret_detected: "키나 토큰처럼 보이는 값이 있어 저장하지 않았어요.",
    invalid_request: "빈 칸이나 형식을 확인해 주세요. @이름은 띄어쓰기 없이 글자·숫자·_·-만 써요.",
    revision_conflict: "다른 곳에서 먼저 고쳤어요. 다시 불러와 주세요.",
    handle_taken: "같은 @이름이 이미 있어요. 전문가와 팀은 이름을 함께 써요."
  };
  const errorText = (e) => ERRORS[e.message] || (e.message === "HTTP 409" ? ERRORS.handle_taken : `처리하지 못했어요 (${e.message}).`);
  const unsupported = (e) => /HTTP 404|not_found|HTTP 501|not_implemented/.test(e?.message || "");

  // ---------------------------------------------------------------- data
  const items = () => state.data.crew || [];
  const agents = () => items().filter((r) => r.crew?.type === "agent");
  const teams = () => items().filter((r) => r.crew?.type === "team");
  const workflows = () => items().filter((r) => r.crew?.type === "workflow");
  const byId = (id) => items().find((r) => r.id === id);
  const sorted = (members) => [...(members || [])].sort((a, b) => (a.order || 0) - (b.order || 0));
  const agentModel = (a) => (a?.crew?.model_mode === "auto" ? "auto" : a?.crew?.model || "");
  const memberModel = (m) => m.model_override || agentModel(byId(m.agent_id));
  const memberName = (m) => byId(m.agent_id)?.crew?.name || m.name || m.agent_id;
  const handleFrom = (name) => (name || "").replace(/\s+/g, "").replace(/[^\p{L}\p{N}_-]/gu, "").slice(0, 32);
  const clone = (x) => JSON.parse(JSON.stringify(x));
  // Records carry their body under crew_meeting / discord_bot / discord_settings (memoryd Record); requests send meeting / bot / settings.
  const unwrap = (r, key) => (r?.[key] ? { ...r[key], id: r.id, revision: r.revision } : r);
  const mt = (r) => unwrap(r, "crew_meeting");
  const bt = (r) => unwrap(r, "discord_bot");
  const body = (extra) => JSON.stringify({ scope: state.scope, ...extra });

  const num = (n) => {
    n = Number(n || 0);
    if (n >= 1e6) return `${(n / 1e6).toFixed(n >= 1e7 ? 0 : 2).replace(/\.?0+$/, "")}M`;
    if (n >= 1e3) return `${(n / 1e3).toFixed(n >= 1e4 ? 0 : 1).replace(/\.0$/, "")}K`;
    return String(Math.round(n));
  };
  const usd = (n) => `$${Number(n || 0).toFixed(2)}`;
  const pad2 = (n) => String(n || 0).padStart(2, "0");
  const pct = (r) => (r == null ? "—" : `${Number(r).toFixed(1)}%`); // the server sends percentages (0–100)
  const est = () => el("span", "tm-est", "추정");
  function modelLabel(m) {
    const s = (m || "").trim();
    if (!s) return "모델 없음";
    if (s === "auto") return "자동 (Clef)";
    const l = s.toLowerCase();
    if (/^(claude )?opus/.test(l)) return `Claude Opus ${s.match(/[\d.]+/)?.[0] || ""}`.trim();
    if (/^(claude )?sonnet/.test(l)) return `Claude Sonnet ${s.match(/[\d.]+/)?.[0] || ""}`.trim();
    if (l.startsWith("deepseek")) return `DeepSeek ${(s.match(/v\d+/i)?.[0] || "").toUpperCase()}`.trim();
    if (l.startsWith("gpt")) return s.toUpperCase();
    if (l.startsWith("glm")) return s.toUpperCase();
    if (l.startsWith("gemini")) return s.replace(/-/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
    return s;
  }
  // The maker's mark for a model (Figma uses the agent logos at 16 px); a robot when the app has no logo.
  function mark(model) {
    const m = (model || "").toLowerCase();
    const box = el("span", "tm-mark");
    box.append(window.ArkismBrand?.mark(m) || icon(m === "auto" ? "sparkle" : "robot"));
    return box;
  }
  const badge = (text, tone = "line") => el("span", `tm-badge ${tone}`, text);
  function sw(on, label, onFlip) {
    const s = el("button", "switch");
    s.type = "button";
    s.setAttribute("role", "switch");
    s.setAttribute("aria-checked", String(!!on));
    s.setAttribute("aria-label", label);
    s.append(el("i"));
    s.addEventListener("click", (e) => { e.stopPropagation(); onFlip(!on); });
    return s;
  }
  function card(cls, title, sub) {
    const c = el("section", `tm-card ${cls || ""}`.trim());
    if (title) { const h = el("div", "tm-card-head"); h.append(el("h3", "", title)); if (sub) h.append(el("p", "", sub)); c.append(h); }
    return c;
  }
  function field(label, input, cls) { const f = el("label", `tm-field ${cls || ""}`.trim()); f.append(el("span", "", label), input); return f; }
  function input(value, onInput, attrs = {}) {
    const i = el(attrs.tag || "input", "tm-input");
    if (attrs.type) i.type = attrs.type;
    if (attrs.placeholder) i.placeholder = attrs.placeholder;
    if (attrs.max) i.maxLength = attrs.max;
    if (attrs.rows) i.rows = attrs.rows;
    i.value = value ?? "";
    i.addEventListener("input", () => onInput(i.value));
    return i;
  }
  function select(options, value, onChange, cls = "tm-select") {
    const s = el("select", cls);
    for (const [v, l] of options) { const o = el("option", "", l); o.value = v; o.selected = v === value; s.append(o); }
    s.addEventListener("change", () => onChange(s.value));
    return s;
  }
  function link(text, onClick, cls = "tm-link") { const b = el("button", cls, text); b.type = "button"; b.addEventListener("click", onClick); return b; }
  const empty = (text) => el("p", "tm-empty", text);
  const sideLabel = (text, aside) => { const h = el("div", "tm-side-head"); h.append(el("h4", "", text)); if (aside) h.append(el("span", "", aside)); return h; };
  function kv(pairs) {
    const box = el("div", "tm-kv");
    for (const [k, v] of pairs) { if (v == null || v === "") continue; const r = el("div", ""); r.append(el("span", "", k)); r.append(typeof v === "string" ? el("b", "", v) : v); box.append(r); }
    return box;
  }
  const side = (...kids) => { const s = el("div", "tm-side"); s.append(...kids); $("#detail").replaceChildren(s); };

  const S = {
    team: null, draft: null, draftFor: null, dirty: false, stats: new Map(), adding: false, ctxAdding: false, drag: -1,
    nd: null, ndFrom: "", estimate: null, estTimer: 0, estKey: "",
    meetTab: "running", meetings: null, counts: {}, meetErr: "", meetSel: null, meeting: null, note: "", noteFocus: false, newMeeting: null, tick: 0, timer: 0,
    discord: null, discordErr: "", discordLoading: false, bot: null, settingsOpen: false, connect: null, channels: new Map(), botDraft: null, botDraftFor: null, connectTimer: 0,
    usage: null, uState: "idle", uErr: "", uRange: "30d", uKind: "all", agentStatus: null
  };
  const here = (view) => state.view === view;

  async function reloadCrew() { state.data.crew = (await request(`/v1/crew?${query()}`)).items || []; }
  function replaceRecord(rec) {
    if (!rec?.id) return;
    const i = items().findIndex((r) => r.id === rec.id);
    if (i >= 0) state.data.crew[i] = { ...state.data.crew[i], ...rec }; else state.data.crew = [...items(), rec];
  }

  // ---------------------------------------------------------------- discord data (shared by 팀 and 디스코드)
  async function loadDiscord(quiet) {
    if (S.discordLoading) return;
    S.discordLoading = true;
    try {
      const d = await request(`/v1/discord?${query()}`);
      const s = d.settings || {};
      S.discord = { bots: (d.bots || []).map(bt), settings: unwrap(s, "discord_settings"), stats: d.stats || {} };
      S.discordErr = "";
    } catch (e) {
      if (!quiet || !S.discord) { S.discord = S.discord || { bots: [], settings: {}, stats: {} }; S.discordErr = unsupported(e) ? "unsupported" : e.message; }
    } finally { S.discordLoading = false; }
    if (["teams", "discord", "teamNew"].includes(state.view)) render(false);
    popRedraw("overview");
  }
  const bots = () => S.discord?.bots || [];
  const botById = (id) => bots().find((b) => b.id === id);
  const channelsFor = (teamId) => bots().flatMap((b) => (b.channels || []).filter((c) => c.action?.kind === "meeting" && c.action.id === teamId).map((c) => ({ ...c, bot: b })));
  function targetLabel(action, short) {
    if (!action?.kind) return "정하지 않음";
    const r = byId(action.id);
    if (action.kind === "meeting") { const tpl = r?.crew?.templates?.find((t) => t.key === action.template_key); return `${r?.crew?.name || "팀"}${short ? "" : ` ${tpl ? `· ${tpl.name}` : "회의"}`}`; }
    if (action.kind === "workflow") return `$${r?.crew?.handle || action.id}`;
    return `@${r?.crew?.handle || action.id}`;
  }

  // ================================================================ 팀 (208:18425)
  function teamBadge(rec) {
    const ch = channelsFor(rec.id);
    if (!ch.length) return null;
    if (ch.some((c) => ["needs_permission", "token_invalid"].includes(c.bot.state))) return ["확인 필요", "warn"];
    return ch.some((c) => c.bot.state === "connected") ? ["연결됨", "on"] : null;
  }
  // 아바타: a preset drawing on a background colour; the model mark stands in when an expert has none.
  function face(rec, size) {
    const a = rec?.crew?.avatar;
    if (!a?.art || !a?.bg) return mark(agentModel(rec));
    const box = el("span", `tm-face${size ? ` ${size}` : ""}`);
    box.style.setProperty("--face-bg", `var(--avatar-${a.bg})`);
    const img = el("img"); img.src = `img/avatars/${a.art}.png`; img.alt = "";
    box.append(img);
    return box;
  }
  const coverSrc = (c) => c?.thumb || c?.url || "";
  function coverCredit(c) {
    if (!c?.author) return null;
    const small = el("small", "tm-credit");
    if (c.author_url) {
      const a = el("a", "", c.author); a.href = c.author_url; a.target = "_blank"; a.rel = "noreferrer"; a.setAttribute("data-no-i18n", "");
      small.append(a);
    } else small.append(raw(el("span", "", tr(c.author))));
    return small;
  }
  function loadStats(id) {
    if (S.stats.has(id)) return;
    S.stats.set(id, null);
    request(`/v1/crew/teams/${encodeURIComponent(id)}/stats?${query({ days: 30 })}`).then((r) => S.stats.set(id, r), () => S.stats.set(id, { error: true })).then(() => popRedraw("overview"));
  }
  const dayKey = (t) => { const d = new Date(t); return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`; };
  // Daily bars for the last 30 days from this team's meetings (the stats endpoint only sends totals).
  async function loadMeetingsOf(pop) {
    try {
      const r = await request(`/v1/crew/meetings?${query({ status: "all", team_id: pop.id, limit: 200 })}`);
      pop.meetings = (r.items || []).map(mt);
    } catch { pop.meetings = []; }
    popRedraw("overview"); popRedraw("history");
  }
  function bars(pop, pick) {
    const days = Array.from({ length: 30 }, (_, i) => dayKey(Date.now() - (29 - i) * 864e5));
    const sum = Object.fromEntries(days.map((k) => [k, 0]));
    for (const m of pop.meetings || []) { const k = dayKey(m.created_at || m.updated_at); if (k in sum) sum[k] += pick(m); }
    const max = Math.max(...Object.values(sum), 0);
    const box = el("span", "tm-bars"); box.setAttribute("aria-hidden", "true");
    days.forEach((k, i) => { const b = el("i"); b.style.height = `${max ? Math.max(8, (sum[k] / max) * 100) : 8}%`; if (i === 29) b.className = "last"; box.append(b); });
    return box;
  }

  // ================================================================ 팀 목록 (208:18425)
  function teamCard(r) {
    const x = r.crew, cv = x.cover;
    // A card holds three controls (open the team, each face, the photo credit), so it is a box with a full-size open button
    // underneath rather than one button with buttons inside it.
    const c = el("div", `tm-card2${cv ? " has-cover" : ""}`); c.dataset.id = r.id;
    const open = el("button", "tm-card2-open"); open.type = "button"; open.setAttribute("aria-label", x.name || "팀"); open.addEventListener("click", () => openPopup(r.id)); c.append(open);
    if (cv) { const img = el("img", "tm-card2-img"); img.alt = ""; img.src = coverSrc(cv); c.append(img); }
    const top = el("div", "tm-card2-top");
    const b = teamBadge(r); top.append(b && b[1] === "warn" ? badge(b[0], b[1]) : el("span")); // Figma shows no status badge; only the needs-attention one stays
    const faces = el("span", "tm-faces");
    sorted(x.members).slice(0, 5).forEach((m) => {
      const a = byId(m.agent_id), f = el("button", "tm-face-btn"); f.type = "button"; f.dataset.name = a?.crew?.name || ""; f.setAttribute("aria-label", a?.crew?.name || "전문가"); f.append(face(a, "l"));
      f.addEventListener("click", () => window.dispatchEvent(new CustomEvent("arkism:open-expert", { detail: { id: m.agent_id } }))); faces.append(f);
    });
    top.append(faces);
    const title = raw(el("strong", /[A-Za-z]/.test(x.name || "") ? "tm-title-en" : "", x.name)); // English names read as the big uppercase title: DESIGN REVIEW.
    c.append(top, title);
    // hover plate: category + @handle slide up from the bottom edge
    const plate = el("span", "tm-card2-plate");
    plate.append(raw(el("span", "tm-card2-cat", x.category || "")), raw(el("b", "", x.handle ? `@${x.handle}` : "")));
    c.append(plate);
    return c;
  }
  const catIcon = ctx.catIcon;
  function renderTeams() {
    $("#controls").replaceChildren();
    const table = $("#table");
    table.className = "tm-wrap";
    if (!S.discord) void loadDiscord(true);
    const list = teams();
    const cats = [...new Set(list.map((r) => r.crew.category).filter(Boolean))];
    if (S.cat && !cats.includes(S.cat)) S.cat = "";
    const panel = el("section", "tm-panel");
    const bar = el("div", "tm-cats");
    const chips = el("div", "tm-chips");
    for (const [key, label] of [["", "모든 카테고리"], ...cats.map((c) => [c, c])]) {
      const n = key ? list.filter((r) => r.crew.category === key).length : list.length;
      const c = el("button", `tm-chip${S.cat === key ? " active" : ""}`); c.type = "button";
      c.append(icon(catIcon(key)), key ? raw(el("span", "", label)) : el("span", "", label), el("output", "", String(n)));
      c.addEventListener("click", () => { S.cat = key; render(false); });
      chips.append(c);
    }
    bar.append(chips, button("+ 새 팀", "primary", null, () => openPopup(null)));
    const grid = el("div", "tm-grid2");
    for (const r of list.filter((x) => !S.cat || x.crew.category === S.cat)) grid.append(teamCard(r));
    const add = el("button", "tm-card2 tm-card2-new"); add.type = "button";
    add.append(icon("plus"), el("b", "", "새 팀"), el("small", "", "전문가를 골라 순서를 정하면 @이름으로 불러요"));
    add.addEventListener("click", () => openPopup(null));
    grid.append(add);
    panel.append(bar, grid);
    if (!list.length) panel.append(empty(agents().length ? "아직 팀이 없어요. 새 팀을 만들어 전문가 여럿이 함께 회의하게 해 보세요." : "팀은 전문가로 만들어요. 먼저 전문가를 만들어 주세요."));
    table.replaceChildren(panel);
  }

  // ================================================================ 팀 상세 팝업 (961:60688 · 981:59766 · 981:60196 · 985:60681 · 967:60615 · 310:19981)
  const popup = window.ArkismCrewPopup?.({ el, button });
  const covers = window.ArkismCoverPicker?.({ el, icon, request, query, state, notice });
  let kit = null;
  const same = (a, b) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);
  const norm = (c) => ({ ...c, mode: c.mode || "round_robin", discord_mirror: !!c.discord_mirror, members: sorted(c.members).map((m) => ({ ...m, focus: m.focus || "" })), ending: { max_rounds: 4, token_cap: 100000, end_on_all_approved: true, end_on_moderator: true, ...(c.ending || {}) } });
  const modeName = (k) => MODES.find(([x]) => x === (k || "round_robin"))?.[1] || k;
  const roleName = (k) => ROLES.find(([x]) => x === k)?.[1] || k;
  // What differs between two versions of a team, in the person's words (save bar count · 기록 탭).
  function changes(a, b) {
    a = norm(a); b = norm(b);
    const out = [];
    if (a.name !== b.name) out.push(`이름 ${a.name || "없음"} → ${b.name || "없음"}`);
    if ((a.role || "") !== (b.role || "")) out.push("설명 바꿈");
    if ((a.handle || "") !== (b.handle || "")) out.push(`@이름 ${a.handle || "없음"} → ${b.handle || "없음"}`);
    if ((a.category || "") !== (b.category || "")) out.push(`카테고리 ${a.category || "없음"} → ${b.category || "없음"}`);
    if (!same(a.cover, b.cover)) out.push("카드 사진 바꿈");
    if (a.mode !== b.mode) out.push(`회의 방식 → ${modeName(b.mode)}`);
    if (a.ending.max_rounds !== b.ending.max_rounds) out.push(`최대 라운드 ${a.ending.max_rounds} → ${b.ending.max_rounds}`);
    if (a.ending.token_cap !== b.ending.token_cap) out.push(`토큰 상한 ${Number(a.ending.token_cap).toLocaleString("en-US")} → ${Number(b.ending.token_cap).toLocaleString("en-US")}`);
    if (a.ending.end_on_all_approved !== b.ending.end_on_all_approved) out.push("리뷰어 승인 종료 바꿈");
    if (a.ending.end_on_moderator !== b.ending.end_on_moderator) out.push("진행자 종료 바꿈");
    const ids = (c) => c.members.map((m) => m.agent_id);
    for (const m of b.members) if (!ids(a).includes(m.agent_id)) out.push(`${memberName(m)} 추가 (${roleName(m.role)})`);
    for (const m of a.members) if (!ids(b).includes(m.agent_id)) out.push(`${memberName(m)} 뺌`);
    if (ids(a).length === ids(b).length && ids(a).every((x) => ids(b).includes(x)) && !same(ids(a), ids(b))) out.push("발언 순서 바꿈");
    if (a.members.some((m) => { const n = b.members.find((x) => x.agent_id === m.agent_id); return n && (n.role !== m.role || n.focus !== m.focus); })) out.push("전문가 역할·맡을 일 바꿈");
    if (!same(a.context, b.context)) out.push("팀 자료 바꿈");
    if (!same(a.templates, b.templates)) out.push("회의 템플릿 바꿈");
    if (!same(a.pinned || [], b.pinned || [])) out.push("항상 넣는 자료 바꿈");
    if (a.discord_mirror !== b.discord_mirror) out.push("디스코드에도 올리기 바꿈");
    return out;
  }
  const pdiff = (pop) => changes(pop.base, pop.d);
  function popRedraw(tab) {
    const pop = S.pop;
    if (!pop?.h?.dialog.isConnected || pop.tab !== tab) return;
    // every box that may be the one scrolling (the body, the dialog in a short window, the page in a 새 창으로 window)
    const kept = [pop.h.body, pop.h.dialog, document.scrollingElement].filter(Boolean).map((e) => [e, e.scrollTop]);
    pop.h.select(tab);
    for (const [e, top] of kept) e.scrollTop = top;
  }
  // 판 이력 from the server (GET /v1/crew/versions/{id}): the live definition first, then the ones each save replaced.
  async function loadVersions(pop) {
    try {
      const items = (await request(`/v1/crew/versions/${encodeURIComponent(pop.id)}?${query()}`)).items || [];
      pop.versions = items.map((x, i) => ({ v: x.revision, at: Date.parse(x.at) || 0, crew: x.crew,
        summary: items[i + 1] ? changes(items[i + 1].crew, x.crew).slice(0, 2).join(" · ") || "저장" : x.revision === 1 ? "처음 만듦" : "처음 기록" }));
    } catch { pop.versions = []; }
    popRedraw("history");
  }

  window.addEventListener("arkism:open-team", (e) => { go("teams"); setTimeout(() => openPopup(e.detail?.id)); });
  function openPopup(id) {
    if (S.pop || !popup) return;
    const rec = id ? byId(id) : null;
    const base = rec ? clone(rec.crew) : blankTeam();
    const pop = S.pop = { id: id || null, rec, base, d: clone(base), est: null, estKey: "", estTimer: 0, meetings: null, tab: "overview", test: { text: "", m: null, busy: false }, handleTouched: !!rec, from: "" };
    if (rec) { S.stats.delete(id); loadStats(id); void loadMeetingsOf(pop); void loadVersions(pop); }
    pop.estBox = el("div", "tm-est-box");
    const tabs = [["overview", "개요"], ["test", "테스트"], ["history", "기록"]].map(([key, label]) => ({ key, label, render: (box) => { pop.tab = key; paintTab(pop, box); } }));
    const actions = [pop.estBox];
    pop.h = popup.open({ title: "", sub: "", tabs, initial: "overview", actions, cls: rec ? "tm-pop" : "tm-pop is-new", popout: rec ? { event: "arkism:open-team", detail: { id }, title: rec.crew.name || "" } : null, onClose: () => { clearTimeout(pop.estTimer); clearTimeout(pop.testTimer); S.pop = null; } });
    const dlg = pop.h.dialog;
    // 961:60688: the tabs sit at the head's right under the estimate, as pills
    const side = dlg.querySelector(".crew-popup-actions"), top = el("div", "tm-pop-side-top");
    top.append(...side.childNodes); side.replaceChildren(top, dlg.querySelector(".crew-popup-tabs"));
    // closing with unsaved changes asks first (Esc and 닫기)
    const guard = (e) => {
      if (!pdiff(pop).length) return;
      e.preventDefault(); e.stopImmediatePropagation();
      void ask(pop.id ? "저장하지 않은 변경이 있어요. 버리고 닫을까요?" : "아직 만들지 않았어요. 버리고 닫을까요?").then((yes) => { if (yes) { pop.base = clone(pop.d); dlg.close(); } });
    };
    dlg.addEventListener("cancel", guard);
    dlg.addEventListener("click", (e) => { if (e.target.closest(".crew-popup-close")) guard(e); }, true);
    paintHead(pop);
    queueEst(pop);
  }
  function closePopup() { S.pop?.h?.dialog.close(); }

  function paintTab(pop, box) {
    box.classList.add("tm-pop-body");
    box.replaceChildren(...(pop.tab === "overview" ? overviewTab(pop) : pop.tab === "test" ? testTab(pop) : historyTab(pop)));
  }
  function changed(pop, structural) {
    if (!pop.handleTouched) pop.d.handle = handleFrom(pop.d.name);
    queueEst(pop);
    if (structural) popRedraw("overview"); else updateBar(pop);
  }

  // head: card preview (opens the photo picker) · category · name · @name · purpose; the estimate sits at the right
  function takenHandle(pop) {
    const h = (pop.d.handle || "").toLowerCase();
    return !!h && [...agents(), ...teams()].some((r) => r.id !== pop.id && (r.crew.handle || "").toLowerCase() === h);
  }
  function inline(pop, get, set, attrs) {
    const i = raw(input(get(), (v) => { set(v); changed(pop); }, attrs));
    let before = "";
    i.className = `tm-plain ${attrs.cls || ""}`.trim();
    i.addEventListener("focus", () => { before = i.value; });
    i.addEventListener("keydown", (e) => {
      if (e.key === "Enter" && !e.isComposing) { e.preventDefault(); i.blur(); }
      if (e.key === "Escape" && i.value !== before) { e.stopPropagation(); e.preventDefault(); i.value = before; set(before); changed(pop); }
    });
    return i;
  }
  function paintHead(pop) {
    const d = pop.d, dlg = pop.h.dialog;
    const head = dlg.querySelector(".crew-popup-head");
    head.classList.remove("has-cover"); head.style.removeProperty("--cover"); // the photo shows on the card preview, not behind the head
    const card = el("button", `tm-prev${d.cover ? " has-cover" : ""}`); card.type = "button"; card.setAttribute("aria-label", "카드 사진 바꾸기");
    if (d.cover) { const img = el("img"); img.alt = ""; img.src = coverSrc(d.cover); card.append(img); } else card.append(icon("image"));
    // the photo credit sits beside the photo, not on it (and a link cannot live inside the button)
    const credit = coverCredit(d.cover); credit?.classList.add("tm-pop-credit");
    card.addEventListener("click", () => covers?.open({ anchor: card, current: d.cover, onPick: (c) => { if (c) d.cover = c; else delete d.cover; paintHead(pop); changed(pop); } }));
    const words = el("div", "tm-pop-words");
    const row = el("div", "tm-pop-row");
    const cats = [...new Set([...teams(), ...workflows()].map((r) => r.crew.category).filter(Boolean))];
    if (d.category && !cats.includes(d.category)) cats.push(d.category);
    const cs = el("button", "tm-chip tm-cat"); cs.type = "button"; cs.setAttribute("aria-label", "카테고리");
    cs.append(...ctx.catChipBody(d.category));
    cs.addEventListener("click", () => ctx.catMenu(cs, { list: cats, current: d.category || "", onPick: (v) => { if (v) d.category = v; else delete d.category; paintHead(pop); changed(pop); } }));
    row.append(cs);
    if (!pop.id && d.members?.length) { // 310:19981 shows the members' faces on a new team; 961:60688 shows only the category. No members: no empty faces box pushing the chip off the name's left edge
      const faces = el("span", "tm-faces"); sorted(d.members).slice(0, 5).forEach((m) => faces.append(face(byId(m.agent_id), "s")));
      row.prepend(faces);
    }
    const line = el("div", "tm-pop-title");
    const name = inline(pop, () => d.name, (v) => { d.name = v; if (!pop.handleTouched) tag.value = handleFrom(v); }, { max: 60, placeholder: "팀 이름을 적어 주세요", cls: "tm-plain-title" });
    name.setAttribute("aria-label", "팀 이름");
    const at = el("span", "tm-at tm-pop-tag"); at.append(el("span", "", "@"));
    const tag = inline(pop, () => d.handle || (pop.id ? "" : handleFrom(d.name)), (v) => { pop.handleTouched = true; d.handle = v.replace(/^@/, ""); warn.hidden = !takenHandle(pop); }, { max: 32, placeholder: "팀이름", cls: "tm-plain-tag" });
    tag.setAttribute("aria-label", "@이름");
    const copy = el("button", "icon-btn plain tm-copy"); copy.type = "button"; copy.title = "@이름 복사"; copy.setAttribute("aria-label", "@이름 복사"); copy.append(icon("copy"));
    copy.addEventListener("click", () => { navigator.clipboard?.writeText(`@${d.handle || handleFrom(d.name)}`).then(() => notice("복사했어요."), () => {}); });
    at.append(tag, copy);
    line.append(name, at);
    const warn = el("small", "tm-warn", "이미 쓰는 이름이에요"); warn.hidden = !takenHandle(pop);
    const purpose = inline(pop, () => d.role, (v) => { d.role = v; }, { max: 200, placeholder: "무엇을 하는 팀인가요? 목적을 한 줄로 적어요", cls: "tm-plain-sub" });
    purpose.setAttribute("aria-label", "목적");
    words.append(row, line, warn, purpose, ...(credit ? [credit] : []));
    dlg.querySelector(".crew-popup-who").replaceChildren(card, words);
  }

  // 1회 예상 (끝쪽): tokens · cost · minutes, computed again whenever members, mode or ending change
  function paintEst(pop) {
    const e = pop.est;
    pop.estBox.replaceChildren(...[["예상 토큰", e ? `약 ${num(e.tokens)}` : "—"], ["예상 비용", e ? `약 ${usd(e.cost_usd)}` : "—"], ["예상 시간", e ? `${e.minutes_min}~${e.minutes_max}분` : "—"]].map(([k, v]) => {
      const r = el("div", ""); r.append(el("span", "", k), el("i"), el("b", "", v)); return r;
    }));
    pop.estBox.title = "한 번 회의할 때 · 공시 단가로 계산한 추정";
  }
  function queueEst(pop) {
    const d = pop.d, key = JSON.stringify([d.members, d.mode, d.ending]);
    paintEst(pop);
    if (key === pop.estKey) return;
    pop.estKey = key;
    clearTimeout(pop.estTimer);
    if (!(d.members || []).length) { pop.est = null; paintEst(pop); return; }
    pop.estTimer = setTimeout(async () => {
      try { const r = await request("/v1/crew/teams/estimate", { method: "POST", body: body({ team: d }) }); if (pop.estKey === key) pop.est = r && r.tokens != null ? r : null; } catch { pop.est = null; }
      if (S.pop === pop) paintEst(pop);
    }, 500);
  }

  // ---------------------------------------------------------------- 개요 (편집 포함)
  function sec(iconName, title, hint, ...heads) {
    const s = el("section", "tm-sec");
    const h = el("div", "tm-sec-head");
    const t = el("h3", ""); t.append(icon(iconName), el("span", "", title));
    h.append(t, ...heads);
    if (hint) h.append(el("small", "", hint));
    s.append(h);
    return s;
  }
  function overviewTab(pop) {
    const d = pop.d, rec = pop.rec;
    const out = [];
    if (rec) out.push(kpis(pop));
    out.push(aiSection(pop));
    const mode = sec("tree-structure", "회의 방식", "누가 언제 말하고, 언제 끝낼지 정해요");
    mode.append(modeCard(d, (s) => changed(pop, s), null, null, true));
    out.push(mode);
    const mem = sec("users-three", "전문가", d.mode === "moderator_picks" ? "진행자가 매번 다음 사람을 골라요" : "위에서부터 순서대로 말해요 · 끌어서 순서를 바꿔요");
    const mc = membersCard(d, (s) => changed(pop, s), true);
    mem.querySelector(".tm-sec-head > small").before(...mc.heads);
    mem.append(...mc.childNodes);
    out.push(mem);
    if (rec) out.push(templatesSection(pop));
    out.push(contextSection(pop));
    if (rec) { const pc = pins?.card({ rec, draft: d, cls: "tm-card", sub: tr(`회의를 열 때마다 ${d.name || rec.crew.name}에게 먼저 넣어요. 위에 있을수록 먼저 들어가요.`), noun: tr("이 팀은"), onChange: () => changed(pop) }); if (pc) out.push(pc); }
    out.push(channelsSection(pop));
    if (!rec) out.push(fromSection(pop), templatesSection(pop)); // new team: these sit at the bottom (Figma 310:19981 has no slot for them)
    if (rec) out.push(dangerRow(pop));
    pop.bar = el("div", "tm-bar2"); out.push(pop.bar);
    updateBar(pop);
    return out;
  }
  function kpis(pop) {
    const st = S.stats.get(pop.id), none = !st || st.error;
    const box = el("div", "tm-kpis tm-kpis2");
    const k = (label, value, unit, sub, mark, series) => {
      const c = el("div", "tm-kpi"); const l = el("div", "tm-kpi-l");
      const v = el("div", "tm-kpi-value"); v.append(el("strong", "", value)); if (unit) v.append(el("span", "", unit)); if (mark) v.append(est());
      l.append(v, el("small", "", sub)); c.append(l, series); return c;
    };
    box.append(k("최근 30일", none ? "—" : String(st.meetings_done || 0), "회", "최근 30일 회의", false, bars(pop, () => 1)),
      k("사용량", none ? "—" : num(st.tokens), "", "전체 토큰", false, bars(pop, (m) => m.tokens_used || 0)),
      k("추정 비용", none ? "—" : usd(st.cost_usd), "", "추정 비용", true, bars(pop, (m) => m.cost_usd || 0)));
    return box;
  }
  // AI로 고치기 · AI로 초안 (ai-chat.js): the same button → bubble → purple list as the expert window; nothing lands before 저장.
  function aiSection(pop) {
    const mine = !!pop.id;
    pop.aiTalk ||= {};
    const s = el("section", "tm-sec tm-ai");
    s.append(window.ArkismAIChat.button({ state: pop.aiTalk, label: mine ? "AI 제안" : "AI로 초안", title: mine ? "AI 제안" : "AI로 초안 만들기",
      sub: mine ? "고칠 곳을 말하면 바뀌는 곳을 보여 줘요" : "어떤 팀인지 말하면 초안을 채워요",
      greeting: mine ? "이 팀의 무엇을 고칠까요? 멤버·회의 방식·라운드를 바꿔 볼 수 있어요." : "어떤 팀이 필요하세요? 무엇을 회의할 팀인지 말해 주세요.",
      placeholder: mine ? "예: 접근성 검토자를 넣고 라운드를 3번으로 줄여 줘" : "예: 시안을 여러 관점에서 보고 고칠 목록을 만드는 팀",
      quick: mine ? ["리뷰어를 한 명 더", "라운드를 줄여 줘", "진행자가 지명하게"] : ["코드 리뷰 팀", "시안 검토 팀", "자료 조사 팀"],
      ask: (text) => runAI(pop, text) }));
    pop.aiRoot = s.firstChild; // its button goes to the right end of the bottom bar (updateBar)
    return s;
  }
  async function runAI(pop, text) {
    kit = kit || window.ArkismAssist?.(ctx);
    if (!kit) throw new Error(tr("AI 도움을 쓸 수 없어요."));
    const d = pop.d;
    const roster = agents().map((a) => ({ id: a.id, name: a.crew.name, about: a.crew.role || a.crew.category || "" }));
    const cur = { name: d.name, role: d.role, mode: d.mode, max_rounds: d.ending?.max_rounds, token_cap: d.ending?.token_cap, members: sorted(d.members).map((m) => ({ agent_id: m.agent_id, role: m.role, focus: m.focus })) };
    const prompt = `Request: ${text}\nCurrent team: ${JSON.stringify(cur)}\nExperts you may use: ${JSON.stringify(roster)}\nAnswer with ONE JSON object holding only the keys that change: name, role (one line), mode ("round_robin" or "moderator_picks"), max_rounds (1-10), token_cap, members ([{agent_id, role: "moderator"|"worker"|"reviewer"|"scribe", focus}] in speaking order; agent_id only from the experts list). Write name and role in the language of the request.`;
    const a = await kit.assist("json", prompt, { persona: { crew: { instructions: "You set up small teams of experts that hold meetings. Reply with the JSON object only." } } });
    if (a.status !== "done") throw new Error(kit.assistError(a));
    const j = kit.parseJSON(a.output);
    if (!j || typeof j !== "object" || Array.isArray(j)) throw new Error(tr("AI의 답을 읽지 못했어요. 다시 해 보세요."));
    // Each change is one proposal; 적용 writes it into the draft on screen (pop.d may be a fresh copy after 취소).
    const props = [];
    const add = (label, from, to, set, show = (v) => v) => { if (to !== undefined && !same(from, to)) props.push({ label, from: show(from), to: show(to), apply: () => { set(to); changed(pop, true); }, undo: () => { set(from); changed(pop, true); } }); };
    const end = (k) => (v) => { pop.d.ending = { ...(pop.d.ending || {}), [k]: v }; };
    if (typeof j.name === "string" && j.name.trim()) add(tr("이름"), d.name || "", j.name.trim().slice(0, 60), (v) => { pop.d.name = v; if (!pop.handleTouched) pop.d.handle = handleFrom(v); paintHead(pop); });
    if (typeof j.role === "string") add(tr("설명"), d.role || "", j.role.trim().slice(0, 200), (v) => { pop.d.role = v; paintHead(pop); });
    if (MODES.some(([k]) => k === j.mode)) add(tr("회의 방식"), d.mode || "round_robin", j.mode, (v) => { pop.d.mode = v; }, modeName);
    if (Number.isFinite(+j.max_rounds) && +j.max_rounds >= 1) add(tr("최대 라운드"), d.ending?.max_rounds ?? 4, Math.min(10, Math.round(+j.max_rounds)), end("max_rounds"), String);
    if (Number.isFinite(+j.token_cap) && +j.token_cap > 0) add(tr("토큰 상한"), d.ending?.token_cap ?? 100000, Math.round(+j.token_cap), end("token_cap"), (v) => Number(v).toLocaleString("en-US"));
    let dropped = [];
    if (Array.isArray(j.members)) {
      const ok = j.members.filter((m) => agents().some((x) => x.id === m?.agent_id));
      dropped = j.members.filter((m) => !ok.includes(m)).map((m) => String(m?.name || m?.agent_id || "").slice(0, 40)).filter(Boolean);
      const next = ok.map((m, i) => ({ agent_id: m.agent_id, order: i + 1, role: ROLES.some(([k]) => k === m.role) ? m.role : i ? "worker" : "moderator", focus: String(m.focus || "").slice(0, 120) }));
      const label = (list) => sorted(list || []).map((m) => `${memberName(m)} · ${roleName(m.role)}`);
      if (next.length && !same(label(d.members), label(next))) props.push({ label: tr("멤버"), from: label(d.members), to: label(next), apply: () => { pop.d.members = clone(next); changed(pop, true); }, undo: ((was) => () => { pop.d.members = was; changed(pop, true); })(clone(d.members || [])) });
    }
    // the AI may name experts that do not exist; they are left out, and the person is told which
    const gone = dropped.length ? ` ${tr("없는 전문가는 뺐어요")}: ${dropped.join(", ")}` : "";
    return { say: (props.length ? `${props.map((p) => p.label).join("·")}을 바꾸자고 제안해요. 보라색 목록에서 보고 적용하세요.` : tr("바꿀 곳을 찾지 못했어요. 말을 바꿔 다시 물어봐 주세요.")) + gone, props };
  }
  // 비슷한 팀에서 시작 (new team only)
  function fromSection(pop) {
    const s = sec("copy", "비슷한 팀에서 시작");
    const chips = el("div", "tm-chips");
    for (const [id, label] of [["", "빈 팀"], ...teams().map((t) => [t.id, t.crew.name])]) {
      const c = el("button", `tm-chip${pop.from === id ? " active" : ""}`, label); c.type = "button"; if (id) raw(c);
      c.addEventListener("click", () => {
        const src = byId(id), keep = { name: pop.d.name, role: pop.d.role, handle: pop.d.handle };
        pop.d = src ? { ...blankTeam(), ...clone(src.crew), ...keep, discord_mirror: false } : { ...blankTeam(), ...keep };
        delete pop.d.cover; delete pop.d.pinned;
        pop.from = id; paintHead(pop); changed(pop, true);
      });
      chips.append(c);
    }
    s.append(chips);
    return s;
  }
  function templatesSection(pop) {
    const d = pop.d;
    const s = sec("chat-text", "회의 템플릿", "자주 여는 회의의 진행 방법을 저장해 두고, 회의를 열 때 골라요");
    const chips = el("div", "tm-chips");
    (d.templates || []).forEach((t) => {
      const c = raw(el("button", "tm-chip", t.name || tr("이름 없음"))); c.type = "button";
      c.addEventListener("click", () => templateDialog(pop, t));
      chips.append(c);
    });
    const add = el("button", "tm-chip add", (d.templates || []).length ? "+ 추가" : "+ 템플릿 만들기"); add.type = "button";
    add.addEventListener("click", () => templateDialog(pop, null));
    chips.append(add);
    s.append(chips);
    return s;
  }
  // 템플릿 창: its own dialog over the team window. Edits a copy; 저장 puts it in the team draft (saved with the team).
  // What a template holds is what the engine reads (crewrun meeting.go): prompt goes into the scribe's brief as
  // "이 회의의 방식" under the topic typed when the meeting opens; overrides.mode and overrides.ending replace the team's.
  function templateDialog(pop, src) {
    const d = pop.d;
    const t = src ? clone(src) : { key: `t${Date.now().toString(36)}`, name: "", prompt: "", overrides: {} };
    t.overrides = t.overrides || {};
    const start = JSON.stringify(t);
    const ai = {}, promptTalk = {}; // the AI talks (ai-chat.js state), kept while this window is open
    const dlg = el("dialog", "crew-dialog tm-tpl-dlg");
    const head = el("div", "dialog-head");
    const hw = el("div", "");
    hw.append(el("h2", "", src ? "회의 템플릿 고치기" : "새 회의 템플릿"), el("p", "", "회의를 열 때 이 템플릿을 고르면, 아래 내용이 그 회의에만 들어가요."));
    const x = el("button", "icon-btn plain"); x.type = "button"; x.setAttribute("aria-label", "닫기"); x.append(icon("x"));
    head.append(hw, x);
    const body = el("div", "dialog-body tm-tpl-body");
    const foot = el("div", "dialog-foot");
    const dirty = () => JSON.stringify(t) !== start;
    const leave = async () => { if (!dirty() || await ask("저장하지 않은 변경이 있어요. 버리고 닫을까요?")) dlg.close(); };
    x.addEventListener("click", () => void leave());
    dlg.addEventListener("cancel", (e) => { e.preventDefault(); void leave(); });
    const ok = button("저장", "primary", null, () => {
      t.name = t.name.trim();
      if (!t.name) { window.ArkismNeed(dlg.querySelector(".tm-tpl-body input"), "템플릿 이름을 적어 주세요."); return; }
      if (!t.overrides.mode && !t.overrides.ending) delete t.overrides;
      const list = d.templates || [];
      d.templates = src ? list.map((y) => (y.key === src.key ? t : y)) : [...list, t];
      dlg.close(); changed(pop, true);
      notice("템플릿을 넣었어요. 팀을 저장하면 반영돼요.");
    });
    const sync = () => {}; // 저장 stays pressable; an empty name is pointed at on press (field-need.js)
    const help = (label, what, ...kids) => { const f = el("div", "tm-tpl-field"); const h = el("div", "tm-tpl-label"); h.append(el("b", "", label), el("small", "", what)); f.append(h, ...kids); return f; };
    const draw = () => {
      const name = raw(input(t.name, (v) => { t.name = v; sync(); }, { max: 60, placeholder: "예: 코드 리뷰, 주간 회고, 시안 검토" }));
      name.setAttribute("aria-label", "템플릿 이름");
      const prompt = raw(input(t.prompt, (v) => { t.prompt = v; }, { tag: "textarea", rows: 6, max: 4000, placeholder: "예:\n- 바뀐 화면을 접근성·디자인 토큰 기준으로 봐요\n- 한 사람이 문제를 3개까지 말해요\n- 끝에 고칠 목록을 중요한 순서로 정리해요" }));
      prompt.setAttribute("aria-label", "이 회의의 진행 방법");
      const promptAI = window.ArkismAIChat.field(prompt, { state: promptTalk, title: "진행 방법 다듬기", preset: "You write the 'how this meeting goes' part of a meeting template for a team of AI experts: steps, what to check, the shape of the result. Korean Markdown list, at most 1500 characters, no topic placeholder (the topic is typed when the meeting opens).",
        context: () => JSON.stringify({ template: t.name, team: d.name, members: sorted(d.members).map((m) => ({ name: memberName(m), role: m.role })) }) });
      // where each part comes from when the meeting starts (the scribe's brief, meeting.go brief())
      const flow = el("ol", "tm-tpl-flow");
      for (const [k, v, on] of [["회의 주제", "회의를 열 때마다 적어요", false], ["이 템플릿의 진행 방법", "여기 적은 글", true], ["팀 자료", "팀에 넣어 둔 자료", false]]) {
        const li = el("li", on ? "on" : ""); li.append(el("b", "", k), el("span", "", v)); flow.append(li);
      }
      const o = t.overrides;
      const modeSel = select([["", "팀 설정대로"], ...MODES.map(([k, l]) => [k, l])], o.mode || "", (v) => { if (v) o.mode = v; else delete o.mode; draw(); });
      modeSel.setAttribute("aria-label", "회의 방식");
      const endRow = el("div", "tm-toggle");
      endRow.append(el("span", "", "끝나는 조건을 이 템플릿만 따로 정하기"), sw(!!o.ending, "끝나는 조건 따로 정하기", (v) => { if (v) o.ending = { max_rounds: 4, token_cap: 100000, end_on_all_approved: true, end_on_moderator: true, ...(d.ending || {}) }; else delete o.ending; draw(); }));
      const over = el("div", "tm-tpl-over");
      over.append(field("회의 방식", modeSel), endRow);
      if (o.ending) {
        const e = o.ending;
        const r = input(e.max_rounds, (v) => { e.max_rounds = Math.max(1, Math.min(10, Number(v) || 1)); }, { type: "number" }); r.min = 1; r.max = 10;
        const cap = input(Number(e.token_cap || 0).toLocaleString("en-US"), (v) => { e.token_cap = Math.min(5_000_000, Number(String(v).replace(/[^\d]/g, "")) || 0); }); cap.inputMode = "numeric"; cap.classList.add("mono");
        const g = el("div", "tm-two"); g.append(field("최대 라운드 (1~10)", r), field("토큰 상한", cap));
        over.append(g);
        for (const [k, label] of [["end_on_all_approved", "리뷰어가 모두 승인하면 끝"], ["end_on_moderator", "진행자가 끝내면 끝"]]) {
          const row = el("div", "tm-toggle"); row.append(el("span", "", label), sw(!!e[k], label, (v) => { e[k] = v; draw(); })); over.append(row);
        }
      }
      const aiRoot = aiBox();
      foot.querySelector(".ex-ai-dock")?.remove();
      foot.append(aiRoot.dock); // AI 제안 at the right end of the foot (CSS order), its purple list in the body
      body.replaceChildren(aiRoot,
        help("이름", "회의를 열 때 목록에서 고르는 이름이에요", name),
        help("이 회의의 진행 방법", "주제는 회의를 열 때 따로 적어요. 여기에는 매번 같은 진행 순서·볼 기준·결과 모양을 적어요.", promptAI, el("small", "tm-tpl-cap", "회의가 시작되면 서기가 이 순서로 읽고 브리핑해요"), flow),
        help("이 템플릿만 바꿀 설정", "비워 두면 팀 설정대로 해요. 이 템플릿으로 연 회의에서만 바뀌어요.", over));
      sync();
    };
    // AI 제안 (ai-chat.js): the same button → bubble → purple list as the expert window; nothing lands before 저장.
    function aiBox() {
      return window.ArkismAIChat.button({ state: ai, label: "AI 제안", title: "AI 제안", sub: "어떤 회의인지 말하면 이름·진행 방법을 채워 드려요",
        greeting: "어떤 회의에 쓸 템플릿인가요? 무엇을 정하고, 결과를 어떤 모양으로 받고 싶은지 말해 주세요.",
        placeholder: "예: PR을 올리기 전에 보안·테스트를 점검하는 회의", quick: ["코드 리뷰 회의", "주간 회고", "시안 검토 후 고칠 목록"], ask: propose });
    }
    const modeName = (k) => MODES.find(([m]) => m === k)?.[1] || tr("팀 설정대로");
    async function propose(text) {
      kit = kit || window.ArkismAssist?.(ctx);
      if (!kit) throw new Error(tr("AI 도움을 쓸 수 없어요."));
      const cur = { name: t.name, prompt: t.prompt, mode: t.overrides.mode || "", max_rounds: t.overrides.ending?.max_rounds };
      const team = { name: d.name, role: d.role, mode: d.mode, members: sorted(d.members).map((m) => ({ name: memberName(m), role: m.role, focus: m.focus })) };
      const prompt = `Request: ${text}\nTeam: ${JSON.stringify(team)}\nCurrent meeting template: ${JSON.stringify(cur)}\nA template is picked when a meeting opens. The topic is typed then, separately; the template's prompt is read with it as "how this meeting goes": steps, what to check, the shape of the result. Answer with ONE JSON object holding only the keys that change: name (<=60 chars), prompt (Korean Markdown list, <=1500 chars, no topic placeholder), mode ("round_robin" or "moderator_picks", only if it clearly fits better than the team's), max_rounds (1-10). Write in the language of the request.`;
      const a = await kit.assist("json", prompt, { persona: { crew: { instructions: "You write meeting templates for small teams of AI experts. Reply with the JSON object only." } } });
      if (a.status !== "done") throw new Error(kit.assistError(a));
      const j = kit.parseJSON(a.output || "");
      if (!j || typeof j !== "object" || Array.isArray(j)) throw new Error(tr("AI의 답을 읽지 못했어요. 다시 해 보세요."));
      const props = [];
      const add = (key, label, from, to, show = (v) => v, back = () => setT(key, from)) => { if (to !== undefined && to !== from) props.push({ label, from: show(from), to: show(to), apply: () => { setT(key, to); draw(); }, undo: () => { back(); draw(); } }); };
      if (typeof j.name === "string" && j.name.trim()) add("name", tr("이름"), t.name, j.name.trim().slice(0, 60));
      if (typeof j.prompt === "string" && j.prompt.trim()) add("prompt", tr("진행 방법"), t.prompt, j.prompt.trim().slice(0, 4000));
      if (MODES.some(([k]) => k === j.mode)) add("mode", tr("회의 방식"), t.overrides.mode || "", j.mode, modeName);
      const own = t.overrides.ending; // undo puts this back, so a template that followed the team's ending follows it again
      if (Number.isFinite(+j.max_rounds) && +j.max_rounds >= 1) add("max_rounds", tr("최대 라운드"), own?.max_rounds ?? d.ending?.max_rounds ?? 4, Math.min(10, Math.round(+j.max_rounds)), String,
        () => { if (own) t.overrides.ending = own; else delete t.overrides.ending; });
      return { say: props.length ? `${props.map((p) => p.label).join("·")}을 제안해요. 보라색 목록에서 보고 적용하세요.` : tr("바꿀 곳을 찾지 못했어요. 말을 바꿔 다시 물어봐 주세요."), props };
    }
    function setT(key, v) {
      if (key === "name" || key === "prompt") t[key] = v;
      else if (key === "mode") { if (v) t.overrides.mode = v; else delete t.overrides.mode; }
      else { t.overrides.ending = { token_cap: 100000, end_on_all_approved: true, end_on_moderator: true, ...(d.ending || {}), ...(t.overrides.ending || {}), max_rounds: v }; }
    }
    if (src) foot.append(link("이 템플릿 지우기", async () => {
      if (!await ask(`'${src.name || tr("이름 없음")}' 템플릿을 지울까요? 팀을 저장하면 반영돼요.`)) return;
      d.templates = (d.templates || []).filter((y) => y.key !== src.key); dlg.close(); changed(pop, true);
    }, "tm-link quiet tm-tpl-del"));
    foot.append(button("취소", "ghost", null, () => void leave()), ok);
    dlg.append(head, body, foot);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg);
    draw();
    dlg.showModal();
    if (!src) body.querySelector("input")?.focus();
  }
  function contextSection(pop) {
    const d = pop.d;
    const add = S.ctxAdding ? null : link("+ 추가", () => { S.ctxAdding = true; popRedraw("overview"); });
    const s = sec("file-text", "팀 자료", "회의 내내 모든 멤버가 보는 자료예요", ...(add ? [add] : []));
    const list = el("div", "tm-ctx-list");
    (d.context || []).forEach((x) => {
      const r = el("div", "tm-ctx-row");
      const pill = el("button", "tm-badge line", x.share === "shared" ? "다른 팀과 공유" : "팀 전용"); pill.type = "button"; pill.title = "누르면 바뀌어요";
      pill.addEventListener("click", () => { x.share = x.share === "shared" ? "team" : "shared"; changed(pop, true); });
      const del = el("button", "icon-btn plain tm-x"); del.type = "button"; del.setAttribute("aria-label", "빼기"); del.append(icon("x"));
      del.addEventListener("click", () => { d.context = d.context.filter((y) => y !== x); changed(pop, true); });
      r.append(raw(el("span", "", x.title || x.id)), pill, del);
      list.append(r);
    });
    if (!(d.context || []).length) list.append(empty("아직 없어요. 회의 내내 모두가 볼 자료를 넣어 주세요."));
    s.append(list);
    if (S.ctxAdding) s.append(contextAdder(d, (st) => changed(pop, st)));
    return s;
  }
  // 채널 연결 (961:60688): the Discord channels whose action is this team's meeting; same bot API as the expert card (crew.js channelCard).
  async function saveTeamBot(pop, b, patch, okText) { await saveBot(b, patch, okText); chRedraw(pop); }
  // Channel edits swap only this section, so the popup never re-renders and the scroll stays where the click was.
  function chRedraw(pop) {
    if (pop?.chSec?.isConnected) pop.chSec.replaceWith(channelsSection(pop));
    else popRedraw("overview");
  }
  function channelsSection(pop) {
    const d = pop.d, mine = !!pop.id;
    const add = mine ? link("+ 채널 연결", () => { pop.chAdd = pop.chAdd ? null : { bot: bots()[0]?.id || "", channel: "" }; chRedraw(pop); }) : null;
    const s = pop.chSec = sec("chat-text", "채널 연결", "어디서 이 팀을 부를지 정해요 · 봇 설정은 설정 › 메신저에서 연결해요", ...(add ? [add] : []));
    const list = el("div", "tm-ctx-list");
    const ch = mine ? channelsFor(pop.id) : [];
    for (const x of ch) {
      const b = x.bot, warn = ["needs_permission", "token_invalid"].includes(b.state);
      const r = el("div", "tm-ctx-row");
      const mode = el("button", "tm-badge tm-ch-mode", x.auto_start ? tr("글이 올라오면 자동 시작") : tr(`@${d.handle || handleFrom(d.name)}로 부를 때만`)); mode.type = "button"; mode.title = tr("눌러서 바꿔요");
      raw(mode);
      mode.addEventListener("click", () => saveTeamBot(pop, b, { channels: b.channels.map((c) => (c.channel_id === x.channel_id ? { ...c, auto_start: !c.auto_start } : c)) }, "바꿨어요."));
      const del = el("button", "icon-btn plain tm-x"); del.type = "button"; del.setAttribute("aria-label", "빼기"); del.append(icon("x"));
      del.addEventListener("click", async () => { if (await ask(tr(`#${x.name || x.channel_id} 연결을 뺄까요?`))) saveTeamBot(pop, b, { channels: b.channels.filter((c) => c.channel_id !== x.channel_id) }, "연결을 뺐어요.").catch(() => {}); });
      // the channel name opens the same picker in place, so the Discord channel changes right here
      const name = el("button", "tm-ch-name", `디스코드 · #${x.name || x.channel_id}`); name.type = "button"; name.title = tr("눌러서 채널을 바꿔요");
      raw(name);
      name.addEventListener("click", () => { pop.chAdd = { bot: b.id, channel: x.channel_id, edit: x.channel_id }; chRedraw(pop); });
      r.append(name, raw(el("small", "", b.name || "")), ...(warn ? [badge("확인 필요", "warn")] : []), mode, del);
      list.append(r);
      if (pop.chAdd?.edit === x.channel_id && pop.chAdd.bot === b.id) list.append(channelForm(pop));
    }
    if (!ch.length) list.append(empty(!mine ? "팀을 만든 뒤 채널을 연결해요." : S.discord ? (bots().length ? "연결된 채널이 없어요." : "연결된 디스코드 봇이 없어요.") : "불러오는 중…"));
    const mirror = el("div", "tm-ctx-row");
    mirror.append(el("span", "", "회의 진행을 디스코드 스레드에도 올리기"), sw(d.discord_mirror, "디스코드에도 올리기", (v) => { d.discord_mirror = v; changed(pop, true); }));
    list.append(mirror);
    s.append(list);
    if (mine && S.discord) {
      // 봇 쓰는 방식 (한 봇을 나눠 쓰기 / 팀마다 봇) moved here from the old 디스코드 screen; same chooser and save.
      const modeBox = el("div", "tm-side-sec");
      modeBox.append(sideLabel("봇 쓰는 방식"), modeChooser(S.discord.settings?.bot_mode || "shared", (k) => { void saveSettings({ bot_mode: k }).then((ok) => { if (ok) chRedraw(pop); }); }));
      s.append(modeBox);
    }
    if (mine && pop.chAdd && !pop.chAdd.edit) s.append(channelForm(pop));
    if (mine) {
      const links = el("div", "tm-ch-links");
      if (S.discord && (S.discord.settings?.bot_mode || "shared") === "per_target") links.append(link("이 팀 전용 봇 연결하기 →", () => { closePopup(); startConnect(null, { mode: "per_target", target: `meeting:${pop.id}` }); }));
      links.append(link("설정 › 메신저에서 디스코드 연결 보기 →", () => { closePopup(); state.settingsTab = "messenger"; go("settings"); }));
      s.append(links);
    }
    return s;
  }
  function channelForm(pop) {
    const f = pop.chAdd;
    if (!bots().length) return empty("먼저 디스코드 봇을 추가해 주세요.");
    else {
      const b = botById(f.bot) || bots()[0];
      void loadChannels(b);
      const known = S.channels.get(b.id);
      const row = el("div", "tm-ch-add");
      const botSel = select(bots().map((x) => [x.id, x.name || x.id]), b.id, (v) => { f.bot = v; f.channel = ""; chRedraw(pop); }); botSel.setAttribute("aria-label", "봇"); botSel.disabled = !!f.edit; // changing a link keeps its bot: moving it to another bot would leave the old link behind
      const chSel = select([["", known ? "채널 고르기" : "채널 불러오는 중…"], ...(known || []).map((c) => [c.channel_id, `#${c.name}${c.parent ? ` · ${c.parent}` : ""}`])], f.channel, (v) => { f.channel = v; }); chSel.setAttribute("aria-label", "채널");
      row.append(botSel, chSel, button(f.edit ? "바꾸기" : "연결", "primary", null, async () => {
        const id = f.channel;
        if (!id) { window.ArkismNeed(chSel, "채널을 골라 주세요."); return; }
        const had = id !== f.edit && (b.channels || []).find((x) => x.channel_id === id);
        if (had && !await ask(tr("이 채널은 이미 다른 곳에 연결돼 있어요. 이 팀으로 바꿀까요?"))) return;
        const old = f.edit ? (b.channels || []).find((x) => x.channel_id === f.edit) : null;
        const c = { ...(old || {}), channel_id: id, name: (known || []).find((x) => x.channel_id === id)?.name || "", action: { kind: "meeting", id: pop.id }, auto_start: !!old?.auto_start };
        pop.chAdd = null;
        void saveTeamBot(pop, b, { channels: [...(b.channels || []).filter((x) => x.channel_id !== id && x.channel_id !== f.edit), c] }, f.edit ? "채널을 바꿨어요." : "채널을 연결했어요.");
      }), link("취소", () => { pop.chAdd = null; chRedraw(pop); }, "tm-link quiet"));
      return row;
    }
  }
  function updateBar(pop) {
    if (!pop.bar) return; // filled before it is mounted: overviewTab builds it off-DOM
    const diff = pdiff(pop), d = pop.d;
    pop.bar.replaceChildren();
    const left = el("div", "tm-bar2-acts");
    const ok = button(pop.id ? "저장" : "팀 만들기", "primary", null, () => savePopup(pop));
    ok.disabled = !!pop.id && !diff.length; // the bar stays: AI 제안 lives at its right end
    left.append(ok, button("취소", "ghost", null, () => {
      if (pop.id) { pop.d = clone(pop.base); pop.handleTouched = true; pop.aiTalk = null; paintHead(pop); changed(pop, true); } else closePopup();
    }));
    if (pop.id) left.append(el("small", "", `바뀐 칸 ${diff.length}개`));
    const hint = pop.id ? `저장하면 다음 판 v${(pop.rec.revision || 1) + 1} · 기록 탭에서 되돌릴 수 있어요`
      : `${(d.members || []).length}명 · ${d.mode === "round_robin" ? "정해진 순서" : "진행자 지명"} · 최대 ${d.ending?.max_rounds || 4}라운드`;
    left.append(el("small", "", hint));
    pop.bar.append(left);
    if (pop.aiRoot?.dock) pop.bar.append(pop.aiRoot.dock);
  }
  async function savePopup(pop, replace) {
    const d = replace || pop.d;
    const bad = validTeam(d) || (takenHandle(pop) ? "이미 쓰는 이름이에요. 다른 @이름을 써 주세요." : "");
    if (bad) { notice(bad); return; }
    d.handle = d.handle || handleFrom(d.name);
    try {
      if (!pop.id) {
        const saved = await request("/v1/crew", { method: "POST", body: body({ crew: d }) });
        await reloadCrew().catch(() => replaceRecord(saved));
        pop.id = saved?.id; pop.rec = byId(pop.id) || saved; pop.base = clone(d); pop.d = clone(d); pop.handleTouched = true;
        pop.h.dialog.classList.remove("is-new");
        if (pop.id) { loadStats(pop.id); void loadMeetingsOf(pop); void loadVersions(pop); }
        notice("팀을 만들었어요.");
      } else {
        const saved = await request(`/v1/crew/${encodeURIComponent(pop.id)}`, { method: "POST", body: body({ expected_revision: pop.rec.revision, crew: d }) });
        const rec = saved?.id ? saved : { ...pop.rec, revision: (pop.rec.revision || 0) + 1, crew: clone(d) };
        replaceRecord(rec);
        pop.rec = byId(pop.id) || rec; pop.base = clone(d); pop.d = clone(d);
        void loadVersions(pop);
        notice("저장했어요.");
      }
      pop.aiTalk = null;
      if (here("teams")) render(false);
      paintHead(pop); popRedraw("overview"); popRedraw("history");
    } catch (e) { notice(errorText(e)); }
  }
  // 팀 삭제: last thing in 개요 like 전문가 삭제 (crew.js dangerRow); the name is typed back. Past meetings stay.
  function dangerRow(pop) {
    const row = el("div", "ex-danger"), t = el("div", "");
    t.append(el("strong", "", "팀 삭제"), el("small", "", "이 팀과 설정을 지워요. 지난 회의 기록은 남고, 되돌릴 수 없어요."));
    row.append(t, button("팀 삭제", "danger small", "trash", () => removeTeam(pop)));
    return row;
  }
  async function removeTeam(pop) {
    const rec = pop.rec;
    if (!await window.askTyped(`'${rec.crew.name}' 팀을 삭제할까요?\n되돌릴 수 없어요. 확인하려면 이름 '${rec.crew.name}'을 그대로 입력하세요.`, rec.crew.name, "삭제")) return;
    try {
      await request(`/v1/crew/${encodeURIComponent(rec.id)}`, { method: "DELETE", body: JSON.stringify({ scope: state.scope, expected_revision: rec.revision }) });
      state.data.crew = items().filter((r) => r.id !== rec.id);
      pop.base = clone(pop.d); // nothing left to lose
      closePopup();
      notice("지웠어요.");
      if (here("teams")) render(false);
    } catch (e) { notice(errorText(e)); }
  }

  // ---------------------------------------------------------------- 테스트 (981:59766): a real meeting from the saved team
  function testTab(pop) {
    const t = pop.test;
    if (!pop.id) return [empty("팀을 만든 뒤 여기서 회의를 열어 볼 수 있어요.")];
    const note = el("p", "tm-test-note", "여기서 연 회의도 실제 실행이에요. 회의 기록은 이번 달 한도에 남아요." + (pdiff(pop).length ? " 저장하지 않은 변경은 반영되지 않아요." : ""));
    const log = el("div", "tm-test-log");
    if (t.m) log.append(testLog(pop, t.m)); else log.append(empty("아래에 맡길 일을 적고 회의를 시작해 보세요."));
    const form = el("form", "tm-test-form");
    const i = el("input", "tm-input"); i.maxLength = 4000; i.value = t.text; i.placeholder = `$${pop.d.handle || handleFrom(pop.d.name)}에게 맡길 일…`;
    i.addEventListener("input", () => { t.text = i.value; });
    const live = !!t.m && LIVE.has(t.m.status);
    const go2 = el("button", "btn primary", live ? "회의 중…" : "회의 시작"); go2.type = "submit"; go2.disabled = live || t.busy;
    form.addEventListener("submit", (e) => { e.preventDefault(); void startTest(pop); });
    form.append(i, go2, link("회의 기록 보기", () => { closePopup(); S.meetTab = "running"; go("meetings"); }, "tm-link quiet"));
    return [note, log, form];
  }
  function testLog(pop, m) {
    const box = el("div", "tm-test-thread");
    const q = el("div", "tm-bubble"); q.append(raw(el("span", "", m.topic || m.title)));
    const a = el("div", "tm-reply");
    const hd = el("div", "tm-reply-head");
    const pic = el("span", "tm-reply-pic"); if (pop.d.cover) { const img = el("img"); img.alt = ""; img.src = coverSrc(pop.d.cover); pic.append(img); }
    hd.append(pic, raw(el("b", "", pop.d.name)), raw(el("small", "", `${tr(modeName(pop.d.mode))} · ${tr(`라운드 ${m.round || 0}/${m.max_rounds || "?"}`)}`)));
    const st = el("div", "tm-steps");
    const turns = m.turns || [];
    for (const x of sorted(membersOf(m))) {
      const name = memberName(x);
      const speaking = m.status === "running" && (m.speaking === x.agent_id || m.speaking === name);
      const done = turns.some((t) => t.agent_id === x.agent_id && t.ok !== false && t.kind !== "human");
      const r = el("div", `tm-step2${speaking ? " now" : done ? " done" : ""}`);
      r.append(el("i", "", done && !speaking ? "✓" : ""), raw(el("span", "", `${name} · ${tr(roleName(x.role))}`)), el("small", "", speaking ? "말하는 중…" : done ? "마침" : "대기"));
      st.append(r);
    }
    const last = [...turns].reverse().find((t) => t.kind !== "conclusion" && t.kind !== "human" && t.ok !== false && t.text);
    a.append(hd, st);
    if (last) a.append(raw(el("p", "tm-reply-text", last.text.slice(0, 1200))));
    if (m.conclusion) a.append(conclusionCard(m));
    else if (m.ended_reason && !LIVE.has(m.status)) a.append(el("p", "tm-ended", ENDED[m.ended_reason] || m.ended_reason));
    a.append(el("small", "tm-meta", `${num(m.tokens_used)} 토큰 · ${usd(m.cost_usd)}`));
    box.append(q, a);
    return box;
  }
  async function startTest(pop) {
    const t = pop.test, text = t.text.trim();
    if (!text) { window.ArkismNeed(pop.h?.dialog?.querySelector(".tm-test-form input") ?? null, "맡길 일을 적어 주세요."); return; }
    t.busy = true;
    try {
      const r = mt(await request("/v1/crew/meetings", { method: "POST", body: body({ meeting: { team_id: pop.id, title: text.slice(0, 40), topic: text } }) }));
      t.m = r; t.text = ""; t.busy = false;
      popRedraw("test");
      pollTest(pop);
    } catch (e) { t.busy = false; notice(errorText(e)); }
  }
  function pollTest(pop) {
    clearTimeout(pop.testTimer);
    const m = pop.test.m;
    if (!m?.id || !LIVE.has(m.status)) return;
    pop.testTimer = setTimeout(async () => {
      if (S.pop !== pop) return;
      try { pop.test.m = mt(await request(`/v1/crew/meetings/${encodeURIComponent(m.id)}?${query()}`)); } catch { /* keep the last picture; try again */ }
      popRedraw("test");
      void loadMeetingsOf(pop);
      pollTest(pop);
    }, 2000);
  }

  // ---------------------------------------------------------------- 기록 (981:60196): 판 이력 · 최근 회의
  function historyTab(pop) {
    if (!pop.id) return [empty("팀을 만들면 저장할 때마다 버전이 남아요.")];
    const list = pop.versions || []; pop.histOpen = pop.histOpen || new Set();
    const s1 = sec("clock-counter-clockwise", "버전 기록", "저장할 때마다 버전이 하나 생겨요. 다른 버전으로 바꿔도 지금 버전은 남아요.");
    const box = el("div", "tm-hist");
    list.forEach((h, i) => {
      const cur = h.v === pop.rec.revision;
      const r = el("div", "tm-hist-row");
      const at = new Date(h.at);
      r.append(el("b", cur ? "cur" : "", `v${h.v}`), el("time", "", `${pad2(at.getMonth() + 1)}-${pad2(at.getDate())} ${pad2(at.getHours())}:${pad2(at.getMinutes())}`), raw(el("span", "", tr(h.summary))));
      const acts = el("span", "tm-hist-acts");
      if (cur) acts.append(el("em", "", "지금 버전"));
      else {
        acts.append(link("바뀐 점 보기", () => { if (!pop.histOpen.delete(h.v)) pop.histOpen.add(h.v); popRedraw("history"); }, "tm-link quiet"), link("이 버전으로 변경", async () => {
          if (!await ask(`v${h.v} 버전으로 변경할까요? 새 버전으로 저장돼요.`)) return;
          void savePopup(pop, { ...clone(h.crew), cover: h.crew.cover ? clone(h.crew.cover) : undefined });
        }, "tm-link plain"));
      }
      r.append(acts);
      box.append(r);
      if (pop.histOpen.has(h.v)) box.append(window.ArkismVersionDiff.render(el, list[0].crew, h.crew, `v${h.v} 버전으로 바꾸면`)); // now (-) against that version (+)
    });
    if (!list.length) box.append(empty(pop.versions ? "버전 기록을 불러오지 못했어요." : "불러오는 중…"));
    s1.append(box);
    const s2 = sec("chat-text", "최근 회의", "테스트 탭에서 연 회의도 여기 남아요");
    const rows = el("div", "tm-hist");
    for (const m of (pop.meetings || []).slice(0, 8)) {
      const r = el("button", "tm-hist-row meet"); r.type = "button";
      const ok = m.status === "concluded";
      r.append(el("b", ok ? "ok" : "warn", ok ? "✓" : "!"), el("time", "", (() => { const t = new Date(m.created_at || m.updated_at); return isNaN(t) ? "" : `${pad2(t.getMonth() + 1)}-${pad2(t.getDate())} ${pad2(t.getHours())}:${pad2(t.getMinutes())}`; })()),
        el("i", "tm-hist-src", TRIGGERS[m.trigger] || "직접"), raw(el("span", "", m.title || "")));
      // 981:60196: a finished meeting shows how long it ran; others show where they stopped
      const ms = Date.parse(m.updated_at) - Date.parse(m.created_at);
      r.append(el("small", "", `${ok && ms > 0 ? dur(ms) : tr(MEETING_STATUS[m.status]?.[0] || "")} · ${usd(m.cost_usd)}`));
      r.addEventListener("click", () => { closePopup(); S.meetSel = m.id; S.meeting = m; S.meetTab = LIVE.has(m.status) ? "running" : "ended"; go("meetings"); });
      rows.append(r);
    }
    if (!(pop.meetings || []).length) rows.append(empty(pop.meetings ? "아직 연 회의가 없어요." : "불러오는 중…"));
    s2.append(rows);
    s1.classList.add("tm-hist-sec"); s2.classList.add("tm-hist-sec"); // 981:60196: plain titles, no rule or icon
    return [s1, s2];
  }

  // 참여자와 발언 순서: a table you can drag to reorder (editor) or a list with focus pills (new team, 310:19981).
  function membersCard(d, changed, compact) {
    const c = card("", compact ? null : "참여자와 발언 순서", null);
    const members = sorted(d.members);
    const renumber = (list) => { list.forEach((m, i) => { m.order = i + 1; }); d.members = list; };
    const box = el("div", compact ? "tm-mlist" : "tm-mtable");
    if (!compact) {
      const h = el("div", "tm-mrow head");
      ["순서", "참여자", "모델", "역할", ""].forEach((t) => h.append(el("span", "", t)));
      box.append(h);
    }
    if (compact && members.length) {
      // column names, so the pill reads as 맡을 일 (what this expert covers in the meeting) and not as a stray tag
      const h = el("div", "tm-mrow tm-mhead");
      const lead = el("span", d.mode === "moderator_picks" ? "tm-mhead-lead" : "tm-mhead-lead ordered"); // same width as face (and grip + number)
      h.append(lead, el("span", "tm-mwho", "전문가 · 역할"), el("span", "tm-mhead-focus", "맡을 일"), el("span", "tm-mmodelr", "모델"), el("span", "tm-mhead-x"));
      box.append(h);
    }
    const picks = d.mode === "moderator_picks"; // 진행자 지명 (972:60042): no order numbers, no drag handles
    members.forEach((m, i) => {
      const row = el("div", "tm-mrow");
      if (!picks) {
        row.draggable = true;
        row.addEventListener("dragstart", (e) => { S.drag = i; e.dataTransfer.effectAllowed = "move"; row.classList.add("dragging"); });
        row.addEventListener("dragend", () => row.classList.remove("dragging"));
        row.addEventListener("dragover", (e) => { e.preventDefault(); row.classList.add("over"); });
        row.addEventListener("dragleave", () => row.classList.remove("over"));
        row.addEventListener("drop", (e) => {
          e.preventDefault();
          if (S.drag < 0 || S.drag === i) return;
          const list = [...members]; const [moved] = list.splice(S.drag, 1); list.splice(i, 0, moved);
          S.drag = -1; renumber(list); changed(true);
        });
      } else row.classList.add("free");
      const model = memberModel(m);
      const roleSel = select(ROLES, m.role || "worker", (v) => { m.role = v; changed(); }, compact ? "tm-mrole" : "tm-plain-select");
      roleSel.setAttribute("aria-label", "역할");
      const remove = el("button", "icon-btn plain tm-x"); remove.type = "button"; remove.setAttribute("aria-label", "빼기"); remove.append(icon("x"));
      remove.addEventListener("click", () => { renumber(members.filter((x) => x !== m)); changed(true); });
      if (compact) {
        const who = el("div", "tm-mwho"); who.append(raw(el("b", "", memberName(m))), roleSel);
        const focus = raw(input(m.focus, (v) => { m.focus = v; changed(); }, { max: 120, placeholder: "맡을 일" })); focus.className = "tm-pill-input"; focus.setAttribute("aria-label", "맡을 일"); focus.title = tr("이 회의에서 이 전문가가 맡을 일");
        // 진행자 at a glance: a crown on the face and the row tinted
        const fc = el("span", "tm-mface"); fc.append(face(byId(m.agent_id), "m"));
        if (m.role === "moderator") { row.classList.add("lead"); const c = icon("crown"); c.classList.add("tm-crown"); c.setAttribute("aria-label", tr("진행자")); fc.append(c); }
        row.append(...(picks ? [] : [el("span", "tm-grip", "⠿"), el("span", "tm-ord", pad2(i + 1))]), fc, who, focus, raw(el("span", "tm-mmodelr", modelLabel(model))), remove);
      } else {
        const mc = el("span", "tm-mmodel"); mc.append(mark(model), el("span", "", modelLabel(model)));
        row.append(el("span", "tm-ord", pad2(i + 1)), raw(el("span", "tm-mname", memberName(m))), mc, roleSel, remove);
      }
      box.append(row);
    });
    if (!members.length) box.append(empty("아직 참여자가 없어요. 전문가를 넣어 주세요."));
    c.append(box);
    const acts = el("div", "tm-row-acts");
    const heads = [];
    const free = agents().filter((a) => !members.some((m) => m.agent_id === a.id));
    if (S.adding && free.length) {
      const pick = select([["", "넣을 전문가 고르기"], ...free.map((a) => [a.id, `${a.crew.name} · ${modelLabel(agentModel(a))}`])], "", (v) => {
        if (!v) return;
        S.adding = false;
        renumber([...members, { agent_id: v, order: members.length + 1, role: members.length ? "worker" : "moderator", focus: "" }]);
        changed(true);
      });
      acts.append(pick, link("취소", () => { S.adding = false; changed(true); }, "tm-link quiet"));
      queueMicrotask(() => pick.focus());
    } else {
      const addFn = () => { if (!free.length) { notice(agents().length ? "넣을 수 있는 전문가를 모두 넣었어요." : "먼저 전문가를 만들어 주세요."); return; } S.adding = true; changed(true); }; // the popup redraws itself; render() only redrew the page behind it
      if (compact) heads.push(link("+ 추가", addFn));
      else acts.append(button("전문가 추가", "", null, addFn));
      if (compact) heads.push(link("+ 새로 만들기", () => { if (ctx.hasView?.("workers")) { state.workerNew = true; go("workers"); } else notice("전문가 화면에서 만들어 주세요."); }, "tm-link quiet"));
    }
    if (acts.childNodes.length) c.append(acts);
    c.heads = heads;
    return c;
  }

  // 회의 방식: how turns go round and when the meeting ends.
  // The diagrams stand still until pointed at, focused or clicked (a click redraws the card, so the clicked one keeps playing).
  let playKey = "";
  function playOnHover(host, svg, playing) {
    const stop = () => { svg.pauseAnimations?.(); svg.setCurrentTime?.(0); };
    if (!playing) requestAnimationFrame(stop);
    const play = () => svg.unpauseAnimations?.();
    host.addEventListener("pointerenter", play); host.addEventListener("focus", play);
    host.addEventListener("pointerleave", () => { if (host.ownerDocument.activeElement !== host) { stop(); playKey = ""; } });
    host.addEventListener("blur", () => { if (!host.matches(":hover")) { stop(); playKey = ""; } });
  }
  function modeCard(d, changed, title, sub, bare) {
    const c = bare ? el("div", "tm-mode-wrap") : card("", title, sub);
    d.ending = { max_rounds: 4, token_cap: 100000, end_on_all_approved: true, end_on_moderator: true, ...(d.ending || {}) };
    const members = sorted(d.members);
    // compact cards hide the "add members" caption, so an empty team still shows the shape (310:19981)
    const shape = bare && !members.length ? ["moderator", "worker", "reviewer", "scribe"].map((role, i) => ({ agent_id: "", name: "", order: i + 1, role })) : members;
    const pick = el("div", "tm-block");
    if (!bare) pick.append(el("h4", "tm-label", "진행 방식"));
    const opts = el("div", "tm-modes"); opts.setAttribute("role", "radiogroup"); opts.setAttribute("aria-label", "진행 방식");
    for (const [key, label, text] of MODES) {
      const o = el("button", `tm-mode${(d.mode || "round_robin") === key ? " active" : ""}`); o.type = "button";
      o.setAttribute("role", "radio"); o.setAttribute("aria-checked", String((d.mode || "round_robin") === key));
      const t = el("div", "tm-mode-title"); t.append(el("i", "tm-radio"), el("b", "", label));
      const svg = key === "round_robin" ? diagramRound(shape, d.ending.max_rounds, bare) : diagramPick(shape);
      o.append(t, el("p", "", text), svg);
      playOnHover(o, svg, playKey === key);
      o.addEventListener("click", () => { playKey = key; if (d.mode !== key) { d.mode = key; changed(true); } else svg.unpauseAnimations?.(); });
      opts.append(o);
    }
    pick.append(opts);
    const end = el("div", "tm-block tm-ending");
    const eh = el("div", "tm-label-row"); eh.append(el("h4", "tm-label", "끝나는 조건"), ...(bare ? [] : [el("span", "", "먼저 닿는 조건에서 끝나요")]));
    const grid = el("div", "tm-two");
    const rounds = el("div", "tm-stepper");
    const rIn = input(d.ending.max_rounds, (v) => { const n = Math.max(1, Math.min(10, Number(v) || 1)); d.ending.max_rounds = n; changed(); }, { type: "number" });
    rIn.min = 1; rIn.max = 10;
    const step = (dx) => { d.ending.max_rounds = Math.max(1, Math.min(10, (Number(d.ending.max_rounds) || 1) + dx)); changed(true); };
    const minus = el("button", "", "−"); minus.type = "button"; minus.setAttribute("aria-label", "한 라운드 줄이기"); minus.addEventListener("click", () => step(-1));
    const plus = el("button", "", "+"); plus.type = "button"; plus.setAttribute("aria-label", "한 라운드 늘리기"); plus.addEventListener("click", () => step(1));
    rounds.append(rIn, minus, plus);
    const cap = el("div", "tm-suffix");
    const cIn = input(Number(d.ending.token_cap || 0).toLocaleString("en-US"), (v) => { d.ending.token_cap = Number(String(v).replace(/[^\d]/g, "")) || 0; changed(); });
    cIn.inputMode = "numeric"; cIn.classList.add("mono");
    cap.append(cIn, el("span", "", "토큰"));
    grid.append(field("최대 라운드", rounds), field("토큰 상한", cap));
    const toggles = el("div", "tm-toggles");
    for (const [k, label] of [["end_on_all_approved", "리뷰어가 모두 승인하면 끝"], ["end_on_moderator", "진행자가 끝내면 끝"]]) {
      const r = el("div", "tm-toggle"); r.append(el("span", "", label), sw(d.ending[k], label, (v) => { d.ending[k] = v; changed(true); }));
      toggles.append(r);
    }
    end.append(eh, grid, toggles);
    c.append(pick, end);
    return c;
  }
  // Little pictures of the two modes (Figma draws them with vectors; same layout, drawn here as SVG).
  const NS = "http://www.w3.org/2000/svg";
  function svgEl(tag, attrs, text) { const n = document.createElementNS(NS, tag); for (const [k, v] of Object.entries(attrs || {})) n.setAttribute(k, v); if (text != null) n.textContent = text; return n; }
  function nodeLogo(g, model, x, y) {
    const m = (model || "").toLowerCase();
    if (/^(claude|opus|sonnet|haiku|fable)/.test(m)) { g.append(svgEl("image", { href: "img/claude.svg", x: x - 6, y: y - 6, width: 12, height: 12 })); return; }
    const ref = /^(gpt|o\d|codex)/.test(m) ? "a-codex" : m.startsWith("glm") ? "a-zcode" : m.startsWith("gemini") ? "a-gemini" : "";
    if (ref) g.append(svgEl("use", { href: `#${ref}`, x: x - 6, y: y - 6, width: 12, height: 12, class: "tm-svg-logo" }));
    else g.append(svgEl("circle", { cx: x, cy: y, r: 3, class: "tm-svg-dot" }));
  }
  function arrowDefs(svg) {
    const defs = svgEl("defs");
    for (const [id, cls] of [["tm-ah", "tm-svg-ah"], ["tm-ah-on", "tm-svg-ah on"]]) {
      const mk = svgEl("marker", { id, viewBox: "0 0 6 6", refX: 5, refY: 3, markerWidth: 6, markerHeight: 6, orient: "auto-start-reverse" });
      mk.append(svgEl("path", { d: "M0 0L6 3L0 6z", class: cls })); defs.append(mk);
    }
    svg.append(defs);
  }
  // Motion (SMIL, so it plays inside the SVG): 순서대로 = the highlight walks node to node; 진행자 지명 = an arrow
  // grows from the moderator to whoever is picked next. Reduced motion keeps the still picture.
  const still = () => !!window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
  const STEP = 1.2; // seconds per turn
  function anim(node, attr, values, times, dur, calcMode = "linear") {
    node.append(svgEl("animate", { attributeName: attr, values: values.join(";"), keyTimes: times.map((t) => +t.toFixed(4)).join(";"), dur: `${dur}s`, repeatCount: "indefinite", calcMode }));
  }
  // class "base on" only during turn i of n (from a to b of that turn)
  const turnOn = (node, base, i, n, dur, a = 0, b = 1) => anim(node, "class", [base, `${base} on`, base], [0, (i + a) / n, (i + b) / n], dur, "discrete");
  function diagramRound(members, maxRounds, bare) {
    const list = members.slice(0, 5);
    const svg = svgEl("svg", { viewBox: bare ? "0 18 371 74" : "0 0 371 112", class: "tm-diagram", "aria-hidden": "true" });
    arrowDefs(svg);
    if (!list.length) { svg.append(svgEl("text", { x: 185, y: 60, class: "tm-svg-cap", "text-anchor": "middle" }, "참여자를 넣으면 순서가 보여요")); return svg; }
    const xs = list.map((_, i) => (list.length === 1 ? 185 : 28 + (i * (343 - 28)) / (list.length - 1)));
    const n = list.length, move = n > 1 && !still(), dur = n * STEP;
    const now = move ? -1 : n > 1 ? 1 : 0;
    for (let i = 0; i < n - 1; i++) {
      const ln = svgEl("line", { x1: xs[i] + 18, y1: 34, x2: xs[i + 1] - 18, y2: 34, class: "tm-svg-line", "marker-end": "url(#tm-ah)" });
      if (move) { turnOn(ln, "tm-svg-line", i, n, dur, .6, 1.1); anim(ln, "marker-end", ["url(#tm-ah)", "url(#tm-ah-on)", "url(#tm-ah)"], [0, (i + .6) / n, (i + 1.1) / n], dur, "discrete"); }
      svg.append(ln);
    }
    if (n > 1) {
      const back = svgEl("path", { d: `M${xs[n - 1]} 64 V86 H${xs[0]} V66`, class: "tm-svg-line dash", "marker-end": "url(#tm-ah)" });
      if (move) { anim(back, "class", ["tm-svg-line dash", "tm-svg-line dash on"], [0, (n - .4) / n], dur, "discrete"); anim(back, "marker-end", ["url(#tm-ah)", "url(#tm-ah-on)"], [0, (n - .4) / n], dur, "discrete"); }
      svg.append(back);
    }
    list.forEach((m, i) => {
      const g = svgEl("g");
      const c = svgEl("circle", { cx: xs[i], cy: 34, r: 13, class: `tm-svg-node${i === now ? " on" : ""}` });
      if (move) turnOn(c, "tm-svg-node", i, n, dur);
      g.append(c);
      nodeLogo(g, memberModel(m), xs[i], 34);
      const name = svgEl("text", { x: xs[i], y: 61, "text-anchor": "middle", class: `tm-svg-name${i === now ? " on" : ""}` }, memberName(m)); name.setAttribute("data-no-i18n", "");
      if (move) turnOn(name, "tm-svg-name", i, n, dur);
      g.append(name);
      if (i === now) g.append(svgEl("text", { x: xs[i], y: 14, "text-anchor": "middle", class: "tm-svg-tag" }, "지금"));
      svg.append(g);
    });
    if (move) {
      // the walking highlight: holds on a node, then slides to the next one
      const vals = [], times = [];
      xs.forEach((x, i) => { vals.push(x, x); times.push(i / n, (i + .6) / n); });
      vals.push(xs[n - 1]); times.push(1);
      const spot = svgEl("circle", { cx: xs[0], cy: 34, r: 17, class: "tm-svg-spot" });
      anim(spot, "cx", vals, times, dur);
      anim(spot, "opacity", [0, 1, 1, 0], [0, .04, .96, 1], dur);
      const tag = svgEl("text", { x: xs[0], y: 14, "text-anchor": "middle", class: "tm-svg-tag" }, "지금");
      anim(tag, "x", vals, times, dur);
      svg.append(spot, tag);
    }
    svg.append(svgEl("text", { x: 185, y: 104, "text-anchor": "middle", class: "tm-svg-cap" }, `다 돌면 처음부터 · 라운드 1/${maxRounds || 4}`));
    return svg;
  }
  function diagramPick(members) {
    const svg = svgEl("svg", { viewBox: "0 0 371 112", class: "tm-diagram", "aria-hidden": "true" });
    arrowDefs(svg);
    const mod = members.find((m) => m.role === "moderator") || members[0];
    const rest = members.filter((m) => m !== mod).slice(0, 3);
    if (!mod) { svg.append(svgEl("text", { x: 185, y: 60, class: "tm-svg-cap", "text-anchor": "middle" }, "참여자를 넣으면 순서가 보여요")); return svg; }
    const ys = rest.length === 1 ? [56] : rest.map((_, i) => 16 + (i * 80) / (rest.length - 1));
    const k = rest.length, move = k > 0 && !still();
    // the picks jump around (not top to bottom), so it reads as 지명 rather than a fixed order
    const order = k === 3 ? [1, 0, 2] : k === 2 ? [1, 0] : [0];
    const turns = order.length, dur = turns * STEP;
    const turnOf = (i) => order.indexOf(i);
    const on = move ? -1 : k > 1 ? 1 : 0;
    rest.forEach((m, i) => {
      const y = ys[i], d = `M54 56 C150 56 180 ${y} 272 ${y}`;
      svg.append(svgEl("path", { d, class: `tm-svg-line${i === on ? " on" : " dash"}`, "marker-end": i === on ? "url(#tm-ah-on)" : "url(#tm-ah)" }));
      if (move) {
        // the arrow grows from the moderator to the picked one, stays a moment, then the next pick
        const t = turnOf(i), s = t / turns, grow = .45 / turns;
        const ray = svgEl("path", { d, pathLength: 100, class: "tm-svg-line on tm-svg-ray", "stroke-dasharray": "100 100", "stroke-dashoffset": 100, opacity: 0 });
        anim(ray, "stroke-dashoffset", [100, 100, 0, 0], [0, s, s + grow, 1], dur);
        anim(ray, "opacity", [0, 1, 0], [0, s, s + .95 / turns], dur, "discrete");
        svg.append(ray);
      }
    });
    const g = svgEl("g");
    g.append(svgEl("circle", { cx: 38, cy: 56, r: 13, class: "tm-svg-node" }));
    nodeLogo(g, memberModel(mod), 38, 56);
    g.append(svgEl("rect", { x: 24, y: 28, width: 28, height: 12, rx: 3, class: "tm-svg-pill" }), svgEl("text", { x: 38, y: 37, "text-anchor": "middle", class: "tm-svg-pilltext" }, "진행"));
    const mn = svgEl("text", { x: 38, y: 84, "text-anchor": "middle", class: "tm-svg-name" }, memberName(mod)); mn.setAttribute("data-no-i18n", ""); g.append(mn);
    svg.append(g);
    rest.forEach((m, i) => {
      const y = ys[i], gg = svgEl("g"), t = turnOf(i);
      const c = svgEl("circle", { cx: 288, cy: y, r: 13, class: `tm-svg-node${i === on ? " on" : ""}` });
      if (move) turnOn(c, "tm-svg-node", t, turns, dur, .4, .95);
      gg.append(c);
      nodeLogo(gg, memberModel(m), 288, y);
      const nm = svgEl("text", { x: 308, y: y + (i === on ? 0 : 4), class: `tm-svg-name${i === on ? " on" : ""}` }, memberName(m)); nm.setAttribute("data-no-i18n", "");
      if (move) turnOn(nm, "tm-svg-name", t, turns, dur, .4, .95);
      gg.append(nm);
      if (i === on) gg.append(svgEl("text", { x: 308, y: y + 12, class: "tm-svg-tag left" }, "지금 차례"));
      svg.append(gg);
    });
    if (move) {
      const tag = svgEl("text", { x: 308, y: ys[order[0]] + 16, class: "tm-svg-tag left", opacity: 0 }, "지금 차례");
      anim(tag, "y", order.map((i) => ys[i] + 16), order.map((_, t) => t / turns), dur, "discrete");
      anim(tag, "opacity", order.flatMap(() => [0, 1]), order.flatMap((_, t) => [t / turns, (t + .4) / turns]), dur, "discrete");
      svg.append(tag);
    }
    return svg;
  }

  // 함께 보는 자료와 채널: shared context for every member and the Discord channels that start this team's meetings.
  function contextSources() {
    const out = [];
    for (const m of state.data.memories || []) if (["procedural", "decision", "user"].includes(m.kind)) out.push(["memories", m.id, (m.title || m.text || "").slice(0, 60)]);
    for (const ds of state.data.designSystems || []) out.push(["design_systems", ds.id, ds.name || ds.title || ds.id]);
    for (const sk of state.data.skills || []) out.push(["skills", sk.id, sk.name]);
    return out.slice(0, 300);
  }
  function contextAdder(d, changed) {
    const used = new Set((d.context || []).map((c) => `${c.collection}:${c.id}`));
    const opts = contextSources().filter(([col, id]) => !used.has(`${col}:${id}`));
    const kindText = { memories: "기억", design_systems: "디자인 시스템", skills: "스킬" };
    const s = select([["", opts.length ? "넣을 자료 고르기" : "넣을 자료가 없어요"], ...opts.map(([col, id, title]) => [`${col}\u0000${id}`, `${kindText[col]} · ${title}`])], "", (v) => {
      if (!v) return;
      const [col, id] = v.split("\u0000");
      const src = opts.find((o) => o[0] === col && o[1] === id);
      d.context = [...(d.context || []), { collection: col, id, title: src?.[2] || id, share: "team" }];
      S.ctxAdding = false; changed(true);
    });
    queueMicrotask(() => s.focus());
    return s;
  }
  function validTeam(d) {
    if (!d.name?.trim()) return "팀 이름을 넣어 주세요.";
    if (!(d.members || []).length) return "참여자를 한 명 이상 넣어 주세요.";
    return "";
  }
  const blankTeam = () => ({ type: "team", name: "", handle: "", role: "", members: [], mode: "round_robin",
    ending: { max_rounds: 4, token_cap: 100000, end_on_all_approved: true, end_on_moderator: true }, context: [], templates: [], discord_mirror: false });

  // ================================================================ 회의 (208:19103)
  async function loadMeetings(quiet) {
    try {
      const r = await request(`/v1/crew/meetings?${query({ status: S.meetTab === "ended" ? "ended" : S.meetTab, limit: 50 })}`);
      S.meetings = (r.items || []).map(mt); S.counts = r.counts || S.counts || {}; S.meetErr = "";
    } catch (e) {
      if (quiet) return;
      S.meetings = []; S.meetErr = unsupported(e) ? "unsupported" : e.message;
    }
    if (!S.meetSel || !S.meetings.some((m) => m.id === S.meetSel)) S.meetSel = S.meetings[0]?.id || (S.meeting?.id ?? null);
    if (S.meetSel && S.meeting?.id !== S.meetSel) await loadMeeting(S.meetSel, true);
    if (here("meetings")) repaintMeetings();
  }
  async function loadMeeting(id, silent) {
    try {
      const m = mt(await request(`/v1/crew/meetings/${encodeURIComponent(id)}?${query()}`));
      const same = S.meeting && S.meeting.id === m.id && JSON.stringify(S.meeting) === JSON.stringify(m);
      S.meeting = m;
      const i = (S.meetings || []).findIndex((x) => x.id === m.id);
      if (i >= 0) S.meetings[i] = { ...S.meetings[i], ...m };
      if (!silent && !same && here("meetings")) repaintMeetings();
    } catch (e) {
      if (!silent) notice(`회의를 불러오지 못했어요 (${e.message}).`);
    }
    schedule();
  }
  // Poll GET /v1/crew/meetings/{id} every 2 s while the meeting on screen is still going (contract).
  function schedule() {
    clearTimeout(S.timer);
    if (!here("meetings") || !S.meeting || !LIVE.has(S.meeting.status)) return;
    S.timer = setTimeout(async () => {
      if (!here("meetings") || !S.meeting) return;
      S.tick++;
      if (S.tick % 5 === 0) void loadMeetings(true);
      await loadMeeting(S.meeting.id, false);
    }, 2000);
  }
  // Re-draw the meeting screen without losing what the person is typing in the note box.
  function repaintMeetings() {
    const focused = document.activeElement?.classList?.contains("tm-note-input");
    const pos = focused ? [document.activeElement.selectionStart, document.activeElement.selectionEnd] : null;
    render(false);
    if (focused) { const n = $(".tm-note-input"); if (n) { n.focus(); try { n.setSelectionRange(...pos); } catch { /* number input */ } } }
  }
  function renderMeetings() {
    ctx.headExtras?.({ aside: [button("팀 보기", "tm-outline", null, () => go("teams")), button("새 회의", "primary", null, () => { S.newMeeting = { team_id: S.meeting?.team_id || teams()[0]?.id || "" }; render(false); })] });
    $("#controls").replaceChildren();
    const table = $("#table");
    table.className = "tm-wrap tm-meet";
    if (S.meetings == null) { S.meetings = []; S.meetLoading = true; void loadMeetings().finally(() => { S.meetLoading = false; }); }
    if (S.meetErr) {
      table.replaceChildren(empty(S.meetErr === "unsupported" ? "이 서버는 아직 회의를 지원하지 않아요. 서버를 새 버전으로 올리면 팀 회의를 여기서 볼 수 있어요." : `회의를 불러오지 못했어요 (${S.meetErr}).`));
      meetingSide(null);
      return;
    }
    const c = S.counts || {};
    const seg = el("div", "tm-seg"); seg.setAttribute("role", "tablist");
    for (const [k, label] of [["running", "진행 중"], ["ended", "끝남"], ["all", "전체"]]) {
      const t = el("button", `tm-seg-tab${S.meetTab === k ? " active" : ""}`); t.type = "button"; t.setAttribute("role", "tab");
      t.append(el("span", "", label), el("output", "", String(c[k] ?? "")));
      t.addEventListener("click", () => { if (S.meetTab !== k) { S.meetTab = k; S.meetSel = null; void loadMeetings(); } });
      seg.append(t);
    }
    const list = el("div", "tm-mlist2");
    for (const m of S.meetings || []) {
      const row = el("button", `tm-meet-row${m.id === S.meetSel ? " selected" : ""}`); row.type = "button";
      const t = el("div", ""); t.append(raw(el("b", "", m.title || "(제목 없음)")), raw(el("small", "", meetingMeta(m))));
      const [label, tone] = MEETING_STATUS[m.status] || [m.status, "line"];
      row.append(t, badge(label, tone));
      row.addEventListener("click", () => { S.meetSel = m.id; S.newMeeting = null; S.meeting = m; render(false); void loadMeeting(m.id); });
      list.append(row);
    }
    if (!(S.meetings || []).length) list.append(empty(S.meetLoading ? "불러오는 중…" : S.meetTab === "running" ? "지금 진행 중인 회의는 없어요. 새 회의를 열어 보세요." : "아직 회의 기록이 없어요."));
    const kids = [seg, list];
    const m = S.meeting && S.meeting.id === S.meetSel ? S.meeting : (S.meetings || []).find((x) => x.id === S.meetSel);
    if (m) {
      if (m.status === "waiting") kids.push(waitingCard(m));
      kids.push(transcript(m));
      if (m.conclusion && Object.keys(m.conclusion).length) kids.push(conclusionCard(m));
      if (LIVE.has(m.status) && m.status !== "stopping") kids.push(noteBox(m));
    }
    table.replaceChildren(...kids);
    meetingSide(m);
    schedule();
  }
  const teamName = (m) => m.team_snapshot?.team?.name || m.team_snapshot?.name || byId(m.team_id)?.crew?.name || "팀";
  function meetingMeta(m) {
    // 팀 이름은 데이터라 그대로, 나머지만 번역 (raw 안에 들어간다)
    if (LIVE.has(m.status)) return `${teamName(m)} · ${tr(`라운드 ${m.round || 0}/${m.max_rounds || "?"}`)}`;
    return `${teamName(m)} · ${tr(ago(m.finished_at || m.updated_at || m.created_at))}`;
  }
  // team_snapshot = {team, agents:{id: agent}} copied when the meeting started; fall back to the live team record.
  function membersOf(m) {
    const snap = m.team_snapshot;
    const list = snap?.team?.members || snap?.members || byId(m.team_id)?.crew?.members || [];
    return sorted(list).map((x) => {
      const a = snap?.agents?.[x.agent_id];
      return a ? { ...x, name: x.name || a.name, model: x.model_override || x.model || (a.model_mode === "auto" ? "auto" : a.model) } : x;
    });
  }
  function speakerModel(m, turn) {
    if (turn.model) return turn.model;
    const mem = membersOf(m).find((x) => x.agent_id === turn.agent_id);
    return mem ? (mem.model || memberModel(mem)) : "";
  }
  // Notes the Discord bot relays come in as by = "discord" (the person's name is in the text).
  const who = (by) => (by === "discord" ? tr("디스코드") : !by || by === "human" || by === "owner" ? tr("나") : by); // raw 안에 들어간다
  function turnMeta(t) {
    if (t.kind === "brief") return "브리프";
    if (t.kind === "human") return "내 의견";
    if (t.kind === "conclusion") return "결론";
    return `라운드 ${t.round || 0}${t.stance && STANCE[t.stance] ? ` · ${STANCE[t.stance]}` : ""}`;
  }
  function transcript(m) {
    const c = el("section", "tm-trans");
    const h = el("div", "tm-trans-head"); h.append(raw(el("h3", "", m.title || "(제목 없음)")), el("span", "mono", `라운드 ${m.round || 0} / ${m.max_rounds || "?"}`));
    c.append(h);
    if (m.topic) { const tp = raw(el("p", "tm-topic", m.topic)); c.append(tp); }
    for (const t of m.turns || []) {
      if (t.kind === "conclusion") continue; // shown in its own card below
      const row = el("div", `tm-turn${t.kind === "human" ? " human" : ""}`);
      row.append(t.kind === "human" ? Object.assign(el("span", "tm-mark"), { textContent: "•" }) : mark(speakerModel(m, t)));
      const b = el("div", "");
      const top = el("div", "tm-turn-top"); top.append(raw(el("b", "", t.kind === "human" ? who(t.speaker) : t.speaker || t.agent_id)), el("small", "", turnMeta(t)));
      if (t.ok === false) top.append(el("small", "amber", " · 실패"));
      b.append(top, raw(el("p", "", t.ok === false ? (t.error || "답을 받지 못했어요.") : t.text || "")));
      row.append(b);
      c.append(row);
    }
    if (m.status === "running" && m.speaking) {
      const mem = membersOf(m).find((x) => x.agent_id === m.speaking || memberName(x) === m.speaking);
      const row = el("div", "tm-turn speaking");
      row.append(mark(mem ? mem.model || memberModel(mem) : ""));
      const top = el("div", "tm-turn-top"); top.append(raw(el("b", "", mem ? memberName(mem) : m.speaking)), el("small", "amber", `라운드 ${m.round || 0} · 말하는 중…`));
      const b = el("div", ""); b.append(top); row.append(b); c.append(row);
    }
    if (m.status === "pending" && !(m.turns || []).length) c.append(empty("곧 시작해요. 기록관이 먼저 브리프를 써요."));
    for (const n of m.notes_pending || []) {
      const row = el("div", "tm-turn human pending");
      row.append(Object.assign(el("span", "tm-mark"), { textContent: "•" }));
      const b = el("div", ""); const top = el("div", "tm-turn-top"); top.append(raw(el("b", "", who(n.by))), el("small", "", "다음 라운드에 반영"));
      b.append(top, raw(el("p", "", n.text || ""))); row.append(b); c.append(row);
    }
    if (m.ended_reason && !LIVE.has(m.status)) c.append(el("p", "tm-ended", ENDED[m.ended_reason] || m.ended_reason));
    return c;
  }
  function conclusionCard(m) {
    const k = m.conclusion;
    const c = el("section", "tm-trans tm-concl");
    const h = el("div", "tm-trans-head"); h.append(el("h3", "", "결론"));
    c.append(h);
    if (k.summary) c.append(raw(el("p", "tm-concl-sum", k.summary)));
    for (const [key, label] of [["decisions", "정한 것"], ["todos", "할 일"], ["open_questions", "남은 질문"]]) {
      const list = (k[key] || []).filter(Boolean);
      if (!list.length) continue;
      const box = el("div", "tm-concl-list"); box.append(el("h4", "", label));
      const ul = el("ul", ""); list.forEach((x) => ul.append(raw(el("li", "", typeof x === "string" ? x : x.text || JSON.stringify(x))))); box.append(ul);
      c.append(box);
    }
    const n = m.saved?.candidate_ids?.length || 0;
    const save = el("div", "tm-saved");
    if (n) { save.append(el("span", "", `기억 후보 ${n}개로 보냈어요.`), link("검토하기 →", () => go("memories", "candidate"))); }
    else save.append(el("span", "", m.status === "concluded" ? "결정과 교훈은 기억 후보로 보내져요. 검토 화면에서 승인해야 기억이 돼요." : ""));
    c.append(save);
    return c;
  }
  function waitingCard(m) {
    const a = el("div", "wf-attn tm-wait");
    const t = el("div", ""); t.append(el("strong", "", "확인해 주세요"), el("p", "", m.waiting?.reason || "예산 한도에 닿아 회의를 잠시 멈췄어요. 계속할까요?"));
    const acts = el("div", "wf-attn-actions");
    acts.append(button("계속", "primary", null, () => decide(m, true)), button("멈춤", "", null, () => decide(m, false)));
    a.append(t, acts);
    return a;
  }
  function noteBox(m) {
    const row = el("form", "tm-note-row");
    const i = el("input", "tm-input tm-note-input"); i.placeholder = "다음 라운드에 반영할 의견을 남겨요"; i.maxLength = 4000; i.value = S.note;
    i.addEventListener("input", () => { S.note = i.value; });
    const send = el("button", "btn primary", "보내기"); send.type = "submit";
    row.addEventListener("submit", async (e) => {
      e.preventDefault();
      const text = S.note.trim(); if (!text) return;
      send.disabled = true;
      try {
        const r = await request(`/v1/crew/meetings/${encodeURIComponent(m.id)}/notes`, { method: "POST", body: body({ expected_revision: m.revision, text }) });
        S.note = "";
        if (r?.id) S.meeting = mt(r); else S.meeting = { ...m, notes_pending: [...(m.notes_pending || []), { by: "나", text, at: new Date().toISOString() }] };
        notice("다음 라운드에 반영할게요.");
        repaintMeetings();
        void loadMeeting(m.id, true);
      } catch (err) { notice(errorText(err)); send.disabled = false; }
    });
    row.append(i, send);
    return row;
  }
  async function decide(m, approve) {
    try {
      const r = await request(`/v1/crew/meetings/${encodeURIComponent(m.id)}/decision`, { method: "POST", body: body({ expected_revision: m.revision, approve }) });
      if (r?.id) S.meeting = mt(r);
      notice(approve ? "회의를 이어서 해요." : "회의를 멈췄어요.");
      await loadMeeting(m.id, false);
    } catch (e) { notice(errorText(e)); }
  }
  async function stopMeeting(m) {
    if (!await ask("회의를 멈출까요? 지금까지의 기록은 남아요.")) return;
    try {
      await request(`/v1/crew/meetings/${encodeURIComponent(m.id)}/stop`, { method: "POST", body: body({}) });
      notice("멈추는 중이에요. 지금 말하는 차례가 끝나면 멈춰요.");
      await loadMeeting(m.id, false);
    } catch (e) { notice(errorText(e)); }
  }
  function bar(value, max) { const b = el("div", "tm-bar"); const i = el("i"); i.style.width = `${Math.min(100, max ? (value / max) * 100 : 0)}%`; b.append(i); return b; }
  function meetingSide(m) {
    if (S.newMeeting) { side(...newMeetingForm()); return; }
    if (!m) { side(empty("회의를 고르면 진행 상황이 여기 보여요.")); return; }
    const prog = el("div", "tm-side-sec");
    prog.append(sideLabel("진행"));
    const r1 = el("div", "tm-prog"); r1.append(el("span", "", "라운드"), el("b", "mono", `${pad2(m.round)} / ${pad2(m.max_rounds)}`));
    const r2 = el("div", "tm-prog"); r2.append(el("span", "", "토큰"), el("b", "mono", `${Number(m.tokens_used || 0).toLocaleString("en-US")} / ${Number(m.token_cap || 0).toLocaleString("en-US")}`));
    prog.append(r1, bar(m.round || 0, m.max_rounds || 1), r2, bar(m.tokens_used || 0, m.token_cap || 1));
    const ppl = el("div", "tm-side-sec");
    const members = membersOf(m);
    ppl.append(sideLabel("참여자", `${members.length}명`));
    for (const mem of members) {
      const name = mem.name || memberName(mem);
      const spoke = (m.turns || []).some((t) => t.agent_id === mem.agent_id && t.kind === "turn");
      const now = m.status === "running" && (m.speaking === mem.agent_id || m.speaking === name);
      const row = el("div", "tm-person");
      row.append(mark(mem.model || memberModel(mem)), raw(el("span", "", name)), el("small", now ? "amber" : "", now ? "말하는 중" : spoke ? "말함" : LIVE.has(m.status) ? "대기" : "말 안 함"));
      ppl.append(row);
    }
    const cost = el("div", "tm-side-sec");
    cost.append(sideLabel("비용"));
    const now = el("b", ""); now.append(usd(m.cost_usd)); now.append(est());
    cost.append(kv([["지금까지", now], ["예상 전체", m.cost_estimate_usd ? `약 ${usd(m.cost_estimate_usd)}` : "—"], ["예산 넘김", m.budget_approved?.at ? `${ago(m.budget_approved.at)} 승인` : ""]]));
    const acts = el("div", "tm-side-acts");
    if (m.discord?.thread_url) { const a = el("a", "btn", "디스코드에서 열기"); a.href = m.discord.thread_url; a.target = "_blank"; a.rel = "noreferrer"; acts.append(a); }
    if (LIVE.has(m.status) && m.status !== "stopping") acts.append(button("회의 멈춤", "ghost", null, () => stopMeeting(m)));
    side(prog, ppl, cost, acts);
  }
  function newMeetingForm() {
    const f = S.newMeeting;
    const team = byId(f.team_id);
    const head = el("div", "tm-side-sec"); head.append(el("p", "tm-side-kind", "새 회의"));
    if (!teams().length) return [head, empty("먼저 팀을 만들어 주세요."), link("팀 만들기 →", () => openPopup(null))];
    const teamSel = select(teams().map((t) => [t.id, t.crew.name]), f.team_id, (v) => { f.team_id = v; f.template_key = ""; render(false); });
    const tpls = team?.crew?.templates || [];
    const topic = raw(input(f.topic, (v) => { f.topic = v; }, { tag: "textarea", rows: 6, max: 20000, placeholder: "무엇을 이야기할지 적어 주세요. 링크나 붙여 넣은 글도 돼요." }));
    const tplSel = select([["", "템플릿 없이"], ...tpls.map((t) => [t.key, t.name])], f.template_key || "", (v) => {
      f.template_key = v;
      const t = tpls.find((x) => x.key === v);
      if (t?.prompt && !f.topic) { f.topic = t.prompt; topic.value = t.prompt; }
    });
    const title = raw(input(f.title, (v) => { f.title = v; }, { max: 120, placeholder: "로그인 화면 다크 테마 검토" }));
    const facts = team ? el("p", "tm-side-text", `${(team.crew.members || []).length}명 · ${team.crew.mode === "round_robin" ? "정해진 순서" : "진행자 지명"} · 최대 ${team.crew.ending?.max_rounds || 4}라운드 · 파일은 고치지 않아요`) : null;
    const acts = el("div", "tm-side-acts");
    acts.append(button("시작", "primary", null, () => startMeeting()), button("취소", "ghost", null, () => { S.newMeeting = null; render(false); }));
    return [head, field("팀", teamSel), ...(facts ? [facts] : []), field("템플릿", tplSel), field("제목", title), field("주제", window.ArkismAIChat.field(topic, { state: f.talk ||= {}, title: "주제 다듬기", preset: "You write the topic a team of AI experts gets when a meeting opens: what to decide, what material to look at, the result wanted. Keep links and pasted text the user wrote. Korean, short.",
      context: () => JSON.stringify({ team: team?.crew?.name || "", title: f.title || "", template: tpls.find((x) => x.key === f.template_key)?.name || "" }) })), acts];
  }
  async function startMeeting() {
    const f = S.newMeeting;
    if (!f.team_id) { notice("팀을 골라 주세요."); return; }
    if (!(f.title || "").trim()) { window.ArkismNeed("제목", "제목을 넣어 주세요."); return; }
    try {
      const r = mt(await request("/v1/crew/meetings", { method: "POST", body: body({ meeting: { team_id: f.team_id, template_key: f.template_key || undefined, title: f.title.trim(), topic: (f.topic || "").trim() } }) }));
      S.newMeeting = null; S.meetTab = "running";
      if (r?.id) { S.meetSel = r.id; S.meeting = r; }
      notice("회의를 열었어요. 곧 시작해요.");
      await loadMeetings();
    } catch (e) { notice(errorText(e)); }
  }

  // ================================================================ 디스코드 (208:19677)
  function renderDiscord() {
    ctx.headExtras?.({ aside: [button("봇 연결", "primary", null, () => startConnect())] });
    $("#controls").replaceChildren();
    const table = $("#table");
    table.className = "tm-wrap tm-discord";
    if (!S.discord) { void loadDiscord(); table.replaceChildren(empty("불러오는 중…")); $("#detail").replaceChildren(); return; }
    if (S.discordErr === "unsupported") { table.replaceChildren(empty("이 서버는 아직 디스코드 연결을 지원하지 않아요. 서버를 새 버전으로 올려 주세요.")); $("#detail").replaceChildren(); return; }
    if (S.connect) { table.replaceChildren(...connectSteps()); connectSide(); return; }
    const kids = [];
    if (S.discordErr) kids.push(empty(`디스코드 상태를 불러오지 못했어요 (${S.discordErr}).`));
    for (const b of bots().filter((x) => ["needs_permission", "token_invalid"].includes(x.state))) {
      const a = el("div", "wf-attn");
      const miss = (b.missing || []).map((m) => `#${channelName(b, m.channel_id)}`).join(", ");
      const t = el("div", "");
      t.append(raw(el("strong", "", tr(b.state === "token_invalid" ? `${b.name} 토큰을 다시 넣어 주세요` : `${b.name} 권한을 다시 확인해 주세요`))),
        raw(el("p", "", tr(b.state === "token_invalid" ? "디스코드가 토큰을 받아 주지 않아요. 개발자 포털에서 토큰을 새로 만들어 넣어 주세요." : `${miss || "채널"}에 글을 쓸 권한이 빠졌어요. 서버 관리자가 역할을 다시 줘야 해요.`))));
      const acts = el("div", "wf-attn-actions");
      acts.append(b.state === "token_invalid" ? button("토큰 다시 넣기", "primary", null, () => startConnect(b)) : button("권한 다시 확인", "primary", null, () => checkBot(b)));
      a.append(t, acts); kids.push(a);
    }
    const st = S.discord.stats || {};
    const kp = el("div", "tm-kpis");
    const k = (label, value, sub) => { const c = el("div", "tm-kpi"); c.append(el("p", "", label), el("div", "tm-kpi-value"), raw(el("small", "", tr(sub || "")))); c.children[1].append(el("strong", "", value)); return c; };
    const byTarget = (st.by_target || []).slice(0, 3).map((t) => `${t.label || t.name || targetLabel(t, true)} ${t.calls ?? t.count ?? 0}`).join(" · ");
    kp.append(k("연결된 봇", String(st.bots ?? bots().length), bots().map((b) => b.name.replace(/-?bot$/i, "")).join(" · ")),
      k("열린 회의", String(st.open_threads ?? 0), "채널 스레드"), k("이번 주 호출", String(st.calls_week ?? 0), byTarget));
    kids.push(kp);
    for (const b of bots()) kids.push(botCard(b));
    if (!bots().length) kids.push(empty("아직 연결한 봇이 없어요. 봇 연결을 눌러 디스코드 서버에 Arkism 봇을 넣어 보세요."));
    table.replaceChildren(...kids);
    if (S.settingsOpen) settingsSide();
    else if (S.bot && botById(S.bot)) botSide(botById(S.bot));
    else discordOverview();
  }
  const channelName = (b, id) => (b.channels || []).find((c) => c.channel_id === id)?.name || id;
  function botCard(b) {
    const c = el("article", `tm-bot${S.bot === b.id ? " selected" : ""}`);
    c.tabIndex = 0;
    const h = el("div", "tm-bot-head");
    const t = el("div", "");
    const first = (b.channels || [])[0];
    t.append(raw(el("b", "", b.name)), raw(el("small", "", tr([first ? targetLabel(first.action, true) : "채널을 정해 주세요", b.last_seen_at ? ago(b.last_seen_at) : ""].filter(Boolean).join(" · ")))));
    const [label, tone] = BOT_STATE[b.state] || [b.state || "—", "line"];
    h.append(t, badge(label, tone));
    c.append(h);
    for (const ch of b.channels || []) {
      const r = el("div", "tm-bot-ch");
      const auto = el("button", `tm-auto${ch.auto_start ? " on" : ""}`, ch.auto_start ? "자동 시작 켜짐" : "자동 시작 꺼짐"); auto.type = "button";
      auto.title = "누르면 바뀌어요";
      auto.addEventListener("click", (e) => { e.stopPropagation(); void saveBot(b, { channels: b.channels.map((x) => (x === ch ? { ...x, auto_start: !x.auto_start } : x)) }); });
      r.append(raw(el("span", "mono", `#${ch.name || ch.channel_id}`)), raw(el("span", "", ch.label || tr(targetLabel(ch.action)))), auto);
      c.append(r);
    }
    const open = () => { S.bot = b.id; S.settingsOpen = false; S.botDraft = null; render(false); };
    c.addEventListener("click", open);
    c.addEventListener("keydown", (e) => { if (e.key === "Enter") open(); });
    return c;
  }
  function discordOverview() {
    const how = el("div", "tm-side-sec");
    how.append(el("h3", "tm-side-title", "채널에서 하는 네 가지"));
    const steps = [["채널에 글 쓰기", "질문을 올리면 연결된 워크플로우가 시작돼요"], ["@이름으로 부르기", "전문가에게 바로 물어봐요"], ["스레드로 회의", "팀 회의가 스레드에서 진행돼요"], ["/회의 멈춤", "진행 중인 회의를 멈춰요"]];
    const ol = el("ol", "tm-four");
    steps.forEach(([a, b], i) => { const li = el("li", ""); li.append(el("span", "mono", pad2(i + 1))); const t = el("div", ""); t.append(el("b", "", a), el("small", "", b)); li.append(t); ol.append(li); });
    how.append(ol);
    const s = S.discord.settings || {};
    const set = el("div", "tm-side-sec");
    set.append(sideLabel("설정"));
    const daily = el("b", ""); daily.append(s.daily_usd ? `하루 $${s.daily_usd}` : "없음"); if (s.daily_usd) daily.append(est());
    set.append(kv([["서버", s.guild_name || "아직 없음"], ["봇 쓰는 방식", BOT_MODES.find(([k]) => k === (s.bot_mode || "shared"))[1]], ["결과 보내는 곳", s.reply_to === "channel" ? "채널" : "같은 스레드"], ["비용 한도", daily]]));
    const acts = el("div", "tm-side-acts");
    acts.append(button("봇 설정", "", null, () => { S.settingsOpen = true; S.bot = null; render(false); }), button("권한 다시 확인", "ghost", null, () => Promise.all(bots().map((b) => checkBot(b, true))).then(() => notice("봇 권한을 다시 확인하라고 보냈어요. 5분 안에 결과가 와요."))));
    side(how, set, acts, el("p", "tm-side-text", "디스코드에서 시작한 일은 파일을 고치지 않고, 개인 선호 기억은 넣지 않아요. 허락한 사람의 글에만 답해요."));
  }
  async function checkBot(b, quiet) {
    try {
      await request(`/v1/discord/bots/${encodeURIComponent(b.id)}/check`, { method: "POST", body: body({}) });
      if (!quiet) notice(`${b.name} 권한을 다시 확인하는 중이에요.`);
      setTimeout(() => loadDiscord(true), 4000);
    } catch (e) { if (!quiet) notice(errorText(e)); }
  }
  // POST /v1/discord/bots/{id}/update takes only the editable fields (DiscordBotInput).
  const botFields = (b) => ({ name: b.name, channels: b.channels || [], ...(b.target ? { target: b.target } : {}), allowed: b.allowed || { user_ids: [], role_ids: [] }, ...(b.guild_id ? { guild_id: b.guild_id } : {}) });
  const botURL = (b) => `/v1/discord/bots/${encodeURIComponent(b.id)}/update`;
  async function saveBot(b, patch, okText) {
    try {
      const saved = await request(botURL(b), { method: "POST", body: body({ expected_revision: b.revision, bot: { ...botFields(b), ...patch } }) });
      const i = bots().findIndex((x) => x.id === b.id);
      if (i >= 0) S.discord.bots[i] = saved?.id ? bt(saved) : { ...b, ...patch, revision: (b.revision || 0) + 1 };
      S.botDraft = null;
      if (okText) notice(okText);
      render(false);
    } catch (e) { notice(errorText(e)); }
  }
  async function loadChannels(b) {
    if (S.channels.has(b.id)) return;
    S.channels.set(b.id, null);
    try { S.channels.set(b.id, (await request(`/v1/discord/bots/${encodeURIComponent(b.id)}/channels?${query()}`)).items || []); } catch { S.channels.set(b.id, []); }
    if (here("discord") && S.bot === b.id) render(false);
    chRedraw(S.pop);
  }
  function targetOptions() {
    const out = [["", "정하지 않음"]];
    for (const t of teams()) out.push([`meeting:${t.id}`, `팀 회의 · ${t.crew.name}`]);
    for (const w of workflows()) out.push([`workflow:${w.id}`, `워크플로우 · $${w.crew.handle}`]);
    for (const a of agents()) out.push([`agent:${a.id}`, `전문가 · @${a.crew.handle}`]);
    return out;
  }
  const actionKey = (a) => (a?.kind ? `${a.kind}:${a.id}` : "");
  const actionOf = (key) => { if (!key) return null; const [kind, ...id] = key.split(":"); return { kind, id: id.join(":") }; };
  function botSide(b) {
    if (S.botDraftFor !== b.id || !S.botDraft) { S.botDraft = clone({ channels: b.channels || [], allowed: b.allowed || { user_ids: [], role_ids: [] }, name: b.name }); S.botDraftFor = b.id; }
    const d = S.botDraft;
    void loadChannels(b);
    const head = el("div", "tm-side-sec");
    const [label, tone] = BOT_STATE[b.state] || [b.state || "—", "line"];
    const t = el("div", "tm-side-titlerow"); t.append(raw(el("h3", "tm-side-title", b.name)), badge(label, tone));
    head.append(el("p", "tm-side-kind", "봇"), t, kv([["서버", b.guild_name || S.discord.settings?.guild_name || "아직 초대 전"], ["토큰", b.token_last4 ? `••••${b.token_last4}` : "—"], ["마지막 확인", b.last_seen_at ? ago(b.last_seen_at) : "—"]]));
    const parts = [head];
    if ((b.missing || []).length) {
      const miss = el("div", "wf-attn small");
      const mt3 = el("div", ""); mt3.append(el("strong", "", "빠진 권한"), raw(el("p", "", b.missing.map((m) => `#${channelName(b, m.channel_id)}: ${(m.perms || []).join(", ")}`).join(" · "))));
      miss.append(mt3, button("권한 다시 확인", "", null, () => checkBot(b)));
      parts.push(miss);
    }
    if (!b.guild_id && b.application_id) parts.push(inviteBlock(b.application_id));
    const chs = el("div", "tm-side-sec");
    chs.append(sideLabel("채널", `${d.channels.length}개`));
    d.channels.forEach((ch, i) => {
      const box = el("div", "tm-chan-edit");
      const top = el("div", "tm-chan-top"); top.append(raw(el("b", "mono", `#${ch.name || ch.channel_id}`)));
      const del = el("button", "icon-btn plain tm-x"); del.type = "button"; del.setAttribute("aria-label", "채널 빼기"); del.append(icon("x"));
      del.addEventListener("click", () => { d.channels.splice(i, 1); render(false); });
      top.append(del);
      const tsel = select(targetOptions(), actionKey(ch.action), (v) => { ch.action = actionOf(v); ch.template_key = undefined; });
      tsel.setAttribute("aria-label", "부를 대상");
      const lab = raw(input(ch.label, (v) => { ch.label = v; }, { max: 60, placeholder: "보이는 이름 (비우면 대상 이름)" }));
      const auto = el("div", "tm-toggle bare"); auto.append(el("span", "", "글이 올라오면 바로 시작"), sw(ch.auto_start, "자동 시작", (v) => { ch.auto_start = v; render(false); }));
      box.append(top, tsel, lab, auto);
      chs.append(box);
    });
    const avail = (S.channels.get(b.id) || []).filter((c) => !d.channels.some((x) => x.channel_id === c.channel_id));
    if (S.channels.get(b.id) === null) chs.append(el("p", "tm-side-text", "채널 목록을 불러오는 중…"));
    else if (avail.length) chs.append(select([["", "+ 채널 추가"], ...avail.map((c) => [c.channel_id, `#${c.name}${c.parent ? ` · ${c.parent}` : ""}`])], "", (v) => {
      const c = avail.find((x) => x.channel_id === v); if (!c) return;
      d.channels.push({ channel_id: c.channel_id, name: c.name, label: "", action: null, auto_start: false }); render(false);
    }));
    else chs.append(el("p", "tm-side-text", b.guild_id ? "봇이 볼 수 있는 채널을 모두 넣었어요." : "서버에 초대하면 채널을 고를 수 있어요."));
    const who = el("div", "tm-side-sec");
    who.append(sideLabel("부를 수 있는 사람"));
    const ids = raw(input((d.allowed.user_ids || []).join(", "), (v) => { d.allowed.user_ids = v.split(/[\s,]+/).filter(Boolean); }, { placeholder: "디스코드 사용자 ID, 쉼표로 나눠요" }));
    who.append(ids, el("p", "tm-side-text", "비우면 서버 주인만 불러요. 다른 봇의 글에는 답하지 않아요."));
    const acts = el("div", "tm-side-acts");
    acts.append(button("저장", "primary", null, () => saveBot(b, { channels: d.channels, allowed: d.allowed }, "봇 설정을 저장했어요.")),
      button("권한 다시 확인", "", null, () => checkBot(b)), button("연결 끊기", "ghost", null, () => removeBot(b)));
    const back = link("← 디스코드 전체", () => { S.bot = null; S.botDraft = null; render(false); }, "tm-link quiet");
    side(back, ...parts, chs, who, acts);
  }
  async function removeBot(b) {
    if (!await ask(`'${b.name}' 봇 연결을 끊을까요? 디스코드 서버에서 봇이 나가지는 않아요.`)) return;
    try {
      await request(botURL(b), { method: "POST", body: body({ expected_revision: b.revision, archive: true }) });
      S.discord.bots = bots().filter((x) => x.id !== b.id); S.bot = null;
      notice("연결을 끊었어요."); render(false);
    } catch (e) { notice(errorText(e)); }
  }
  function modeChooser(value, onPick) {
    const box = el("div", "tm-modes two");
    for (const [k, label, text] of BOT_MODES) {
      const o = el("button", `tm-mode${value === k ? " active" : ""}`); o.type = "button";
      o.setAttribute("role", "radio"); o.setAttribute("aria-checked", String(value === k));
      const t = el("div", "tm-mode-title"); t.append(el("i", "tm-radio"), el("b", "", label));
      o.append(t, el("p", "", text));
      o.addEventListener("click", () => onPick(k));
      box.append(o);
    }
    return box;
  }
  function settingsSide() {
    const s0 = S.discord.settings || {};
    if (!S.setDraft) S.setDraft = { bot_mode: s0.bot_mode || "shared", reply_to: s0.reply_to || "same_thread", daily_usd: s0.daily_usd ?? 5, guild_id: s0.guild_id, guild_name: s0.guild_name };
    const d = S.setDraft;
    const head = el("div", "tm-side-sec"); head.append(el("p", "tm-side-kind", "봇 설정"), kv([["서버", s0.guild_name || "아직 없음"]]));
    const mode = el("div", "tm-side-sec"); mode.append(sideLabel("봇 쓰는 방식"), modeChooser(d.bot_mode, (k) => { d.bot_mode = k; render(false); }));
    const reply = el("div", "tm-side-sec"); reply.append(sideLabel("결과 보내는 곳"), select([["same_thread", "같은 스레드"], ["channel", "채널"]], d.reply_to, (v) => { d.reply_to = v; }));
    const daily = el("div", "tm-side-sec");
    const di = input(d.daily_usd, (v) => { d.daily_usd = Math.max(0, Number(v) || 0); }, { type: "number" }); di.min = 0; di.step = "0.5";
    const sfx = el("div", "tm-suffix"); sfx.append(el("span", "pre", "$"), di, el("span", "", "하루"));
    daily.append(sideLabel("비용 한도 (추정)"), sfx, el("p", "tm-side-text", "한도에 닿으면 멈추지 않고 스레드에서 먼저 물어봐요."));
    const acts = el("div", "tm-side-acts");
    acts.append(button("저장", "primary", null, saveSettings), button("닫기", "ghost", null, () => { S.settingsOpen = false; S.setDraft = null; render(false); }));
    side(head, mode, reply, daily, acts);
  }
  async function saveSettings(extra) {
    const s0 = S.discord.settings || {};
    const settings = { ...(S.setDraft || { bot_mode: s0.bot_mode, reply_to: s0.reply_to, daily_usd: s0.daily_usd, guild_id: s0.guild_id, guild_name: s0.guild_name }), ...(extra?.bot_mode ? extra : {}) };
    try {
      const r = await request("/v1/discord/settings", { method: "POST", body: body({ ...(s0.revision != null ? { expected_revision: s0.revision } : {}), settings }) });
      S.discord.settings = r?.discord_settings ? unwrap(r, "discord_settings") : { ...s0, ...settings, revision: (s0.revision || 0) + 1 };
      if (!extra?.bot_mode) { S.settingsOpen = false; S.setDraft = null; notice("설정을 저장했어요."); render(false); }
      return true;
    } catch (e) { notice(errorText(e)); return false; }
  }

  // 봇 연결: pick how bots are used, make one in the Developer Portal, turn on Message Content Intent, paste the
  // token (it goes to the host keyring through a short handoff), invite it with exactly the permissions it needs.
  function section(n, title, sub) { // numbered step card of the 봇 연결 screen
    const c = el("section", "tm-card tm-step");
    const h = el("div", "tm-step-head"); h.append(el("span", "tm-step-n", String(n)), el("h3", "", title));
    c.append(h);
    if (sub) c.append(el("p", "tm-step-sub", sub));
    return c;
  }
  function startConnect(retry, preset) {
    const s0 = S.discord?.settings || {};
    S.connect = { mode: s0.bot_mode || "shared", name: retry?.name || "", token: "", appId: retry?.application_id || "", intent: false, target: "", bot: null, busy: false, retry: retry?.id || null, from: { view: state.view, tab: state.settingsTab }, ...preset };
    S.bot = null; S.settingsOpen = false;
    if (!here("discord")) go("discord"); else render(false);
  }
  // Back goes to where 봇 연결 was opened (e.g. the team list), never into the hidden 디스코드 list.
  function leaveConnect() {
    const from = S.connect?.from;
    S.connect = null; clearTimeout(S.connectTimer);
    if (from && from.view !== "discord") { if (from.view === "settings") state.settingsTab = from.tab; go(from.view); } else render(false);
  }
  function connectSteps() {
    const c = S.connect;
    const back = link(S.connect.from && S.connect.from.view !== "discord" ? "← 돌아가기" : "← 디스코드", leaveConnect, "tm-link quiet");
    const s1 = section(1, "봇 쓰는 방식");
    s1.append(modeChooser(c.mode, (k) => { c.mode = k; render(false); }));
    const s2 = section(2, "디스코드에서 봇 만들기");
    const ol = el("ol", "tm-howto");
    const portal = el("a", "tm-link", "Discord 개발자 포털"); portal.href = "https://discord.com/developers/applications"; portal.target = "_blank"; portal.rel = "noreferrer";
    const li1 = el("li", ""); li1.append(portal, " 에서 New Application을 눌러 앱을 만들어요.");
    ol.append(li1, el("li", "", "General Information에서 Application ID를 복사해 아래에 넣어요."), el("li", "", "Bot 탭에서 Reset Token을 눌러 토큰을 복사해요."));
    s2.append(ol, field("Application ID", raw(input(c.appId, (v) => { c.appId = v.trim(); paintInvite(); }, { max: 24, placeholder: "1234567890123456789" })), "tm-mono-field"));
    const s3 = section(3, "Message Content Intent 켜기", "채널 글을 읽으려면 꼭 켜야 해요. 끄면 @이름과 자동 시작이 동작하지 않아요.");
    const path = el("p", "tm-path mono", "Bot › Privileged Gateway Intents › MESSAGE CONTENT INTENT");
    const chk = el("label", "tm-check"); const cb = el("input"); cb.type = "checkbox"; cb.checked = c.intent; cb.addEventListener("change", () => { c.intent = cb.checked; paintConnectButton(); });
    chk.append(cb, el("span", "", "켰어요"));
    s3.append(path, chk);
    const s4 = section(4, "토큰 넣기");
    const two = el("div", "tm-two");
    const tok = input(c.token, (v) => { c.token = v.trim(); paintConnectButton(); }, { type: "password", placeholder: "MTEx…" }); tok.autocomplete = "off"; tok.spellcheck = false;
    two.append(field("봇 이름", raw(input(c.name, (v) => { c.name = v; }, { max: 60, placeholder: c.mode === "per_target" ? "Design-Bot" : "Arkism" }))), field("봇 토큰", tok));
    s4.append(two);
    if (c.mode === "per_target") s4.append(field("이 봇이 부를 대상", select(targetOptions(), c.target, (v) => { c.target = v; })));
    s4.append(el("p", "tm-step-sub", "토큰은 서버 기록에 남지 않아요. 10분 안에 서버 PC의 키 저장소로 옮겨지고, 화면에는 끝 네 자리만 보여요."));
    const s5 = section(5, "서버에 초대하기", "아래 권한만 받아요. 관리자 권한은 받지 않아요.");
    const perms = el("ul", "tm-perms");
    for (const [en, ko] of PERMS) { const li = el("li", ""); li.append(el("b", "", ko), el("small", "", en)); perms.append(li); }
    S.inviteBox = el("div", "tm-invite");
    s5.append(perms, S.inviteBox);
    const foot = el("div", "tm-foot");
    S.connectBtn = button(c.bot ? "다시 연결" : "연결", "primary", null, submitConnect);
    foot.append(S.connectBtn, link("취소", leaveConnect, "tm-link plain"));
    S.connectState = el("span", "tm-connect-state");
    foot.append(S.connectState);
    const main = el("div", "tm-form-main");
    main.append(back, s1, s2, s3, s4, s5, foot);
    queueMicrotask(() => { paintInvite(); paintConnectButton(); });
    return [main];
  }
  function inviteBlock(appId) {
    const box = el("div", "tm-invite");
    const url = inviteURL(appId);
    const a = el("a", "btn", "초대 링크 열기"); a.href = url; a.target = "_blank"; a.rel = "noreferrer";
    box.append(raw(el("code", "tm-url", url)), a);
    return box;
  }
  function paintInvite() {
    if (!S.inviteBox || !S.connect) return;
    const id = S.connect.bot?.application_id || S.connect.appId;
    S.inviteBox.replaceChildren(...(/^\d{5,24}$/.test(id || "") ? inviteBlock(id).childNodes : [el("p", "tm-step-sub", "Application ID를 넣거나 봇을 연결하면 초대 링크가 생겨요.")]));
  }
  function paintConnectButton() {
    const c = S.connect;
    if (!S.connectBtn || !c) return;
    S.connectBtn.disabled = c.busy || !c.intent || c.token.length < 20;
    const b = c.bot;
    if (S.connectState) {
      S.connectState.replaceChildren();
      if (b) { const [label, tone] = BOT_STATE[b.state] || [b.state, "line"]; S.connectState.append(badge(label, tone)); if (b.state === "connected") S.connectState.append(link("채널 정하기 →", () => { if (c.from && c.from.view !== "discord") leaveConnect(); else { S.connect = null; S.bot = b.id; render(false); } })); }
      else if (!c.intent) S.connectState.append(el("small", "", "3번을 켜면 연결할 수 있어요."));
    }
  }
  async function submitConnect() {
    const c = S.connect;
    c.busy = true; paintConnectButton();
    try {
      const s0 = S.discord.settings || {};
      if ((s0.bot_mode || "shared") !== c.mode && !(await saveSettings({ bot_mode: c.mode }))) return;
      const bot = { name: (c.name || "").trim() || (c.mode === "per_target" ? "Team-Bot" : "Arkism"), token: c.token, ...(c.appId ? { application_id: c.appId } : {}) };
      const target = actionOf(c.target);
      if (c.mode === "per_target" && target) bot.target = target;
      const old = c.retry && botById(c.retry);
      const r = bt(old ? await request(botURL(old), { method: "POST", body: body({ expected_revision: old.revision, bot: { ...botFields(old), name: bot.name, token: bot.token } }) })
        : await request("/v1/discord/bots", { method: "POST", body: body({ bot }) }));
      c.token = ""; c.bot = r?.id ? r : { state: "waiting_token", name: bot.name };
      notice("토큰을 넘겼어요. 서버가 디스코드에 붙는 중이에요.");
      render(false);
      watchConnect();
    } catch (e) { notice(errorText(e)); } finally { c.busy = false; paintConnectButton(); }
  }
  function watchConnect() {
    clearTimeout(S.connectTimer);
    const c = S.connect;
    if (!c?.bot?.id || !here("discord")) return;
    S.connectTimer = setTimeout(async () => {
      await loadDiscord(true);
      const b = botById(c.bot.id);
      if (!S.connect || S.connect !== c) return;
      if (b) c.bot = b;
      paintInvite(); paintConnectButton();
      if (!b || ["waiting_token", "connecting"].includes(b.state)) watchConnect();
    }, 2000);
  }
  function connectSide() {
    const s = el("div", "tm-side-sec");
    s.append(el("h3", "tm-side-title", "봇 연결은 이렇게 돼요"));
    const ol = el("ol", "tm-four");
    [["토큰을 넘겨요", "이 앱 → 서버 Arkism, 10분만 메모리에"], ["키 저장소에 넣어요", "서버 PC의 키 저장소(arkism-discord)"], ["디스코드에 붙어요", "5분마다 권한을 확인해요"], ["채널을 정해요", "채널마다 팀·워크플로우·에이전트"]]
      .forEach(([a, b], i) => { const li = el("li", ""); li.append(el("span", "mono", pad2(i + 1))); const t = el("div", ""); t.append(el("b", "", a), el("small", "", b)); li.append(t); ol.append(li); });
    s.append(ol);
    side(s, el("p", "tm-side-text", "디스코드는 서버 하나만 연결해요. 디스코드에서 부른 일은 읽기만 하고 파일을 고치지 않아요."));
  }

  // ================================================================ 사용량 (325:37545)
  // Everything the usage API does not sum up is worked out here from the same data: runs and meetings are read once more
  // (newest 200 / 50) for each table's 시작·평균 시간·마지막 실패·마지막 회의; 쓰는 곳 comes from the crew records.
  const USAGE_FILTERS = [["all", "전체"], ["agent", "전문가"], ["team", "팀"], ["workflow", "자동화"]];
  async function loadUsage() {
    S.uState = "loading"; paintUsage();
    const ticket = (S.uTicket = (S.uTicket || 0) + 1);
    try {
      const [doc, runs, meets] = await Promise.all([
        request(`/v1/crew/usage?${query({ range: S.uRange })}`),
        request(`/v1/crew/runs?${query({ limit: 200 })}`).catch(() => null),
        request(`/v1/crew/meetings?${query({ status: "all", limit: 50 })}`).catch(() => null)]);
      if (!S.discord) await loadDiscord(true);
      if (ticket !== S.uTicket) return;
      if (!doc || !doc.kpi) throw new Error("invalid_response");
      S.usage = doc; S.uRuns = runs?.items || []; S.uMeets = meets?.items || []; S.uState = "ready";
    } catch (e) {
      if (ticket !== S.uTicket) return;
      S.usage = null; S.uState = unsupported(e) ? "unsupported" : "error"; S.uErr = e.message;
    }
    paintUsage();
  }
  const paintUsage = () => { if (here("crewUsage")) render(false); };
  function segBox(tabs, cur, onPick) {
    const s = el("div", "tm-seg"); s.setAttribute("role", "tablist");
    for (const [k, l] of tabs) { const t = el("button", `tm-seg-tab${k === cur ? " active" : ""}`, l); t.type = "button"; t.setAttribute("role", "tab"); t.addEventListener("click", () => onPick(k)); s.append(t); }
    return s;
  }
  const rangeLabel = () => (S.uRange === "today" ? "오늘" : S.uRange === "all" ? "전체 기간" : `최근 ${RANGES.find(([k]) => k === S.uRange)?.[1] || ""}`);
  const usageFrom = () => { const d = { today: 0, "7d": 7, "30d": 30, "90d": 90 }[S.uRange]; if (d == null) return 0; if (!d) { const t = new Date(); t.setHours(0, 0, 0, 0); return t.getTime(); } return Date.now() - d * 86400000; };
  const inRange = (rec) => Date.parse(rec.created_at || rec.crew_meeting?.created_at) >= usageFrom();
  const runAt = (r) => Date.parse(r.updated_at || r.created_at);
  const meetAt = (r) => Date.parse(r.crew_meeting.finished_at || r.crew_meeting.updated_at || r.updated_at);
  const sumBy = (rows, key) => rows.reduce((s, x) => s + (x[key] || 0), 0);
  const dur = (ms) => { const s = Math.round(ms / 1000); return s >= 60 ? `${Math.floor(s / 60)}분 ${String(s % 60).padStart(2, "0")}초` : `${s}초`; };
  const TRIGGERS = { manual: "직접", call: "직접", test: "직접", workflow: "자동화", schedule: "일정", webhook: "웹훅", discord: "디스코드", agent: "전문가" };
  // failed splits by kind: agent_failed, team_failed, and the rest of failed is automations (older servers: all automations).
  const dayParts = (x) => { const hasKinds = x.workflow != null || x.team != null || x.agent != null; const tf = x.team_failed || 0; return { agent: x.agent || 0, team: hasKinds ? x.team || 0 : 0, auto: hasKinds ? x.workflow || 0 : x.ok || 0, failed: (x.failed || 0) + (x.agent_failed || 0), fAgent: x.agent_failed || 0, fTeam: tf, fAuto: Math.max(0, (x.failed || 0) - tf) }; };
  function usageAvatar(rec, model) {
    const art = rec?.crew?.avatar?.art;
    if (!art || !/^[\w-]+$/.test(art)) return mark(model);
    const box = el("span", "tm-mark cu-face"); const img = el("img"); img.src = `img/avatars/${art}.png`; img.alt = ""; box.append(img);
    if (/^[\w-]+$/.test(rec.crew.avatar.bg || "")) box.style.background = `var(--avatar-${rec.crew.avatar.bg})`;
    return box;
  }
  function renderUsage() {
    const ctl = el("div", "tm-uctl");
    ctl.append(segBox(RANGES, S.uRange, (k) => { if (S.uRange !== k) { S.uRange = k; void loadUsage(); } }), segBox(USAGE_FILTERS, S.uKind, (k) => { if (S.uKind !== k) { S.uKind = k; render(false); } }));
    const span = el("span", "tm-urange mono");
    const days = { today: 1, "7d": 7, "30d": 30, "90d": 90 }[S.uRange];
    if (days) { const to = new Date(), from = new Date(Date.now() - (days - 1) * 86400000); const f = (x) => x.toLocaleDateString(globalThis.ArkismI18n?.locale() || "ko-KR", { month: "long", day: "numeric" }); span.textContent = days === 1 ? f(to) : `${f(from)} – ${f(to)}`; }
    ctl.append(span);
    $("#controls").replaceChildren(ctl);
    const table = $("#table");
    table.className = "tm-wrap tm-usage";
    if (S.uState === "idle") queueMicrotask(loadUsage);
    if (S.uState !== "ready") {
      table.replaceChildren(empty(S.uState === "unsupported" ? "이 서버는 아직 워크플로우·에이전트 사용량을 지원하지 않아요." : S.uState === "error" ? `사용량을 불러오지 못했어요 (${S.uErr}).` : "불러오는 중…"));
      return;
    }
    const d = S.usage, k = d.kpi, byDay = d.by_day || [];
    const kind = S.uKind;
    const direct = sumBy(d.agents || [], "direct"), meetings = sumBy(d.teams || [], "meetings"), autoRuns = sumBy(d.workflows || [], "runs");
    const total = direct + meetings + autoRuns;
    const ok = byDay.reduce((s, x) => { const p = dayParts(x); return s + p.agent + p.team + p.auto; }, 0), failed = byDay.reduce((s, x) => s + dayParts(x).failed, 0);
    const kp = el("div", "tm-ukpis");
    const tile = (value, label, sub, cls, extra) => { const c = el("div", `tm-ukpi ${cls || ""}`.trim()); const v = el("strong", "", value); if (extra) v.append(extra); c.append(v, el("p", "", label)); if (sub) c.append(sub); return c; };
    const waitSub = el("small", "");
    if (k.waiting) { waitSub.append(k.oldest_waiting_at ? `가장 오래된 것 ${ago(k.oldest_waiting_at)} ` : ""); waitSub.append(link("확인하기 →", () => go("crew"))); } else waitSub.append("기다리는 일이 없어요");
    const cost = Number(k.cost_usd || 0);
    kp.append(tile(total.toLocaleString("en-US"), "실행", el("small", "", `전문가 ${direct} · 팀 ${meetings} · 자동화 ${autoRuns}`)),
      tile(ok + failed ? pct((ok / (ok + failed)) * 100) : "—", "성공률", el("small", "", `끝난 ${ok + failed}번 중 실패 ${failed}번`)), // 실행 also counts runs still going or cancelled
      tile(`≈${cost >= 100 ? `$${Math.round(cost).toLocaleString("en-US")}` : usd(cost)}`, "비용", el("small", "", total ? `실행당 평균 ${usd(cost / total)}` : "")), // ≈ marks the estimate (325:37545 has no 추정 badge)
      tile(String(k.waiting || 0), "사람 확인 대기", waitSub, k.waiting ? "amber" : ""));
    const kids = [kp, dayChart(byDay)];
    if (kind === "all" || kind === "agent") kids.push(expertTable(d.agents || [], direct));
    if (kind === "all" || kind === "team") kids.push(teamTable(d.teams || [], meetings));
    if (kind === "all" || kind === "workflow") kids.push(autoTable(d.workflows || []), failingCard(d.failing_nodes || []));
    table.replaceChildren(...kids);
  }
  function dayChart(days) {
    const c = card("tm-chart", "날짜별 실행");
    const show = (key) => S.uKind === "all" || S.uKind === key;
    const parts = days.map((x) => { const p = dayParts(x); return { x, p, agent: show("agent") ? p.agent : 0, team: show("team") ? p.team : 0, auto: show("workflow") ? p.auto : 0, failed: (show("agent") ? p.fAgent : 0) + (show("team") ? p.fTeam : 0) + (show("workflow") ? p.fAuto : 0) }; });
    const ok = sumBy(parts, "agent") + sumBy(parts, "team") + sumBy(parts, "auto"), bad = sumBy(parts, "failed");
    const head = c.firstChild;
    head.append(el("p", "", `${rangeLabel()} · 성공 ${ok} · 실패 ${bad} · 막대에 올리면 전문가·팀·자동화로 나뉘어 보여요`));
    const legend = el("div", "tm-legend"); legend.append(el("span", "ok", "성공"), el("span", "bad", "실패")); head.append(legend);
    const sum = (q) => q.agent + q.team + q.auto + q.failed;
    const max = Math.max(1, ...parts.map(sum));
    const top = Math.ceil(max / 10) * 10 || 10;
    const plot = el("div", "tm-plot");
    const axis = el("div", "tm-axis"); [top, Math.round(top / 2), 0].forEach((v) => axis.append(el("span", "mono", String(v))));
    const cols = el("div", "tm-cols");
    const tip = el("div", "cu-tip"); tip.hidden = true;
    const step = Math.max(1, Math.ceil(days.length / 5));
    parts.forEach((q, i) => {
      const col = el("div", "tm-col");
      for (const [cls, v] of [["bad", q.failed], ["cu-ag", q.agent], ["team", q.team], ["auto", q.auto]]) { const b = el("i", cls); b.style.height = `${(v / top) * 100}%`; col.append(b); }
      const [, mm, dd] = (q.x.date || "").split("-");
      col.append(el("small", "mono", i % step === 0 || i === parts.length - 1 ? `${Number(mm)}.${Number(dd)}` : ""));
      const label = new Date(`${q.x.date}T12:00:00`).toLocaleDateString(globalThis.ArkismI18n?.locale() || "ko-KR", { month: "long", day: "numeric" });
      col.setAttribute("aria-label", `${label} · 실행 ${sum(q)} · 실패 ${q.failed}`);
      col.addEventListener("mouseenter", () => {
        tip.replaceChildren(el("b", "", `${label} · 실행 ${sum(q)}`));
        for (const [cls, name, v] of [["cu-ag", "전문가", q.agent], ["team", "팀", q.team], ["auto", "자동화", q.auto], ["bad", "실패", q.failed]]) { const r = el("p", ""); r.append(el("i", cls), el("span", "", name), el("em", "mono", String(v))); tip.append(r); }
        tip.hidden = false;
        const left = col.offsetLeft + col.offsetWidth + 8;
        tip.style.left = `${left + tip.offsetWidth > cols.clientWidth ? Math.max(0, col.offsetLeft - tip.offsetWidth - 8) : left}px`;
        col.classList.add("hot");
      });
      col.addEventListener("mouseleave", () => { tip.hidden = true; col.classList.remove("hot"); });
      cols.append(col);
    });
    if (!days.length) cols.append(empty("이 기간에는 실행 기록이 없어요."));
    cols.append(tip);
    plot.append(axis, cols);
    c.append(plot);
    return c;
  }
  // A table card: title + subtitle + link on the right, then a grid of rows (first column left, the rest right-aligned).
  function usageTable(title, sub, link_, cols, rows, emptyText, cls) {
    const [linkText, linkGo] = link_;
    const c = card("cu-card", title, sub);
    if (linkText) c.firstChild.append(link(linkText, linkGo));
    const t = el("div", `tm-utable cu-table ${cls}`);
    const h = el("div", "tm-urow head"); cols.forEach((x) => h.append(el("span", "", x))); t.append(h);
    for (const r of rows) t.append(r);
    if (!rows.length) t.append(empty(emptyText));
    c.append(t);
    return c;
  }
  function urow(cells, onClick) {
    const r = el(onClick ? "button" : "div", "tm-urow"); if (onClick) { r.type = "button"; r.addEventListener("click", onClick); }
    for (const x of cells) r.append(typeof x === "string" || x == null ? el("span", "mono", x ?? "—") : x);
    return r;
  }
  const lowRate = (v) => (v != null && v < 90 ? " amber" : "");
  const cell = (text, cls = "") => el("span", cls, text);
  function expertTable(rows, direct) {
    const list = rows.filter((a) => a.calls > 0);
    const body = list.map((a) => {
      const rec = byId(a.id), model = agentModel(rec);
      const name = el("span", "cu-name"); name.append(usageAvatar(rec, model), raw(el("span", "", `@${rec?.crew?.handle || a.name}`)));
      const rateV = a.calls ? ((a.calls - (a.failed || 0)) / a.calls) * 100 : null;
      const teamN = teams().filter((t) => (t.crew.members || []).some((m) => m.agent_id === a.id)).length;
      // An automation uses an agent through a subflow node that calls the agent's own workflow ($handle).
      const own = (rec?.crew?.workflow || "").replace(/^\$/, "");
      const autoN = own ? items().filter((r) => r.crew.type === "workflow" && r.crew.handle !== own && (r.crew.graph?.nodes || []).some((n) => n.type === "subflow" && (n.config?.workflow || "").replace(/^\$/, "") === own)).length : 0;
      const where = [autoN ? `자동화 ${autoN}` : "", teamN ? `팀 ${teamN}` : ""].filter(Boolean).join(" · ") || "—";
      const cost = el("span", "mono", usd(a.cost_usd));
      return urow([name, cell(modelLabel(model), "mono quiet"), cell(String(a.calls), "mono"), cell(pct(rateV), `mono${lowRate(rateV)}`), cell(num(a.tokens / a.calls), "mono"), cost, cell(where, "quiet")], () => openExpertHistory(a.id));
    });
    return usageTable("전문가", `${list.length}명 · 호출 ${sumBy(list, "calls")}번(단독 ${direct}) · 줄을 누르면 사용 기록을 봐요`, ["호출 기록 전체 보기 →", () => go("workers")],
      ["이름", "모델", "호출", "성공률", "평균 토큰", "비용", "쓰는 곳"], body, "이 기간에 부른 전문가가 없어요.", "cu-expert");
  }
  function teamTable(rows, meetings) {
    const recs = (S.uMeets || []).filter((m) => inRange(m) && m.crew_meeting);
    const body = rows.map((t) => {
      const mine = recs.filter((m) => m.crew_meeting.team_id === t.id).sort((a, b) => meetAt(b) - meetAt(a));
      const from = [...new Set(mine.map((m) => TRIGGERS[m.crew_meeting.trigger] || "직접"))];
      if (!from.length && channelsFor(t.id).length) from.push("디스코드");
      const last = mine[0];
      // the time is UI text and is translated; the meeting title is the user's and stays as written
      const lastCell = cell(last ? ago(new Date(meetAt(last))) : "—", "quiet cu-clip");
      if (last) lastCell.append(" · ", raw(el("span", "", last.crew_meeting.title || last.crew_meeting.topic || "")));
      return urow([raw(el("span", "", `@${byId(t.id)?.crew?.handle || t.name}`)), cell(from.map(tr).join(" · ") || "—", "quiet"), cell(String(t.meetings), "mono"), cell(pct(t.concluded_rate), `mono${lowRate(t.concluded_rate)}`), cell((t.avg_rounds || 0).toFixed(1), "mono"), cell(usd(t.cost_usd ?? t.avg_cost_usd * t.meetings), "mono"), lastCell], () => { go("meetings"); });
    });
    return usageTable("팀", `${rows.length}개 · 회의 ${meetings}번 · 줄을 누르면 회의 기록으로 가요`, ["회의 기록 전체 보기 →", () => go("meetings")],
      ["이름", "부르는 곳", "회의", "결론 난 비율", "평균 라운드", "비용", "마지막 회의"], body, "이 기간에 회의한 팀이 없어요.", "cu-team");
  }
  function autoTable(rows) {
    const runs = (S.uRuns || []).filter((r) => inRange(r) && r.crew_run);
    const body = rows.map((w) => {
      const rec = byId(w.id), x = rec?.crew || {};
      const mine = runs.filter((r) => r.crew_run.crew_id === w.id);
      const done = mine.filter((r) => r.crew_run.status === "succeeded");
      const avg = done.length ? done.reduce((s, r) => s + (runAt(r) - Date.parse(r.created_at)), 0) / done.length : null;
      const avgOK = Number.isFinite(avg) && avg >= 0;
      const bad = mine.filter((r) => r.crew_run.status === "failed").sort((a, b) => runAt(b) - runAt(a))[0];
      const reason = bad && Object.values(bad.crew_run.nodes || {}).find((n) => n.status === "failed" && n.error)?.error;
      const start = [x.trigger ? (x.trigger.kind === "schedule" ? "일정" : "웹훅") : "직접"];
      if (bots().some((b) => (b.channels || []).some((c) => c.action?.kind === "workflow" && c.action.id === w.id))) start.push("디스코드");
      const rateCell = cell(pct(w.success_rate), `mono${lowRate(w.success_rate)}`);
      return urow([raw(el("span", "", `$${x.handle || w.name}`)), cell(start.join("·"), "quiet"), cell(String(w.runs || 0), "mono"), rateCell, cell(avgOK ? dur(avg) : "—", "mono"), cell(usd(w.cost_usd), "mono"), raw(cell(bad ? `${ago(new Date(runAt(bad)))}${reason ? ` · ${reason}` : ""}` : "—", "quiet cu-clip"))],
        () => { go("crew"); state.selected = `crew:${w.id}`; render(); });
    });
    return usageTable("자동화", `${rows.length}개 · 줄을 누르면 실행 기록으로 가요`, ["실행 기록 전체 보기 →", () => go("crew")],
      ["이름", "시작", "실행", "성공률", "평균 시간", "비용", "마지막 실패"], body, "이 기간에 돈 자동화가 없어요.", "cu-auto");
  }
  // The expert popup (crew.js) opens over this page on its 사용량 tab; it listens for this event.
  function openExpertHistory(id) { window.dispatchEvent(new CustomEvent("arkism:open-expert", { detail: { id, tab: "usage", over: true } })); }
  // 실패한 곳부터 다시: the same request crew.js sends from a failed run (POST /v1/crew/runs, done nodes kept).
  const lastFailed = (wfId, nodeId) => (S.uRuns || []).filter((r) => r.crew_run?.crew_id === wfId && r.crew_run.status === "failed" && (!nodeId || r.crew_run.nodes?.[nodeId]?.status === "failed")).sort((a, b) => runAt(b) - runAt(a))[0];
  async function retryFailed(runs, what) {
    const todo = runs.filter((r) => r && byId(r.crew_run.crew_id));
    if (!todo.length) { notice("다시 돌릴 실패 기록을 찾지 못했어요. 실행 기록에서 열어 주세요."); return; }
    if (!await ask(`${what} 실패한 곳부터 다시 돌릴까요? 끝난 단계는 결과를 그대로 써요.`)) return;
    let ok = 0;
    for (const run of todo) {
      const rec = byId(run.crew_run.crew_id), cr = run.crew_run;
      const nodes = Object.fromEntries((rec.crew.graph?.nodes || []).map((n) => [n.id, { status: "pending" }]));
      for (const [id, st] of Object.entries(cr.nodes || {})) if (nodes[id] && st.status === "done") nodes[id] = { status: "done", output: st.output, port: st.port, runs: st.runs };
      const next = { crew_id: rec.id, crew_revision: rec.revision, trigger: "manual", status: "running", input: cr.input || JSON.stringify({ request: "" }), nodes, ...(cr.loops ? { loops: cr.loops } : {}) };
      try { await request("/v1/crew/runs", { method: "POST", body: body({ run: next }) }); ok++; } catch (e) { notice(`$${rec.crew.handle}을 다시 돌리지 못했어요 (${e.message}).`); }
    }
    if (ok) { notice(`${ok}개 실행을 맡겼어요. 서버 실행기가 10초 안에 시작해요.`); void loadUsage(); }
  }
  function failingCard(rows) {
    const total = sumBy(rows, "failures");
    const c = card("cu-card cu-fail", "실패가 많은 노드", `${rangeLabel()} · 실패 ${total}번이 ${rows.length}곳에서`);
    if (rows.length) c.firstChild.append(link("실패한 곳부터 다시 →", () => retryFailed([...new Set(rows.map((f) => f.workflow_id))].map((id) => lastFailed(id)), "실패한 자동화마다 마지막 실행을")));
    for (const f of rows) {
      const wf = byId(f.workflow_id);
      const node = (wf?.crew?.graph?.nodes || []).find((n) => n.id === f.node_id);
      const run = (S.uRuns || []).filter((r) => r.crew_run?.crew_id === f.workflow_id && r.crew_run.nodes?.[f.node_id]?.error).sort((a, b) => runAt(b) - runAt(a))[0];
      const reason = run?.crew_run.nodes[f.node_id].error;
      const r = el("div", "tm-ulist");
      const t = el("div", ""); t.append(raw(el("b", "", node?.title || f.node_id)), raw(el("small", "", `$${wf?.crew?.handle || f.workflow_id}${reason ? ` · ${reason}` : ""}`)));
      const right = el("div", "tm-ulist-right"); right.append(el("span", "amber mono", `${f.failures}번`), link("다시 실행", () => retryFailed([lastFailed(f.workflow_id, f.node_id)], `$${wf?.crew?.handle || f.workflow_id}의 마지막 실패 실행을`)));
      r.append(t, right); c.append(r);
    }
    if (!rows.length) c.append(empty("실패한 노드가 없어요."));
    return c;
  }

  // ---------------------------------------------------------------- pages (registered like crew.js pages)
  const pages = {
    teams: { title: "팀", count: () => `${teams().length}개`, sub: () => "전문가 여럿이 정한 순서대로 말하며 하나의 결과를 만들어요. 연결된 채널에 글을 쓰면 회의가 시작돼요.",
      bare: true, head: () => [button("회의 보기", "ghost", null, () => go("meetings"))], render: renderTeams },
    teamNew: { title: "새 팀", sub: () => "", keepDetail: true, head: () => [], render: () => { setTimeout(() => { go("teams"); setTimeout(() => openPopup(null)); }); $("#table").replaceChildren(); } },
    meetings: { title: "회의", sub: () => "팀이 정한 순서로 의견을 나누고 하나의 결론을 만들어요.", keepDetail: true, head: () => [], render: renderMeetings }, // 팀 보기 sits beside 새 회의 (208:19103)
    discord: { title: "디스코드", sub: () => "채널에서 에이전트·팀·워크플로우를 부르고, 결과를 채널로 받아요.", keepDetail: true, head: () => [], render: renderDiscord },
    crewUsage: { title: "사용량", sub: () => "워크플로우·전문가·팀이 얼마나 불리고 얼마나 썼는지 봐요. 비용은 공시 단가로 계산한 추정이에요.",
      bare: true, head: () => [button("새로고침", "ghost", null, loadUsage)], render: renderUsage } // like 팀: no repeated title, the button sits on the tab bar
  };
  return { pages, _test: { inviteURL, PERM_BITS, modelLabel, handleFrom } };
};
