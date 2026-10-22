// The whole app (entry.mjs) boots on the built-in Arkism (the preview server) and walks every menu and tab.
// Run: node --test desktop/ui/*.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const { boot } = require("./testdata/app-harness.js");

let app;
test.before(async () => { app = await boot(); });
test.after(() => app?.stop());

test("boots unlocked on the built-in Arkism and fills home", async () => {
  assert.equal(app.$("#app").inert, false);
  assert.ok(app.calls.some(([c]) => c === "local_start"));
  assert.ok(app.apiCalls.some(([, p]) => p.startsWith("/v1/capabilities")));
});

test("every menu and area tab draws", async () => {
  const errors = [];
  process.on("unhandledRejection", (e) => errors.push(e));
  for (const nav of app.$$(".nav-item[data-menu]")) {
    await app.click(nav, 50);
    const tabs = app.$$("#area-tabs .area-tab");
    for (let i = 0; i < tabs.length; i++) {
      const t = app.$$("#area-tabs .area-tab")[i];
      if (t) await app.click(t, 50);
    }
  }
  assert.deepEqual(errors, []);
});
