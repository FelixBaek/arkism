// Run with: node --test desktop/ui/*.test.js
// MEMORY › 개요 stage windows on a small DOM stand-in.
const test = require("node:test");
const assert = require("node:assert/strict");
require("./keep.js"); // the shared stale-reply and redraw guards the screens use
const dom = require("./testdata/fake-dom.cjs");
const { document } = dom.install();
const h = dom.helpers(document);
require("./session-list.js");
require("./delete-reason.js"); // the reason picker the review rows share
require("./overview-windows.js");
require("./home-start.js"); // 분석's extractor failure words

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
  // more sessions than the loaded page: the server's sums count all of them, and the bars say whose they are
  Object.assign(env, { sessionsTotal: () => 120, sessionsSums: () => ({ messages: 900, tokens: 5000, today: 4 }) });
  win.open("extract");
  assert.ok(body().includes("최근 7일 대화 120개, 메시지 900개를 봤어요."));
  assert.ok(body().includes("막대는 최근 대화 3개 기준이에요."));
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

test("분류 AI 도움 can be cancelled, a late answer is ignored, and 다시 시도 asks again", async () => {
  const { win, state } = windows([], null, () => ({}));
  state.data.candidates = [{ id: "c1", kind: "lesson", text: "Keep PRs small", created_at: "2026-10-29T00:00:00Z" }];
  const asks = [];
  window.ArkismAIChat = { kit: {
    assist: (kind, prompt, opts) => new Promise((resolve, reject) => { asks.push({ resolve, signal: opts.signal, model: opts.model }); opts.signal.addEventListener("abort", () => reject(Object.assign(Error("취소했어요"), { code: "cancelled" }))); }),
    assistError: (a) => a.error, parseJSON: (t) => JSON.parse(t) } };
  win.open("classify");
  const ai = () => dom.buttons(document.body, "AI 도움").concat(dom.buttons(document.body, "취소"), dom.buttons(document.body, "다시 시도")).find((b) => b.querySelector("svg"));
  const note = () => document.querySelector(".ov-win-err").textContent;
  ai().click();
  assert.match(ai().textContent, /취소/);
  assert.match(note(), /1분 안에/);
  assert.equal(asks[0].model, "haiku", "one short pick goes to the fast model");
  assert.ok(dom.buttons(document.body, "승인")[0].disabled, "승인 waits while AI reads");
  ai().click(); await dom.flush();
  assert.ok(asks[0].signal.aborted);
  assert.equal(note(), "AI 도움을 취소했어요. 후보는 그대로예요.");
  assert.match(ai().textContent, /다시 시도/);
  asks[0].resolve({ status: "done", output: '{"pick":"approve","reason":"late"}' }); await dom.flush();
  assert.ok(document.querySelector(".ov-verdict").hidden, "an answer after cancel changes nothing");
  ai().click(); // 다시 시도
  asks[1].resolve({ status: "failed", error: "11분 안에 답이 오지 않았어요. 다시 시도해 주세요." }); await dom.flush();
  assert.match(note(), /AI 도움을 받지 못했어요: 11분 안에/);
  assert.match(ai().textContent, /다시 시도/);
  assert.ok(!dom.buttons(document.body, "승인")[0].disabled);
  ai().click();
  asks[2].resolve({ status: "done", output: '{"pick":"reject","reason":"one-off"}' }); await dom.flush();
  assert.match(document.querySelector(".ov-verdict").textContent, /AI 추천 · 버리기/);
  assert.match(ai().textContent, /AI 도움/);
  assert.equal(state.data.candidates[0].text, "Keep PRs small");
  delete window.ArkismAIChat;
  win.reset();
});

test("분석 explains an extractor failure in plain words with one fix, the code only under 자세히", () => {
  const cases = [["extractor_exit_1", "로그인하기", "agents"], ["extractor_no_json", "다른 모델 고르기", "roles"], ["TimeoutExpired", "다시 시도", null]];
  for (const [code, label, tab] of cases) {
    const state = { data: { memories: [], candidates: [], conflicted: [], tools: { extractor: { cli: "codex", state: "ready" }, analysis_run: { at: new Date().toISOString(), jobs: 1, failed: 1, error: code } } }, scope: {}, connection: { generation: 1 } };
    let went = null, reloaded = 0;
    const ctx = { state, ...h, query: (o = {}) => h.query(o), request: async () => ({ items: [] }), ago: () => "방금", go(v) { went = v; }, render() {}, reload: async () => { reloaded++; },
      remoteCollection: () => ({ load: async () => {}, reset() {}, snapshot: () => ({ items: [], total: 0, loading: false }) }) };
    const win = window.ArkismOverviewWindows(ctx, { stages: () => [{ key: "extract", label: "분석", tiles: [], badge: ["응답 없음", "a"] }], sessions: () => [] });
    win.open("extract");
    assert.ok(document.querySelector("dialog.ov-win-extract .ov-win-badge.a"), "the failed run's warning tone");
    assert.match(document.querySelector("dialog.ov-win-extract .ov-stage-what").textContent, /AI \(Codex\)/, "the extractor that runs, not a fixed name");
    const why = document.querySelector(".ov-sum-why");
    assert.doesNotMatch(why.textContent, /extractor_|TimeoutExpired/, "no raw code as the main text");
    const more = document.querySelector("details.ov-recall-more");
    assert.match(more.textContent, /자세히/); assert.ok(more.textContent.includes(code));
    const b = document.querySelectorAll("button").find((x) => x.textContent === label);
    assert.ok(b, label);
    b.click();
    if (tab) { assert.equal(went, "settings"); assert.equal(state.settingsTab, tab); } else assert.equal(reloaded, 1);
    win.reset();
  }
});

// 분류 › 자세히 used to open 승인 대기, which looked like the same screen; it now opens the row's own classification.
test("분류 자세히 shows the classification in the row", async () => {
  const { win, state } = windows([], null, () => ({}));
  state.data.candidates = [{ id: "c1", kind: "fact", text: "Deploy on Fridays", created_at: "2026-10-29T00:00:00Z" },
    { id: "c2", kind: "fact", text: "Uses port 8091", created_at: "2026-10-29T00:00:00Z", classification: { risk_reasons: ["개인 정보"] } }];
  win.open("classify");
  const dialogs = () => document.querySelectorAll("dialog").length, before = dialogs();
  const row = document.querySelectorAll(".ov-win-item").find((r) => r.textContent.includes("Deploy on Fridays"));
  const more = row.querySelector(".ov-class-more");
  assert.ok(more.hidden);
  dom.buttons(row, "자세히")[0].click(); await dom.flush();
  assert.ok(!more.hidden);
  assert.match(more.textContent, /아직 위험도와 중복 힌트가 붙지 않았어요/);
  assert.equal(dialogs(), before, "no second window");
  dom.buttons(row, "자세히")[0].click();
  assert.ok(more.hidden, "자세히 again folds it");
  assert.ok(!document.body.textContent.includes("Uses port 8091"), "a classified candidate has left 분류");
  win.reset();
});

test("기억 관리: numbers, a merge that asks first, retry on stopped work; an agent token gets no change buttons", async () => {
  require("./memory-management.js");
  const last = (sel) => document.querySelectorAll(sel).at(-1);
  const read = (path, body) => {
    if (path.startsWith("/v1/stats")) return { memories: { total: 5, by_status: { candidate: 5 } }, pending_candidates: 2, sources: { total: 1 } };
    if (path.startsWith("/v1/tags/rename")) return { renamed: 3, more: false, body };
    if (path.startsWith("/v1/tags")) return { items: [{ tag: "work", count: 3 }, { tag: "home", count: 1 }, { tag: "session:s1", count: 4 }] };
    if (path.startsWith("/v1/operations?")) return { items: [{ id: "op1", kind: "embedding", operable: true, revision: 4, updated_at: "2026-11-05T00:00:00Z" },
      { id: "op2", kind: "file_memory_extract", operable: true, revision: 2, updated_at: "2026-11-05T00:00:00Z" }] };
    return {};
  };
  const { win, calls, state } = windows([], {}, read);
  win.wide("ops"); await dom.flush(); await dom.flush();
  const dlg = last("dialog.ov-wide-win"), text = () => dlg.textContent;
  assert.match(text(), /0개쓰는 기억/, "a present status map without active means zero");
  assert.match(text(), /대화에서 자동으로 붙어 바꿀 수 없어요/);
  const btn = (root, t) => [root, ...root.querySelectorAll("button")].find((b) => b.tagName === "BUTTON" && b.textContent.trim() === t);
  btn(dlg, "이름 바꾸기").click();
  const input = dlg.querySelectorAll("input").find((n) => n.getAttribute("aria-label") === "새 태그 이름"); input.value = "home";
  btn(dlg, "바꾸기").click(); await dom.flush();
  const ask = last("dialog.ov-ask");
  assert.match(ask.textContent, /'work' 태그를 'home'에 합칠까요\?/);
  assert.ok(!calls.some((c) => c === "/v1/tags/rename"), "nothing renamed before the answer");
  btn(ask, "합치기").click(); await dom.flush();
  assert.ok(calls.includes("/v1/tags/rename"));
  assert.ok(btn(last("dialog.ov-wide-win"), "다시 시도"));
  assert.equal(last("dialog.ov-wide-win").querySelectorAll("button").filter((b) => b.textContent.trim() === "다시 시도").length, 1, "a file extraction resumes under its uploader, not here");
  win.reset?.();
  state.caps = { actions: { review_decide: false } };
  win.wide("ops"); await dom.flush(); await dom.flush();
  const agent = last("dialog.ov-wide-win");
  assert.equal(btn(agent, "이름 바꾸기"), undefined); assert.equal(btn(agent, "다시 시도"), undefined);
});

test("기억 관리 › 활동 기록, 모델 호출, 추이: filters reach the server, 더 보기 follows the cursor, an agent token is told plainly", async () => {
  require("./memory-management.js");
  require("./memory-pages.js"); // the shared cursor pager 더 보기 runs on
  const last = () => document.querySelectorAll("dialog.ov-wide-win").at(-1);
  const btn = (root, t) => root.querySelectorAll("button").find((b) => b.textContent.trim() === t);
  let denied = false;
  const read = (path) => {
    if (denied && /audit-events|llm-requests/.test(path)) throw Error("access_denied");
    if (path.startsWith("/v1/audit-events?")) return /cursor=c2/.test(path) ? { items: [{ id: "a2", action: "delete:memories", actor: "felix", actor_type: "human", created_at: "2026-11-04T00:00:00Z" }] }
      : { items: [{ id: "a1", action: "write:memories:defense_blocked", actor: "codex", actor_type: "agent", target: { collection: "memories", id: "m1" }, created_at: "2026-11-05T00:00:00Z" }], next_cursor: "c2" };
    if (path.startsWith("/v1/llm-requests/stats")) return { items: [{ day: "2026-11-05", operation: "embed", calls: 4, errors: 1, total_tokens: 40 }] };
    if (path.startsWith("/v1/llm-requests?")) return { items: [{ id: "l1", created_at: "2026-11-05T00:00:00Z", request: { operation: "rerank", generator: { model: "small-rerank" }, total_tokens: 12, latency_ms: 300, status: "error", error: "timeout" } }] };
    if (path.startsWith("/v1/stats/timeseries")) return { points: [{ bucket: "2026-11-04T00:00:00Z", key: "user", count: 2 }, { bucket: "2026-11-05T00:00:00Z", key: "user", count: 3 }] };
    return {};
  };
  const { win, calls } = windows([], {}, read);
  win.wide("ops"); await dom.flush(); await dom.flush();
  btn(last(), "활동 기록").click(); await dom.flush(); await dom.flush();
  assert.match(last().textContent, /기억 바꿈/); assert.match(last().textContent, /막힘/); assert.match(last().textContent, /write:memories:defense_blocked · m1/);
  btn(last(), "더 보기").click(); await dom.flush(); await dom.flush();
  assert.ok(calls.some((c) => /\/v1\/audit-events\?.*cursor=c2/.test(c)), "더 보기 asks for the next page");
  assert.match(last().textContent, /기억 지움/); assert.equal(btn(last(), "더 보기"), undefined);
  const kindSel = last().querySelectorAll("select").find((s) => s.getAttribute("aria-label") === "활동 종류");
  kindSel.value = "delete:"; kindSel.dispatchEvent(new dom.Event("change")); await dom.flush(); await dom.flush();
  assert.ok(calls.some((c) => /\/v1\/audit-events\?.*action=delete%3A/.test(c)), "the kind filter goes to the server");
  btn(last(), "모델 호출").click(); await dom.flush(); await dom.flush();
  assert.match(last().textContent, /검색 결과 다시 고르기/); assert.match(last().textContent, /small-rerank · timeout/); assert.match(last().textContent, /4번모델 호출/);
  btn(last(), "추이").click(); await dom.flush(); await dom.flush();
  assert.ok(calls.some((c) => /\/v1\/stats\/timeseries\?.*collection=memories.*by=kind.*bucket=day/.test(c)));
  assert.match(last().textContent, /모두 5개/);
  denied = true;
  btn(last(), "활동 기록").click(); await dom.flush(); await dom.flush();
  assert.match(last().textContent, /사람 계정으로 로그인하면 보여요/); assert.equal(btn(last(), "다시 시도"), undefined);
  win.reset?.();
});

test("기억 관리 ops: link kinds, an audit event's detail and transport filter, model calls by kind with tokens split, time axis, and the full job list", async () => {
  require("./memory-management.js");
  require("./memory-pages.js");
  const last = () => document.querySelectorAll("dialog.ov-wide-win").at(-1);
  const btn = (root, t) => root.querySelectorAll("button").find((b) => b.textContent.trim() === t);
  const sel = (label) => last().querySelectorAll("select").find((s) => s.getAttribute("aria-label") === label);
  const pickRow = (t) => last().querySelectorAll(".ov-ops-pick").find((r) => r.textContent.includes(t));
  const sent = [];
  let failedOps = [{ id: "j2", kind: "embedding", status: "failed", operable: true, revision: 2 }, { id: "j3", kind: "file_memory_extract", status: "failed", operable: true, revision: 1 }];
  const read = (path, body) => {
    if (body) sent.push([path, body]);
    if (path.startsWith("/v1/stats/timeseries")) return { points: [{ bucket: "2026-11-05T00:00:00Z", key: "user", count: 1 }] };
    if (path.startsWith("/v1/stats")) return { memories: { total: 1, by_status: { active: 1 } }, links: { causes: 3, supports: 1 } };
    if (path.startsWith("/v1/tags")) return { items: [] };
    if (path.startsWith("/v1/audit-events/stats")) return { total: 7, by_day: [{ bucket: "2026-11-04", count: 3 }, { bucket: "2026-11-05", count: 4 }], by_action: [{ key: "read:memories", count: 5 }] };
    if (path.startsWith("/v1/audit-events/a1")) return { id: "a1", action: "write:memories", actor: "codex", actor_type: "agent", transport: "mcp", method: "POST", route: "/v1/capture", duration_ms: 42, decision: "allow", mandatory: true, created_at: "2026-11-05T00:00:00Z" };
    if (path.startsWith("/v1/audit-events?")) return { items: [{ id: "a1", action: "write:memories", actor: "codex", actor_type: "agent", transport: "mcp", created_at: "2026-11-05T00:00:00Z" },
      { id: "a2", action: "read:memories", actor: "felix", actor_type: "human", transport: "http", created_at: "2026-11-05T00:00:00Z" }] };
    if (path.startsWith("/v1/llm-requests/stats")) return { items: [{ day: "2026-11-05", operation: "reflect", calls: 1, total_tokens: 30 }, { day: "2026-11-05", operation: "embedding_query", calls: 2, total_tokens: 4 }] };
    if (path.startsWith("/v1/llm-requests?")) return { items: [{ id: "l1", created_at: "2026-11-05T00:00:00Z", request: { operation: "reflect", generator: { model: "large" }, input_tokens: 20, output_tokens: 10, total_tokens: 30, latency_ms: 900, status: "ok", trace_id: "t-run-1", job_id: "j9" } },
      { id: "l2", created_at: "2026-11-05T00:00:00Z", request: { operation: "embedding_query", generator: { model: "embedder" }, total_tokens: 2, latency_ms: 10, status: "ok", trace_id: "t-run-1" } }] };
    if (/^\/v1\/operations\/j1\?/.test(path)) return { id: "j1", kind: "embedding", status: "running", operable: true, revision: 5, attempts: 2, lease_until: "2026-11-05T00:01:00Z", created_at: "2026-11-05T00:00:00Z", updated_at: "2026-11-05T00:00:00Z" };
    if (path.startsWith("/v1/operations?") && /status=failed/.test(path)) return { items: failedOps };
    if (path.startsWith("/v1/operations?")) return { items: [{ id: "j1", kind: "embedding", status: "running", operable: true, revision: 5 }, ...failedOps, { id: "j4", kind: "wiki_build", status: "done", operable: true, revision: 3 }, { id: "j5", kind: "capture_receipt", status: "done", operable: false, revision: 1 }] };
    return {};
  };
  const { win, calls } = windows([], {}, read);
  const opened = []; window.addEventListener("arkism:open-settings", (e) => opened.push(e.detail.tab));
  win.wide("ops"); await dom.flush(); await dom.flush();
  assert.match(last().textContent, /기억 사이 연결/); assert.match(last().textContent, /원인→결과3/);
  btn(last(), "모델 연결 시험").click();
  assert.deepEqual(opened, ["agents"], "the model test opens 설정 › 에이전트, where models.js runs it");
  win.wide("ops"); await dom.flush(); await dom.flush();
  // 활동 기록: the volume chart, then one event's detail; 어디서 filters each page here
  btn(last(), "활동 기록").click(); await dom.flush(); await dom.flush();
  assert.match(last().textContent, /요청량 · 모두 7건/); assert.match(last().textContent, /기억 읽음5/);
  pickRow("기억 바꿈").click(); await dom.flush(); await dom.flush();
  assert.ok(calls.some((c) => c.startsWith("/v1/audit-events/a1?")));
  for (const t of ["에이전트 도구(MCP)", "POST /v1/capture", "42ms", "예 — 끌 수 없어요"]) assert.ok(last().textContent.includes(t), t);
  assert.ok(pickRow("기억 바꿈").classList.contains("on"));
  sel("어디서").value = "http"; sel("어디서").dispatchEvent(new dom.Event("change")); await dom.flush(); await dom.flush();
  assert.equal(last().querySelectorAll(".ov-ops-pick").length, 1); assert.ok(pickRow("기억 읽음"));
  sel("어디서").value = ""; sel("어디서").dispatchEvent(new dom.Event("change")); await dom.flush();
  // 모델 호출: kind filter, in/out tokens, per-model fold, a call's run ids
  btn(last(), "모델 호출").click(); await dom.flush(); await dom.flush();
  assert.match(last().textContent, /입력 20 · 출력 10/); assert.match(last().textContent, /모델별 호출 \(불러온 것\)/); assert.match(last().textContent, /같은 실행에서 여러 번 부른 것/);
  sel("호출 종류").value = "embedding"; sel("호출 종류").dispatchEvent(new dom.Event("change")); await dom.flush(); await dom.flush();
  assert.equal(last().querySelectorAll(".ov-ops-pick").length, 1); assert.ok(pickRow("검색어 색인 만들기"));
  sel("호출 종류").value = ""; sel("호출 종류").dispatchEvent(new dom.Event("change")); await dom.flush(); await dom.flush();
  pickRow("질문에 답하기").click();
  for (const t of ["입력 토큰", "20 토큰", "t-run-1", "j9", "같은 실행의 호출"]) assert.ok(last().textContent.includes(t), t);
  // 추이: the time axis goes to the server
  btn(last(), "추이").click(); await dom.flush(); await dom.flush();
  sel("어느 때 기준").value = "occurred"; sel("어느 때 기준").dispatchEvent(new dom.Event("change")); await dom.flush(); await dom.flush();
  assert.ok(calls.some((c) => /\/v1\/stats\/timeseries\?.*time_field=occurred/.test(c)));
  // 작업: every job, the buttons each allows, delete asks first, retry-all skips what the route refuses
  btn(last(), "작업").click(); await dom.flush(); await dom.flush();
  const acts = (id) => pickRow(id === "j1" ? "도는 중" : id === "j4" ? "위키 만들기" : "").querySelectorAll("button").map((b) => b.textContent.trim());
  assert.deepEqual(acts("j1"), ["멈추기"]); assert.deepEqual(acts("j4"), ["지우기"]);
  assert.ok(last().textContent.includes("기타 작업"), "an unknown kind is named plainly");
  pickRow("도는 중").click(); await dom.flush(); await dom.flush();
  assert.ok(last().textContent.includes("2번") && last().textContent.includes("잡고 있는 시간"));
  btn(pickRow("위키 만들기"), "지우기").click();
  const ask = document.querySelectorAll("dialog.ov-ask").at(-1);
  assert.ok(!sent.some(([p]) => p === "/v1/operations/j4"), "nothing deleted before the answer");
  btn(ask, "지우기").click(); await dom.flush();
  assert.deepEqual(sent.find(([p]) => p === "/v1/operations/j4")[1], { scope: {}, expected_revision: 3 });
  btn(last(), "작업").click(); await dom.flush(); await dom.flush();
  btn(last(), "멈춘 작업 모두 다시 시도").click(); await dom.flush(); await dom.flush();
  assert.ok(sent.some(([p]) => p === "/v1/operations/j2/retry")); assert.ok(!sent.some(([p]) => p === "/v1/operations/j3/retry"), "a file extraction is left to its uploader");
  sel("상태").value = "failed"; sel("상태").dispatchEvent(new dom.Event("change")); await dom.flush(); await dom.flush();
  assert.ok(calls.some((c) => /\/v1\/operations\?.*status=failed/.test(c)));
  win.reset?.();
});
