// Run with: node --test desktop/ui/*.test.js
// Memory detail panels (기억 평가 · 기억 맥락 · 생성 모델 기록 · Claude API 월 예산) and the 과거 기억 tab, on a small DOM stand-in.
const test = require("node:test");
const assert = require("node:assert/strict");
require("./keep.js"); // the shared stale-reply and redraw guards the screens use
const dom = require("./testdata/fake-dom.cjs");
const { document } = dom.install();
const h = dom.helpers(document);
dom.Element.prototype.setCustomValidity = function (m) { this.validity = m; };
for (const f of ["memory-evaluation.js", "memory-context.js", "model-generation.js", "model-budget.js", "memory-time.js", "memory-pages.js"]) require(`./${f}`);

const section = (title) => { const s = h.el("section", "section"); s.append(h.el("h3", "", title)); return s; };
const facts = (pairs) => { const dl = h.el("dl", "facts"); for (const [k, v] of pairs) dl.append(h.el("dt", "", k), h.el("dd", "", v)); return dl; };
const host = () => { const c = h.el("div"); document.body.append(c); return c; };

test("memory-pages.js publishes ArkismMemoryPages on window in the browser", () => {
  assert.equal(typeof window.ArkismMemoryPages.create, "function");
  assert.equal(window.ArkismMemoryPages.create(async () => ({})).snapshot().ready, false);
});

test("review payloads reuse one operation id per identical request and keep at most 200", () => {
  const ev = window.ArkismMemoryEvaluation;
  const a = ev.reviewPayload("m1", { action: "approve" }), b = ev.reviewPayload("m1", { action: "approve" });
  assert.equal(a.action, "approve");
  assert.equal(a.operation_id, b.operation_id);
  assert.notEqual(ev.reviewPayload("m1", { action: "reject" }).operation_id, a.operation_id);
  for (let i = 0; i < 200; i++) ev.reviewPayload(`x${i}`, {});
  assert.notEqual(ev.reviewPayload("m1", { action: "approve" }).operation_id, a.operation_id); // the oldest entry was dropped
});

test("evaluation summaries never treat scheduling or self-rating as independent evidence", () => {
  const { describe } = window.ArkismMemoryEvaluation;
  const target = { collection: "memories", id: "m", revision: 3 };
  const base = { authority: "data_only_untrusted", target };
  for (const bad of [null, { ...base, authority: "x" }, { ...base, target: { ...target, collection: "skills" } }, { ...base, target: { ...target, id: "o" } }, { ...base, target: { ...target, revision: 2 } }])
    assert.throws(() => describe(bad, target), /invalid_evaluation_summary/);
  const g = (n) => ({ groups: { user_rating: { count: n }, automated_check: { count: 1 } } });
  assert.deepEqual([describe({ ...base, ...g(1), passed: false }, target).label, describe({ ...base, ...g(1), passed: false }, target).tone], ["관측에서 실패", "bad"]);
  assert.equal(describe({ ...base, ...g(0), passed: true }, target).tone, "ok");
  assert.equal(describe({ ...base, ...g(2) }, target).label, "판정 없는 관측");
  assert.equal(describe({ ...base, ...g(2) }, target).independent, 3);
  const self = describe({ ...base, groups: { model_self: { count: 4 } }, passed: true, tracking: { status: "done" } }, target);
  assert.deepEqual([self.label, self.self, self.tracking], ["모델 자기평가만 있음", 4, "관측 기록됨"]);
  const pending = describe({ ...base, tracking: { status: "pending" } }, target);
  assert.deepEqual([pending.label, pending.tracking], ["관측 대기", "관측 대기"]);
  assert.deepEqual([describe(base, target).label, describe(base, target).tracking], ["관측 없음", "추적 기록 없음"]);
});

test("the evaluation panel reads the current revision, shows a retry and ignores late answers", async () => {
  const calls = [];
  let answer = { authority: "data_only_untrusted", target: { collection: "memories", id: "m", revision: 2 }, groups: { user_rating: { count: 1 } }, passed: true };
  const request = async (path, opts) => { calls.push([path, JSON.parse(opts.body)]); if (answer instanceof Error) throw answer; return answer; };
  const c = host();
  const panel = window.ArkismMemoryEvaluation.mount({ container: c, record: { id: "m", revision: 2 }, scope: { project_id: "p" }, request, el: h.el, section, facts, button: h.button });
  assert.match(panel.textContent, /불러오는 중/);
  await dom.flush();
  assert.deepEqual(calls[0], ["/v1/evaluations/summary", { scope: { project_id: "p" }, target: { collection: "memories", id: "m", revision: 2 } }]);
  assert.match(panel.textContent, /관측에서 통과/);
  assert.match(panel.textContent, /독립된 관측1건/);
  const good = answer;
  answer = new Error("403");
  // a failure retries once without a word, then says so plainly with 다시 시도 (no permission guess)
  const failing = window.ArkismMemoryEvaluation.mount({ container: c, record: { id: "m", revision: 2 }, scope: {}, request, el: h.el, section, facts, button: h.button });
  const before = calls.length; await dom.flush();
  assert.doesNotMatch(failing.textContent, /못했어요/);
  await new Promise((r) => setTimeout(r, 1600)); await dom.flush();
  assert.equal(calls.length, before + 1);
  assert.match(failing.textContent, /평가를 불러오지 못했어요/);
  assert.doesNotMatch(failing.textContent, /권한/);
  assert.equal(dom.buttons(failing, "다시 시도").length, 1);
  // a panel closed before the answer stays as it was, for both outcomes
  for (const late of [answer, good]) {
    answer = late;
    const c2 = host();
    const p2 = window.ArkismMemoryEvaluation.mount({ container: c2, record: { id: "m", revision: 2 }, scope: {}, request, el: h.el, section, facts, button: h.button });
    c2.remove(); await dom.flush();
    assert.match(p2.textContent, /불러오는 중/);
    assert.equal(dom.buttons(p2).length, 0);
  }
});

test("memory context labels describe the situation and fall back for missing fields", () => {
  const render = window.ArkismMemoryContext({ el: h.el });
  assert.equal(render(null), null);
  assert.equal(render({ memory_context: { domains: "personal" } }), null);
  const full = render({ memory_context: { domains: ["personal", "work", "odd"], applicability: "응답", evidence_kind: "tool_observation", semantic_kind: "lesson", observed_at: "2026-01-02T03:04:05Z", valid_from: "2027", uncertainty: "예외" } });
  assert.equal(full.getAttribute("aria-label"), "기억 맥락");
  assert.match(full.textContent, /개인 · 업무 · 미분류/);
  for (const t of ["적용 조건   응답", "근거 유형   도구 관측", "기억 유형   교훈", "적용 시점   2027", "불확실성   예외"]) assert.ok(full.textContent.includes(t), t);
  assert.ok(!full.textContent.includes("관측 시각   기록되지"));
  const empty = render({ memory_context: { domains: [], observed_at: "not a date" } });
  for (const t of ["조건이 기록되지 않았어요.", "근거 유형   미분류", "기억 유형   미분류", "관측 시각   기록되지 않았어요.", "적용 시점   기록되지 않았어요.", "별도 설명이 없어요."]) assert.ok(empty.textContent.includes(t), t);
});

test("generation provenance shows only a stored model name and its versions", () => {
  const render = window.ArkismModelGeneration({ el: h.el });
  for (const r of [null, {}, { generator: {} }, { generator: { model: 3 } }, { generator: { model: "" } }]) assert.equal(render(r), null);
  const short = render({ generator: { model: "claude-x" } });
  assert.match(short.textContent, /claude-x/);
  assert.ok(!short.textContent.includes("프롬프트 버전"));
  const full = render({ generator: { model: "m", prompt_version: "p7", pipeline_version: "v2" } });
  assert.ok(full.textContent.includes("프롬프트 버전   p7") && full.textContent.includes("처리 버전   v2"));
});

test("budget documents are validated before any number is shown", () => {
  const { validate } = window.ArkismModelBudget;
  const ok = { month: "2026-10", monthly_limit_pico_usd: 0, general_limit_pico_usd: 0, daily_soft_target_pico_usd: 0, monthly_committed_pico_usd: 0, general_committed_pico_usd: 0, daily_committed_pico_usd: 0, outstanding_pico_usd: 0, daily_soft_target_exceeded: false };
  assert.equal(validate(ok), ok);
  for (const bad of [null, { ...ok, month: "2026-1" }, { ...ok, outstanding_pico_usd: -1 }, { ...ok, monthly_limit_pico_usd: 1.5 }, { ...ok, daily_soft_target_exceeded: "no" }, { ...ok, daily_limit_pico_usd: -2 }, { ...ok, daily_limit_pico_usd: "1" }])
    assert.throws(() => validate(bad), /invalid_budget/);
  assert.equal(validate({ ...ok, daily_limit_pico_usd: 5 }).daily_limit_pico_usd, 5);
});

test("the budget card shows dollars, the daily cap, subscription use and a retry", async () => {
  const P = 1e12;
  const doc = { month: "2026-10", monthly_limit_pico_usd: 100 * P, general_limit_pico_usd: 50 * P, daily_soft_target_pico_usd: 5 * P, monthly_committed_pico_usd: 12.345 * P, general_committed_pico_usd: 2 * P,
    daily_committed_pico_usd: 6 * P, outstanding_pico_usd: P / 2, daily_soft_target_exceeded: true, daily_limit_pico_usd: 8 * P, subscription_requests: 7, subscription_estimated_pico_usd: 3 * P };
  let answer = doc; const calls = [];
  const request = async (path, opts) => { calls.push([path, opts.body]); if (answer instanceof Error) throw answer; return answer; };
  const c = host(); c.append(h.el("div", "existing"));
  const panel = window.ArkismModelBudget.mount({ container: c, scope: { project_id: "p" }, request, el: h.el, button: h.button });
  assert.equal(c.firstChild, panel); // the card goes first
  await dom.flush();
  assert.deepEqual(calls[0], ["/v1/model-budget/status", JSON.stringify({ scope: { project_id: "p" } })]);
  for (const t of ["2026-10 · 매달 ", "$12.35 / $100.00", "$2.00 / $50.00", "$6.00 / $5.00", "오늘 상한$8.00", "미정산 예약$0.50", "7회 · API 환산 $3.00 (예산 밖)", "오늘 목표를 넘었어요"]) assert.ok(panel.textContent.includes(t), t);
  answer = { ...doc, daily_soft_target_exceeded: false, daily_limit_pico_usd: 0, subscription_requests: -1 };
  dom.buttons(panel, "예산 다시 조회")[0].click(); await dom.flush();
  assert.ok(panel.textContent.includes("지출 추이를 보는 기준"));
  assert.ok(!panel.textContent.includes("오늘 상한") && !panel.textContent.includes("구독"));
  answer = { ...doc, month: "bad" };
  dom.buttons(panel, "예산 다시 조회")[0].click(); await dom.flush();
  assert.match(panel.textContent, /예산을 불러오지 못했어요/);
  answer = new Error("not_found"); // no budget configured: the card goes away
  dom.buttons(panel, "예산 다시 조회")[0].click(); await dom.flush();
  assert.equal(panel.isConnected, false);
  for (const late of [doc, new Error("x")]) {
    answer = late;
    const c2 = host(), p2 = window.ArkismModelBudget.mount({ container: c2, scope: {}, request, el: h.el, button: h.button });
    p2.remove(); await dom.flush();
    assert.match(p2.textContent, /불러오는 중/);
  }
});

test("with no budget policy the card offers one monthly amount, saved with the routing policy", async () => {
  const calls = []; let answer = { configured: false, month: "2026-10", day: "2026-10-30", routing_month_cost_usd: 3.5, routing_revision: 4 };
  const request = async (path, opts) => { calls.push([path, JSON.parse(opts.body)]); if (path === "/v1/routing/policy") { if (answer instanceof Error) throw answer; answer = { ...answer, routing_budget_usd: 20, routing_revision: 5 }; return {}; } return answer; };
  assert.throws(() => window.ArkismModelBudget.validate({ configured: false, month: "x" }), /invalid_budget/);
  assert.throws(() => window.ArkismModelBudget.validate({ configured: false, month: "2026-10", routing_budget_usd: -1 }), /invalid_budget/);
  const c = host(), panel = window.ArkismModelBudget.mount({ container: c, scope: { project_id: "p" }, request, el: h.el, button: h.button });
  await dom.flush();
  for (const t of ["월 예산", "아직 월 예산이 없어요. 이번 달 약 $3.50를 썼어요.", "요청을 막지 않고"]) assert.ok(panel.textContent.includes(t), t);
  assert.ok(!panel.textContent.includes("Claude API"));
  const input = panel.querySelector("input");
  input.value = "-3"; dom.buttons(panel, "저장")[0].click(); await dom.flush();
  assert.match(panel.textContent, /0에서 100000 사이/); assert.equal(calls.length, 1);
  input.value = "20"; dom.buttons(panel, "저장")[0].click(); await dom.flush(); await dom.flush();
  assert.deepEqual(calls[1], ["/v1/routing/policy", { scope: { project_id: "p" }, expected_revision: 4, monthly_budget_usd: 20 }]);
  assert.ok(panel.textContent.includes("이번 달 약 $3.50 / 월 예산 $20.00"), panel.textContent);
  const again = { configured: false, month: "2026-10" }; const req2 = async (path) => { if (path === "/v1/routing/policy") throw new Error("x"); return again; };
  const p2 = window.ArkismModelBudget.mount({ container: host(), scope: {}, request: req2, el: h.el, button: h.button }); await dom.flush();
  p2.querySelector("input").value = ""; dom.buttons(p2, "저장")[0].click(); await dom.flush();
  assert.match(p2.textContent, /저장하지 못했어요/);
});

// ---------------------------------------------------------------- 과거 기억 (memory-time.js)
function timeView(request) {
  document.body.replaceChildren();
  for (const id of ["chips", "stats", "table", "detail"]) { const n = h.el("div"); n.id = id; document.body.append(n); }
  let opened = 0;
  const detailShell = (label) => { const body = h.el("div", "detail-body"); h.$("#detail").replaceChildren(h.el("span", "", label), body); return body; };
  const state = { server: "s", token: "t", scope: { project_id: "p" }, view: "memories", memTab: "past" };
  const view = window.ArkismMemoryTime({ state, $: h.$, el: h.el, icon: h.icon, button: h.button, request, detailShell, openDetail: () => opened++ });
  // these tests cover 그 시각 상태; the tab opens on 시간순
  const draw = view.draw; view.draw = () => { draw(); const c = dom.buttons(h.$("#chips"), "그 시각 상태")[0]; if (!c.classList.contains("active")) c.click(); };
  return { view, state, opened: () => opened };
}
const past = "2025-05-06T07:08";
const page = (asOf, items, unknown = [], next_cursor = "") => ({ authority: "data_only_untrusted", as_of: asOf, items, unknown, next_cursor });
const submit = (time = past, q = "") => {
  const [t, s] = h.$("#chips form").querySelectorAll("input");
  dom.input(t, time); dom.input(s, q);
  h.$("#chips form").requestSubmit();
};

test("the past tab asks for a time first and rejects future or unreadable input", async () => {
  const calls = [];
  const { view } = timeView(async (...a) => { calls.push(a); return page("", [], []); });
  view.draw();
  assert.match(h.$("#table").textContent, /날짜와 시간을 골라 조회하세요/);
  const [time, q] = h.$("#chips form").querySelectorAll("input");
  assert.equal(time.type, "datetime-local");
  view.draw(); // same identity keeps the form
  assert.equal(h.$("#chips form").querySelectorAll("input")[0], time);
  submit("2999-01-01T00:00");
  assert.equal(time.validity, "현재보다 이전 시각을 입력하세요.");
  submit("nonsense");
  assert.equal(time.validity, "현재보다 이전 시각을 입력하세요.");
  dom.fire(time, "input"); assert.equal(time.validity, "");
  submit(past, "가".repeat(90)); // 270 bytes
  assert.equal(q.validity, "검색어를 조금 짧게 입력하세요.");
  dom.fire(q, "input"); assert.equal(q.validity, "");
  assert.equal(calls.length, 0);
  view.detail();
  assert.match(h.$("#detail").textContent, /날짜를 골라 확인하세요/);
});

test("a past lookup lists what was stored, opens details, pages on and closes back to the row", async () => {
  const asOf = new Date(past).toISOString(), calls = [];
  const replies = [
    page(asOf, [{ id: "a", text: "그때 지시", historical_revision: 1, current_revision: 3, observed_at: "2025-05-01T00:00:00Z" }, { id: "b", text: "", summary: "요약", historical_revision: 2, current_revision: 2 }], [{ id: "u", reason: "history_not_recorded" }], "c1"),
    page(asOf, [{ id: "c", text: "" }], [{ id: "v", reason: "content_unavailable" }], "")
  ];
  const { view, opened } = timeView(async (path, opts) => { calls.push([path, JSON.parse(opts.body)]); return replies.shift(); });
  view.draw(); submit(past, " 찾기 ");
  assert.match(h.$("#table").textContent, /불러오는 중/);
  assert.equal(h.$("#chips button[type=submit]").disabled, true);
  await dom.flush();
  assert.deepEqual(calls[0], ["/v1/memories/at", { scope: { project_id: "p" }, as_of: asOf, q: "찾기", limit: 20, cursor: "" }]);
  const t = h.$("#table").textContent;
  for (const s of ["3개 항목", "그때 지시", "당시 판 1 (지금 판 3)", "요약", "당시 판 2", "상태 기록 없음", "확인 불가"]) assert.ok(t.includes(s), s);
  assert.ok(!t.includes("당시 판 2 (지금"));
  dom.buttons(h.$("#table"), "과거 기억 더 보기")[0].click(); await dom.flush();
  assert.equal(calls[1][1].cursor, "c1");
  assert.ok(h.$("#table").textContent.includes("본문 없음") && h.$("#table").textContent.includes("당시 본문을 확인할 수 없어요"));
  assert.equal(dom.buttons(h.$("#table"), "더 보기").length, 0);
  const rows = h.$("#table").querySelectorAll(".original-row");
  rows[0].click();
  assert.equal(opened(), 1);
  assert.ok(h.$("#detail").textContent.includes("1 (지금은 3)"));
  assert.equal(document.activeElement, h.$("#detail h2"));
  assert.ok(h.$("#table").querySelectorAll(".original-row")[0].classList.contains("selected"));
  h.$("#table").querySelectorAll(".original-row")[2].click();
  assert.match(h.$("#detail").textContent, /상태 이력이 기록되지 않아/);
  h.$("#table").querySelectorAll(".original-row")[4].click();
  assert.match(h.$("#detail").textContent, /당시 본문이 보존되어 있지 않아/);
  h.$("#table").querySelectorAll(".original-row")[1].click();
  assert.ok(h.$("#detail").textContent.includes("요약"));
  view.close();
  assert.equal(document.activeElement, h.$("#table").querySelectorAll(".original-row")[1]);
  assert.match(h.$("#detail").textContent, /날짜를 골라/);
  view.close(); // nothing selected: nothing to focus
});

test("bad or failed answers show a retry; stale answers and other tabs are ignored", async () => {
  const asOf = new Date(past).toISOString();
  const bad = [null, { ...page(asOf, []), authority: "x" }, page("2020-01-01T00:00:00Z", []), { ...page(asOf, []), items: {} }, { ...page(asOf, []), unknown: null }, { ...page(asOf, []), next_cursor: 1 },
    page(asOf, [{ id: 1, text: "" }]), page(asOf, [], [{ id: "u", reason: "other" }])];
  let reply;
  const { view, state } = timeView(async () => { if (reply instanceof Error) throw reply; return reply; });
  view.draw();
  for (const r of [...bad, new Error("net")]) {
    reply = r; submit(); await dom.flush();
    assert.match(h.$("#table").textContent, /불러오지 못했어요/);
  }
  reply = page(asOf, [{ id: "a", text: "x" }]); // the retry button starts over
  dom.buttons(h.$("#table"), "다시 조회")[0].click(); await dom.flush();
  assert.match(h.$("#table").textContent, /1개 항목/);
  reply = page(asOf, [], [], ""); submit(); await dom.flush();
  assert.match(h.$("#table").textContent, /표시할 기억이 없어요/);
  // a repeated cursor is invalid
  reply = page(asOf, [{ id: "a", text: "x" }], [], "c"); submit(); await dom.flush();
  reply = page(asOf, [], [], "c"); dom.buttons(h.$("#table"), "더 보기")[0].click(); await dom.flush();
  assert.match(h.$("#table").textContent, /불러오지 못했어요/);
  // leaving the tab mid-request drops the answer (success and failure)
  for (const late of [page(asOf, [{ id: "z", text: "late" }]), new Error("late")]) {
    let release; reply = null;
    const v2 = timeView((_, __) => new Promise((res, rej) => { release = () => (late instanceof Error ? rej(late) : res(late)); }));
    v2.view.draw(); submit();
    v2.state.view = "home"; release(); await dom.flush();
    assert.ok(!h.$("#table").textContent.includes("late"));
    v2.view.draw(); // tab not active: submit does nothing
    submit(); await dom.flush();
  }
  // a different server resets the tab
  state.server = "other"; view.draw();
  assert.match(h.$("#table").textContent, /날짜와 시간을 골라/);
  view.reset();
});

test("a second submit while loading is ignored", async () => {
  let n = 0, release;
  const asOf = new Date(past).toISOString();
  const { view } = timeView(() => { n++; return new Promise((r) => { release = () => r(page(asOf, [])); }); });
  view.draw(); submit(); submit();
  assert.equal(n, 1);
  release(); await dom.flush();
  assert.match(h.$("#table").textContent, /표시할 기억이 없어요/);
});

// ---------------------------------------------------------------- 시간순 (memory-time.js)
function lineView(request, extra = {}) {
  document.body.replaceChildren();
  for (const id of ["chips", "stats", "table", "detail"]) { const n = h.el("div"); n.id = id; document.body.append(n); }
  let opened = 0;
  const detailShell = (label) => { const body = h.el("div", "detail-body"); h.$("#detail").replaceChildren(h.el("span", "", label), body); return body; };
  const state = { server: "s", token: "t", scope: { project_id: "p" }, view: "memories", memTab: "past" };
  const view = window.ArkismMemoryTime({ state, $: h.$, el: h.el, icon: h.icon, button: h.button, request, detailShell, openDetail: () => opened++, query: () => "project_id=p", ...extra });
  return { view, state, opened: () => opened };
}
const hits = (items, time_window) => ({ authority: "data_only_untrusted", items, ...(time_window ? { explain: { lanes: ["keyword", "temporal"], time_window } } : {}) });
const week = { phrase: "지난주", from: "2026-10-25T15:00:00Z", to: "2026-11-01T15:00:00Z" };

test("시간순 opens first, offers examples and sends the time words to the server parser", async () => {
  const calls = [];
  const { view } = lineView(async (path, opts) => { calls.push([path, JSON.parse(opts.body)]); return hits([], week); });
  view.draw();
  assert.ok(dom.buttons(h.$("#chips"), "시간순")[0].classList.contains("active"));
  assert.match(h.$("#table").textContent, /언제 있었던 일인지/);
  h.$("#chips form").requestSubmit(); // empty: asks for words, sends nothing
  assert.equal(h.$("#chips form input").validity, "언제 일인지 적어 주세요.");
  assert.equal(calls.length, 0);
  dom.buttons(h.$("#table"), "지난주")[0].click(); await dom.flush();
  assert.equal(calls[0][0], "/v1/search");
  assert.equal(calls[0][1].query, "지난주"); assert.equal(calls[0][1].explain, true); assert.equal(calls[0][1].limit, 50);
  assert.match(h.$("#table").textContent, /‘지난주’/);
  assert.match(h.$("#table").textContent, /그때 기억을 찾지 못했어요/);
  view.detail(); assert.match(h.$("#detail").textContent, /기억을 골라 확인하세요/);
});

test("시간순 sorts by event time, groups by day, splits lanes and opens the memory", async () => {
  const items = [
    { id: "f", collection: "memories", kind: "decision", text: "배포는 금요일에 안 한다", created_at: "2026-10-20T01:00:00Z", memory_context: { observed_at: "2026-10-28T03:00:00Z" }, lanes: ["temporal"] },
    { id: "e", collection: "memories", kind: "episodic", text: "결제 오류를 고쳤다", created_at: "2026-09-10T05:00:00Z", occurred: { from: "2026-10-30T05:00:00Z", to: "2026-10-31T05:00:00Z" }, lanes: ["keyword", "temporal"] },
    { id: "o", collection: "observations", derived: true, kind: "semantic", text: "주로 오후에 배포한다", source_ids: ["f", "e"], updated_at: "2026-10-29T05:00:00Z" },
    { id: "old", collection: "memories", kind: "user", text: "예전 기억", created_at: "2026-09-01T05:00:00Z", lanes: ["vector"] }
  ];
  const opened = [];
  let reply = hits(items, week);
  const calls = [];
  const { view, opened: shown } = lineView(async (path, opts) => { calls.push(path); if (path.startsWith("/v1/memories/")) { if (reply instanceof Error) throw reply; return reply; } return hits(items, week); }, { openRecord: (r) => opened.push(r.id) });
  view.draw();
  const q = h.$("#chips form input"); dom.input(q, "지난주 배포"); h.$("#chips form").requestSubmit(); await dom.flush();
  const rows = () => h.$("#table").querySelectorAll(".original-row");
  assert.deepEqual(rows().map((r) => r.querySelector("strong").textContent), ["결제 오류를 고쳤다", "주로 오후에 배포한다", "배포는 금요일에 안 한다", "예전 기억"]);
  assert.deepEqual(rows().map((r) => r.querySelector(".mem-badge").textContent), ["겪은 일", "정리된 관찰", "사실", "사실"]);
  assert.match(rows()[2].textContent, /일어난 때/);
  assert.match(rows()[3].textContent, /찾은 기간 밖/);
  assert.equal(h.$("#table").querySelectorAll(".mem-time-day").length, 4);
  // the count sits only next to the chosen chip
  const chip = (l) => dom.buttons(h.$("#table .mem-time-lanes"), l)[0];
  assert.equal(chip("전체").querySelector("output").textContent, "4");
  assert.equal(chip("사실").querySelector("output"), null);
  chip("사실").click();
  assert.equal(rows().length, 2); assert.equal(chip("사실").querySelector("output").textContent, "2");
  chip("정리된 관찰").click(); rows()[0].click();
  assert.equal(shown(), 1);
  assert.match(h.$("#detail").textContent, /바탕 기억 2개/);
  assert.equal(dom.buttons(h.$("#detail"), "기억 화면에서 열기").length, 0); // an observation is not a memory record
  chip("전체").click(); rows()[2].click();
  assert.match(h.$("#detail").textContent, /찾은 방법시간/);
  reply = new Error("gone"); dom.buttons(h.$("#detail"), "기억 화면에서 열기")[0].click(); await dom.flush();
  assert.match(h.$("#detail").textContent, /열지 못했어요/);
  reply = { id: "f", status: "active", text: "배포는 금요일에 안 한다" }; dom.buttons(h.$("#detail"), "기억 화면에서 열기")[0].click(); await dom.flush();
  assert.deepEqual(opened, ["f"]); assert.equal(calls.at(-1), "/v1/memories/f?project_id=p");
  view.close();
  assert.equal(document.activeElement, rows()[2]);
});

test("시간순 says when no time was found, retries a failure and drops stale answers", async () => {
  let reply = hits([{ id: "a", collection: "memories", kind: "lesson", text: "x", created_at: "2026-10-01T00:00:00Z" }]);
  const { view, state } = lineView(async () => { if (reply instanceof Error) throw reply; return reply; });
  view.draw(); dom.input(h.$("#chips form input"), "결제"); h.$("#chips form").requestSubmit(); await dom.flush();
  assert.match(h.$("#table").textContent, /시간 표현을 찾지 못해/);
  for (const bad of [null, { authority: "x", items: [] }, { authority: "data_only_untrusted", items: [{ id: 1 }] }, new Error("net")]) {
    reply = bad; h.$("#chips form").requestSubmit(); await dom.flush();
    assert.match(h.$("#table").textContent, /찾지 못했어요. 연결과/);
  }
  reply = hits([], week); dom.buttons(h.$("#table"), "다시 찾기")[0].click(); await dom.flush();
  assert.match(h.$("#table").textContent, /‘지난주’/);
  let release; reply = new Promise((r) => { release = r; });
  const { view: v2, state: s2 } = lineView(async () => reply);
  v2.draw(); dom.input(h.$("#chips form input"), "어제"); h.$("#chips form").requestSubmit();
  s2.memTab = "active"; release(hits([{ id: "z", collection: "memories", text: "늦은 답" }])); await dom.flush();
  assert.ok(!h.$("#table").textContent.includes("늦은 답"));
  // coming back to the tab after that late answer can search again (not stuck on 찾는 중)
  s2.memTab = "past"; reply = hits([], week); v2.draw(); h.$("#chips form").requestSubmit(); await dom.flush();
  assert.match(h.$("#table").textContent, /‘지난주’/);
  // switching to 그 시각 상태 and back keeps each view's own form
  state.memTab = "past"; s2.memTab = "past"; v2.draw();
  dom.buttons(h.$("#chips"), "그 시각 상태")[0].click();
  assert.equal(h.$("#chips form input").type, "datetime-local");
  dom.buttons(h.$("#chips"), "시간순")[0].click();
  assert.equal(h.$("#chips form input").type, "search");
});

test("시간순 examples send the words the screen shows (English screen: English words the server also reads)", async () => {
  const before = globalThis.ArkismI18n, calls = [];
  globalThis.ArkismI18n = { translate: (s) => ({ 지난주: "Last week" })[s] || s, locale: () => "en-US" };
  try {
    const { view } = lineView(async (path, opts) => { calls.push(JSON.parse(opts.body)); return hits([], week); });
    view.draw(); dom.buttons(h.$("#table"), "지난주")[0].click(); await dom.flush();
    assert.equal(calls[0].query, "Last week");
    assert.equal(calls[0].timezone, Intl.DateTimeFormat().resolvedOptions().timeZone);
  } finally { globalThis.ArkismI18n = before; }
});
