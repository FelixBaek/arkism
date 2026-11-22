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
  await app.click("#local-retry", 200);
  assert.equal(app.$("#unlock").hidden, true);
  assert.equal(app.calls.filter(([name]) => name === "managed_local_connect").length, 2);
  assert.equal(app.calls.some(([name]) => name === "local_start"), false);
  const [, call] = app.calls.find(([name, args]) => name === "api" && args.path.startsWith("/v1/capabilities?"));
  assert.equal(call.base, app.origin);
  assert.equal(call.token, "arkism-managed-local");
  assert.equal(new URL(call.base + call.path).searchParams.get("user_id"), "management:owner");
});
