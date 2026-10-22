// Run with: node --test desktop/ui/*.test.js
// CREW › 항상 넣는 자료 (pins.js): what can be pinned, the card's edits and how saved pins resolve, on a small DOM stand-in.
const test = require("node:test");
const assert = require("node:assert/strict");
const dom = require("./testdata/fake-dom.cjs");

const settle = async (n = 6) => { for (let i = 0; i < n; i++) await new Promise((r) => setImmediate(r)); };

function setup({ knowledge = [], pins = null, fail = false } = {}) {
  const { document, window } = dom.install();
  const h = dom.helpers(document);
  if (!window.ArkismPins) require("./pins.js");
  const state = {
    scope: { project_id: "p" },
    data: {
      memories: [
        { id: "m1", status: "active", approved_by: "owner", kind: "decision", text: "짧게 답한다\n둘째 줄", revision: 3 },
        { id: "m2", status: "active", approved_by: "owner", kind: "odd", text: "", revision: 1 },
        { id: "m3", status: "candidate", approved_by: "", text: "후보" },
        { id: "m4", status: "active", approved_by: "owner", content_erased: true, text: "지움" }
      ],
      skills: [
        { id: "s1", status: "active", name: "review", description: "코드 검토", active_version: "v3", version_history: ["v1", "v2", "v3"] },
        { id: "s2", status: "active", name: "lint", description: "", active_version: "x" },
        { id: "s3", status: "deprecated", name: "old", active_version: "v1" }
      ]
    }
  };
  const calls = [];
  const env = { calls, pins: pins || [] };
  const request = (path) => {
    calls.push(path);
    if (path.startsWith("/v1/knowledge/sources")) return fail ? Promise.reject(new Error("x")) : Promise.resolve({ items: knowledge });
    if (path.startsWith("/v1/context/pins")) return env.pinsFail ? Promise.reject(new Error("x")) : Promise.resolve({ items: env.pins });
    throw new Error(path);
  };
  const query = (o = {}) => new URLSearchParams({ ...state.scope, ...o }).toString();
  const api = window.ArkismPins({ state, el: h.el, button: h.button, request, query });
  return { api, state, env, document, window, h };
}

function mount(x, opts) {
  let changes = 0;
  const root = x.api.card({ sub: "부를 때마다 넣어요", noun: "이 에이전트는", onChange: () => changes++, ...opts });
  x.document.body.append(root);
  return { root, changes: () => changes };
}

test("only approved records of the scope can be pinned, with readable titles", async () => {
  const x = setup({ knowledge: [
    { id: "k1", status: "active", trust_level: "reviewed", approved_by: "owner", display_name: "디자인 가이드", chunk_count: 3, revision: 2 },
    { id: "k2", status: "active", trust_level: "reviewed", approved_by: "owner" },
    { id: "k3", status: "draft", trust_level: "reviewed", approved_by: "owner" }
  ] });
  const mem = x.api.candidates("memories");
  assert.deepEqual(mem.map((c) => [c.id, c.title, c.meta, c.chars]), [["m1", "짧게 답한다", "결정", 11], ["m2", "", "기억", 0]]);
  const skills = x.api.candidates("skills");
  assert.deepEqual(skills.map((c) => [c.id, c.versions, c.active, c.mono, c.chars]), [["s1", 3, 3, true, 30], ["s2", 1, 1, true, 0]]);
  assert.deepEqual(x.api.candidates("knowledge_sources"), []);
  x.api.loadLibrary();
  x.api.loadLibrary(); // already loading: one request
  await settle();
  x.api.loadLibrary(); // fresh for 60s: no request
  assert.equal(x.env.calls.filter((p) => p.startsWith("/v1/knowledge/sources")).length, 1);
  assert.match(x.env.calls[0], /limit=500/);
  const k = x.api.candidates("knowledge_sources");
  assert.deepEqual(k.map((c) => [c.id, c.title, c.meta, c.chars]), [["k1", "디자인 가이드", "문서 · 조각 3개", 1800], ["k2", "k2", "문서 · 조각 0개", 600]]);
  assert.equal(x.api.known({ collection: "skills", id: "s1" }).title, "review");
  assert.equal(x.api.known({ collection: "skills", id: "nope" }), null);
  assert.equal(x.api.tokensOf(1), 1);
  assert.equal(x.api._test.tokensOf(250), 100);
  assert.equal(x.api.MAX, 20);
  assert.deepEqual(x.api.CAP, { full: 8000, summary: 380, reference: 80 });
  assert.deepEqual(x.api.KINDS.map(([k]) => k), ["memories", "skills", "knowledge_sources"]);
  assert.ok(x.api.MODES.length === 3 && x.api.STATUS.denied);
});

test("a failed library read leaves knowledge empty", async () => {
  const x = setup({ fail: true });
  x.api.loadLibrary(); await settle();
  assert.deepEqual(x.api.candidates("knowledge_sources"), []);
});

test("the picker adds approved records, filters by words and kind, and keeps the box open while picking", async () => {
  const x = setup({ knowledge: [{ id: "k1", status: "active", trust_level: "reviewed", approved_by: "o", display_name: "가이드", chunk_count: 1 }] });
  const draft = {};
  const { root, changes } = mount(x, { rec: { id: "c1", revision: 1, crew: { handle: "a", pinned: [] } }, draft });
  assert.match(root.textContent, /아직 없어요/);
  assert.match(root.textContent, /고정 장착 0/);
  const q = () => root.querySelector("input[type=search]");
  const drop = () => root.querySelector(".pn-drop");
  assert.equal(drop().hidden, true);
  dom.fire(q(), "focus");
  assert.equal(drop().hidden, false);
  assert.deepEqual(drop().querySelectorAll(".pn-option").map((b) => b.textContent), ["짧게 답한다결정", "기억"]);
  dom.input(q(), " 짧게 ");
  assert.equal(drop().querySelectorAll(".pn-option").length, 1);
  const ev = new dom.Event("mousedown", { bubbles: true, cancelable: true });
  drop().querySelector(".pn-option").dispatchEvent(ev);
  assert.equal(ev.defaultPrevented, true);
  drop().querySelector(".pn-option").click();
  assert.equal(changes(), 1);
  assert.deepEqual(draft.pinned, [{ collection: "memories", id: "m1", revision: 0, injection: "full", priority: 100 }]);
  assert.equal(x.document.activeElement, q());
  assert.match(root.textContent, /고정 장착 1/);
  assert.match(root.textContent, /약 4 토큰/); // 11 chars of an approved memory
  // kinds: skills, then knowledge; an already pinned record is not offered again
  const chip = (label) => root.querySelectorAll(".pn-kindchip").find((b) => b.textContent === label);
  const md = new dom.Event("mousedown", { bubbles: true, cancelable: true });
  chip("스킬").dispatchEvent(md);
  assert.equal(md.defaultPrevented, true);
  chip("스킬").click();
  assert.equal(chip("스킬").getAttribute("aria-pressed"), "true");
  assert.deepEqual(drop().querySelectorAll(".pn-option").map((b) => b.textContent), ["review코드 검토", "lint"]);
  drop().querySelector(".pn-option").click();
  assert.equal(draft.pinned[1].collection, "skills");
  chip("지식").click();
  assert.match(drop().textContent, /불러오는 중|가이드/);
  await settle();
  chip("지식").click();
  drop().querySelector(".pn-option").click();
  assert.equal(draft.pinned[2].injection, "summary");
  // nothing left to pick
  chip("지식").click();
  assert.match(drop().textContent, /넣을 수 있는 승인된 자료가 없어요/);
  // blur closes; Escape blurs
  dom.fire(q(), "blur");
  assert.equal(drop().hidden, true);
  q().focus();
  let blurred = 0; q().addEventListener("blur", () => blurred++);
  const box = q(); box.blur = () => blurred++;
  dom.fire(box, "keydown", { key: "a" });
  dom.fire(box, "keydown", { key: "Escape" });
  assert.equal(blurred, 1);
  // + 자료 넣기 focuses the search box
  x.document.activeElement = x.document.body;
  dom.buttons(root, "+ 자료 넣기")[0].click();
  assert.equal(x.document.activeElement, q());
});

test("rows reorder, change mode and version, and are removed", () => {
  const x = setup();
  window.ArkismI18n = { translate: (s) => s };
  const draft = { pinned: [
    { collection: "memories", id: "m1", revision: 0, injection: "full", priority: 90 },
    { collection: "skills", id: "s1", revision: 0, injection: "full", priority: 100 },
    { collection: "knowledge_sources", id: "gone", revision: 5, injection: "reference", priority: 80 }
  ] };
  const { root, changes } = mount(x, { rec: { id: "c", revision: 1 }, draft, cls: "my-card" });
  delete window.ArkismI18n;
  assert.ok(root.classList.contains("my-card"));
  const names = () => root.querySelectorAll(".pn-row:not(.pn-th) .pn-title").map((n) => n.textContent);
  assert.deepEqual(names(), ["review", "짧게 답한다", "gone"]); // by priority
  const rows = () => root.querySelectorAll(".pn-row:not(.pn-th)");
  assert.match(rows()[0].textContent, /최신 v3/);
  assert.match(rows()[2].textContent, /지식/);
  assert.equal(rows()[0].querySelector("[aria-label=위로]").disabled, true);
  assert.equal(rows()[2].querySelector("[aria-label=아래로]").disabled, true);
  rows()[0].querySelector("[aria-label=아래로]").click();
  assert.deepEqual(names(), ["짧게 답한다", "review", "gone"]);
  assert.deepEqual(draft.pinned.map((p) => p.priority), [100, 95, 90]);
  rows()[1].querySelector("[aria-label=위로]").click();
  assert.deepEqual(names(), ["review", "짧게 답한다", "gone"]);
  // versions: a skill lists every version, a memory its current and pinned revision, knowledge its pinned one
  const ver = (i) => rows()[i].querySelector("[aria-label=버전]");
  assert.deepEqual(ver(0).options.map((o) => o.textContent), ["최신", "고정 v3", "고정 v2", "고정 v1"]);
  assert.deepEqual(ver(1).options.map((o) => o.textContent), ["최신", "고정 v3"]);
  assert.deepEqual(ver(2).options.map((o) => o.textContent), ["최신", "고정 v5"]);
  dom.input(ver(1), "3");
  assert.equal(draft.pinned.find((p) => p.id === "m1").revision, 3);
  assert.deepEqual(ver(1).options.map((o) => o.selected), [false, true]);
  dom.input(ver(1), "0");
  const mode = rows()[1].querySelector("[aria-label='넣는 방식']") || rows()[1].querySelectorAll("select")[0];
  dom.input(mode, "reference");
  assert.equal(draft.pinned.find((p) => p.id === "m1").injection, "reference");
  rows()[2].querySelector(".pn-del").click();
  assert.deepEqual(names(), ["review", "짧게 답한다"]);
  assert.equal(changes(), 6);
  assert.match(root.textContent, /합계 약 \d+ 토큰 · 최대 20개/);
});

test("a newer skill version asks to move up or keep the pinned one", () => {
  const x = setup();
  const draft = { pinned: [{ collection: "skills", id: "s1", revision: 1, injection: "full", priority: 100 }] };
  const { root } = mount(x, { rec: { id: "c", revision: 1 }, draft });
  assert.match(root.textContent, /고정 v1/);
  assert.match(root.querySelector(".pn-new").textContent, /새 버전 v3 있음/);
  assert.match(root.querySelector(".pn-alert").textContent, /review 스킬에 v3가 나왔어요\. 이 에이전트는 v1로 고정돼 있어요\./);
  dom.buttons(root, "v1 유지")[0].click();
  assert.equal(root.querySelector(".pn-alert"), null);
  assert.equal(draft.pinned[0].revision, 1);
  draft.pinned[0].revision = 2;
  const again = mount(x, { rec: { id: "c", revision: 1 }, draft }).root;
  dom.buttons(again, "v3로 바꾸기")[0].click();
  assert.equal(draft.pinned[0].revision, 3);
  assert.equal(again.querySelector(".pn-alert"), null);
});

test("saved pins resolve on the server; one that is not ok is flagged and not counted", async () => {
  const saved = [
    { collection: "memories", id: "m1", revision: 0, injection: "full", priority: 100 },
    { collection: "memories", id: "lost", revision: 0, injection: "summary", priority: 95 },
    { collection: "skills", id: "s1", revision: 2, injection: "full", priority: 90 },
    { collection: "memories", id: "m2", revision: 0, injection: "full", priority: 85 }
  ];
  const x = setup({ pins: [
    { pin: saved[0], status: "ok", item: { text: "짧게 답한다 전체 본문", summary: "요약" } },
    { pin: saved[1], status: "not_found", item: { summary: "잃어버린 기억\n둘" } },
    { pin: saved[2], status: "revision_mismatch" },
    { pin: saved[3], status: "weird" }
  ] });
  const rec = { id: "c", revision: 7, crew: { handle: "helper", pinned: saved } };
  const draft = { pinned: saved.map((p) => ({ ...p })) };
  const { root } = mount(x, { rec, draft });
  assert.equal(x.api.resolution(rec), null); // still loading
  await settle();
  assert.ok(x.env.calls.some((p) => p.includes("agent=%40helper")));
  assert.equal(x.api.resolution(rec).items.length, 4);
  const rows = root.querySelectorAll(".pn-row.bad");
  assert.equal(rows.length, 3);
  assert.match(rows[0].textContent, /잃어버린 기억/);
  assert.match(rows[0].textContent, /없어짐/);
  assert.match(rows[0].textContent, /넣지 않아요/);
  assert.equal(rows[0].querySelector(".wf-badge").title, "자료가 지워졌거나 찾을 수 없어요.");
  assert.match(rows[2].querySelector(".wf-badge").textContent, /weird/);
  assert.match(root.textContent, /약 6 토큰/); // the resolved text (12 + 2 chars) of the ok pin
  // the first pin needing a decision: the lost memory, with only 빼기
  assert.match(root.querySelector(".pn-alert").textContent, /기억 '잃어버린 기억' — 자료가 지워졌거나 찾을 수 없어요\. 이 자료는 넣지 않아요\./);
  dom.buttons(root.querySelector(".pn-alert"), "빼기")[0].click();
  assert.equal(draft.pinned.some((p) => p.id === "lost"), false);
  // next: the skill pinned at an old revision (the newer-version notice comes first, keep it)
  dom.buttons(root.querySelector(".pn-alert"), "v2 유지")[0].click();
  assert.match(root.querySelector(".pn-alert").textContent, /고정한 버전이 바뀌었어요/);
  dom.buttons(root.querySelector(".pn-alert"), "최신으로 바꾸기")[0].click();
  assert.equal(draft.pinned.find((p) => p.id === "s1").revision, 0);
  // unknown status: its own code is shown
  assert.match(root.querySelector(".pn-alert").textContent, /weird/);
  // a pin at another revision is not the saved one
  assert.equal(x.api.statusOf(rec, { ...saved[0], revision: 9 }, x.api.resolution(rec)), null);
  assert.equal(x.api.statusOf({ id: "z" }, saved[0], null), null);
});

test("a record without handle or pins asks nothing; a failed resolution stays unresolved", async () => {
  const x = setup();
  assert.equal(x.api.resolution({ id: "a", revision: 1 }), null);
  assert.equal(x.api.resolution({ id: "b", revision: 1, crew: { handle: "h", pinned: [] } }), null);
  assert.equal(x.env.calls.filter((p) => p.startsWith("/v1/context/pins")).length, 0);
  x.env.pinsFail = true;
  const rec = { id: "c", revision: 1, crew: { handle: "h", pinned: [{ collection: "memories", id: "m1" }] } };
  assert.equal(x.api.resolution(rec), null);
  await settle();
  assert.equal(x.api.resolution(rec), null);
  assert.equal(x.env.calls.filter((p) => p.startsWith("/v1/context/pins")).length, 1);
});

test("twenty pins fill the card; redraws reach only cards still on screen", async () => {
  const x = setup({ knowledge: [] });
  const draft = { pinned: Array.from({ length: 20 }, (_, i) => ({ collection: "memories", id: `x${i}`, revision: 0, injection: "full", priority: 0 })) };
  const { root, changes } = mount(x, { rec: { id: "c", revision: 1 }, draft });
  const add = dom.buttons(root, "+ 자료 넣기")[0];
  assert.equal(add.disabled, true);
  add.disabled = false; add.click(); // still refused at the cap
  assert.equal(changes(), 0);
  assert.equal(root.querySelector("input[type=search]").disabled, true);
  // watch(): the expert popup redraws while its root is connected, and is dropped once removed
  let redrawn = 0;
  const pop = x.h.el("div"); x.document.body.append(pop);
  x.api.watch(pop, () => redrawn++);
  const gone = x.h.el("div");
  x.api.watch(gone, () => redrawn += 100);
  root.remove();
  x.api.loadLibrary(); await settle(); // a library answer redraws every live card
  assert.equal(redrawn, 1);
  pop.remove();
  x.api.watch(x.h.el("div"), () => {});
});
