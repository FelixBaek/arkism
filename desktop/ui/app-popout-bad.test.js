// The app (entry.mjs) as a popup window whose payload is unreadable (e.g. the window was reloaded): it closes at once.
// Run: node --test desktop/ui/*.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const { boot } = require("./testdata/app-harness.js");

let app;
test.before(async () => { app = await boot({ popout: "{not json" }); await app.settle(500); });
test.after(() => app?.stop());

test("an unreadable popup payload closes the window without connecting", () => {
  assert.equal(app.calls.filter(([c]) => c === "popout_close").length, 1);
  assert.ok(!app.apiCalls.some(([, p]) => p.startsWith("/v1/capabilities")));
  assert.equal(app.document.documentElement.classList.contains("popout"), false);
});
