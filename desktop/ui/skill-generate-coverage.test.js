// 스킬 › 교훈으로 개선안 만들기 (skill-generate.js): lessons and the eval profile fill themselves, journal, send once.
const test = require("node:test");
const assert = require("node:assert/strict");
const { createHash } = require("node:crypto");
require("./keep.js"); // the shared stale-reply and redraw guards the screens use
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
const profile = (extra = {}) => ({ id: "fixed", name: "고정 과제", cases: 4, dataset_sha256: "d".repeat(64), environment_sha256: "e".repeat(64),
  default_plan: { min_score_gain: 0.05, max_cost: 1e6, max_duration_ms: 240000 }, ...extra });

function setup(routes = {}, caps = { role: "human" }, profiles = () => ({ authority: "data_only_untrusted", items: [profile()] })) {
  const doc = createDocument(); globalThis.document = doc;
  const h = house(doc), state = { server: "s", token: "t", scope: { ...scope }, view: "skills", selected: "skill:sk1", caps, defaults: { ssh_host: "box" } };
  let reloads = 0;
  const calls = [], invokes = [];
  const r = {
    skill: () => ({ id: "sk1", scope, revision: 2, active_version: "v1" }),
    reflections: (subject) => ({ authority: "data_only_untrusted", items: [
      { scope, subject, interpretation: "적음", status: "candidate", candidate_refs: refs(2) },
      { scope, subject, interpretation: "배포 전 묻기", status: "candidate", candidate_refs: refs(4) },
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
  const tauri = profiles && { invoke: async (cmd, arg) => { invokes.push([cmd, arg]); return profiles(); } };
  const make = window.ArkismSkillGenerate({ state, el: h.el, button: h.button, request, query: (q) => q ? `subject=${encodeURIComponent(q.subject)}` : "q=1", reload: async () => { reloads++; }, tauri });
  const panel = make(sk);
  doc.body.append(panel);
  return { doc, h, panel, state, calls, invokes, make, reloads: () => reloads };
}
const open = async p => { p.open = true; await p.fire("toggle"); await idle(); };
const sent = ctx => ctx.calls.filter(([p]) => p === "/v1/skills/generate").map(([, o]) => JSON.parse(o.body));

test("not a human account, a broken baseline and a collapsed panel", async () => {
  store.clear();
  const a = setup({}, null); await open(a.panel);
  assert.match(text(a.panel), /관리 계정에서/);
  const b = setup({ skill: () => ({ id: "sk1", scope, revision: 0 }) }); await open(b.panel);
  assert.match(text(b.panel), /기준 버전을 읽지 못했어요/);
  b.panel.open = false; await b.panel.fire("toggle");
  assert.equal(b.panel.querySelector("p"), null);
});

test("opening fills lessons, eval set and pass rule; one press sends it", async () => {
  store.clear();
  const ctx = setup();
  await open(ctx.panel);
  assert.deepEqual(ctx.invokes[0], ["skill_execution", { host: "box", request: { scope, server: "s", action: "profiles" } }]);
  assert.ok(ctx.calls.some(([p]) => p === "/v1/reflections?subject=skill%3Ask1"));
  assert.match(text(ctx.panel), /배포 전 묻기/);
  assert.match(text(ctx.panel), /평가 과제 · 고정 과제 · 과제 4개/);
  assert.match(text(ctx.panel), /점수가 5점 이상 오르고 4분 안에 끝나면 통과 · 비용 제한 없음/);
  assert.doesNotMatch(text(ctx.panel), /밀리초|평가 과제 파일|교훈 찾기/);
  assert.equal(ctx.panel.querySelector(".generation-advanced").open, undefined);
  const boxes = ctx.panel.querySelectorAll("fieldset input");
  assert.equal(boxes.filter(b => b.checked).length, 4);
  boxes[0].checked = boxes[1].checked = false; await boxes[1].fire("change");
  await ctx.panel.querySelector("form").fire("submit");
  assert.match(text(ctx.panel), /3~16개 골라 주세요/);
  boxes[1].checked = true; await boxes[1].fire("change");
  await ctx.panel.querySelector("form").fire("submit"); await idle();
  const [input] = sent(ctx);
  assert.deepEqual(input.lessons.map(ref => ref.id), ["m1", "m2", "m3"]);
  assert.deepEqual(input.comparison_plan, { baseline_version: "v1", dataset_sha256: "d".repeat(64), environment_sha256: "e".repeat(64), min_score_gain: 0.05, max_cost: 1e6, max_duration_ms: 240000 });
  assert.equal(store.size, 0);
  assert.equal(ctx.reloads(), 1);
  assert.match(text(ctx.panel), /개선안을 저장했어요/);
});

test("advanced settings take points, minutes and an optional cost", async () => {
  store.clear();
  const many = { ...profile({ id: "second", name: "둘째", default_plan: { min_score_gain: 0.2, max_cost: 3, max_duration_ms: 90000 } }) };
  const ctx = setup({}, { role: "human" }, () => ({ authority: "data_only_untrusted", items: [profile(), many, { id: "bad id" }] }));
  await open(ctx.panel);
  const selects = ctx.panel.querySelectorAll("select");
  assert.equal(selects.length, 1);
  selects[0].value = "1"; await selects[0].fire("change");
  assert.match(text(ctx.panel), /20점 이상 오르고 1.5분 안에 끝나면 통과 · 비용 3 이하/);
  const [gain, minutes, cost] = ctx.panel.querySelectorAll(".generation-advanced input");
  gain.value = "200"; await gain.fire("input");
  assert.match(text(ctx.panel), /고급 설정의 값을 확인해 주세요/);
  await ctx.panel.querySelector("form").fire("submit");
  assert.equal(ctx.panel.querySelector(".generation-advanced").open, true);
  assert.equal(sent(ctx).length, 0);
  gain.value = "10"; minutes.value = "2"; cost.value = ""; await cost.fire("input");
  assert.match(text(ctx.panel), /10점 이상 오르고 2분 안에 끝나면 통과 · 비용 제한 없음/);
  await ctx.panel.querySelector("form").fire("submit"); await idle();
  assert.deepEqual(sent(ctx)[0].comparison_plan, { baseline_version: "v1", dataset_sha256: "d".repeat(64), environment_sha256: "e".repeat(64), min_score_gain: 0.1, max_cost: 1e6, max_duration_ms: 120000 });
});

test("several lessons can be switched", async () => {
  store.clear();
  const ctx = setup({ reflections: subject => ({ authority: "data_only_untrusted", items: [
    { scope, subject, interpretation: "첫째", status: "candidate", candidate_refs: refs(5) },
    { scope, subject, interpretation: "둘째", status: "candidate", candidate_refs: refs(3).map(ref => ({ ...ref, id: `n${ref.id}` })) },
    { scope, subject, interpretation: "끝남", status: "accepted", candidate_refs: refs(9) },
  ] }) });
  await open(ctx.panel);
  const select = ctx.panel.querySelector("select");
  assert.equal(select.querySelectorAll("option").length, 2);
  select.value = "1"; await select.fire("change");
  await ctx.panel.querySelector("form").fire("submit"); await idle();
  assert.deepEqual(sent(ctx)[0].lessons.map(ref => ref.id), ["nm0", "nm1", "nm2"]);
});

test("what cannot be filled is named instead of shown as empty fields", async () => {
  store.clear();
  const cases = [
    [setup({}, { role: "human" }, null), /데스크톱 앱에서 열어 주세요/],
    [setup({}, { role: "human" }, () => { throw new Error("ssh_unreachable"); }), /평가 서버\(box\)에 연결하지 못했어요/],
    [setup({}, { role: "human" }, () => ({ error: "x" })), /평가 설정을 보내지 않았어요/],
    [setup({}, { role: "human" }, () => ({ authority: "data_only_untrusted", items: [] })), /등록된 평가 과제가 없어요/],
    [setup({}, { role: "human" }, () => ({ authority: "data_only_untrusted", items: [profile({ default_plan: undefined })] })), /업데이트하면 자동으로 채워져요/],
    [setup({ reflections: subject => ({ authority: "data_only_untrusted", items: [{ scope, subject, interpretation: "적음", status: "candidate", candidate_refs: refs(2) }] }) }), /관측이 아직 2개예요/],
    [setup({ reflections: () => ({ authority: "data_only_untrusted", items: [] }) }), /관측이 아직 0개예요/],
    [setup({ reflections: () => ({ authority: "x" }) }), /교훈을 읽지 못했어요/],
  ];
  for (const [ctx, why] of cases) {
    await open(ctx.panel);
    assert.match(text(ctx.panel), /아직 개선안을 만들 수 없어요/);
    assert.match(text(ctx.panel), why);
    assert.equal(ctx.panel.querySelector("form"), null);
  }
  const [ctx] = cases[3];
  const before = ctx.invokes.length;
  await btn(ctx.panel, "다시 확인").click(); await idle();
  assert.equal(ctx.invokes.length, before + 1);
});

test("a baseline that moved before sending stops the request", async () => {
  store.clear();
  let skill = { id: "sk1", scope, revision: 2, active_version: "v1" };
  const ctx = setup({ skill: () => skill });
  await open(ctx.panel);
  skill = { ...skill, revision: 3 };
  await ctx.panel.querySelector("form").fire("submit"); await idle();
  assert.match(text(ctx.panel), /만들기를 시작하지 못했어요/);
  assert.equal(sent(ctx).length, 0);
  assert.equal(store.size, 0);
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
