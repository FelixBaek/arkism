// alerts.js: hidden-window reads and the first run list as a baseline.
// Run: node --test desktop/ui/*.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("fs");
const path = require("path");
const vm = require("vm");

function load(stored = {}) {
  let now = 0;
  const sandbox = { window: {}, localStorage: { getItem: () => JSON.stringify(stored), setItem() {} }, Date: { now: () => now } };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, "alerts.js"), "utf8"), sandbox);
  const server = { candidates: 0, up: true, runs: [] };
  const sent = [];
  const deps = {
    state: { token: "t", scope: {}, caps: { actions: { review_list: true } }, data: { crew: [] }, loadError: "" },
    query: () => "",
    request: async (p) => {
      if (p === "/readyz") { if (!server.up) throw new Error("down"); return {}; }
      if (p.startsWith("/v1/memory_candidates")) return { total: server.candidates };
      return { items: server.runs };
    },
    tauri: { invoke: async (_cmd, args) => { sent.push(args.title); } },
    away: () => true
  };
  const tick = async () => { now += 30001; await sandbox.window.ArkismAlerts.check(deps); };
  return { alerts: sandbox.window.ArkismAlerts, server, sent, tick };
}

test("review and connection alerts read the server while the window is hidden", async () => {
  const { server, sent, tick } = load();
  await tick(); // baseline
  server.candidates = 2;
  server.up = false;
  await tick();
  assert.deepEqual(sent, ["검토할 기억이 생겼어요", "Arkism 연결이 끊겼어요"]);
});

test("runs that finished before 작업 완료 알림 was turned on are not sent", async () => {
  const { alerts, server, sent, tick } = load({ done: false });
  server.runs = [{ id: "old", crew_run: { status: "succeeded", crew_id: "c" } }];
  await tick(); // baseline without runs
  alerts.set({ done: true });
  await tick();
  assert.deepEqual(sent, []);
  server.runs = [...server.runs, { id: "new", crew_run: { status: "failed", crew_id: "c" } }];
  await tick();
  assert.deepEqual(sent, ["작업이 실패했어요"]);
});
