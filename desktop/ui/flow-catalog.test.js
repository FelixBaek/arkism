const test = require("node:test");
const assert = require("node:assert/strict");
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

test("research is previewed before creation and preserves its graph and deep mode", async () => {
  const crew = { type: "workflow", name: "리서치", handle: "리서치", model: "auto", graph: simpleGraph(), limits: { max_steps: 12, max_minutes: 20 }, research: { quick_node: "ai1", deep_node: "deep", deep_alias: "딥리서치", evidence_strategy: "sources-and-gaps-v1" } };
  const h = await boot({ view: "crew", routes: [["GET", /^\/v1\/crew\/templates\/research\?/, () => ({ crew, aliases: { standard: "리서치", deep: "딥리서치" }, changes: ["근거 부족 시 추가 조사"] })]] });
  h.render(); await h.settle();
  h.btn(document.querySelector("#controls"), "+ 새 워크플로우").click();
  h.btn(h.top(), /^리서치/).click(); await h.settle();
  assert.ok(h.top()?.classList.contains("wf-settings"));
  assert.equal(h.calls.filter((r) => r.method === "POST" && r.url === "/v1/crew").length, 0);
  assert.match(h.top().querySelector(".research-review").textContent, /시간이 오래 걸린다는 이유로 전환하지 않아요/);
  h.btn(h.top(), "리서치 만들기").click(); await h.settle();
  const saved = h.calls.find((r) => r.method === "POST" && r.url === "/v1/crew");
  assert.ok(saved);
  assert.equal(saved.body.crew.handle, "리서치");
  assert.equal(saved.body.crew.model, crew.model);
  assert.deepEqual(saved.body.crew.graph, crew.graph);
  assert.deepEqual(saved.body.crew.limits, crew.limits);
  assert.deepEqual(saved.body.crew.research, crew.research);
  h.closeAll();
});

test("research review of an existing definition explicitly saves a reviewed revision", async () => {
  const records = crewRecords();
  const existing = records.find((r) => r.id === "wf2");
  Object.assign(existing.crew, { name: "리서치", handle: "리서치", enabled: true, skills: ["source-check"], budget: { monthly_usd: 25 } });
  records.push({ id: "existing-deep", revision: 2, crew: { ...structuredClone(existing.crew), name: "기존 딥리서치", handle: "딥리서치" } });
  const crew = { ...structuredClone(existing.crew), research: { quick_node: "ai1", deep_node: "deep", deep_alias: "리서치_심층", evidence_strategy: "sources-and-gaps-v1", helper_origin: "reviewed" } };
  const h = await boot({ view: "crew", data: { crew: records }, routes: [["GET", /^\/v1\/crew\/templates\/research\?/, () => ({ crew, existing_id: "wf2", expected_revision: 17, aliases: { standard: "리서치", deep: "리서치_심층" }, changes: ["기존 딥리서치를 보존하고 고유 별칭을 제안해요"] })]] });
  h.render(); await h.settle();
  await reviewResearch(h);
  assert.ok(h.top()?.classList.contains("wf-settings"));
  assert.match(h.top().querySelector(".research-review").textContent, /@리서치_심층/);
  assert.equal(h.calls.some((r) => r.method === "POST"), false);
  dom.type(h.top().querySelector('[aria-label="깊은 조사 @이름"]'), "근거심층조사");
  h.btn(h.top(), "개선안 저장").click(); await h.settle();
  const saved = h.calls.find((r) => r.method === "POST" && r.url === "/v1/crew/wf2");
  assert.ok(saved);
  assert.equal(saved.body.expected_revision, 17, "use the reviewed server revision, not the stale catalogue revision");
  for (const key of ["graph", "model", "limits", "skills", "budget", "enabled", "trigger"]) assert.deepEqual(saved.body.crew[key], crew[key], key);
  assert.deepEqual(saved.body.crew.research, { ...crew.research, deep_alias: "근거심층조사" });
  assert.equal(h.calls.some((r) => r.method === "POST" && (r.url === "/v1/crew" || r.url === "/v1/crew/existing-deep")), false);
  h.closeAll();
});

test("an owned graph returns to its role inside FLOW with the unsaved role draft", async () => {
  const h = await boot({ view: "crew" });
  h.render(); await h.settle();
  document.querySelector('[data-id="crew:ag1"]').click(); await h.settle();
  dom.type(h.fields.find((f) => f.input.classList.contains("wk-instr")).input, "아직 저장하지 않은 지시");
  h.btn(h.top(), "단계 고치기").click(); await h.settle();
  assert.equal(h.state.view, "graph");
  assert.equal(h.top(), null, "the role dialog no longer covers the graph");
  h.btn(document.querySelector(".page-head"), "돌아가기").click(); await h.settle();
  assert.equal(h.state.view, "crew");
  assert.ok(h.top()?.classList.contains("ex-pop"));
  assert.equal(h.fields.filter((f) => f.input.classList.contains("wk-instr")).at(-1).input.value, "아직 저장하지 않은 지시");
  h.closeAll();
});

test("research review refuses an unknown revision and keeps conflicts open for review", async () => {
  const crew = { type: "workflow", name: "리서치", handle: "리서치", graph: simpleGraph(), research: { deep_alias: "딥리서치" } };
  for (const issue of ["missing-revision", "revision_conflict", "handle_taken"]) {
    const proposal = { crew, existing_id: "wf1", changes: [], ...(issue === "missing-revision" ? {} : { expected_revision: 17 }) };
    const h = await boot({ view: "crew", routes: [["GET", /^\/v1\/crew\/templates\/research\?/, () => proposal], ["POST", /^\/v1\/crew\/wf1$/, () => new Error(issue)]] });
    h.render(); await h.settle();
    await reviewResearch(h);
    if (issue === "missing-revision") {
      assert.equal(h.top(), null);
      assert.equal(h.calls.some((r) => r.method === "POST"), false);
    } else {
      h.btn(h.top(), "개선안 저장").click(); await h.settle();
      assert.ok(h.top()?.classList.contains("wf-settings"));
      assert.ok(h.top().querySelector(".form-error").textContent);
      assert.equal(h.calls.some((r) => r.method === "POST" && r.url === "/v1/crew"), false);
    }
    h.closeAll();
  }
});

test("a basic research fallback does not claim to support automatic deep escalation", async () => {
  const crew = { type: "workflow", name: "리서치", handle: "리서치", graph: simpleGraph() };
  const h = await boot({ view: "crew", routes: [["GET", /^\/v1\/crew\/templates\/research\?/, () => ({ crew, changes: ["기본 조사 템플릿"], aliases: { standard: "리서치", deep: "" } })]] });
  h.render(); await h.settle();
  await reviewResearch(h);
  assert.match(h.top().querySelector(".research-review").textContent, /자동 심층 전환이 포함되지 않아요/);
  assert.equal(h.top().querySelector('[aria-label="깊은 조사 @이름"]'), null);
  assert.equal(h.calls.some((r) => r.method === "POST"), false);
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
