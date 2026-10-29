const test = require("node:test");
const assert = require("node:assert/strict");
require("./keep.js"); // the shared stale-reply and redraw guards the screens use
const { boot, dom, simpleGraph, crewRecords } = require("./testdata/crew-harness.cjs");
// 리서치 개선안 검토 lives in 새 워크플로우 › 리서치 (the existing research itself opens from its card).
async function reviewResearch(h) {
  h.btn(document.querySelector("#controls"), "+ 새 워크플로우").click();
  h.btn(h.top(), /^리서치/).click(); await h.settle();
}

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
  h.btn(document.querySelector(".graph-win-head"), "돌아가기").click(); await h.settle();
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
  h.btn(document.querySelector("#controls"), "+ 새 워크플로우").click();
  assert.deepEqual([...h.top().querySelectorAll(".flow-create-option strong")].map((n) => n.textContent), ["단독 실행", "협업·회의", "단계 구성"], "리서치 is no longer a choice");
  h.closeAll();
  for (const label of ["단독 실행", "협업·회의", "단계 구성"]) {
    h.render();
    h.btn(document.querySelector("#controls"), "+ 새 워크플로우").click();
    const choice = [...h.top().querySelectorAll(".flow-create-option")].find((n) => n.textContent.includes(label));
    assert.ok(choice, label);
    choice.click(); await h.settle();
    assert.ok(h.top(), `${label} opens its existing editor`);
    h.closeAll();
  }
});

test("agent and team cards show their cover like a workflow card, the avatar in the bottom-right", async () => {
  const records = crewRecords();
  const cover = { id: "c1", url: "https://img/c.jpg", thumb: "https://img/ct.jpg", author: "Kim", author_url: "https://u/kim" };
  Object.assign(records.find((r) => r.id === "ag1").crew, { cover, avatar: { art: "001", bg: "sage" } });
  records.find((r) => r.id === "tm1").crew.cover = cover;
  const h = await boot({ view: "crew", data: { crew: records } });
  h.render(); await h.settle();
  const card = (id) => document.querySelector(`[data-id="crew:${id}"]`);
  for (const id of ["ag1", "tm1"]) {
    assert.ok(card(id).classList.contains("has-cover"), id);
    assert.equal(card(id).querySelector(".crew-cover-img").src, cover.url);
  }
  assert.ok(card("ag1").querySelector(".flow-card-foot .wk-face"), "an avatar moves to the bottom-right");
  assert.equal(card("tm1").querySelector(".flow-card-foot .wf-mark"), null, "no avatar, no corner mark");
  assert.ok(card("ag2").querySelector(".flow-card-top .wf-mark"), "a card without a photo keeps the avatar top-left");
  card("ag1").querySelector(".crew-cover-img").dispatchEvent(new window.Event("error"));
  assert.equal(card("ag1").classList.contains("has-cover"), false, "a photo that fails to load drops the dark cover look");
  assert.ok(card("ag1").querySelector(".flow-card-role") && card("ag1").querySelector(".flow-card-top .wk-face"), "and the card reads like one without a photo");
  const wf = document.querySelector(".wf-card.has-cover[data-flow-kind=workflow]");
  wf.querySelector(".crew-cover-img").dispatchEvent(new window.Event("error"));
  assert.equal(wf.classList.contains("has-cover"), false, "a workflow card keeps its layout and only drops the photo");

  // the expert window picks a photo apart from the avatar and saves it with the agent
  card("ag2").click(); await h.settle();
  const d = h.top();
  assert.ok(d.querySelector(".ex-heads .ex-face") && d.querySelector(".ex-heads .ex-cover"));
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

test("FLOW filters by 실행 방식 tabs 전체 | 단독 | 단계 | 협업, with the search box first", async () => {
  const h = await boot({ view: "crew" });
  h.render(); await h.settle();
  const bar = document.querySelector(".wf-bar");
  assert.ok(bar.firstElementChild.classList.contains("wf-search"));
  const tabs = [...bar.querySelectorAll(".wf-kinds [role=tab]")];
  assert.deepEqual(tabs.map((t) => t.textContent), ["전체", "단독", "단계", "협업"]);
  const kinds = () => new Set([...document.querySelectorAll(".wf-card[data-flow-kind]")].map((n) => n.dataset.flowKind));
  for (const [i, kind] of [[1, "agent"], [2, "workflow"], [3, "team"]]) {
    tabs[i].click(); await h.settle();
    assert.deepEqual(kinds(), new Set([kind]));
    assert.equal(tabs[i].getAttribute("aria-selected"), "true");
    assert.equal(bar.querySelectorAll(".wf-kinds .active").length, 1);
  }
  tabs[0].click(); await h.settle();
  assert.deepEqual(kinds(), new Set(["workflow", "agent", "team"]));
});
