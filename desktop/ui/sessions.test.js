// Run with: node --test desktop/ui/*.test.js
// MEMORY › 세션 (sessions.js): list, filters, selection, export·delete and the archived dialogue, on a small DOM stand-in.
const test = require("node:test");
const assert = require("node:assert/strict");
const dom = require("./testdata/fake-dom.cjs");
const { create: createPager } = require("./memory-pages.js");

const settle = async (n = 8) => { for (let i = 0; i < n; i++) await new Promise((r) => setImmediate(r)); };
const HOUR = 3600000;
const session = (id, over = {}) => ({
  id, session_id: `sid-${id}`, title: `세션 ${id}`, agent: "claude_code", device: "pc", project: "arkism", messages: 4, tokens: 1200, cost_usd: 0.5, priced: true,
  started_at: new Date(Date.now() - 2 * HOUR).toISOString(), ended_at: new Date(Date.now() - HOUR).toISOString(), active_seconds: 90,
  input: 10, output: 20, cache_read: 1, cache_write: 2, requests: 3, models: [{ name: "opus", tokens: 9, cost_usd: 0.1, priced: true }],
  tools: [{ name: "Bash", count: 2 }], skills: [{ name: "review", count: 1 }], ...over
});
const page = (items, extra = {}) => ({ authority: "data_only_untrusted", items, next_cursor: "", total: items.length, ...extra });

async function setup(t, { agents = ["claude_code"], list = () => page([session("a"), session("b")]) } = {}) {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const { document, window } = dom.install();
  const h = dom.helpers(document);
  for (const id of ["table", "stats", "title-count", "controls", "chips", "detail"]) { const d = h.el("div"); d.id = id; document.body.append(d); }
  const { createRemoteCollection } = await import("./modules/remoteCollection.mjs");
  const { createVirtualList } = await import("./modules/virtualList.mjs");
  if (!window.ArkismSessions) require("./sessions.js");
  const state = { view: "sessions", server: "s", scope: { project_id: "p" }, connection: { generation: 1 } };
  const env = { calls: [], routes: { list, transcript: null, export: null, delete: null }, went: [], loaded: 0, asks: [], answer: true };
  globalThis.ask = async (m) => { env.asks.push(m); return env.answer; };
  const request = async (path, opts = {}) => {
    const body = opts.body ? JSON.parse(opts.body) : null;
    env.calls.push({ path, body });
    if (path.startsWith("/v1/usage/sessions?")) return env.routes.list(new URL(path, "http://x").searchParams);
    const name = path.split("/").pop();
    return env.routes[name](body);
  };
  const usage = {
    fmt: {
      num: (n) => String(n ?? 0), usd: (v, p) => (p ? `$${v}` : `≈$${v}`), dur: (s) => `${s}초`, logo: (k) => h.el("i", "logo", k), agentName: (a) => (a ? `A:${a}` : ""),
      bars: (list, v, l, f) => { const d = h.el("div", "bars"); for (const x of list || []) d.append(h.el("span", "", `${l(x)} ${v(x)}${f ? ` ${f(x)}` : ""}`)); return d; }
    },
    agents: () => agents, load: () => { env.loaded++; }
  };
  let view;
  const s = window.ArkismSessions({
    ...h, state, request, usage, render: () => view.render(), go: (v) => env.went.push(v),
    detailShell: (label) => { const body = h.el("div", "detail-body"); document.querySelector("#detail").replaceChildren(h.el("span", "label", label), body); return body; },
    remoteCollection: (read) => createRemoteCollection({ state, read, createPager }), virtualList: createVirtualList()
  });
  view = s.views.sessions;
  const $ = h.$;
  return { s, view, state, env, $, document, window, h, table: () => $("#table"), detail: () => $("#detail"), btn: (root, label) => dom.buttons(root, label)[0] };
}

test("the list loads once, selects the first session and shows its usage", async (t) => {
  const x = await setup(t);
  assert.equal(x.view.title, "세션");
  assert.match(x.view.sub(), /에이전트와 일한 기록/);
  assert.equal(x.view.keepDetail, true);
  assert.equal(x.s.count(), "");
  x.view.render();
  assert.match(x.table().textContent, /불러오는 중/);
  await settle();
  assert.equal(x.env.calls.filter((c) => c.path.startsWith("/v1/usage/sessions?")).length, 1);
  assert.equal(x.$("#title-count").textContent, "2개");
  assert.equal(x.s.count(), "2");
  assert.equal(x.table().querySelectorAll(".ss-item").length, 2);
  assert.match(x.table().textContent, /세션 2개/);
  assert.ok(x.table().querySelector(".ss-item.selected[data-id=\"a\"]") || x.detail().textContent.includes("세션 a"));
  const d = x.detail().textContent;
  assert.match(d, /A:claude_code · pc/);
  assert.match(d, /입력 10 · 출력 20/);
  assert.match(d, /Bash 2/);
  assert.match(d, /opus 9 \$0.1/);
  assert.match(d, /sid-a/);
  assert.match(d, /활동 90초/);
  // chips: 전체 + one per agent; the active chip shows the total
  const chips = x.$("#chips").querySelectorAll("button");
  assert.equal(chips.length, 2);
  assert.equal(chips[0].querySelector("output").textContent, "2");
  assert.equal(x.env.loaded, 0);
  assert.equal(x.view.head()[0].textContent, "새로고침");
});

test("an empty agent list asks usage to load, and the empty result says so", async (t) => {
  const x = await setup(t, { agents: [], list: () => page([]) });
  x.view.render();
  assert.equal(x.env.loaded, 1);
  await settle();
  assert.match(x.table().textContent, /조건에 맞는 세션이 없어요/);
  assert.equal(x.$("#chips").children.length, 0);
  assert.equal(x.detail().children.length, 0);
});

test("filters, chips and search reload with the chosen values", async (t) => {
  const x = await setup(t);
  x.view.render(); await settle();
  const last = () => new URL(x.env.calls.filter((c) => c.path.startsWith("/v1/usage/sessions?")).at(-1).path, "http://x").searchParams;
  const [range] = x.$("#controls").querySelectorAll("select");
  assert.deepEqual(range.options.map((o) => o.textContent), ["오늘", "최근 7일", "최근 30일", "최근 90일", "전체 기간"]);
  const before = x.env.calls.length;
  dom.input(range, "7d"); await settle(); // same range: no reload
  assert.equal(x.env.calls.length, before);
  dom.input(x.$("#controls select"), "30d"); await settle();
  assert.equal(last().get("range"), "30d");
  dom.input(x.$("#controls").querySelectorAll("select")[1], "cost"); await settle();
  assert.equal(last().get("sort"), "cost");
  // chip: picking the active one does nothing, another one filters by source
  const count = x.env.calls.length;
  x.$("#chips").querySelectorAll("button")[0].click(); await settle();
  assert.equal(x.env.calls.length, count);
  x.$("#chips").querySelectorAll("button")[1].click(); await settle();
  assert.equal(last().get("source"), "claude_code");
  // search waits 300ms, trims, and keeps focus in the rebuilt box
  const search = x.$("#controls input[type=search]");
  search.focus();
  dom.input(search, "  hello ");
  t.mock.timers.tick(299); await settle();
  assert.equal(last().get("q"), null);
  t.mock.timers.tick(1); await settle();
  assert.equal(last().get("q"), "hello");
  const again = x.$("#controls input[type=search]");
  assert.notEqual(again, search);
  assert.equal(x.document.activeElement, again);
});

test("errors: 404 means unsupported, others show the message, stale answers are dropped", async (t) => {
  const x = await setup(t, { list: () => { throw new Error("HTTP 404"); } });
  x.view.render(); await settle();
  assert.match(x.table().textContent, /아직 세션 목록을 지원하지 않아요/);
  x.s.reset();
  x.env.routes.list = () => { throw new Error("boom"); };
  x.view.render(); await settle();
  assert.match(x.table().textContent, /세션을 불러오지 못했어요 \(boom\)/);
  // refresh is silent and does nothing until a list is ready
  const n = x.env.calls.length;
  await x.s.refresh();
  assert.equal(x.env.calls.length, n);
  // a slower first answer loses to a newer load
  let release;
  x.env.routes.list = () => new Promise((r) => { release = r; });
  const first = x.s.load();
  const firstRelease = release;
  x.env.routes.list = () => page([session("z")]);
  x.s.reset();
  const second = x.s.load();
  firstRelease(page([session("old")]));
  await Promise.all([first, second]); await settle();
  x.view.render();
  assert.ok(x.table().querySelector("[data-id=\"z\"]"));
  assert.equal(x.table().querySelector("[data-id=\"old\"]"), null);
  // a stale failure is dropped too
  x.env.routes.list = () => new Promise((_, rej) => { release = rej; });
  const failing = x.s.load();
  const rej = release;
  x.env.routes.list = () => page([session("y")]);
  x.s.reset();
  const ok = x.s.load();
  rej(new Error("late"));
  await Promise.all([failing, ok]); await settle();
  x.view.render();
  assert.doesNotMatch(x.table().textContent, /late/);
});

test("more pages, a failed more and its retry", async (t) => {
  let fail = false;
  const x = await setup(t, { list: (q) => {
    if (q.get("cursor")) { if (fail) throw new Error("net"); return page([session("c")], { total: 3 }); }
    return page([session("a"), session("b")], { next_cursor: "n1", total: 3 });
  } });
  x.view.render(); await settle();
  assert.match(x.table().textContent, /3개 중 2개 표시/);
  fail = true;
  x.btn(x.table(), "세션 더 보기").click(); await settle();
  assert.match(x.table().textContent, /불러오지 못했어요 \(net\)/);
  fail = false;
  x.btn(x.table(), "다시 시도").click();
  assert.equal(x.btn(x.table(), "불러오는 중").disabled, true);
  await settle();
  assert.equal(x.table().querySelectorAll(".ss-item").length, 3);
  assert.equal(x.btn(x.table(), "세션 더 보기"), undefined);
  // a silent refresh failure is kept as a more error, not a blank screen
  x.env.routes.list = () => { throw new Error("quiet"); };
  await x.s.refresh();
  assert.match(x.table().textContent, /불러오지 못했어요 \(quiet\)/);
  assert.equal(x.table().querySelectorAll(".ss-item").length, 3);
});

test("rows open by click or Enter/space, and a repaint off-screen is skipped", async (t) => {
  const old = new Date(Date.now() - 30 * 24 * HOUR).toISOString();
  const x = await setup(t, { list: () => page([
    session("a"), session("b", { title: "", ended_at: new Date(Date.now() - 24 * HOUR).toISOString(), started_at: new Date(Date.now() - 25 * HOUR).toISOString(), device: "", project: "", tools: [], skills: [] }),
    session("c", { ended_at: new Date(Date.now() - 3 * 24 * HOUR).toISOString(), started_at: new Date(Date.now() - 3 * 24 * HOUR).toISOString(), priced: false }),
    session("d", { ended_at: old, started_at: old })
  ]) });
  x.view.render(); await settle();
  const metas = x.table().querySelectorAll(".v4s-meta").map((m) => m.textContent);
  assert.match(metas[0], /오늘 /);
  assert.match(metas[1], /어제/);
  assert.match(metas[2], /3일 전/);
  assert.doesNotMatch(metas[3], /오늘|어제|일 전/);
  assert.match(x.table().querySelectorAll(".ss-cost")[2].textContent, /\$0.5/);
  const row = (id) => x.table().querySelector(`[data-id="${id}"]`);
  row("b").click();
  assert.match(x.detail().textContent, /\(제목 없음\)/);
  assert.ok(row("b").classList.contains("selected"));
  row("b").click(); // same row again: stays
  const ev = new dom.Event("keydown", { key: "Enter", bubbles: true, cancelable: true });
  row("c").dispatchEvent(ev);
  assert.equal(ev.defaultPrevented, true);
  assert.ok(row("c").classList.contains("selected"));
  dom.fire(row("d"), "keydown", { key: " " });
  assert.ok(row("d").classList.contains("selected"));
  dom.fire(row("a"), "keydown", { key: "x" });
  assert.ok(row("d").classList.contains("selected"));
  // off-screen: the view is not redrawn
  x.state.view = "home";
  x.table().replaceChildren();
  await x.s.load(); await settle();
  assert.equal(x.table().children.length, 0);
});

test("selecting sessions: pick, pick all, export both formats and close", async (t) => {
  const x = await setup(t);
  const made = [], revoked = [];
  t.mock.method(URL, "createObjectURL", (blob) => { made.push(blob); return `blob:${made.length}`; });
  t.mock.method(URL, "revokeObjectURL", (u) => revoked.push(u));
  x.view.render(); await settle();
  x.$("#controls .ss-pick").click();
  assert.equal(x.$("#controls .ss-pick").textContent, "선택 끝내기");
  assert.match(x.table().textContent, /지울 세션을 골라요/);
  const box = x.table().querySelector("[data-id=\"a\"] input[type=checkbox]");
  box.click(); // stays on the row: no selection change through the row
  box.checked = true; dom.fire(box, "change");
  assert.match(x.table().textContent, /1개 선택/);
  const unbox = x.table().querySelector("[data-id=\"a\"] input[type=checkbox]");
  assert.equal(unbox.checked, true);
  unbox.checked = false; dom.fire(unbox, "change");
  assert.match(x.table().textContent, /지울 세션을 골라요/);
  x.btn(x.table(), "모두 선택").click();
  assert.match(x.table().textContent, /2개 선택/);
  // export markdown: default filename, notice
  x.env.routes.export = (b) => ({ content: "# hi", sessions: b.ids.length });
  x.btn(x.table(), "Markdown 내보내기").click();
  assert.ok(x.btn(x.table(), "JSON 내보내기").disabled);
  await settle();
  const exp = x.env.calls.find((c) => c.path === "/v1/usage/sessions/export");
  assert.deepEqual(exp.body, { scope: { project_id: "p" }, ids: ["a", "b"], format: "markdown", transcript: true });
  assert.equal(made[0].type, "text/plain");
  assert.match(x.detail().textContent, /세션 2개를 내보냈어요\./);
  t.mock.timers.tick(1000);
  assert.deepEqual(revoked, ["blob:1"]);
  // export json: truncated notice; a second click while busy is ignored
  let done;
  x.env.routes.export = () => new Promise((r) => { done = r; });
  x.btn(x.table(), "JSON 내보내기").click();
  x.btn(x.table(), "선택 해제").click();
  assert.match(x.detail().textContent, /내보내는 중/);
  const busy = x.env.calls.filter((c) => c.path.endsWith("/export")).length;
  done({ content: "{}", content_type: "application/json", filename: "x.json", sessions: 2, truncated: true });
  await settle();
  assert.equal(busy, 2);
  assert.match(x.detail().textContent, /일부 대화는 잘렸어요/);
  assert.equal(made[1].type, "application/json");
  // nothing picked: export does nothing; 닫기 leaves selection mode
  x.btn(x.table(), "닫기").click();
  assert.equal(x.table().querySelector(".ss-bar"), null);
  x.$("#controls .ss-pick").click();
  x.$("#controls .ss-pick").click();
  assert.equal(x.table().querySelector(".ss-bar"), null);
});

test("export failures and the detail's own export buttons", async (t) => {
  const x = await setup(t);
  x.view.render(); await settle();
  x.env.routes.export = () => ({ content: 3 });
  x.btn(x.detail(), "JSON").click(); await settle();
  assert.match(x.detail().textContent, /내보내지 못했어요 \(invalid_response\)/);
  t.mock.method(URL, "createObjectURL", () => "blob:z");
  t.mock.method(URL, "revokeObjectURL", () => {});
  x.env.routes.export = (b) => ({ content: "x", sessions: 1, filename: "", format: b.format });
  x.btn(x.detail(), "Markdown으로 내보내기").click(); await settle();
  assert.match(x.detail().textContent, /세션 1개를 내보냈어요/);
  x.env.routes.export = () => { throw new Error("down"); };
  x.btn(x.detail(), "JSON").click(); await settle();
  assert.match(x.detail().textContent, /내보내지 못했어요 \(down\)/);
});

test("delete asks first, then removes picked sessions and reloads", async (t) => {
  const x = await setup(t);
  x.view.render(); await settle();
  x.env.answer = false;
  x.btn(x.detail(), "지우기").click(); await settle();
  assert.match(x.env.asks[0], /세션 1개와 보관된 대화를 지울까요/);
  assert.equal(x.env.calls.some((c) => c.path.endsWith("/delete")), false);
  x.env.answer = true;
  x.env.routes.delete = () => { throw new Error("access_denied"); };
  x.btn(x.detail(), "지우기").click(); await settle();
  assert.match(x.detail().textContent, /지울 권한이 없어요/);
  x.env.routes.delete = () => { throw new Error("nope"); };
  x.btn(x.detail(), "지우기").click(); await settle();
  assert.match(x.detail().textContent, /지우지 못했어요 \(nope\)/);
  // pick both, delete; the server says a is deleted and b missing
  x.$("#controls .ss-pick").click();
  x.btn(x.table(), "모두 선택").click();
  x.env.routes.delete = (b) => ({ deleted: [b.ids[0]], missing: [b.ids[1]], archives: 1 });
  x.env.routes.list = () => page([session("c")]);
  x.btn(x.table(), "지우기").click(); await settle();
  assert.match(x.table().textContent, /세션 1개를 지웠어요 \(보관된 대화 1개\)/);
  assert.equal(x.table().querySelectorAll(".ss-item").length, 1);
  assert.match(x.detail().textContent, /세션 c/);
  // a picked row that vanished from a reload is dropped from the pick
  x.btn(x.table(), "모두 선택").click();
  x.env.routes.list = () => page([session("d")]);
  await x.s.load(); await settle();
  assert.match(x.table().textContent, /지울 세션을 골라요/);
  // server without archives/deleted fields
  x.btn(x.table(), "모두 선택").click();
  x.env.routes.delete = () => ({});
  x.btn(x.table(), "지우기").click(); await settle();
  assert.deepEqual(x.env.calls.filter((c) => c.path.endsWith("/delete")).at(-1).body.ids, ["d"]);
  assert.equal(x.env.calls.at(-1).path.startsWith("/v1/usage/sessions?"), true); // reloaded after the delete
});

test("the dialogue: states, marks, hidden, archives, more and close", async (t) => {
  const x = await setup(t);
  x.view.render(); await settle();
  const states = { none: /보관된 대화가 없어요/, expired: /만료됐거나 지워졌어요/, unsupported: /이 형식의 대화는 아직 볼 수 없어요/, active: /보여 줄 대화가 없어요/ };
  for (const [st, re] of Object.entries(states)) {
    x.env.routes.transcript = () => ({ authority: "data_only_untrusted", items: [], state: st });
    const open = x.btn(x.detail(), "대화 보기") || x.btn(x.detail(), "대화 닫기");
    if (open.textContent === "대화 닫기") { open.click(); x.btn(x.detail(), "대화 보기").click(); } else open.click();
    assert.match(x.detail().textContent, /불러오는 중/);
    await settle();
    assert.match(x.detail().textContent, re, st);
    assert.match(x.detail().textContent, /도구 호출 2회/);
  }
  x.btn(x.detail(), "대화 닫기").click();
  assert.ok(x.btn(x.detail(), "대화 보기"));
  // items with more pages; next page appends
  x.env.routes.transcript = (b) => b.after
    ? { authority: "data_only_untrusted", items: [{ role: "assistant", text: "둘째" }], state: "active", archives: ["a1"], archive_id: "a1" }
    : { authority: "data_only_untrusted", items: [{ role: "user", text: "첫째", truncated: true }], state: "active", archives: ["a1", "a2"], archive_id: "a1", next_cursor: "c1", hidden: { secret_records: 2 } };
  x.btn(x.detail(), "대화 보기").click(); await settle();
  let d = x.detail().textContent;
  assert.match(d, /사용자첫째/);
  assert.match(d, /길어서 앞부분만 보여요/);
  assert.match(d, /숨긴 메시지 2개/);
  assert.match(d, /보관본이 2개 있어요/);
  x.btn(x.detail(), "메시지 더 보기").click(); await settle();
  const more = x.env.calls.filter((c) => c.path.endsWith("/transcript")).at(-1).body;
  assert.equal(more.after, "c1"); assert.equal(more.archive_id, "a1");
  d = x.detail().textContent;
  assert.match(d, /에이전트둘째/);
  assert.equal(x.btn(x.detail(), "메시지 더 보기"), undefined);
  // errors
  x.btn(x.detail(), "대화 닫기").click();
  x.env.routes.transcript = () => { throw new Error("HTTP 404"); };
  x.btn(x.detail(), "대화 보기").click(); await settle();
  assert.match(x.detail().textContent, /아직 대화 보기를 지원하지 않아요/);
  x.btn(x.detail(), "대화 닫기").click();
  x.env.routes.transcript = () => ({ items: [] });
  x.btn(x.detail(), "대화 보기").click(); await settle();
  assert.match(x.detail().textContent, /대화를 불러오지 못했어요 \(invalid_response\)/);
});

test("a stale dialogue answer is ignored after the reader moves on", async (t) => {
  const x = await setup(t);
  x.view.render(); await settle();
  let release, reject;
  x.env.routes.transcript = () => new Promise((r) => { release = r; });
  x.btn(x.detail(), "대화 보기").click();
  x.btn(x.detail(), "대화 닫기").click();
  release({ authority: "data_only_untrusted", items: [{ role: "user", text: "늦음" }], state: "active" });
  await settle();
  assert.doesNotMatch(x.detail().textContent, /늦음/);
  x.env.routes.transcript = () => new Promise((_, r) => { reject = r; });
  x.btn(x.detail(), "대화 보기").click();
  x.table().querySelector("[data-id=\"b\"]").click();
  reject(new Error("late"));
  await settle();
  assert.doesNotMatch(x.detail().textContent, /late/);
});

test("a memory opens its session over the whole range with the quote marked", async (t) => {
  const x = await setup(t, { list: (q) => page(q.get("q") === "sid-b" ? [session("b")] : [session("a")]) });
  x.view.render(); await settle();
  let pages = 0;
  x.env.routes.transcript = (b) => {
    pages++;
    return b.after ? { authority: "data_only_untrusted", items: [{ role: "user", text: "앞 짧게 해줘 뒤" }], state: "active" }
      : { authority: "data_only_untrusted", items: [{ role: "user", text: "다른 말" }], state: "active", next_cursor: "n" };
  };
  x.window.dispatchEvent({ type: "arkism:open-session", detail: {} }); // no session id: ignored
  assert.deepEqual(x.env.went, []);
  x.window.dispatchEvent({ type: "arkism:open-session", detail: { session_id: "sid-b", quote: " 짧게 " } });
  assert.deepEqual(x.env.went, ["sessions"]);
  x.view.render(); await settle();
  t.mock.timers.tick(0); await settle();
  assert.equal(pages, 2);
  const hit = x.detail().querySelector("mark.ss-hit");
  assert.equal(hit.textContent, "짧게");
  assert.equal(hit.scrolledIntoView, 1);
  // repaint does not scroll again
  x.view.render(); t.mock.timers.tick(0);
  assert.equal(x.detail().querySelector("mark.ss-hit").scrolledIntoView, undefined);
  const q = new URL(x.env.calls.filter((c) => c.path.startsWith("/v1/usage/sessions?")).at(-1).path, "http://x").searchParams;
  assert.equal(q.get("range"), "all");
  // a session that is gone: a notice, and the first row is not picked in its place
  x.window.dispatchEvent({ type: "arkism:open-session", detail: { session_id: "sid-gone" } });
  x.view.render(); await settle();
  assert.match(x.table().textContent, /이 기억이 나온 세션을 찾지 못했어요/);
  assert.equal(x.$("#detail").children.length, 0);
});

test("open(id) from usage widens the range only when the session is not listed", async (t) => {
  const x = await setup(t);
  x.view.render(); await settle();
  x.s.open("b");
  assert.deepEqual(x.env.went, ["sessions"]);
  x.view.render();
  assert.match(x.detail().textContent, /세션 b/);
  x.s.open("zzz");
  x.view.render(); await settle();
  const q = new URL(x.env.calls.filter((c) => c.path.startsWith("/v1/usage/sessions?")).at(-1).path, "http://x").searchParams;
  assert.equal(q.get("range"), "all");
  assert.equal(x.$("#detail").children.length, 0);
});
