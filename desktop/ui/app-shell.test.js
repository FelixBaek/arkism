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
  const errors = [], shown = [];
  process.on("unhandledRejection", (e) => errors.push(e));
  for (const nav of app.$$(".nav-item[data-menu]")) {
    await app.click(nav, 50);
    const tabs = app.$$("#area-tabs .area-tab");
    for (let i = 0; i < tabs.length; i++) {
      const t = app.$$("#area-tabs .area-tab")[i];
      if (t) await app.click(t, 50);
      // the tab names the screen: no repeated title or "15개" line under it, and a count only on the open tab
      const name = app.$("#area-tabs").dataset.active;
      assert.ok(!app.$$("#area-tabs .area-tab:not(.active) output").length, `${name}: a closed tab shows a count`);
      const n = app.$("#area-tabs .area-tab.active output")?.textContent;
      assert.notEqual(n, "0", `${name}: a zero count on the tab`);
      if (n) shown.push(name);
      if (app.$("#title").textContent === name) assert.equal(app.$("#title-count").textContent, "", `${name}: count line under the tabs`);
    }
  }
  assert.deepEqual(errors, []);
  assert.ok(shown.length >= 3, `non-zero counts still show: ${shown}`);
});

test("ROUTER has no 라우팅 설정 tab: the policy card opens it and 라우팅 현황 stays the open tab", async () => {
  const text = (n) => app.dom.text(n);
  const tabs = () => app.$$("#area-tabs .area-tab").map((t) => text(t.querySelector("span")));
  await app.click('.nav-item[data-menu="router"]', 100);
  assert.deepEqual(tabs(), ["라우팅 현황", "사용량"]);
  const open = app.$$("#detail button").find((b) => text(b) === "라우팅 설정");
  assert.ok(open, "the policy card links to the routing settings");
  await app.click(open, 100);
  assert.equal(app.$(".nav-item.active").dataset.menu, "router");
  assert.deepEqual(tabs(), ["라우팅 현황", "사용량"]);
  assert.equal(app.$("#area-tabs").dataset.active, "라우팅 현황");
  assert.ok(app.$("#table .v4-set-routing"), "the settings page is drawn");
  await app.click(app.$$("#area-tabs .area-tab")[0], 100);
  assert.ok(!app.$("#table .v4-set-routing"), "the tab goes back to 라우팅 현황");
});
