// Run with: node --test desktop/ui/*.test.js
const test = require("node:test");
const assert = require("node:assert/strict");
require("./keep.js"); // the shared typed-list reader
const dom = require("./testdata/fake-dom.cjs");

const SCOPE = { workspace_id: "w", project_id: "p" };
const mem = (id, o = {}) => ({ id, text: `기억 ${id}`, kind: "user", status: "active", created_at: "2026-01-01T00:00:00Z", ...o });

function fixtures() {
  const t1 = { id: "t1", title: "응답 습관", body: "짧게 답함", fresh: true, status: "active", revision: 2, updated_at: "2026-01-05T00:00:00Z", outdated: true,
    review: { at: "2026-01-04T00:00:00Z" }, heat: { score: 10, injected: 3, used: 1 }, members: ["m1", "m2", "m3", "m4"].map((id) => ({ id })) };
  const t2 = { id: "t2", title: "가나 주제", body: "조용함", fresh: false, status: "active", revision: 1, updated_at: "2026-01-02T00:00:00Z", heat: { score: 1 } };
  const c1 = { id: "c1", revision: 3, proposal: "update", fresh: true, tags: ["review:instruction_like"], updated_at: "2026-01-06T00:00:00Z", members: [{ id: "m1" }, { id: "m4" }, { id: "m3" }],
    diff: { base: [{ id: "t1" }], title_before: "응답 습관", title_after: "응답 습관 v2", members_added: [{ id: "m9" }, { id: "m8" }], members_removed: [{ id: "m3" }],
      lines: [{ op: "-", text: "짧게 답함" }, { op: "+", text: "아주 짧게 답함" }, { op: " ", text: "그대로" }] } };
  const c2 = { id: "c2", revision: 1, proposal: "new", fresh: false, title: "새 주제", body: "a", updated_at: "2026-01-03T00:00:00Z" };
  const c3 = { id: "c3", revision: 1, proposal: "merge", fresh: true, body: "합친 줄\n둘째 줄", updated_at: "2026-01-01T00:00:00Z", members: [],
    diff: { base: [{ id: "t1" }, { id: "t2" }], title_before: "응답 습관 + 가나 주제" } };
  const u1 = { id: "u1", body: "나는 짧은 답을 좋아함", fresh: true, status: "active", revision: 2, updated_at: "2026-01-05T00:00:00Z", members: [{ id: "m1" }], review: { at: "2026-01-05T00:00:00Z", action: "approve" } };
  const uc1 = { id: "uc1", revision: 3, judge: "conflict", fresh: true, body: "새 요약", members: [{ id: "m2" }] };
  const uo1 = { id: "uo1", revision: 1, status: "superseded", updated_at: "2026-01-01T00:00:00Z" };
  const mp = [
    { id: "p1", revision: 1, created_at: "2026-01-05T00:00:00Z", merge: { proposal: { action: "merge", candidate: { id: "k1" }, targets: [{ id: "m1" }], reason_code: "same_meaning", reason: "같은 뜻이에요" } } },
    { id: "p2", revision: 1, created_at: "2026-01-05T00:00:00Z", merge: { proposal: { action: "store", candidate: { id: "k2" }, reason_code: "new_information" } } },
    { id: "p3", revision: 1, created_at: "2026-01-05T00:00:00Z", merge: { proposal: { action: "skip", candidate: { id: "k1" }, targets: [{ id: "m5" }, { id: "m6" }] } } },
    { id: "p4", revision: 1, merge: {} }
  ];
  return { t1, t2, c1, c2, c3, u1, uc1, uo1, mp };
}

function setup(opts = {}) {
  const { document } = dom.install();
  delete require.cache[require.resolve("./digests.js")];
  require("./digests.js");
  require("./text-diff.js");
  const h = dom.helpers(document);
  for (const id of ["app", "controls", "chips", "stats", "table", "detail"]) { const n = h.el("div"); n.id = id; document.body.append(n); }
  const f = fixtures();
  const state = { view: "memories", memTab: "digests", selected: null, server: "s", token: "t", scope: { ...SCOPE }, query: "",
    data: { memories: [mem("m1"), mem("m2", { status: "superseded" }), mem("m3")], candidates: [mem("k1", { text: "새 후보 본문" })] } };
  const calls = [], notices = [], opened = [];
  let renders = 0;
  const routes = {
    "/v1/digests?": (path) => {
      const q = new URL(path, "http://x").searchParams, kind = q.get("kind"), status = q.get("status");
      if (opts.digestsDown) throw new Error("down");
      if (kind === "topic" && status === "active") return { items: [f.t1, f.t2], cap: 2, active: 2 };
      if (kind === "topic" && status === "candidate") return { items: [f.c1, f.c2, f.c3] };
      if (kind === "topic" && status === "rejected") return { items: [{ ...f.t2, id: "r1", title: "거절된 주제" }] };
      if (kind === "topic") return {};
      if (kind === "user" && status === "active") return { items: opts.noUser ? [] : [opts.staleUser ? { ...f.u1, fresh: false } : f.u1] };
      if (kind === "user" && status === "candidate") return { items: opts.noUserCand ? [] : [f.uc1] };
      return { items: [f.uo1] };
    },
    "/v1/merge_proposals?": () => { if (opts.noMerge) throw new Error("not_found"); return { items: f.mp }; },
    "/v1/merge_proposals/p1?": () => ({ proposal: { id: "p1" }, current: true, candidate: mem("k1", { text: "보는 후보" }), targets: [mem("m1", { text: "보는 기억" })], notice: "서버 안내" }),
    "/v1/merge_proposals/p2?": () => ({ proposal: { id: "p2" }, current: false }),
    "/v1/merge_proposals/p3?": () => { throw new Error("gone"); },
    "/v1/memories/m4?": () => mem("m4"),
    "/v1/memories/m8?": () => ({ id: "other" }),
    "/v1/memories/m9?": () => { throw new Error("gone"); },
    "/v1/memories/m5?": () => mem("m5", { kind: "lesson" }),
    "/v1/memories/m6?": () => mem("m6"),
    "/v1/memory_candidates/k2?": () => mem("k2", { text: "저장할 후보" }),
    "/v1/causal-links?": () => { throw new Error("not_found"); }, // an older server without causal links
    ...opts.routes
  };
  const request = async (path, o) => {
    calls.push([path, o]);
    for (const [p, fn] of Object.entries(routes)) if (path.startsWith(p)) return fn(path, o);
    throw new Error(`unexpected ${path}`);
  };
  const query = (o = {}) => new URLSearchParams(Object.entries({ ...state.scope, ...o }).filter(([, v]) => v !== "")).toString();
  const detailShell = (label) => { const body = h.el("div", "detail-body"); h.$("#detail").replaceChildren(h.el("span", "", label), body); return body; };
  const markSelected = () => document.querySelectorAll(".row[data-id]").forEach((r) => r.classList.toggle("selected", r.dataset.id === state.selected));
  const row = (id, template, cells, onClick) => {
    const r = h.el("button", `row${state.selected === id ? " selected" : ""}`); r.type = "button"; r.dataset.id = id; r.append(...cells);
    r.addEventListener("click", () => { state.selected = id; markSelected(); onClick(); }); return r;
  };
  const section = (title, aside) => { const s = h.el("section", "section"), hd = h.el("h3", "", title); if (aside) hd.append(h.el("small", "", aside)); s.append(hd); return s; };
  const facts = (pairs) => { const dl = h.el("dl", "facts"); pairs.filter(([, v]) => v).forEach(([k, v]) => dl.append(h.el("dt", "", k), h.el("dd", "", v))); return dl; };
  const memSelect = (label, options, value, onChange) => {
    const s = h.el("select"); s.setAttribute("aria-label", label); s.value = value;
    for (const [k, l] of options) { const o = h.el("option", "", l); o.value = k; s.append(o); }
    s.addEventListener("change", () => onChange(s.value)); return s;
  };
  const segment = (tabs, cur, onPick) => { const sg = h.el("div", "segment"); for (const [k, l] of tabs) sg.append(h.button(l, k === cur ? "tab active" : "tab", null, () => onPick(k))); return sg; };
  const gone = [];
  const go = (v, tab) => { gone.push([v, tab]); state.memTab = tab; state.selected = null; };
  let view;
  const ctx = { segment, go, state, $: h.$, el: h.el, raw: h.raw, tr: (s) => s, icon: h.icon, button: h.button, request, query, detailShell,
    openDetail: () => h.$("#app").classList.add("detail-open"), notice: (m) => notices.push(m), ago: () => "방금", render: () => { renders++; if (state.memTab === "merge") view.drawMerge(); else if (state.view === "memories") view.draw(); },
    row, markSelected, kindLabel: (k) => `종류:${k}`, originOf: () => "Codex", memSelect, section, facts, openRecord: (r) => opened.push(r),
    reloadMemories: async () => { if (opts.reloadFails) throw new Error("x"); }, canRefresh: opts.canRefresh };
  view = window.ArkismDigests(ctx);
  return { view, state, calls, notices, opened, gone, h, document, f, renders: () => renders };
}
const rows = (t) => t.h.$("#table").querySelectorAll(".row");
const detailText = (t) => t.h.$("#detail").textContent;
const btn = (root, label) => dom.buttons(root, label)[0];

test("모아보기 lists topics with their waiting candidates, filters, sorts and searches", async () => {
  const t = setup();
  t.view.draw();
  assert.match(t.h.$("#table").textContent, /요약을 불러오는 중/);
  assert.equal(t.view.title(), "");
  await dom.flush();
  assert.equal(t.view.title(), "주제 2개");
  // the first candidate entry is picked once
  assert.equal(t.state.selected, "digest:t1");
  assert.equal(rows(t).length, 4); // t1 (+c1), t2, c2, c3
  assert.match(t.h.$("#controls").textContent, /확인해 주세요4/);
  assert.match(t.h.$("#table").textContent, /최대 2개 · 최대 개수에 닿아서/);
  assert.match(t.h.$("#table").textContent, /바탕 기억이 수정됨/);
  const chips = t.h.$("#chips").querySelectorAll("button");
  assert.deepEqual(chips.map((c) => c.textContent), ["전체4", "요즘 자주 씀", "새 후보3", "한동안 안 씀1"]);
  chips[3].click();
  assert.equal(rows(t).length, 1);
  assert.match(rows(t)[0].textContent, /가나 주제.*한동안 안 씀/);
  t.h.$("#chips button").click();
  const [status, sort] = t.h.$("#controls").querySelectorAll("select");
  for (const v of ["recent", "name", "heat"]) { sort.value = v; dom.fire(sort, "change"); }
  assert.match(rows(t)[0].textContent, /응답 습관/);
  dom.input(t.h.$("#controls input"), "가나");
  assert.equal(rows(t).length, 1);
  dom.input(t.h.$("#controls input"), "zzz");
  assert.match(t.h.$("#table").textContent, /검색과 맞는 주제가 없어요/);
  dom.input(t.h.$("#controls input"), "");
  status.value = "rejected"; dom.fire(status, "change");
  await dom.flush();
  assert.equal(rows(t).length, 1);
  assert.match(rows(t)[0].textContent, /거절된 주제.*거절됨/);
  status.value = "stale"; dom.fire(status, "change");
  await dom.flush();
  assert.match(t.h.$("#table").textContent, /여기에 표시할 요약이 없어요/);
  status.value = "rejected"; dom.fire(status, "change"); // cached
  status.value = "active"; dom.fire(status, "change");
  // other tabs and views do not paint the list
  t.state.memTab = "idle"; t.view.draw();
  t.state.view = "todo"; t.view.draw();
});

test("a topic detail shows the candidate diff, members and approves or rejects with a reason", async () => {
  const t = setup({ routes: { "/v1/digests/c1/decide": (_, o) => { const b = JSON.parse(o.body); if (b.decision === "reject" && b.reason === "conflict") throw new Error("revision_conflict"); return {}; } } });
  t.view.draw(); await dom.flush();
  rows(t)[0].click();
  await dom.flush();
  let d = detailText(t);
  assert.doesNotMatch(d, /열기 \d/);
  assert.match(d, /지시처럼 보이는 문장 포함/);
  assert.match(d, /응답 습관 → 응답 습관 v2/);
  assert.match(d, /새 기억 2개가 들어와서/);
  assert.match(d, /빠진 기억 1개/);
  assert.match(d, /5개 모두 보기/);
  assert.match(d, /읽을 수 없는 기억이에요/); // m9 failed, m8 has the wrong id
  assert.match(d, /2026|1월/);
  assert.equal(t.h.$("#detail").querySelectorAll(".dg-line.del").length, 1);
  assert.match(d, /3번 참고 · 1번 사용/);
  // the active member opens; an unreadable one is disabled
  const items = t.h.$("#detail").querySelectorAll(".dg-item");
  items.find((i) => /기억 m1/.test(i.textContent)).click();
  assert.equal(t.opened[0].id, "m1");
  assert.ok(items.find((i) => /읽을 수 없는/.test(i.textContent)).disabled);
  btn(t.h.$("#detail"), "5개 모두 보기").click();
  assert.match(detailText(t), /기억 m4|불러오는 중/);
  // 버리기 first opens the reason box; typing marks the detail dirty so background repaints wait
  const reason = t.h.$("#detail .dg-reason");
  btn(t.h.$("#detail .dg-cand"), "버리기").click();
  assert.equal(reason.hidden, false);
  dom.input(reason, "conflict");
  assert.ok(reason.hasAttribute("data-sync-dirty"));
  t.view.detail(true); // soft detail keeps the dirty box
  assert.equal(t.h.$("#detail .dg-reason"), reason);
  btn(t.h.$("#detail .dg-cand"), "버리기").click();
  btn(t.h.$("#detail .dg-cand"), "버리기").click(); // busy: ignored
  await dom.flush();
  assert.match(t.notices[0], /바탕이 바뀌었거나/);
  const decides = t.calls.filter(([p]) => p.includes("/decide"));
  assert.equal(decides.length, 1);
  assert.deepEqual(JSON.parse(decides[0][1].body), { scope: SCOPE, kind: "topic", expected_revision: 3, decision: "reject", reason: "conflict" });
  rows(t)[0].click();
  btn(t.h.$("#detail .dg-cand"), "승인").click();
  await dom.flush();
  assert.equal(t.notices[1], "요약을 바꿨어요. 이전 버전은 이력에 남아요.");
  rows(t)[0].click();
  const no = btn(t.h.$("#detail .dg-cand"), "버리기"); no.click(); no.click();
  await dom.flush();
  assert.equal(t.notices[2], "후보를 버렸어요.");
});

test("loose candidates, quiet topics, regenerate and the empty detail", async () => {
  let regen = () => ({});
  const t = setup({ routes: { "/v1/digests/regenerate": () => regen() } });
  t.view.draw(); await dom.flush();
  const byText = (s) => rows(t).find((r) => r.querySelector(".text").textContent === s);
  byText("새 주제").click();
  assert.match(detailText(t), /주제 후보 · 새 주제/);
  assert.match(detailText(t), /새 주제를 묶었어요/);
  assert.match(detailText(t), /쓸 수 없어요/);
  assert.ok(btn(t.h.$("#detail"), "승인").disabled);
  assert.match(detailText(t), /묶인 기억이 없어요/);
  byText("제목 없음").click();
  assert.match(detailText(t), /두 요약을 하나로 합치자는/);
  assert.match(detailText(t), /응답 습관 \+ 가나 주제 →/);
  assert.equal(t.h.$("#detail").querySelectorAll(".dg-line.add").length, 2);
  byText("가나 주제").click();
  assert.match(detailText(t), /바탕 기억이 바뀌거나/); // not fresh
  btn(t.h.$("#detail"), "다시 만들기").click(); await dom.flush();
  assert.equal(t.notices.at(-1), "다음 야간 실행 때 새 후보로 나와요.");
  assert.deepEqual(JSON.parse(t.calls.at(-1)[1].body), { scope: SCOPE, kind: "topic", id: "t2" });
  assert.ok(btn(t.h.$("#detail"), "다시 만들기").disabled); // stays off after a request went through
  t.view.detail();
  regen = () => { throw new Error("access_denied"); };
  btn(t.h.$("#detail"), "다시 만들기").click(); await dom.flush();
  assert.match(t.notices.at(-1), /다시 만들 수 없어요/);
  regen = () => { throw new Error("boom"); };
  const again = btn(t.h.$("#detail"), "다시 만들기"); again.click(); await dom.flush();
  assert.match(t.notices.at(-1), /요청하지 못했어요 \(boom\)/);
  assert.equal(again.disabled, false);
  t.view.close();
  assert.match(detailText(t), /주제를 고르세요/);
  // an update candidate without added members
  t.state.selected = "digest:t1";
  t.f.c1.diff.members_added = []; t.f.c1.diff.lines = []; t.f.c1.body = "줄 하나";
  t.view.detail();
  assert.match(detailText(t), /AI가 요약을 고쳤어요/);
});

test("나에 대한 요약: card, candidate approval, history and states without a summary", async () => {
  const t = setup({ routes: { "/v1/digests/uc1/decide": () => { throw new Error("access_denied"); }, "/v1/digests/regenerate": () => ({}) } });
  t.view.draw(); await dom.flush();
  const card = t.h.$("#controls .dg-user");
  assert.match(card.textContent, /새 후보.*나는 짧은 답을 좋아함.*기억 1개에서/);
  btn(t.h.$("#controls"), "하나씩 확인하기").click();
  assert.equal(t.state.selected, "digest:t1");
  card.click();
  assert.equal(t.state.selected, "digest:user");
  assert.ok(t.h.$("#controls .dg-user").classList.contains("selected"));
  const d = detailText(t);
  assert.match(d, /기존과 충돌/);
  assert.match(d, /최근 대화에서 바뀐 습관/);
  assert.match(d, /근거가 된 기억/);
  assert.match(d, /v2 · 지금 쓰는 중 · 승인/);
  assert.match(d, /v1 · 이전 버전/);
  assert.match(d, /9자|길이/);
  btn(t.h.$("#detail .dg-cand"), "승인").click(); await dom.flush();
  assert.equal(t.notices[0], "이 계정은 요약을 승인할 수 없어요.");
  t.state.selected = "digest:user"; t.view.detail();
  btn(t.h.$("#detail"), "다시 만들기").click(); await dom.flush();
  assert.deepEqual(JSON.parse(t.calls.at(-1)[1].body), { scope: SCOPE, kind: "user" });
});

test("user summary variants: active only, stale, candidate only and none", async () => {
  const only = setup({ noUserCand: true, routes: { "/v1/digests/uc1/decide": () => ({}) } });
  only.view.draw(); await dom.flush();
  assert.match(only.h.$("#controls").textContent, /확인해 주세요3/);
  only.h.$("#controls .dg-user").click();
  assert.match(detailText(only), /지금 요약v2/);
  const stale = setup({ staleUser: true, noUserCand: true });
  stale.view.draw(); await dom.flush();
  assert.match(stale.h.$("#controls .dg-user").textContent, /바탕 기억이 바뀌거나 지워져/);
  stale.h.$("#controls .dg-user").click();
  assert.match(detailText(stale), /바탕 기억이 바뀌거나/);
  const cand = setup({ noUser: true, routes: { "/v1/digests/uc1/decide": () => ({}) } });
  cand.view.draw(); await dom.flush();
  assert.match(cand.h.$("#controls .dg-user").textContent, /후보를 확인해 주세요/);
  cand.h.$("#controls .dg-user").click();
  assert.match(detailText(cand), /승인하면 새 세션 시작 때/);
  btn(cand.h.$("#detail .dg-cand"), "승인").click(); await dom.flush();
  assert.equal(cand.notices[0], "나에 대한 요약을 바꿨어요. 새 세션부터 쓰여요.");
  const none = setup({ noUser: true, noUserCand: true });
  none.view.draw(); await dom.flush();
  assert.match(none.h.$("#controls .dg-user").textContent, /아직 요약이 없어요/);
  none.h.$("#controls .dg-user").click();
  assert.doesNotMatch(detailText(none), /지금 요약/);
});

test("counts for the memory screen and HOME menu rows", async () => {
  const t = setup();
  assert.equal(t.view.mergeCount(), null);
  await dom.flush();
  assert.equal(t.view.mergeCount(), 3);
  assert.deepEqual(t.view.digestCounts(), { topic: 3, user: 1 });
  const menu = [["MEMORY", "기억", [{ label: "x", n: 1 }], ["memories", "active"]], ["TOOLS", "스킬", [], ["skills"]]];
  t.view.menuRows(menu);
  assert.deepEqual(menu[0][2].at(-1), { label: "합치기 제안 3", n: 3 });
  assert.deepEqual(menu[1], ["MEMORY", "모아보기", [{ label: "주제 요약 바뀜 3", n: 3 }, { label: "나에 대한 요약 바뀜 1", n: 1 }], ["memories", "digests"]]);
  const noMemory = [];
  t.view.menuRows(noMemory);
  assert.equal(noMemory[0][1], "모아보기");
  // a stale cache refreshes; canRefresh false holds it
  t.view.refresh(); await dom.flush();
  const down = setup({ noMerge: true, canRefresh: () => false, digestsDown: true });
  down.view.mergeCount(); await dom.flush();
  assert.equal(down.view.mergeCount(), 0);
  assert.deepEqual(down.view.digestCounts(), { topic: 0, user: 0 });
  down.view.refresh(); await dom.flush();
  // identity change while loading drops the result
  const moved = setup();
  moved.view.mergeCount();
  moved.state.token = "other";
  await dom.flush();
  assert.equal(moved.view.mergeCount(), null);
});

test("repaint follows the screen: HOME and memory tabs render, dirty detail holds", async () => {
  const home = setup();
  home.state.view = "todo";
  home.view.mergeCount(); await dom.flush();
  assert.equal(home.renders(), 1);
  const other = setup();
  other.state.view = "skills";
  other.view.mergeCount(); await dom.flush();
  assert.equal(other.renders(), 0);
  const dirty = setup();
  const box = dirty.h.el("textarea"); box.setAttribute("data-sync-dirty", ""); dirty.h.$("#detail").append(box);
  dirty.view.mergeCount(); await dom.flush();
  assert.equal(dirty.renders(), 0);
  assert.equal(rows(dirty).length, 4);
  dirty.view.detail(true);
  assert.ok(dirty.h.$("#detail textarea"));
});

test("병합 제안 lists AI proposals and accepts, edits or dismisses them", async () => {
  const decided = [];
  const t = setup({ routes: { "/v1/merge_proposals/p1/decide": (_, o) => { decided.push(JSON.parse(o.body)); return {}; },
    "/v1/merge_proposals/p2/decide": () => { throw new Error("revision_conflict"); },
    "/v1/merge_proposals/p3/decide": (_, o) => { if (JSON.parse(o.body).decision === "accept") throw new Error("access_denied"); throw new Error("weird"); } } });
  t.state.memTab = "merge";
  t.view.drawMerge();
  assert.match(t.h.$("#table").textContent, /제안을 불러오는 중/);
  await dom.flush();
  assert.equal(rows(t).length, 3);
  const table = t.h.$("#table").textContent;
  assert.match(table, /새 후보 본문AI 제안: 합치기 · 대상 “기억 m1”/);
  assert.match(table, /저장할 후보AI 제안: 저장 · 새 정보/);
  assert.match(table, /대상 “기억 m5”/);
  assert.match(t.h.$(".dg-merge-note")?.textContent || "", /고르면 그때 저장돼요/, "the note sits under the list, outside the rows");
  t.state.query = "같은 뜻"; t.view.drawMerge();
  assert.equal(rows(t).length, 1);
  t.state.query = "없음없음"; t.view.drawMerge();
  assert.match(t.h.$("#table").textContent, /검색과 맞는 제안이 없어요/);
  t.state.query = ""; t.view.drawMerge();
  t.view.detail();
  assert.match(detailText(t), /제안을 고르세요/);
  rows(t)[0].click();
  assert.ok(btn(t.h.$("#detail"), "합치기").disabled); // not read yet
  await dom.flush();
  let d = detailText(t);
  assert.match(d, /비슷한 기억이 이미 있어요/);
  assert.match(d, /같은 뜻이에요/);
  assert.match(d, /서버 안내/);
  assert.match(d, /“보는 후보”/);
  assert.match(d, /합친 뒤/);
  // existing memory vs candidate, word by word: the shared word stays plain, the rest is marked
  const words = t.h.$("#detail .dg-words");
  assert.equal(words.getAttribute("data-no-i18n"), "");
  assert.deepEqual([...words.querySelectorAll("del")].map((n) => n.textContent), ["기억"]);
  assert.deepEqual([...words.querySelectorAll("ins")].map((n) => n.textContent), ["후보"]);
  assert.equal(words.textContent, "보는 기억후보");
  btn(t.h.$("#detail"), "열기").click();
  assert.equal(t.opened[0].id, "m1");
  btn(t.h.$("#detail"), "고쳐서 수락").click();
  const edit = t.h.$("#detail textarea");
  assert.equal(edit.value, "보는 후보");
  dom.input(edit, "고친 후보");
  t.view.detail(true); // dirty: soft repaint waits
  btn(t.h.$("#detail"), "고친 내용으로 수락").click();
  await dom.flush();
  assert.deepEqual(decided[0], { scope: SCOPE, expected_revision: 1, decision: "accept", content: "고친 후보" });
  assert.equal(t.notices[0], "합쳤어요. 옛 기억은 이력에 남아요.");
  assert.equal(t.state.selected, null);
  // store: the server says the proposal is no longer current
  rows(t).find((r) => r.dataset.id === "merge:p2").click(); await dom.flush();
  d = detailText(t);
  assert.match(d, /새 기억으로 저장 제안/);
  assert.match(d, /바뀜/);
  assert.match(d, /그사이 바뀌어서/);
  btn(t.h.$("#detail"), "무시").click(); await dom.flush();
  assert.match(t.notices[1], /제안은 낡았어요/);
  // skip: the proposal read fails, targets come from records
  t.state.selected = "merge:p3"; t.view.detail();
  await dom.flush();
  d = detailText(t);
  assert.match(d, /이미 있는 기억과 같아요/);
  assert.match(d, /지금 내용을 읽지 못했어요/);
  assert.match(d, /이미 있는 기억/);
  assert.equal(t.h.$("#detail").querySelectorAll(".dg-more").length, 1);
  btn(t.h.$("#detail"), "무시").click(); await dom.flush();
  assert.match(t.notices[2], /처리하지 못했어요: weird/);
  t.view.close();
  assert.match(detailText(t), /제안을 고르세요/);
  t.view.detail(true); // soft with nothing selected: no-op
});

test("merge decisions report success for store, skip and dismiss, and the list without server support", async () => {
  const t = setup({ reloadFails: true, routes: {
    "/v1/merge_proposals/p2?": () => ({ proposal: { id: "p2" }, current: true, candidate: mem("k2", { text: "저장할 후보" }) }),
    "/v1/merge_proposals/p3?": () => ({ proposal: { id: "p3" }, current: true }),
    "/v1/merge_proposals/p2/decide": () => ({}), "/v1/merge_proposals/p3/decide": () => ({}), "/v1/merge_proposals/p1/decide": () => { throw new Error("access_denied"); } } });
  t.state.memTab = "merge"; t.view.drawMerge(); await dom.flush();
  t.state.selected = "merge:p2"; t.view.detail(); await dom.flush();
  btn(t.h.$("#detail"), "저장").click(); await dom.flush();
  assert.equal(t.notices[0], "기억으로 저장했어요.");
  t.state.selected = "merge:p3"; t.view.detail(); await dom.flush();
  btn(t.h.$("#detail"), "건너뛰기").click(); await dom.flush();
  assert.equal(t.notices[1], "후보를 건너뛰었어요.");
  t.state.selected = "merge:p1"; t.view.detail(); await dom.flush();
  btn(t.h.$("#detail"), "무시").click(); await dom.flush();
  assert.match(t.notices[2], /제안을 처리할 수 없어요/);
  t.state.selected = "merge:p2"; t.view.detail(); await dom.flush();
  btn(t.h.$("#detail"), "무시").click(); await dom.flush();
  assert.equal(t.notices[3], "제안을 닫았어요. 후보는 새로 저장 목록에 남아요.");
  t.view.reset();
  const none = setup({ noMerge: true });
  none.state.memTab = "merge"; none.view.drawMerge(); await dom.flush();
  assert.match(none.h.$("#table").textContent, /합치기 제안을 지원하지 않아요/);
  const empty = setup({ routes: { "/v1/merge_proposals?": () => ({ items: [] }) } });
  empty.state.memTab = "merge"; empty.view.drawMerge(); await dom.flush();
  assert.match(empty.h.$("#table").textContent, /확인할 합치기 제안이 없어요/);
  empty.state.memTab = "digests"; empty.state.view = "skills"; empty.view.drawMerge();
});

test("#465 digests: a failed read says it could not load; only a missing route reads as not supported", async () => {
  const down = setup({ routes: { "/v1/merge_proposals?": () => { const e = new Error("boom"); e.status = 500; throw e; } }, digestsDown: true });
  assert.equal(down.view.mergeCount(), null, "loading");
  await dom.flush();
  assert.equal(down.view.mergeCount(), false, "failed, not 0");
  down.view.draw(); await dom.flush();
  assert.match(down.h.$("#table").textContent, /요약을 불러오지 못했어요/);
  assert.doesNotMatch(down.h.$("#table").textContent, /아직 주제 요약이 없어요/);
  down.state.memTab = "merge"; down.view.drawMerge(); await dom.flush();
  assert.match(down.h.$("#table").textContent, /합치기 제안을 불러오지 못했어요/);
  assert.doesNotMatch(down.h.$("#table").textContent, /지원하지 않아요/);
  const old = setup({ routes: { "/v1/merge_proposals?": () => { const e = new Error("not_found"); e.status = 404; throw e; } } });
  old.state.memTab = "merge"; old.view.drawMerge(); await dom.flush();
  assert.match(old.h.$("#table").textContent, /합치기 제안을 지원하지 않아요/);
  assert.equal(old.view.mergeCount(), 0);
});

test("AI 정리 lists observations and mental-model drafts; only a person approves or rejects them", async () => {
  const sent = [];
  const ob1 = { id: "ob1", revision: 2, content: "답은 짧게 쓰는 편이에요", fresh: true, durability: "lasting", confidence: 0.8, updated_at: "2026-01-05T00:00:00Z",
    source_memories: [{ collection: "memories", id: "m1" }, { collection: "observations", id: "ob0" }], generator: { model: "small" } };
  const mm1 = { id: "mm1", revision: 4, title: "배포 방식", query: "배포는 어떻게 하나요?", refresh: "on_change", updated_at: "2026-01-04T00:00:00Z",
    active: { version: 1, text: "옛 요약", at: "2026-01-01T00:00:00Z" }, draft: { version: 2, text: "새 요약", citations: [{ collection: "memories", id: "m3" }], withheld: false },
    draft_diff: [{ op: "-", text: "옛 요약" }, { op: "+", text: "새 요약" }] };
  const mm2 = { id: "mm2", revision: 1, title: "초안 없음" };
  const mm3 = { id: "mm3", revision: 1, title: "가린 초안", draft: { version: 1, text: "", citations: [], withheld: true } };
  const t = setup({ routes: {
    "/v1/observations?": () => ({ items: [ob1] }), "/v1/mental-models?": () => ({ items: [mm1, mm2, mm3] }),
    "/v1/observations/ob1/review": (_, o) => { sent.push(["ob1", JSON.parse(o.body)]); return {}; },
    "/v1/mental-models/mm1/review": (_, o) => { sent.push(["mm1", JSON.parse(o.body)]); throw new Error("revision_conflict"); } } });
  t.state.memTab = "notes"; t.state.caps = { role: "human" };
  assert.equal(t.view.notesCount(), null);
  t.view.drawNotes();
  assert.match(t.h.$("#table").textContent, /AI 정리를 불러오는 중/);
  await dom.flush();
  assert.equal(t.view.notesCount(), 3, "models without a draft are not waiting");
  assert.equal(rows(t).length, 3);
  assert.match(t.h.$("#table").textContent, /기억에서 뽑은 정리 · 근거 기억 2개/);
  assert.match(t.h.$("#table").textContent, /근거 바뀜/);
  t.view.detail();
  assert.match(detailText(t), /정리를 고르세요/);
  rows(t)[0].click(); await dom.flush();
  let d = detailText(t);
  assert.match(d, /답은 짧게 쓰는 편이에요/);
  assert.match(d, /승인된 다른 AI 정리 1개도 근거로 썼어요/);
  assert.match(d, /오래 갈 내용/);
  assert.match(d, /80%/);
  btn(t.h.$("#detail"), "승인").click(); await dom.flush();
  assert.deepEqual(sent[0], ["ob1", { scope: SCOPE, expected_revision: 2, action: "approve" }]);
  assert.match(t.notices[0], /승인했어요/);
  rows(t).find((r) => r.dataset.id === "note:model:mm1").click(); await dom.flush();
  d = detailText(t);
  assert.match(d, /배포는 어떻게 하나요/);
  assert.match(d, /지금 요약/);
  assert.match(d, /옛 요약/);
  btn(t.h.$("#detail"), "버리기").click();
  dom.input(t.h.$("#detail textarea"), "틀렸어요");
  btn(t.h.$("#detail"), "버리기").click(); await dom.flush();
  assert.deepEqual(sent[1], ["mm1", { scope: SCOPE, expected_revision: 4, action: "reject", reason: "틀렸어요" }]);
  assert.match(t.notices[1], /그사이 정리나 근거 기억이 바뀌었어요/);
  rows(t).find((r) => r.dataset.id === "note:model:mm3").click(); await dom.flush();
  assert.ok(btn(t.h.$("#detail"), "승인").disabled, "a withheld draft cannot be approved");
  assert.match(detailText(t), /근거 기억이 지워져서 내용을 가렸어요/);
  t.state.caps = { role: "agent" }; t.view.detail();
  assert.equal(btn(t.h.$("#detail"), "승인"), undefined);
  assert.match(detailText(t), /승인과 버리기는 사람만 할 수 있어요/);
});

test("AI 정리: an agent's proposed topic waits for a person; 승인 saves it as it is, 버리기 deletes it", async () => {
  const sent = [];
  const pm = { id: "pm1", revision: 1, status: "proposed", title: "배포 절차", query: "배포는 어떻게 하나요?", refresh: { mode: "manual" }, created_at: "2026-01-04T00:00:00Z", updated_at: "2026-01-04T00:00:00Z" };
  const t = setup({ routes: {
    "/v1/observations?": () => ({ items: [] }), "/v1/mental-models?": (p) => ({ items: p.includes("status=proposed") ? [pm] : [] }),
    "/v1/mental-models/pm1": (_, o) => { sent.push([o.method, JSON.parse(o.body)]); return {}; } } });
  t.state.memTab = "notes"; t.state.caps = { role: "human" };
  t.view.drawNotes(); await dom.flush();
  assert.equal(rows(t).length, 1);
  assert.match(t.h.$("#table").textContent, /에이전트가 제안한 주제/);
  assert.match(t.h.$("#table").textContent, /AI 제안/);
  rows(t)[0].click(); await dom.flush();
  assert.match(detailText(t), /승인하기 전에는 요약을 쓰지 않아요/);
  assert.match(detailText(t), /배포는 어떻게 하나요/);
  btn(t.h.$("#detail"), "승인").click(); await dom.flush();
  assert.deepEqual(sent[0], ["PATCH", { scope: SCOPE, expected_revision: 1, title: "배포 절차", query: "배포는 어떻게 하나요?", refresh: { mode: "manual" } }]);
  rows(t)[0].click(); await dom.flush(); // the mock still lists it
  btn(t.h.$("#detail"), "버리기").click(); await dom.flush();
  assert.deepEqual(sent.at(-1), ["DELETE", { scope: SCOPE, expected_revision: 1 }]);
  t.state.caps = { role: "agent" }; t.view.detail();
  assert.equal(btn(t.h.$("#detail"), "승인"), undefined);
});

test("AI 정리 without server support or with a failed read", async () => {
  const gone = () => { throw new Error("not_found"); };
  let t = setup({ routes: { "/v1/observations?": gone, "/v1/mental-models?": gone } });
  t.state.memTab = "notes"; t.view.drawNotes(); await dom.flush();
  assert.match(t.h.$("#table").textContent, /AI 정리를 지원하지 않아요/);
  t = setup({ routes: { "/v1/observations?": () => { throw new Error("down"); }, "/v1/mental-models?": () => ({ items: [] }) } });
  t.state.memTab = "notes"; t.view.drawNotes(); await dom.flush();
  assert.equal(t.view.notesCount(), false);
  assert.match(t.h.$("#table").textContent, /불러오지 못했어요/);
  t = setup({ routes: { "/v1/observations?": () => ({ items: [] }), "/v1/mental-models?": gone } });
  t.state.memTab = "notes"; t.view.drawNotes(); await dom.flush();
  assert.match(t.h.$("#table").textContent, /확인할 AI 정리가 없어요/);
});

test("AI 정리 also lists causal link candidates: cause → effect with the reason, a person approves or rejects without a reason", async () => {
  const sent = [];
  const l1 = { id: "cl1", revision: 3, status: "candidate", cause: { collection: "memories", id: "m1", revision: 1 }, effect: { collection: "memories", id: "m4", revision: 1 },
    reason: "스테이징 확인을 빼먹어서 배포가 실패했어요", confidence: 0.7, generator: { model: "small" }, updated_at: "2026-01-06T00:00:00Z" };
  const t = setup({ routes: {
    "/v1/observations?": () => ({ items: [] }), "/v1/mental-models?": () => ({ items: [] }),
    "/v1/causal-links?": (path) => { assert.match(path, /status=candidate/); return { items: [l1, { id: "bad", revision: 1 }] }; },
    "/v1/causal-links/cl1/review": (_, o) => { sent.push(JSON.parse(o.body)); return {}; } } });
  t.state.memTab = "notes"; t.state.caps = { role: "human" };
  t.view.drawNotes(); await dom.flush(); await dom.flush();
  assert.equal(t.view.notesCount(), 1, "a link without both memories is skipped");
  assert.match(t.h.$("#table").textContent, /원인과 결과 제안/);
  assert.match(t.h.$("#table").textContent, /→/);
  rows(t)[0].click(); await dom.flush();
  const d = detailText(t);
  assert.match(d, /스테이징 확인을 빼먹어서/);
  assert.match(d, /원인 기억/);
  assert.match(d, /결과 기억/);
  btn(t.h.$("#detail"), "버리기").click(); await dom.flush();
  assert.deepEqual(sent[0], { scope: SCOPE, expected_revision: 3, action: "reject" }, "rejecting asks no reason (the route takes none)");
  rows(t)[0].click(); await dom.flush();
  btn(t.h.$("#detail"), "승인").click(); await dom.flush();
  assert.deepEqual(sent[1], { scope: SCOPE, expected_revision: 3, action: "approve" });
  t.state.caps = { role: "agent" }; t.view.detail();
  assert.equal(btn(t.h.$("#detail"), "승인"), undefined);
  assert.match(detailText(t), /승인과 버리기는 사람만 할 수 있어요/);
});

test("AI 정리: a stale topic summary keeps its approved text and only a person asks for a new draft; observations show when it happened", async () => {
  require("./memory-time.js");
  const ob1 = { id: "ob1", revision: 1, content: "3월 초에 이사했어요", fresh: true, updated_at: "2026-03-09T00:00:00Z", source_memories: [{ collection: "memories", id: "m1" }],
    occurred: { from: "2026-03-04T00:00:00Z", to: "2026-03-08T00:00:00Z" } };
  const mm = (id, reason) => ({ id, revision: 2, title: `요약 ${id}`, query: "q", stale: true, stale_reason: reason, active: { version: 1, text: "지금 쓰는 요약", citations: [{ collection: "memories", id: "m1" }], at: "2026-01-01T00:00:00Z" } });
  let refresh = () => ({});
  const t = setup({ routes: { "/v1/observations?": () => ({ items: [ob1] }), "/v1/mental-models?": () => ({ items: [mm("s1", "sources_changed"), mm("s2", "new_memories"), { id: "ok", revision: 1, title: "괜찮음", active: { version: 1, text: "x" } }] }),
    "/v1/mental-models/s1/refresh": (_, o) => refresh(o) } });
  t.state.memTab = "notes"; t.state.caps = { role: "human" };
  t.view.drawNotes(); await dom.flush();
  assert.equal(rows(t).length, 3, "a model that is neither stale nor drafted is not listed");
  const table = t.h.$("#table").textContent;
  assert.match(table, /일어난 때 2026년 3월 4일 ~ 2026년 3월 7일/);
  assert.match(table, /다시 만들 주제 요약/); assert.match(table, /새 기억 있음/); assert.match(table, /근거 바뀜/);
  rows(t).find((r) => r.dataset.id === "note:model:s1").click(); await dom.flush();
  assert.match(detailText(t), /근거로 든 기억이 지워졌거나 더는 승인 상태가 아니에요/);
  assert.match(detailText(t), /지금 쓰는 요약/);
  assert.equal(btn(t.h.$("#detail"), "승인"), undefined, "nothing to approve until a draft exists");
  refresh = () => { throw new Error("model_not_configured"); };
  btn(t.h.$("#detail"), "다시 만들기").click(); await dom.flush();
  assert.match(t.notices.at(-1), /서버에 요약을 쓸 모델이 없어요/);
  let sent;
  refresh = (o) => { sent = JSON.parse(o.body); return {}; };
  btn(t.h.$("#detail"), "다시 만들기").click(); await dom.flush();
  assert.deepEqual(sent, { scope: SCOPE });
  assert.match(t.notices.at(-1), /새 초안을 만들었어요/);
  t.state.caps = { role: "agent" }; t.view.detail();
  assert.equal(btn(t.h.$("#detail"), "다시 만들기"), undefined);
  assert.match(detailText(t), /다시 만들기는 사람만 할 수 있어요/);
});

// ---------------------------------------------------------------- 모아보기 · 직접 정한 주제 · 기억 정리
const sent = (t, path, method) => t.calls.filter(([p, o]) => p.startsWith(path) && (o?.method || "GET") === method).map(([, o]) => (o?.body ? JSON.parse(o.body) : null));
const seg = (t, label) => btn(t.h.$("#controls"), label);

test("직접 정한 주제: list, trigger with next run and last result, history diff, and only a person edits, remakes, clears or deletes", async () => {
  const m1 = { id: "x1", revision: 4, title: "배포 순서", query: "배포할 때 확인하는 순서는?", status: "active",
    refresh: { mode: "on_change", cron: "0 9 * * 1", after_consolidation: true, min_interval: "6h", kinds: ["procedural"] },
    active: { version: 2, text: "스테이징 확인 뒤 배포한다.", citations: [{ collection: "memories", id: "m1" }], at: "2026-10-10T09:00:00Z" },
    next_refresh_at: "2026-11-09T09:00:00Z", last_outcome: { code: "content_unchanged", trigger: "cron", at: "2026-11-02T09:00:00Z" } };
  const m2 = { id: "x2", revision: 1, title: "빈 주제", query: "q", refresh: "manual", status: "active" };
  const hist = { id: "x1", revisions: [{ version: 1, action: "approve", by: "me", at: "2026-10-01T00:00:00Z", diff: [{ op: "+", text: "옛 요약" }] },
    { version: 2, action: "approve", by: "me", at: "2026-10-10T00:00:00Z", diff: [{ op: "-", text: "옛 요약" }, { op: "+", text: "스테이징 확인 뒤 배포한다." }] }] };
  const t = setup({ routes: { "/v1/mental-models?": () => ({ items: [m1, m2] }), "/v1/mental-models/x1/history": () => hist,
    "/v1/mental-models/x1/clear": () => ({}), "/v1/mental-models/x1": () => ({ id: "x1" }), "/v1/mental-models/cron-preview": () => ({ next: ["2026-11-09T09:00:00Z", "2026-11-16T09:00:00Z"] }),
    "/v1/observations?": () => ({ items: [] }) } });
  t.state.caps = { role: "human" };
  t.view.draw(); await dom.flush();
  seg(t, "직접 정한 주제").click(); await dom.flush();
  assert.equal(t.view.title(), "주제 2개");
  assert.equal(rows(t).length, 2);
  assert.match(t.h.$("#table").textContent, /기억이 바뀔 때 · 다음 .*바뀐 내용이 없었어요/);
  assert.match(t.h.$("#table").textContent, /비어 있음/);
  rows(t)[0].click(); await dom.flush();
  const d = detailText(t);
  assert.match(d, /배포할 때 확인하는 순서는\?/); assert.match(d, /일정 \(UTC\)0 9 \* \* 1/); assert.match(d, /기억 정리 뒤다시 만들어요/); assert.match(d, /최소 간격6h/);
  assert.match(d, /지난 결과바뀐 내용이 없었어요 · 정한 일정/);
  assert.match(d, /v2 · 승인/); assert.match(d, /v1 · 승인/);
  assert.ok(t.h.$("#detail").querySelector(".dg-line.del"), "the newest revision opens with its diff");
  // 고치기 keeps what the form does not show (kinds) and drops a cleared field
  btn(t.h.$("#detail"), "고치기").click();
  const inputs = t.h.$("#detail").querySelectorAll("input");
  dom.input(inputs.find((i) => i.value === "6h"), "");
  btn(t.h.$("#detail"), "일정 미리보기").click(); await dom.flush();
  assert.match(detailText(t), /다음 실행 \(이 PC 시각\)/);
  assert.deepEqual(sent(t, "/v1/mental-models/cron-preview", "POST")[0], { scope: SCOPE, cron: "0 9 * * 1" });
  btn(t.h.$("#detail"), "저장").click(); await dom.flush();
  const patch = sent(t, "/v1/mental-models/x1", "PATCH")[0];
  assert.deepEqual(patch, { scope: SCOPE, expected_revision: 4, title: "배포 순서", query: "배포할 때 확인하는 순서는?",
    refresh: { mode: "on_change", cron: "0 9 * * 1", after_consolidation: true, kinds: ["procedural"] } });
  assert.match(t.notices.at(-1), /주제를 고쳤어요/);
  // 비우기 asks once more, then clears with the revision
  t.state.selected = "mm:x1"; t.view.detail();
  btn(t.h.$("#detail"), "비우기").click();
  assert.match(detailText(t), /승인된 요약과 초안을 비울까요/);
  assert.equal(sent(t, "/v1/mental-models/x1/clear", "POST").length, 0);
  dom.buttons(t.h.$("#detail .dg-confirm"), "비우기")[0].click(); await dom.flush();
  assert.deepEqual(sent(t, "/v1/mental-models/x1/clear", "POST")[0], { scope: SCOPE, expected_revision: 4 });
  t.state.selected = "mm:x1"; t.view.detail();
  btn(t.h.$("#detail"), "지우기").click();
  dom.buttons(t.h.$("#detail .dg-confirm"), "지우기")[0].click(); await dom.flush();
  assert.deepEqual(sent(t, "/v1/mental-models/x1", "DELETE")[0], { scope: SCOPE, expected_revision: 4 });
  // an agent token sees the summary but no buttons
  t.state.caps = { role: "agent" }; t.state.selected = "mm:x1"; t.view.detail();
  assert.equal(btn(t.h.$("#detail"), "고치기"), undefined);
  assert.match(detailText(t), /고치기와 다시 만들기는 사람만 할 수 있어요/);
});

test("새 주제 만들기 sends only what was filled in; an invalid schedule says how to write it", async () => {
  let fail = true;
  const t = setup({ routes: { "/v1/mental-models?": () => ({ items: [] }), "/v1/mental-models": (_, o) => { if (fail) { fail = false; throw new Error("invalid"); } return { id: "n1", ...JSON.parse(o.body) }; } } });
  t.state.caps = { role: "human" };
  t.view.draw(); await dom.flush();
  seg(t, "직접 정한 주제").click(); await dom.flush();
  assert.match(t.h.$("#table").textContent, /아직 직접 정한 주제가 없어요/);
  btn(t.h.$("#controls"), "새 주제 만들기").click();
  assert.equal(t.state.selected, "mm:new");
  btn(t.h.$("#detail"), "저장").click(); await dom.flush();
  assert.match(t.notices.at(-1), /제목과 질문을 적어 주세요/);
  const [title] = t.h.$("#detail").querySelectorAll("input");
  dom.input(title, "리뷰 받는 법"); dom.input(t.h.$("#detail textarea"), "리뷰를 어떻게 받고 싶어 하나요?");
  btn(t.h.$("#detail"), "저장").click(); await dom.flush();
  assert.match(t.notices.at(-1), /일정은 다섯 칸/);
  btn(t.h.$("#detail"), "저장").click(); await dom.flush();
  assert.deepEqual(sent(t, "/v1/mental-models", "POST").at(-1), { scope: SCOPE, title: "리뷰 받는 법", query: "리뷰를 어떻게 받고 싶어 하나요?", refresh: { mode: "manual" } });
  assert.equal(t.state.selected, "mm:n1");
});

test("주제 고치기: tags, recall options, answer shape and the wiki folder go into the save; the folder list comes from the wiki tree", async () => {
  const m = { id: "x2", revision: 3, title: "배포", query: "배포 순서?", status: "active", folder: "ops", refresh: { mode: "manual", tags: ["deploy"], tag_groups: [[{ tags: ["a"] }]] } };
  const t = setup({ routes: { "/v1/mental-models?": () => ({ items: [m] }), "/v1/wiki/tree": () => ({ folders: [{ path: "ops", pages: 1 }, { path: "guides", pages: 0 }] }),
    "/v1/mental-models/x2": (_, o) => ({ id: "x2", ...JSON.parse(o.body) }), "/v1/observations?": () => ({ items: [] }) } });
  t.state.caps = { role: "human" };
  t.view.draw(); await dom.flush();
  seg(t, "직접 정한 주제").click(); await dom.flush();
  rows(t)[0].click(); await dom.flush();
  btn(t.h.$("#detail"), "고치기").click(); await dom.flush();
  const sel = (label) => t.h.$("#detail").querySelectorAll("select").find((s) => s.getAttribute("aria-label") === label);
  const folder = sel("위키에 두기");
  assert.deepEqual(folder.querySelectorAll("option").map((o) => o.value), ["\u0000none", "", "ops", "guides"]);
  assert.match(detailText(t), /자세히/);
  const inputs = t.h.$("#detail").querySelectorAll("input");
  dom.input(inputs.find((i) => i.value === "deploy"), "deploy, ops, deploy");
  dom.input(inputs.find((i) => i.placeholder === "예: 4000"), "3000");
  inputs.find((i) => i.type === "checkbox" && !i.checked && i.parentElement.textContent.includes("원문 조각")).checked = true;
  dom.input(sel("읽을 종류"), "observation");
  dom.input(sel("태그 맞추는 방법"), "all_strict");
  const areas = t.h.$("#detail").querySelectorAll("textarea");
  assert.equal(areas[1].value, "a");
  dom.input(areas[2], "{ bad");
  btn(t.h.$("#detail"), "저장").click(); await dom.flush();
  assert.match(t.notices.at(-1), /요약 모양 JSON을 읽을 수 없어요/);
  dom.input(areas[2], '{"type": "object"}');
  dom.input(folder, "guides");
  btn(t.h.$("#detail"), "저장").click(); await dom.flush();
  assert.deepEqual(sent(t, "/v1/mental-models/x2", "PATCH")[0], { scope: SCOPE, expected_revision: 3, title: "배포", query: "배포 순서?", folder: "guides",
    refresh: { mode: "manual", tags: ["deploy", "ops"], tags_match: "all_strict", tag_groups: [[{ tags: ["a"] }]], fact_types: ["observation"], include_chunks: true, recall_max_tokens: 3000, response_schema: { type: "object" } } });
  // taking it out of the wiki sends no_folder
  t.state.selected = "mm:x2"; t.view.detail(); await dom.flush();
  btn(t.h.$("#detail"), "고치기").click(); await dom.flush();
  dom.input(t.h.$("#detail").querySelectorAll("select").find((s) => s.getAttribute("aria-label") === "위키에 두기"), "\u0000none");
  btn(t.h.$("#detail"), "저장").click(); await dom.flush();
  assert.equal(sent(t, "/v1/mental-models/x2", "PATCH")[1].no_folder, true);
});

test("기억 정리: by status and scope, history and delete; 지금 정리하기 shows the plan, runs on a click and reports what waits in AI 정리", async () => {
  const ob = (id, tags, o = {}) => ({ id, status: "active", revision: 1, content: `정리 ${id}`, durability: "lasting", tags, source_memories: [{ collection: "memories", id: "m1" }], updated_at: "2026-10-01T00:00:00Z", ...o });
  let release, deleted = 0;
  const t = setup({ routes: {
    "/v1/mental-models?": () => ({ items: [] }),
    "/v1/observations/scopes": () => ({ items: [{ tags: ["orbit"], active: 1, proposed: 0 }, { tags: [], active: 1, proposed: 2 }] }),
    "/v1/observations/o1/history": () => ({ id: "o1", revisions: [{ version: 1, action: "propose", by: "service", at: "2026-10-01T00:00:00Z", diff: [{ op: "+", text: "정리 o1" }] }] }),
    "/v1/observations/strategies/preview": () => ({ scopes: 3, unclaimed: 1, strategies: [{ name: "per_project", claimed: 2, rules: [{}] }] }),
    "/v1/observations/consolidate": (_, o) => (JSON.parse(o.body).plan_only ? { plan: { stale_observations: ["a"], groups: [["m1", "m2"]], mental_models: [] } }
      : new Promise((r) => { release = () => r({ proposed: [{}, {}], refreshed: [{}], rejected: [], mental_models: [], calls: 3 }); })),
    "/v1/observations?": (path) => (new URL(path, "http://x").searchParams.get("status") === "active" ? { items: [ob("o1", ["orbit", "review:x"]), ob("o2", [])] } : { items: [] }),
    "/v1/observations": (_, o) => { const b = JSON.parse(o.body); if (b.all) { deleted++; return { deleted: ["a"], more: deleted < 2 }; } return { deleted: b.ids }; } } });
  t.state.caps = { role: "human" };
  t.view.draw(); await dom.flush();
  seg(t, "기억 정리").click(); await dom.flush();
  assert.equal(rows(t).length, 2);
  assert.equal(t.view.title(), "정리 2개");
  const scopeSel = t.h.$("#controls").querySelectorAll("select").find((s) => s.getAttribute("aria-label") === "묶음");
  dom.input(scopeSel, "orbit");
  assert.deepEqual(rows(t).map((r) => r.dataset.id), ["ob:o1"], "review: tags are not a scope");
  rows(t)[0].click(); await dom.flush();
  assert.match(detailText(t), /묶음: orbit/); assert.match(detailText(t), /v1 · AI 제안/);
  btn(t.h.$("#detail"), "지우기").click();
  dom.buttons(t.h.$("#detail .dg-confirm"), "지우기")[0].click(); await dom.flush();
  assert.deepEqual(sent(t, "/v1/observations", "DELETE")[0], { scope: SCOPE, ids: ["o1"] });
  // 지금 정리하기
  btn(t.h.$("#controls"), "지금 정리하기").click(); await dom.flush();
  assert.deepEqual(sent(t, "/v1/observations/consolidate", "POST")[0], { scope: SCOPE, plan_only: true });
  assert.match(detailText(t), /근거 바뀐 정리 1개 · 새로 묶을 기억 1묶음 · 다시 만들 주제 0개/);
  btn(t.h.$("#detail"), "정리 시작").click(); await dom.flush();
  assert.match(detailText(t), /정리하는 중…/); assert.match(detailText(t), /초 지났어요/);
  release(); await dom.flush();
  assert.match(detailText(t), /새 정리 2개 · 고친 정리 1개 · 주제 초안 0개 · 못 쓴 답 0개 · AI 호출 3번/);
  assert.deepEqual(sent(t, "/v1/observations/consolidate", "POST")[1], { scope: SCOPE });
  btn(t.h.$("#detail"), "AI 정리에서 확인하기").click();
  assert.deepEqual(t.gone.at(-1), ["memories", "notes"]);
  // 자세히: strategy preview and 모두 지우기 (repeats while the server says more)
  t.state.memTab = "digests"; t.state.selected = "ob:run"; t.view.detail();
  btn(t.h.$("#detail"), "미리보기").click(); await dom.flush();
  assert.match(detailText(t), /per_project묶음 2개 · 규칙 1개/); assert.match(detailText(t), /기본 방식묶음 1개/);
  btn(t.h.$("#detail"), "정리 모두 지우기").click();
  dom.buttons(t.h.$("#detail .dg-confirm"), "모두 지우기")[0].click(); await dom.flush();
  assert.equal(deleted, 2);
  assert.match(t.notices.at(-1), /정리 2개를 지웠어요/);
});

test("mental model helpers: refreshBody keeps hidden keys, planText/runText, Markdown export", () => {
  setup();
  const D = window.ArkismDigests;
  assert.deepEqual(D.refreshOf({ refresh: "daily" }), { mode: "daily" });
  assert.deepEqual(D.refreshBody({ mode: "manual", tags: ["a"], cron: "1 1 * * *", budget: "low" }, { mode: "daily", cron: " ", budget: "high", strategy: "delta", after_consolidation: false }),
    { mode: "daily", tags: ["a"], budget: "high", strategy: "delta" });
  assert.deepEqual(D.refreshBody({ mode: "manual", tags: ["old"], include_chunks: true }, { tags: [], include_chunks: false, fact_types: ["memory"], recall_max_tokens: 500, response_schema: { type: "object" } }),
    { mode: "manual", fact_types: ["memory"], recall_max_tokens: 500, response_schema: { type: "object" } });
  const groups = [[{ tags: ["a", "b"], match: "all_strict" }, { tags: ["c"], not: true }], [{ tags: ["d"] }]];
  assert.deepEqual(D.tagGroups(D.tagGroupsText(groups)), groups);
  assert.equal(D.tagGroupsText(undefined), "");
  assert.deepEqual(D.folderBody(null, "\u0000none"), {});
  assert.deepEqual(D.folderBody("docs", "\u0000none"), { no_folder: true });
  assert.deepEqual(D.folderBody("docs", "docs"), {});
  assert.deepEqual(D.folderBody(null, ""), { folder: "" });
  assert.equal(D.planText({ stale_observations: [], groups: [], mental_models: [] }), "지금은 정리할 것이 없어요");
  assert.equal(D.modelMarkdown({ title: "T", query: "Q", active: { text: "본문" } }, ["첫\n줄"]), "# T\n\n> Q\n\n본문\n\n## 근거 기억\n\n- 첫 줄\n");
  assert.match(D.modelMarkdown({ title: "T", query: "Q" }), /아직 승인된 요약이 없어요/);
});
