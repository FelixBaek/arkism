// arkism-triangle.js: the idle rail art redraws a few times a second, not on every animation frame (#177).
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

test("one second of frames renders about twelve times", () => {
  let writes = 0, queued = null;
  const node = () => ({ setAttribute: () => { writes++; }, appendChild() {}, remove() {} });
  const win = { matchMedia: () => ({ matches: false, addEventListener() {} }) };
  const document = { hidden: false, createElementNS: node, addEventListener() {} };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, "arkism-triangle.js"), "utf8"), {
    window: win, document, Math, Array, performance: { now: () => 0 },
    requestAnimationFrame: (fn) => { queued = fn; return 1; }, cancelAnimationFrame() {},
  });
  win.mountArkismTriangle(node());
  writes = 0;
  for (let i = 1; i <= 60; i++) queued((i * 1000) / 60);
  const perRender = 28 * 2 + 1 + 3 * 2; // layer d+opacity, hole, two strokes per glint
  assert.ok(writes <= 13 * perRender, `svg writes in one second: ${writes}`);
  assert.ok(writes >= 10 * perRender, `still animates: ${writes}`);
});
