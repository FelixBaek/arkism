// Run with: node --test desktop/ui/*.test.js
// 기억 › 목록 · 타임라인 (modules/memoryViews.mjs through modules/memoryList.mjs), on a small DOM stand-in.
const test = require("node:test");
const assert = require("node:assert/strict");
require("./keep.js");
const dom = require("./testdata/fake-dom.cjs");

const at = (d) => `2026-${d}T10:00:00Z`;
const MEMS = [
  { id: "a", kind: "user", text: "Alex는 리뷰 코멘트를 파일별 목록으로 받는 걸 좋아한다", created_at: at("07-02") },
  { id: "b", kind: "decision", text: "결정 b", created_at: at("09-01") },
  { id: "c", kind: "decision", text: "같은 날 c", created_at: at("10-10") },
  { id: "d", kind: "lesson", text: "같은 날 d", created_at: at("10-10"), tags: ["review:pending_correction"] },
  { id: "e", kind: "lesson", text: "사건 날짜가 따로 있는 e", created_at: at("10-13"), occurred: { from: "2026-08-15T00:00:00Z" } },
  { id: "f", kind: "user", text: "가장 최근 f", created_at: at("10-20") }
];
const USED = { a: 0, b: 2, c: 1, d: 1, e: 40, f: 10 };

function setup() {
  const { document } = dom.install();
  require("./memory-evaluation.js");
  require("./memory-management.js");
  const h = dom.helpers(document);
  const main = h.el("div", "main"), head = h.el("div", "page-head");
  main.append(head);
  for (const id of ["controls", "chips", "stats", "table"]) { const n = h.el("div"); n.id = id; main.append(n); }
  const search = h.el("input"); search.id = "search"; search.value = "";
  document.body.append(main, search);
  const log = [], stored = {}, snaps = {}, calls = [];
  const snap = (name) => (snaps[name] ||= { items: [], total: undefined, facets: {}, error: null, loading: false, cursor: "" });
  const state = { data: { candidates: [], memories: [], conflicted: [], maintenance: [] }, usageDone: true, view: "memories", selected: null, caps: { actions: {} }, scope: {} };
  const deps = {
    state, $: h.$, el: h.el, icon: h.icon, button: h.button, raw: h.raw, DAY: 86400000,
    memory: { memTab: "active", repo: "", sort: "recent", kind: "all", query: "", memView: "list" },
    generation: 1, approvalUnproven: () => false, idle: (m) => m.id === "a",
    digests: { mergeCount: () => 0, notesCount: () => 0 }, insight: {},
    catalogQuery: { snapshot: snap, ensure: () => {}, more: async (n) => log.push(`more:${n}`), pin: (id) => log.push(`pin:${id}`) },
    memUse: () => null, go: () => {}, reviewOverdue: () => false, standing: () => false,
    memStat: (id) => ({ injected: 1, used: USED[id] || 0 }), usageOf: () => null, overlapReview: () => false,
    segment: (opts, value, pick) => { const s = h.el("div", "segment"); for (const [k, l] of opts) { const b = h.button(l, k === value ? "tab active" : "tab", null, () => pick(k)); b.dataset.key = k; s.append(b); } return s; },
    store: (k, v) => { stored[k] = v; },
    inject: { button: () => h.button("주입", "") },
    KINDS: { user: 1, decision: 1, lesson: 1 }, kindLabel: (k) => k || "기억",
    row: (id, tpl, cells, onClick) => { const r = h.el("div", "row"); r.dataset.id = id; r.append(...cells); r.addEventListener("click", onClick); return r; },
    originOf: () => "Codex", ago: () => "방금", shortDate: (v) => String(v).slice(0, 10), memRepo: () => "",
    renderDetail: () => log.push(`detail:${state.selected}`), markSelected: () => log.push("mark"), openDetail: () => log.push("open"),
    virtualList: { mount: ({ container, items, row }) => container.replaceChildren(...items.map(row)), clear: () => log.push("vclear") },
    render: () => log.push("render"), request: async (path) => { calls.push(path); return { nodes: [], edges: [{ from: "c", to: "d", type: "supports" }, { from: "c", to: "zz", type: "supports" }] }; },
    query: (o) => new URLSearchParams(o).toString(), notice: () => {},
    listCount: () => ({ state: "loaded", n: 0, min: 0, text: "0" }), idleCount: () => ({ state: "loaded", n: 0, min: 0, text: "0" }), partialMemories: () => false
  };
  snap("memories").items = MEMS; snap("memories").total = MEMS.length;
  return { document, h, deps, state, snap, log, stored, calls };
}

async function page(env) {
  const { createMemoryList } = await import("./modules/memoryList.mjs");
  const api = createMemoryList(env.deps);
  return { api, draw: () => api.renderMemories() };
}
const viewTab = (env, k) => env.document.querySelector(`#controls .mem-view-seg [data-key="${k}"]`);

test("the view switch (목록 · 타임라인) sits in the toolbar, is remembered, and keeps search, kind tabs and 자세히", async () => {
  const env = setup(), { draw } = await page(env);
  draw();
  assert.ok(env.document.querySelector("#controls .mem-tools .mem-view-seg"), "the segment sits in the toolbar row");
  assert.equal(env.document.querySelectorAll("#table .row").length, MEMS.length, "목록 is the plain list");
  env.deps.memory.query = "같은"; env.deps.memory.kind = "lesson";
  viewTab(env, "timeline").click();
  assert.equal(env.deps.memory.memView, "timeline");
  assert.equal(env.stored["arkism.memView"], "timeline", "the choice is stored");
  assert.deepEqual([env.deps.memory.query, env.deps.memory.kind], ["같은", "lesson"], "switching keeps the filters");
  draw();
  assert.ok(env.document.querySelector("#controls .panel-search"), "search stays");
  assert.ok(env.document.querySelector("#chips .mem-kind.active"), "kind tabs stay");
  assert.ok(env.document.querySelector("#chips .mem-more-pop"), "자세히 stays");
  assert.ok(env.document.querySelector("#table .mem-page-tl"));
  assert.equal(env.document.querySelectorAll("#table .row").length, 0);
  const more = env.document.querySelector("#chips .mem-more-pop");
  assert.equal(more.querySelector('select[aria-label="간격"]'), null, "no 간격 choice: the day strip and day groups are the one way");
  assert.equal(more.querySelector('select[aria-label="정렬"]'), null, "sorting means nothing on a timeline");
  assert.deepEqual(env.document.querySelectorAll("#controls .mem-view-seg .tab").map((b) => b.dataset.key), ["list", "timeline"], "no 그래프");
  env.deps.memory.memView = "graph"; draw();
  assert.ok(env.document.querySelectorAll("#table .row").length > 0 && !env.document.querySelector("#table .mem-page-tl"), "a stored 그래프 falls back to 목록");
});

test("the timeline catalog loads newest first, so 더 이전 불러오기 really loads older memories", async () => {
  const { createCatalogQuery } = await import("./modules/catalogQuery.mjs");
  const bodies = [];
  const state = { server: "s", scope: {}, connection: { generation: 1 }, memTab: "active", kind: "all", repo: "", sort: "used", query: "", memView: "timeline" };
  const createPager = (fetch) => ({ snapshot: () => ({}), load: (k) => fetch(k, { limit: 50 }) });
  const q = createCatalogQuery({ state, request: async (_, o) => { bodies.push(JSON.parse(o.body)); return { items: [], total: 0, facets: {}, next_cursor: "" }; }, createPager, preferences: () => ({ categories: [], agents: [], kept: [] }), render: () => {} });
  await q.more("memories");
  state.memView = "list"; await q.more("memories");
  assert.deepEqual(bodies.map((b) => b.sort), ["recent", "used"]);
});

test("timeline groups: newest day first, newest first inside a day", async () => {
  const { timelineGroups, memoryWhen } = await import("./modules/memoryViews.mjs");
  assert.equal(memoryWhen(MEMS[4]), Date.parse("2026-08-15T00:00:00Z"), "the event time wins over the saved time");
  const days = timelineGroups(MEMS);
  assert.deepEqual(days.map((g) => g.items.map((m) => m.id).join("")), ["f", "cd", "b", "e", "a"], "c and d share a day");
  assert.ok(days.every((g, i) => !i || g.at < days[i - 1].at));
});

test("the timeline: a month heading over a day strip, then day groups with rows, labels, keyboard and detail", async () => {
  const env = setup(), { draw } = await page(env);
  env.deps.memory.memView = "timeline"; env.deps.state.data.maintenance = [];
  draw();
  const box = env.document.querySelector("#table .mem-page-tl");
  const days = box.querySelectorAll(".tlc-day");
  assert.equal(days.length, 5);
  assert.ok(days.every((d) => d.querySelector(".tlc-dayhead").tagName === "H3"));
  assert.deepEqual(days.map((d) => d.querySelector(".tlc-n").textContent), ["1개", "2개", "1개", "1개", "1개"]);
  const rows = box.querySelectorAll(".mem-brief-row");
  assert.deepEqual(rows.map((r) => r.dataset.id), ["f", "c", "d", "b", "e", "a"]);
  assert.equal(rows[0].querySelector(".mem-brief-meta").textContent, "user · 답에 보임 10번");
  assert.equal(rows[2].querySelector(".mem-label").textContent, "정정 검토 중", "a memory waiting on review keeps its badge in place");
  assert.equal(rows[0].querySelector(".mem-labels"), null, "no label line without labels");
  assert.ok(rows[5].className.includes("muted"));
  // the strip: every day from the oldest memory to today; only days with memories are buttons to use, one tab stop
  const bars = box.querySelectorAll(".tlc-strip .tlc-bar"), live = bars.filter((b) => !b.disabled);
  assert.ok(bars.length > 120, "July to today, one bar a day");
  assert.equal(live.length, 5);
  assert.deepEqual(live.map((b) => b.tabIndex), [-1, -1, -1, -1, 0], "the newest day is the strip's one tab stop");
  assert.ok(bars.at(-1).className.includes("today"));
  assert.match(box.querySelector(".tlc-month").textContent, /2026/);
  // ← → walk the days with memories; a bar (Enter or click) jumps to its day and focuses its first memory
  live[4].focus(); dom.fire(live[4], "keydown", { key: "ArrowLeft" });
  assert.equal(env.document.activeElement, live[3]); assert.deepEqual([live[3].tabIndex, live[4].tabIndex], [0, -1]);
  dom.fire(live[3], "keydown", { key: "Home" }); assert.equal(env.document.activeElement, live[0]);
  live[1].click();
  assert.equal(days[3].scrolledIntoView, 1, "the 08-15 bar scrolls to its day");
  assert.equal(env.document.activeElement, rows[4]);
  // ↑ ↓ walk the rows
  rows[0].focus(); dom.fire(rows[0], "keydown", { key: "ArrowDown" }); assert.equal(env.document.activeElement, rows[1]);
  dom.fire(rows[1], "keydown", { key: "End" }); assert.equal(env.document.activeElement, rows[5]);
  rows[2].click();
  assert.equal(env.state.selected, "d");
  assert.deepEqual(env.log.slice(-4), ["pin:d", "mark", "detail:d", "open"]);
  draw();
  assert.equal(env.document.querySelector("#table .mem-brief-row.selected").dataset.id, "d");
  assert.equal(env.document.querySelector("#table .mem-brief-older"), null, "every page loaded: no 더 이전 불러오기");
});

test("the timeline loads older pages: at its bottom end, and when ‹ goes to a month not loaded yet", async () => {
  const env = setup(), { draw } = await page(env);
  const seen = [];
  globalThis.IntersectionObserver = class { constructor(cb) { this.cb = cb; seen.push(this); } observe(n) { this.n = n; } disconnect() { this.off = true; } };
  try {
    env.deps.memory.memView = "timeline"; env.snap("memories").cursor = "next";
    draw();
    const bars = env.document.querySelectorAll("#table .tlc-bar"), first = new Date(Number(env.document.querySelector("#table .tlc-day:last-child").dataset.at));
    let days = 0; for (const t = new Date(2026, 6, 3); t <= new Date(); t.setDate(t.getDate() + 1)) days++; // 07-03 through today, whatever today is
    assert.ok(bars.length === days && first.getMonth() === 6, "with pages left, the strip starts the day after the oldest saved memory (07-03), never on a day not loaded");
    const io = seen.find((o) => o.n === env.document.querySelector("#table .mem-brief-older"));
    io.cb([{ isIntersecting: true }]); io.cb([{ isIntersecting: true }]);
    assert.deepEqual(env.log.filter((l) => l === "more:memories"), ["more:memories"], "the bottom end loads once");
    draw();
    const month = () => env.document.querySelector("#table .tlc-month").textContent, prev = () => env.document.querySelector("#table .tlc-arrow.prev");
    assert.match(month(), /10/);
    prev().click(); assert.match(month(), /9/, "‹ goes to the newest day of the month before");
    prev().click(); assert.match(month(), /8/);
    const before = env.log.filter((l) => l === "more:memories").length;
    prev().click(); await new Promise((r) => setTimeout(r, 0));
    const loads = env.log.filter((l) => l === "more:memories").length - before;
    assert.ok(loads > 0 && loads <= 20, `July is not covered yet: older pages load, at most 20 (${loads})`);
    env.snap("memories").cursor = ""; draw(); await Promise.resolve();
    assert.match(month(), /7/, "once loaded, the heading names the month asked for");
    assert.equal(env.document.querySelector("#table .tlc-day:last-child").scrolledIntoView, 1, "and its newest day is scrolled to");
  } finally { delete globalThis.IntersectionObserver; }
});
