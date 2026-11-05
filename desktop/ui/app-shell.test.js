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

test("MEMORY and one-page menus draw no tab line; 한눈에 chips lead to 확인해 주세요", async () => {
  for (const menu of ["home", "memory", "agent"]) {
    await app.click(app.$(`.nav-item[data-menu="${menu}"]`), 50);
    assert.equal(app.$$("#area-tabs .area-tab").length, 0, `${menu}: no tab line`);
    assert.equal(app.$("#area-tabs").parentElement.hidden, true, `${menu}: the empty tab line is hidden`);
  }
  await app.click(app.$('.nav-item[data-menu="memory"]'), 50);
  assert.ok(app.$("#table .ov-chips"), "MEMORY opens on 한눈에");
  const waiting = app.$('.nav-item[data-menu="memory"] .nav-count');
  assert.ok(waiting && !waiting.hidden && /^[1-9]\d*\+?$/.test(app.dom.text(waiting)), "the rail shows what waits beside Memory");
  // the memory list: no 확인해 주세요 banner; the side panel opens with 확인할 것 and one way in
  await app.click(app.$$("#table .ov-chip").find((c) => /확인해 주세요/.test(app.dom.text(c))), 50);
  await app.click(app.$("#mem-back"), 50); // ← 기억: the main list, nothing selected
  assert.equal(app.$("#controls .mem-check"), null, "no banner over the list");
  const todo = app.$$("section.section").find((x) => app.dom.text(x.querySelector("h3")) === "확인할 것");
  assert.ok(todo && /새 기억 확인/.test(app.dom.text(todo)), "확인할 것 lists what waits");
  await app.click([...todo.querySelectorAll("button")].find((b) => app.dom.text(b) === "하나씩 확인하기"), 50);
  assert.equal(app.$(".nav-item.active").dataset.menu, "memory");
  await app.click(app.$('.nav-item[data-menu="memory"]'), 50);
  const chip = app.$$("#table .ov-chip").find((c) => /확인해 주세요/.test(app.dom.text(c)));
  assert.ok(chip, "the 확인해 주세요 chip");
  await app.click(chip, 50);
  assert.ok(!app.$("#table .ov-chips"), "the chip left 한눈에 for the review list");
  assert.equal(app.$(".nav-item.active").dataset.menu, "memory");
});

test("TOOLS › 관리 lists skills by use, and the skills page shows the routing flow", async () => {
  await app.click(app.$('.nav-item[data-menu="connect"]'), 50);
  assert.equal(app.$$("#controls .v4s-flow-steps li").length, 5, "스킬 처리 흐름: five steps");
  await app.click(app.$$("#area-tabs .area-tab").find((t) => t.dataset.tab === "manage"), 50);
  const boxes = app.$$("#table .mg-box").map((b) => app.dom.text(b.querySelector("h3")));
  assert.deepEqual(boxes, ["자주 씀", "거의 안 씀", "업데이트 필요", "업데이트·설치 실패", "최근 추가"]);
  const often = app.$$("#table .mg-box")[0].querySelectorAll(".mg-side");
  const n = [...often].map((x) => parseInt(app.dom.text(x), 10));
  assert.ok(n.length && n.every((v, i) => !i || n[i - 1] >= v), "most used first");
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

test("the rail folds to icons, says where you are, and stays folded after a reload of the setting", async () => {
  const root = app.$("#app").ownerDocument.documentElement, fold = app.$("#rail-toggle");
  await app.click(app.$('.nav-item[data-menu="router"]'), 50);
  assert.equal(root.classList.contains("rail-collapsed"), false);
  assert.equal(fold.getAttribute("aria-label"), "메뉴 접기");
  await app.click(fold, 20);
  assert.ok(root.classList.contains("rail-collapsed"));
  assert.equal(fold.getAttribute("aria-expanded"), "false");
  assert.equal(fold.getAttribute("aria-label"), "메뉴 펼치기");
  assert.equal(app.$("#location-bar").textContent, "Router›라우팅 현황");
  await app.click(app.$('.nav-item[data-menu="agent"]'), 50);
  assert.equal(app.$("#location-bar").textContent, "Flow", "a one-page menu names only the menu");
  assert.equal(app.$("#app").ownerDocument.defaultView.localStorage.getItem("arkism.rail.folded"), "1");
  await app.click(fold, 20);
  assert.equal(root.classList.contains("rail-collapsed"), false);
});

test("a 모아보기 page keeps ← 기억 but no title repeating its open tab", async () => {
  await app.click(app.$('.nav-item[data-menu="memory"]'), 50);
  await app.click(app.$$("#table .ov-chip").find((c) => /확인해 주세요/.test(app.dom.text(c))), 50);
  await app.click(app.$("#mem-back"), 50); // ← 기억: the main list with its 모아보기 chips
  const past = app.$$("#chips .mem-look button.cat-menu-item").find((c) => app.dom.text(c) === "과거 시점");
  assert.ok(past, "the 과거 시점 item in 모아보기");
  await app.click(past, 50);
  assert.equal(app.dom.text(app.$("#controls .mem-seg .tab.active")), "과거 시점");
  assert.equal(app.$("#title").textContent, "과거 시점", "screen readers keep the heading");
  assert.ok(app.$("#title").classList.contains("sr-only"), "the title does not repeat the open tab");
  assert.equal(app.$("#mem-back").hidden, false);
});
