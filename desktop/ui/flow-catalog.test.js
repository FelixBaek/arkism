const test = require("node:test");
const assert = require("node:assert/strict");
require("./keep.js"); // the shared stale-reply and redraw guards the screens use
const { boot, dom, crewRecords } = require("./testdata/crew-harness.cjs");

test("FLOW preserves callable roles and teams without exposing owned workflow definitions", async () => {
  const { flowItems, flowAlias } = await import("./modules/flowCatalog.mjs");
  const records = [
    { id: "role", crew: { type: "agent", handle: "review", workflow: "$owned" } },
    { id: "owned", crew: { type: "workflow", handle: "owned" } },
    { id: "public", crew: { type: "workflow", handle: "딥리서치" } },
    { id: "collision", crew: { type: "workflow", handle: "review" } },
    { id: "team", crew: { type: "team", handle: "meeting" } },
    { id: "persona", crew: { type: "persona", handle: "tone" } }
  ];
  assert.deepEqual(flowItems(records).map((r) => r.id), ["role", "public", "collision", "team"]);
  assert.equal(flowAlias(records[2], records), "@딥리서치");
  assert.equal(flowAlias(records[3], records), "$review");
  assert.equal(flowAlias(records[0], records), "@review");
});

test("an owned graph returns to its role inside FLOW with the unsaved role draft", async () => {
  const h = await boot({ view: "crew" });
  h.render(); await h.settle();
  document.querySelector('[data-id="crew:ag1"]').click(); await h.settle();
  dom.type(h.fields.find((f) => f.input.classList.contains("wk-instr")).input, "아직 저장하지 않은 지시");
  h.btn(h.top(), "단계 고치기").click(); await h.settle();
  assert.ok(h.top()?.classList.contains("graph-win"), "the graph opens in its own window, the role dialog no longer over it");
  assert.equal(h.dialogs().length, 1);
  document.querySelector(".graph-win-head .win-close").click(); await h.settle();
  assert.equal(document.querySelector(".graph-win"), null);
  assert.ok(h.top()?.classList.contains("ex-pop"));
  assert.equal(h.fields.filter((f) => f.input.classList.contains("wk-instr")).at(-1).input.value, "아직 저장하지 않은 지시");
  h.closeAll();
});

test("FLOW opens role and team editors from one catalogue and keeps every creation path", async () => {
  const h = await boot({ view: "crew" });
  h.render(); await h.settle();
  const rows = [...document.querySelectorAll(".wf-card[data-flow-kind]")];
  assert.deepEqual(new Set(rows.map((n) => n.dataset.flowKind)), new Set(["workflow", "agent", "team"]));
  assert.equal(document.querySelector('[data-id="crew:wf3"]'), null, "owned workflow is reached through its role");
  document.querySelector('[data-id="crew:ag1"]').click(); await h.settle();
  assert.ok(h.top()?.classList.contains("ex-pop"));
  assert.equal(h.state.view, "crew");
  h.closeAll();
  document.querySelector('[data-id="crew:tm1"]').click(); await h.settle();
  assert.ok(h.top()?.classList.contains("tm-pop"));
  assert.equal(h.state.view, "crew");
  h.closeAll();
  // 1511:4650: no single·team·step popup; the editor opens on 직접 부르기 → AI 작업 → 결과
  h.btn(document.querySelector("#controls"), "+ 새 워크플로우").click(); await h.settle();
  assert.equal(document.querySelector(".flow-create"), null);
  const win = document.querySelector("dialog.graph-win[open]");
  assert.ok(win, "the graph editor opens directly");
  assert.deepEqual([...win.querySelectorAll(".g-node")].map((n) => n.querySelector("strong")?.textContent), ["직접 부르기", "AI 작업", "결과"]);
  assert.ok(win.querySelector('.g-node[data-id="ai1"]').classList.contains("needs-task"));
  assert.match(win.textContent, /처음이라면/);
  h.closeAll();
});

test("every card is the one photo card (1478:12809): cover or the default photo, faces top-left, @name above the name", async () => {
  const records = crewRecords();
  const cover = { id: "c1", url: "https://img/c.jpg", thumb: "https://img/ct.jpg", author: "Kim", author_url: "https://u/kim" };
  Object.assign(records.find((r) => r.id === "ag1").crew, { cover, avatar: { art: "001", bg: "sage" } });
  records.find((r) => r.id === "tm1").crew.cover = cover;
  const h = await boot({ view: "crew", data: { crew: records } });
  h.render(); await h.settle();
  const card = (id) => document.querySelector(`[data-id="crew:${id}"]`);
  for (const id of ["ag1", "tm1"]) assert.equal(card(id).querySelector(".crew-cover-img").src, cover.url, id);
  for (const c of document.querySelectorAll(".wf-card[data-flow-kind]")) {
    assert.ok(c.classList.contains("has-cover") && c.querySelector(".crew-cover-img"), c.dataset.id);
    assert.equal(c.querySelector(".flow-card-role, .flow-card-category, .flow-kind"), null, "no role, category or kind line");
    assert.ok(c.querySelector(".crew-cover-top .wf-card-when"), "how it starts, top-right");
    const alias = c.querySelector(".flow-alias");
    if (alias.textContent) assert.equal(alias.nextElementSibling, c.querySelector(".crew-cover-title"), "@name sits above the name");
  }
  assert.ok(/img\/flow-cover\.webp$/.test(card("ag2").querySelector(".crew-cover-img").src), "no cover: the default photo");
  assert.ok(card("ag1").querySelector(".flow-faces .wk-face"), "the agent's own face top-left");
  assert.equal(card("ag1").querySelector(".wf-card-when").textContent, "직접 실행");
  card("ag1").querySelector(".crew-cover-img").dispatchEvent(new window.Event("error"));
  assert.ok(card("ag1").classList.contains("has-cover") && /flow-cover\.webp$/.test(card("ag1").querySelector(".crew-cover-img").src), "a photo that fails to load gives way to the default one");

  // the expert window picks a photo (no avatar in the head) and saves it with the agent
  card("ag2").click(); await h.settle();
  const d = h.top();
  assert.ok(!d.querySelector(".ex-heads .ex-face") && d.querySelector(".ex-heads .ex-cover"));
  d.querySelector(".ex-cover").click(); await h.settle();
  document.querySelector("dialog.tp-cover .tp-cover-ph").click(); await h.settle();
  assert.ok(d.querySelector(".ex-cover img"));
  assert.ok(d.querySelector(".ex-heads .tm-pop-credit a"), "the photo credit sits beside the photo");
  h.btn(d, "저장").click(); await h.settle();
  const saved = h.calls.filter((c) => c.method === "POST" && /\/v1\/crew\/ag2$/.test(c.url)).at(-1);
  assert.equal(saved?.body.crew.cover.author, "Park");
  d.querySelector(".ex-cover").click(); await h.settle();
  h.btn(document.querySelector("dialog.tp-cover"), "배경 없애기").click(); await h.settle();
  assert.equal(d.querySelector(".ex-cover img"), null);
  assert.equal(d.querySelector(".ex-heads .tm-pop-credit").textContent, "");
  h.btn(d, "저장").click(); await h.settle();
  assert.equal(h.calls.filter((c) => c.method === "POST" && /\/v1\/crew\/ag2$/.test(c.url)).at(-1).body.crew.cover, undefined, "배경 빼기 saves without one");
  h.closeAll();
});

test("FLOW has the search box first and a category picker, no 실행 방식 tabs (1424:2375)", async () => {
  const h = await boot({ view: "crew" });
  h.render(); await h.settle();
  const bar = document.querySelector(".wf-bar");
  assert.ok(bar.firstElementChild.classList.contains("wf-search"));
  assert.equal(bar.querySelector(".wf-kinds, [role=tab]"), null);
  assert.deepEqual(new Set([...document.querySelectorAll(".wf-card[data-flow-kind]")].map((n) => n.dataset.flowKind)), new Set(["workflow", "agent", "team"]));
  const pick = bar.querySelector(".wf-cat-btn");
  assert.equal(pick.textContent, "모든 카테고리", "no count beside 모든 카테고리");
  pick.click();
  const items = [...document.querySelectorAll(".wf-cat-menu .cat-menu-item")];
  assert.ok(items.length > 1 && items.slice(1).every((b) => b.querySelector(".wf-chip .icon")), "each category is its icon + tag");
  items[1].click();
  assert.equal(document.querySelector(".wf-cat-menu"), null);
  assert.ok(pick.querySelector(".icon"), "the picked category shows with its icon");
});

test("a new workflow is drafted by 노드 짜기 only on the button, can be undone, and is named on its first save", async () => {
  const h = await boot({ view: "crew" });
  h.render(); await h.settle();
  h.btn(document.querySelector("#controls"), "+ 새 워크플로우").click(); await h.settle();
  const win = () => document.querySelector("dialog.graph-win[open]");
  assert.match(win().textContent, /새 워크플로우/);
  assert.deepEqual(JSON.parse(win().dataset.popout), { detail: { new: true } }, "a new workflow opens in its own window too");
  assert.equal(h.btn(win(), /^실행$/).disabled, false, "실행 saves first (asking only for the name), then runs");
  const q = win().querySelector("input[aria-label='AI에게 맡길 일']");
  dom.type(q, "매일 PR 요약");
  assert.equal(h.assists.length, 0, "typing alone never asks the AI");
  h.assistReply = () => ({ status: "done", output: JSON.stringify({ graph: { nodes: [{ id: "start", type: "start", title: "직접 부르기" }, { id: "ai1", type: "ai", title: "PR 모으기", config: { goal: "열린 PR 모으기" } }, { id: "sum", type: "expert", title: "요약", config: { goal: "요약" } }, { id: "end", type: "end", title: "결과" }],
    edges: [{ from: "start", to: "ai1" }, { from: "ai1", to: "sum" }, { from: "sum", to: "end" }] } }) });
  h.btn(win(), "노드 짜기").click(); await h.settle();
  assert.equal(h.assists.length, 1);
  assert.deepEqual([...win().querySelectorAll(".g-node.drafted")].map((n) => n.dataset.id).sort(), ["ai1", "sum"]);
  assert.ok(win().querySelector('.g-node[data-id="sum"]').classList.contains("ai"), "AI steps come in as AI 작업 nodes");
  assert.doesNotMatch(win().textContent, /처음이라면/, "the guide leaves once the graph changed");
  assert.equal(h.btn(win().querySelector(".g-draft"), "되돌리기"), undefined, "no second 되돌리기 menu under the AI line");
  win().querySelector(".g-undo").click(); await h.settle(); // the toolbar's ← takes the draft back
  assert.equal(win().querySelectorAll(".g-node").length, 3);
  // 노드 짜기 also sets when it starts; a new schedule is saved switched on
  const drafted = JSON.parse(h.assistReply().output);
  h.assistReply = () => ({ status: "done", output: JSON.stringify({ ...drafted, trigger: { kind: "schedule", cron: "0 9 * * *" } }) });
  h.btn(win(), "노드 짜기").click(); await h.settle();
  assert.match(win().querySelector('.g-node[data-id="start"]').textContent, /매일 09:00/);
  // the first save asks only for the name, filled from the one line; no settings window
  h.asks.length = 0;
  h.btn(win(), "저장").click(); await h.settle();
  assert.equal(document.querySelector("dialog.wf-settings"), null);
  assert.equal(h.asks.length, 1);
  assert.match(h.asks[0], /이름을 정해/);
  assert.deepEqual(h.askOpts, { okLabel: "저장", primary: true });
  const made = h.calls.find((x) => x.method === "POST" && x.url === "/v1/crew" && x.body.crew.type === "workflow");
  assert.ok(made);
  assert.deepEqual([made.body.crew.name, made.body.crew.handle], ["매일 PR 요약", "매일PR요약"]);
  assert.deepEqual(made.body.crew.graph.nodes.map((n) => [n.id, n.type]), [["start", "start"], ["ai1", "ai"], ["sum", "ai"], ["end", "end"]]);
  assert.equal(made.body.crew.graph.nodes[1].config.model, "auto");
  assert.deepEqual([made.body.crew.trigger, made.body.crew.enabled], [{ kind: "schedule", cron: "0 9 * * *" }, true]);
  h.closeAll();
});

test("실행 on a new workflow names it with 저장하고 실행, and a long line makes a short @이름", async () => {
  const h = await boot({ view: "crew" });
  h.render(); await h.settle();
  h.btn(document.querySelector("#controls"), "+ 새 워크플로우").click(); await h.settle();
  const win = () => document.querySelector("dialog.graph-win[open]");
  dom.type(win().querySelector("input[aria-label='AI에게 맡길 일']"), "매일 아침 열린 PR을 모아 요약해서 텔레그램으로 보내 줘");
  h.btn(win(), /^실행$/).click(); await h.settle();
  assert.deepEqual(h.askOpts, { okLabel: "저장하고 실행", primary: true });
  const made = h.calls.find((x) => x.method === "POST" && x.url === "/v1/crew" && x.body.crew.type === "workflow");
  assert.equal(made.body.crew.name, "매일 아침 열린 PR을 모아 요약해서 텔레그램으로 보내 줘");
  assert.equal(made.body.crew.handle, "매일아침열린PR을모아", "whole words, about 12 Korean letters");
  h.closeAll();
});
