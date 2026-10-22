// 스킬 › 교훈으로 개선안 만들기 (skill-generate.js): pick lessons, hash the comparison files, journal, send once.
const test = require("node:test");
const assert = require("node:assert/strict");
const { createHash } = require("node:crypto");
const { createDocument, house, flush } = require("./testdata/views-dom.js");

// Real crypto.subtle resolves on the thread pool, so under load it can land after the test looks; hash in-process instead.
Object.defineProperty(globalThis, "crypto", { configurable: true, value: { randomUUID: () => require("node:crypto").randomUUID(), subtle: { digest: async (_, data) => { const h = createHash("sha256").update(Buffer.from(data instanceof ArrayBuffer ? new Uint8Array(data) : data)).digest(); return h.buffer.slice(h.byteOffset, h.byteOffset + h.length); } } } });
globalThis.window = globalThis;
globalThis.document = createDocument();
require("./skill-generate.js");

const store = new Map();
globalThis.localStorage = { getItem: k => store.has(k) ? store.get(k) : null, setItem: (k, v) => store.set(k, String(v)), removeItem: k => store.delete(k) };

const scope = { workspace_id: "w" };
const sk = { id: "sk1", source_path: "skills/a/SKILL.md" };
const btn = (root, label) => root.querySelectorAll("button").find(b => b.textContent.includes(label));
const text = n => n.textContent;
const idle = () => flush(16);
const refs = (n) => Array.from({ length: n }, (_, i) => ({ collection: "memory_candidates", id: `m${i}`, revision: 1 }));
const key = () => "arkism.skill-generation.v1." + createHash("sha256").update(JSON.stringify(["s", "t", "w", "", "", "", "sk1"])).digest("hex");
const hex = "c".repeat(64);
const pendingInput = (extra = {}) => ({ scope: { workspace_id: "w" }, event_id: "ev-1", skill_id: "sk1", path: sk.source_path, lessons: refs(3),
  comparison_plan: { baseline_version: "v1", dataset_sha256: hex, environment_sha256: hex, min_score_gain: 0.1, max_cost: 1, max_duration_ms: 10 }, ...extra });
const file = (body, size) => ({ size: size ?? body.length, arrayBuffer: async () => new TextEncoder().encode(body).buffer });

function setup(routes = {}, caps = { role: "human" }) {
  const doc = createDocument(); globalThis.document = doc;
  const h = house(doc), state = { server: "s", token: "t", scope: { ...scope }, view: "skills", selected: "skill:sk1", caps };
  let reloads = 0;
  const calls = [];
  const r = {
    skill: () => ({ id: "sk1", scope, revision: 2, active_version: "v1" }),
    reflections: (subject) => ({ authority: "data_only_untrusted", items: [
      { scope, subject, interpretation: "배포 전 묻기", status: "candidate", candidate_refs: refs(4) },
      { scope, subject, interpretation: "적음", status: "candidate", candidate_refs: refs(2) },
    ] }),
    generate: () => ({ record: { skill_id: "sk1", scope, status: "candidate", revision: 1, id: "v9" } }),
    ...routes,
  };
  const request = async (path, opt) => {
    calls.push([path, opt]);
    if (path.startsWith("/v1/reflections")) return r.reflections(decodeURIComponent(path.split("subject=")[1] || ""));
    if (path === "/v1/skills/generate") return r.generate(JSON.parse(opt.body));
    return r.skill();
  };
  const make = window.ArkismSkillGenerate({ state, el: h.el, button: h.button, request, query: (q) => q ? `subject=${encodeURIComponent(q.subject)}` : "q=1", reload: async () => { reloads++; } });
  const panel = make(sk);
  doc.body.append(panel);
  return { doc, h, panel, state, calls, make, reloads: () => reloads };
}
const open = async p => { p.open = true; await p.fire("toggle"); await idle(); };

async function fill(panel, { check = 3 } = {}) {
  const [search, form] = panel.querySelectorAll("form");
  await search.fire("submit"); await flush();
  const select = panel.querySelector("select"); select.value = "0"; await select.fire("change");
  const boxes = panel.querySelectorAll("fieldset input");
  for (const b of boxes.slice(0, check)) { b.checked = true; await b.fire("change"); }
  const inputs = form.querySelectorAll("input");
  return { form, inputs };
}

test("not a human account, a broken baseline and a collapsed panel", async () => {
  store.clear();
  const a = setup({}, null); await open(a.panel);
  assert.match(text(a.panel), /관리 계정에서/);
  const b = setup({ skill: () => ({ id: "sk1", scope, revision: 0 }) }); await open(b.panel);
  assert.match(text(b.panel), /기준 버전을 읽지 못했어요/);
  b.panel.open = false; await b.panel.fire("toggle");
  assert.equal(b.panel.querySelector("p"), null);
});

test("lessons search, selection rules and a sent request", async () => {
  store.clear();
  const ctx = setup();
  await open(ctx.panel);
  const [search, form] = ctx.panel.querySelectorAll("form");
  const topic = search.querySelector("input");
  topic.value = " "; await search.fire("submit");
  assert.match(text(ctx.panel), /128바이트 이내로/);
  topic.value = "skill:sk1"; await topic.fire("input");
  const { inputs } = await fill(ctx.panel, { check: 2 });
  assert.match(text(ctx.panel), /관측 4 · 판 1/);
  await form.fire("submit");
  assert.match(text(ctx.panel), /3~16개 선택해 주세요/);
  const select = ctx.panel.querySelector("select"); select.value = ""; await select.fire("change");
  select.value = "0"; await select.fire("change");
  for (const b of ctx.panel.querySelectorAll("fieldset input").slice(0, 3)) { b.checked = true; await b.fire("change"); }
  const [dataset, environment, gain, cost, duration] = inputs;
  gain.value = "2"; await form.fire("submit");
  assert.equal(ctx.calls.filter(([p]) => p === "/v1/skills/generate").length, 0);
  gain.value = "0.1"; cost.value = "1"; duration.value = "10";
  dataset.files = [file("{}")]; environment.files = [file("{\"a\":1}")];
  await form.fire("submit"); await idle();
  const sent = ctx.calls.find(([p]) => p === "/v1/skills/generate");
  assert.equal(JSON.parse(sent[1].body).lessons.length, 3);
  assert.equal(store.size, 0);
  assert.equal(ctx.reloads(), 1);
  assert.match(text(ctx.panel), /개선안을 저장했어요/);
});

test("bad files, changed baselines and bad lesson pages", async () => {
  store.clear();
  let skill = { id: "sk1", scope, revision: 2, active_version: "v1" };
  const ctx = setup({ skill: () => skill, reflections: () => ({ authority: "x" }) });
  await open(ctx.panel);
  const [search] = ctx.panel.querySelectorAll("form");
  await search.fire("submit"); await flush();
  assert.match(text(ctx.panel), /교훈을 읽지 못했어요/);
  const none = setup({ reflections: () => ({ authority: "data_only_untrusted", items: [] }) });
  await open(none.panel); await none.panel.querySelector("form").fire("submit"); await flush();
  assert.match(text(none.panel), /검토 대기 교훈이 없어요/);

  const big = setup(); await open(big.panel);
  const { form, inputs } = await fill(big.panel);
  inputs[2].value = "0.5"; inputs[3].value = "0"; inputs[4].value = "5";
  inputs[0].files = [file("{}", 9 * 1024 * 1024)]; inputs[1].files = [file("{}")];
  await form.fire("submit"); await idle();
  assert.match(text(big.panel), /생성을 시작하지 못했어요/);
  const moved = setup({ skill: () => skill }); await open(moved.panel);
  const m = await fill(moved.panel);
  m.inputs[2].value = "0.5"; m.inputs[3].value = "0"; m.inputs[4].value = "5";
  m.inputs[0].files = [file("{}")]; m.inputs[1].files = [file("{}")];
  skill = { ...skill, revision: 3 };
  await m.form.fire("submit"); await idle();
  assert.match(text(moved.panel), /생성을 시작하지 못했어요/);
});

test("a pending request is confirmed again; rejected ones can be reset", async () => {
  store.clear();
  store.set(key(), JSON.stringify(pendingInput()));
  let fail = "boom";
  const ctx = setup({ generate: () => { if (fail) throw new Error(fail); return { record: { skill_id: "sk1", scope, status: "active", revision: 1, id: "v9" } }; } });
  await open(ctx.panel);
  assert.match(text(ctx.panel), /처리 결과를 확인할 생성 요청이 있어요/);
  await btn(ctx.panel, "생성 결과 다시 확인").click(); await idle();
  assert.match(text(ctx.panel), /같은 요청으로 다시 확인하세요/);
  fail = "access_denied";
  await btn(ctx.panel, "생성 결과 다시 확인").click(); await idle();
  await btn(ctx.panel, "생성 결과 다시 확인").click(); await idle();
  assert.equal(ctx.panel.querySelectorAll(".generation-reset").length, 1);
  const stale = btn(ctx.panel, "생성 결과 다시 확인");
  // a second panel for the same skill confirms it; the first one's retry then only reloads
  const other = ctx.make(sk); ctx.doc.body.append(other);
  fail = null;
  await open(other);
  await btn(other, "생성 결과 다시 확인").click(); await idle();
  assert.equal(store.size, 0);
  const before = ctx.reloads();
  await stale.click(); await idle();
  assert.equal(ctx.reloads(), before + 1);
  // reset after a rejection
  store.set(key(), JSON.stringify(pendingInput()));
  fail = "not_found";
  const r = setup({ generate: () => { throw new Error(fail); } }); await open(r.panel);
  await btn(r.panel, "생성 결과 다시 확인").click(); await idle();
  const reset = btn(r.panel, "입력 다시 선택");
  const real = localStorage.removeItem; localStorage.removeItem = () => { throw new Error("locked"); };
  await reset.click();
  localStorage.removeItem = real;
  assert.match(text(r.panel), /보류 요청을 지우지 못했어요/);
  await reset.click(); await idle();
  assert.ok(r.panel.querySelector("form"));
});

test("broken journal entries block new requests", async () => {
  const bad = [
    "x".repeat(40000),
    JSON.stringify({ ...pendingInput(), extra: 1 }),
    JSON.stringify(pendingInput({ lessons: refs(2) })),
    JSON.stringify(pendingInput({ comparison_plan: { ...pendingInput().comparison_plan, min_score_gain: 2 } })),
  ];
  for (const raw of bad) {
    store.clear(); store.set(key(), raw);
    const ctx = setup(); await open(ctx.panel);
    assert.match(text(ctx.panel), /보류 요청을 읽지 못했어요/);
  }
  store.clear();
});
