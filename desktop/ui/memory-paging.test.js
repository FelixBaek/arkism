// Memory cursors: search fallback for older servers, counts, and filtered result loading.
const test = require("node:test");
const assert = require("node:assert/strict");

const pagers = [];
globalThis.window = {ArkismMemoryPages: {create: read => {
  const pager = {read, snap: {}, loads: [],
    snapshot() { return this.snap; },
    async load(key) { this.loads.push(key); if (this.fail) throw new Error(this.fail); this.snap = {...this.snap, key}; return this.items || []; },
    async retry(key) { this.retried = key; this.snap = {key}; return this.items || []; }};
  pagers.push(pager);
  return pager;
}}};

async function setup(state = {}, request = async () => ({items: []})) {
  pagers.length = 0;
  const routes = [];
  let renders = 0;
  const deps = {
    state: {server: "s", scope: {workspace_id: "w"}, query: "", kind: "all", memTab: "active", view: "memories", data: {memories: []}, ...state},
    generation: 1, $: () => ({value: ` ${deps.state.query} `}), render: () => renders++, query: () => "",
    request: async route => { routes.push(route); return request(route); }
  };
  const { createMemoryPaging } = await import("./modules/memoryPaging.mjs");
  const paging = createMemoryPaging(deps);
  return {deps, paging, routes, renders: () => renders, pages: pagers[0], results: pagers[1]};
}

test("search asks for case-insensitive matches and falls back once for older servers", async () => {
  const h = await setup({query: "Rust", kind: "lesson"}, route => {
    if (route.includes("ignore_case")) throw new Error("invalid_request");
    return {items: ["row"]};
  });
  const key = h.paging.memoryKey(true);
  assert.deepEqual(JSON.parse(key), {server: "s", scope: {workspace_id: "w"}, generation: 1, q: "Rust", kind: "lesson"});
  assert.deepEqual(await h.pages.read(key, {limit: 20, cursor: "c2"}), {items: ["row"]});
  assert.equal(h.routes.length, 2);
  const retried = new URLSearchParams(h.routes[1].split("?")[1]);
  assert.deepEqual(Object.fromEntries(retried), {workspace_id: "w", status: "active", q: "Rust", kind: "lesson", limit: "20", cursor: "c2"});
  assert.ok(h.paging.legacyMemorySearch.has("s"));
  await h.pages.read(key, {limit: 20});
  assert.equal(h.routes.length, 3);
  assert.ok(!h.routes[2].includes("ignore_case"));
});

test("other read failures are not retried", async () => {
  const h = await setup({query: "x"}, () => { throw new Error("offline"); });
  await assert.rejects(h.pages.read(h.paging.memoryKey(true), {limit: 5}), /offline/);
  await assert.rejects(h.pages.read(h.paging.memoryKey(false), {limit: 5}), /offline/);
  assert.equal(h.routes.length, 2);
  assert.ok(!h.routes[1].includes("q="));
});

test("the count prefers the server total and marks a partial load", async () => {
  const h = await setup({data: {memories: [1, 2]}});
  assert.equal(h.paging.memoryCount(), "2");
  h.pages.snap = {cursor: "next"};
  assert.equal(h.paging.partialMemories(), true);
  assert.equal(h.paging.memoryCount(), "2+");
  h.deps.state.memoryTotals = {active: 40};
  assert.equal(h.paging.memoryCount(), "40");
  h.paging.lastMemoryUsage = 9;
  assert.equal(h.paging.lastMemoryUsage, 9);
});

test("filtered results load once per key and redraw only while still current", async () => {
  const h = await setup({query: "go"});
  assert.equal(h.paging.filteredMemories(), true);
  assert.equal(h.paging.currentMemoryPages(), h.results);
  h.paging.ensureMemoryResults();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(h.results.loads.length, 1);
  assert.equal(h.renders(), 1);
  h.paging.ensureMemoryResults();
  assert.equal(h.results.loads.length, 1);
  h.deps.state.query = "rust"; h.results.fail = "offline";
  h.paging.ensureMemoryResults();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(h.renders(), 2);
  h.deps.state.query = "";
  h.paging.ensureMemoryResults();
  assert.equal(h.results.loads.length, 2);
  assert.equal(h.paging.currentMemoryPages(), h.pages);
});

test("fetching more keeps the old page on failure and retries after an error", async () => {
  const h = await setup();
  h.pages.items = ["a", "b"];
  await h.paging.fetchMoreMemories();
  assert.deepEqual(h.deps.state.data.memories, ["a", "b"]);
  h.pages.fail = "offline";
  await h.paging.fetchMoreMemories();
  assert.deepEqual(h.deps.state.data.memories, ["a", "b"]);
  h.pages.snap = {error: "offline"}; h.pages.items = ["c"];
  await h.paging.fetchMoreMemories();
  assert.equal(h.pages.retried, h.paging.memoryKey(false));
  assert.deepEqual(h.deps.state.data.memories, ["c"]);
  h.deps.state.query = "x"; h.results.items = ["filtered"];
  await h.paging.fetchMoreMemories();
  assert.deepEqual(h.deps.state.data.memories, ["c"]);
});

test("memory tidy refs without an id are skipped, not fetched or cached", async () => {
  const h = await setup({data: {memories: [], maintenance: [{maintenance: {keep: {}, retire: {id: "r", revision: 1}}}]}},
    () => ({revision: 1}));
  await h.paging.hydrateMaintenance(1);
  assert.deepEqual(h.routes.map(r => r.split("?")[0]), ["/v1/memories/r"]);
  assert.equal(h.paging.maintenanceRecords.has(undefined), false);
});
