// Run with: node --test desktop/ui/*.test.js
const test = require("node:test");
const assert = require("node:assert/strict");
require("./keep.js"); // the shared stale-reply and redraw guards the screens use
const dom = require("./testdata/fake-dom.cjs");
const { document } = dom.install();
const h = dom.helpers(document);
dom.Element.prototype.setCustomValidity = function (m) { this.validity = m; };
dom.Element.prototype.reportValidity = function () { return !this.validity; };
require("./originals.js");

const A = "data_only_untrusted";
for (const id of ["table", "chips", "stats", "detail"]) { const n = h.el("div"); n.id = id; document.body.append(n); }
const $ = (s) => document.querySelector(s);

function setup(handler) {
  const calls = [];
  let opened = 0;
  const state = { server: "s", token: "t", scope: { workspace_id: "w" }, view: "memories", memTab: "originals", selected: null };
  const request = async (path, opts) => { const body = JSON.parse(opts.body); calls.push({ path, body }); return handler(path.split("/").pop(), body, state); };
  const detailShell = (label) => { const body = h.el("div", "detail-body"); $("#detail").replaceChildren(h.el("span", "", label), body); return body; };
  const api = window.ArkismOriginals({ state, $, el: h.el, icon: h.icon, button: h.button, request, detailShell, openDetail: () => opened++, ago: () => "방금" });
  return { api, calls, state, opened: () => opened };
}
const btn = (root, label) => dom.buttons(root, label)[0];
const archive = { id: "a1", origin: "codex", device: "pc", observed_at: "2026-01-01T00:00:00Z", expires_at: "2026-02-01T00:00:00Z", state: "active" };
const found = { ref: { archive_id: "a2", seq: 1 }, text: "찾은 메시지", role: "user" };

test("the list loads, pages with 더 보기, notes skipped records and opens an archive's messages", async () => {
  let listCalls = 0;
  const t = setup((path, body) => {
    if (path === "list") {
      listCalls++;
      return body.after ? { authority: A, items: [{ ...archive, id: "a3", origin: "odd", device: "", state: "expired" }] }
        : { authority: A, items: [archive], next_cursor: "n1", oversized_records: 1 };
    }
    if (path === "search") return body.after ? { authority: A, items: [], invalid_records: 2 } : { authority: A, items: [{ authority: A, ref: { archive_id: "a1", seq: 2 }, text: "안녕", role: "user" }, { authority: A, ref: { archive_id: "a1", seq: 3 }, role: "assistant" }], next_cursor: "m2" };
    if (path === "message") return body.text_offset === 0 ? { authority: A, ref: { archive_id: "a1", seq: 2 }, text: "앞", text_offset: 0, next_text_offset: 5, role: "assistant" } : { authority: A, ref: { archive_id: "a1", seq: 2 }, text: "뒤", text_offset: 5, role: "user" };
  });
  t.api.draw(); await dom.flush();
  assert.equal(listCalls, 1);
  assert.match($("#table").textContent, /읽을 수 없거나 표시할 수 없는 기록/);
  assert.match($("#table").textContent, /Codex · pc/);
  assert.equal($("#table").getAttribute("aria-busy"), "false");
  btn($("#table"), "원문 더 보기").click(); await dom.flush();
  assert.equal(t.calls.at(-1).body.after, "n1");
  assert.match($("#table").textContent, /odd · 기기/);
  assert.match($("#table").textContent, /조회 제한/);
  assert.equal(btn($("#table"), "원문 더 보기"), undefined);

  $("#table").querySelector(".original-row").click(); await dom.flush();
  assert.equal(t.state.selected, "original-selected");
  assert.equal(t.opened(), 1);
  assert.match($("#detail").textContent, /Codex 대화/);
  assert.match($("#detail").textContent, /2개 메시지를 불러왔어요/);
  assert.match($("#detail").textContent, /빈 메시지/);
  btn($("#detail"), "메시지 더 보기").click(); await dom.flush();
  assert.match($("#detail").textContent, /제외됐어요/);
  const open = btn($("#detail"), "메시지 열기");
  open.click(); await dom.flush();
  assert.equal(open.getAttribute("aria-expanded"), "true");
  assert.match($("#detail").textContent, /앞/);
  assert.match($("#detail").textContent, /에이전트 · 조회만으로/);
  btn($("#detail"), "메시지 이어 읽기").click(); await dom.flush();
  assert.match($("#detail .original-text").textContent, /^앞뒤$/);
  assert.equal(btn($("#detail"), "메시지 이어 읽기").hidden, true);
  open.click(); assert.equal(open.getAttribute("aria-expanded"), "false");

  // closing returns focus to the selected row
  t.api.close();
  assert.equal(document.activeElement, $("#table").querySelector(".original-row"));
  t.api.close(); // nothing selected: no focus move
});

test("search, empty sections and failures each show their own message", async () => {
  let mode = "empty-next";
  const t = setup((path, body) => {
    if (mode === "bad") return { authority: "other", items: [] };
    if (mode === "repeat") return { authority: A, items: [], next_cursor: body.after || "x" };
    if (mode === "empty-next") return { authority: A, items: [], next_cursor: "c" };
    if (mode === "found") return { authority: A, items: [found] };
    return { authority: A, items: [] };
  });
  t.api.draw(); await dom.flush();
  assert.match($("#table").textContent, /다음 구간을 확인하세요/);
  mode = "none"; t.api.reload(); await dom.flush();
  assert.match($("#table").textContent, /표시할 원문이 없어요/);
  mode = "bad"; t.api.reload(); await dom.flush();
  assert.match($("#table").textContent, /원문을 불러오지 못했어요/);
  mode = "none"; btn($("#table"), "다시 불러오기").click(); await dom.flush();
  assert.doesNotMatch($("#table").textContent, /불러오지 못했어요/);

  const form = $(".original-search"), input = form.querySelector("input");
  input.value = "가".repeat(100); dom.fire(form, "submit");
  assert.equal(input.validity, "검색어를 조금 짧게 입력하세요.");
  dom.fire(input, "input"); assert.equal(input.validity, "");
  mode = "found"; input.value = " 찾기 "; dom.fire(form, "submit"); await dom.flush();
  assert.equal(t.calls.at(-1).path, "/v1/session-archives/search");
  assert.equal(t.calls.at(-1).body.q, "찾기");
  assert.match($("#table").textContent, /사용자 메시지/);
  // draw again keeps the term and rows for the same owner
  t.api.draw(); assert.equal($(".original-search input").value, "찾기");
  // a repeated cursor is rejected
  mode = "repeat"; t.api.reload(); await dom.flush();
  btn($("#table"), "원문 더 보기").click(); await dom.flush();
  assert.match($("#table").textContent, /불러오지 못했어요/);
});

test("a searched message opens straight into the reader, which recovers from bad replies", async () => {
  let reply = "bad";
  const t = setup((path, body) => {
    if (path === "search" && !body.archive_id) return { authority: A, items: [found] };
    if (reply === "bad") return { authority: A, text: 1 };
    if (reply === "offset") return { authority: A, ref: found.ref, text: "x", text_offset: 0, next_text_offset: 0 };
    return { authority: A, ref: found.ref, text: "본문", text_offset: 0, role: "system" };
  });
  t.api.draw();
  dom.fire($(".original-search"), "submit"); // term empty: list
  await dom.flush();
  $(".original-search input").value = "q"; dom.fire($(".original-search"), "submit"); await dom.flush();
  $("#table").querySelector(".original-row").click(); await dom.flush();
  assert.equal(document.activeElement, $("#detail h2"));
  assert.match($("#detail").textContent, /검색한 메시지/);
  assert.match($("#detail").textContent, /원문을 읽지 못했어요/);
  reply = "offset"; btn($("#detail"), "다시 읽기").click(); await dom.flush();
  assert.match($("#detail").textContent, /원문을 읽지 못했어요/);
  reply = "ok"; btn($("#detail"), "다시 읽기").click(); await dom.flush();
  assert.match($("#detail").textContent, /메시지 · 조회만으로/);
});

test("answers that arrive after the view or owner changed are dropped", async () => {
  const pending = [];
  const t = setup(() => new Promise((resolve) => pending.push(resolve)));
  t.api.draw();
  assert.equal($("#table").getAttribute("aria-busy"), "true");
  assert.match($("#table").textContent, /불러오는 중/);
  t.api.reload(); // busy: ignored
  assert.equal(pending.length, 1);
  t.state.view = "skills";
  pending.shift()({ authority: A, items: [archive] }); await dom.flush();
  t.state.view = "memories";
  assert.equal($("#table").textContent.includes("Codex"), false);
  // a rejected answer after an owner change keeps no error
  const t2 = setup(() => Promise.reject(new Error("x")));
  t2.api.draw(); t2.state.token = "other"; await dom.flush();
  t2.api.draw(); // owner key changed: reset and load again
  await dom.flush();
  assert.match($("#table").textContent, /불러오지 못했어요/);
  t2.api.reset();
  assert.equal($("#table").hasAttribute("aria-busy"), false);
});

test("archive messages and the reader stop when the detail is no longer live", async () => {
  const pending = [];
  const t = setup((path) => {
    if (path === "list") return { authority: A, items: [archive] };
    return new Promise((resolve, reject) => pending.push({ resolve, reject, path }));
  });
  t.api.draw(); await dom.flush();
  $("#table").querySelector(".original-row").click(); await dom.flush();
  t.state.view = "skills";
  pending.shift().resolve({ authority: A, items: [] }); await dom.flush();
  assert.match($("#detail").textContent, /메시지를 불러오는 중/);
  t.state.view = "memories";
  btn($("#detail"), "메시지 더 보기").click();
  btn($("#detail"), "메시지 더 보기").click(); // still loading: ignored
  assert.equal(pending.length, 1);
  pending.shift().resolve({ authority: A, items: [] }); await dom.flush();
  assert.match($("#detail").textContent, /표시할 메시지가 없어요/);
  // a failed page clears the list
  const t2 = setup((path, body) => {
    if (path === "list") return { authority: A, items: [archive] };
    if (path === "search") return body.after ? { authority: A, items: [{ authority: "x" }] } : { authority: A, items: [], next_cursor: "c" };
    return new Promise((resolve, reject) => pending.push({ resolve, reject }));
  });
  t2.api.draw(); await dom.flush();
  $("#table").querySelector(".original-row").click(); await dom.flush();
  assert.match($("#detail").textContent, /이 구간에 표시할 메시지가 없어요/);
  btn($("#detail"), "메시지 더 보기").click(); await dom.flush();
  assert.match($("#detail").textContent, /메시지를 불러오지 못했어요/);
  // reader: not live while pending, then a rejection after leaving is ignored
  const t3 = setup((path) => {
    if (path !== "message") return { authority: A, items: [found] };
    return new Promise((resolve, reject) => pending.push({ resolve, reject }));
  });
  t3.api.draw(); await dom.flush();
  $("#table").querySelector(".original-row").click(); await dom.flush();
  t3.state.view = "skills";
  pending.shift().resolve({ authority: A }); await dom.flush();
  t3.state.view = "memories";
  t3.api.detail(); t3.state.view = "skills";
  pending.shift().reject(new Error("x")); await dom.flush();
  assert.match($("#detail").textContent, /원문을 읽는 중/);
  // messages() rejected after leaving
  const t4 = setup((path) => (path === "list" ? { authority: A, items: [archive] } : Promise.reject(new Error("x"))));
  t4.api.draw(); await dom.flush();
  $("#table").querySelector(".original-row").click(); t4.state.view = "skills"; await dom.flush();
  assert.match($("#detail").textContent, /메시지를 불러오는 중/);
  t4.state.view = "memories";
  // a load whose run was superseded keeps busy for the newer one
  const t5 = setup((path, body) => (body.q ? { authority: A, items: [found] } : new Promise((resolve) => pending.push({ resolve }))));
  t5.api.draw(); t5.api.reset(); t5.api.draw(); await dom.flush();
  pending.shift().resolve({ authority: A, items: [archive] }); await dom.flush();
  pending.shift().resolve({ authority: A, items: [] }); await dom.flush();
  assert.match($("#table").textContent, /표시할 원문이 없어요/);
  t5.api.reset(); t5.api.detail();
  assert.match($("#detail").textContent, /보존 중인 대화 확인/);
});

test("a memory's sources show their origin and open each linked message", async () => {
  const t = setup(() => ({ authority: A, ref: { archive_id: "r1" }, text: "원문", text_offset: 0 }));
  const host = h.el("div"); document.body.append(host);
  const memory = { id: "m1", revision: 2 };
  t.state.selected = "m1";
  t.api.sourceDetails(host, memory, { authority: "x" });
  assert.match(host.textContent, /출처를 불러오지 못했어요/);
  t.api.sourceDetails(host, memory, { authority: A, memory: { id: "m1", revision: 2 }, sources: [] });
  assert.match(host.textContent, /연결된 출처가 없어요/);
  t.api.sourceDetails(host, memory, { authority: A, memory: { id: "m1", revision: 2 }, sources: [
    { origin_product: "claude_code", collected_at: "2026-01-01T00:00:00Z", origin_agent_id: "agent-7", excerpt: "근거", archive_messages: [{ archive_id: "r1" }, { archive_id: "r1", seq: 2 }] },
    { origin_product: "other", state: "erased" },
    {}
  ] });
  assert.match(host.textContent, /Claude · /);
  assert.match(host.textContent, /원래 실행 에이전트 {3}agent-7/);
  assert.match(host.textContent, /other · 시각 없음/);
  assert.match(host.textContent, /출처 · 시각 없음/);
  assert.match(host.textContent, /원문이 삭제됐어요/);
  assert.match(host.textContent, /정확한 대화 위치가 연결되지 않은 출처예요/);
  assert.match(host.textContent, /조회할 수 있는 근거 본문이 없어요/);
  const first = btn(host, "연결된 원문 1 열기");
  assert.ok(btn(host, "연결된 원문 2 열기"));
  first.click(); await dom.flush();
  assert.match(host.textContent, /원문/);
  assert.equal(t.calls[0].body.ref.archive_id, "r1");
  first.click(); assert.equal(first.getAttribute("aria-expanded"), "false");
  t.api.sourceDetails(host, memory, { authority: A, memory: { id: "m1", revision: 2 }, sources: [{ origin_product: "codex", archive_messages: [{ archive_id: "r1" }] }] });
  assert.ok(btn(host, "연결된 원문 열기"));
  host.remove();
});
