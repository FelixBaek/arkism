const test = require("node:test"), assert = require("node:assert/strict");

test("screen filters and connection identity have one owner", async () => {
  const { createAppState } = await import("./modules/state.mjs");
  const state = createAppState({server: "local", scope: {}, kind: "all", memTab: "active", skillTab: "all", crewTab: "all"});
  state.kind = "decision";
  assert.equal(state.screens.memory.kind, "decision");
  state.screens.memory.memTab = "candidate";
  assert.equal(state.memTab, "candidate");
  state.mode = "remote";
  assert.equal(state.connection.mode, "remote");
  state.server = "remote";
  assert.equal(state.connection.server, "remote");
  assert.equal(state.screens.skills.skillTab, "all");
  assert.equal(state.screens.crew.crewTab, "all");
});

async function runFixture() {
  const { createCrewRunView } = await import("./modules/crewRunView.mjs");
  let resolve, paints = 0;
  const state = { view: "graph", server: "local", scope: {project_id: "a"}, token: "fixture", connection: {generation: 1} };
  const ed = {rec: {id: "workflow"}, run: {id: "run", crew_run: {status: "running"}}};
  const api = createCrewRunView({state, ed, request: () => new Promise(r => { resolve = r; }), query: () => "", render: () => { paints++; }});
  const pending = api.pollRun();
  return {state, ed, api, pending, resolve: value => resolve(value), paints: () => paints};
}

for (const [name, change] of [
  ["connection scope changes", f => { f.state.scope = {project_id: "b"}; }],
  ["same run reopens in another editor", f => { f.ed.rec = {id: "workflow"}; }],
  ["locking cancels polling", f => { f.api.stopPolling(); }],
  ["navigation leaves the graph", f => { f.state.view = "memories"; }]
]) test("late polling cannot repaint after " + name, async () => {
  const f = await runFixture(), original = f.ed.run;
  change(f);
  f.resolve({id: "run", crew_run: {status: "running"}});
  await f.pending;
  assert.equal(f.ed.run, original);
  assert.equal(f.ed.timer, undefined);
  assert.equal(f.paints(), 0);
});

test("current run updates exactly once", async () => {
  const f = await runFixture(), result = {id: "run", crew_run: {status: "succeeded"}};
  f.resolve(result); await f.pending;
  assert.equal(f.ed.run, result);
  assert.equal(f.paints(), 1);
});

test("transport discards replies from an old connection", async () => {
  const { createAppApi } = await import("./modules/appApi.mjs");
  const state = {server:"local",token:"fixture",scope:{project_id:"a"},connection:{generation:1}};
  let resolve;
  const api = createAppApi({state,tauri:{invoke:() => new Promise(r => {resolve=r;})},trimSlash:x=>x,readJSON:JSON.parse});
  const request = api.request("/v1/memories");
  state.scope = {project_id:"b"};
  resolve({status:200,body:'{"items":["private old scope"]}'});
  await assert.rejects(request, error => error.code === "api_context_changed");
});

test("transport retains status and server machine error code", async () => {
  const { createAppApi } = await import("./modules/appApi.mjs");
  const api = createAppApi({state:{server:"local",token:"fixture",scope:{}},tauri:{invoke:async()=>({status:409,body:'{"code":"revision_conflict"}'})},trimSlash:x=>x,readJSON:JSON.parse});
  await assert.rejects(api.request("/v1/crew"), error => error.status === 409 && error.code === "revision_conflict" && error.message === "revision_conflict");
});
