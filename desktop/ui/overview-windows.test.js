// Run with: node --test desktop/ui/*.test.js
// MEMORY › 개요 stage windows on a small DOM stand-in.
const test = require("node:test");
const assert = require("node:assert/strict");
const dom = require("./testdata/fake-dom.cjs");
const { document } = dom.install();
const h = dom.helpers(document);
require("./session-list.js");
require("./overview-windows.js");

function windows(memories, memoryUse, read) {
  const state = { data: { memories, candidates: [], tools: { memory_use: memoryUse } }, scope: {}, connection: { generation: 1 } };
  const calls = [];
  const request = async (path, opts) => { calls.push(path); return read(path, opts && JSON.parse(opts.body)); };
  const ctx = { state, ...h, query: (o = {}) => h.query(o), request, ago: () => "방금", go() {}, render() {} };
  const env = { stages: () => [{ key: "trace", label: "사용 기록", tiles: [] }], sessions: () => [] };
  return { win: window.ArkismOverviewWindows(ctx, env), calls, state };
}
const titles = () => document.querySelectorAll(".ov-win-item-title").map((n) => n.textContent);
const byIds = (rows) => (path, body) => { if (path === "/v1/memories/query") return { items: rows.filter((r) => body.ids.includes(r.id)) }; throw Error("not_found"); };

test("사용 기록 names a used memory that is outside the loaded first page in one batch read (A3)", async () => {
  const { win, calls } = windows([{ id: "loaded", text: "Loaded rule" }], { days: 7, items: [{ id: "older", used: 9, injected: 9 }, { id: "loaded", used: 1, injected: 2 }, { id: "gone", used: 1, injected: 1 }] },
    byIds([{ id: "older", text: "Older rule", status: "active" }]));
  win.open("trace");
  assert.ok(document.querySelector("dialog.ov-wide-win"), "사용 기록 opens wide");
  assert.deepEqual(titles(), ["불러오는 중…", "Loaded rule", "불러오는 중…"]);
  await dom.flush(); await dom.flush();
  assert.deepEqual(titles(), ["Older rule", "Loaded rule", "(지워졌거나 아직 못 읽은 기억)"]);
  assert.deepEqual(calls, ["/v1/memories/query"], "both unknown names in one query");
  win.open("trace", false, true); // the stage window: cached, no second read
  assert.ok(titles().includes("Older rule"));
  assert.equal(calls.length, 1);
  win.reset();
});

test("a server without the ids filter falls back to one read per memory", async () => {
  const { win, calls } = windows([], { days: 7, items: [{ id: "a", used: 2, injected: 2 }, { id: "b", used: 1, injected: 1 }] },
    (path) => { if (path === "/v1/memories/query") throw Error("HTTP 400"); const id = path.split("/")[3].split("?")[0]; return { id, text: `Rule ${id}`, status: "active" }; });
  win.open("trace");
  await dom.flush(); await dom.flush(); await dom.flush();
  assert.deepEqual(titles(), ["Rule a", "Rule b"]);
  assert.equal(calls.filter((c) => c.startsWith("/v1/memories/") && c !== "/v1/memories/query").length, 2);
  win.reset();
});

test("사용 기록 sorts, filters and says the trend in words", async () => {
  const mems = [{ id: "x", kind: "lesson", text: "X rule" }, { id: "y", kind: "decision", text: "Y rule" }, { id: "z", kind: "fact", text: "Z rule" }];
  const { win, state } = windows(mems, { days: 30, injections: 20, items: [{ id: "x", injected: 10, used: 1 }, { id: "y", injected: 4, used: 3 }, { id: "z", injected: 6, used: 0, unused: true }] }, () => ({}));
  state.usage = new Map([["x", { d7: 9, d30: 12, last: "2026-10-01T00:00:00Z" }], ["y", { d7: 0, d30: 10, last: "2026-10-03T00:00:00Z" }]]);
  win.open("trace");
  assert.deepEqual(titles(), ["Y rule", "X rule", "Z rule"], "답에 쓰인 순 first");
  const sorter = document.querySelector("select.ov-wide-sort");
  for (const [v, want] of [["injected", ["X rule", "Z rule", "Y rule"]], ["last", ["Y rule", "X rule", "Z rule"]], ["trend", ["X rule", "Y rule", "Z rule"]]]) {
    document.querySelector("select.ov-wide-sort").value = v; dom.fire(document.querySelector("select.ov-wide-sort"), "change");
    assert.deepEqual(titles(), want, v);
  }
  assert.ok(sorter);
  assert.ok(document.body.textContent.includes("늘었어요 ↑") && document.body.textContent.includes("줄었어요 ↓"));
  const chip = document.querySelectorAll(".ov-wide-chip").find((c) => c.textContent.startsWith("안 쓰임")); chip.click();
  assert.deepEqual(titles(), ["Z rule"]);
  assert.ok(document.body.textContent.includes("넣었는데 답에 안 쓰였어요"));
  win.reset();
});

test("검증 lists a memory returned unused_after times without a receipt, and one with only an estimate stays out", async () => {
  const memories = [{ id: "stale", text: "Stale rule" }, { id: "guessed", text: "Guessed rule" }, { id: "fresh", text: "Fresh rule" }];
  const { win, state } = windows(memories, { days: 7, unused_after: 10, items: [
    { id: "stale", injected: 12, used: 0, estimated: 2, unreceipted: 12, unused: true },
    { id: "guessed", injected: 4, used: 0, estimated: 1, unreceipted: 4, unused: false },
    { id: "fresh", injected: 4, used: 0, estimated: 0, unreceipted: 4, unused: false }] }, () => { throw Error("not_found"); });
  state.data.memories = memories;
  win.open("judge");
  assert.deepEqual(titles(), ["Stale rule", "Fresh rule"]);
  assert.ok(document.body.textContent.includes("쓰였다는 보고 없이 12번 건넸어요"));
  win.reset();
});

test("분석 and 대화 저장 summarize what went in and what came out, each read once per open", async () => {
  const now = new Date().toISOString(), old = new Date(Date.now() - 3 * 86400000).toISOString();
  const state = { data: { memories: [{ id: "m1", kind: "decision", text: "Kept", tags: ["event_id:codex:s2:x"], created_at: now }], candidates: [
    { id: "c1", kind: "lesson", text: "A", tags: ["event_id:codex:s1:x"], classification: { dup_hint: "m1" }, created_at: now }], conflicted: [] }, scope: {}, connection: { generation: 1 } };
  const calls = [];
  const request = async (path, opts) => {
    calls.push(path);
    if (path.startsWith("/v1/memory_candidates?")) return { items: [{ id: "r1", kind: "fact", review: { reason: "작업과 관계없음" } }, { id: "r2", kind: "fact", review: { reason: "작업과 관계없음" } }] };
    if (path === "/v1/usage/sessions/archives") { assert.deepEqual(JSON.parse(opts.body).ids, ["ss-1", "ss-2", "ss-3"]); return { items: [{ id: "ss-1", state: "active", archives: [{ state: "active", expires_at: new Date(Date.now() + 2.5 * 86400000).toISOString() }] }, { id: "ss-2", state: "expired", archives: [] }], missing: ["ss-3"] }; }
    return {};
  };
  const ctx = { state, ...h, query: (o = {}) => h.query(o), request, ago: () => "방금", go() {}, render() {},
    remoteCollection: () => ({ load: async () => {}, reset() {}, snapshot: () => ({ items: sessions, total: sessions.length, loading: false }) }) };
  const sessions = [{ id: "ss-1", session_id: "s1", messages: 10, tokens: 100, ended_at: now }, { id: "ss-2", session_id: "s2", messages: 5, tokens: 50, ended_at: old }, { id: "ss-3", session_id: "s3", messages: 1, tokens: 0, ended_at: old }];
  const env = { stages: () => [{ key: "extract", label: "분석", tiles: [] }, { key: "collect", label: "대화 저장", tiles: [] }], sessions: () => sessions };
  const win = window.ArkismOverviewWindows(ctx, env), body = () => document.body.textContent;
  win.open("extract");
  assert.ok(body().includes("최근 대화 3개, 메시지 16개를 봤어요."));
  await dom.flush(); await dom.flush();
  assert.ok(body().includes("버린 이유") && body().includes("작업과 관계없음"), "rejections grouped by reason");
  assert.ok(/중복 의심1건/.test(body()) && /버림 \(7일\)2건/.test(body()) && /승인됨 \(7일\)1건/.test(body()));
  assert.equal(calls.filter((c) => c.startsWith("/v1/memory_candidates?")).length, 1, "the redraw reuses the read");
  win.open("collect");
  await dom.flush(); await dom.flush();
  assert.ok(body().includes("토큰 150개를 저장했어요"));
  assert.ok(/보관 중1개/.test(body()) && /만료됨1개/.test(body()) && /원문 없음1개/.test(body()));
  assert.ok(body().includes("가장 먼저 지워지는 원문은 3일 남았어요."));
  assert.ok(document.querySelector("dialog.ov-wide-win"), "대화 저장 opens wide");
  assert.ok(body().includes("분석 끝") && body().includes("원문 D-3") && body().includes("원문 만료") && body().includes("원문 없음"), "each saved conversation says what it produced and whether its text is kept");
  assert.equal(calls.filter((c) => c === "/v1/usage/sessions/archives").length, 1);
  win.reset();
});

test("AI 도움 then 승인 or 버리기 tells the server whether the person followed the advice (#466)", async () => {
  window.ArkismMemoryEvaluation = { reviewPayload: (id, p) => p };
  let pick = "approve";
  window.ArkismAIChat = { kit: { assist: async () => ({ status: "done", output: "x" }), assistError: () => "", parseJSON: () => ({ pick, reason: "좋아요" }) } };
  const cand = (id) => ({ id, kind: "fact", text: `후보 ${id}`, revision: 1, created_at: new Date().toISOString() });
  const state = { data: { memories: [], candidates: [cand("c1"), cand("c2"), cand("c3")], conflicted: [] }, scope: { project_id: "p" }, connection: { generation: 1 } };
  const posts = [];
  const request = async (path, opts) => { if (path === "/v1/decisions") posts.push(JSON.parse(opts.body)); return {}; };
  const ctx = { state, ...h, query: (o = {}) => h.query(o), request, ago: () => "방금", go() {}, render() {} };
  const win = window.ArkismOverviewWindows(ctx, { stages: () => [{ key: "classify", label: "분류", tiles: [] }], sessions: () => [] });
  win.open("classify"); // 분류 lists waiting candidates with 승인·버리기·AI 도움
  const row = (id) => document.querySelectorAll(".ov-win-item").find((r) => r.textContent.includes(`후보 ${id}`));
  const press = async (id, label) => { dom.buttons(row(id), label)[0].click(); await dom.flush(); };
  await press("c1", "AI 도움"); await press("c1", "승인"); // followed
  pick = "reject";
  await press("c2", "AI 도움"); await press("c2", "승인"); // went against it
  await press("c3", "승인"); // no AI help asked: nothing to count
  await dom.flush();
  assert.deepEqual(posts, [
    { scope: { project_id: "p" }, kind: "ai_help_accepted", target_id: "c1" },
    { scope: { project_id: "p" }, kind: "ai_help_discarded", target_id: "c2" },
  ]);
  delete window.ArkismAIChat; delete window.ArkismMemoryEvaluation;
});
