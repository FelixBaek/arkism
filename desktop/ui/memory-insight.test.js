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
  for (const f of ["./digests.js", "./memory-insight.js"]) { delete require.cache[require.resolve(f)]; require(f); } // memory rows come from digests.js
  const h = dom.helpers(document);
  for (const id of ["app", "chips", "stats", "table", "detail"]) { const n = h.el("div"); n.id = id; document.body.append(n); }
  const state = { view: "memories", memTab: tab, server: "s", token: "t", scope: { ...SCOPE }, caps: { role } };
  const calls = [], notices = [], opened = [], asked = [];
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
  const view = window.ArkismMemoryInsight({ state, $: h.$, el: h.el, raw: h.raw, icon: h.icon, button: h.button, request, query, detailShell, section, facts, callout,
    openDetail: () => h.$("#app").classList.add("detail-open"), notice: (m) => notices.push(m), openRecord: (r) => opened.push(r) }, tab);
  return { view, state, calls, notices, opened, asked, h };
}
const body = (call) => JSON.parse(call[1].body);
const err = (code, status) => { const e = new Error(code); e.status = status; return e; };
const memory = (id, o = {}) => ({ id, text: `기억 ${id}`, status: "active", revision: 1, ...o });

test("ask sends the question, shows the answer and opens a cited memory", async () => {
  const t = setup("ask", [["/v1/reflect", () => ({ answer: "스테이징에서 먼저 확인해요.", cited_memory_ids: ["m4", "gone"], directives_applied: ["d1"], retrieved: [{ id: "m4" }] })],
    ["/v1/memories/m4?", () => memory("m4")], ["/v1/memories/gone?", () => { throw err("not_found", 404); }]]);
  t.view.draw();
  assert.match(t.h.$("#table").textContent, /승인된 기억만 보고 답해요/);
  const input = t.h.$("#chips input");
  input.value = "  "; dom.fire(t.h.$("#chips form"), "submit"); await dom.flush();
  assert.equal(t.calls.length, 0, "an empty question is not sent");
  input.value = "배포 전에 뭐 해?"; t.h.$("#chips input[type=number]").value = "5";
  dom.fire(t.h.$("#chips form"), "submit"); await dom.flush();
  assert.deepEqual(body(t.calls[0]), { scope: SCOPE, query: "배포 전에 뭐 해?", limit: 5 });
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
  assert.equal(body(t.calls[0]).limit, undefined, "the server picks how many memories to use");
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
    ["/v1/entities/e1?", () => ({ ...entity(), memories: [{ id: "m5", snippet: "Alex는 목록을 좋아해요" }], related: [{ id: "e2", name: "Orbit", type: "project", weight: 1 }], timeline: [] })],
    ["/v1/entities/e1/merge", () => entity({ id: "e2" })], ["/v1/entities/e1", () => entity({ proposed: false })], ["/v1/memories/m5?", () => memory("m5")]], { answers: ["Alexander"] });
  t.view.draw(); await dom.flush();
  assert.match(t.h.$("#table").textContent, /사람 · 기억 2개/);
  assert.match(t.h.$("#table").textContent, /확인 필요/);
  t.h.$("#table .original-row").click(); await dom.flush();
  assert.match(t.h.$("#detail").textContent, /Alex는 목록을 좋아해요/);
  assert.match(t.h.$("#detail").textContent, /알렉스/);
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
