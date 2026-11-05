// Run with: node --test desktop/ui/*.test.js
// 기억 › 목록 · 타임라인 · 그래프 (modules/memoryViews.mjs through modules/memoryList.mjs), on a small DOM stand-in.
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

// memory-bubbles.js draws on a canvas the stand-in DOM lacks: a recorder with the same calls
function fakeBubbles(env, made = []) {
  require("./memory-bubbles.js");
  const real = window.ArkismMemoryBubbles;
  window.ArkismMemoryBubbles = { KIND_COLOR: real.KIND_COLOR, create: (opts) => { const m = { opts, el: env.h.el("div", "mm-bubbles"), destroyed: 0, sel: [], setItems: (x) => { m.items = x; }, setEdges: (x) => { m.edges = x; }, setSelected: (id) => m.sel.push(id), destroy: () => { m.destroyed++; } }; made.push(m); return m; } };
  return { made, real };
}

async function page(env) {
  const { createMemoryList } = await import("./modules/memoryList.mjs");
  const api = createMemoryList(env.deps);
  return { api, draw: () => api.renderMemories() };
}
const viewTab = (env, k) => env.document.querySelector(`#controls .mem-view-seg [data-key="${k}"]`);

test("the view switch sits in the toolbar, is remembered, and keeps search, kind tabs and 자세히 for every view", async () => {
  const env = setup(), { draw } = await page(env);
  fakeBubbles(env);
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
  assert.ok(env.document.querySelector("#table .mem-tl"));
  assert.equal(env.document.querySelectorAll("#table .row").length, 0);
  const more = env.document.querySelector("#chips .mem-more-pop");
  assert.ok(more.querySelector('select[aria-label="간격"]'), "zoom lives in 자세히 on the timeline");
  assert.equal(more.querySelector('select[aria-label="정렬"]'), null, "sorting means nothing on a timeline");
  viewTab(env, "graph").click(); draw();
  assert.ok(env.document.querySelector("#controls .panel-search") && env.document.querySelector("#chips .mem-kind.active"));
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

test("timeline layout: oldest left, newest right, labels alternate, size follows use, same day shares one dot", async () => {
  const { timelineLayout, memoryWhen } = await import("./modules/memoryViews.mjs");
  assert.equal(memoryWhen(MEMS[4]), Date.parse("2026-08-15T00:00:00Z"), "the event time wins over the saved time");
  const lay = timelineLayout(MEMS, { use: (m) => USED[m.id], hollow: (m) => m.id === "a", review: (m) => (m.tags || []).length > 0 });
  assert.equal(lay.dots.length, 5, "c and d (same day) share a dot");
  const xs = lay.dots.map((d) => d.x);
  assert.deepEqual(xs, [...xs].sort((a, b) => a - b));
  assert.deepEqual(lay.dots.map((d) => d.items.map((m) => m.id).join("")), ["a", "e", "b", "cd", "f"], "ordered by when it happened");
  assert.ok(xs.slice(1).every((x, i) => x - xs[i] >= 84), "neighbours never crowd");
  assert.deepEqual(lay.dots.map((d) => d.side), ["above", "below", "above", "below", "above"], "alternating, the newest above");
  const size = Object.fromEntries(lay.dots.map((d) => [d.items[0].id, d.size]));
  assert.equal(size.e, 20); assert.equal(size.a, 8); assert.ok(size.f > size.b && size.b > 8, "used more = bigger, clamped to 8–20");
  assert.deepEqual([lay.dots[0].hollow, lay.dots[3].review, lay.dots[3].hollow], [true, true, false]);
  assert.ok(lay.width > xs.at(-1));
  assert.equal(timelineLayout(MEMS, { zoom: "month" }).dots.length, 4, "zoomed out to months: Jul · Aug · Sep · Oct");
});

test("the timeline strip starts at the newest, groups open a popover, dots pick the detail and walk by arrow keys", async () => {
  const env = setup(), { draw } = await page(env);
  env.deps.memory.memView = "timeline"; env.snap("memories").cursor = "next";
  draw();
  const strip = env.document.querySelector("#table .mem-tl");
  assert.equal(strip.scrollLeft, strip.layout.width, "scrolled to the right end (the browser clamps it)");
  const dots = env.document.querySelectorAll("#table .mem-tl-item");
  assert.equal(dots.length, 5);
  assert.deepEqual(dots.map((d) => d.tabIndex), [-1, -1, -1, -1, 0], "one tab stop: the newest");
  assert.match(dots[4].getAttribute("aria-label"), /가장 최근 f/);
  assert.ok(dots[0].className.includes("hollow") && dots[3].className.includes("group") && dots[3].className.includes("review"));
  assert.equal(dots[0].querySelector(".mem-tl-title").textContent.length, 18, "labels are cut to ~18 characters");
  // arrows
  dots[4].focus(); dom.fire(dots[4], "keydown", { key: "ArrowLeft" });
  assert.equal(env.document.activeElement, dots[3]); assert.equal(dots[3].tabIndex, 0); assert.equal(dots[4].tabIndex, -1);
  dom.fire(dots[3], "keydown", { key: "Home" }); assert.equal(env.document.activeElement, dots[0]);
  // a single dot opens the detail
  dots[4].click();
  assert.equal(env.state.selected, "f");
  assert.deepEqual(env.log.slice(-4), ["pin:f", "mark", "detail:f", "open"]);
  // a grouped day lists its memories, and one of them opens the detail
  dots[3].click();
  const pop = env.document.querySelector("#table .mem-tl-pop");
  assert.equal(pop.querySelectorAll(".mem-tl-pop-row").length, 2);
  assert.equal(env.document.activeElement, pop.querySelector(".mem-tl-pop-row"));
  dom.fire(pop, "keydown", { key: "Escape" });
  assert.equal(env.document.querySelector("#table .mem-tl-pop"), null); assert.equal(env.document.activeElement, dots[3]);
  dots[3].click(); env.document.querySelectorAll("#table .mem-tl-pop-row")[1].click();
  assert.equal(env.state.selected, "d");
  // older pages load from the left end, and the view stays where it was
  strip.scrollLeft = 100; dom.fire(strip, "scroll");
  env.document.querySelector("#table .mem-tl-more").click();
  assert.equal(env.log.at(-1), "more:memories");
  draw();
  const again = env.document.querySelector("#table .mem-tl");
  assert.equal(again.scrollLeft, 100, "a redraw keeps the scroll position");
  assert.ok(again.querySelector(".mem-tl-item.selected[data-id=d]") === null && again.querySelector(".mem-tl-item.group.selected"), "the group holding the pick is marked");
  env.snap("memories").cursor = ""; draw();
  assert.equal(env.document.querySelector("#table .mem-tl-more"), null, "every page loaded: no 더 이전 불러오기");
});

test("the graph view reuses the memory map, and a picked node opens the detail panel", async () => {
  const env = setup(), { made, real } = fakeBubbles(env);
  try {
    const { draw } = await page(env);
    env.deps.memory.memView = "graph"; draw();
    assert.equal(made.length, 1); assert.ok(env.document.querySelector("#table .mem-graph .mm-bubbles"));
    assert.deepEqual(made[0].items.map((x) => x.id), MEMS.map((m) => m.id));
    assert.equal(made[0].opts.groupOf("kind", { kind: "user" }).color, real.KIND_COLOR.user, "kind colours are the map's own");
    await dom.flush();
    assert.match(env.calls[0], /^\/v1\/memory-graph\?mode=all/);
    assert.equal(made[0].destroyed, 1, "the lines arrived: the map is drawn again with them");
    draw();
    assert.deepEqual(made[1].edges, [{ from: "c", to: "d", t: "supports" }], "only lines between shown memories");
    draw(); assert.equal(made.length, 2, "a redraw with the same memories keeps the map");
    made[1].opts.onSelect("e");
    assert.equal(env.state.selected, "e");
    assert.deepEqual(env.log.slice(-4), ["pin:e", "mark", "detail:e", "open"]);
    assert.equal(made[1].sel.at(-1), "e");
    env.deps.memory.memView = "list"; draw();
    assert.equal(made[1].destroyed, 1, "leaving the graph stops the map");
    assert.equal(env.calls.length, 1, "the lines are read once per connection");
  } finally { window.ArkismMemoryBubbles = real; }
});
