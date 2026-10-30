// 사용량 (usage.js): every menu's 개요 plus 요청·세션·시간대 and one session, drawn into a DOM stand-in from fixed server replies.
const test = require("node:test");
const assert = require("node:assert/strict");
require("./keep.js"); // the shared stale-reply and redraw guards the screens use
const { install, byText, text, flush } = require("./testdata/router-dom.js");

const dom = install();
window.ArkismModelBudget = { mount() {} };
require("./usage.js");

const at = "2026-10-02T10:00:00Z";
const session = { id: "s1", title: "고친 일", project: "arkism", agent: "claude", models: ["opus"], ended_at: at, started_at: at, device: "pc", tokens: 5000, input: 10, output: 20, cached: 3, cost_usd: 2, priced: true, active_seconds: 4000, messages: 7 };
const hours = Array.from({ length: 168 }, (_, i) => (i === 30 ? 9 : i % 11 === 0 ? 2 : 0));
function summary(over = {}) {
  return {
    authority: "data_only_untrusted", updated_at: at,
    kpi: { sessions: 4, tokens: 2_500_000_000, input: 15_000, output: 2_000_000, cost_usd: 150, priced: true, active_seconds: 40000, active_days: 3, cache_hit_pct: 40, cache_read: 5000, median_tokens: 300, projects: 2, root_sessions: 1 },
    by_day: [{ date: "2026-10-01", tokens: 10, cost_usd: 1, sessions: 1 }, { date: "2026-10-02", tokens: 20, cost_usd: 0, sessions: 2 }],
    by_agent: [{ name: "claude", tokens: 100, cost_usd: 1, priced: true, sessions: 2 }, { name: "zz", tokens: 5, cost_usd: 0, priced: false, sessions: 0 }],
    tools: [{ name: "mcp__git_hub__push", count: 5, sessions: 2 }, { name: "Read", count: 3, sessions: 1 }],
    skills: [{ name: "review", count: 2 }],
    hours, shape: [1, 2, 3, 0, 0, 0],
    projects: [{ name: "arkism", tokens: 5, cost_usd: 0, priced: false }], models: [{ name: "opus", tokens: 5, cost_usd: 1, priced: true }],
    top_sessions: [session, { ...session, id: "s2", title: "", project: "", models: null, ended_at: "", agent: "" }], latest: [session],
    agents: [{ name: "claude", all_sessions: 3 }, { name: "zz" }],
    requests: {
      count: 10, failed: 2, input: 100, output: 200, cost_usd: 0.5,
      models: [{ name: "openai/gpt", requests: 6, tokens: 100, cost_usd: 0.3, priced: true }, { name: "b", requests: 2, tokens: 1, cost_usd: 0.1 }, { name: "c", requests: 1, tokens: 1 }, { name: "d", requests: 1, tokens: 1, cost_usd: 0.1 }],
      items: [
        { model: "openai/gpt", decision: "clef", ok: true, input: 10, output: 5, latency_ms: 1200, at, cost_usd: 0.01 },
        { model: "b", replaced: "openai/gpt", decision: "failover", ok: true, input: 1, output: 1, latency_ms: 10, at },
        { model: "b", decision: "fixed", rule: "code", ok: false, error: "boom", input: 1, output: 0, latency_ms: 10, at },
        { model: "c", decision: "", ok: false, input: 1, output: 0, latency_ms: 10, at },
        { model: "openai/gpt", decision: "clef", ok: true, input: 1, output: 1, latency_ms: 1, at },
      ],
    },
    ...over,
  };
}
const daily = [{ t: "2026-10-01T00:00:00Z", traffic: 3, cost: 1 }, { t: "2026-10-02T00:00:00Z", traffic: 5, cost: 2 }];
const overviewDoc = (points = daily) => ({ authority: "data_only_untrusted", kpi: { p95_ms: 2000, p50_ms: 1000, requests: 5, success_rate: 99 },
  models: [{ id: "openai/gpt", name: "GPT", success_rate: 97, p95_ms: 1500 }, { id: "b", success_rate: 99 }], series: { points } });

function mk({ menu = "router", doc = summary(), ov = overviewDoc(), fail = null, extra = {} } = {}) {
  const state = { menu, view: "usage", scope: { project_id: "p" } };
  const nodes = Object.fromEntries(["#table", "#stats", "#chips", "#controls", "#detail", ".main > .page-head"].map((k) => [k, dom.el("div")]));
  const calls = [];
  const went = [];
  const ctx = {
    state, $: (s) => nodes[s], el: dom.el, button: dom.button, segment: dom.segment, agentLogo: dom.agentLogo, AGENTS: [["claude", "Claude Code", "img:claude.svg"]], ago: () => "방금",
    query: (o) => new URLSearchParams(o || {}).toString(),
    request: async (path) => {
      calls.push(path);
      if (fail) throw new Error(fail);
      if (path.startsWith("/v1/routing/overview")) { if (ov instanceof Error) throw ov; return ov; }
      return typeof doc === "function" ? doc() : doc;
    },
    render: () => u.views.usage.render(), detailShell: () => { const b = dom.el("div"); nodes["#detail"].replaceChildren(b); return b; },
    go: (v) => went.push(v), openSettings: (v) => went.push(`settings:${v}`), openSession: (id) => went.push(`session:${id}`),
    skillAgents: () => ["claude", "unknown"],
    ...extra,
  };
  const u = window.ArkismUsage(ctx);
  return { u, nodes, calls, state, went, ctx, table: nodes["#table"] };
}
async function open(v) { v.u.views.usage.render(); for (let i = 0; i < 4; i++) await flush(); return v; }
const click = (root, t) => { const all = byText(root, t), n = all.find((x) => x.tagName === "BUTTON") || all[0]; assert.ok(n, `no node with text ${t}`); n.click(); };

test("ROUTER 라우팅 흐름 counts rule-routed calls in their own lane, not 고정 모델", async () => {
  const it = (o) => ({ model: "b", ok: true, input: 1, output: 1, latency_ms: 1, at, ...o });
  const items = [it({ decision: "fixed", rule: "code" }), it({ decision: "fixed", rule: "writing" }), it({ decision: "fixed" })];
  const v = await open(mk({ doc: summary({ requests: { count: 3, failed: 0, input: 3, output: 3, cost_usd: 0, models: [{ name: "b", requests: 3, tokens: 6 }], items } }) }));
  assert.match(text(v.table), /규칙으로2건 · 66\.7%/);
  assert.match(text(v.table), /고정 모델1건 · 33\.3%/);
  click(v.table, "b"); // one model: its main route is the rule lane
  assert.match(text(v.table), /규칙으로 66\.7%주로 받은 경로/);
});

test("ROUTER 개요 shows routed calls, the flow card, model share and picks one model", async () => {
  const v = await open(mk());
  assert.match(text(v.table), /라우팅 흐름/);
  assert.match(text(v.table), /기타 1개/);
  assert.match(text(v.table), /응답 시간1\.0초보통 걸린 시간/);
  assert.doesNotMatch(text(v.table), /p95|p50|공시|MEMORY·FLOW/);
  assert.doesNotMatch(text(v.nodes["#controls"]), /결정/); // ROUTER keeps routing only; decision counts live in the other menus
  assert.equal(v.u.count(), "10");
  assert.match(v.u.views.usage.sub(), /라우터가 보낸 요청/);
  assert.equal(v.u.views.usage.keepDetail, false);
  click(v.table, "비용"); // day chart metric
  click(v.table, "GPT");
  assert.match(text(v.table), /받은 요청/);
  assert.match(text(v.table), /만 진하게/);
  click(v.table, "전체 모델");
  click(v.table, "요청"); // traffic metric back
  assert.equal(v.u.views.usage.title(), "사용량");
  v.u.views.usage.head()[0].click();
  await flush();
  v.u.leave();
  v.u.reset();
  assert.equal(v.u.count(), "");
});

test("ROUTER 개요 labels sub-day points by time, several days by date and time, and other ranges by a hint", async () => {
  const hourly = [{ t: "2026-10-01T00:00:00Z", traffic: 1 }, { t: "2026-10-01T01:00:00Z", traffic: 2 }];
  const multi = [{ t: "2026-10-01T00:00:00Z", traffic: 1 }, { t: "2026-10-01T06:00:00Z", traffic: 2 }, { t: "2026-10-03T00:00:00Z", traffic: 2 }];
  for (const [pts, range] of [[hourly, "오늘"], [multi, "7일"]]) {
    const v = await open(mk({ ov: overviewDoc(pts) }));
    click(v.nodes["#controls"], range);
    for (let i = 0; i < 4; i++) await flush();
    assert.match(text(v.table), /시간별/);
  }
  const v = await open(mk({ ov: { authority: "data_only_untrusted", kpi: {} } }));
  click(v.nodes["#controls"], "90일");
  for (let i = 0; i < 4; i++) await flush();
  assert.match(text(v.table), /날짜별 그래프는 오늘·7일·30일/);
  assert.match(text(v.table), /대신 받은 요청/);
  click(v.nodes["#controls"], "90일"); // same range: nothing reloads
});

test("이번 달 asks the server for the month in the reader's time zone", async () => {
  const calls = [];
  const request = async (path) => { calls.push(path); return path.startsWith("/v1/routing/models") ? { authority: "data_only_untrusted", items: [{ id: "openai/gpt", name: "GPT 화면 이름" }] } : summary(); };
  const v = await open(mk({ extra: { request } }));
  v.calls = calls;
  click(v.nodes["#controls"], "이번 달");
  for (let i = 0; i < 4; i++) await flush();
  const tz = -new Date().getTimezoneOffset();
  assert.ok(v.calls.includes(`/v1/usage/summary?range=month&tz=${tz}`), v.calls.join(" "));
  assert.ok(!v.calls.some((c) => c.startsWith("/v1/routing/overview?range=month")), "the routing overview has no month range");
  assert.ok(v.calls.some((c) => c.startsWith("/v1/routing/models")), "names come from the model list instead");
  assert.match(text(v.table), /GPT 화면 이름/);
  assert.match(text(v.table), /날짜별 그래프는 오늘·7일·30일/);
});

test("ROUTER with no routed calls points to the routing settings; a failed overview read is ignored", async () => {
  const doc = summary({ requests: { count: 0, failed: 0, input: 0, output: 0, cost_usd: 0, models: [], items: [] } });
  const v = await open(mk({ doc, ov: new Error("down") }));
  assert.match(text(v.table), /아직 라우터를 거친 요청이 없어요/);
  click(v.table, "라우팅 설정 열기");
  click(v.table, "라우팅 현황 보기 ›");
  assert.deepEqual(v.went, ["settings:routing", "routing"]);
  assert.equal(v.u.count(), "");
});

test("요청 tab lists each call and the side panel sums failures, fallbacks and speed", async () => {
  const v = await open(mk());
  click(v.nodes["#controls"], "요청");
  assert.match(text(v.table), /실패 · boom/);
  assert.match(text(v.table), /대신 받음/);
  assert.match(text(v.nodes["#detail"]), /응답 속도/);
  // one plain reason per request, tokens named; the slow tail waits under 자세히
  for (const t of [/판정 모델이 골라서 → GPT입력 10 · 출력 5 · 1\.2초/, /GPT에서 실패해 넘김 → b/, /코딩 요청이라 → b/, /기본 모델이라 → c/]) assert.match(text(v.table), t);
  assert.match(text(v.nodes["#detail"]), /보통1\.0초/);
  assert.match(text(v.nodes["#detail"]), /자세히느릴 때2\.0초100건 중 가장 느린 5건/);
  assert.doesNotMatch(text(v.nodes["#detail"]), /p95|p50/);
  assert.equal(v.u.views.usage.keepDetail, true);
  assert.match(v.u.views.usage.sub(), /라우터를 거친 요청의/);
  click(v.nodes["#detail"], "라우팅 현황에서 보기 ›");
  click(v.nodes["#detail"], "새로고침");
  await flush();
  const empty = await open(mk({ doc: summary({ requests: { count: 0, failed: 0, input: 0, output: 0, cost_usd: 0, models: [], items: [] } }), ov: { authority: "data_only_untrusted", kpi: { requests: 1 } } }));
  click(empty.nodes["#controls"], "요청");
  assert.match(text(empty.table), /이 기간에 Arkism 라우터를 거친 요청이 없어요/);
  assert.match(text(empty.nodes["#detail"]), /p50—|—/);
});

test("MEMORY 개요 without memory_use keeps session numbers; with it shows the memory flow and tidy list", async () => {
  const plain = await open(mk({ menu: "memory" }));
  assert.match(text(plain.table), /PC 수집기가 보고하면/);
  assert.match(text(plain.table), /가장 바쁜 때/);
  assert.match(plain.u.views.usage.sub(), /기억이 답변에/);
  const opened = [];
  const now = new Date().toISOString();
  const memory = () => ({
    use: { days: 7, injections: 10, used: 3, inject_sessions: 2, items: [{ id: "m1", injected: 4, used: 3 }, { id: "m2", injected: 6, used: 0 }, { id: "m3", injected: 1, used: 0 }] },
    memories: [{ id: "m1", kind: "lesson", text: "a", created_at: now }, { id: "m2", kind: "decision", text: "b", created_at: now }, { id: "m4", kind: "lesson", text: "d", created_at: "2000-01-01" }],
    often: (x) => x.id !== "m1", kindLabel: (k) => `종류 ${k}`, openIdle: () => opened.push("idle"), openMemory: (id) => opened.push(id),
  });
  const v = await open(mk({ menu: "memory", extra: { memory }, doc: summary({ hours: Array(168).fill(0), skills: [] }) }));
  assert.match(text(v.table), /답변 문구 겹침 비율/);
  assert.match(text(v.table), /30%/);
  assert.match(text(v.table), /종류 lesson/);
  click(v.table, "정리하기 →");
  click(v.table, "모두 보기 →");
  v.table.all((n) => n.classList.contains("v4u-tidy"))[0].click();
  assert.deepEqual(opened, ["idle", "idle", "m2"]);
  const none = await open(mk({ menu: "memory", extra: { memory: () => ({ use: { days: 7, injections: 0, used: 0, items: [] } }) } }));
  assert.match(text(none.table), /아직 답변에 들어간 기억이 없어요/);
  assert.match(text(none.table), /정리할 기억이 없어요/);
});

test("CREW and TOOLS 개요 put agents and tools first; empty periods say so", async () => {
  const crew = await open(mk({ menu: "agent" }));
  assert.match(text(crew.table), /에이전트별/);
  assert.match(text(crew.table), /세션당/);
  const tools = await open(mk({ menu: "connect" }));
  assert.match(text(tools.table), /git_hub/);
  assert.doesNotMatch(text(tools.table), /Read/, "많이 쓴 도구 lists MCP tools only (325:34690)");
  const health = { calls: 100, failed: 3, p50_ms: 400, p95_ms: 2000, avg_ms: 600, servers: [{ name: "git_hub", calls: 100, failed: 3, p95_ms: 2000 }],
    by_day: [{ date: "2026-09-30", calls: 0, failed: 0, p95_ms: null },
      { date: "2026-10-01", calls: 40, failed: 0, p95_ms: 800, servers: [{ name: "git_hub", calls: 30, failed: 0, p95_ms: 700 }, { name: "notion", calls: 10, failed: 0, p95_ms: 900 }] },
      { date: "2026-10-02", calls: 100, failed: 3, p95_ms: 2000, servers: [{ name: "git_hub", calls: 90, failed: 3, p95_ms: 2100 }, { name: "notion", calls: 10, failed: 0, p95_ms: 950 }] }],
    fails: [{ tool: "mcp__git_hub__push", at: at, ms: 30000, err: "timeout", agent: "claude" }] };
  const h = await open(mk({ menu: "connect", doc: summary({ tool_health: health }) }));
  const ht = text(h.table);
  assert.match(ht, /3\.0%/);
  assert.match(ht, /느림/, "p95 over 1.5s marks the server slow");
  assert.match(ht, /git_hub · 시간 초과 \(30초\)/);
  assert.match(ht, /2\.0초 · 실패 3 \(10-02\)/, "the peak day is tagged");
  assert.match(ht, /10-02 git_hub 서버가 느려지며 실패가 함께 늘었어요\. 나머지 날은 1초 안팎이에요\./, "the caption names the server that slowed against its own usual");
  const up = await open(mk({ menu: "connect", doc: summary({ tool_health: health, tool_calls: 120, tool_calls_change_pct: 6 }) }));
  assert.match(text(up.table), /120회30일 · 지난달보다 \+6%/);
  assert.match(ht, /30일 · 도구 2개/, "no window before: no change shown");
  const emptyDoc = summary({ by_agent: [], tools: [], updated_at: "" });
  const e1 = await open(mk({ menu: "agent", doc: emptyDoc }));
  assert.match(text(e1.table), /기록된 세션이 없어요/);
  const e2 = await open(mk({ menu: "connect", doc: emptyDoc }));
  assert.match(text(e2.table), /기록된 MCP 도구 호출이 없어요/);
  assert.match(text(e2.table), /서버가 보고한 MCP 서버가 없어요/);
  assert.match(text(e2.table), /이 기간에 실패한 MCP 호출이 없어요/);
});

test("세션 tab sorts heavy sessions, opens one, and goes back", async () => {
  const v = await open(mk({ menu: "memory" }));
  const sel = v.nodes["#controls"].all((n) => n.tagName === "SELECT")[0];
  sel.value = "claude"; sel.fire("change");
  for (let i = 0; i < 4; i++) await flush();
  assert.ok(v.calls.some((c) => c.includes("source=claude")));
  click(v.nodes["#controls"], "세션");
  assert.match(text(v.table), /많이 쓴 세션/);
  assert.match(text(v.nodes["#detail"]), /세션 길이/);
  click(v.table, "비용");
  click(v.table, "활동");
  click(v.nodes["#detail"], "세션 목록 보기 ›");
  assert.ok(v.went.includes("session:undefined") && !v.went.includes("sessions"), "the list opens the 세션 기록 window, not a screen");
  v.table.all((n) => n.classList.contains("v4u-session"))[0].click();
  assert.match(v.u.views.usage.title(), /고친 일|\(제목 없음\)/);
  assert.match(v.u.views.usage.sub(), /Claude Code/);
  assert.match(text(v.table), /쓴 것/);
  for (const t of ["대화 내용 보기 (세션 기록) ›", "모델 관리", "도구 관리", "스킬 관리"]) click(v.nodes["#detail"], t);
  assert.ok(v.went.includes("session:s1") || v.went.includes("session:s2"));
  click(v.nodes[".main > .page-head"], "← 사용량 · 세션");
  assert.equal(v.u.views.usage.title(), "사용량");
  v.table.all((n) => n.classList.contains("v4u-session"))[0].click();
  v.u.leave();
  const empty = await open(mk({ menu: "memory", doc: summary({ top_sessions: [], latest: [], kpi: { ...summary().kpi, sessions: 0 } }) }));
  click(empty.nodes["#controls"], "세션");
  assert.match(text(empty.table), /아직 기록이 없어요/);
  assert.deepEqual(empty.u.agents(), ["claude", "zz"]);
  assert.deepEqual(empty.u.agentCounts(), [["claude", 3], ["zz", undefined]]);
});

test("시간대 tab shows the heatmap, the busiest hour and the work pattern", async () => {
  const v = await open(mk({ menu: "agent" }));
  click(v.nodes["#controls"], "시간대");
  assert.match(text(v.table), /요일 × 시간/);
  assert.match(text(v.nodes["#detail"]), /일하는 패턴/);
  assert.match(v.u.views.usage.sub(), /요일·시간별/);
  const none = await open(mk({ menu: "agent", doc: summary({ hours: Array(168).fill(0), by_day: [], by_agent: [] }) }));
  click(none.nodes["#controls"], "시간대");
  assert.match(text(none.nodes["#detail"]), /기록 없음/);
});

test("결정 tab sums the server's decision counts per kind and reason over the range (#466)", async () => {
  const rows = [
    { period: "2026-09-01", kind: "skill_trashed", reason_code: "", count: 2 },
    { period: "2026-10-01", kind: "skill_trashed", reason_code: "", count: 3 },
    { period: "2026-10-01", kind: "merge_dismiss", reason_code: "same_meaning", count: 4 },
    { period: "2026-10-01", kind: "skill_route_loaded", reason_code: "pick", count: 1 },
    { period: "2026-10-01", kind: "new_kind", reason_code: "x", count: 1 },
  ];
  let reply = { authority: "data_only_untrusted", period: "month", from: "2026-09-30", rows };
  const calls = [];
  const request = async (path) => {
    calls.push(path);
    if (!path.startsWith("/v1/decisions/summary")) return summary();
    if (reply instanceof Error) throw reply;
    return reply;
  };
  const v = await open(mk({ menu: "memory", extra: { request } }));
  v.calls = calls;
  click(v.nodes["#controls"], "결정");
  for (let i = 0; i < 4; i++) await flush();
  assert.ok(v.calls.some((c) => c === "/v1/decisions/summary?period=month&days=30"), v.calls.join(" "));
  const rowsText = v.table.querySelectorAll(".v4u-req").map((r) => text(r));
  assert.deepEqual(rowsText, ["스킬을 휴지통으로이유 없음5", "병합 제안 넘김same_meaning4", "new_kindx1", "고른 스킬을 불러옴라우터가 고른 것1"]);
  assert.doesNotMatch(text(v.table), /^결정/); // no title repeating the tab name
  assert.match(v.u.views.usage.sub(), /종류·이유별로/);
  click(v.nodes["#controls"], "7일");
  for (let i = 0; i < 4; i++) await flush();
  assert.ok(v.calls.includes("/v1/decisions/summary?period=month&days=7"));

  reply = { ...reply, rows: [] };
  click(v.nodes["#controls"], "90일");
  for (let i = 0; i < 4; i++) await flush();
  assert.match(text(v.table), /이 기간에 센 결정이 없어요/);

  reply = new Error("HTTP 404");
  click(v.nodes["#controls"], "30일");
  for (let i = 0; i < 4; i++) await flush();
  assert.match(text(v.table), /아직 결정 집계를 지원하지 않아요/);
  reply = { rows: [] };
  click(v.nodes["#controls"], "전체");
  for (let i = 0; i < 4; i++) await flush();
  assert.match(text(v.table), /결정 수를 불러오지 못했어요 \(invalid_response\)/);
});

test("errors: an old server is unsupported, others can retry, bad replies are invalid", async () => {
  const old = await open(mk({ fail: "HTTP 404" }));
  assert.match(text(old.table), /아직 사용량을 지원하지 않아요/);
  const bad = await open(mk({ fail: "boom" }));
  assert.match(text(bad.table), /사용량을 불러오지 못했어요 \(boom\)/);
  click(bad.table, "다시 시도");
  const junk = await open(mk({ doc: { authority: "other" } }));
  assert.match(text(junk.table), /invalid_response/);
  await junk.u.refresh(); // quiet refresh before ready does nothing
});

test("quiet refresh keeps the screen when nothing changed, redraws when it did, and stops when blocked", async () => {
  let n = 0;
  const doc = () => (n++ < 2 ? summary() : summary({ updated_at: "2026-10-03T00:00:00Z" }));
  const v = await open(mk({ doc }));
  await v.u.refresh(); // same
  await v.u.refresh(); // changed
  v.ctx.canRefresh = () => false;
  await v.u.refresh();
  // another menu reads a different key: renderView asks again quietly
  v.state.menu = "memory";
  v.u.views.usage.render();
  for (let i = 0; i < 4; i++) await flush();
  assert.ok(v.calls.length >= 4);
  // a failed quiet read changes nothing
  v.ctx.request = async () => { throw new Error("x"); };
  v.ctx.canRefresh = undefined;
  await v.u.refresh();
  // the view is elsewhere: no paint
  v.state.view = "home";
  await v.u.load();
});

test("fmt helpers cover every size and the unknown agent tile", () => {
  const { fmt } = mk().u;
  assert.equal(fmt.num(1500), "1.5K"); assert.equal(fmt.num(15000), "15K"); assert.equal(fmt.num(1.5e6), "1.5M"); assert.equal(fmt.num(2e7), "20M"); assert.equal(fmt.num(3e9), "3.00B");
  assert.equal(fmt.usd(0, false), "—"); assert.equal(fmt.usd(250), "≈$250");
  assert.equal(fmt.dur(3600 * 12), "12시간"); assert.equal(fmt.dur(3700), "1시간 1분"); assert.equal(fmt.dur(120), "2분");
  assert.equal(fmt.logo("").title, ""); assert.equal(fmt.agentName("x"), "x");
  assert.match(text(fmt.bars([], (x) => x, String)), /아직 기록이 없어요/);
});
