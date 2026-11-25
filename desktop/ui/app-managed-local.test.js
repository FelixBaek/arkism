"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const { boot } = require("./testdata/app-harness.js");

test("a managed local profile keeps the desktop on its existing runtime when retrying", async (t) => {
  const app = await boot({
    invoke: {
      app_defaults: { local: true, managed_local: true },
      managed_local_connect: new Error("managed_local_server_unavailable")
    }
  });
  t.after(() => app.stop());
  assert.equal(app.$("#unlock").hidden, false);
  assert.equal(app.$("#local-retry").hidden, false);
  assert.equal(app.$("#local-doctor").hidden, true);
  assert.equal(app.calls.some(([name]) => name === "local_start"), false);

  app.fake.managed_local_connect = {
    server: app.origin,
    scope: { workspace_id: "default", project_id: "default", user_id: "management:owner" }
  };
  let version = "0.1.9";
  app.window.apiOverride = ({path}) => path === "/healthz"
    ? { status: 200, body: JSON.stringify({status: "ok", version, api_level: 1}) } : null;
  await app.click("#local-retry", 200);
  assert.equal(app.$("#unlock").hidden, true);
  assert.equal(app.calls.filter(([name]) => name === "managed_local_connect").length, 2);
  assert.equal(app.calls.some(([name]) => name === "local_start"), false);
  const [, call] = app.calls.find(([name, args]) => name === "api" && args.path.startsWith("/v1/capabilities?"));
  assert.equal(call.base, app.origin);
  assert.equal(call.token, "arkism-managed-local");
  assert.equal(new URL(call.base + call.path).searchParams.get("user_id"), "management:owner");

  const settings = async (tab) => {
    app.window.dispatchEvent(new app.window.CustomEvent("arkism:open-settings", { detail: { tab } }));
    await app.settle(100);
  };
  const checks = () => app.apiCalls.filter(([,path]) => path === "/healthz").length;
  version = "0.1.11";
  await settings("info");
  assert.match(app.dom.text(app.$("#table")), /이 PC의 기억 저장소.*v0\.1\.11/s);
  const before = checks();
  version = "0.1.12";
  const retry = app.$("#table").querySelectorAll("button").find((b) => app.dom.text(b) === "다시 확인");
  assert.ok(retry);
  await app.click(retry);
  assert.equal(checks(), before + 1);
  assert.match(app.dom.text(app.$("#table")), /이 PC의 기억 저장소.*v0\.1\.12/s);
  assert.equal(app.calls.some(([cmd]) => cmd === "update_install"), false);
  await settings("general");
  version = "0.1.13";
  await settings("info");
  assert.match(app.dom.text(app.$("#table")), /이 PC의 기억 저장소.*v0\.1\.13/s);
});
