// The "들어가는 기억" popup (inject.js): effect stats, the SessionStart preview by repository and a prompt try-out,
// against a stand-in API. Run: node --test desktop/ui/*.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
require("./keep.js"); // the shared stale-reply and redraw guards the screens use
const dom = require("./testdata/dom.js");

const { window, document, timers } = dom.install();
require("./inject.js");
const { $, text, flush } = dom;

const el = (tag, cls, t) => { const n = document.createElement(tag); if (cls) n.className = cls; if (t != null) n.textContent = t; return n; };
const button = (label, cls, icon, fn) => { const b = el("button", cls, label); b.type = "button"; if (fn) b.addEventListener("click", fn); return b; };
const section = (title, sub) => { const s = el("section"); s.append(el("h3", "", title)); if (sub) s.append(el("p", "sub", sub)); return s; };
const facts = (rows) => { const dl = el("dl"); for (const [k, v] of rows) dl.append(el("dt", "", k + " "), el("dd", "", v)); return dl; };

const use = (days, extra = {}) => ({ days, sessions: 10, injections: 4, used: 3, inject_sessions: 2, inject_chars: 2000,
  items: [{ id: "m1", injected: 3, used: 2 }, { id: "gone", injected: 1, used: 1 }, { id: "m2", injected: 2, used: 0 }, { id: "m3", injected: 2, used: 0 }], ...extra });
const memories = [
  { id: "m1", kind: "user", text: "보고는 짧게", tags: [] },
  { id: "m2", kind: "procedural", text: "테스트 먼저", tags: ["repo:o/a"] },
  { id: "m3", kind: "decision", text: "다른 저장소", tags: ["repo:o/b"] }
];
const state = { server: "s", token: "t", scope: { workspace_id: "w" }, data: { memories, tools: { memory_use: use(7), memory_use_30: use(30) } } };
const calls = [];
let api = {};
async function request(path, opts = {}) {
  calls.push([opts.method || "GET", path, opts.body && JSON.parse(opts.body)]);
  const route = Object.keys(api).find((k) => path.startsWith(k));
  const r = route ? api[route] : null;
  if (typeof r === "function") return r(path, opts);
  if (r instanceof Error) throw r;
  return r;
}
const query = (o) => new URLSearchParams(o).toString();
const inject = window.ArkismInject({ state, el, button, request, query, section, facts });
const dlg = () => $("dialog.inject-dialog");
const settle = async () => { await timers.tick(1); await flush(); };

test("opens from its button, shows effect stats and the SessionStart preview with token counts", async () => {
  api = {
    "/v1/memories": (p) => ({ items: memories.filter((m) => p.includes(`kind=${m.kind}`)) }),
    "/v1/context/tokens": (_, o) => ({ tokens: JSON.parse(o.body).text.length * 2, token_encoding: "o200k_base" })
  };
  const b = inject.button();
  assert.equal(b.textContent, "들어가는 기억");
  b.click();
  await settle();
  const d = dlg();
  assert.ok(d.open);
  const t = text(d);
  assert.match(t, /최근 7일 4번 들어감 · 문구 겹침 3번 · 75% · 대화 10개/);
  assert.match(t, /세션당 평균 들어간 양 1,000자 · 약/);
  assert.match(t, /토큰은 아래 미리보기의 글자당 토큰 비율로/);
  assert.match(t, /보고는 짧게 · 문구 겹침 2번 \/ 3번 들어감/);
  assert.match(t, /gone · 문구 겹침 1번/); // an id the list does not hold shows as the id
  assert.match(t, /o200k_base 기준/);
  assert.match(t, /들어가는 기억 1개/);
  assert.match(t, /다른 저장소 전용 기억 1개는 이 폴더에서 빠져요/);
  assert.deepEqual(calls.filter(([, p]) => p.startsWith("/v1/memories")).map(([, p]) => new URLSearchParams(p.split("?")[1]).get("kind")), ["user", "procedural"]);
  // choosing a repository reloads; the token count for a repeated text comes from the cache
  const sel = d.querySelector("select");
  assert.deepEqual([...sel.options].map((o) => o.value), ["", "o/a", "o/b"]);
  sel.value = "o/a";
  dom.fire(sel, "change");
  assert.match(text(dlg()), /불러오는 중…/);
  await settle();
  assert.match(text(dlg()), /들어가는 기억 2개/);
  const tokenCalls = calls.filter(([, p]) => p === "/v1/context/tokens").length;
  dlg().querySelector("select").value = "o/a";
  dom.fire(dlg().querySelector("select"), "change");
  await settle();
  assert.equal(calls.filter(([, p]) => p === "/v1/context/tokens").length, tokenCalls);
  dlg().querySelector("button[aria-label=닫기]").click();
  assert.equal(dlg(), null);
});

test("a prompt try-out ignores short text, falls back to lexical search on 503 and says when nothing is added", async () => {
  window.dispatchEvent(new dom.Event("arkism:inject-preview"));
  await settle();
  const form = () => dlg().querySelector("form.inject-try");
  const ask = async (q) => { form().querySelector("textarea").value = q; form().requestSubmit(); await settle(); };
  await ask("짧은 질문");
  assert.match(text(dlg()), /8자보다 짧은 질문에는 아무것도 넣지 않아요/);
  let first = true;
  api["/v1/search"] = (_, o) => {
    const body = JSON.parse(o.body);
    if (first) { first = false; throw new Error("HTTP 503"); }
    assert.deepEqual(body.modes, ["exact", "fts"]);
    return { items: [{ id: "m1", kind: "user", text: "보고는 짧게", tags: [] }] };
  };
  await ask("배포 전에 무엇을 확인해야 해?");
  assert.match(text(dlg()), /UserPromptSubmit/);
  assert.equal(dlg().querySelector("textarea").value, "배포 전에 무엇을 확인해야 해?");
  api["/v1/search"] = () => ({});
  await ask("관련 기억이 없는 질문이에요");
  assert.match(text(dlg()), /이 질문에는 더 들어가는 기억이 없어요/);
  api["/v1/search"] = () => { throw new Error("HTTP 500"); };
  await ask("검색이 실패하는 질문이에요");
  assert.match(text(dlg()), /찾지 못했어요 \(HTTP 500\)/);
  api["/v1/search"] = () => ({ items: [{ id: "x", kind: "user", text: "토큰을 못 세는 글", tags: [] }] });
  api["/v1/context/tokens"] = () => ({ tokens: "many" });
  await ask("토큰 수를 셀 수 없는 질문");
  assert.match(text(dlg()), /토큰 수는 계산하지 못했어요/);
});

test("a changed connection starts fresh; failures, missing stats and empty memories show their notes", async () => {
  state.token = "other";
  state.data.tools = { memory_use: use(7, { inject_sessions: 0, injections: 0, items: [] }) };
  api = { "/v1/memories": new Error("offline"), "/v1/context/tokens": () => ({ tokens: 1 }) };
  window.dispatchEvent(new dom.Event("arkism:inject-preview"));
  await settle();
  let t = text(dlg());
  assert.match(t, /불러오지 못했어요 \(offline\)/);
  assert.match(t, /들어간 기억 없음 · 대화 10개/);
  assert.match(t, /문구 겹침이 확인된 기억이 아직 없어요/);
  assert.equal(dlg().querySelector("textarea").value, ""); // the old question is gone
  state.data.tools = {};
  state.data.memories = [];
  state.token = "third";
  api["/v1/memories"] = () => ({ items: "bad" });
  window.dispatchEvent(new dom.Event("arkism:inject-preview")); // a second open replaces the first
  await settle();
  t = text(dlg());
  assert.equal(document.querySelectorAll("dialog.inject-dialog").length, 1);
  assert.match(t, /아직 서버가 문구 겹침 통계를 보내지 않았어요/);
  assert.match(t, /새 세션에 들어갈 기억이 없어요/);
  // an answer that arrives after the popup closed is dropped quietly
  let release;
  state.token = "fourth";
  api["/v1/memories"] = () => new Promise((r) => { release = r; });
  window.dispatchEvent(new dom.Event("arkism:inject-preview"));
  await settle();
  dlg().close();
  release({ items: [] });
  await settle();
  assert.equal(dlg(), null);
});

test("memory page tiles show the overlap rate over 7 and 30 days and the review count", () => {
  const stat = (label, value, unit, warn) => ({ label, value, unit, warn: !!warn });
  state.data.tools = { memory_use: use(7, { injections: 0, used: 0 }), memory_use_30: use(30) };
  const tiles = inject.tiles(stat, 2);
  assert.deepEqual(tiles.map((x) => [x.label, x.value]), [["최근 7일 문구 겹침 비율", "—"], ["최근 30일 문구 겹침 비율", "75"], ["세션당 평균 들어간 양", "1,000"], ["문구 겹침을 검토할 기억", "2"]]);
  assert.equal(tiles[3].warn, true);
  state.data.tools = { memory_use: use(7, { inject_sessions: 0 }) };
  assert.deepEqual(inject.tiles(stat, 0).map((x) => x.label), ["최근 7일 문구 겹침 비율", "문구 겹침을 검토할 기억"]);
});
