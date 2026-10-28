// Boots the crew screens (crew.js and its modules, crew-automation.js, crew-popup.js, team-cover.js, team.js) on the
// small DOM in crew-dom.cjs, with app.js's helpers copied in their plain form and a recorded request router.
"use strict";
const path = require("path");
const dom = require("./crew-dom.cjs");

const UI = path.join(__dirname, "..");
const clone = (x) => (x === undefined ? undefined : JSON.parse(JSON.stringify(x)));
const iso = (min) => new Date(Date.UTC(2026, 9, 20, 9, 0, 0) + min * 60000).toISOString();

// ---------------------------------------------------------------- fixtures
function richGraph() {
  return {
    nodes: [
      { id: "start", type: "start", title: "", x: 0, y: 0, config: {} },
      { id: "ai1", type: "ai", title: "조사", x: 200, y: 0, config: { goal: "{{input}} 조사해서 {요약}", model: "sonnet", persona: "$리뷰어", max_workers: 3, collab: "on", access: "read" } },
      { id: "once", type: "ai_once", title: "", x: 400, y: 0, config: { prompt: "한 줄로 {{input}}" } },
      { id: "ex", type: "expert", title: "전문가 단계", x: 600, y: 0, config: { persona: "@reviewer", goal: "검토" } },
      { id: "cond", type: "condition", title: "통과?", x: 800, y: 0, config: { expr: "in.ok == true" } },
      { id: "code", type: "code", title: "다듬기", x: 1000, y: 0, config: { lang: "go", code: "package main", timeout: "30s" } },
      { id: "http", type: "http", title: "API", x: 1200, y: 0, config: { method: "DELETE", url: "https://api.example.com", headers: "A: b", body: "{}" } },
      { id: "gh", type: "github", title: "", x: 1400, y: 0, config: { args: "pr create --fill", repo_dir: "/srv/app" } },
      { id: "ok", type: "approval", title: "확인", x: 1600, y: 0, config: { question: "보낼까요?" } },
      { id: "loop", type: "loop", title: "반복", x: 1800, y: 0, config: { path: "in.items", concurrency: "2" } },
      { id: "wait", type: "wait", title: "", x: 2000, y: 0, config: { duration: "30m" } },
      { id: "sub", type: "subflow", title: "하위", x: 2200, y: 0, config: { workflow: "$daily", task: "{{input}}" } },
      { id: "merge", type: "merge", title: "", x: 2400, y: 0, config: {} },
      { id: "note", type: "notify", title: "알림", x: 2600, y: 0, config: { channel: "slack", to: "team", message: "끝" } },
      { id: "n8n", type: "n8n", title: "", x: 2800, y: 0, config: { url: "https://n8n.example.com/webhook/a", mode: "async", timeout: "30m" } },
      { id: "team", type: "team", title: "회의", x: 3000, y: 0, config: { team: "@release" } },
      { id: "end", type: "end", title: "", x: 3200, y: 0, config: { message: "요약 + 링크" } }
    ],
    edges: [
      { from: "start", to: "ai1" }, { from: "ai1", to: "once" }, { from: "once", to: "ex" }, { from: "ex", to: "cond" },
      { from: "cond", to: "code", port: "yes" }, { from: "cond", to: "end", port: "no" }, { from: "code", to: "http", port: "pass" },
      { from: "code", to: "ai1", port: "fail", max: 2 }, { from: "http", to: "gh" }, { from: "gh", to: "ok" }, { from: "ok", to: "loop", port: "approve" },
      { from: "ok", to: "end", port: "reject" }, { from: "loop", to: "wait" }, { from: "wait", to: "sub" }, { from: "sub", to: "merge" }, { from: "merge", to: "note" },
      { from: "note", to: "n8n" }, { from: "n8n", to: "team" }, { from: "team", to: "end" }
    ]
  };
}
const simpleGraph = () => ({
  nodes: [{ id: "start", type: "start", x: 0, y: 0, config: {} }, { id: "ai1", type: "ai", title: "일", x: 200, y: 0, config: { goal: "할 일 {{input}}", model: "auto" } }, { id: "end", type: "end", x: 400, y: 0, config: {} }],
  edges: [{ from: "start", to: "ai1" }, { from: "ai1", to: "end" }]
});

function crewRecords() {
  return [
    { id: "wf1", revision: 3, updated_at: iso(-60), crew: { type: "workflow", name: "릴리스 점검", handle: "release-check", role: "릴리스 전에 확인해요", category: "릴리스", model: "sonnet", enabled: true,
      trigger: { kind: "schedule", cron: "0 9 * * 1-5", label: "평일 9시" }, limits: { max_minutes: 30, max_steps: 40 }, goal: { task: "점검", done: "모두 통과", output: "보고서" },
      notify: { channels: [{ kind: "discord", channel: "c1" }], moments: ["start", "end"] },
      cover: { id: "ph1", url: "https://img/1.jpg", thumb: "https://img/1t.jpg", author: "Kim", author_url: "https://u/kim" }, graph: richGraph() } },
    { id: "wf2", revision: 1, updated_at: iso(-120), crew: { type: "workflow", name: "매일 요약", handle: "daily", role: "", category: "", model: "auto", enabled: false, trigger: { kind: "webhook" }, graph: simpleGraph() } },
    { id: "wf3", revision: 2, crew: { type: "workflow", name: "리뷰 순서", handle: "review-flow", category: "코드", graph: simpleGraph() } },
    { id: "ag1", revision: 4, updated_at: iso(-30), crew: { type: "agent", name: "리뷰어", handle: "reviewer", role: "코드를 봐요", category: "코드", model: "sonnet", fallback_model: "haiku", effort: "high", enabled: true,
      instructions: "## 역할\n리뷰", workflow: "$review-flow", skills: ["git-review", "missing-skill"], tools: ["github"], avatar: { art: "001", bg: "amber" },
      budget: { monthly_usd: 20, ask_over_usd: 2 }, pinned: [{ collection: "memories", id: "m1", injection: "full" }, { collection: "knowledge_sources", id: "k1" }], agents: ["claude_code"], memory: { kinds: ["lesson"] } } },
    { id: "ag2", revision: 1, crew: { type: "agent", name: "기획자", handle: "planner", role: "", model: "gpt-5", enabled: false, instructions: "계획" } },
    { id: "tm1", revision: 2, updated_at: iso(-10), crew: { type: "team", name: "릴리스 팀", handle: "release", role: "릴리스 회의", category: "릴리스", members: [{ id: "ag1", role: "worker" }, { id: "ag2", role: "moderator" }], mode: "round_robin", max_turns: 6,
      cover: { id: "ph2", url: "https://img/2.jpg", thumb: "https://img/2t.jpg", author: "Lee", author_url: "" } } },
    { id: "ps1", revision: 1, crew: { type: "persona", name: "꼼꼼이", handle: "careful", instructions: "꼼꼼하게", model: "sonnet" } },
    { id: "tp1", revision: 1, crew: { type: "template", name: "기본 틀", handle: "base", instructions: "틀", model: "haiku", effort: "low", skills: ["git-review"] } }
  ];
}
function runRecords() {
  return [
    { id: "run1", revision: 2, created_at: iso(-5), crew_run: { crew_id: "wf1", status: "waiting", trigger: "schedule", input: JSON.stringify({ request: "점검해 줘" }),
      waiting: { kind: "approval", node_id: "ok", question: "보낼까요?" }, loops: { "code>ai1:fail": 1, "x>y": 0 }, summary: "",
      nodes: { start: { status: "done", started_at: iso(-5), finished_at: iso(-5) }, ai1: { status: "done", output: JSON.stringify({ output: "조사 결과", workers: [{ worker: "w1", task: "a", ok: true }, { task: "b", ok: false }] }), started_at: iso(-5), finished_at: iso(-4), tokens: 1200, runs: 2 },
        once: { status: "done", output: "{\"a\":1}" }, cond: { status: "done", port: "yes" }, code: { status: "failed", error: "exit 1", output: "" }, ok: { status: "waiting" } } } },
    { id: "run2", revision: 1, created_at: iso(-50), crew_run: { crew_id: "wf1", status: "succeeded", trigger: "manual", input: "그냥 글", summary: "다 됐어요",
      nodes: { start: { status: "done", started_at: iso(-50), finished_at: iso(-49) }, end: { status: "done", output: "끝", started_at: iso(-49), finished_at: iso(-48) } } } },
    { id: "run3", revision: 1, created_at: iso(-80), crew_run: { crew_id: "wf2", status: "failed", trigger: "webhook", input: "{bad", nodes: { ai1: { status: "failed", error: "timeout" } } } },
    { id: "run4", revision: 1, created_at: iso(-90), crew_run: { crew_id: "wf2", status: "running", trigger: "call", input: "{}", nodes: { ai1: { status: "running", started_at: iso(-90) } } } }
  ];
}
const skills = () => [{ name: "git-review", description: "리뷰", category: "코드" }, { name: "write-doc", description: "문서" }];
const toolServers = () => [{ name: "github" }, { name: "notion" }];

// Default answers for the endpoints the screens read. Tests add their own in front.
function defaultRoutes(h) {
  return [
    ["GET", /^\/v1\/crew\?/, () => ({ items: h.data.crew })],
    ["GET", /^\/v1\/crew\/runs\/([^/?]+)\?/, (m) => h.data.runs.find((r) => r.id === decodeURIComponent(m[1])) || h.data.runs[0]],
    ["GET", /^\/v1\/crew\/runs\?/, (m, url) => { const id = new URLSearchParams(url.split("?")[1]).get("crew_id"); return { items: id ? h.data.runs.filter((r) => r.crew_run.crew_id === id) : h.data.runs }; }],
    ["POST", /^\/v1\/crew\/runs$/, (m, url, body) => ({ id: "run-new", revision: 1, created_at: iso(0), crew_run: { crew_id: body.crew_id, status: "running", input: body.input, nodes: {} } })],
    ["POST", /^\/v1\/crew\/runs\/[^/]+\/(decision|progress|cancel|retry)/, () => ({ ok: true, id: "run1", revision: 3, crew_run: { ...h.data.runs[0].crew_run, status: "running" } })],
    ["GET", /^\/v1\/crew\/versions\/([^/?]+)/, (m) => { const r = h.data.crew.find((x) => x.id === decodeURIComponent(m[1])); return { items: r ? [{ revision: r.revision, at: iso(-1), crew: r.crew }, { revision: r.revision - 1 || 1, at: iso(-100), crew: { ...r.crew, name: `${r.crew.name} 옛판`, skills: [], tools: ["notion"], budget: {}, avatar: null, graph: simpleGraph() } }, { revision: 1, at: iso(-200), crew: { ...r.crew, model: "haiku" } }] : [] }; }],
    ["GET", /^\/v1\/crew\/agents\/status/, () => ({ items: [{ id: "ag1", state: "busy", runs: 2 }, { id: "ag2", state: "idle" }] })],
    ["GET", /^\/v1\/crew\/agents\/[^/]+\/calls/, () => ({ items: [{ at: iso(-3), ok: true, latency_ms: 4200, model: "sonnet", trigger: "discord", input_tokens: 900, output_tokens: 300, cost_usd: 0.12 }, { at: iso(-7), ok: false, latency_ms: 0, model: "", trigger: "odd", error: "budget" }, { at: iso(-8), ok: true, latency_ms: 9000, model: "haiku", trigger: "test" }] })],
    ["GET", /^\/v1\/crew\/usage/, () => ({ agents: [{ id: "ag1", calls: 12, tokens: 2_500_000, cost_usd: 3.5 }, { id: "ag2", calls: 0, tokens: 900, cost_usd: 0 }], by_day: [{ agent: 2, agent_failed: 1 }, { agent: 0 }, { agent: 5 }], teams: [{ id: "tm1", meetings: 3, tokens: 4000, cost_usd: 0.5 }] })],
    ["GET", /^\/v1\/routing\/overview/, () => ({ router: "clef", models: ["sonnet", "haiku"] })],
    ["GET", /^\/v1\/unsplash\/search/, (m, url) => { const p = new URLSearchParams(url.split("?")[1]); if (p.get("q") === "none") return { items: [], total_pages: 0 }; return { items: [{ id: `u${p.get("page")}`, url: "https://u/1", thumb: "https://u/1t", author: "Park", author_url: "https://u/p", alt: "숲" }, { id: "ph1", url: "https://img/1.jpg", thumb: "https://img/1t.jpg", author: "Kim" }], total_pages: 3 }; }],
    ["POST", /^\/v1\/unsplash\/photos\//, () => ({ ok: true })],
    ["GET", /^\/v1\/discord(\?|$)/, () => clone(h.data.discord)],
    ["POST", /^\/v1\/discord\/bots\/[^/]+\/update/, (m, url, body) => ({ ...h.data.discord.bots[0], ...body })],
    ["GET", /^\/v1\/discord\/bots\/[^/]+\/channels/, () => ({ items: [{ id: "c1", name: "general" }, { id: "c2", name: "release" }, { id: "c3", name: "random" }] })],
    ["POST", /^\/v1\/discord\/bots/, () => ({ ...h.data.discord.bots[0] })],
    ["DELETE", /^\/v1\/discord\/bots\//, () => ({ ok: true })],
    ["GET", /^\/v1\/notify/, () => ({ destinations: [{ kind: "slack", name: "team" }, { kind: "telegram", name: "me" }] })],
    ["POST", /^\/v1\/crew\/(?!meetings$)([^/?]+)$/, (m, url, body) => { const r = h.data.crew.find((x) => x.id === decodeURIComponent(m[1])) || { id: decodeURIComponent(m[1]), revision: 0 }; return { id: r.id, revision: (r.revision || 0) + 1, crew: body.crew || r.crew }; }],
    ["POST", /^\/v1\/crew$/, (m, url, body) => ({ id: `new-${h.calls.length}`, revision: 1, crew: body.crew })],
    ["DELETE", /^\/v1\/crew\//, () => ({ ok: true })],
    ["GET", /^\/v1\/crew\/meetings\/([^/?]+)/, () => clone(h.data.meetings[0])],
    ["GET", /^\/v1\/crew\/meetings/, () => ({ items: clone(h.data.meetings) })],
    ["POST", /^\/v1\/crew\/meetings/, (m, url, body) => ({ ...clone(h.data.meetings[0]), id: "mt-new", ...body })],
    ["GET", /^\/v1\/crew\/templates/, () => ({ items: [] })],
    ["GET", /./, () => ({ items: [] })],
    ["POST", /./, () => ({ ok: true })]
  ];
}

// ---------------------------------------------------------------- app helpers (app.js, plain)
function appHelpers(h) {
  const doc = document;
  const el = (tag, cls, text) => { const n = doc.createElement(tag); if (cls) n.className = cls; if (text !== undefined && text !== null) n.textContent = text; return n; };
  const raw = (n) => { n.setAttribute("data-no-i18n", ""); return n; };
  const icon = (name, cls = "icon") => { const svg = doc.createElementNS("http://www.w3.org/2000/svg", "svg"); svg.setAttribute("class", cls); svg.setAttribute("aria-hidden", "true"); svg.dataset.icon = name; return svg; };
  const button = (label, cls, iconName, onClick) => { const b = el("button", `btn ${cls || ""}`.trim()); b.type = "button"; if (iconName) b.append(icon(iconName)); b.append(label); if (onClick) b.addEventListener("click", onClick); return b; };
  const $ = (s) => doc.querySelector(s);
  const segment = (tabs, current, onPick) => {
    const s = el("div", "segment");
    for (const [key, label, n, warn] of tabs) {
      const t = el("button", `tab${key === current ? " active" : ""}`); t.type = "button"; t.append(label);
      if (n !== undefined) t.append(el("output", warn && n ? "warn" : "", n === null ? "…" : String(n)));
      t.addEventListener("click", () => onPick(key)); s.append(t);
    }
    return s;
  };
  const detailShell = (label) => { const d = $("#detail"); const bar = el("div", "detail-bar"); bar.append(el("span", "", label)); const body = el("div", "detail-body"); d.replaceChildren(bar, body); return body; };
  const section = (title, aside) => { const s = el("section", "section"); const t = el("h3", "", title); if (aside) t.append(el("small", "", aside)); s.append(t); return s; };
  const facts = (pairs) => { const dl = el("dl", "facts"); pairs.filter(([, v]) => v).forEach(([k, v]) => dl.append(el("dt", "", k), el("dd", "", v))); return dl; };
  const callout = (title, text, iconName = "warning-circle") => { const c = el("div", "callout"); const t = el("div", ""); t.append(el("strong", "", title), el("p", "", text)); c.append(icon(iconName), t); return c; };
  const markSelected = () => { doc.querySelectorAll(".crew-card[data-id], .row[data-id]").forEach((r) => r.classList.toggle("selected", r.dataset.id === h.state.selected)); };
  const notice = (t) => { h.notices.push(t); };
  const ago = (v) => (v ? "방금" : "");
  const query = (extra = {}) => new URLSearchParams({ project_id: "p1", ...extra }).toString();
  const catIcon = (c) => (c ? "tag" : "squares-four");
  const catChipBody = (c) => [icon(c ? catIcon(c) : "tag"), raw(el("span", "", c || "카테고리")), icon("caret-down")];
  // The real menu lives in app.js; here a pick runs at once with the value the test sets (h.catPick).
  const catMenu = (anchor, { list, current, onPick }) => { h.catMenus.push({ list, current }); onPick(h.catPick === undefined ? (current ? "" : list[0] || "새 분류") : h.catPick); };
  const copyButton = (text) => { const b = el("button", "copy"); b.type = "button"; b.addEventListener("click", () => navigator.clipboard.writeText(text)); return b; };
  const headExtras = ({ count = "", back = null, aside = [] } = {}) => {
    $("#title-count").textContent = count;
    const head = $(".page-head");
    head.querySelector(".head-back")?.remove(); head.querySelector(".head-aside")?.remove();
    if (back) { const b = el("button", "head-back", `← ${back[0]}`); b.type = "button"; b.addEventListener("click", back[1]); head.prepend(b); }
    if (aside.length) { const a = el("div", "head-aside"); a.append(...aside); head.append(a); }
  };
  const agentLogo = (spec, name) => { const t = el("span", "agent-tile"); t.title = name; return t; };
  // pickerGroups: Anthropic with a MAX login and a key (two groups), OpenAI on a subscription with no plan; modelGroup as models.js draws it.
  const groups = [{ id: "anthropic", name: "Anthropic", badges: ["MAX"], models: ["sonnet", "haiku"] }, { id: "anthropic", name: "Anthropic", badges: ["API"], models: ["sonnet", "haiku"] }, { id: "openai", name: "OpenAI", badges: [], models: ["gpt-5"] }];
  const modelGroup = (p, valueOf = (m) => `${p.id}|${m}`) => {
    const g = el("optgroup"); g.label = p.name; g.dataset.badges = p.badges.join(",");
    for (const m of p.models) { const o = el("option", "", m); o.value = valueOf(m); o.dataset.badges = g.dataset.badges; g.append(o); }
    return g;
  };
  const models = { enabledModels: () => ["sonnet", "haiku", "gpt-5", "gemini-2.5-pro", "vision-x"], enabledByProvider: () => [{ id: "anthropic", name: "Anthropic", models: ["sonnet", "haiku"] }, { id: "openai", name: "OpenAI", models: ["gpt-5"] }],
    pickerGroups: () => groups, modelGroup };
  return { el, raw, icon, button, $, segment, detailShell, section, facts, callout, markSelected, notice, ago, query, catIcon, catChipBody, catMenu, copyButton, headExtras, agentLogo, models };
}

// ---------------------------------------------------------------- shell
function shell() {
  const doc = document;
  const app = doc.createElement("div"); app.id = "app";
  const head = doc.createElement("div"); head.className = "page-head";
  const line = doc.createElement("div"); line.className = "title-line";
  for (const id of ["title", "title-count"]) { const n = doc.createElement("span"); n.id = id; line.append(n); }
  const acts = doc.createElement("div"); acts.id = "head-actions";
  head.append(line, acts);
  const parts = ["controls", "chips", "stats", "table", "detail"].map((id) => { const n = doc.createElement("div"); n.id = id; return n; });
  app.append(head, ...parts);
  doc.body.append(app);
}

function loadScript(file) {
  const p = path.join(UI, file);
  delete require.cache[require.resolve(p)];
  require(p);
}

// boot({ view, data, routes, teams: true }) → harness with crew, teams pages and helpers.
async function boot(opts = {}) {
  const env = dom.install();
  shell();
  const h = { env, calls: [], notices: [], asks: [], catMenus: [], catPick: undefined, answer: true, typed: true, assists: [], chats: [], fields: [], needs: [] };
  h.data = { crew: crewRecords(), runs: runRecords(), meetings: opts.meetings || [], discord: opts.discord || { bots: [], settings: {} }, ...(opts.data || {}) };
  h.routes = [...(opts.routes || []), ...defaultRoutes(h)];
  h.request = async (url, o = {}) => {
    const method = o.method || "GET";
    const body = o.body ? JSON.parse(o.body) : undefined;
    h.calls.push({ method, url, body });
    for (const [m, re, fn] of h.routes) {
      if (m !== method) continue;
      const got = re.exec(url);
      if (!got) continue;
      const out = await fn(got, url, body);
      if (out instanceof Error) throw out;
      return clone(out);
    }
    return {};
  };
  // prompts the screens ask through window.ask / askTyped
  globalThis.ask = async (msg) => { h.asks.push(msg); return typeof h.answer === "function" ? h.answer(msg) : h.answer; };
  globalThis.askTyped = async (msg) => { h.asks.push(msg); return h.typed; };
  globalThis.askText = async (msg, v) => { h.asks.push(msg); return v; };
  globalThis.ArkismNeed = (node, msg) => { h.needs.push(msg); };
  h.assistReply = (kind, prompt) => ({ status: "done", output: JSON.stringify({ name: "새 동료", role: "돕는 일", instructions: "## 역할\n돕기", file_name: "new-mate", skills: ["git-review", "nope"], tools: ["github", "nope"], effort: "high" }) });
  const kit = {
    assist: async (kind, prompt, extra) => { h.assists.push({ kind, prompt, extra }); const r = h.assistReply(kind, prompt, extra); if (r instanceof Error) throw r; return r; },
    assistError: (a) => `AI 실패 (${a.status})`,
    parseJSON: (t) => { try { return JSON.parse(String(t).replace(/^```json|```$/g, "")); } catch { return null; } }
  };
  globalThis.ArkismAssist = () => kit;
  globalThis.ArkismAIChat = {
    button: (o) => { const b = document.createElement("button"); b.className = "ai-chat-btn"; b.type = "button"; b.textContent = o.label || "AI"; b.dock = document.createElement("span"); b.dock.className = "ai-dock"; h.chats.push(o); return b; },
    field: (input, o) => { const s = document.createElement("span"); s.className = "ai-field"; h.fields.push({ input, o }); return s; }
  };
  globalThis.ArkismBrand = { mark: (m) => { if (!m || m === "plain") return null; const s = document.createElement("span"); s.className = "brand"; return s; }, kindLabel: (m) => (m === "vision-x" ? "이미지" : m ? "텍스트" : ""), kind: (m) => (m === "vision-x" ? "vision" : "text") };
  globalThis.ArkismPerf = { note() {} };
  globalThis.ArkismVersionDiff = { render: (el, a, b, title) => { const d = el("div", "vdiff", title); return d; } };
  globalThis.ArkismPins = () => ({
    STATUS: { missing: ["없음", "지워진 자료"], stale: ["옛판", "새 판이 있어요"] }, MODES: [["full", "전체"], ["summary", "요약"], ["reference", "참조"]],
    loadLibrary() {}, resolution: () => ({}), statusOf: (rec, p) => ({ status: p.id === "k1" ? "missing" : "ok" }),
    candidates: (kind) => (kind === "skills" ? [{ id: "write-doc", title: "write-doc", sub: "문서" }, { id: "git-review", title: "git-review" }] : [{ id: "m9", title: "새 기억", collection: kind }]),
    known: (p) => (p.id === "m1" ? { title: "배포 규칙", body: "본문", tokens: 30 } : null),
    watch() {}, card: ({ cls }) => { const d = document.createElement("div"); d.className = `pins ${cls}`; return d; }
  });
  delete globalThis.ArkismI18n;
  if (opts.search) globalThis.location.search = opts.search;
  loadScript("crew-popup.js");
  loadScript("team-cover.js");
  loadScript("crew-automation.js");
  const a = appHelpers(h);
  h.a = a;
  h.state = {
    view: opts.view || "crew", data: { crew: h.data.crew, skills: skills(), tools: { servers: toolServers() } }, scope: { project_id: "p1" }, server: "local",
    connection: { generation: 1 }, crewTab: "all", crewCat: "all", selected: opts.selected || null, ...(opts.state || {})
  };
  h.go = (view, tab) => { h.state.view = view; if (tab) h.state.crewTab = tab; h.render(); };
  h.renders = 0;
  h.render = () => {
    h.renders++;
    const v = h.state.view;
    document.querySelectorAll(".page-back, .page-head .head-back, .page-head .head-aside").forEach((n) => n.remove());
    if (v === "settings") {
      const group = (t, sub) => { const g = a.el("section", "pg-group"); g.append(a.el("h3", "", t), a.el("p", "", sub)); return g; };
      document.querySelector("#table").replaceChildren(...h.crew.settingsTab({ group }));
      return;
    }
    const page = h.crew.pages[v];
    if (page) {
      document.querySelector("#controls").replaceChildren();
      const acts = document.querySelector("#head-actions");
      if (page.headInTitle) { acts.replaceChildren(); a.headExtras({ count: "", aside: page.head() }); } else acts.replaceChildren(...page.head());
      page.render();
      return;
    }
    if (v === "crew") { document.querySelector("#head-actions").replaceChildren(...h.crew.head()); h.crew.render(); h.crew.detail(); }
  };
  const ctx = { state: h.state, $: a.$, el: a.el, icon: a.icon, catIcon: a.catIcon, catMenu: a.catMenu, catChipBody: a.catChipBody, button: a.button, segment: a.segment, go: h.go, render: h.render,
    request: h.request, query: a.query, notice: a.notice, detailShell: a.detailShell, section: a.section, facts: a.facts, callout: a.callout, copyButton: a.copyButton, markSelected: a.markSelected,
    openDetail: () => {}, agentLogo: a.agentLogo, ago: a.ago, models: a.models, allowed: () => true, skillCategory: (sk) => sk.category || "", skillCategories: () => [{ name: "코드", icon: "code" }],
    toolServers: () => h.state.data.tools.servers };
  const { createCrew } = await import(require("url").pathToFileURL(path.join(UI, "crew.js")).href);
  h.crew = createCrew(ctx);
  if (opts.teams !== false) {
    loadScript("team.js");
    h.teamsApi = globalThis.ArkismTeams({ state: h.state, $: a.$, el: a.el, icon: a.icon, catIcon: a.catIcon, catMenu: a.catMenu, catChipBody: a.catChipBody, button: a.button, go: h.go, render: h.render,
      request: h.request, query: a.query, notice: a.notice, ago: a.ago, headExtras: a.headExtras, hasView: () => true, flowRuns: (...a) => h.crew.flowRuns(...a) });
    Object.assign(h.crew.pages, h.teamsApi.pages);
  }
  h.settle = async (ms) => { for (let i = 0; i < 4; i++) { await dom.flush(4); env.timers.tick(ms ?? 1000, 50); } await dom.flush(4); };
  h.dialogs = () => document.querySelectorAll("dialog[open]");
  h.top = () => { const d = h.dialogs(); return d[d.length - 1] || null; };
  h.closeAll = () => { for (const d of document.querySelectorAll("dialog")) { if (d.open) d.close(); d.remove(); } };
  h.btn = (root, text) => dom.byText(root || document, "button", text);
  h.click = async (root, text) => { const b = typeof text === "string" || text instanceof RegExp ? h.btn(root, text) : root; if (!b) throw new Error(`no button ${text}`); b.click(); await h.settle(); return b; };
  return h;
}

module.exports = { boot, dom, clone, iso, richGraph, simpleGraph, crewRecords, runRecords };
