// A count or list that came from the server shows loaded, partial ("N+") and failed ("—") apart, never 0 or the first page (#465).
// One test per past regression, named after it; the booted app comes from testdata/app-harness.js (one boot for this file).
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { boot } = require("./testdata/app-harness.js");

test("serverCount tells loading, failed, partial and loaded apart, and a server total wins", async () => {
  const { serverCount } = await import("./modules/collectionPaging.mjs");
  assert.deepEqual(serverCount(undefined), { state: "loading", n: null, min: 0, text: "…" });
  assert.deepEqual(serverCount(null), { state: "failed", n: null, min: 0, text: "—" });
  assert.deepEqual(serverCount([1, 2], { more: true }), { state: "partial", n: null, min: 2, text: "2+" });
  assert.deepEqual(serverCount(3), { state: "loaded", n: 3, min: 3, text: "3" });
  assert.deepEqual(serverCount([1, 2], { more: true, total: 640 }), { state: "loaded", n: 640, min: 640, text: "640" });
  assert.equal(serverCount(null, { total: 0 }).text, "0", "a server total of 0 is a real 0");
  assert.equal(serverCount([], { total: null }).text, "0", "no total: the rows count");
});

test("#166: the memory count after a failed load is —, not 0", async () => {
  const { createMemoryPaging } = await import("./modules/memoryPaging.mjs");
  const real = globalThis.window.ArkismMemoryPages; // the booted app's window: borrow it for a stand-in pager
  globalThis.window.ArkismMemoryPages = { create: () => ({ snapshot: () => ({ cursor: "" }) }) };
  const state = { data: { memories: [] }, loadError: { code: "boom" }, memoryTotals: null };
  const paging = createMemoryPaging({ state, $: () => ({ value: "" }) });
  assert.equal(paging.memoryCount(), "—");
  state.loadError = "";
  assert.equal(paging.memoryCount(), "0");
  globalThis.window.ArkismMemoryPages = real;
});

test("#186: a list that failed at load and answers on the next refresh stops reading as failed", async () => {
  const { publishRefresh } = await import("./modules/visibleSync.mjs");
  const old = [];
  const state = { data: { conflicted: old }, listErrors: { conflicted: { code: "boom" } } };
  assert.equal(publishRefresh(state, [{ key: "conflicted", old }], [{ status: "fulfilled", value: [] }], () => {}), true);
  assert.deepEqual(state.listErrors, {});
  state.listErrors = { conflicted: { code: "boom" } };
  publishRefresh(state, [{ key: "conflicted", old }], [{ status: "rejected", reason: new Error("again") }], () => {});
  assert.ok(state.listErrors.conflicted, "a refresh that fails again keeps the error");
});

// Hand-rolled "N+" and first-page lengths are how #166, #188 and #438 came back; counts go through serverCount instead.
test("#465: no screen builds a partial count by hand or shows the candidate first page as a count", () => {
  const files = fs.readdirSync(__dirname).filter((f) => /\.(m?js)$/.test(f) && !/\.test\.js$|^i18n/.test(f)).map((f) => path.join(__dirname, f))
    .concat(fs.readdirSync(path.join(__dirname, "modules")).filter((f) => f.endsWith(".mjs") && f !== "collectionPaging.mjs").map((f) => path.join(__dirname, "modules", f)));
  const bad = [];
  for (const file of files) {
    fs.readFileSync(file, "utf8").split("\n").forEach((line, i) => {
      if (/\.cursor\s*\?\s*"\+"/.test(line) || /\$\{[^}]*\b(candidates|conflicted)\.length\b[^}]*\}/.test(line)) bad.push(`${path.basename(file)}:${i + 1}`);
    });
  }
  assert.deepEqual(bad, []);
});

// ---- the booted app
const routes = [];
const ok = (body) => ({ status: 200, body: JSON.stringify(body) });
const fail = (status, code) => ({ status, body: JSON.stringify({ code }) });
const route = (method, prefix, answer) => { const r = [method, prefix, answer]; routes.unshift(r); return () => routes.splice(routes.indexOf(r), 1); };
const NOW = Date.now(), ago = (h) => new Date(NOW - h * 3600000).toISOString();
let app;
test.before(async () => {
  app = await boot({ beforeEntry(env) { env.window.apiOverride = (a) => { for (const [m, p, fn] of routes) if (a.method === m && a.path.startsWith(p)) { const o = fn(a); if (o) return o; } return null; }; } });
});
test.after(() => app?.stop());
const text = (n) => app.dom.text(n);
const reload = async () => { app.dom.fire(app.window, "storage", { key: "arkism.popout-saved" }); await app.settle(200); };
const home = async () => { for (const d of app.$$("dialog[open]")) d.close(); app.$('.nav-item[data-menu="home"]').click(); await app.settle(100); };
const page = (items, next = "") => ok({ items, next_cursor: next, authority: "data_only_untrusted" });
// totals and use counts arrive after the lists: settle until the screen says so (or give up and let the assertion show it)
const until = async (pred) => { for (let i = 0; i < 60 && !pred(); i++) await app.settle(100); };
const has = (sel, re) => () => re.test(text(app.$(sel)));
// every booted test drops its routes and reloads, even when it fails, so the next one starts from the stock server
const withRoutes = (fn) => async () => { const offs = []; try { await fn((...r) => offs.push(route(...r))); } finally { offs.forEach((off) => off()); await reload(); } };

test("#188: Home's review count is the server total while more pages wait, and at least the page without it", withRoutes(async (add) => {
  const cand = { id: "cx1", text: "first page only", status: "candidate", kind: "user", created_at: ago(1) };
  let total = () => ok({ items: [], total: 123, next_cursor: "", authority: "data_only", facets: {} });
  add("GET", "/v1/memory_candidates?", (a) => (/status=candidate/.test(a.path) ? page([cand], "more") : null));
  add("POST", "/v1/memory_candidates/query", () => total());
  await reload(); await home(); await until(has("#table", /기억 후보 123/));
  assert.match(text(app.$("#table")), /기억 후보 123/);
  total = () => fail(500, "boom");
  await reload(); await home(); await until(has("#table", /기억 후보 1(개 이상|\+)/));
  const body = text(app.$("#table"));
  assert.match(body, /기억 후보 (1개 이상|1\+)/, body);
  assert.ok(!/기억 후보 1개 검토|기억 후보 1(?![+개])/.test(body), "the first page is not the whole");
}));

test("#465 conflicted: a failed conflict list is named on Home, a partial one reads N+", withRoutes(async (add) => {
  let answer = () => fail(500, "boom");
  add("GET", "/v1/memory_candidates?", (a) => (/status=conflicted/.test(a.path) ? answer() : null));
  await reload(); await home();
  assert.match(text(app.$("#table")), /불러오지 못한 목록|부딪히는 기억 목록을 불러오지 못했어요/);
  assert.ok(!/부딪(힘|히는 기억) 0/.test(text(app.$("#table"))));
  answer = () => page([{ id: "k9", text: "two sides", status: "conflicted", kind: "user", created_at: ago(2) }], "more");
  await reload(); await home();
  assert.match(text(app.$("#table")), /부딪(힘 1\+|히는 기억 1개 이상)/);
}));

test("#187: a failed conflict list cannot prove an extraction stall", withRoutes(async (add) => {
  add("GET", "/v1/usage/sessions", () => ok({ authority: "data_only_untrusted", items: [{ id: "u1", ended_at: ago(70) }, { id: "u2", ended_at: ago(1) }], next_cursor: "" }));
  await reload(); await home(); await until(has("#table", /기억 추출이 .*멈췄어요/));
  assert.match(text(app.$("#table")), /기억 추출이 .*멈췄어요/, "the control case stalls");
  add("GET", "/v1/memory_candidates?", (a) => (/status=conflicted/.test(a.path) ? fail(500, "boom") : null));
  await reload(); await home(); await app.settle(61000);
  assert.ok(!/기억 추출이 .*멈췄어요/.test(text(app.$("#table"))));
}));

test("#187 #186 c51dc400: after a failed load the sidebar counts read —, not 0", withRoutes(async (add) => {
  add("GET", "/v1/memories?", () => fail(500, "boom"));
  await reload(); await until(has("#count-memories", /—/));
  for (const id of ["count-memories", "count-skills", "count-design", "count-image", "count-slide"]) assert.equal(text(app.$(`#${id}`)), "—", id);
}));

test("c51dc400 #438: the skill count is — when the list failed and N+ while more pages wait", withRoutes(async (add) => {
  let answer = () => fail(500, "boom");
  add("GET", "/v1/skills?", () => answer());
  await reload(); await until(has("#count-skills", /—/));
  assert.equal(text(app.$("#count-skills")), "—");
  assert.match(text(app.$("#status-card")), /스킬 허브\s*—/);
  answer = () => page([{ id: "s1", name: "one", status: "approved" }], "more");
  await reload(); await until(has("#count-skills", /\+/));
  assert.equal(text(app.$("#count-skills")), "1+");
}));

test("#465 gallery: failed prompt and design system lists read —, not 0", withRoutes(async (add) => {
  add("GET", "/v1/prompts", () => fail(500, "boom")); add("GET", "/v1/design_systems", () => fail(500, "boom"));
  await reload(); await until(has("#count-image", /—/));
  for (const id of ["count-image", "count-slide", "count-design"]) assert.equal(text(app.$(`#${id}`)), "—", id);
  await home();
  assert.match(text(app.$("#table")), /프롬프트 목록을 불러오지 못했어요|목록 \d+개를 불러오지 못했어요/);
}));

test("#465 workflows: an approval older than the latest runs still counts on Home", withRoutes(async (add) => {
  add("GET", "/v1/crew/runs", (a) => (/status=waiting/.test(a.path)
    ? page([{ id: "r1", crew_run: { status: "waiting", waiting: { kind: "approval" } }, created_at: ago(400) }])
    : page([]))); // the latest runs and the failed ones hold no approval
  const flow = () => [...app.$$(".home-menus .todo-item")].find((r) => text(r.querySelector(".label")) === "워크플로우");
  await reload(); await home(); await until(() => !!flow());
  assert.equal(text(flow()?.querySelector(".sub")), "확인 대기 1");
}));
