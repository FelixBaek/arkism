// MCP·플러그인 (modules/toolViews.mjs): the list, details, switches, sign-in, reconnect and the add-server dialog.
const test = require("node:test");
const assert = require("node:assert/strict");
const { createDocument, house, flush } = require("./testdata/views-dom.js");
const mod = import("./modules/toolViews.mjs");

// Timers run on the next turn so polling, waiting and the 1.5 s auto-close finish inside a test.
let ticks = [];
globalThis.setTimeout = (fn, ms, ...a) => setImmediate(fn, ...a);
globalThis.clearTimeout = (id) => clearImmediate(id);
globalThis.setInterval = (fn) => { ticks.push(fn); return 1; };

const AGENTS = [["claude_code", "Claude Code", "cc"], ["codex", "Codex", "cx"], ["gemini", "Gemini", "gm"], ["hermes", "Hermes", "hm"]];
const NOW = new Date().toISOString();

function tools() {
  return {
    servers: [
      { name: "github", transport: "stdio", target: "npx gh", desc: "깃허브", agents: { claude_code: "on", codex: "off" }, calls: { today: 2, recent: [{ agent: "codex", tool: "list", ok: true, at: NOW }, { agent: "zz", tool: "push", ok: false, at: NOW }] },
        tools: Array.from({ length: 7 }, (_, i) => ({ name: `t${i}`, desc: i ? "" : "첫 도구", risky: i === 3 })) },
      { name: "cloudflare", transport: "http", target: "https://cf", agents: { claude_code: "on", codex: "on" }, login: { codex: "needed", claude_code: "ok" } },
      { name: "broken", transport: "stdio", note: "끊김", agents: { claude_code: "on" } },
      { name: "remote-broken", transport: "http", note: "인증 만료", agents: { codex: "on" } },
      { name: "off", transport: "http", agents: { claude_code: "off" } },
      { name: "arkism", transport: "stdio", agents: {} },
    ],
    plugins: [
      { name: "superpowers", transport: "plugin", version: "1.2", desc: "플러그인", agents: { claude_code: "on" }, contents: [{ kind: "skill", name: "brainstorm" }, { kind: "mcp", name: "db", desc: "디비", risky: true }, { kind: "hook", name: "h" }] },
      { name: "quiet", transport: "plugin", note: "고장", agents: { claude_code: "off" } },
    ],
    requests: [
      { id: "q1", name: "github", kind: "mcp", agent: "claude_code", status: "pending", enabled: true, requested_at: NOW },
      { id: "q2", name: "github", kind: "mcp", agent: "codex", status: "failed", enabled: false, error: "denied", requested_at: NOW },
      { id: "q3", name: "github", kind: "mcp", agent: "codex", status: "odd", enabled: true },
      { id: "q4", name: "new", kind: "mcp_add", status: "done" },
      { id: "q5", name: "cloudflare", kind: "mcp_login", status: "waiting", error: "x" },
      { id: "q6", name: "zz", kind: "mcp", status: "weird", enabled: false },
    ],
    collected_at: NOW,
  };
}

function setup({ handler, data = tools(), tauri = null } = {}) {
  ticks = [];
  const doc = createDocument();
  globalThis.document = doc;
  globalThis.window = { ArkismCatalog: { PRESETS: [{ id: "gh", name: "GitHub", how: "stdio" }, { id: "cf", name: "Cloudflare", how: "http" }] } };
  const h = house(doc);
  for (const id of ["controls", "chips", "stats", "table"]) doc.body.append(Object.assign(h.el("div"), { id }));
  const calls = [], notices = [], log = [], watched = [];
  const state = { token: "t", toolTab: "mcp", toolAgent: "", toolCat: "all", toolStatus: "all", selected: null, scope: { project_id: "p" }, server: "local", data: { tools: data, skills: [], crew: [] } };
  const deps = {
    ...h, state, AGENTS, generation: 1, tauri,
    AGENT_SYNC: { claude_code: "x", codex: "y" },
    agentName: (k) => Object.fromEntries(AGENTS.map(([a, n]) => [a, n]))[k] || k,
    agentLogo: (logo, n) => Object.assign(h.el("img", "logo"), { title: n }),
    query: () => "q=1",
    renderStatusCard: () => log.push("status"), render: (x) => log.push(["render", x]), renderDetail: () => log.push("detail"), openDetail: () => log.push("open"),
    notice: (t) => notices.push(t),
    headExtras: (x) => log.push(["head", x]),
    keepFocus: (sel) => { log.push(["keep", sel]); return () => log.push("restored"); },
    shownAgents: () => AGENTS.slice(0, 3),
    panelSearch: (ph) => { const w = h.el("div", "v4s-search"), i = h.el("input"); i.placeholder = ph; w.append(i); return w; },
    filterMenu: (key, groups) => Object.assign(h.el("div", "v4s-filter"), { key, groups }),
    barActions: (reload) => [h.button("새로고침", "", null, reload), h.button("설치")],
    matches: () => true,
    detailShell: (t) => { const b = h.el("div", "detail"); b.heading = t; doc.body.append(b); return b; },
    ago: () => "방금", copyButton: (t, label) => { const b = h.el("button", "copy", label || "복사"); b.copyText = t; return b; },
    catalog: {
      presetField: ({ name, how, cmd, url, sync }) => {
        const f = h.el("label", "field", "프리셋");
        const s = h.el("select");
        for (const v of ["", "gh", "cf", "unknown"]) { const o = h.el("option", "", v); o.value = v; s.append(o); }
        s.addEventListener("change", () => { if (s.value === "cf") { name.value = "cloudflare"; how.value = "http"; url.value = "https://cf.example/mcp"; } else { name.value = "github"; how.value = "stdio"; cmd.value = "npx -y 'server gh' \"--flag x\""; } sync(); });
        f.append(s);
        return f;
      },
      watchAdd: (r, n) => watched.push(n),
    },
    request: async (path, opt) => { const body = opt?.body && JSON.parse(opt.body); calls.push([path, body]); return handler ? handler(path, body, state) : { agent_tools: state.data.tools }; },
  };
  return { doc, h, deps, state, calls, notices, log, watched };
}
async function views(o) { const s = setup(o); const { createToolViews } = await mod; s.v = createToolViews(s.deps); return s; }
const until = async (fn, n = 200) => { for (let i = 0; i < n; i++) { const v = fn(); if (v) return v; await new Promise((r) => setImmediate(r)); } throw new Error("never happened"); };
const btn = (root, label) => root.querySelectorAll("button").find((b) => b.textContent.includes(label));
const dialogs = (s) => s.doc.body.children.filter((x) => x.tagName === "DIALOG");

test("loadTools fills defaults, reports a missing host as null, and the minute tick respects context", async () => {
  const s = await views({ handler: (path) => { if (s.fail) throw new Error("down"); return s.answer; } });
  s.answer = { agent_tools: { servers: [{ name: "a" }] } };
  const t = await s.v.loadTools();
  assert.deepEqual(t.plugins, []);
  assert.equal(t.agents, null);
  s.answer = {};
  assert.deepEqual((await s.v.loadTools()).servers, []);
  const warn = console.warn; console.warn = () => {};
  s.fail = true;
  assert.equal(await s.v.loadTools(), null);
  console.warn = warn;
  assert.equal(s.v.pendingTool(null), false);
  assert.equal(s.v.pendingTool(tools()), true);
  assert.equal(s.v.toolEntries().length, 8);
  s.state.data.tools = null;
  assert.deepEqual(s.v.toolEntries(), []);
  // The minute tick: skipped without a token or while hidden, kept on failure, dropped after a context change.
  const tick = ticks[0];
  s.state.token = ""; await tick(); assert.equal(s.log.length, 0);
  s.state.token = "t"; s.doc.hidden = true; await tick(); assert.equal(s.log.length, 0);
  s.doc.hidden = false;
  console.warn = () => {};
  await tick(); assert.deepEqual(s.log, ["status"]); assert.equal(s.state.data.tools, null);
  console.warn = warn;
  s.fail = false; s.answer = { agent_tools: { servers: [] } };
  await tick(); assert.deepEqual(s.state.data.tools.servers, []);
  s.answer = { agent_tools: { servers: [{ name: "late" }] } };
  const p = tick(); s.deps.generation = 2; await p;
  assert.deepEqual(s.state.data.tools.servers, []);
  // refreshTools reads and redraws.
  await s.v.refreshTools();
  assert.deepEqual(s.state.data.tools.servers, [{ name: "late" }]);
});

test("renderTools: tiles, stale warning, agent filter, rows, mini buttons and the plugin tab", async () => {
  const s = await views();
  s.state.data.tools.collected_at = "2020-01-01T00:00:00Z";
  s.v.renderTools();
  assert.match(s.notices[0], /최근 5분 동안/);
  const tiles = s.doc.querySelectorAll("#stats .cn-tile strong").map((x) => x.textContent);
  assert.deepEqual(tiles, ["1", "1", "2", "2"]);
  assert.equal(s.state.selected, null); // nothing opens until a row is clicked (the detail is a popup)
  const rows = s.doc.querySelectorAll("#table .cn-row");
  assert.equal(rows.length, 6);
  assert.match(rows[0].textContent, /깃허브 · 도구 7개/);
  assert.match(rows[0].textContent, /적용 대기/);
  assert.match(rows[1].textContent, /Codex 로그인 필요/);
  assert.match(rows[3].textContent, /다시 연결/);
  assert.match(rows[5].textContent, /꺼짐/);
  assert.equal(rows[5].querySelector(".cn-quiet").textContent, "에이전트 없음");
  rows[2].click();
  assert.equal(s.state.selected, "tool:broken");
  await rows[4].fire("keydown", { key: "Enter", target: rows[4], preventDefault() {} });
  assert.equal(s.state.selected, "tool:off");
  await rows[4].fire("keydown", { key: "a", target: rows[4] });
  await rows[4].fire("keydown", { key: " ", target: rows[0] });
  assert.equal(s.state.selected, "tool:off");
  // 로그인 mini opens the agent popup straight into sign-in (the row does not open).
  s.deps.request = async () => { throw new Error("no"); };
  btn(rows[1], "로그인").click();
  assert.equal(s.state.selected, "tool:off");
  const pop = dialogs(s).at(-1);
  assert.match(pop.textContent, /Codex · cloudflare/);
  await until(() => /로그인하지 못했어요: no/.test(pop.textContent));
  pop.close();
  // 다시 연결 mini on an http server opens the sign-in popup too.
  btn(rows[3], "다시 연결").click();
  assert.match(dialogs(s).at(-1).textContent, /Codex · remote-broken/);
  dialogs(s).at(-1).close();
  // Agent filter: an agent the host does not read gets a note; an unknown one resets.
  const menu = s.doc.querySelector("#controls .v4s-filter");
  assert.equal(menu.key, "mcp");
  assert.deepEqual(menu.groups.map((g) => g.title), ["카테고리", "상태", "에이전트"]);
  assert.deepEqual(menu.groups[2].chips.map((c) => `${c[0]} ${c[1]}`.trim()), ["모든 에이전트", "Claude Code 3", "Codex 2", "Gemini 0"]);
  assert.equal(menu.groups[1].chips.find((c) => c[2])[0], "전체");
  assert.deepEqual(s.doc.querySelectorAll("#controls .btn").map((b) => b.textContent), ["새로고침", "설치"]);
  menu.groups[2].chips[3][3]();
  assert.equal(s.state.toolAgent, "gemini");
  s.v.renderTools();
  assert.equal(s.doc.querySelector("#controls .v4s-filter").groups[2].set, true);
  assert.match(s.doc.querySelector("#chips").textContent, /Gemini의 MCP 설정은 아직 읽지 않아요/);
  assert.match(s.doc.querySelector("#table").textContent, /여기에 표시할 항목이 없어요/);
  s.state.toolAgent = "hermes"; s.v.renderTools();
  assert.equal(s.state.toolAgent, "");
  // Plugin tab.
  s.state.toolTab = "plugin"; s.state.selected = null; s.v.renderTools();
  const prow = s.doc.querySelectorAll("#table .cn-row");
  assert.equal(prow.length, 2);
  assert.equal(s.doc.querySelector("#controls .v4s-filter").key, "plugin");
  assert.match(prow[0].textContent, /v1\.2/);
  assert.match(prow[0].textContent, /스킬 1 · MCP 1 · hook 1/);
  assert.equal(s.state.selected, null);
  assert.equal(s.doc.querySelector("#stats").children.length, 0);
  // No report from the host.
  s.state.data.tools = null; s.v.renderTools();
  assert.match(s.doc.querySelector("#table").textContent, /MCP 목록을 불러오지 못했어요/);
  s.state.data.tools = tools();
  // Never collected: treated as stale.
  s.state.data.tools.collected_at = ""; s.state.toolTab = "mcp"; s.v.renderTools();
  assert.match(s.notices.at(-1), /최근 5분/);
});

test("server detail: facts, switches, tools with more, calls, history, skills, workflows and disconnect", async () => {
  const s = await views();
  s.state.data.skills = [
    { id: "k1", name: "github-mcp", description: "PR 리뷰" },
    { id: "k2", name: "other", description: "uses github api" },
    { id: "k3", name: "github", status: "deprecated" },
  ];
  s.state.data.crew = [
    { crew: { type: "workflow", handle: "reviewer", graph: { nodes: [{ type: "ai", config: { tools: "github" } }] } } },
    { crew: { type: "workflow", handle: "none", graph: {} } },
    { crew: { type: "expert", handle: "x" } },
  ];
  s.deps.request = async (path, opt) => {
    s.calls.push([path, opt && JSON.parse(opt.body)]);
    if (path === "/v1/skills/resolve") { const ref = JSON.parse(opt.body).refs[0]; if (ref.startsWith("k2")) throw new Error("x"); return { items: [{ bundle: { files: [{ path: "scripts/run.sh" }, { path: "SKILL.md" }] } }] }; }
    return { agent_tools: { requests: [...s.state.data.tools.requests, { id: "n", name: "github", kind: "mcp", agent: "claude_code", status: "done", enabled: false }] } };
  };
  const gh = s.state.data.tools.servers[0];
  s.v.toolDetail(gh);
  const d = s.doc.querySelector(".detail");
  assert.equal(d.heading, "MCP 서버");
  assert.match(d.textContent, /MCP 서버 · 적용 대기/);
  assert.match(d.textContent, /stdio · npx gh/);
  assert.match(d.textContent, /~\/\.claude\.json · ~\/\.codex\/config\.toml/);
  const chips = d.querySelectorAll(".cn-chip");
  assert.deepEqual(chips.map((c) => c.textContent), ["Claude Code · 켜는 중", "Codex", "Gemini", "Hermes · 지원 안 함"]);
  assert.equal(chips[3].title, "이 에이전트는 아직 켜고 끌 수 없어요");
  assert.match(d.textContent, /‘확인’ 표시는 바꾸거나 보내는 도구예요 · 1개/);
  assert.equal(d.querySelectorAll(".cn-sec")[2].querySelectorAll(".cn-subrow").length, 5);
  btn(d, "2개 더 보기").click();
  assert.equal(d.querySelectorAll(".cn-sec")[2].querySelectorAll(".cn-subrow").length, 7);
  assert.match(d.textContent, /오늘 2번/);
  assert.match(d.textContent, /성공/);
  assert.match(d.textContent, /\$reviewer/);
  assert.match(d.textContent, /연결된 스킬2개/);
  await until(() => d.querySelectorAll(".cn-sec").some((x) => x.textContent.startsWith("스크립트") && !x.hidden));
  assert.match(d.textContent, /scripts\/run\.sh/);
  assert.match(d.textContent, /최근 요청3건/);
  assert.match(d.textContent, /Codex 끄기방금 · denied/);
  assert.equal(btn(d, "다시 연결").textContent, "다시 연결");
  // Cached scripts: a second detail does not ask again; a server change does.
  const before = s.calls.length;
  s.v.toolDetail(gh); await flush();
  assert.equal(s.calls.length, before);
  s.state.server = "other"; s.v.toolDetail(gh); await flush();
  assert.equal(s.calls.length, before + 2);
  // A context change while a script answer is out drops it.
  s.state.server = "third";
  s.v.toolDetail(gh);
  s.state.server = "fourth";
  await flush();
  // 연결 끊기: cancel, then confirm.
  const foot = btn(d, "연결 끊기");
  foot.click();
  let pop = dialogs(s).at(-1);
  assert.match(pop.textContent, /연결 끊기할까요/);
  btn(pop, "취소").click();
  await flush();
  const n = s.calls.length;
  foot.click();
  pop = dialogs(s).at(-1);
  btn(pop, "연결 끊기").click();
  await until(() => s.notices.some((x) => /github 끄기를 요청했어요/.test(x)));
  assert.deepEqual(s.calls[n][1], { scope: { project_id: "p" }, agent: "claude_code", kind: "mcp", name: "github", enabled: false });
  // A server with no tools, no calls, nothing on, a short name.
  s.v.toolDetail({ name: "ab", transport: "http", agents: {} });
  const bare = s.doc.querySelectorAll(".detail").at(-1);
  assert.match(bare.textContent, /주소로 연결하는 서버는 로그인이 필요해/);
  assert.match(bare.textContent, /오늘은 이 서버의 도구를 부르지 않았어요/);
  assert.match(bare.textContent, /아직 없어요/);
  assert.equal(bare.querySelector(".cn-foot"), null);
  s.v.toolDetail({ name: "abc", transport: "stdio", agents: { gemini: "on" }, calls: { today: 0 } });
  assert.match(s.doc.querySelectorAll(".detail").at(-1).textContent, /하루에 한 번 도구 목록을/);
});

test("login and broken callouts in the detail, and a plugin's contents, remove and turn-off", async () => {
  const s = await views();
  const [, cf, broken] = s.state.data.tools.servers;
  s.deps.request = async () => { throw new Error("nope"); };
  s.v.toolDetail(cf);
  const d = s.doc.querySelector(".detail");
  assert.match(d.textContent, /Codex 로그인 필요/);
  btn(d, "Codex 로그인").click();
  await until(() => /로그인하지 못했어요/.test(dialogs(s).at(-1).textContent));
  dialogs(s).at(-1).close();
  s.v.toolDetail(broken);
  const b = s.doc.querySelectorAll(".detail")[1];
  assert.match(b.textContent, /끊김/);
  assert.match(b.textContent, /‘다시 연결’을 누르거나/);
  const plugin = tools().plugins[0];
  s.state.data.tools = tools();
  let answer = { agent_tools: { requests: [{ id: "r", kind: "plugin_remove" }] } };
  s.deps.request = async (path, opt) => { s.calls.push([path, JSON.parse(opt.body)]); if (answer instanceof Error) throw answer; return answer; };
  s.v.toolDetail(plugin);
  const p = s.doc.querySelectorAll(".detail")[2];
  assert.equal(p.heading, "플러그인");
  assert.match(p.textContent, /들어 있는 것3개/);
  assert.match(p.textContent, /확인/);
  assert.match(p.textContent, /켜 둔 에이전트1 \/ 4/);
  assert.equal(btn(p, "Codex").disabled, true);
  btn(p, "제거").click();
  btn(dialogs(s).at(-1), "취소").click(); await flush();
  assert.equal(s.calls.length, 0);
  btn(p, "제거").click();
  btn(dialogs(s).at(-1), "제거").click();
  await until(() => s.notices.some((x) => /superpowers 제거를 요청했어요/.test(x)));
  assert.equal(s.calls[0][1].kind, "plugin_remove");
  assert.deepEqual(s.state.data.tools.requests, [{ id: "r", kind: "plugin_remove" }]);
  answer = {};
  btn(p, "제거").click(); btn(dialogs(s).at(-1), "제거").click(); await flush();
  answer = new Error("fail");
  btn(p, "제거").click(); btn(dialogs(s).at(-1), "제거").click();
  await until(() => s.notices.some((x) => x === "요청하지 못했어요 (fail)."));
  btn(p, "끄기").click(); btn(dialogs(s).at(-1), "끄기").click();
  await until(() => s.notices.some((x) => x === "요청하지 못했어요: fail"));
  // A plugin with a note and nothing read yet.
  s.v.toolDetail(tools().plugins[1]);
  const q = s.doc.querySelectorAll(".detail")[3];
  assert.match(q.textContent, /\/plugin 으로 다시 확인/);
  assert.match(q.textContent, /서버가 아직 이 플러그인 폴더를 읽지 않았어요/);
});

test("agent popup switches a server on and off, and polls until the request lands", async () => {
  let polls = 0;
  const s = await views({ handler: (path, body, state) => {
    if (path.startsWith("/v1/agent_tools?")) { polls++; return { agent_tools: { ...state.data.tools, requests: polls > 2 ? [] : state.data.tools.requests } }; }
    return { agent_tools: { requests: [{ id: "x", name: body.name, kind: body.kind, status: "pending", agent: body.agent }] } };
  } });
  const gh = s.state.data.tools.servers[0];
  s.v.toolDetail(gh);
  const d = s.doc.querySelector(".detail");
  btn(d, "Codex").click();
  const pop = dialogs(s).at(-1);
  assert.match(pop.textContent, /지금 Codex에서 꺼져 있어요/);
  assert.ok(!/로그인이 필요할 수 있어요/.test(pop.textContent));
  btn(pop, "켜기").click();
  await until(() => s.notices.some((x) => /Codex에서 github 켜기를 요청했어요/.test(x)));
  await until(() => polls >= 3);
  assert.equal(pop.open, false);
  // On, and an answer without requests keeps the old list.
  s.deps.request = async (path) => (path.startsWith("/v1/agent_tools?") ? { agent_tools: { requests: [] } } : {});
  btn(d, "Claude Code").click();
  const off = dialogs(s).at(-1);
  assert.match(off.textContent, /지금 Claude Code에서 켜져 있어요/);
  btn(off, "끄기").click();
  await until(() => s.notices.some((x) => /github 끄기를 요청했어요/.test(x)));
  // The x closes the popup without doing anything.
  btn(d, "Gemini").click();
  dialogs(s).at(-1).querySelector(".icon-btn").click();
  assert.equal(dialogs(s).filter((x) => x.open).length, 0);
});

function loginHandler(s, { ready = "waiting", end = "done", mine = true, error } = {}) {
  let step = 0;
  return async (path, opt) => {
    const body = opt?.body && JSON.parse(opt.body);
    s.calls.push([path, body]);
    if (path === "/v1/agent_tools/requests") return { agent_tools: { requests: mine ? [{ id: "L1", kind: "mcp_login", agent: body.agent, name: body.name, status: "pending" }] : [] } };
    if (path.endsWith("/login")) { step = 2; return {}; }
    if (path.startsWith("/v1/agent_tools?")) {
      step++;
      if (step === 1) return { agent_tools: { requests: [{ id: "L1", status: "pending" }] } };
      if (step === 2) return { agent_tools: { requests: [{ id: "L1", status: ready, auth_url: "https://auth", error }] } };
      return { agent_tools: { requests: [{ id: "L1", status: end, error }] } };
    }
    return {};
  };
}

test("sign-in through the desktop app: waits for the server, opens the page, replays the callback", async () => {
  const invoked = [];
  const s = await views({ tauri: { invoke: async (cmd, a) => { invoked.push([cmd, a]); if (s.tauriError) throw s.tauriError; return "/cb?code=1"; } } });
  const cf = s.state.data.tools.servers[1];
  s.deps.request = loginHandler(s);
  s.v.toolDetail(cf);
  const d = s.doc.querySelector(".detail");
  btn(d, "Codex 로그인").click();
  const pop = dialogs(s).at(-1);
  await until(() => /Codex에 로그인했어요/.test(pop.textContent));
  assert.deepEqual(invoked, [["mcp_login", { authUrl: "https://auth" }]]);
  assert.deepEqual(s.calls.find((c) => c[0].endsWith("/login"))[1], { scope: { project_id: "p" }, callback: "/cb?code=1" });
  await until(() => !pop.open);
  // 다시 로그인 on an agent already signed in; a second click while busy does nothing.
  for (const [err, re] of [["port_busy", /받을 칸이 이미 쓰이고/], ["timeout", /10분 안에 로그인하지 않았어요/], ["other", /other/]]) {
    s.tauriError = err;
    s.deps.request = loginHandler(s);
    btn(d, "Claude Code").click();
    const p2 = dialogs(s).at(-1);
    const lg = btn(p2, "다시 로그인");
    lg.click(); lg.disabled = false; lg.click();
    await until(() => re.test(p2.textContent));
    p2.close();
  }
  s.tauriError = null;
  for (const [opts, re] of [[{ mine: false }, /요청을 찾지 못했어요/], [{ ready: "failed", error: "no browser" }, /no browser/], [{ ready: "failed" }, /로그인 창을 열지 못했어요/], [{ end: "failed", error: "denied" }, /denied/], [{ end: "failed" }, /실패/]]) {
    s.deps.request = loginHandler(s, opts);
    btn(d, "Claude Code").click();
    const p3 = dialogs(s).at(-1);
    btn(p3, "다시 로그인").click();
    await until(() => re.test(p3.textContent));
    p3.close();
  }
});

test("sign-in in a plain browser takes the pasted address; closing the popup or a slow server gives up", async () => {
  const s = await views();
  const cf = s.state.data.tools.servers[1];
  s.deps.request = loginHandler(s);
  s.v.toolDetail(cf);
  const d = s.doc.querySelector(".detail");
  btn(d, "Codex 로그인").click();
  const pop = dialogs(s).at(-1);
  const box = await until(() => pop.querySelector(".apply-paste"));
  assert.equal(box.querySelector("a").href, "https://auth");
  const input = box.querySelector("input");
  input.value = "https://evil.test/cb";
  btn(box, "확인").click();
  assert.match(pop.textContent, /http:\/\/localhost: 로 시작하는 주소/);
  input.value = "not a url";
  btn(box, "확인").click();
  input.value = " http://localhost:1234/callback?code=9 ";
  btn(box, "확인").click();
  await until(() => /Codex에 로그인했어요/.test(pop.textContent));
  assert.equal(s.calls.find((c) => c[0].endsWith("/login"))[1].callback, "/callback?code=9");
  // Closing the window while it waits for the pasted address.
  s.deps.request = loginHandler(s);
  btn(d, "Codex 로그인").click();
  const p2 = dialogs(s).at(-1);
  await until(() => p2.querySelector(".apply-paste"));
  p2.close();
  await flush();
  // The server never answers: the wait gives up.
  const realNow = Date.now;
  let now = realNow();
  Date.now = () => (now += 30000);
  s.deps.request = async (path, opt) => (path === "/v1/agent_tools/requests" ? { agent_tools: { requests: [{ id: "L9", kind: "mcp_login", agent: "codex", name: "cloudflare" }] } } : { agent_tools: { requests: [] } });
  btn(d, "Codex 로그인").click();
  const p3 = dialogs(s).at(-1);
  try { await until(() => /서버가 답하지 않아요/.test(p3.textContent)); } finally { Date.now = realNow; }
});

test("reconnect a program server: out and back in per agent, with its failures", async () => {
  const s = await views();
  const broken = { name: "broken", transport: "stdio", note: "끊김", agents: { claude_code: "on", codex: "on", hermes: "on" } };
  let fail = null, missing = false;
  s.deps.request = async (path, opt) => {
    const body = opt?.body && JSON.parse(opt.body);
    s.calls.push([path, body]);
    if (path === "/v1/agent_tools/requests") return { agent_tools: missing ? {} : { requests: [{ id: `${body.agent}${body.enabled}`, kind: "mcp", agent: body.agent, name: body.name, enabled: body.enabled }] } };
    return { agent_tools: { requests: ["claude_codefalse", "claude_codetrue", "codexfalse", "codextrue"].map((id) => ({ id, status: fail && id === "codextrue" ? "failed" : "done", error: fail === "e" ? "bad" : undefined })) } };
  };
  s.v.toolDetail(broken);
  const d = s.doc.querySelector(".detail");
  btn(d.querySelector(".cn-callout"), "다시 연결").click();
  const pop = dialogs(s).at(-1);
  assert.match(pop.textContent, /Claude Code·Codex 설정에서 broken을 뺐다가/);
  const go = btn(pop.querySelector(".dialog-foot"), "다시 연결");
  go.click(); go.disabled = false; go.click();
  await until(() => /다시 연결했어요/.test(pop.textContent));
  assert.deepEqual(s.calls.filter((c) => c[0] === "/v1/agent_tools/requests").map((c) => [c[1].agent, c[1].enabled]), [["claude_code", false], ["claude_code", true], ["codex", false], ["codex", true]]);
  for (const [f, m, re] of [["e", false, /다시 연결하지 못했어요: bad/], ["x", false, /다시 연결하지 못했어요: 실패/], [null, true, /요청을 찾지 못했어요/]]) {
    fail = f; missing = m;
    s.v.toolDetail(broken);
    btn(s.doc.querySelectorAll(".detail").at(-1).querySelector(".cn-callout"), "다시 연결").click();
    const p = dialogs(s).at(-1);
    btn(p.querySelector(".dialog-foot"), "다시 연결").click();
    await until(() => re.test(p.textContent));
  }
  // A healthy program server shows 다시 연결 among the buttons too.
  s.v.toolDetail({ name: "fine", transport: "stdio", agents: { codex: "on" } });
  btn(s.doc.querySelectorAll(".detail").at(-1).querySelector(".cn-btns"), "다시 연결").click();
  assert.match(dialogs(s).at(-1).textContent, /다시 연결 · fine/);
});

test("add server: presets, method switch, agent picks, validation, sends per agent and its errors", async () => {
  const s = await views();
  let fail = null;
  s.deps.request = async (path, opt) => { const body = JSON.parse(opt.body); s.calls.push([path, body]); if (fail) throw new Error(fail); return { agent_tools: s.answerEmpty ? {} : { requests: [{ id: "a", kind: "mcp_add", name: body.name, status: "pending" }] } }; };
  s.v.openAddServer();
  const dlg = dialogs(s).at(-1);
  assert.equal(dlg.open, true);
  assert.match(dlg.textContent, /최근 요청5건/);
  assert.match(dlg.textContent, /cloudflare 로그인로그인 대기 · x/);
  assert.match(dlg.textContent, /zz 끄기weird/);
  const inputs = dlg.querySelectorAll("input");
  const [name, cmd, url] = [inputs[0], inputs.find((i) => i.placeholder.startsWith("npx")), inputs.find((i) => i.placeholder.startsWith("https"))];
  const form = dlg.querySelector("form");
  const submit = () => form.fire("submit", { submitter: { value: "ok" }, preventDefault() {} });
  // Bad name → notice, nothing sent.
  name.value = "bad name!";
  await submit();
  assert.match(s.notices.at(-1), /이름, 프로그램이나 https 주소/);
  await form.fire("submit", { submitter: { value: "cancel" } });
  // Preset chips fill the form; http needs https.
  const presets = dlg.querySelectorAll(".mcp-preset");
  assert.deepEqual(presets.map((p) => p.textContent), ["GitHub프로그램", "Cloudflare주소", "unknown프로그램"]);
  presets[1].click();
  assert.ok(presets[1].classList.contains("on"));
  assert.equal(url.closest("label").hidden, false);
  assert.equal(cmd.closest("label").hidden, true);
  url.value = "http://plain";
  await submit();
  assert.equal(s.calls.length, 0);
  // Method pills, then agents: none picked is refused.
  const how = dlg.querySelectorAll(".mcp-how button");
  how[0].click();
  assert.ok(how[0].classList.contains("active"));
  presets[0].click();
  const sws = dlg.querySelectorAll(".mcp-agent .switch");
  assert.equal(sws.length, 3);
  sws[0].click(); sws[1].click();
  assert.equal(sws[0].getAttribute("aria-checked"), "false");
  await submit();
  assert.equal(s.calls.length, 0);
  sws[0].click(); sws[2].click();
  await submit();
  assert.deepEqual(s.calls.map((c) => [c[1].agent, c[1].command]), [["claude_code", ["npx", "-y", "server gh", "--flag x"]], ["gemini", ["npx", "-y", "server gh", "--flag x"]]]);
  assert.equal(dlg.open, false);
  assert.deepEqual(s.watched, ["github"]);
  assert.match(s.notices.at(-1), /github 추가를 요청했어요/);
  // http add, an answer without requests, then the two errors.
  s.answerEmpty = true;
  s.v.openAddServer();
  const d2 = dialogs(s).at(-1);
  d2.querySelectorAll(".mcp-preset")[1].click();
  await d2.querySelector("form").fire("submit", { submitter: { value: "ok" }, preventDefault() {} });
  assert.equal(s.calls.at(-1)[1].url, "https://cf.example/mcp");
  for (const [f, re] of [["secret_detected", /키나 토큰처럼 보이는 값/], ["boom", /요청하지 못했어요 \(boom\)/]]) {
    fail = f;
    s.v.openAddServer();
    const d3 = dialogs(s).at(-1);
    d3.querySelectorAll(".mcp-preset")[0].click();
    await d3.querySelector("form").fire("submit", { submitter: { value: "ok" }, preventDefault() {} });
    assert.match(s.notices.at(-1), re);
    assert.equal(btn(d3, "추가 요청").disabled, false);
  }
  // No requests sent yet.
  s.state.data.tools.requests = [];
  s.v.openAddServer();
  assert.match(dialogs(s).at(-1).textContent, /아직 보낸 요청이 없어요/);
  s.state.data.tools = null;
  s.v.openAddServer();
  dialogs(s).at(-1).close();
});

test("confirmPop resolves from the dialog's answer", async () => {
  const s = await views();
  const yes = s.v.confirmPop("제목", "본문", "확인");
  btn(dialogs(s).at(-1), "확인").click();
  assert.equal(await yes, true);
  const no = s.v.confirmPop("제목", "본문", "확인");
  dialogs(s).at(-1).close();
  assert.equal(await no, false);
});
