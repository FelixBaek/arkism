// Run with: node --test desktop/ui/*.test.js
// Memory list (modules/memoryList.mjs), candidate groups (candidateGroups.mjs) and focus retention (listFocus.mjs).
const test = require("node:test");
const assert = require("node:assert/strict");
const dom = require("./testdata/fake-dom.cjs");

const DAY = 86400000;
const old = new Date(Date.now() - 40 * DAY).toISOString();
const now = new Date().toISOString();

function setup(overrides = {}) {
  const { document } = dom.install();
  require("./memory-evaluation.js");
  require("./memory-management.js");
  const h = dom.helpers(document);
  const main = h.el("div", "main"), head = h.el("div", "page-head");
  main.append(head);
  for (const id of ["controls", "chips", "stats", "table"]) { const n = h.el("div"); n.id = id; main.append(n); }
  const search = h.el("input"); search.id = "search"; search.value = "";
  document.body.append(main, search);
  const calls = [], notices = [], gone = [], log = [];
  const snaps = {};
  const snap = (name) => (snaps[name] ||= { items: [], total: undefined, facets: {}, error: null, loading: false, cursor: "" });
  const state = { data: { candidates: [], memories: [], conflicted: [], maintenance: null }, usageDone: true, view: "memories", selected: null, caps: { actions: { review_decide: true } }, scope: { workspace_id: "w" } };
  const panel = (name) => ({ draw: () => log.push(`draw:${name}`) });
  const deps = {
    state, $: h.$, el: h.el, icon: h.icon, button: h.button, raw: h.raw, DAY,
    memory: { memTab: "active", repo: "", sort: "recent", kind: "all", query: "" },
    generation: 1,
    approvalUnproven: (m) => !!m.unproven, idle: (m) => !!m.idle,
    digests: { mergeCount: () => 0, title: () => "주제 3개", draw: () => log.push("digests"), drawMerge: () => log.push("merge") },
    relations: panel("relations"), traits: panel("traits"), reflections: panel("reflections"), memoryTime: panel("past"), originals: panel("originals"),
    catalogQuery: { snapshot: snap, ensure: (n) => log.push(`ensure:${n}`), more: async (n) => log.push(`more:${n}`), refreshVisible: async () => log.push("refreshVisible") },
    memUse: () => null, memoryCount: () => 7,
    go: (...a) => gone.push(a),
    reviewOverdue: (m) => !!m.overdue, standing: (m) => !!m.standing,
    memStat: () => null, usageOf: () => null, overlapReview: (m) => !!m.often,
    segment: (opts, value, pick) => { const s = h.el("div", "segment"); for (const [k, l] of opts) s.append(h.button(l, k === value ? "on" : "", null, () => pick(k))); return s; },
    inject: { button: () => h.button("주입", "") },
    KINDS: { user: 1, project: 1 }, kindLabel: (k) => ({ user: "사용자", project: "프로젝트" }[k] || "기억"),
    row: (id, tpl, cells, onClick) => { const r = h.el("div", "row"); r.dataset.id = id; r.append(...cells); r.addEventListener("click", onClick); return r; },
    originOf: () => "Codex", ago: () => "방금", memRepo: (m) => m.repo || "", renderDetail: () => log.push("renderDetail"),
    virtualList: { mount: ({ container, items, row }) => container.replaceChildren(...items.map(row)) },
    render: (reset) => log.push(`render:${reset}`), markSelected: () => log.push("mark"), openDetail: () => log.push("open"),
    riskLabel: () => "위험 낮음", withAgent: () => "Claude", savedHow: () => "자동", waitReason: (c) => c.warn ? ["이유 — 자세히", true] : ["대기", false],
    request: async (path, opts) => { calls.push([path, opts]); return (deps.reply || (() => ({})))(path, opts); },
    query: () => "w=w",
    notice: (m) => notices.push(m), failNotice: (e, a, b) => notices.push(b), stateNotice: (k, _, o) => notices.push(o.text),
    collectionPaging: { read: async (n) => state.data[n] },
    memoryPages: { refresh: async () => state.data.memories }, memoryKey: () => "k",
    loadMaintenance: async () => "maint", memoryResults: { reset: () => log.push("resetResults") },
    hydrateMaintenance: () => log.push("hydrate"), loadMemoryTotals: () => log.push("totals"),
    // app.js listCount/idleCount: every row is loaded here, so a count is the rows' length
    listCount: (name) => { const n = state.data[name].length; return { state: "loaded", n, min: n, text: String(n) }; },
    idleCount: () => { const n = state.usageDone ? state.data.memories.filter(deps.idle).length : null; return { state: n === null ? "loading" : "loaded", n, min: n ?? 0, text: n === null ? "…" : String(n) }; },
    partialMemories: () => false,
    ...overrides
  };
  return { document, h, deps, state, snaps, snap, calls, notices, gone, log };
}

async function list(env) {
  const { createMemoryList } = await import("./modules/memoryList.mjs");
  return createMemoryList(env.deps);
}

test("memTitle names each tab and counts from the catalog or local data", async () => {
  const env = setup(); const api = await list(env);
  env.state.data.candidates = [{ id: "c" }]; env.state.data.conflicted = [{ id: "x" }, { id: "y" }];
  env.state.data.memories = [{ id: "a", unproven: true, idle: true }];
  assert.deepEqual(api.memTitle(), ["기억", "AI가 답할 때 참고하는 내용이에요.", "7개"]);
  env.deps.memUse = () => ({ injections: 4, used: 1 }); env.snap("memories").total = 12;
  assert.match(api.memTitle()[1], /25%/); assert.equal(api.memTitle()[2], "12개");
  env.deps.memory.memTab = "candidate";
  assert.equal(api.memTitle()[2], "4개"); // 1 candidate + 2 conflicted + 1 idle; provenance not counted
  env.snap("candidates").total = 9; assert.equal(api.memTitle()[2], "9개");
  env.deps.memory.memTab = "provenance"; assert.equal(api.memTitle()[0], "확인해 주세요");
  env.deps.memory.memTab = "digests"; assert.deepEqual(api.memTitle()[2], "주제 3개");
  env.deps.memory.memTab = "traits"; assert.equal(api.memTitle()[0], "사용자 특성");
  env.state.usageDone = false; env.deps.memory.memTab = "conflicted"; env.snap("memories").total = undefined;
  assert.equal(api.memTitle()[2], "3+개"); // idle use counts still loading: at least the 3 known, not exactly 3 (#465)
});

test("the main list draws check card, filters, chips, badges, paging and empty state", async () => {
  const env = setup(); const api = await list(env);
  const { deps, state, h } = env;
  state.data.candidates = [{ id: "c1" }]; state.data.conflicted = [];
  deps.digests.mergeCount = () => 2;
  const fresh = { created_at: now }, stale = { created_at: old };
  const items = [
    { id: "m1", text: "a", kind: "user", overdue: true, ...stale },
    { id: "m2", text: "b", standing: true, ...stale },
    { id: "m3", text: "c", idle: true, ...stale },
    { id: "m4", text: "d", stat: { injected: 3, used: 2 }, ...stale },
    { id: "m5", text: "e", stat: { injected: 0 }, ...fresh },
    { id: "m6", text: "f", stat: { injected: 0 }, ...stale },
    { id: "m7", text: "g", ...stale },
    { id: "m8", text: "h", use: { d30: 0 }, ...fresh },
    { id: "m9", text: "i", use: { d30: 0 }, ...stale },
    { id: "m10", text: "j", use: { d30: 5 }, repo: "o/r", ...stale }
  ];
  deps.memStat = (id) => items.find((m) => m.id === id).stat;
  deps.usageOf = (id) => items.find((m) => m.id === id).use;
  Object.assign(env.snap("memories"), { items, total: 60, cursor: "next", facets: { repositories: { "o/r": 4, "": 2 }, kinds: { user: 3 } } });
  deps.memory.repo = "x/y";
  api.renderMemories();
  assert.deepEqual(env.log.slice(0, 1), ["ensure:memories"]);
  const controls = h.$("#controls");
  assert.match(controls.textContent, /확인해 주세요3/);
  assert.match(controls.textContent, /병합 제안 2/);
  assert.equal(h.$("#mem-back").hidden, true);
  const badges = h.$("#table").querySelectorAll(".mem-badge").map((b) => b.textContent);
  assert.deepEqual(badges, ["재검토 예정일 지남", "항상 적용", "검토 필요", "문구 겹침 2번", "새 기억", "—", "…", "새 기억", "안 불림", "5번 참고"]);
  assert.match(h.$("#table .collection-more").textContent, /불러온 10개 중 10개 표시/);
  const chips = h.$("#chips").querySelectorAll("button");
  assert.deepEqual(chips.map((c) => c.textContent), ["전체3", "사용자3", "프로젝트0"]);
  chips[1].click(); assert.equal(deps.memory.kind, "user");
  // card button goes to the first non-empty check list
  dom.buttons(controls, "하나씩 확인하기")[0].click();
  assert.deepEqual(env.gone.at(-1), ["memories", "candidate"]);
  const [repo, look, sort] = controls.querySelectorAll("select");
  assert.deepEqual(repo.options.map((o) => o.textContent), ["저장소: 모두", "저장소: 공용 2", "저장소: r 4", "저장소: y 0"]);
  dom.input(repo, "o/r"); assert.equal(deps.memory.repo, "o/r");
  dom.input(look, ""); dom.input(look, "traits"); assert.deepEqual(env.gone.at(-1), ["memories", "traits"]);
  dom.input(sort, "used"); assert.equal(deps.memory.sort, "used");
  // search box mirrors into #search and the memory query
  const q = controls.querySelector(".panel-search input");
  q.value = "  Hello "; q.selectionStart = 3; dom.fire(q, "input");
  assert.equal(h.$("#search").value, "  Hello "); assert.equal(deps.memory.query, "hello");
  // "더 보기" fetches the next server page
  await h.$("#table .collection-more button").click();
  await dom.flush();
  assert.ok(env.log.includes("more:memories"));
  // a row click opens detail
  h.$("#table .row").click(); assert.ok(env.log.includes("renderDetail"));
  // the back button is reused and leads back to the main list
  deps.memory.memTab = "past"; api.renderMemories();
  assert.equal(h.$("#mem-back").hidden, false); h.$("#mem-back").click();
  assert.deepEqual(env.gone.at(-1), ["memories", "active"]);
  assert.ok(env.log.includes("draw:past"));
  dom.buttons(h.$("#controls"), "작업 관계")[0].click(); assert.deepEqual(env.gone.at(-1), ["memories", "relations"]);
});

test("an empty main list explains why and kinds show no numbers on an older server", async () => {
  const env = setup(); const api = await list(env);
  env.snap("memories").error = new Error("x");
  api.renderMemories();
  assert.match(env.h.$("#table").textContent, /여기에 표시할 기억이 없어요/);
  assert.match(env.h.$("#stats").textContent, /불러오지 못했어요/);
  assert.equal(env.h.$("#controls .mem-check"), null);
  assert.deepEqual(env.h.$("#chips").querySelectorAll("output").map((o) => o.textContent), ["", "", ""]);
  env.deps.memory.query = "zz"; env.snap("memories").error = null; env.snap("memories").loading = true;
  api.renderMemories();
  assert.match(env.h.$("#table").textContent, /검색과 맞는 기억이 없어요/);
  assert.match(env.h.$("#stats").textContent, /찾는 중/);
  env.deps.memory.memTab = "digests"; api.renderMemories(); assert.ok(env.log.includes("digests"));
  env.deps.memory.memTab = "merge"; api.renderMemories(); assert.ok(env.log.includes("merge"));
});

test("check lists draw provenance, idle and conflicted rows with their reasons", async () => {
  const env = setup(); const api = await list(env);
  const { deps, h } = env;
  deps.memory.memTab = "provenance";
  env.snap("memories").items = [{ id: "p1", text: "p", created_at: old }];
  api.renderMemories();
  assert.match(h.$("#table").textContent, /승인 경로 확인/);
  assert.equal(h.$("#chips").children.length, 0);
  deps.memory.memTab = "idle"; deps.memUse = () => ({ days: 7 }); deps.memStat = () => ({ injected: 4, used: 0 });
  env.snap("memories").items = [{ id: "i1", text: "a", overdue: true }, { id: "i2", text: "b", often: true }, { id: "i3", text: "c" }];
  api.renderMemories();
  assert.deepEqual(h.$("#table").querySelectorAll(".mem-badge").map((b) => b.textContent), ["재검토 예정일 지남", "문구 겹침 미확인", "최근 호출 없음"]);
  assert.match(h.$("#table").textContent, /최근 7일 4번 전달 · 문구 겹침 0번/);
  deps.memory.memTab = "conflicted";
  env.snap("conflicted").items = [{ id: "x1", text: "x", warn: true }, { id: "x2", text: "y" }];
  api.renderMemories();
  assert.deepEqual(h.$("#table").querySelectorAll(".mem-badge").map((b) => b.className), ["mem-badge warn", "mem-badge"]);
  assert.equal(h.$("#table .mem-badge").textContent, "이유");
  env.snap("conflicted").items = []; deps.memory.query = "q";
  api.renderMemories();
  assert.match(h.$("#table").textContent, /검색과 맞는 기억이 없어요/);
  deps.memory.query = ""; api.renderMemories();
  assert.match(h.$("#table").textContent, /확인할 기억이 없어요/);
  dom.buttons(h.$("#controls"), "헷갈림")[0].click(); assert.deepEqual(env.gone.at(-1), ["memories", "conflicted"]);
});

test("candidates group, offer batch and stale cleanup, and expire through the server", async () => {
  const env = setup(); const api = await list(env);
  const { deps, state, h } = env;
  deps.memory.memTab = "candidate";
  const c = (id, text, extra = {}) => ({ id, status: "candidate", kind: "user", text, created_at: old, scope: { workspace_id: "w" }, ...extra });
  const items = [c("a", "응답은 짧게"), c("b", "응답은 짧게"), c("d", "다른 것", { conflict_id: "k" })];
  state.data.candidates = items; env.snap("candidates").items = items;
  api.renderMemories();
  assert.match(h.$("#chips").textContent, /3개 후보/);
  dom.buttons(h.$("#chips"), "여러 개 한 번에 처리")[0].click();
  assert.equal(state.selected, "batch"); assert.deepEqual(env.log.slice(-3), ["mark", "renderDetail", "open"]);
  const stale = dom.buttons(h.$("#chips"), "30일 지난 후보 정리 2")[0];
  assert.ok(h.$("#table details.candidate-group"));
  assert.equal(h.$("#table").querySelectorAll(".row").length, 1); // collapsed group draws no children; single draws a row
  // declined: nothing sent
  globalThis.ask = async () => false;
  await stale.click(); await dom.flush();
  assert.equal(env.calls.length, 0);
  // accepted: server expires, notice and reload
  globalThis.ask = async () => true;
  deps.reply = () => ({ expired: [1, 2] });
  stale.click(); await dom.flush();
  assert.equal(env.calls[0][0], "/v1/memory_candidates/expire");
  const body = JSON.parse(env.calls[0][1].body);
  assert.equal(body.limit, 100); assert.deepEqual(body.scope, { workspace_id: "w" });
  assert.equal(env.notices.at(-1), "후보 2개를 정리했어요.");
  assert.ok(env.log.includes("refreshVisible") && env.log.includes("hydrate") && env.log.includes("totals"));
  assert.equal(stale.disabled, true); // stays off until the list redraws
  stale.disabled = false; deps.reply = () => ({});
  stale.click(); await dom.flush();
  assert.equal(env.notices.at(-1), "정리할 후보가 없었어요.");
  stale.disabled = false; deps.reply = () => { throw new Error("forbidden"); };
  stale.click(); await dom.flush();
  assert.equal(env.notices.at(-1), "정리하지 못했어요 (forbidden).");
  assert.equal(stale.disabled, false);
  // no decide capability: no batch or stale buttons; empty list message
  state.caps = {}; api.renderMemories();
  assert.equal(dom.buttons(h.$("#chips")).length, 0);
  env.snap("candidates").items = []; api.renderMemories();
  assert.match(h.$("#table").textContent, /확인할 기억이 없어요/);
  deps.memory.query = "q"; api.renderMemories();
  assert.match(h.$("#table").textContent, /검색과 맞는 기억이 없어요/);
});

test("reloadMemories ignores a result from an older connection and tolerates a failed conflicted read", async () => {
  const env = setup(); const api = await list(env);
  env.deps.collectionPaging.read = async (n) => { if (n === "conflicted") throw new Error("x"); return ["fresh"]; };
  env.state.data.conflicted = ["kept"];
  await api.reloadMemories();
  assert.deepEqual(env.state.data.conflicted, ["kept"]); assert.equal(env.state.data.maintenance, "maint");
  env.deps.loadMaintenance = async () => { env.deps.generation++; return "late"; };
  await api.reloadMemories();
  assert.equal(env.state.data.maintenance, "maint");
});

test("decide reviews once per item with an operation id and reports conflicts", async () => {
  const env = setup(); const api = await list(env);
  let release; env.deps.reply = () => new Promise((r) => { release = r; });
  const first = api.decide({ id: "c/1", revision: 3 }, "approve");
  await api.decide({ id: "c/1", revision: 3 }, "approve"); // ignored while the first is running
  assert.equal(env.calls.length, 1);
  release({}); await first;
  const [path, opts] = env.calls[0];
  assert.equal(path, "/v1/memory_candidates/c%2F1/review");
  const body = JSON.parse(opts.body);
  assert.equal(body.action, "approve"); assert.equal(body.expected_revision, 3); assert.ok(body.operation_id);
  assert.equal(env.notices.at(-1), "기억으로 저장했어요.");
  env.deps.reply = () => ({});
  await api.decide({ id: "c2", revision: 1 }, "reject", "틀림");
  assert.equal(JSON.parse(env.calls.at(-1)[1].body).reason, "틀림");
  assert.equal(env.notices.at(-1), "후보를 버렸어요.");
  env.deps.reply = () => { throw new Error("revision_conflict"); };
  await api.decide({ id: "c3" }, "approve");
  assert.match(env.notices.at(-1), /이미 처리됐거나/);
  env.deps.reply = () => { throw new Error("boom"); };
  env.deps.collectionPaging.read = async () => { throw new Error("reload failed"); };
  assert.equal(await api.reviewOnce("z", async () => { throw new Error("boom"); }), false, "a failure says so to the caller");
  assert.equal(await api.reviewOnce("z", async () => {}), true);
  assert.equal(env.notices.at(-1), "처리하지 못했어요: boom");
});

test("resolveConflict sends the chosen resolution and deletes the old memory on drop", async () => {
  const env = setup(); const api = await list(env);
  const rel = { id: "rel", revision: 2, relations: [{ collection: "memories", id: "m1", revision: 5 }] };
  env.deps.reply = (path) => (path.startsWith("/v1/memory_relations/") ? rel : {});
  const c = { id: "c1", conflict_id: "k1", revision: 4 };
  for (const [choice, action, resolution, note] of [["new", "approve", "replace_old", "새 내용으로"], ["both", "approve", "keep_both", "둘 다"], ["keep_old", "reject", "keep_old", "기존 기억을"]]) {
    env.calls.length = 0;
    await api.resolveConflict(c, choice);
    assert.equal(env.calls[0][0], "/v1/memory_relations/k1?w=w");
    const body = JSON.parse(env.calls[1][1].body);
    assert.equal(body.action, action); assert.equal(body.conflict.resolution, resolution);
    assert.equal(body.conflict.memory.id, "m1"); assert.equal(!!body.reason, action === "reject");
    assert.ok(env.notices.at(-1).startsWith(note));
  }
  env.calls.length = 0;
  await api.resolveConflict(c, "drop");
  assert.equal(env.calls[2][1].method, "DELETE");
  assert.equal(JSON.parse(env.calls[2][1].body).expected_revision, 6);
  assert.match(env.notices.at(-1), /모두 지웠어요/);
  env.deps.reply = (path, opts) => { if (opts?.method === "DELETE") throw new Error("x"); return path.startsWith("/v1/memory_relations/") ? rel : {}; };
  await api.resolveConflict(c, "drop");
  assert.match(env.notices.at(-1), /지금 기억은 지우지 못했어요/);
  env.deps.reply = () => ({ relations: [] });
  await api.resolveConflict(c, "new");
  assert.match(env.notices.at(-1), /이미 처리됐거나/);
  assert.equal(typeof api.memSelect, "function");
});

test("candidate groups page 50 at a time and read evidence when opened", async () => {
  const { document } = dom.install();
  require("./memory-management.js");
  const h = dom.helpers(document);
  const { candidateGroup } = await import("./modules/candidateGroups.mjs");
  const row = (c) => { const r = h.el("div", "row", c.text); r.dataset.id = c.id; return r; };
  const reqs = [];
  const request = async (path) => { reqs.push(path); if (path.includes("bad")) throw new Error("x"); return { complete: true, user_mention_keys: [path] }; };
  assert.equal(candidateGroup({ items: [{ id: "one", text: "t" }] }, { el: h.el, button: h.button, row, request, query: () => "q" }).textContent, "t");
  const items = Array.from({ length: 60 }, (_, i) => ({ id: i === 1 ? "bad" : `c${i}`, text: `후보 ${i}` }));
  const box = candidateGroup({ items }, { el: h.el, button: h.button, row, request, query: () => "q=1" });
  document.body.append(box);
  assert.match(box.textContent, /비슷한 후보 60개/);
  box.open = true; dom.fire(box, "toggle");
  dom.fire(box, "toggle"); // second toggle while loading only repaints
  assert.equal(box.querySelectorAll(".row").length, 50);
  await dom.flush(20);
  assert.equal(reqs.length, 32);
  assert.equal(reqs[0], "/v1/memory_candidates/c0/evidence-summary?q=1");
  assert.match(box.querySelector("[aria-live]").textContent, /서로 다른 사용자 발화 31회 이상 확인/);
  const [prev, next] = ["이전 후보", "다음 후보"].map((l) => dom.buttons(box, l)[0]);
  assert.equal(prev.disabled, true);
  next.click();
  assert.equal(box.querySelectorAll(".row").length, 10);
  assert.match(box.textContent, /60개 중 51–60개 표시/);
  box.querySelectorAll("button").find((b) => b.textContent === "이전 후보").click();
  assert.match(box.textContent, /1–50개 표시/);
  box.open = false; dom.fire(box, "toggle");
  assert.equal(box.querySelectorAll(".row").length, 0);
  // a box removed before evidence arrives does not write its label
  const gone = candidateGroup({ items: items.slice(0, 3) }, { el: h.el, button: h.button, row, request, query: () => "" });
  document.body.append(gone); gone.open = true; dom.fire(gone, "toggle"); gone.remove();
  await dom.flush();
  assert.match(gone.querySelector("[aria-live]").textContent, /확인하는 중/);
});

test("retainListFocus restores a row control, a filter input, or nothing", async () => {
  const { document, window } = dom.install();
  const h = dom.helpers(document);
  const { retainListFocus } = await import("./modules/listFocus.mjs");
  const table = h.el("div"); table.id = "table"; const controls = h.el("div"); controls.id = "controls";
  document.body.append(controls, table);
  const mkRow = (id) => { const r = h.el("div", "row"); r.dataset.id = id; r.append(h.button("a", ""), h.button("b", "")); return r; };
  table.append(mkRow("x"));
  table.querySelectorAll("button")[1].focus();
  let restore = retainListFocus(document);
  table.replaceChildren(mkRow("x")); document.activeElement = document.body;
  restore();
  assert.equal(document.activeElement, table.querySelectorAll("button")[1]);
  // focus moved elsewhere meanwhile: left alone
  restore = retainListFocus(document);
  const other = h.button("o", ""); document.body.append(other); other.focus();
  restore(); assert.equal(document.activeElement, other);
  // row not drawn yet: retried on the next frames
  table.querySelectorAll("button")[0].focus();
  restore = retainListFocus(document);
  table.replaceChildren(); document.activeElement = document.body;
  restore();
  table.append(mkRow("x"));
  await dom.flush();
  assert.equal(document.activeElement, table.querySelector("button"));
  // a filter input keeps its caret
  const q = h.el("input"); q.setAttribute("aria-label", "검색"); q.selectionStart = 2; q.selectionEnd = 3; controls.append(q); q.focus();
  restore = retainListFocus(document);
  const q2 = h.el("input"); q2.setAttribute("aria-label", "검색"); let range;
  q2.setSelectionRange = (a, b) => { range = [a, b]; };
  controls.replaceChildren(q2); document.activeElement = document.body;
  restore();
  assert.equal(document.activeElement, q2); assert.deepEqual(range, [2, 3]);
  restore = retainListFocus(document); q2.focus(); restore(); // still focused: untouched
  // a select is found by its label; nothing else is tracked
  const s = h.el("select"); s.setAttribute("aria-label", "정렬"); controls.replaceChildren(s); s.focus();
  restore = retainListFocus(document);
  const s2 = h.el("select"); s2.setAttribute("aria-label", "정렬"); controls.replaceChildren(s2); document.activeElement = document.body;
  restore(); assert.equal(document.activeElement, s2);
  other.focus(); retainListFocus(document)();
  assert.equal(document.activeElement, other);
  void window;
});
