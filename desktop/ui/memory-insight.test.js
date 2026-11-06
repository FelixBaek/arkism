// Run with: node --test desktop/ui/*.test.js
const test = require("node:test");
const assert = require("node:assert/strict");
require("./keep.js");
const dom = require("./testdata/fake-dom.cjs");

const SCOPE = { workspace_id: "w", project_id: "p" };

function setup(tab, routes, { role = "human", answers = [] } = {}) {
  const { document, window } = dom.install();
  dom.Element.prototype.setCustomValidity = function (m) { this.validity = m; };
  require("./memory-pages.js");
  for (const f of ["./digests.js", "./memory-time.js", "./knowledge.js", "./memory-insight.js"]) { delete require.cache[require.resolve(f)]; require(f); } // memory rows come from digests.js
  const h = dom.helpers(document);
  for (const id of ["app", "chips", "stats", "table", "detail"]) { const n = h.el("div"); n.id = id; document.body.append(n); }
  const state = { view: "memories", memTab: tab, server: "s", token: "t", scope: { ...SCOPE }, caps: { role } };
  const calls = [], notices = [], opened = [], asked = [], saved = [];
  window.ask = async (m) => { asked.push(m); return answers.length ? answers.shift() : true; };
  window.askText = async (m) => { asked.push(m); return answers.shift() ?? null; };
  const request = async (path, opts) => {
    calls.push([path, opts]);
    for (const [prefix, fn] of routes) if (path.startsWith(prefix)) return fn(path, opts);
    throw new Error(`unexpected ${path}`);
  };
  const query = (o = {}) => new URLSearchParams(Object.entries({ ...state.scope, ...o }).filter(([, v]) => v !== "")).toString();
  const detailShell = (label) => { const body = h.el("div", "detail-body"); h.$("#detail").replaceChildren(h.el("span", "", label), body); return body; };
  const section = (title, aside) => { const s = h.el("section", "section"), hd = h.el("h3", "", title); if (aside) hd.append(h.el("small", "", aside)); s.append(hd); return s; };
  const facts = (pairs) => { const dl = h.el("dl", "facts"); pairs.filter(([, v]) => v).forEach(([k, v]) => dl.append(h.el("dt", "", k), h.el("dd", "", v))); return dl; };
  const callout = (title, text) => { const c = h.el("div", "callout"); c.append(h.el("strong", "", title), h.el("p", "", text)); return c; };
  const view = window.ArkismMemoryInsight({ state, $: h.$, el: h.el, raw: h.raw, icon: h.icon, button: h.button, request, query, detailShell, section, facts, callout, KINDS, kindLabel: (k) => (KINDS[k] || [k])[0],
    openDetail: () => h.$("#app").classList.add("detail-open"), notice: (m) => notices.push(m), openRecord: (r) => opened.push(r),
    saveFile: (...a) => saved.push(a), fileSize: (n) => `${n} B` }, tab);
  return { view, state, calls, notices, opened, asked, saved, h };
}
const KINDS = { user: ["선호"], procedural: ["작업 규칙"], decision: ["결정"], lesson: ["교훈"], episodic: ["기록"], semantic: ["사실"] };
const body = (call) => JSON.parse(call[1].body);
const sentTo = (t, path) => t.calls.filter(([p]) => p === path);
const err = (code, status) => { const e = new Error(code); e.status = status; return e; };
const memory = (id, o = {}) => ({ id, text: `기억 ${id}`, status: "active", revision: 1, ...o });

test("ask sends the question, shows the answer and opens a cited memory", async () => {
  const t = setup("ask", [["/v1/reflect", () => ({ answer: "스테이징에서 먼저 확인해요.", cited_memory_ids: ["m4", "gone"], directives_applied: ["d1"], retrieved: [{ id: "m4" }] })],
    ["/v1/memories/m4?", () => memory("m4")], ["/v1/memories/gone?", () => { throw err("not_found", 404); }]]);
  t.view.draw();
  assert.match(t.h.$("#table").textContent, /승인된 기억만 보고 답해요/);
  const input = t.h.$("#chips input");
  input.value = "  "; dom.fire(t.h.$("#chips form"), "submit"); await dom.flush();
  assert.equal(sentTo(t, "/v1/reflect").length, 0, "an empty question is not sent");
  input.value = "배포 전에 뭐 해?"; t.h.$("#chips input[type=number]").value = "5";
  dom.fire(t.h.$("#chips form"), "submit"); await dom.flush();
  assert.deepEqual(body(sentTo(t, "/v1/reflect")[0]), { scope: SCOPE, query: "배포 전에 뭐 해?", limit: 5 });
  assert.match(t.h.$("#table").textContent, /스테이징에서 먼저 확인해요/);
  assert.match(t.h.$("#table").textContent, /근거 기억 2개/);
  assert.equal(t.h.$("#table .insight-answer p").getAttribute("data-no-i18n"), "", "the answer is data, never translated");
  const [cited, gone] = t.h.$("#table").querySelectorAll(".dg-item");
  assert.match(cited.textContent, /기억 m4/);
  assert.equal(gone.disabled, true);
  cited.click(); await dom.flush();
  assert.equal(t.opened[0].id, "m4");
});

test("ask says a model must be set up when the server has none, and other failures plainly", async () => {
  let fail = err("model_not_configured", 503);
  const t = setup("ask", [["/v1/reflect", () => { throw fail; }]]);
  t.view.draw();
  t.h.$("#chips input").value = "질문"; dom.fire(t.h.$("#chips form"), "submit"); await dom.flush();
  assert.equal(body(sentTo(t, "/v1/reflect")[0]).limit, undefined, "the server picks how many memories to use");
  assert.match(t.h.$("#table .callout").textContent, /모델 설정 필요/);
  fail = err("model_budget_exceeded", 429);
  dom.fire(t.h.$("#chips form"), "submit"); await dom.flush();
  assert.match(t.h.$("#table .callout").textContent, /사용량을 다 썼어요/);
});

const rule = (o = {}) => ({ id: "d1", revision: 3, scope: SCOPE, title: "배포 전 묻기", rule: "운영 배포 전에 사람에게 묻는다.", priority: 80, applies_to: "both", active: true, tags: ["x"], created_by: "me", created_at: "2026-10-01T00:00:00Z", updated_at: "2026-10-02T00:00:00Z", ...o });

test("rules list, turn off, edit and delete with the revision; agents only read", async () => {
  let rows = [rule(), rule({ id: "d2", title: "꺼진 규칙", active: false })];
  const t = setup("rules", [["/v1/directives?", () => ({ items: rows, next_cursor: "" })], ["/v1/directives/d1", (p, o) => (o.method === "DELETE" ? { deleted: true } : { ...rule(), ...JSON.parse(o.body), revision: 4 })],
    ["/v1/directives", (p, o) => ({ ...rule({ id: "d9" }), ...JSON.parse(o.body), revision: 1 })]]);
  t.view.draw(); await dom.flush();
  const items = t.h.$("#table").querySelectorAll(".original-row");
  assert.equal(items.length, 2);
  assert.match(items[1].textContent, /꺼짐/);
  items[0].click();
  assert.match(t.h.$("#detail").textContent, /운영 배포 전에 사람에게 묻는다/);
  dom.buttons(t.h.$("#detail"), "끄기")[0].click(); await dom.flush();
  const off = t.calls.find(([p, o]) => p === "/v1/directives/d1" && o?.method === "POST");
  assert.deepEqual(body(off), { scope: SCOPE, title: "배포 전 묻기", rule: "운영 배포 전에 사람에게 묻는다.", priority: 80, applies_to: "both", active: false, tags: ["x"], expected_revision: 3 });
  assert.equal(t.notices.at(-1), "규칙을 껐어요.");
  // a new rule: title and text, the rest under 자세히 with defaults
  dom.buttons(t.h.$("#chips"), "규칙 추가")[0].click();
  const form = t.h.$("#detail form");
  form.querySelector("input").value = "새 규칙"; form.querySelector("textarea").value = "짧게 답한다.";
  dom.fire(form, "submit"); await dom.flush();
  const made = t.calls.find(([p, o]) => p === "/v1/directives" && o?.method === "POST");
  assert.deepEqual(body(made), { scope: SCOPE, title: "새 규칙", rule: "짧게 답한다.", applies_to: "both", active: true, tags: [] });
  // delete asks first and sends the revision
  rows = [rule()]; t.view.close(); await t.view.draw();
  t.h.$("#table .original-row").click();
  dom.buttons(t.h.$("#detail"), "지우기")[0].click(); await dom.flush();
  const del = t.calls.find(([, o]) => o?.method === "DELETE");
  assert.deepEqual(body(del), { scope: SCOPE, expected_revision: 3 });
  assert.match(t.asked.at(-1), /이 규칙을 지울까요/);

  const agent = setup("rules", [["/v1/directives?", () => ({ items: [rule()] })]], { role: "agent" });
  agent.view.draw(); await dom.flush();
  assert.equal(dom.buttons(agent.h.$("#chips"), "규칙 추가").length, 0);
  agent.h.$("#table .original-row").click();
  assert.equal(dom.buttons(agent.h.$("#detail"), "끄기").length, 0);
  assert.match(agent.h.$("#detail").textContent, /사람만 만들고 바꿀 수 있어요/);
});

test("an agent's proposed rule waits for a person: 승인 saves it on, 거절 deletes it; an agent sees no buttons", async () => {
  const proposed = rule({ id: "d1", active: false, proposed: true, created_by: "agent-1" });
  const t = setup("rules", [["/v1/directives?", () => ({ items: [proposed], next_cursor: "" })],
    ["/v1/directives/d1", (p, o) => (o.method === "DELETE" ? { deleted: true } : { ...proposed, ...JSON.parse(o.body), proposed: false, revision: 4 })]]);
  t.view.draw(); await dom.flush();
  const row = t.h.$("#table .original-row");
  assert.match(row.textContent, /AI 제안/);
  row.click();
  assert.match(t.h.$("#detail").textContent, /규칙 · AI 제안/);
  assert.match(t.h.$("#detail").textContent, /승인하기 전에는 AI가 지키지 않아요/);
  dom.buttons(t.h.$("#detail"), "승인")[0].click(); await dom.flush();
  const ok = t.calls.find(([p, o]) => p === "/v1/directives/d1" && o?.method === "POST");
  assert.equal(body(ok).active, true); assert.equal(body(ok).expected_revision, 3);
  assert.equal(t.notices.at(-1), "규칙을 승인했어요. 지금부터 지켜요.");
  t.view.close(); await t.view.draw();
  t.h.$("#table .original-row").click();
  dom.buttons(t.h.$("#detail"), "거절")[0].click(); await dom.flush();
  assert.match(t.asked.at(-1), /제안을 거절할까요/);
  assert.deepEqual(body(t.calls.find(([, o]) => o?.method === "DELETE")), { scope: SCOPE, expected_revision: 3 });
  assert.equal(t.notices.at(-1), "제안을 거절했어요.");

  const agent = setup("rules", [["/v1/directives?", () => ({ items: [proposed] })]], { role: "agent" });
  agent.view.draw(); await dom.flush();
  agent.h.$("#table .original-row").click();
  assert.equal(dom.buttons(agent.h.$("#detail"), "승인").length, 0);
  assert.match(agent.h.$("#detail").textContent, /승인과 거절은 사람만 할 수 있어요/);
});

test("a failed rule list says so and offers a retry", async () => {
  let down = true;
  const t = setup("rules", [["/v1/directives?", () => { if (down) throw err("x", 500); return { items: [rule()] }; }]]);
  t.view.draw(); await dom.flush();
  assert.match(t.h.$("#table").textContent, /불러오지 못했어요/);
  down = false; dom.buttons(t.h.$("#table"), "다시 불러오기")[0].click(); await dom.flush();
  assert.equal(t.h.$("#table").querySelectorAll(".original-row").length, 1);
});

const entity = (o = {}) => ({ id: "e1", name: "Alex", type: "person", aliases: ["알렉스"], mentions: 2, proposed: true, ...o });

test("an entity page lists its memories and a person can confirm, rename, drop an alias and merge", async () => {
  const t = setup("entities", [["/v1/entities?", (p) => ({ items: [entity(), entity({ id: "e2", name: "Orbit", type: "project", aliases: [], proposed: false })], next_cursor: "", q: p })],
    ["/v1/entities/e1?", () => ({ ...entity(), first_seen: "2026-10-01T00:00:00Z", last_seen: "2026-10-30T00:00:00Z", memories: [{ id: "m5", snippet: "Alex는 목록을 좋아해요" }], related: [{ id: "e2", name: "Orbit", type: "project", weight: 1 }], timeline: [] })],
    ["/v1/entities/e1/merge", () => entity({ id: "e2" })], ["/v1/entities/e1", () => entity({ proposed: false })], ["/v1/memories/m5?", () => memory("m5")]], { answers: ["Alexander"] });
  t.view.draw(); await dom.flush();
  assert.match(t.h.$("#table").textContent, /사람 · 기억 2개/);
  assert.match(t.h.$("#table").textContent, /확인 필요/);
  t.h.$("#table .original-row").click(); await dom.flush();
  assert.match(t.h.$("#detail").textContent, /Alex는 목록을 좋아해요/);
  assert.match(t.h.$("#detail").textContent, /알렉스/);
  assert.match(t.h.$("#detail").textContent, /처음 나온 때 .*2026.* · 마지막으로 나온 때 .*2026/);
  t.h.$("#detail .dg-item").click(); await dom.flush();
  assert.equal(t.opened[0].id, "m5", "a mention opens the memory");
  dom.buttons(t.h.$("#detail"), "맞아요")[0].click(); await dom.flush();
  assert.deepEqual(body(t.calls.find(([p, o]) => p === "/v1/entities/e1" && o?.method === "PATCH")), { scope: SCOPE, confirm: true });
  dom.buttons(t.h.$("#detail"), "이름 바꾸기")[0].click(); await dom.flush();
  assert.deepEqual(body(t.calls.filter(([, o]) => o?.method === "PATCH").at(-1)), { scope: SCOPE, name: "Alexander" });
  dom.buttons(t.h.$("#detail"), "빼기")[0].click(); await dom.flush();
  assert.deepEqual(body(t.calls.filter(([, o]) => o?.method === "PATCH").at(-1)), { scope: SCOPE, remove_aliases: ["알렉스"] });
  const into = t.h.$("#detail details select"); into.value = "e2";
  dom.buttons(t.h.$("#detail"), "합치기")[0].click(); await dom.flush();
  assert.deepEqual(body(t.calls.find(([p]) => p === "/v1/entities/e1/merge")), { scope: SCOPE, into: "e2" });
  assert.equal(t.notices.at(-1), "이름을 합쳤어요.");

  const agent = setup("entities", [["/v1/entities?", () => ({ items: [entity()] })], ["/v1/entities/e1?", () => ({ ...entity(), memories: [], related: [], timeline: [] })]], { role: "agent" });
  agent.view.draw(); await dom.flush();
  agent.h.$("#table .original-row").click(); await dom.flush();
  assert.equal(dom.buttons(agent.h.$("#detail"), "이름 바꾸기").length, 0);
  assert.equal(dom.buttons(agent.h.$("#detail"), "빼기").length, 0);
  assert.match(agent.h.$("#detail").textContent, /읽을 수 있는 기억이 없어요/);
});

test("the entity search goes to the server and an empty result says so", async () => {
  const t = setup("entities", [["/v1/entities?", () => ({ items: [] })]]);
  t.view.draw(); await dom.flush();
  assert.match(t.h.$("#table").textContent, /아직 모인 이름이 없어요/);
  t.h.$("#chips input").value = "orb"; dom.fire(t.h.$("#chips form"), "submit"); await dom.flush();
  assert.match(t.calls.at(-1)[0], /q=orb/);
  assert.match(t.h.$("#table").textContent, /검색과 맞는 이름이 없어요/);
});

test("비슷한 이름: the list can keep only merge proposals; 합치기 merges into the proposed name, 다른 이름이에요 dismisses", async () => {
  const near = [{ id: "e2", name: "Alexx", score: 0.71 }];
  const t = setup("entities", [["/v1/entities?", () => ({ items: [entity({ proposed: false, similar: near })] })],
    ["/v1/entities/e1?", () => ({ ...entity({ proposed: false, similar: near }), memories: [], related: [], timeline: [] })],
    ["/v1/entities/e1/merge", () => entity({ id: "e2" })], ["/v1/entities/e1", () => entity({ proposed: false })]]);
  t.view.draw(); await dom.flush();
  assert.match(t.h.$("#table").textContent, /합칠 후보/);
  const only = t.h.$("#chips input[type=checkbox]"); only.checked = true; dom.fire(only, "change"); await dom.flush();
  assert.match(t.calls.at(-1)[0], /similar=true/);
  t.h.$("#table .original-row").click(); await dom.flush();
  assert.match(t.h.$("#detail").textContent, /비슷한 이름/); assert.match(t.h.$("#detail").textContent, /71% 비슷해요/);
  dom.buttons(t.h.$("#detail .insight-similar"), "합치기")[0].click(); await dom.flush(); await dom.flush();
  assert.match(t.asked.at(-1), /Alexx/);
  assert.deepEqual(body(t.calls.find(([p]) => p === "/v1/entities/e1/merge")), { scope: SCOPE, into: "e2" });
  t.h.$("#table .original-row").click(); await dom.flush();
  dom.buttons(t.h.$("#detail"), "다른 이름이에요")[0].click(); await dom.flush();
  assert.deepEqual(body(t.calls.filter(([, o]) => o?.method === "PATCH").at(-1)), { scope: SCOPE, dismiss_similar: true });

  const agent = setup("entities", [["/v1/entities?", () => ({ items: [entity()] })]], { role: "agent" });
  agent.view.draw(); await dom.flush();
  assert.equal(agent.h.$("#chips input[type=checkbox]"), null, "agents get no proposals, so no filter");
});

test("ask sends the 자세히 options and a field list as a JSON schema, then shows steps, the structured answer and the space mission", async () => {
  const t = setup("ask", [["/v1/spaces/w/p/config/effective", () => ({ config: { reflect_mission: "배포 안전을 먼저 본다" } })],
    ["/v1/reflect", () => ({ answer: '{"할 일":"스테이징 확인","급함":true}', structured_answer: { "할 일": "스테이징 확인", "급함": true }, cited_memory_ids: ["m4"], directives_applied: [], retrieved: [{ id: "m4", kind: "lesson", score: 0.9 }],
      tool_trace: [{ tool: "recall", args: { query: "배포 순서" }, ids: ["m4"] }, { tool: "entity", args: { name: "Orbit" }, ids: [], error: "denied" }], iterations: 3, usage: { total_tokens: 812 } })],
    ["/v1/memories/m4?", () => memory("m4")]]);
  t.view.draw(); await dom.flush();
  assert.match(t.h.$("#table .insight-mission").textContent, /배포 안전을 먼저 본다/, "the space mission is shown before asking");
  const form = t.h.$("#chips form");
  t.h.$("#chips input").value = "배포 전에 뭐 해?";
  form.querySelector("[data-opt=iterations]").value = "5";
  form.querySelector("[data-opt=tokens]").value = "10"; // out of range: the server default stays
  const add = [...form.querySelectorAll("button")].find((b) => b.textContent === "칸 추가");
  add.click(); add.click(); add.click();
  const names = form.querySelectorAll(".insight-shape-row input:not([type])"), kinds = form.querySelectorAll(".insight-shape-row select");
  names[0].value = "할 일"; dom.fire(names[0], "input");
  names[1].value = "급함"; dom.fire(names[1], "input"); kinds[1].value = "boolean"; dom.fire(kinds[1], "change");
  const must = form.querySelectorAll(".insight-shape-row input[type=checkbox]")[1]; must.checked = false; dom.fire(must, "change");
  dom.fire(form, "submit"); await dom.flush();
  assert.deepEqual(body(sentTo(t, "/v1/reflect")[0]), { scope: SCOPE, query: "배포 전에 뭐 해?", max_iterations: 5,
    response_schema: { type: "object", properties: { "할 일": { type: "string" }, "급함": { type: "boolean" } }, required: ["할 일"], additionalProperties: false } }, "the empty third field is left out");
  const table = t.h.$("#table");
  assert.match(table.querySelector(".insight-answer").textContent, /스테이징 확인/);
  assert.match(table.querySelector(".insight-answer").textContent, /예/, "a yes/no field reads as words");
  assert.match(table.textContent, /AI가 더 찾아본 과정 2번/);
  assert.match(table.textContent, /기억 다시 찾기 · 배포 순서/);
  assert.match(table.textContent, /권한이 없어 건너뛰었어요/);
  assert.match(table.textContent, /이 공간의 목표에 맞춰 답해요/);
  assert.match(table.querySelector(".insight-json").textContent, /"total_tokens": 812/);
  assert.match(table.textContent, /AI 호출3번/);
});

test("ask with its own mission says so, and a schema miss is explained", async () => {
  const fail = err("invalid_model_output", 502);
  const t = setup("ask", [["/v1/reflect", () => { throw fail; }]]);
  t.view.draw(); await dom.flush();
  t.h.$("#chips input").value = "질문"; t.h.$("#chips [data-opt=mission]").value = "짧게 답한다";
  dom.fire(t.h.$("#chips form"), "submit"); await dom.flush();
  assert.equal(body(sentTo(t, "/v1/reflect")[0]).mission, "짧게 답한다");
  assert.match(t.h.$("#table .callout").textContent, /답 모양에 맞는 답/);
});

test("search 살펴보기 sends explain with the 자세히 options and shows lanes, ranks and scores; JSON copies", async () => {
  const hit = { id: "m4", collection: "memories", kind: "lesson", status: "active", text: "배포 전에는 스테이징에서 확인한다.", score: 0.82, lanes: ["keyword", "graph"],
    occurred: { from: "2026-10-20T00:00:00+09:00", to: "2026-10-21T00:00:00+09:00" }, created_at: "2026-10-22T03:00:00Z",
    explain: { lane_ranks: { keyword: 1, graph: 2 }, fused_score: 0.0245, rerank_score: 0.82, graph_hops: 2, recency: 0.875, outcome: 1.2 }, entities: [{ id: "e1", name: "Orbit", type: "project" }],
    source_traces: [{ excerpt: "스테이징 먼저 보자" }], tags: ["배포"] };
  const t = setup("search", [["/v1/search", () => ({ authority: "data_only_untrusted", context_id: "c1", items: [hit],
    explain: { lanes: ["exact", "keyword", "vector", "graph"], reranked: true, min_score_dropped: 3, lane_ms: { keyword: 3, vector: 12, rerank: 40 }, dropped: { semantic: 2, prune: 0 }, lane_weights: { keyword: 2 },
      lane_hits: { keyword: [{ collection: "memories", id: "m4", revision: 1 }, { collection: "memories", id: "m9", revision: 2 }] }, time_window: { phrase: "지난주", from: "2026-10-25T15:00:00Z", to: "2026-11-01T15:00:00Z" } } })],
    ["/v1/memories/m4?", () => memory("m4")], ["/v1/memories/m9?", () => memory("m9")]]);
  let copied = "";
  t.view.draw();
  t.h.$("#table").ownerDocument.defaultView = { navigator: { clipboard: { writeText: async (x) => { copied = x; } } } }; // the button's own window

  assert.match(t.h.$("#table").textContent, /에이전트가 기억을 찾을 때와 똑같이/);
  const form = t.h.$("#chips form");
  t.h.$("#chips input").value = "지난주 배포";
  form.querySelector("[data-opt=limit]").value = "20"; form.querySelector("[data-opt=depth]").value = "3"; form.querySelector("[data-opt=min]").value = "0.2";
  form.querySelector("[data-opt=at]").value = "2026-11-03T09:00";
  form.querySelector("[data-opt=tags]").value = "배포, 운영, 배포"; form.querySelector("[data-opt=match]").value = "all_strict";
  form.querySelector("[data-opt=groups]").value = "a+b | -c\n\n d ";
  for (const k of ["part:sources", "part:entities", "observations", "kind:lesson", "kind:decision", "prune"]) form.querySelector(`[data-opt="${k}"]`).checked = true;
  const set = (k, v) => { form.querySelector(`[data-opt="${k}"]`).value = v; };
  set("rerank", "model"); set("budget", "3000"); set("w:keyword", "2"); set("w:vector", "9"); set("floor:semantic", "0.3"); set("tokens:sources", "500");
  set("from", "2026-10-01T00:00"); set("to", "2026-10-08T00:00");
  dom.fire(form, "submit"); await dom.flush();
  const sent = body(sentTo(t, "/v1/search")[0]);
  assert.equal(sent.explain, true);
  assert.equal(sent.query_time, new Date("2026-11-03T09:00").toISOString());
  assert.deepEqual(sent.temporal_window, { from: new Date("2026-10-01T00:00").toISOString(), to: new Date("2026-10-08T00:00").toISOString() });
  delete sent.query_time; delete sent.temporal_window;
  assert.deepEqual(sent, { scope: SCOPE, query: "지난주 배포", explain: true, limit: 20, graph_depth: 3, min_score: 0.2, kinds: ["decision", "lesson"],
    filters: { tags: ["배포", "운영"], tags_match: "all_strict", tag_groups: [[{ tags: ["a", "b"], match: "all_strict" }, { tags: ["c"], not: true }], [{ tags: ["d"] }]] },
    budget_tokens: 3000, token_encoding: "cl100k_base", lane_weights: { keyword: 2 }, min_scores: { semantic: 0.3 },
    include: ["sources", "entities"], include_options: { sources: { max_tokens: 500 } }, prefer_observations: true, rerank: "model", rerank_options: { prune: true } },
    "out-of-range weights are left to the server default");
  const table = t.h.$("#table");
  assert.match(table.textContent, /같은 문구, 단어, 비슷한 뜻, 이어진 기억/);
  assert.match(table.textContent, /‘지난주’/);
  assert.match(table.textContent, /AI로 다시 매겼어요/);
  assert.match(table.textContent, /점수가 낮아 뺀 기억3개/);
  assert.match(table.textContent, /걸린 시간순서 다시 매김 40ms · 비슷한 뜻 12ms · 단어 3ms/, "lane times, slowest first");
  assert.match(table.textContent, /방법마다 무게단어 ×2/);
  assert.match(table.textContent, /단계별 제외비슷한 뜻 하한 2/);
  // each lane's own list, read only when opened; m9 was left out of the results
  const lane = table.querySelector("details.insight-more");
  assert.match(lane.textContent, /단어 2개/);
  assert.equal(sentTo(t, "/v1/memories/m9?").length, 0);
  lane.open = true; dom.fire(lane, "toggle"); await dom.flush();
  assert.match(lane.textContent, /기억 m9/); assert.match(lane.textContent, /결과에 남은 것 1개/);
  const row = table.querySelector(".original-row");
  assert.match(row.textContent, /단어 1위 · 이어진 기억 2위 · 2단계 이어짐 · 일어난 때/);
  row.click(); await dom.flush();
  const d = t.h.$("#detail");
  assert.match(d.textContent, /1위로 찾은 기억/);
  assert.match(d.textContent, /합친 점수0\.0245/);
  assert.match(d.textContent, /AI가 다시 매긴 점수0\.82/);
  assert.match(d.textContent, /최근성 가산×0\.88/); assert.match(d.textContent, /결과 가중치×1\.20/);
  assert.match(d.textContent, /기억한 때/);
  assert.match(d.textContent, /Orbit/);
  assert.match(d.textContent, /스테이징 먼저 보자/);
  d.querySelector(".dg-item").click(); await dom.flush();
  assert.equal(t.opened[0].id, "m4");
  [...table.querySelectorAll("button")].find((b) => b.textContent === "JSON 복사").click(); await dom.flush();
  assert.equal(JSON.parse(copied).request.query, "지난주 배포");
  assert.equal(JSON.parse(copied).response.items[0].id, "m4");
});

test("search 살펴보기 shows the split questions and the raw conversation turns as their own parts", async () => {
  const hit = { id: "m4", collection: "memories", kind: "episodic", status: "active", text: "Caroline went to the LGBTQ support group on 7 May 2023.", score: 0.7, lanes: ["vector"] };
  const t = setup("search", [["/v1/search", () => ({ authority: "data_only_untrusted", items: [hit],
    conversation: [{ id: "s1", collection: "sources", text: "Caroline: I went to a LGBTQ support group yesterday and it was so powerful.", collected_at: "2023-05-08T13:56:00Z" },
      { id: "s2", collection: "sources", summary: "Melanie: That's great, how did it feel?" }, { id: "s3", collection: "sources" }],
    explain: { lanes: ["vector"], expand: "applied", expand_queries: ["Caroline support group date", "Caroline LGBTQ group visit", 7, ""] } })]]);
  t.view.draw();
  t.h.$("#chips input").value = "When did Caroline go to the support group?"; dom.fire(t.h.$("#chips form"), "submit"); await dom.flush();
  const table = t.h.$("#table");
  assert.match(table.textContent, /질문 나눠 찾기서버 AI가 나눠서 같이 찾았어요/);
  const heads = [...table.querySelectorAll("h3")].map((n) => n.textContent);
  assert.ok(heads.includes("나눠서 찾은 질문") && heads.includes("대화 원문"), heads.join());
  const split = [...table.querySelectorAll(".insight-ask li")].map((n) => n.textContent);
  assert.deepEqual(split, ["Caroline support group date", "Caroline LGBTQ group visit"], "only non-empty strings");
  const talk = table.querySelector(".insight-conversation");
  assert.equal(talk.querySelectorAll("li").length, 2, "a turn without text is skipped");
  assert.match(talk.textContent, /사람이 확인한 기억이 아니에요/);
  assert.match(talk.textContent, /support group yesterday/); assert.match(talk.textContent, /how did it feel/);
  assert.equal(table.querySelectorAll(".original-row").length, 1, "turns are never ranked among the memories");
});

test("search 살펴보기 says why the question was not split, and draws no empty parts", async () => {
  const t = setup("search", [["/v1/search", () => ({ authority: "data_only_untrusted", items: [], explain: { lanes: ["keyword"], expand: "budget_exceeded" } })]]);
  t.view.draw();
  t.h.$("#chips input").value = "배포"; dom.fire(t.h.$("#chips form"), "submit"); await dom.flush();
  const table = t.h.$("#table");
  assert.match(table.textContent, /모델 예산을 넘어 나누지 않았어요/);
  assert.doesNotMatch(table.textContent, /나눠서 찾은 질문|대화 원문/);
});

test("search 살펴보기 sends only explain by default and says when the server refuses the options", async () => {
  const t = setup("search", [["/v1/search", () => { throw err("invalid_request", 400); }]]);
  t.view.draw();
  t.h.$("#chips input").value = "  "; dom.fire(t.h.$("#chips form"), "submit"); await dom.flush();
  assert.equal(sentTo(t, "/v1/search").length, 0);
  t.h.$("#chips input").value = "배포"; dom.fire(t.h.$("#chips form"), "submit"); await dom.flush();
  assert.deepEqual(body(sentTo(t, "/v1/search")[0]), { scope: SCOPE, query: "배포", explain: true });
  assert.match(t.h.$("#table .callout").textContent, /찾을 수 없는 설정/);
});

test("a 묶음 and a 묶음 목록 hold their own fields, and a nested answer draws as nested rows with model text kept as data", async () => {
  const t = setup("ask", [["/v1/reflect", () => ({ answer: "{}", cited_memory_ids: [], directives_applied: [], retrieved: [],
    structured_answer: { "담당": { "이름": "<b>민지</b>", "확인함": true }, "단계": [{ "할 일": "스테이징 확인" }, { "할 일": "배포" }] },
    tool_trace: [null, "x", { tool: "<img src=x>", args: "bad", ids: "m1" }] })]]);
  t.view.draw(); await dom.flush();
  const form = t.h.$("#chips form");
  const top = () => [...form.querySelectorAll("button")].find((b) => b.textContent === "칸 추가");
  top().click(); top().click();
  let items = form.querySelectorAll(".insight-shape-list > .insight-shape-rows > .insight-shape-item");
  const set = (item, name, type) => {
    const n = item.querySelector("input:not([type])"); n.value = name; dom.fire(n, "input");
    const k = item.querySelector("select"); k.value = type; dom.fire(k, "change");
  };
  set(items[0], "담당", "object"); set(form.querySelectorAll(".insight-shape-list > .insight-shape-rows > .insight-shape-item")[1], "단계", "objects");
  items = form.querySelectorAll(".insight-shape-list > .insight-shape-rows > .insight-shape-item");
  assert.equal(items[0].querySelectorAll(".insight-shape-sub select option").length, 0, "a new 묶음 starts empty");
  for (const [n, item] of [...items].entries()) {
    [...item.querySelectorAll("button")].find((b) => b.textContent === "안에 칸 추가").click();
    const inner = form.querySelectorAll(".insight-shape-list > .insight-shape-rows > .insight-shape-item")[n].querySelector(".insight-shape-sub .insight-shape-item");
    assert.ok(![...inner.querySelectorAll("option")].some((o) => o.value === "object" || o.value === "objects"), "no group inside a group");
    set(inner, n ? "할 일" : "이름", "string");
  }
  t.h.$("#chips input").value = "누가 배포해?"; dom.fire(form, "submit"); await dom.flush();
  const inner = (name) => ({ type: "object", properties: { [name]: { type: "string" } }, required: [name], additionalProperties: false });
  assert.deepEqual(body(sentTo(t, "/v1/reflect")[0]).response_schema, { type: "object", additionalProperties: false, required: ["담당", "단계"],
    properties: { "담당": inner("이름"), "단계": { type: "array", items: inner("할 일") } } });
  const ans = t.h.$("#table .insight-answer");
  assert.equal(ans.querySelectorAll("b").length + ans.querySelectorAll("img").length, 0, "model text is never markup");
  assert.match(ans.textContent, /<b>민지<\/b>/);
  assert.equal(ans.querySelectorAll(".insight-data-list > li").length, 2);
  assert.match(ans.textContent, /확인함예/);
  const step = t.h.$("#table .insight-step");
  assert.equal(t.h.$("#table").querySelectorAll(".insight-step").length, 1, "steps that are not objects are skipped");
  assert.match(step.textContent, /<img src=x>/); assert.equal(t.h.$("#table").querySelectorAll("img").length, 0);
});

test("an entity's summary: a model draft waits for a person; approving a draft whose memories changed says so (409)", async () => {
  const draft = { version: 1, status: "draft", text: "Alex는 배포를 맡아요.", citations: [{ collection: "memories", id: "m5", revision: 1 }], at: "2026-10-20T00:00:00Z" };
  let current = entity({ proposed: false, summary_draft: draft }), approve = err("revision_conflict", 409);
  const t = setup("entities", [["/v1/entities?", () => ({ items: [current] })],
    ["/v1/entities/e1?", () => ({ ...current, memories: [], related: [], timeline: [] })],
    ["/v1/entities/e1/summary", () => { current = entity({ proposed: false, summary_draft: draft }); return current; }],
    ["/v1/entities/e1", (_, o) => { const b = JSON.parse(o.body); if (b.summary_review === "approve" && approve) throw approve;
      current = entity({ proposed: false, ...(b.summary_review === "approve" ? { summary: { ...draft, status: "active" } } : {}) }); return current; }]]);
  t.view.draw(); await dom.flush();
  assert.match(t.h.$("#table").textContent, /요약 초안/, "the list marks a waiting draft");
  t.h.$("#table .original-row").click(); await dom.flush();
  const d = () => t.h.$("#detail");
  assert.match(d().textContent, /AI가 쓴 요약 초안이에요. 승인하기 전에는 쓰이지 않아요/);
  assert.match(d().textContent, /Alex는 배포를 맡아요/); assert.match(d().textContent, /근거 기억 1개/);
  dom.buttons(d(), "승인")[0].click(); await dom.flush();
  assert.deepEqual(body(t.calls.filter(([, o]) => o?.method === "PATCH").at(-1)), { scope: SCOPE, summary_review: "approve" });
  assert.match(t.notices.at(-1), /근거 기억이 그사이 바뀌어서 이 초안은 승인할 수 없어요/);
  dom.buttons(d(), "버리기")[0].click(); await dom.flush();
  assert.deepEqual(body(t.calls.filter(([, o]) => o?.method === "PATCH").at(-1)), { scope: SCOPE, summary_review: "reject" });
  assert.match(d().textContent, /아직 요약이 없어요/);
  dom.buttons(d(), "AI에게 요약 초안 쓰게 하기")[0].click(); await dom.flush();
  assert.deepEqual(body(t.calls.find(([p]) => p === "/v1/entities/e1/summary")), { scope: SCOPE });
  assert.equal(t.notices.at(-1), "요약 초안을 만들었어요. 읽어 보고 승인해 주세요.");
  approve = null;
  dom.buttons(d(), "승인")[0].click(); await dom.flush();
  assert.match(d().textContent, /승인 · 근거 기억 1개/);
  assert.equal(dom.buttons(d(), "새 요약 초안 만들기").length, 1);

  const agent = setup("entities", [["/v1/entities?", () => ({ items: [entity({ summary_draft: draft })] })], ["/v1/entities/e1?", () => ({ ...entity({ summary_draft: draft }), memories: [], related: [], timeline: [] })]], { role: "agent" });
  agent.view.draw(); await dom.flush();
  agent.h.$("#table .original-row").click(); await dom.flush();
  assert.equal(dom.buttons(agent.h.$("#detail"), "승인").length, 0, "only a person decides");
  assert.match(agent.h.$("#detail").textContent, /승인과 버리기는 사람만 할 수 있어요/);
});

test("asking for an entity summary without a server model says what to set up", async () => {
  const t = setup("entities", [["/v1/entities?", () => ({ items: [entity({ proposed: false })] })], ["/v1/entities/e1?", () => ({ ...entity({ proposed: false }), memories: [], related: [], timeline: [] })],
    ["/v1/entities/e1/summary", () => { throw err("model_not_configured", 503); }]]);
  t.view.draw(); await dom.flush();
  t.h.$("#table .original-row").click(); await dom.flush();
  dom.buttons(t.h.$("#detail"), "AI에게 요약 초안 쓰게 하기")[0].click(); await dom.flush();
  assert.match(t.notices.at(-1), /서버에 요약을 쓸 모델이 없어요/);
});

test("ask sends filters, budget, fact types and disposition; shows tokens in and out, step time and evidence by kind; an answer becomes a rule only through the form", async () => {
  const t = setup("ask", [["/v1/reflect", () => ({ answer: "스테이징에서 결제를 끝까지 눌러 봐요.", cited_memory_ids: ["m4"], directives_applied: [],
    retrieved: [{ id: "m4", kind: "lesson", score: 0.9 }, { id: "o1", kind: "observation", score: 0.5 }, { id: "m7", kind: "lesson", score: 0.4 }],
    tool_trace: [{ tool: "recall", args: { query: "배포" }, ids: ["m4"], ms: 37 }], iterations: 2, graph_nodes: 2, usage: { input_tokens: 2140, output_tokens: 188, total_tokens: 2328 } })],
    ["/v1/memories/", (p) => memory(p.split("/")[3].split("?")[0])], ["/v1/directives", (p, o) => ({ id: "d5", revision: 1, ...JSON.parse(o.body) })]]);
  t.view.draw();
  const form = t.h.$("#chips form"), set = (k, v) => { form.querySelector(`[data-opt="${k}"]`).value = v; };
  t.h.$("#chips input").value = "배포 전에 뭐 해?";
  set("budget", "high"); set("tags", "배포"); set("match", "all_strict"); set("skepticism", "4"); set("empathy", "9");
  for (const k of ["fact:memory", "nomodels", "alldirectives"]) form.querySelector(`[data-opt="${k}"]`).checked = true;
  dom.fire(form, "submit"); await dom.flush();
  assert.deepEqual(body(sentTo(t, "/v1/reflect")[0]), { scope: SCOPE, query: "배포 전에 뭐 해?", budget: "high", fact_types: ["memory"], tags: ["배포"], tags_match: "all_strict",
    exclude_mental_models: true, disposition: { skepticism: 4 }, apply_all_directives: true }, "an out-of-range disposition stays the space's");
  const table = t.h.$("#table");
  assert.match(table.textContent, /보낸 토큰2140/); assert.match(table.textContent, /받은 토큰188/);
  assert.match(table.textContent, /관계를 따라가 찾은 기억 2개/);
  assert.match(table.querySelector(".insight-step").textContent, /1개 찾음 · 37ms/);
  // evidence tabs: the count only next to the active tab
  const tabs = () => [...table.querySelectorAll(".insight-seg .tm-seg-tab")];
  assert.deepEqual(tabs().map((b) => b.textContent), ["전체3", "교훈", "정리된 관찰"]);
  tabs()[2].click(); await dom.flush();
  assert.deepEqual(tabs().map((b) => b.textContent), ["전체", "교훈", "정리된 관찰1"]);
  assert.equal(t.h.$("#table").querySelectorAll(".dg-item").length, 3, "the cited memory, the step's find and the one observation");
  // 규칙으로 만들기 opens a prefilled form; nothing is written until the person saves
  [...t.h.$("#table").querySelectorAll("button")].find((b) => b.textContent === "규칙으로 만들기").click(); await dom.flush();
  assert.equal(sentTo(t, "/v1/directives").length, 0);
  const d = t.h.$("#detail");
  assert.match(d.textContent, /AI 답에서 가져온 초안/);
  assert.equal(d.querySelector("textarea").value, "스테이징에서 결제를 끝까지 눌러 봐요.");
  dom.fire(d.querySelector("form"), "submit"); await dom.flush();
  assert.equal(body(sentTo(t, "/v1/directives")[0]).rule, "스테이징에서 결제를 끝까지 눌러 봐요.");
  assert.match(t.h.$("#detail").textContent, /근거 기억을 눌러 확인하세요/);
});

test("ask shows the prompt only when asked, and the 답 모양 JSON tab sends its own schema or refuses bad JSON", async () => {
  const t = setup("ask", [["/v1/reflect", () => ({ answer: "", cited_memory_ids: [], directives_applied: [], retrieved: [], tool_trace: [], prompt: { system: "규칙을 지켜요", user: "질문: 배포" } })]]);
  t.view.draw();
  const form = t.h.$("#chips form");
  t.h.$("#chips input").value = "배포";
  form.querySelector('[data-opt="promptonly"]').checked = true;
  [...form.querySelectorAll(".tm-seg-tab")].find((b) => b.textContent === "JSON 코드").click();
  const code = form.querySelector(".insight-code");
  assert.equal(code.value, "{}");
  code.value = "[1"; dom.fire(form, "submit"); await dom.flush();
  assert.equal(sentTo(t, "/v1/reflect").length, 0, "bad JSON is not sent");
  code.value = '{"type":"object","properties":{"답":{"type":"string"}}}'; dom.fire(form, "submit"); await dom.flush();
  const sent = body(sentTo(t, "/v1/reflect")[0]);
  assert.equal(sent.prompt_only, true);
  assert.deepEqual(sent.response_schema, { type: "object", properties: { 답: { type: "string" } } });
  assert.match(t.h.$("#table").textContent, /AI에 보내지 않았어요/);
  assert.match(t.h.$("#table").textContent, /질문: 배포/);
});

test("ask can leave out picked topics (exclude_mental_model_ids)", async () => {
  const t = setup("ask", [["/v1/mental-models?", () => ({ items: [{ id: "mm1", title: "배포 정리" }, { id: "mm2", query: "휴가 규칙?" }] })], ["/v1/reflect", () => ({ answer: "네", cited_memory_ids: [], retrieved: [] })]]);
  t.view.draw(); await dom.flush();
  const form = t.h.$("#chips form");
  assert.match(form.textContent, /읽지 않을 생각 정리/); assert.match(form.textContent, /휴가 규칙\?/);
  t.h.$("#chips input").value = "배포?";
  form.querySelector('[data-opt="skip:mm2"]').checked = true;
  dom.fire(form, "submit"); await dom.flush();
  assert.deepEqual(body(sentTo(t, "/v1/reflect")[0]).exclude_mental_model_ids, ["mm2"]);
});

test("search and ask can attach the kept original files: each shows its kind and size and downloads through the artifact route", async () => {
  const file = { id: "a1", source_id: "src1", kind: "pdf", mime: "application/pdf", bytes: 2048 };
  const t = setup("search", [["/v1/search", () => ({ items: [{ id: "m4", collection: "memories", kind: "lesson", text: "배포 메모", score: 0.5, attachments: [file] }] })],
    ["/v1/sources/src1/artifacts/a1?", (_, o) => { assert.equal(o.binary, true); return new Uint8Array([1]); }], ["/v1/memories/m4?", () => memory("m4")]]);
  t.view.draw();
  const form = t.h.$("#chips form");
  t.h.$("#chips input").value = "배포";
  form.querySelector('[data-opt="attach"]').checked = true;
  dom.fire(form, "submit"); await dom.flush();
  assert.deepEqual(body(sentTo(t, "/v1/search")[0]).include, ["attachments"]);
  t.h.$("#table .original-row").click(); await dom.flush();
  const d = t.h.$("#detail");
  assert.match(d.textContent, /보관된 원본 파일PDFapplication\/pdf · 2048 B내려받기/);
  [...d.querySelectorAll("button")].find((b) => b.textContent === "내려받기").click(); await dom.flush();
  assert.deepEqual(t.saved[0].slice(0, 1), ["a1.pdf"]);
  assert.ok(t.calls.some(([p]) => p.startsWith("/v1/sources/src1/artifacts/a1?") && p.includes("workspace_id=w")));

  const a = setup("ask", [["/v1/reflect", () => ({ answer: "답", cited_memory_ids: ["m4"], retrieved: [{ id: "m4", kind: "lesson", attachments: [file] }, { id: "m5", kind: "lesson" }] })],
    ["/v1/memories/", () => memory("m4")]]);
  a.view.draw();
  a.h.$("#chips input").value = "배포?";
  a.h.$("#chips form").querySelector('[data-opt="attach"]').checked = true;
  dom.fire(a.h.$("#chips form"), "submit"); await dom.flush();
  assert.deepEqual(body(sentTo(a, "/v1/reflect")[0]).include, { attachments: true });
  assert.match(a.h.$("#table").textContent, /보관된 원본 파일PDF/);
});
