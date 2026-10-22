// Run with: node --test desktop/ui/*.test.js
const test = require("node:test");
const assert = require("node:assert/strict");
const { createHash } = require("node:crypto");
const dom = require("./testdata/fake-dom.cjs");

const SCOPE = { workspace_id: "w", project_id: "p", user_id: "u", agent_id: "" };

function setup(tab, routes, extra = {}) {
  const { document } = dom.install();
  dom.Element.prototype.setCustomValidity = function (m) { this.validity = m; };
  delete require.cache[require.resolve("./intelligence.js")];
  require("./intelligence.js");
  const h = dom.helpers(document);
  for (const id of ["app", "chips", "stats", "table", "detail"]) { const n = h.el("div"); n.id = id; document.body.append(n); }
  const state = { view: "memories", memTab: tab || "reflections", server: "s", token: "t", scope: { ...SCOPE }, caps: { role: "human" } };
  const calls = [], notices = [], opened = [];
  const request = async (path, opts) => {
    calls.push([path, opts]);
    for (const [prefix, fn] of routes) if (path.startsWith(prefix)) return fn(path, opts);
    throw new Error(`unexpected ${path}`);
  };
  const query = (o = {}) => new URLSearchParams(Object.entries({ ...state.scope, ...o }).filter(([, v]) => v !== "")).toString();
  const detailShell = (label) => { const body = h.el("div", "detail-body"); h.$("#detail").replaceChildren(h.el("span", "", label), body); return body; };
  const ctx = { state, $: h.$, el: h.el, icon: h.icon, button: h.button, request, query, detailShell, openDetail: () => h.$("#app").classList.add("detail-open"),
    notice: (m) => notices.push(m), openRecord: (r) => opened.push(r), ...extra };
  const view = window.ArkismIntelligence(ctx, tab);
  const submit = async (value) => { const input = h.$("#chips input"); input.value = value; dom.fire(h.$("#chips form"), "submit"); await dom.flush(); };
  return { view, state, calls, notices, opened, h, document, submit, routes };
}
const page = (items) => ({ authority: "data_only_untrusted", items });
const lesson = (o = {}) => ({ id: "l1", subject: "figma", interpretation: "프레임 이름을 먼저 정한다", observation: "두 번 다시 고침", status: "candidate",
  distinct_runs: 3, confidence: 0.5, evidence_refs: [], ...o });
const record = (id, o = {}) => ({ id, revision: 2, scope: { ...SCOPE }, text: "본문", status: "active", ...o });
const sha = (t) => createHash("sha256").update(t).digest("hex");
const toggle = (panel) => panel.dispatchEvent(new dom.Event("toggle")); // toggle does not bubble
// waits until the reader settles (the span check hashes on another thread)
const open = async (panel) => { panel.open = true; toggle(panel); for (let i = 0; i < 200 && panel.children[1].getAttribute("aria-busy") === "true"; i++) await dom.flush(1); };

test("reflections need a topic, list lessons for it and show their observation", async () => {
  const items = [lesson(), lesson({ id: "l2", status: "validated", interpretation: "다른 교훈", observation: "" })];
  const t = setup(undefined, [["/v1/reflections", () => page(items)]]);
  t.view.draw();
  assert.match(t.h.$("#table").textContent, /작업 주제를 입력하세요/);
  assert.equal(t.h.$("#chips input").required, true);
  await t.submit("   ");
  assert.equal(t.calls.length, 0);
  assert.match(t.h.$("#chips input").validity, /주제를 입력/);
  dom.fire(t.h.$("#chips input"), "input");
  assert.equal(t.h.$("#chips input").validity, "");
  await t.submit("x".repeat(129));
  assert.equal(t.calls.length, 0);
  await t.submit("figma");
  assert.match(t.calls[0][0], /^\/v1\/reflections\?.*subject=figma/);
  const rows = t.h.$("#table").querySelectorAll(".original-row");
  assert.equal(rows.length, 2);
  assert.match(t.h.$("#table").textContent, /2개 교훈/);
  assert.ok(rows[0].querySelector(".mem-badge.warn") && rows[1].querySelector(".mem-badge.strong"));
  rows[1].click();
  assert.ok(t.h.$("#app").classList.contains("detail-open"));
  const detail = t.h.$("#detail").textContent;
  assert.match(detail, /교훈 · figma · 검증됨/);
  assert.match(detail, /기록된 관측 설명이 없어요/);
  assert.match(detail, /연결된 근거가 없어요/);
  assert.match(detail, /독립 3회 · 점수 50%/);
  assert.equal(t.document.activeElement, t.h.$("#detail h2"));
  // drawing again with the same identity keeps the form and repaints
  const form = t.h.$("#chips form");
  t.view.draw();
  assert.equal(t.h.$("#chips form"), form);
  // detail() is a no-op while the same item is shown
  const shown = t.h.$("#detail h2");
  t.view.detail();
  assert.equal(t.h.$("#detail h2"), shown);
  t.view.close();
  assert.match(t.h.$("#detail").textContent, /교훈을 선택하세요/);
  assert.equal(t.document.activeElement, t.h.$("#table").querySelectorAll(".original-row")[1]);
});

test("a malformed or failed page shows an error with a retry, and quiet refresh keeps quiet", async () => {
  let reply = () => page([lesson({ subject: "other" })]);
  const t = setup(undefined, [["/v1/reflections", () => reply()]]);
  t.view.draw();
  await t.view.refresh(); // never searched: nothing
  assert.equal(t.calls.length, 0);
  await t.submit("figma");
  assert.match(t.h.$("#table").textContent, /불러오지 못했어요/);
  reply = () => page([lesson()]);
  dom.buttons(t.h.$("#table"), "다시 조회")[0].click();
  await dom.flush();
  assert.match(t.h.$("#table").textContent, /1개 교훈/);
  // quiet refresh with identical rows changes nothing; a failure is swallowed
  await t.view.refresh();
  reply = () => { throw new Error("down"); };
  await t.view.refresh();
  assert.match(t.h.$("#table").textContent, /1개 교훈/);
  // quiet refresh finds the selected item again in new rows
  t.h.$("#table .original-row").click();
  reply = () => page([lesson({ confidence: 0.9 })]);
  await t.view.refresh();
  assert.match(t.h.$("#detail").textContent, /점수 90%/);
  // an empty result
  reply = () => page([]);
  await t.submit("figma");
  assert.match(t.h.$("#table").textContent, /이 주제에 저장된 교훈이 없어요/);
  // not active: load does nothing
  t.state.memTab = "traits";
  const before = t.calls.length;
  await t.submit("figma");
  assert.equal(t.calls.length, before);
});

test("quiet refresh is skipped while the view says it cannot refresh", async () => {
  let blocked = false, n = 0;
  const t = setup(undefined, [["/v1/reflections", () => page([lesson({ confidence: (++n) / 10 })])]], { canRefresh: () => !blocked });
  t.view.draw();
  await t.submit("figma");
  blocked = true;
  await t.view.refresh();
  assert.match(t.h.$("#table").textContent, /1개 교훈/);
  t.h.$("#table .original-row").click();
  assert.match(t.h.$("#detail").textContent, /점수 10%/);
});

test("evidence opens the run record and its sources, checking the span hash", async () => {
  const src = "가나다라마바사";
  const ref = { collection: "agent_runs", id: "r1", revision: 2 };
  const runRecord = record("r1", { text: "실행 기록", status: "done", run: { task_type: "figma" },
    sources: [{ source_id: "s1", span: { start: 1, end: 3, content_hash: sha(src) } }, { source_id: "s2" }, { source_id: "s3", span: { start: 1, end: 3, content_hash: "bad" } }, { source_id: "s4", span: { start: 3, end: 1 } }] });
  const t = setup(undefined, [
    ["/v1/reflections", () => page([lesson({ evidence_refs: [ref, { collection: "bogus", id: "x", revision: 1 }, { collection: "agent_runs", id: "r9", revision: 1 }] })])],
    ["/v1/agent_runs/r1", () => runRecord],
    ["/v1/agent_runs/r9", () => record("r9", { sources: "nope", revision: 1 })],
    ["/v1/sources/s1", () => record("s1", { text: src })],
    ["/v1/sources/s2", () => record("s2", { text: "현재 출처", expires_at: "2999-01-01T00:00:00Z", effective_at: "2000-01-01T00:00:00Z" })],
    ["/v1/sources/s3", () => record("s3", { text: src })],
    ["/v1/sources/s4", () => record("s4", { text: src })]
  ]);
  t.view.draw();
  await t.submit("figma");
  t.h.$("#table .original-row").click();
  const readers = t.h.$("#detail .d-refs").children;
  assert.equal(readers.length, 3);
  await open(readers[0]);
  const content = readers[0].children[1];
  assert.match(content.textContent, /실행 기록/);
  assert.match(content.textContent, /작업 · figma \/ 결과 · 미기록/);
  assert.equal(content.getAttribute("aria-busy"), "false");
  const nested = content.querySelectorAll("details");
  assert.equal(nested.length, 4);
  await open(nested[0]);
  assert.match(nested[0].textContent, /기록된 원문 구간/);
  assert.equal(nested[0].querySelector(".original-text").textContent, "나다");
  await open(nested[1]);
  assert.match(nested[1].textContent, /현재 출처를 표시해요.*현재 출처/);
  await open(nested[2]);
  assert.match(nested[2].textContent, /근거를 읽을 수 없어요/);
  await open(nested[3]);
  assert.match(nested[3].textContent, /근거를 읽을 수 없어요/);
  // closing clears the content
  nested[0].open = false; toggle(nested[0]);
  assert.equal(nested[0].children[1].childNodes.length, 0);
  await open(readers[1]);
  assert.match(readers[1].textContent, /근거를 읽을 수 없어요/);
  await open(readers[2]);
  assert.match(readers[2].textContent, /근거를 읽을 수 없어요/);
  // a reader whose item is no longer selected stays silent
  const stale = readers[0];
  t.view.close();
  stale.open = true; toggle(stale); await dom.flush();
  assert.match(stale.textContent, /근거를 불러오는 중/);
});

test("record checks refuse wrong id, revision, scope, erased text and inactive sources", async () => {
  const bad = [
    { id: "other" }, { revision: 3 }, { revision: 0 }, { scope: { ...SCOPE, user_id: "x" } }, { content_erased: true },
    { privacy_quarantined: true }, { text: 1 }, { status: 2 }
  ];
  const sources = [{ status: "deprecated" }, { expires_at: "2000-01-01T00:00:00Z" }, { effective_at: "2999-01-01T00:00:00Z" }];
  let current;
  const refs = [{ collection: "agent_runs", id: "r1", revision: 2 }];
  const t = setup(undefined, [
    ["/v1/reflections", () => page([lesson({ evidence_refs: refs })])],
    ["/v1/agent_runs/", () => current],
    ["/v1/sources/", () => current.src]
  ]);
  t.view.draw();
  await t.submit("figma");
  t.h.$("#table .original-row").click();
  for (const b of bad) {
    current = record("r1", { sources: [], ...b });
    const reader = t.h.$("#detail .d-refs details");
    reader.open = false; toggle(reader);
    await open(reader);
    assert.match(reader.textContent, /근거를 읽을 수 없어요/, JSON.stringify(b));
  }
  for (const s of sources) {
    current = record("r1", { sources: [{ source_id: "s1" }] }); current.src = record("s1", s);
    const reader = t.h.$("#detail .d-refs details");
    reader.open = false; toggle(reader);
    await open(reader);
    const inner = reader.querySelector("details details") || reader.children[1].querySelector("details");
    await open(inner);
    assert.match(inner.textContent, /근거를 읽을 수 없어요/, JSON.stringify(s));
  }
});

test("a candidate lesson opens its representative candidate only while it still matches", async () => {
  let cand = record("l1", { status: "candidate", text: "프레임 이름을 먼저 정한다", kind: "lesson" });
  const t = setup(undefined, [["/v1/reflections", () => page([lesson()])], ["/v1/memory_candidates/l1", () => cand]]);
  t.view.draw();
  await t.submit("figma");
  t.h.$("#table .original-row").click();
  const action = dom.buttons(t.h.$("#detail"), "대표 후보 검토")[0];
  action.click(); await dom.flush();
  assert.equal(t.opened.length, 1);
  assert.equal(action.disabled, false);
  cand = { ...cand, text: "바뀜" };
  action.click(); await dom.flush();
  assert.equal(t.opened.length, 1);
  assert.match(t.h.$("#detail").textContent, /기록을 열 수 없어요/);
  // a validated lesson has no review link
  t.view.close();
});

test("traits list every claim, filter by the search word and open confirmed memories", async () => {
  const claims = [
    { id: "c1", claim: "짧은 답을 선호", type: "preference", status: "confirmed", revision: 2, distinct_runs: 4, confidence: 0.8, last_confirmed: "2026-01-02T00:00:00Z", evidence_refs: [] },
    { id: "c2", claim: "오전에 일함", type: "working_style", status: "inferred", revision: 1, distinct_runs: 1, confidence: 0.3, evidence_refs: [] },
    { id: "c3", claim: "목표", type: "stable_goal", status: "rejected", revision: 1, distinct_runs: 0, confidence: 0, evidence_refs: [] }
  ];
  let mem = record("c1", { kind: "user", text: "짧은 답을 선호" });
  const t = setup("traits", [["/v1/twin/claims", () => page(claims)], ["/v1/memories/c1", () => mem], ["/v1/memory_candidates/c2", () => record("c2", { revision: 1, kind: "user", text: "오전에 일함", status: "candidate" })]]);
  t.view.draw();
  assert.match(t.h.$("#table").textContent, /특성 조회를 눌러/);
  assert.equal(t.h.$("#chips input").required, false);
  await t.submit("x".repeat(129));
  assert.match(t.h.$("#chips input").validity, /조금 짧게/);
  await t.submit("");
  assert.match(t.calls[0][0], /^\/v1\/twin\/claims/);
  assert.equal(t.h.$("#table").querySelectorAll(".original-row").length, 3);
  t.h.$("#table .original-row").click();
  assert.match(t.h.$("#detail").textContent, /선호 · 확인됨/);
  assert.match(t.h.$("#detail").textContent, /기억 저장/);
  assert.match(t.h.$("#detail").textContent, /신뢰도 80%/);
  dom.buttons(t.h.$("#detail"), "기억 확인·정정")[0].click(); await dom.flush();
  assert.equal(t.opened[0].id, "c1");
  t.h.$("#table").querySelectorAll(".original-row")[1].click();
  dom.buttons(t.h.$("#detail"), "후보 검토")[0].click(); await dom.flush();
  assert.equal(t.opened[1].id, "c2");
  t.h.$("#table").querySelectorAll(".original-row")[2].click();
  assert.equal(dom.buttons(t.h.$("#detail"), "검토").length, 0);
  await t.submit("오전");
  assert.equal(t.h.$("#table").querySelectorAll(".original-row").length, 1);
  await t.submit("없는말");
  assert.match(t.h.$("#table").textContent, /표시할 사용자 특성이 없어요/);
  // a malformed claim is refused
  t.routes[0][1] = () => page([{ ...claims[0], type: "unknown" }]);
  await t.submit("");
  assert.match(t.h.$("#table").textContent, /불러오지 못했어요/);
});

test("relations show edges and confirm one only after a passing dry run", async () => {
  const edge = (o = {}) => ({ source: "figma", target: "export", relation_type: "often_followed_by", status: "candidate", occurrence_count: 5, confidence: 0.6,
    last_seen: "2026-01-02T00:00:00Z", scope: { ...SCOPE }, evidence_refs: [], ...o });
  let reply = (body) => ({ target: "tacit_relation", id: "figma|often_followed_by|export", promotable: true, promoted: !body.dry_run });
  const t = setup("relations", [["/v1/tacit/edges", () => page([edge(), edge({ source: "a", target: "b", status: "confirmed", confirmed_at: "2026-01-03T00:00:00Z" })])],
    ["/v1/improvements", (_, o) => reply(JSON.parse(o.body))]]);
  t.view.draw();
  assert.match(t.h.$("#table").textContent, /관계 조회를 눌러/);
  await t.submit("");
  assert.match(t.h.$("#table").textContent, /figma → export/);
  assert.match(t.h.$("#table").textContent, /독립 관측 5회 · 마지막/);
  t.h.$("#table").querySelectorAll(".original-row")[1].click();
  assert.match(t.h.$("#detail").textContent, /확인/);
  assert.equal(dom.buttons(t.h.$("#detail"), "확인 조건 검사").length, 0);
  t.h.$("#table .original-row").click();
  const [check] = dom.buttons(t.h.$("#detail"), "확인 조건 검사");
  const [confirm] = dom.buttons(t.h.$("#detail"), "관계 확인");
  assert.equal(confirm.hidden, true);
  check.click(); await dom.flush();
  assert.equal(JSON.parse(t.calls.at(-1)[1].body).dry_run, true);
  assert.equal(confirm.hidden, false);
  assert.match(t.h.$("#detail").textContent, /확인 조건을 충족했어요/);
  reply = () => ({ target: "tacit_relation", id: "figma|often_followed_by|export", promotable: false, promoted: false, reason: "low_confidence" });
  check.click(); await dom.flush();
  assert.match(t.h.$("#detail").textContent, /점수가 기준보다 낮아요/);
  assert.equal(confirm.hidden, true);
  reply = () => ({ target: "tacit_relation", id: "figma|often_followed_by|export", promotable: false, promoted: false, reason: "??" });
  check.click(); await dom.flush();
  assert.match(t.h.$("#detail").textContent, /확인되지 않았어요/);
  reply = () => ({ target: "other" });
  check.click(); await dom.flush();
  assert.match(t.h.$("#detail").textContent, /처리 결과를 확인하지 못했어요/);
  assert.equal(check.disabled, false);
  reply = (body) => ({ target: "tacit_relation", id: "figma|often_followed_by|export", promotable: true, promoted: !body.dry_run });
  confirm.click(); await dom.flush();
  assert.deepEqual(t.notices, ["관계를 확인했어요."]);
  // a non-human viewer gets no confirm controls
  t.state.caps.role = "agent";
  t.state.token = "t2"; // new identity resets the view
  t.view.draw();
  await t.submit("");
  t.h.$("#table .original-row").click();
  assert.equal(dom.buttons(t.h.$("#detail"), "확인 조건 검사").length, 0);
  // an edge with a | in its name is refused
  t.routes[0][1] = () => page([edge({ source: "a|b" })]);
  await t.submit("");
  assert.match(t.h.$("#table").textContent, /불러오지 못했어요/);
  t.routes[0][1] = () => page([]);
  await t.submit("");
  assert.match(t.h.$("#table").textContent, /표시할 작업 관계가 없어요/);
});
