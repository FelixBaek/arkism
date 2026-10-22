// The app (entry.mjs) as a separate popup window: it takes over the main window's connection,
// opens the one popup the payload names, hands the window on when that popup swaps itself, and closes with it.
// Run: node --test desktop/ui/*.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const { boot, startPreview } = require("./testdata/app-harness.js");

let app, preview;
test.before(async () => {
  preview = await startPreview();
  const popout = JSON.stringify({ session: { mode: "local", server: preview.origin, token: "preview", scope: { workspace_id: "default" } }, recipe: { event: "arkism:open-team", detail: { id: "team-design" } } });
  app = await boot({ preview, popout });
  for (let i = 0; i < 40 && !app.$("dialog.popout-root"); i++) await app.settle(500); // startPopout polls for the popup every 100 ms
});
test.after(() => { app?.stop(); preview?.stop(); });

const ran = (cmd) => app.calls.filter(([c]) => c === cmd).length;

test("the popup window opens the named popup on the passed connection", () => {
  assert.ok(app.document.documentElement.classList.contains("popout"));
  assert.equal(ran("app_defaults"), 0); // the main window's connection, not a start of its own
  assert.ok(app.apiCalls.some(([, p]) => p.startsWith("/v1/capabilities")));
  const root = app.$("dialog[open]");
  assert.ok(root);
  assert.ok(root.classList.contains("popout-root"));
});

test("a popup that swaps itself hands the window on; the last one closing closes the window", async () => {
  const first = app.$("dialog.popout-root[open]");
  const next = app.document.createElement("dialog");
  app.document.body.append(next);
  next.showModal();
  first.close();
  await app.settle(100);
  assert.ok(next.classList.contains("popout-root"));
  assert.equal(ran("popout_close"), 0);
  app.window.dispatchEvent(new app.window.Event("pagehide"));
  assert.ok(app.window.localStorage.getItem("arkism.popout-saved"));
  next.close();
  await app.settle(100);
  assert.equal(ran("popout_close"), 1);
});
