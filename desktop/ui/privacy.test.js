// Run with: node --test desktop/ui/*.test.js
const test = require("node:test");
const assert = require("node:assert/strict");
const dom = require("./testdata/fake-dom.cjs");
const { document } = dom.install();
const h = dom.helpers(document);
dom.Element.prototype.setCustomValidity = function (m) { this.validity = m; };
dom.Element.prototype.reportValidity = function () { this.reported = (this.reported || 0) + 1; return !this.validity; };
const store = new Map();
let storageBroken = false;
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => { if (storageBroken) throw new Error("quota"); store.set(k, String(v)); },
  removeItem: (k) => store.delete(k)
};
require("./memory-management.js");
require("./privacy.js");

let answers = [], texts = [];
globalThis.ask = async () => answers.shift();
globalThis.askText = async () => texts.shift();

function setup(handler) {
  const calls = [], notices = [];
  let changed = 0;
  const state = { server: "s", token: "t", scope: { workspace_id: "w" }, selected: "m1" };
  const request = async (path, opts) => { calls.push({ path, opts, body: opts?.body ? JSON.parse(opts.body) : null }); return handler(path, opts, state); };
  const section = (title, aside) => { const s = h.el("section", "section"); const t = h.el("h3", "", title); if (aside) t.append(h.el("small", "", aside)); s.append(t); return s; };
  const api = window.ArkismPrivacy({ state, el: h.el, button: h.button, request, query: (o = {}) => h.query(o), notice: (m) => notices.push(m), section, afterChange: async () => { changed++; } });
  return { api, calls, notices, state, changed: () => changed };
}
const btn = (root, label) => dom.buttons(root, label)[0];
const memory = { id: "m1", revision: 3, status: "active", text: "홍길동 메일", sources: [{ source_id: "s2" }, {}], source_event_id: "s1" };

test("expiry picks post the chosen time and report each failure without locking the buttons", async () => {
  let fail = null;
  const t = setup(() => { if (fail) throw new Error(fail); return {}; });
  const [expiry, danger] = t.api.memorySections({ ...memory, expires_at: "2030-01-01T00:00:00Z" });
  assert.match(expiry.textContent, /만료일/);
  assert.match(danger.textContent, /개인정보 지우기/);
  btn(expiry, "1주").click(); await dom.flush();
  const week = Date.parse(t.calls[0].body.expires_at) - Date.now();
  assert.ok(Math.abs(week - 7 * 86400000) < 3 * 86400000);
  assert.equal(t.calls[0].path, "/v1/memories/m1/expiry");
  assert.equal(t.calls[0].body.expected_revision, 3);
  assert.match(t.notices[0], /까지만 써요/);
  assert.equal(t.changed(), 1);
  assert.ok(btn(expiry, "1개월").disabled, "buttons stay locked until the view is redrawn");
  const fresh = () => t.api.memorySections({ ...memory, expires_at: "2030-01-01T00:00:00Z" })[0];
  btn(fresh(), "1개월").click(); await dom.flush();
  btn(fresh(), "3개월").click(); await dom.flush();
  btn(fresh(), "만료일 지우기").click(); await dom.flush();
  assert.equal(t.calls.length, 4);
  assert.equal(t.calls.at(-1).body.expires_at, null);
  assert.equal(t.notices.at(-1), "만료일을 지웠어요. 계속 써요.");
  const again = fresh();
  for (const [code, want] of [["conflict", /그새 바뀐/], ["access_denied", /바꿀 수 없어요/], ["boom", /\(boom\)/]]) {
    fail = code; btn(again, "1주").click(); await dom.flush();
    assert.match(t.notices.at(-1), want);
    assert.ok(again.querySelectorAll("button, input").every((b) => !b.disabled));
  }
});

test("a typed expiry date must be after today", async () => {
  const t = setup(() => ({}));
  const [expiry] = t.api.memorySections(memory);
  assert.equal(btn(expiry, "만료일 지우기"), undefined);
  const date = expiry.querySelector("input");
  btn(expiry, "날짜로 정하기").click();
  assert.equal(date.validity, "오늘 이후 날짜를 고르세요.");
  date.value = "2000-01-01"; btn(expiry, "날짜로 정하기").click();
  assert.equal(t.calls.length, 0);
  dom.fire(date, "input"); assert.equal(date.validity, "");
  date.value = "2999-02-03"; btn(expiry, "날짜로 정하기").click(); await dom.flush();
  assert.equal(new Date(t.calls[0].body.expires_at).getFullYear(), 2999);
});

test("erasing one memory asks twice, erases its sources and keeps a receipt id", async () => {
  store.clear();
  const t = setup((path) => {
    if (path.startsWith("/v1/sources/s1/derived")) return { items: [{ id: "m1" }, { id: "m9" }] };
    if (path.startsWith("/v1/sources/s2/derived")) throw new Error("down");
    if (path === "/v1/privacy/deletions") return { id: "job-1" };
    return {};
  });
  const [danger] = t.api.memorySections({ ...memory, status: "candidate" });
  answers = [false];
  btn(danger, "영구 삭제").click(); await dom.flush();
  assert.equal(t.calls.filter((c) => c.opts?.method === "POST").length, 0);
  answers = [true, false];
  btn(danger, "영구 삭제").click(); await dom.flush();
  assert.equal(t.calls.filter((c) => c.opts?.method === "POST").length, 0);
  answers = [true, true];
  btn(danger, "영구 삭제").click(); await dom.flush();
  const post = t.calls.find((c) => c.path === "/v1/privacy/deletions");
  assert.deepEqual(post.body.target_ids, ["s1", "s2"]);
  assert.equal(post.body.target_type, "source_event");
  assert.match(post.body.idempotency_key, /^desktop:/);
  assert.equal(t.state.selected, null);
  assert.equal(t.changed(), 1);
  assert.match(t.notices.at(-1), /삭제 요청을 접수했어요/);
  const saved = JSON.parse([...store.values()][0]);
  assert.equal(saved[0].id, "job-1");
  assert.equal(btn(danger, "영구 삭제").disabled, false);
});

test("a memory without sources is deleted directly and errors are explained", async () => {
  let fail = null;
  const t = setup(() => { if (fail) throw new Error(fail); return {}; });
  const m = { id: "m2", revision: 1, status: "candidate", text: "x" };
  const [danger] = t.api.memorySections(m);
  answers = [true, true];
  btn(danger, "영구 삭제").click(); await dom.flush();
  assert.equal(t.calls[0].opts.method, "DELETE");
  assert.equal(t.calls[0].path, "/v1/memories/m2");
  for (const [code, want] of [["access_denied", /지울 수 없어요/], ["conflict", /진행 중/], ["not_found", /이미 지워졌/], ["odd", /\(odd\)/]]) {
    fail = code; answers = [true, true];
    btn(danger, "영구 삭제").click(); await dom.flush();
    assert.match(t.notices.at(-1), want);
  }
});

test("source erasure is split into the server's 64-id batches", async () => {
  storageBroken = true;
  const t = setup((path) => (path === "/v1/privacy/deletions" ? {} : { items: [] }));
  const many = { id: "m3", revision: 1, status: "candidate", text: "x", sources: Array.from({ length: 65 }, (_, i) => ({ source_id: `id${String(i).padStart(3, "0")}` })) };
  answers = [true, true];
  btn(t.api.memorySections(many)[0], "영구 삭제").click(); await dom.flush(20);
  const posts = t.calls.filter((c) => c.path === "/v1/privacy/deletions");
  assert.deepEqual(posts.map((p) => p.body.target_ids.length), [64, 1]);
  storageBroken = false;
});

test("deletion history reads each stored receipt and never treats a failed read as done", async () => {
  store.clear();
  const t = setup((path) => {
    if (path.startsWith("/v1/privacy/deletions/bad")) throw new Error("x");
    return { privacy_deletion: { state: "completed", report: { live_access_removed: true, backup_expiry_pending: true } } };
  });
  const key = `arkism.privacyReceipts:${JSON.stringify([t.state.server, t.state.scope])}`;
  const now = new Date().toISOString();
  store.set(key, JSON.stringify([{ id: "ok-1", at: now }, { id: "bad", at: now }, { id: "bad id!", at: now }, { id: "old", at: "2000-01-01" }]));
  const g = t.api.settingsGroup(); document.body.append(g);
  const box = g.querySelector("details");
  dom.fire(box, "toggle"); // closed: nothing
  assert.equal(t.calls.length, 0);
  box.open = true; dom.fire(box, "toggle"); dom.fire(box, "toggle"); await dom.flush();
  assert.deepEqual(t.calls.map((c) => c.path.split("?")[0]), ["/v1/privacy/deletions/bad", "/v1/privacy/deletions/ok-1"]);
  assert.match(box.textContent, /서버 처리 완료/);
  assert.match(box.textContent, /접근 차단: 서버 완료 보고/);
  assert.match(box.textContent, /실행 데이터: 완료 보고 전/);
  assert.match(box.textContent, /처리 기록을 읽지 못했어요/);
  store.set(key, "not json"); box.open = true; dom.fire(box, "toggle"); await dom.flush();
  assert.match(box.textContent, /접수한 삭제 요청 기록이 없어요/);
  store.set(key, JSON.stringify({ id: "x" })); dom.fire(box, "toggle"); await dom.flush();
  assert.match(box.textContent, /기록이 없어요/);
  g.remove();
});

test("the storage audit says what it checked and never claims a clean archive on failure", async () => {
  let result = null;
  const t = setup(() => { if (!result) throw new Error("x"); return result; });
  const g = t.api.settingsGroup();
  const audit = btn(g, "보관소 잔여 점검"), out = audit.nextSibling;
  for (const [r, want] of [[{ state: "checked_scope", orphan_candidates: 2 }, /후보 2개\./], [{ state: "partial", orphan_candidates: 5 }, /5개 이상/], [{ state: "timeout" }, /마치지 못했어요/], [null, /점검하지 못했어요/]]) {
    result = r; audit.click(); await dom.flush();
    assert.match(out.textContent, want);
    assert.equal(audit.disabled, false);
  }
});

test("erase by phrase validates, previews counts, then needs the phrase typed again", async () => {
  let failScan = null, failErase = false;
  const t = setup((path, opts, state) => {
    if (failScan) { if (failScan === "owner") { state.token = "other"; return { items: [] }; } throw new Error(failScan); }
    if (path.startsWith("/v1/memories?")) {
      return path.includes("cursor=") ? { items: [{ id: "m5", text: "no source" }, { id: "m6", text: "no source 2" }] } : { items: [{ id: "m4", source_event_id: "e1" }], next_cursor: "c2" };
    }
    if (path.startsWith("/v1/sources?")) return { items: [{ id: "e2" }] };
    if (path === "/v1/privacy/deletions") { if (failErase) throw new Error("conflict"); return { id: "job-9" }; }
    if (path === "/v1/memories/m6") throw new Error("gone");
    return {};
  });
  const g = t.api.settingsGroup(); document.body.append(g);
  const form = g.querySelector("form"), input = form.querySelector("input"), status = g.querySelector("[role=status]");
  const submit = () => dom.fire(form, "submit");
  submit(); assert.equal(input.reported, 1);
  input.value = "가".repeat(100); submit(); assert.equal(input.validity, "조금 짧게 입력하세요.");
  dom.fire(input, "input"); assert.equal(input.validity, "");
  input.value = "a"; submit(); assert.equal(input.validity, "두 글자 이상 입력하세요.");

  for (const [code, want] of [["access_denied", /찾을 수 없어요/], ["net", /연결을 확인/]]) {
    failScan = code; input.value = "홍길동"; submit(); await dom.flush();
    assert.match(status.textContent, want);
  }
  failScan = "owner"; submit(); await dom.flush();
  assert.equal(status.textContent, "찾는 중…");
  t.state.token = "t"; failScan = null;

  submit(); await dom.flush();
  assert.match(status.textContent, /기억 3개와 원본 기록 2개/);
  const go = btn(g, "모두 영구 삭제 (5)");
  assert.ok(go);
  // a fresh settings page keeps the phrase and the preview for the same owner
  const again = t.api.settingsGroup();
  assert.equal(again.querySelector("input").value, "홍길동");
  assert.ok(btn(again, "모두 영구 삭제"));

  answers = [false]; go.click(); await dom.flush();
  answers = [true]; texts = [null]; go.click(); await dom.flush();
  answers = [true]; texts = ["다른 말"]; go.click(); await dom.flush();
  assert.equal(t.notices.at(-1), "입력한 말이 달라 지우지 않았어요.");
  assert.equal(t.calls.filter((c) => c.opts?.method).length, 0);

  failErase = true; answers = [true]; texts = [" 홍길동 "]; go.click(); await dom.flush();
  assert.match(status.textContent, /진행 중/);
  failErase = false; answers = [true]; texts = ["홍길동"]; go.click(); await dom.flush();
  const erase = t.calls.filter((c) => c.path === "/v1/privacy/deletions").at(-1);
  assert.deepEqual(erase.body.target_ids, ["e1", "e2"]);
  assert.deepEqual(t.calls.filter((c) => c.opts?.method === "DELETE").map((c) => c.path), ["/v1/memories/m5", "/v1/memories/m6"]);
  assert.match(t.notices.at(-1), /기억 1개는 요청하지 못했어요/);
  assert.equal(status.textContent, "기억 1개는 지우지 못했어요.");
  assert.equal(btn(g, "모두 영구 삭제"), undefined);
  // run() ignores a stale button once the preview is gone
  go.click(); await dom.flush();
  g.remove();
});

test("a phrase found nowhere, and a clean erase, say so", async () => {
  let none = true;
  const t = setup((path) => {
    if (path.startsWith("/v1/memories?")) return { items: none ? [] : [{ id: "m7", sources: [{ source_id: "z" }] }] };
    if (path.startsWith("/v1/sources?")) return { items: [] };
    return {};
  });
  const g = t.api.settingsGroup();
  const form = g.querySelector("form"), input = form.querySelector("input"), status = g.querySelector("[role=status]");
  input.value = "없는말"; dom.fire(form, "submit"); await dom.flush();
  assert.match(status.textContent, /원본 기록이 없어요/);
  none = false; dom.fire(form, "submit"); await dom.flush();
  answers = [true]; texts = ["없는말"]; btn(g, "모두 영구 삭제").click(); await dom.flush();
  assert.match(status.textContent, /원본 기록 1개 · 원본 없는 기억 0개/);
  assert.match(t.notices.at(-1), /삭제 처리 내역에서 확인/);
  // another owner sees an empty status
  t.state.scope = { workspace_id: "other" };
  assert.equal(t.api.settingsGroup().querySelector("[role=status]").textContent, "");
});
