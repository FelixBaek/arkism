// 스킬 › 제한적으로 시험 적용 (skill-trial.js) and its 자동 처리 상태 panel (skill-trial-status.js).
const test = require("node:test");
const assert = require("node:assert/strict");
const { createHash } = require("node:crypto");
const { createDocument, house, flush, Element } = require("./testdata/views-dom.js");
Element.prototype.select = function () { this.selectionStart = 0; this.selectionEnd = this.value.length; };

// Real crypto.subtle resolves on the thread pool, so under load it can land after the test looks; hash in-process instead.
Object.defineProperty(globalThis, "crypto", { configurable: true, value: { randomUUID: () => require("node:crypto").randomUUID(), subtle: { digest: async (_, data) => { const h = createHash("sha256").update(Buffer.from(data instanceof ArrayBuffer ? new Uint8Array(data) : data)).digest(); return h.buffer.slice(h.byteOffset, h.byteOffset + h.length); } } } });
globalThis.window = globalThis;
globalThis.document = createDocument();
require("./skill-trial-status.js");
require("./skill-trial.js");

const store = new Map();
let storageBroken = false;
globalThis.localStorage = {
  getItem: k => store.has(k) ? store.get(k) : null,
  setItem: (k, v) => { if (storageBroken) throw new Error("full"); store.set(k, String(v)); },
  removeItem: k => { if (storageBroken) throw new Error("full"); store.delete(k); },
};
let clip = async () => {};
Object.defineProperty(globalThis, "navigator", { value: { clipboard: { writeText: t => clip(t) } }, configurable: true });

const scope = { workspace_id: "w" };
const TID = "trial-" + "b".repeat(64);
const future = new Date(Date.now() + 86400000).toISOString();
const btn = (root, label) => root.querySelectorAll("button").find(b => b.textContent.includes(label));
const text = n => n.textContent;
const record = (extra = {}) => ({ id: TID, scope, skill_id: "sk1", revision: 1, status: "active", created_at: "2026-10-01T00:00:00Z",
  skill_trial: { candidate: { collection: "skill_versions", id: "v1", revision: 1 }, runtime: "codex", ends_at: future }, ...extra });
const view = (extra = {}) => ({ record: record(), status: "active", resolved_version_id: "v1", resolved_version: 1, ...extra });
const statusReply = (extra = {}) => ({ authority: "data_only_untrusted", scope, skill_id: "sk1", trial: { collection: "skill_trials", id: TID, revision: 1 },
  candidate: { collection: "skill_versions", id: "v1" }, state: "active", observation_state: "not_available", ...extra });

function setup(routes = {}, caps = { role: "human" }) {
  const doc = createDocument(); globalThis.document = doc;
  const h = house(doc), state = { server: "s", token: "t", scope: { ...scope }, view: "skills", selected: "skill:sk1", caps };
  const calls = [];
  const r = {
    list: (b) => ({ authority: "data_only_untrusted", items: [view()], next_cursor: b.after ? "" : "c2" }),
    inspect: () => view(),
    stop: () => record({ status: "stopped" }),
    create: (input) => record({ skill_trial: { ...record().skill_trial, runtime: input.runtime, ends_at: input.ends_at } }),
    version: () => ({ id: "v1", skill_id: "sk1", scope, revision: 1, status: "approved", comparison_plan: {} }),
    status: () => statusReply(),
    ...routes,
  };
  const request = async (path, opt) => {
    const body = opt?.body ? JSON.parse(opt.body) : null; calls.push([path, body]);
    const name = path.endsWith("/list") ? "list" : path.endsWith("/inspect") ? "inspect" : path.endsWith("/stop") ? "stop" : path.endsWith("/status") ? "status" : path === "/v1/skill-trials" ? "create" : "version";
    return r[name](body);
  };
  const make = window.ArkismSkillTrial({ state, el: h.el, button: h.button, request, query: () => "q=1" });
  const panel = make("sk1", "v1");
  doc.body.append(panel);
  return { doc, panel, state, calls, make, h, request };
}
const idle = () => flush(16);
const open = async p => { p.open = true; await p.fire("toggle"); await idle(); };
const journalKey = () => "arkism.skill-trial.v1." + createHash("sha256").update(JSON.stringify(["s", "t", scope, "sk1", "v1"])).digest("hex");
const pendingInput = (extra = {}) => ({ scope, skill_id: "sk1", candidate: { collection: "skill_versions", id: "v1", revision: 1 }, runtime: "codex", ends_at: future, event_id: "ev-1", ...extra });

test("history, detail, copy and stop", async () => {
  store.clear();
  const { panel, calls } = setup();
  await open(panel);
  assert.match(text(panel), /Codex · .* · 시험 가능/);
  await btn(panel, "Codex").click(); await idle();
  assert.match(text(panel), /현재 반환: 버전 1 · 시험 후보/);
  assert.match(text(panel), /시험 진행 중/);
  await btn(panel, "시험 요청 복사").click(); await idle();
  assert.match(text(panel), /복사했어요/);
  clip = async () => { throw new Error("denied"); };
  await btn(panel, "시험 요청 복사").click(); await idle();
  assert.match(text(panel), /직접 복사해 주세요/);
  await btn(panel, "처리 상태 다시 조회").click(); await idle();
  await btn(panel, "시험 상태 새로고침").click(); await idle();
  await btn(panel, "시험 종료").click(); await idle();
  assert.match(text(panel), /시험을 종료하고/);
  await btn(panel, "다음 기록").click(); await idle();
  assert.ok(calls.some(([p, b]) => p.endsWith("/list") && b.after === "c2"));
  await btn(panel, "첫 기록으로").click(); await idle();
  panel.open = false; await panel.fire("toggle");
  assert.equal(panel.querySelector("p"), null);
});

test("expired copy, unresolved versions, failed stop, refresh and page", async () => {
  store.clear();
  const past = view({ record: record({ skill_trial: { ...record().skill_trial, ends_at: "2000-01-01T00:00:00Z" } }) });
  let inspect = () => past;
  const ctx = setup({ inspect: () => inspect(), stop: () => ({}), list: () => ({ authority: "data_only_untrusted", items: [] }) }, { role: "human" });
  await open(ctx.panel);
  assert.match(text(ctx.panel), /표시할 시험 기록이 없어요/);
  // reach a trial through a fresh draw with a selected id via start's success path is covered elsewhere; here use refresh by hand
  const a = setup({ inspect: () => inspect(), stop: () => ({}) });
  await open(a.panel); await btn(a.panel, "Codex").click(); await idle();
  await btn(a.panel, "시험 요청 복사").click();
  assert.match(text(a.panel), /종료 시각이 지났어요/);
  await btn(a.panel, "시험 종료").click(); await idle();
  assert.match(text(a.panel), /종료 결과를 확인하지 못했어요/);
  inspect = () => view({ status: "stopped", record: record({ status: "stopped" }), resolution_error: "skill_not_found", resolved_version_id: undefined });
  await btn(a.panel, "시험 상태 새로고침").click(); await idle();
  assert.match(text(a.panel), /불러올 수 있는 버전이 없어요/);
  inspect = () => ({ nope: 1 });
  await btn(a.panel, "시험 상태 새로고침").click(); await idle();
  assert.match(text(a.panel), /현재 상태를 읽지 못했어요/);
  const bad = setup({ list: () => ({ authority: "x" }) }, null);
  await open(bad.panel);
  await btn(bad.panel, "기록 다시 조회").click(); await idle();
  assert.match(text(bad.panel), /시험 기록을 읽지 못했어요/);
});

test("a new trial is journaled, started and shown", async () => {
  store.clear();
  const ctx = setup();
  await open(ctx.panel);
  const form = ctx.panel.querySelector("form");
  const ends = form.querySelector("input");
  assert.ok(ends.value);
  ends.value = "2000-01-01T00:00";
  await form.fire("submit");
  assert.match(text(ctx.panel), /미래의 종료 시각/);
  ends.value = future.slice(0, 16);
  await form.fire("submit"); await idle();
  assert.ok(ctx.calls.some(([p]) => p === "/v1/skill-trials"));
  assert.equal(store.size, 0);
  assert.match(text(ctx.panel), /서버에서 현재 상태를 확인했어요/);
  // storage that cannot hold the request stops before sending
  const full = setup();
  await open(full.panel);
  storageBroken = true;
  full.panel.querySelector("input").value = future.slice(0, 16);
  await full.panel.querySelector("form").fire("submit");
  storageBroken = false;
  assert.match(text(full.panel), /요청을 보관하지 못했어요/);
});

test("a rejected start offers retry and a fresh choice", async () => {
  store.clear();
  let fail = "revision_conflict";
  const ctx = setup({ create: () => { throw new Error(fail); } });
  await open(ctx.panel);
  ctx.panel.querySelector("input").value = future.slice(0, 16);
  await ctx.panel.querySelector("form").fire("submit"); await flush();
  assert.match(text(ctx.panel), /준비 결과를 확인하지 못했어요/);
  fail = "boom";
  await btn(ctx.panel, "같은 시험 요청 다시 확인").click(); await idle();
  assert.equal(btn(ctx.panel, "시험 입력 다시 선택"), undefined);
  fail = "access_denied";
  await btn(ctx.panel, "같은 시험 요청 다시 확인").click(); await idle();
  storageBroken = true;
  await btn(ctx.panel, "시험 입력 다시 선택").click();
  storageBroken = false;
  assert.match(text(ctx.panel), /앱 저장 공간을 확인해 주세요/);
  await btn(ctx.panel, "시험 입력 다시 선택").click(); await idle();
  assert.equal(store.size, 0);
});

test("a pending journal entry is checked again; broken ones block new trials", async () => {
  store.clear();
  store.set(journalKey(), JSON.stringify(pendingInput()));
  const ctx = setup({ create: () => record() });
  await open(ctx.panel);
  assert.match(text(ctx.panel), /확인할 시험 준비 요청이 남아 있어요/);
  await btn(ctx.panel, "같은 시험 요청 다시 확인").click(); await idle();
  assert.equal(store.size, 0);
  for (const raw of ["x".repeat(40000), JSON.stringify(pendingInput({ runtime: "nope" }))]) {
    store.set(journalKey(), raw);
    const b = setup(); await open(b.panel);
    assert.match(text(b.panel), /새 시험을 준비할 수 없어요/);
  }
  store.clear();
  const notApproved = setup({ version: () => ({ id: "v1", skill_id: "sk1", scope, revision: 1, status: "candidate" }) });
  await open(notApproved.panel);
  assert.match(text(notApproved.panel), /승인된 후보에서 새 시험을/);
});

test("trial status panel reads every state and scheduler shape", async () => {
  const run = async (reply) => {
    const ctx = setup({ status: reply });
    const panel = window.ArkismSkillTrialStatus({ state: ctx.state, el: ctx.h.el, button: ctx.h.button, request: ctx.request })("sk1", TID, "v1");
    ctx.doc.body.append(panel); await flush();
    return text(panel);
  };
  assert.match(await run(() => statusReply({ scheduler: { status: "retry", attempts: 2, next_check: future } })), /현재 시도 {3}2회.*다음 확인/);
  assert.match(await run(() => statusReply({ state: "promoted", scheduler: { status: "done", attempts: 0 } })), /기본 버전으로 적용됨.*확인 작업 종료/);
  assert.match(await run(() => statusReply()), /등록되어 있지 않아요/);
  assert.match(await run(() => statusReply({ scheduler: { status: "x", attempts: 0 } })), /확인하지 못했어요/);
  assert.match(await run(() => statusReply({ scheduler: { status: "pending", attempts: 0, next_check: "nope" } })), /확인하지 못했어요/);
  assert.match(await run(() => statusReply({ state: "nope" })), /확인하지 못했어요/);
});
