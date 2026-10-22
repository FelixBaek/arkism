// The app (entry.mjs) in a plain browser, without the desktop shell: it waits on the connect form,
// says a server connection needs the app, and its API calls go out over fetch.
// Run: node --test desktop/ui/*.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const { boot } = require("./testdata/app-harness.js");

const realFetch = globalThis.fetch;
let pages, app;
const fetched = [];
test.before(async () => {
  app = await boot({
    stored: { "arkism.ssh": "me@old-server" }, // an older version saved the address as a plain string
    beforeEntry: (env) => {
      delete env.window.__TAURI__;
      env.window.fetch = (url, init) => { fetched.push([url, init]); return realFetch(url, init); };
      let fn;
      Object.defineProperty(env.window, "ArkismPages", { configurable: true, get: () => fn, set: (f) => { fn = (d) => { pages = d; return f(d); }; } });
    }
  });
  await app.settle(500);
});
test.after(() => app?.stop());

test("without the desktop shell it waits on the connect form with the saved address", () => {
  assert.equal(app.$("#unlock").hidden, false);
  assert.equal(app.calls.length, 0);
  assert.deepEqual(pages.state.ssh, { host: "me@old-server" });
  assert.equal(app.$("#ssh-form input").value, "me@old-server");
});

test("SSH 테스트 and 연결하기 say a server connection needs the app", async () => {
  assert.match((await pages.sshTest({ host: "srv", port: null })).text, /Arkism 앱에서만/);
  await assert.rejects(pages.sshConnect({ host: "srv", port: null }), /Arkism 앱에서만/);
});

test("API calls go out over fetch with the token, and HTTP errors keep their status", async () => {
  pages.state.server = app.origin + "//";
  pages.state.token = "tok";
  const caps = await pages.request(`/v1/capabilities?${pages.query()}`);
  assert.ok(caps);
  const [url, init] = fetched.at(-1);
  assert.equal(url.split("?")[0], app.origin + "/v1/capabilities");
  assert.equal(init.headers.Authorization, "Bearer tok");
  // a reply that lands after the connection changed is dropped
  const late = pages.request("/v1/skills");
  pages.state.token = "other";
  await assert.rejects(late, (e) => e.code === "api_context_changed" && e.status === 0);
  pages.state.token = "";
});
