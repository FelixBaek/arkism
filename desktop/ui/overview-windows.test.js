// Run with: node --test desktop/ui/*.test.js
// MEMORY › 개요 stage windows on a small DOM stand-in.
const test = require("node:test");
const assert = require("node:assert/strict");
const dom = require("./testdata/fake-dom.cjs");
const { document } = dom.install();
const h = dom.helpers(document);
require("./overview-windows.js");

function windows(memories, memoryUse, read) {
  const state = { data: { memories, candidates: [], tools: { memory_use: memoryUse } }, scope: {}, connection: { generation: 1 } };
  const calls = [];
  const request = async (path) => { calls.push(path); return read(path); };
  const ctx = { state, ...h, query: (o = {}) => h.query(o), request, ago: () => "방금", go() {}, render() {} };
  const env = { stages: () => [{ key: "trace", label: "사용 기록", tiles: [] }], sessions: () => [] };
  return { win: window.ArkismOverviewWindows(ctx, env), calls, state };
}
const titles = () => document.querySelectorAll(".ov-win-item-title").map((n) => n.textContent);

test("사용 기록 names a used memory that is outside the loaded first page (A3)", async () => {
  const { win, calls } = windows([{ id: "loaded", text: "Loaded rule" }], { days: 7, items: [{ id: "older", used: 9, injected: 9 }, { id: "loaded", used: 1, injected: 2 }, { id: "gone", used: 1, injected: 1 }] },
    (path) => { if (path.startsWith("/v1/memories/older?")) return { id: "older", text: "Older rule", status: "active" }; throw Error("not_found"); });
  win.open("trace");
  assert.deepEqual(titles(), ["불러오는 중…", "Loaded rule", "불러오는 중…"]);
  await dom.flush(); await dom.flush();
  assert.deepEqual(titles(), ["Older rule", "Loaded rule", "(지워졌거나 아직 못 읽은 기억)"]);
  win.open("trace"); // cached: no second read
  assert.equal(calls.length, 2);
  win.reset();
});
