// 라우팅 현황 (routing.js + routing-turns.js) and 설정 › 라우팅 설정: drawn into a DOM stand-in from fixed server replies.
const test = require("node:test");
const assert = require("node:assert/strict");
require("./keep.js"); // the shared stale-reply and redraw guards the screens use
const { install, byText, text, flush } = require("./testdata/router-dom.js");

const dom = install();
require("./routing-turns.js");
require("./routing.js");

const at = "2026-10-02T10:00:00Z";
const pts = (n, traffic, p95 = 3000) => Array.from({ length: n }, (_, i) => ({ t: new Date(Date.UTC(2026, 9, 1, i)).toISOString(), traffic: traffic * (i + 1) / n, p50: 1000, p95, success_rate: i ? 99 : undefined, cost: i ? 1 : undefined }));
const events = [
  { kind: "incident", title: "장애", detail: "d", at, family: "CLAUDE" }, { kind: "policy", title: "정책", at }, { kind: "recovery", title: "복구", at },
  { kind: "other", title: "기타", at }, { kind: "incident", title: "장애2", at }, { kind: "incident", title: "장애3", at },
];
function overview(over = {}) {
  return {
    authority: "data_only_untrusted",
    kpi: { requests: 1000, requests_now_per_min: 3, requests_delta_pct: 5, p50_ms: 1200, p50_delta_ms: -300, p95_ms: 3000, success_rate: 99.5, success_rate_delta_pp: -0.2, failures: 3, cost_usd: 150, cost_delta_pct: 0, budget_usd: 300 },
    series: { points: pts(6, 30), incident: { index: 1, label: "장애" } },
    models: [{ id: "a", name: "A", family: "CLAUDE", provider: "anthropic", status: "slow", requests: 5, p50_ms: 1, p95_ms: 2, success_rate: 99, cost_usd: 1 }, { id: "b", name: "B", family: "ZZ", status: "watch" }],
    families: [{ family: "CLAUDE", share: 60, requests: 6, note: "n" }, { family: "", share: 40, requests: 4 }], families_note: "주의",
    policy: { auto_routing: true, classifier_ms: 300, failover: { order: ["a", "x"] }, last_decision: "결정" },
    events,
    gateway: {
      main: { requests: 3, failures: 1, input_tokens: 1, output_tokens: 2, cost_usd: 1, estimated: true }, side: { requests: 2, by_side: { fork: 1, weird: 2 } },
      turns: [{ session_id: "abcdefghij", turn_seq: 2, main_calls: 1, side_calls: 1, at, calls: [{ ok: true, model: "m", latency_ms: 1000, input_tokens: 1 }, { ok: false, side: "query", error: "e", model: "m", latency_ms: 5 }, { ok: true, side: "weird", latency_ms: 1 }] }],
    },
    ...over,
  };
}
const modelsDoc = () => ({ authority: "data_only_untrusted", items: [
  { id: "a", name: "A", family: "CLAUDE", provider: "anthropic", order: 1, when: "코드", connection: { state: "watch", label: "주의" }, revision: 1, rpm_limit: 5, concurrency: 2 },
  { id: "b", name: "B", family: "ZZ", enabled: false, order: 2 }, { id: "c", name: "C", family: "GPT" }] });
const groupsDoc = () => ({ revision: 1, auto_discover: true, items: [
  { id: "group/x", name: "X", strategy: "smart", members: ["a", "c", "zz", "b"] }, { id: "group/auto", name: "Auto", discovered: true, strategy: "ordered", members: ["a"], enabled: false }] });

function mk({ replies = {}, mode = "local" } = {}) {
  const state = { view: "routing", scope: { project_id: "p" }, mode, settingsTab: "routing" };
  const nodes = Object.fromEntries(["#table", "#stats", "#chips", "#controls", "#detail", "main", "side"].map((k) => [k, dom.el("div")]));
  const notes = [], calls = [], went = [];
  const R = {
    "/v1/routing/overview": () => overview(), "/v1/routing/models": () => modelsDoc(), "/v1/routing/groups": () => groupsDoc(),
    "/v1/routing/skills": () => ({ items: [
      { at, selected: "skill-a", selected_name: "PDF 읽기", confirmed: true, provider: "clef-flash", candidates: 3, loaded: "skill-a", outcome: "failure", rival: "skill-b" },
      { at, selected: "skill-b", loaded: "skill-x", confirmed: false, provider: "jev-1.13.0", degraded: ["jev_uncertain"], candidates: 2, rival: "skill-a", rival_name: "PDF 읽기" },
      { at, selected: "skill-c", selected_name: "검색", confirmed: false, provider: "rules", degraded: ["jev_unavailable"], candidates: 1 },
      { at, confirmed: false, provider: "rules", degraded: ["odd"], candidates: 0 }] }),
    "/v1/routing/policy": () => ({}), "/v1/routing/decide": () => ({ id: "a", decision: "clef", classifier_ms: 200, fallbacks: ["c", "q"] }), ...replies,
  };
  const ctx = {
    state, $: (s) => nodes[s], el: dom.el, icon: dom.icon, button: dom.button, segment: dom.segment, agentLogo: dom.agentLogo, ago: () => "방금",
    query: (o) => new URLSearchParams(o || {}).toString(), go: (v) => went.push(v), notice: (m) => notes.push(m),
    request: async (path, opt) => {
      calls.push([path, opt && JSON.parse(opt.body)]);
      const f = R[path.split("?")[0]];
      const v = typeof f === "function" ? f(path, opt) : f;
      if (v instanceof Error) throw v;
      return v;
    },
    detailShell: () => { const b = dom.el("div"); nodes["#detail"].replaceChildren(b); return b; },
    render: () => draw(),
  };
  const kit = { head() {}, side: (...x) => nodes.side.replaceChildren(...x), sideNote: (t) => dom.el("p", "", t) };
  const draw = () => { if (state.view === "routing") r.render(); else if (state.view === "settings" && state.settingsTab === "routing") nodes.main.replaceChildren(...r.tab(kit)); };
  const r = window.ArkismRouting(ctx);
  return { r, ctx, state, nodes, notes, calls, went, draw, table: nodes["#table"], R };
}
const settle = async () => { for (let i = 0; i < 6; i++) await flush(); };
const click = (root, t) => { const all = byText(root, t), n = all.find((x) => x.tagName === "BUTTON") || all[0]; assert.ok(n, `no node with text ${t}`); n.click(); return n; };

test("라우팅 현황 draws KPIs, charts, models, families, gateway turns and events", async () => {
  const v = mk();
  v.r.render(); await settle();
  const t = text(v.table);
  assert.match(t, /요청 수/); assert.match(t, /모델 계열별 분배/); assert.match(t, /Claude Code 호출/); assert.match(t, /갈래 작업 1/); assert.match(t, /weird 2/);
  assert.equal(v.r.count(), 2);
  // 스킬 판정: the judge, the pick and why it fell back
  assert.match(t, /스킬 판정/); assert.match(t, /PDF 읽기/); assert.match(t, /Clef/); assert.match(t, /skill-b/); assert.match(t, /확신 낮음/);
  // what the agent did with the pick
  assert.match(t, /실패/); assert.match(t, /다른 후보 · 불러옴/); assert.match(t, /불러오지 않음/);
  // the two skills the judge found fitting the same questions twice, each picked once
  assert.match(t, /겹치는 스킬/); assert.match(t, /PDF 읽기↔skill-b2번 겹침1 : 1/);
  assert.match(t, /판정 모델 없음/); assert.match(t, /규칙 2/); assert.match(t, /고른 스킬 없음/); assert.match(t, /odd/);
  assert.match(v.r.views.routing.sub(), /실시간/);
  // a turn opens and closes
  const head = v.table.all((n) => n.classList.contains("rtg-turn-head"))[0];
  head.click();
  assert.match(text(v.table), /짧은 질의/);
  v.table.all((n) => n.classList.contains("rtg-turn-head"))[0].click();
  // events: filter, show all, widen to 30 days, collapse
  click(v.nodes["#detail"], "장애");
  click(v.nodes["#detail"], "전체");
  click(v.nodes["#detail"], "이벤트 전체 보기 (6건)");
  assert.match(text(v.nodes["#detail"]), /30일로 넓혀 보기/);
  click(v.nodes["#detail"], "30일로 넓혀 보기"); await settle();
  click(v.nodes["#detail"], "접기");
  click(v.nodes["#detail"], "복구"); click(v.nodes["#detail"], "정책 변경");
  click(v.nodes["#detail"], "라우팅 설정");
  assert.equal(v.state.settingsTab, "routing"); assert.deepEqual(v.went, ["settings"]);
  // head: refresh and range segment
  const [again, seg] = v.r.views.routing.head();
  again.click(); await settle();
  click(seg, "24시간"); click(seg, "1시간"); await settle();
  v.r.leave(); v.r.leave(); v.r.reset();
});

test("each range labels its axis; big, small and empty series pick their own scale", async () => {
  const shapes = [["1h", pts(6, 60000, 30000)], ["7d", pts(6, 600)], ["30d", pts(6, 60)], ["1h", pts(6, 3)]];
  for (const [range, points] of shapes) {
    const v = mk({ replies: { "/v1/routing/overview": () => overview({ series: { points }, kpi: { requests: 1, success_rate: 100, success_rate_delta_pp: 0, requests_delta_pct: -1, cost_delta_pct: 5 }, families: undefined, models: undefined, gateway: null, events: undefined, policy: undefined }) } });
    v.r.render(); await settle();
    click(v.r.views.routing.head()[1], { "1h": "1시간", "7d": "7일", "30d": "30일", "24h": "24시간" }[range]); await settle();
    assert.match(text(v.table), /트래픽/);
    assert.match(text(v.nodes["#detail"]), /해당하는 이벤트가 없어요/);
  }
});

test("an empty series draws the axes without lines", async () => {
  const v = mk({ replies: { "/v1/routing/overview": () => overview({ series: { points: [] } }) } });
  v.r.render(); await settle();
  assert.match(text(v.table), /트래픽/);
  const charts = v.table.all((n) => n.attrs.class === "rt-chart");
  assert.equal(charts.length, 2);
  for (const c of charts) assert.equal(c.all((n) => n.tagName === "PATH" || n.tagName === "CIRCLE").length, 0);
});

test("auto routing off: every model but the fixed one dims and a callout points to settings", async () => {
  const policy = { auto_routing: false, default_model: "a", auto_failover: false };
  const v = mk({ replies: { "/v1/routing/overview": () => overview({ policy, events: Array(50).fill(events[1]) }) } });
  v.r.render(); await settle();
  assert.match(text(v.table), /자동 라우팅이 꺼져 있어요/);
  assert.match(text(v.table), /모든 요청이 A로/);
  assert.match(text(v.nodes["#detail"]), /꺼짐 · A로 고정/);
  click(v.table, "라우팅 설정");
  click(v.nodes["#detail"], "이벤트 전체 보기 (50건)");
  assert.match(text(v.nodes["#detail"]), /최근 50건까지/);
  const nofix = mk({ replies: { "/v1/routing/overview": () => overview({ policy: { auto_routing: false, default_model: "q" } }) } });
  nofix.r.render(); await settle();
  assert.match(text(nofix.table), /고정 모델로/);
  const plain = mk({ replies: { "/v1/routing/overview": () => overview({ policy: { classifier: "X" } }) } });
  plain.r.render(); await settle();
  assert.match(text(plain.nodes["#detail"]), /켜짐 · X/);
  assert.match(text(plain.nodes["#detail"]), /라우팅 순서대로/);
});

test("errors: unsupported, failed with retry, invalid reply; quiet refresh skips or ignores", async () => {
  for (const [err, re] of [[new Error("HTTP 404"), /아직 라우팅을 지원하지 않아요/], [new Error("boom"), /\(boom\)/], [{ authority: "x" }, /invalid_response/]]) {
    const v = mk({ replies: { "/v1/routing/overview": () => err } });
    await v.r.refresh(); // not ready: nothing
    v.r.render(); await settle();
    assert.match(text(v.table), re);
    if (err.message === "boom") click(v.table, "다시 시도");
    v.r.detail();
  }
  const v = mk();
  v.r.render(); await settle();
  await v.r.refresh(); // same doc
  let n = 0; v.R["/v1/routing/overview"] = () => overview({ kpi: { ...overview().kpi, requests: ++n } });
  await v.r.refresh(); // changed
  v.ctx.canRefresh = () => false; await v.r.refresh();
  v.ctx.canRefresh = undefined; v.R["/v1/routing/overview"] = () => new Error("x"); await v.r.refresh();
  v.state.view = "home"; await v.r.load();
  assert.match(v.r.views.routing.sub(), /판정 모델이 요청을 나누고/);
  const routes = window.ArkismRoutingTurns({ el: dom.el });
  assert.equal(routes.card(null), null);
  assert.equal(routes.card({ main: {}, side: {} }), null);
});

test("라우팅 설정: policy cards, model list, model detail and saves", async () => {
  globalThis.ask = async () => true;
  const tauri = { calls: [], invoke: async (cmd, arg) => {
    tauri.calls.push([cmd, arg]);
    if (cmd === "gateway_info") return { url: "http://127.0.0.1:8099", agents: { claude_code: { connected: true, current: true }, codex: { connected: true }, gemini: { installed: false } } };
    if (arg.agent === "codex") throw new Error("codex_provider_exists");
    if (arg.agent === "gemini") throw new Error("unreadable");
    if (arg.agent === "x") throw new Error("no");
    return { agents: {} };
  } };
  window.__TAURI__ = { core: tauri };
  const policy = { revision: 3, auto_routing: false, default_model: "a", failover: { order: ["a"] } };
  const v = mk({ replies: { "/v1/routing/overview": () => overview({ policy }) } });
  v.state.view = "settings";
  v.draw(); await settle();
  const main = () => v.nodes.main, side = () => v.nodes.side;
  assert.match(text(main()), /자동 라우팅/); assert.match(text(main()), /에이전트 연결/); assert.match(text(main()), /중계기 127\.0\.0\.1:8099 · 켜짐/);
  // switches and picks on the policy cards
  main().all((n) => n.getAttribute("aria-label") === "자동 라우팅")[0].click(); await settle();
  assert.ok(v.calls.some(([p, b]) => p === "/v1/routing/policy" && b.auto_routing === true));
  const selects = () => main().all((n) => n.tagName === "SELECT");
  const s0 = selects()[0]; s0.value = "c"; s0.fire("change"); await settle();
  const [, f1, f2] = selects(); f1.value = "c"; f1.fire("change"); f2.value = "a"; f2.fire("change");
  const inputs = main().all((n) => n.tagName === "INPUT" && n.parent?.tagName === "LABEL");
  inputs[0].value = "7"; inputs[0].fire("input"); inputs[1].value = "30"; inputs[1].fire("input");
  click(main(), "조건 저장"); await settle();
  const saved = v.calls.filter(([p]) => p === "/v1/routing/policy").pop()[1];
  assert.deepEqual(saved.failover, { error_rate_pct: 7, p95_ms: 30000, order: ["c", "a"] });
  main().all((n) => n.getAttribute("aria-label") === "자동 페일오버")[0].click(); await settle();
  // the model list: open B, flip A, then edit A in the detail panel
  main().all((n) => n.classList.contains("rt-mcard"))[2].click();
  assert.match(text(side()), /B/);
  main().all((n) => n.getAttribute("aria-label") === "A 사용")[0].click(); await settle();
  main().all((n) => n.classList.contains("rt-mcard"))[0].click();
  const ta = side().all((n) => n.tagName === "TEXTAREA")[0]; ta.value = "새 설명"; ta.fire("input");
  for (const i of side().all((n) => n.tagName === "INPUT")) { i.value = "3"; i.fire("input"); }
  click(side(), "저장"); await settle();
  assert.ok(v.calls.some(([p, b]) => p === "/v1/routing/models" && b.when === "새 설명" && b.order === 3));
  click(side(), "저장"); // nothing staged: no request
  side().all((n) => n.getAttribute("aria-label") === "라우팅에 쓰기")[0].click(); await settle();
  click(side(), "에이전트 설정 열기 ›"); assert.equal(v.state.settingsTab, "agents");
  v.state.settingsTab = "routing"; v.draw();
  // agent links
  click(main(), "끊기"); await settle();
  assert.ok(v.notes.includes("연결을 끊고 원래 설정으로 돌려놨어요."));
  window.__TAURI__.core.invoke = async (cmd, arg) => { tauri.calls.push([cmd, arg]); if (arg?.agent === "claude_code") return { agents: {} }; throw new Error(arg?.agent === "codex" ? "codex_provider_exists" : arg?.agent === "gemini" ? "unreadable" : "no"); };
  v.r.leaveSettings(); v.draw(); await settle();
  // S.links stays from before; reconnect codex fails with its own message
  const links = main().all((n) => n.classList.contains("rt-link-row"));
  assert.equal(links.length, 3);
  // failing saves
  v.R["/v1/routing/policy"] = () => new Error("revision_conflict");
  main().all((n) => n.getAttribute("aria-label") === "자동 라우팅")[0].click(); await settle();
  assert.ok(v.notes.some((m) => /저장하지 못했어요 \(revision_conflict\)/.test(m)));
  v.R["/v1/routing/models"] = (p, o) => (o ? new Error("bad") : modelsDoc());
  main().all((n) => n.getAttribute("aria-label") === "A 사용")[0].click(); await settle();
  assert.ok(v.notes.some((m) => m === "저장하지 못했어요 (bad)."));
  v.r.leaveSettings(); v.r.leaveSettings();
  delete window.__TAURI__;
});

test("agent link buttons connect, reconnect and report each failure", async () => {
  let fail = "";
  window.__TAURI__ = { core: { invoke: async (cmd) => {
    if (cmd === "gateway_info") return { agents: { claude_code: {}, codex: { connected: true } } };
    if (fail) throw new Error(fail);
    return { agents: { claude_code: { connected: true, current: true } } };
  } } };
  const v = mk();
  v.state.view = "settings"; v.draw(); await settle();
  assert.match(text(v.nodes.main), /중계기 꺼짐/);
  for (fail of ["codex_provider_exists", "unreadable", "other"]) { click(v.nodes.main, "연결"); await settle(); v.draw(); }
  fail = ""; click(v.nodes.main, "다시 연결"); await settle();
  assert.deepEqual(v.notes.slice(-4), ["Codex 설정에 이미 arkism 공급자가 있어요. 직접 지운 뒤 다시 해 주세요.", "에이전트 설정 파일을 읽지 못했어요.", "연결하지 못했어요.", "연결했어요. 에이전트를 다시 시작하면 적용돼요."]);
  delete window.__TAURI__;
});

test("agent links say the relay state is unknown when gateway_info fails, not that it is off", async () => {
  window.__TAURI__ = { core: { invoke: async () => { throw new Error("down"); } } };
  const v = mk();
  v.state.view = "settings"; v.draw(); await settle();
  assert.match(text(v.nodes.main), /중계기 상태를 확인하지 못했어요/);
  assert.doesNotMatch(text(v.nodes.main), /중계기 꺼짐/);
  delete window.__TAURI__;
});

test("라우팅 그룹: pick, edit, reorder, add, remove, keep a discovered group, try a route and save", async () => {
  globalThis.ask = async () => true;
  const v = mk({ mode: "server" });
  v.state.view = "settings"; v.draw(); await settle();
  const main = () => v.nodes.main, side = () => v.nodes.side;
  assert.doesNotMatch(text(main()), /에이전트 연결/);
  // try a route without a group, by Enter and by button, with every outcome
  const input = () => main().all((n) => n.getAttribute("aria-label") === "시험할 요청")[0];
  click(main(), "어디로 가나 보기"); // empty: nothing
  input().value = "테스트 써 줘"; input().fire("input");
  input().fire("keydown", { key: "a" });
  input().fire("keydown", { key: "Enter" }); await settle();
  assert.match(text(main()), /판정 모델이 골랐어요 · 0\.2초/); assert.match(text(main()), /분류 담당없음/); assert.match(text(main()), /판정 모델이 없어서 그룹 밖 요청은 순서상 첫 모델로/); assert.match(text(main()), /C → q/);
  for (const reply of [{ id: "a", decision: "default" }, { id: "a", decision: "odd", fallbacks: [] }, {}, new Error("HTTP 503"), new Error("weird")]) {
    v.R["/v1/routing/decide"] = () => reply;
    click(main(), "어디로 가나 보기"); await settle();
  }
  assert.match(text(main()), /weird/);
  // pick group X, change strategy, rename, reorder, remove and add members
  click(main(), "X");
  assert.match(text(main()), /X 안에서 고르기/);
  click(main(), "순서대로");
  assert.match(text(main()), /그룹을 바꿨어요/);
  const ni = side().all((n) => n.getAttribute("aria-label") === "그룹 이름")[0]; ni.value = "  Y  "; ni.fire("change");
  side().all((n) => n.getAttribute("aria-label") === "아래로")[0].click();
  side().all((n) => n.getAttribute("aria-label") === "위로")[1].click();
  side().all((n) => n.getAttribute("aria-label") === "이 그룹 쓰기")[0].click();
  click(side(), "빼기");
  const add = side().all((n) => n.getAttribute("aria-label") === "모델 넣기")[0];
  add.value = ""; add.fire("change"); add.value = "a"; add.fire("change");
  assert.match(text(side()), /목록에 없는 모델/);
  click(main(), "되돌리기");
  // a new group, then 되돌리기 drops it and the selection
  click(main(), "+ 새 그룹");
  assert.match(text(side()), /새 그룹/);
  click(main(), "되돌리기");
  // the discovered group cannot change strategy but becomes a group of its own
  click(main(), "Auto");
  click(main(), "의도별");
  assert.match(text(side()), /자동으로 묶은 그룹/);
  click(side(), "그룹으로 만들기");
  click(main(), "그룹 저장"); await settle();
  const sent = v.calls.filter(([p]) => p === "/v1/routing/groups").map(([, b]) => b).filter(Boolean).pop();
  assert.equal(sent.groups.length, 2); assert.equal(sent.auto_discover, true);
  // delete group X, then a failed save explains the server's word
  click(main(), "X");
  v.R["/v1/routing/groups"] = (p, o) => (o ? new Error("not_found") : groupsDoc());
  click(side(), "그룹 지우기"); await settle();
  assert.ok(v.notes.some((m) => /그룹에 꺼진 모델이 있어요/.test(m)));
  globalThis.ask = async () => false;
  v.r.leaveSettings(); v.draw(); await settle();
  click(main(), "X"); click(side(), "그룹 지우기"); await settle();
  click(main(), "X"); // toggles off
  v.R["/v1/routing/groups"] = (p, o) => (o ? new Error("odd") : groupsDoc());
  main().all((n) => n.getAttribute("aria-label") === "같은 모델 자동으로 묶기")[0].click(); await settle();
  assert.ok(v.notes.some((m) => m === "저장하지 못했어요 (odd)."));
  // an empty group says so; a group with all models in it has nothing to add
  v.R["/v1/routing/groups"] = () => ({ revision: 2, items: [{ id: "group/e", name: "E", strategy: "x", members: [] }, { id: "group/f", name: "F", strategy: "smart", members: ["a", "c"] }] });
  v.r.leaveSettings(); v.draw(); await settle();
  click(main(), "E"); assert.match(text(side()), /모델이 없어요/);
  click(main(), "F"); assert.match(text(side()), /넣을 수 있는 모델이 없어요/);
  click(side(), "에이전트 설정 열기 ›");
});

test("라우팅 설정 without groups, with a bad reply, an old server, an error, and a stale load", async () => {
  const noGroups = mk({ replies: { "/v1/routing/groups": () => new Error("HTTP 404"), "/v1/routing/models": () => ({ authority: "data_only_untrusted", items: [] }) } });
  noGroups.state.view = "settings"; noGroups.draw(); await settle();
  assert.match(text(noGroups.nodes.main), /아직 라우팅 그룹을 지원하지 않아요/);
  assert.match(text(noGroups.nodes.side), /모델을 고르세요/);
  for (const [reply, re] of [[{ authority: "x" }, /invalid_response/], [new Error("HTTP 501"), /아직 라우팅을 지원하지 않아요/], [new Error("boom"), /\(boom\)/]]) {
    const v = mk({ replies: { "/v1/routing/models": () => reply } });
    v.state.view = "settings"; v.draw();
    assert.match(text(v.nodes.main), /불러오는 중/);
    await settle();
    assert.match(text(v.nodes.main), re);
  }
  const groupsFail = mk({ replies: { "/v1/routing/groups": () => new Error("boom") } });
  groupsFail.state.view = "settings"; groupsFail.draw(); await settle();
  assert.match(text(groupsFail.nodes.main), /\(boom\)/);
  // leaving the tab mid-load drops the reply, success or failure
  for (const reply of [modelsDoc, () => new Error("late")]) {
    const releases = [];
    const stale = mk({ replies: { "/v1/routing/models": () => new Promise((r) => releases.push(r)).then(reply).then((x) => { if (x instanceof Error) throw x; return x; }) } });
    const first = stale.r.loadSettings(), second = stale.r.loadSettings();
    releases[1](); await second;
    releases[0](); await first;
  }
});

test("스킬 판정 card hides when the server has no decision log or nothing was decided", async () => {
  for (const reply of [() => new Error("HTTP 404"), () => ({ items: [] })]) {
    const v = mk({ replies: { "/v1/routing/skills": reply } });
    v.r.render(); await settle();
    assert.match(text(v.table), /요청 수/);
    assert.doesNotMatch(text(v.table), /스킬 판정/);
  }
});
