// Run with: node --test desktop/ui/*.test.js
// Behavior of the shared list modules: the collection footer, the visible-page sync, the virtual list and catalog ensure.
const test = require("node:test");
const assert = require("node:assert/strict");
const dom = require("./testdata/fake-dom.cjs");
const { create: createPager } = require("./memory-pages.js");

const { document } = dom.install();
// The fake DOM has no layout; rows count as rendered so keyboard targets are focusable.
dom.Element.prototype.getClientRects = () => [{}];
// Content inside a <main> scrollport moves up as it scrolls, like a browser box.
dom.Element.prototype.getBoundingClientRect = function () { const port = this.parentNode && this.closest("main"); return { top: port && port !== this ? -port.scrollTop : 0, height: 0 }; };
const observers = [];
globalThis.ResizeObserver = class {
  constructor(fn) { this.fn = fn; this.seen = new Set(); this.off = false; observers.push(this); }
  observe(n) { this.seen.add(n); }
  unobserve(n) { this.seen.delete(n); }
  disconnect() { this.off = true; this.seen.clear(); }
};

test("collection footer hides when everything is shown and reveals, fetches and refocuses otherwise", async () => {
  const { collectionFooter } = await import("./modules/collectionPaging.mjs");
  const base = { document, name: "skills", total: 3, shown: 3, render() {}, isCurrent: () => true };
  assert.equal(collectionFooter({ ...base, snapshot: () => ({ cursor: "", error: "", loading: false }) }), null);

  let revealed = 0, renders = 0;
  const local = collectionFooter({ ...base, shown: 1, snapshot: () => ({}), reveal: () => revealed++, render: () => renders++ });
  assert.equal(local.dataset.collection, "skills");
  assert.match(local.textContent, /3개 중 1개 표시 · 더 보기/);
  document.body.replaceChildren(local);
  local.querySelector("button").focus();
  local.querySelector("button").click();
  assert.equal(revealed, 1); assert.equal(renders, 1);
  assert.equal(document.activeElement, local.querySelector("button")); // the same footer is still drawn, so focus stays on it

  const loading = collectionFooter({ ...base, snapshot: () => ({ loading: true }) });
  assert.equal(loading.querySelector("button").textContent, "불러오는 중…");
  assert.equal(loading.querySelector("button").disabled, true);

  // a remote page: the redraw drops the footer, so focus moves to the search box
  let fetched = 0;
  const search = document.createElement("input"); search.id = "search";
  const remote = collectionFooter({ ...base, snapshot: () => ({ cursor: "next" }), fetch: async () => { fetched++; throw Error("offline"); },
    render: () => { renders++; document.body.replaceChildren(search); document.activeElement = document.body; } });
  assert.match(remote.textContent, /불러온 3개 중 3개 표시 · 다음 목록 있음/);
  document.body.replaceChildren(remote);
  remote.querySelector("button").focus();
  remote.querySelector("button").click();
  await dom.flush();
  assert.equal(fetched, 1); assert.equal(renders, 3);
  assert.equal(document.activeElement, search);

  // a reply after navigation does not redraw or move focus
  let late = 0;
  const retry = collectionFooter({ ...base, snapshot: () => ({ error: "x" }), fetch: async () => {}, render: () => late++, isCurrent: () => false });
  assert.equal(retry.querySelector("button").textContent, "다시 불러오기");
  document.body.replaceChildren(retry);
  retry.querySelector("button").focus();
  retry.querySelector("button").click();
  await dom.flush();
  assert.equal(late, 1);
  assert.equal(document.activeElement, retry.querySelector("button"));
});

function syncFixture(over = {}) {
  const calls = [];
  const state = { token: "t", loading: false, pending: new Map(), view: "memories", memTab: "active", connection: { generation: 1 }, caps: { actions: { review_list: true } },
    data: { memories: [{ id: "m" }], candidates: [], conflicted: [], maintenance: [], skills: [], skillTrash: [], skillUse: new Map(), tools: {}, crew: [], providers: [] }, versions: new Map(), ...over.state };
  const memoryPaging = {
    lastMemoryUsage: 0, memoryKey: (filtered) => (filtered ? "filtered" : "all"), filteredMemories: () => true,
    memoryPages: { snapshot: () => ({ loading: false }), refresh: async (key) => { calls.push(["memories", key]); return [{ id: "m" }, { id: "n" }]; } },
    memoryResults: { snapshot: () => ({ loading: false }), refresh: async (key) => { calls.push(["results", key]); } },
    hydrateMaintenance: (g) => calls.push(["hydrate", g]), ...over.memoryPaging
  };
  const deps = {
    state, memoryPaging,
    collectionPaging: { read: async (key, refresh) => { calls.push(["read", key, refresh]); return [{ id: key }]; } },
    request: async (path) => { calls.push(["request", path]); return path.startsWith("/v1/providers") ? { providers: [{ id: "p" }] } : { items: [{ id: path, skill_id: "s" }] }; },
    query: (o) => new URLSearchParams(o || {}).toString(),
    loadTools: async () => ({ fresh: true }), loadUsage: (g) => calls.push(["usage", g]),
    render: (reset) => calls.push(["render", reset]), invalidateVersions: () => calls.push(["invalidate"]),
    routing: { refresh: async () => calls.push(["routing"]) }, panels: () => ({}), digests: { refresh: () => calls.push(["digests"]) },
    ...over.deps
  };
  return { state, deps, calls };
}

test("visible sync refreshes opened memory pages, review lists and filtered results, then publishes", async () => {
  const { createVisibleSync } = await import("./modules/visibleSync.mjs");
  document.body.replaceChildren(); document.activeElement = document.body;
  const { state, deps, calls } = syncFixture();
  await createVisibleSync(deps)();
  assert.deepEqual(calls.filter(([k]) => k === "usage"), [["usage", 1]]); // usage older than ten minutes reloads
  assert.ok(calls.some(([k, v]) => k === "memories" && v === "all"));
  assert.ok(calls.some(([k, v]) => k === "request" && v === "/v1/maintenance_proposals?status=open"));
  assert.deepEqual(state.data.memories.map((m) => m.id), ["m", "n"]);
  assert.deepEqual(state.data.candidates, [{ id: "candidates" }]);
  assert.ok(calls.some(([k, v]) => k === "hydrate" && v === 1));
  assert.ok(calls.some(([k, v]) => k === "results" && v === "filtered"));
  assert.deepEqual(calls.filter(([k]) => k === "render"), [["render", undefined], ["render", false]]);
});

test("visible sync keeps an in-flight memory page and hands visible catalog pages to catalogQuery", async () => {
  const { createVisibleSync } = await import("./modules/visibleSync.mjs");
  let refreshed = 0;
  const { state, deps, calls } = syncFixture({
    state: { caps: {} },
    memoryPaging: { lastMemoryUsage: Date.now(), memoryPages: { snapshot: () => ({ loading: true }), refresh: () => assert.fail("loading page is not refreshed") } },
    deps: { catalogQuery: { visible: () => true, refreshVisible: async () => refreshed++ } }
  });
  const before = state.data.memories;
  await createVisibleSync(deps)();
  assert.equal(state.data.memories, before);
  assert.equal(refreshed, 1);
  assert.equal(calls.some(([k]) => k === "usage" || k === "results" || k === "read"), false);
});

test("visible sync reads skills, tools, settings and simple lists for their own pages", async () => {
  const { createVisibleSync } = await import("./modules/visibleSync.mjs");
  for (const [view, extra, key, want] of [
    ["skills", {}, "skillUse", (v) => v instanceof Map && v.get("s")],
    ["tools", {}, "tools", (v) => v.fresh],
    ["settings", { settingsTab: "models" }, "providers", (v) => v[0].id === "p"],
    ["crew", {}, "crew", (v) => v[0].id === "/v1/crew?"],
    ["images", {}, "prompts", (v) => v[0].id === "/v1/prompts?"],
    ["design", {}, "designSystems", (v) => v[0].id === "/v1/design_systems?"]
  ]) {
    const { state, deps } = syncFixture({ state: { view, ...extra } });
    await createVisibleSync(deps)();
    assert.ok(want(state.data[key]), view);
  }
});

test("visible sync runs separate refreshes for routing and memory panels", async () => {
  const { createVisibleSync } = await import("./modules/visibleSync.mjs");
  let panel = 0;
  const routing = syncFixture({ state: { view: "routing" } });
  await createVisibleSync(routing.deps)();
  assert.deepEqual(routing.calls, [["routing"]]);
  const panels = syncFixture({ state: { memTab: "traits" }, deps: { panels: () => ({ traits: { refresh: async () => panel++ } }) } });
  await createVisibleSync(panels.deps)();
  assert.equal(panel, 1); assert.equal(panels.calls.length, 0);
  const digests = syncFixture({ state: { memTab: "digests" }, memoryPaging: { filteredMemories: () => false } });
  await createVisibleSync(digests.deps)();
  assert.equal(digests.calls[0][0], "digests");
  assert.ok(digests.calls.some(([k]) => k === "memories")); // the regular plan still runs
  assert.equal(digests.calls.some(([k]) => k === "results"), false);
});

test("visible sync stays idle when blocked or ineligible and queues one rerun while busy", async () => {
  const { createVisibleSync } = await import("./modules/visibleSync.mjs");
  for (const over of [{ token: "" }, { view: "home" }, { memTab: "past" }, { view: "settings", settingsTab: "general" }]) {
    const { deps, calls } = syncFixture({ state: over });
    await createVisibleSync(deps)();
    assert.deepEqual(calls, [], JSON.stringify(over));
  }
  const field = document.createElement("textarea"); document.body.replaceChildren(field); field.focus();
  const typing = syncFixture();
  const sync = createVisibleSync(typing.deps);
  assert.equal(sync.blocked(), true);
  await sync(); assert.deepEqual(typing.calls, []);
  field.id = "search"; assert.equal(sync.blocked(), false);
  const dialog = document.createElement("dialog"); dialog.setAttribute("open", ""); document.body.append(dialog);
  assert.equal(sync.blocked(), true);
  document.body.replaceChildren(); document.activeElement = document.body;

  let release, runs = 0;
  const busy = syncFixture({ state: { view: "routing" }, deps: { routing: { refresh: () => { runs++; return new Promise((r) => { release = r; }); } } } });
  const once = createVisibleSync(busy.deps);
  const first = once("timer");
  await once("timer"); // a second call while syncing only marks a rerun
  assert.equal(runs, 1);
  release(); await first; await dom.flush();
  assert.equal(runs, 2);
  release(); await dom.flush();

  const broken = syncFixture({ state: { view: "crew" }, deps: { request: () => { throw Error("offline"); } } });
  await createVisibleSync(broken.deps)(); // a thrown read keeps current rows
  assert.deepEqual(broken.state.data.crew, []);
  const late = syncFixture({ state: { view: "crew" } });
  const pending = createVisibleSync(late.deps)();
  late.state.connection.generation = 2;
  await pending;
  assert.deepEqual(late.state.data.crew, []); // a reply for an old connection is dropped
});

test("filtered result refresh skips a busy result pager and a changed filter", async () => {
  const { createVisibleSync } = await import("./modules/visibleSync.mjs");
  const busy = syncFixture({ memoryPaging: { memoryResults: { snapshot: () => ({ loading: true }), refresh: () => assert.fail("busy") } } });
  await createVisibleSync(busy.deps)();
  let key = "a";
  const moved = syncFixture({ memoryPaging: { memoryKey: () => key, memoryResults: { snapshot: () => ({}), refresh: async () => { key = "b"; throw Error("x"); } } } });
  await createVisibleSync(moved.deps)();
  assert.deepEqual(moved.calls.filter(([k, v]) => k === "render" && v === false), []);
});

const rows = (n) => Array.from({ length: n }, (_, i) => ({ id: `r${i}`, name: `row ${i}` }));
function row(item) {
  const b = document.createElement("div"); b.className = "row";
  const first = document.createElement("button"); first.textContent = item.name;
  const second = document.createElement("button"); second.textContent = "more";
  b.append(first, second);
  return b;
}

test("short lists render every row without a virtual host", async () => {
  const { createVirtualList } = await import("./modules/virtualList.mjs");
  const container = document.createElement("div");
  createVirtualList().mount({ container, items: rows(3), row });
  assert.equal(container.children.length, 3);
  assert.equal(container.querySelector(".virtual-list"), null);
});

test("long lists draw a window of rows with spacers, measure heights and clear their observers", async () => {
  const { createVirtualList } = await import("./modules/virtualList.mjs");
  const list = createVirtualList();
  const scrollport = document.createElement("main"); scrollport.clientHeight = 760; scrollport.clientWidth = 400; scrollport.clientTop = 0;
  const container = document.createElement("section"); scrollport.append(container); document.body.replaceChildren(scrollport);
  observers.length = 0;
  list.mount({ container, scrollport, items: rows(400), row, key: "skills", threshold: 10 });
  const host = container.querySelector(".virtual-list");
  assert.equal(host.getAttribute("role"), "list");
  const drawn = () => host.querySelectorAll(".virtual-item").map((n) => Number(n.dataset.virtualIndex));
  assert.deepEqual(drawn(), Array.from({ length: 18 }, (_, i) => i)); // 760px / 76px estimate → 10 rows + 8 overscan
  const items = host.querySelectorAll(".virtual-item");
  assert.equal(items[0].getAttribute("aria-posinset"), "1");
  assert.equal(items[0].getAttribute("aria-setsize"), "400");
  const spacers = host.querySelectorAll(".virtual-spacer");
  assert.equal(spacers.at(-1).style.height, `${(400 - 18) * 76}px`);

  // scrolling far moves the window and keeps the leading spacer equal to the skipped rows
  scrollport.scrollTop = 76 * 200;
  scrollport.dispatchEvent(new dom.Event("scroll"));
  await dom.flush();
  assert.equal(drawn()[0], 194);
  assert.equal(host.querySelector(".virtual-spacer").style.height, `${194 * 76}px`);

  // measured rows above the anchor shift the scroll position by their height change
  const [rowObserver, viewport] = observers;
  const above = host.querySelector(".virtual-item");
  rowObserver.fn([{ target: above, borderBoxSize: [{ blockSize: 100 }] }]);
  assert.equal(scrollport.scrollTop, 76 * 200 + 24);
  viewport.fn([]);
  await dom.flush();

  // End jumps to the last row, which carries the last marker
  const focusedRow = host.querySelectorAll(".virtual-item")[5];
  focusedRow.querySelector("button").focus();
  const end = new dom.Event("keydown", { key: "End", bubbles: true, cancelable: true });
  focusedRow.querySelector("button").dispatchEvent(end);
  assert.equal(end.defaultPrevented, true);
  const last = document.activeElement.closest(".virtual-item");
  assert.equal(last.dataset.virtualIndex, "399");
  assert.ok(last.hasAttribute("data-virtual-last"));

  // the focused row stays drawn after scrolling back to the top
  scrollport.scrollTop = 0; scrollport.dispatchEvent(new dom.Event("scroll"));
  await dom.flush();
  assert.ok(drawn().includes(399) && drawn().includes(0));

  // Tab on the last control moves to the next row; earlier controls and modified keys stay native
  const firstRow = host.querySelector(".virtual-item");
  const [one, two] = firstRow.querySelectorAll("button");
  const tab = (target, init) => { const e = new dom.Event("keydown", { bubbles: true, cancelable: true, ...init }); target.dispatchEvent(e); return e; };
  assert.equal(tab(one, { key: "Tab" }).defaultPrevented, false);
  assert.equal(tab(two, { key: "Tab" }).defaultPrevented, true);
  assert.equal(document.activeElement.closest(".virtual-item").dataset.virtualIndex, "1");
  const back = tab(document.activeElement, { key: "Tab", shiftKey: true });
  assert.equal(back.defaultPrevented, true);
  assert.equal(document.activeElement, two); // backwards lands on the previous row's last control
  assert.equal(tab(two, { key: "ArrowDown", ctrlKey: true }).defaultPrevented, false);
  assert.equal(tab(two, { key: "x" }).defaultPrevented, false);
  assert.equal(tab(two, { key: "ArrowUp" }).defaultPrevented, true); // clamps at the first row
  assert.equal(document.activeElement.closest(".virtual-item").dataset.virtualIndex, "0");
  const field = document.createElement("input"); firstRow.firstChild.append(field);
  assert.equal(tab(field, { key: "ArrowDown" }).defaultPrevented, false);
  assert.equal(tab(host, { key: "ArrowDown" }).defaultPrevented, false); // outside any row

  // a remount with the same key restores the scroll position and reuses measured heights
  scrollport.scrollTop = 1234;
  list.clear();
  assert.ok(rowObserver.off && viewport.off);
  observers.length = 0;
  list.mount({ container, scrollport, items: rows(400), row, key: "skills", threshold: 10 });
  assert.equal(scrollport.scrollTop, 1234);
  const host2 = container.querySelector(".virtual-list");
  assert.ok(host2.querySelector('[data-virtual-index="16"]')); // scrolled window around 1234px

  // a detached host stops drawing
  container.remove();
  observers[1].fn([]);
  await dom.flush();
  list.clear();
});

test("catalog ensure opens the current page once and redraws after a failure", async () => {
  const { createCatalogQuery } = await import("./modules/catalogQuery.mjs");
  const state = { server: "s", scope: {}, connection: { generation: 1 }, view: "memories", memTab: "active", kind: "all", sort: "recent" };
  let calls = 0, fail = false, renders = 0;
  const catalog = createCatalogQuery({ state, createPager, preferences: () => ({ kept: [] }), render: () => renders++,
    request: async () => { calls++; if (fail) throw Error("offline"); return { items: [{ id: "a" }], total: 1, facets: {}, next_cursor: "", authority: "data_only_untrusted" }; } });
  catalog.ensure();
  catalog.ensure(); // already loading
  await dom.flush();
  assert.equal(calls, 1); assert.equal(catalog.snapshot("memories").ready, true);
  catalog.ensure(); assert.equal(calls, 1); // ready pages are not read again
  fail = true; renders = 0;
  catalog.ensure("candidates"); await dom.flush();
  assert.equal(calls, 2); assert.equal(renders, 1);
  assert.notEqual(catalog.snapshot("candidates").error, "");
  catalog.ensure("candidates"); assert.equal(calls, 2); // an error waits for an explicit retry
});
