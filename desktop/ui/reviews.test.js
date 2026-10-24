// Run with: node --test desktop/ui/*.test.js
const test = require("node:test");
const assert = require("node:assert/strict");
const dom = require("./testdata/fake-dom.cjs");
const { document, window } = dom.install();
const h = dom.helpers(document);
const store = new Map();
let storageBroken = false;
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => { if (storageBroken) throw new Error("quota"); store.set(k, String(v)); }
};
require("./reviews.js");
for (const id of ["controls", "chips", "stats", "table", "detail"]) { const n = h.el("div"); n.id = id; document.body.append(n); }
const $ = (s) => document.querySelector(s);

let chats = [], withDock = false;
window.ArkismAIChat = { button: (o) => { const b = h.button(o.label, "ai"); if (withDock) b.dock = h.el("span", "dock", "dock"); chats.push(o); return b; } };
const opened = [];
window.open = (...a) => opened.push(a);

const DAY = 864e5, ago = (n) => new Date(Date.now() - n).toISOString();
const finding = (severity, text, line = 1) => ({ severity, path: "a.go", line, text });
const items = () => [
  { repo: "o/arkism", number: 1, branch: "fix", base: "main", head_sha: "abcdef1234", title: "running PR", state: "running", started_at: ago(1000), findings: [], steps: [{ name: "runner", status: "running" }] },
  { repo: "o/arkism", number: 2, branch: "feat", base: "main", head_sha: "1234567890", title: "blocking PR", state: "done", pr_state: "open", model: "codex / gpt-x", quality: "success",
    started_at: ago(2 * DAY), finished_at: ago(2 * DAY - 90000), url: "https://x/2",
    findings: [finding("blocking", "nil check"), finding("advisory", "rename", 2)],
    steps: [{ name: "runner", status: "success", started_at: ago(10000), finished_at: ago(9000) }, { name: "quality", status: "done" }, { name: "ai", status: "failed" }, { name: "comment", status: "queued" }, { name: "custom", status: "success" }] },
  { repo: "solo", number: 3, branch: "b", base: "main", head_sha: "0000000000", title: "advisory PR", state: "done", pr_state: "merged", quality: "2 new",
    started_at: ago(9 * DAY), finished_at: ago(9 * DAY - 30000), findings: [finding("advisory", "style", 3)], steps: [{ name: "quality", status: "success" }] },
  { repo: "o/arkism", number: 4, branch: "c", base: "main", head_sha: "1111111111", title: "failed PR", state: "failed", findings: [], steps: [] },
  { repo: "o/arkism", number: 5, branch: "d", base: "main", head_sha: "2222222222", title: "clean PR", state: "done", started_at: ago(3 * DAY), findings: [] }
];
const docWith = (extra = {}) => ({ collected_at: ago(60000), repos: [{ name: "o/arkism", prs: 3 }, { name: "solo", prs: 0 }, { name: "x/never", prs: 1 }], items: items(), ...extra });

function setup(reply = () => ({ code_reviews: docWith() })) {
  const calls = [], notices = [], heads = [], gone = [];
  let renders = 0, marks = 0, opens = 0, confirmAnswer = true, assistReply = () => ({ status: "done", output: "고쳤어요\n자세히" });
  const state = { data: {}, scope: { workspace_id: "w" }, selected: null };
  const request = async (path, opts) => { calls.push({ path, body: opts?.body ? JSON.parse(opts.body) : null }); return reply(path, opts); };
  const api = window.ArkismReviews({
    state, $, el: h.el, button: h.button, render: () => renders++, request, query: () => "", notice: (m) => notices.push(m),
    detailShell: (label) => { const b = h.el("div", "detail-body"); $("#detail").replaceChildren(h.el("span", "", label), b); return b; },
    markSelected: () => marks++, openDetail: () => opens++, ago: () => "방금", go: (v) => gone.push(v),
    headExtras: (o) => heads.push(o), confirmPop: async () => confirmAnswer,
    assist: async (kind, text) => { calls.push({ assist: kind, text }); return assistReply(kind, text); }
  });
  return { api, state, calls, notices, heads, gone, renders: () => renders, marks: () => marks, opens: () => opens,
    confirm: (v) => { confirmAnswer = v; }, assist: (fn) => { assistReply = fn; } };
}
const btn = (root, label) => dom.buttons(root, label)[0];

test("load and reload read the collected reviews, and a failure reads as none", async () => {
  let fail = false;
  const t = setup(() => { if (fail) throw new Error("x"); return { code_reviews: { collected_at: "now" } }; });
  assert.deepEqual(await t.api.load(), { collected_at: "now" });
  await t.api.reload();
  assert.equal(t.state.data.reviews.collected_at, "now");
  assert.equal(t.renders(), 1);
  fail = true;
  assert.equal(await t.api.load(), null);
  const t2 = setup(() => ({}));
  assert.equal(await t2.api.load(), null);
});

test("the list shows week tiles, running and finished PRs, and opens one", async () => {
  store.clear();
  const t = setup();
  t.api.render();
  assert.match($("#table").textContent, /아직 코드 리뷰를 지원하지 않아요/);
  assert.equal(t.api.views.reviews.sub(), "PR을 올리면 AI가 읽고 지적해요.");
  t.state.data.reviews = { repos: [] };
  t.api.render();
  assert.match($("#table").textContent, /10분마다 모아요/);

  t.state.data.reviews = docWith();
  t.api.render();
  const stats = $("#stats").textContent;
  assert.match(stats, /이번 주 검토3건지난주보다 \+2/);
  assert.match(stats, /평균 시간1분 30초codex \/ gpt-x/);
  assert.match(stats, /고칠 것2건blocking 1건/);
  assert.match(stats, /처리 중1러너 10대 중/);
  assert.ok($("#stats .warn"));
  const table = $("#table").textContent;
  assert.match(table, /지금 처리 중1/);
  assert.match(table, /최근 끝난 PR4/);
  assert.match(table, /AI가 읽는 중/);
  assert.match(table, /검사 실패/);
  assert.match(table, /고칠 것 1건/);
  assert.match(table, /권고 1건/);
  assert.match(table, /문제 없음/);
  assert.match(table, /arkism · #2 · feat/);
  assert.match(table, /solo · #3/);
  assert.match(table, /레포 3곳 · 방금 모음/);
  assert.equal($("#table").className, "rv-wrap");
  const dotClasses = $("#table").querySelectorAll(".rv-dot").map((d) => d.className.trim());
  assert.ok(dotClasses.includes("rv-dot ok") && dotClasses.includes("rv-dot bad") && dotClasses.includes("rv-dot run") && dotClasses.includes("rv-dot"));
  assert.match(t.api.views.reviews.sub(), /레포 3곳, 이번 주 3건/);
  assert.equal(t.api.count(), 3);
  assert.deepEqual(t.api.head(), []);

  // header: refresh, settings (no ← link: the 저장소 screen it went back to is gone)
  const head = t.heads.at(-1);
  assert.equal(head.back, undefined);
  head.aside[0].click(); await dom.flush(); assert.equal(t.calls.at(-1).path, "/v1/code_reviews?");
  head.aside[1].click(); assert.ok($("dialog.rv-settings")); $("dialog.rv-settings").close();

  const row = $("#table").querySelectorAll(".rv-row").find((r) => r.dataset.id === "review:o/arkism#2");
  row.click();
  assert.equal(t.state.selected, "review:o/arkism#2");
  assert.equal(t.marks(), 1); assert.equal(t.opens(), 1);
  assert.match($("#detail").textContent, /PR #2 · 코드 리뷰/);
  t.api.render();
  assert.ok($("#table").querySelector(".rv-row.selected"));
});

test("fix tile notes cover advisory-only and nothing left, and short averages read in seconds", () => {
  const t = setup();
  const now = Date.now();
  t.state.data.reviews = { collected_at: "x", items: [
    { repo: "r", number: 9, branch: "b", title: "t", state: "done", started_at: new Date(now - 1000).toISOString(), finished_at: new Date(now - 990).toISOString(), findings: [finding("advisory", "a")] }
  ] };
  t.api.render();
  assert.match($("#stats").textContent, /평균 시간0초/);
  assert.match($("#stats").textContent, /권고 · 1건 확인 대기/);
  assert.match($("#table").textContent, /레포 0곳/);
  t.state.data.reviews.items[0].findings = [];
  t.state.data.reviews.items[0].finished_at = undefined;
  t.api.render();
  assert.match($("#stats").textContent, /평균 시간—/);
  assert.match($("#stats").textContent, /고칠 것0건없어요/);
  assert.match($("#stats").textContent, /지난주보다 \+1/);
  t.state.data.reviews.items = [{ ...t.state.data.reviews.items[0], started_at: new Date(now - 8 * DAY).toISOString() }];
  t.api.render();
  assert.match($("#stats").textContent, /지난주보다 -1/);
});

test("the summary shows the saved settings when nothing is selected", () => {
  const t = setup();
  t.api.detail();
  assert.match($("#detail").textContent, /PR을 올리면 AI가 읽고 지적해요/);
  assert.doesNotMatch($("#detail").textContent, /지금 설정/);
  t.state.data.reviews = docWith({ settings: { provider: "claude", on_blocking: "block", repos_off: ["o/arkism"] } });
  t.state.selected = "review:none#0";
  t.api.detail();
  assert.match($("#detail").textContent, /리뷰하는 AIClaude/);
  assert.match($("#detail").textContent, /심각한 건 병합 막기/);
  assert.match($("#detail").textContent, /꺼 둔 레포arkism/);
  t.state.data.reviews.settings = { provider: "codex", on_blocking: "notify" };
  t.state.selected = "other";
  t.api.detail();
  assert.match($("#detail").textContent, /리뷰하는 AICodex/);
  assert.match($("#detail").textContent, /알림만/);
  assert.match($("#detail").textContent, /꺼 둔 레포없어요/);
});

test("a PR's detail lists steps and findings, and 무시 hides a finding on this PC", async () => {
  store.clear();
  const t = setup();
  t.state.data.reviews = docWith({ reruns: [{ repo: "o/arkism", number: 2, status: "done", done_at: "x" }] });
  t.state.selected = "review:o/arkism#2";
  t.api.detail();
  const d = $("#detail");
  assert.match(d.textContent, /arkism · feat → main · 1234567/);
  assert.match(d.textContent, /총 1분 30초/);
  assert.match(d.textContent, /러너 시작대기 1초/);
  assert.match(d.textContent, /품질 검사새 문제 0/);
  assert.match(d.textContent, /AI 리뷰codex \/ gpt-x/);
  assert.match(d.textContent, /PR 코멘트 게시/);
  assert.match(d.textContent, /custom/);
  assert.deepEqual(d.querySelectorAll(".rv-steps li").map((li) => [li.className, li.dataset.icon]),
    [["ok", "check-circle"], ["ok", "check-circle"], ["warn", "warning-circle"], ["", "clock-counter-clockwise"], ["ok", "check-circle"]]);
  assert.match(d.textContent, /AI 지적 · 2건blocking 1건/);
  assert.match(d.textContent, /다시 검사를 시작했어요 \(방금\)/);
  assert.equal(d.querySelectorAll(".rv-finding").length, 2);
  assert.equal(btn(d, "AI가 고쳐서 올리기").disabled, false);

  storageBroken = true;
  btn(d, "무시").click(); // private window: nothing stored, still redraws
  storageBroken = false;
  assert.equal(t.renders(), 1);
  btn($("#detail"), "무시").click();
  assert.equal($("#detail").querySelectorAll(".rv-finding").length, 1);
  assert.match($("#detail").textContent, /AI 지적 · 1건blocking 0건/);
  assert.equal(btn($("#detail"), "AI가 고쳐서 올리기"), undefined);
  btn($("#detail"), "무시").click();
  assert.match($("#detail").textContent, /남은 지적이 없어요/);
  store.set("arkism.reviewDismissed", "{bad");
  t.api.detail();
  assert.equal($("#detail").querySelectorAll(".rv-finding").length, 2);

  t.state.selected = "review:o/arkism#1";
  t.api.detail();
  assert.match($("#detail").textContent, /실행 중/);
  assert.match($("#detail").textContent, /AI가 아직 읽고 있어요/);
  t.state.selected = "review:solo#3";
  t.api.detail();
  assert.match($("#detail").textContent, /병합됨/);
  assert.match($("#detail").textContent, /품질 검사2 new/);
  const fix = btn($("#detail"), "AI로 고치기");
  assert.equal(fix.disabled, true);
  assert.equal(fix.title, "열린 PR에서만 고칠 수 있어요");
  t.state.selected = "review:o/arkism#4";
  t.api.detail(); // no steps, no started_at
  assert.match($("#detail").textContent, /총 —|실행 중/);
});

test("rerun notes follow the last request for that PR", () => {
  const t = setup();
  t.state.selected = "review:o/arkism#2";
  for (const [rr, want] of [[{ status: "pending" }, /서버가 1분 안에 시작해요/], [{ status: "failed", error: "busy" }, /다시 검사하지 못했어요: busy/], [{ status: "failed" }, /다시 검사하지 못했어요: $/], [{ status: "odd" }, null]]) {
    t.state.data.reviews = docWith({ reruns: [{ repo: "other", number: 2, status: "done" }, { repo: "o/arkism", number: 2, ...rr }] });
    t.api.detail();
    const note = $("#detail").querySelectorAll(".cn-note").at(-1);
    if (want) assert.match(note.textContent, want); else assert.equal(note.textContent, "");
  }
});

test("다시 검사 and GitHub buttons ask the server and open the PR", async () => {
  let fail = false;
  const t = setup((path) => { if (fail) throw new Error("down"); return { code_reviews: docWith({ reruns: [{ repo: "o/arkism", number: 2, status: "pending" }] }) }; });
  t.state.data.reviews = docWith();
  t.state.selected = "review:o/arkism#2";
  t.api.detail();
  btn($("#detail"), "GitHub에서 열기").click();
  assert.deepEqual(opened.at(-1), ["https://x/2", "_blank"]);
  btn($("#detail"), "다시 검사").click(); await dom.flush();
  assert.deepEqual(t.calls.at(-1), { path: "/v1/code_reviews/rerun", body: { scope: { workspace_id: "w" }, repo: "o/arkism", number: 2 } });
  assert.match(t.notices.at(-1), /다시 검사를 요청했어요/);
  assert.equal(t.state.data.reviews.reruns.length, 1);
  fail = true;
  btn($("#detail"), "다시 검사").click(); await dom.flush();
  assert.equal(t.notices.at(-1), "요청하지 못했어요 (down).");
});

test("AI로 고치기 explains a fix, then commits only after a confirm", async () => {
  store.clear(); chats = []; withDock = true;
  const t = setup();
  t.state.data.reviews = docWith();
  t.state.selected = "review:o/arkism#2";
  t.api.detail();
  assert.equal(chats.length, 2);
  assert.equal($("#detail").querySelectorAll(".rv-finding .dock").length, 2);
  const chat = chats[0];
  assert.match(chat.greeting, /a.go:1 지적/);
  t.assist(() => ({ status: "done", output: "  이렇게 고쳐요  " }));
  const answer = await chat.ask("짧게");
  assert.equal(answer.say, "이렇게 고쳐요");
  assert.match(t.calls.at(-1).text, /The owner says: 짧게/);
  t.assist(() => ({ status: "failed", error: "x".repeat(200) }));
  await assert.rejects(chat.ask("다시"), (e) => e.message.length < 150 && /AI가 답하지 못했어요/.test(e.message));
  t.assist(() => ({ status: "failed" }));
  await assert.rejects(chat.ask("다시"), /\(실패\)/);

  t.assist(() => ({ status: "done", output: "ok" }));
  const prop = (await chat.ask("고쳐")).props[0];
  assert.match(prop.to, /a.go:1 → feat\n고쳐/);
  t.confirm(false);
  assert.equal(await prop.apply(), false);
  t.confirm(true);
  t.assist(() => ({ status: "done", output: "커밋했어요\n세부" }));
  assert.equal(await prop.apply(), true);
  assert.equal(t.notices.at(-1), "커밋했어요");
  const fixCall = t.calls.filter((c) => c.assist === "fix").at(-1);
  assert.deepEqual(JSON.parse(fixCall.text), { repo: "o/arkism", number: 2, path: "a.go", line: 1, text: "nil check\n\n고쳐" });
  t.assist(() => ({ status: "failed", error: "conflict" }));
  assert.equal(await prop.apply(), false);
  assert.equal(t.notices.at(-1), "고치지 못했어요: conflict");
  t.assist(() => ({ status: "failed" }));
  await prop.apply();
  assert.equal(t.notices.at(-1), "고치지 못했어요: 알 수 없음");
  t.assist(() => { throw new Error("offline"); });
  assert.equal(await prop.apply(), false);
  assert.equal(t.notices.at(-1), "고치지 못했어요 (offline).");
  withDock = false;
});

test("AI가 고쳐서 올리기 fixes each open finding in turn and reports failures", async () => {
  store.clear();
  const t = setup();
  t.state.data.reviews = docWith();
  t.state.selected = "review:o/arkism#2";
  t.api.detail();
  const all = btn($("#detail"), "AI가 고쳐서 올리기");
  t.confirm(false); all.click(); await dom.flush();
  assert.equal(t.calls.length, 0);
  t.confirm(true); all.click(); await dom.flush();
  assert.equal(t.notices.at(-1), "2건 모두 고쳐서 올렸어요.");
  assert.equal(all.textContent, "AI가 고쳐서 올리기");
  assert.equal(all.disabled, false);
  let n = 0;
  t.assist(() => { if (n++ === 0) throw new Error("timeout"); return { status: "failed" }; });
  all.click(); await dom.flush();
  assert.equal(t.notices.at(-1), "0건 고쳤고 2건은 못 고쳤어요: a.go:1 timeout");
  t.assist(() => ({ status: "failed", error: "bad" }));
  all.click(); await dom.flush();
  assert.match(t.notices.at(-1), /a.go:1 bad/);
  // a closed PR cannot be fixed
  t.state.data.reviews.items[1].pr_state = "closed";
  t.api.detail();
  const closed = btn($("#detail"), "AI가 고쳐서 올리기");
  assert.equal(closed.disabled, true);
  assert.equal(closed.title, "열린 PR에서만 고칠 수 있어요");
});

test("review settings pick the reviewer, repos and blocking rule, then save", async () => {
  let fail = false;
  const t = setup((path, opts) => { if (fail) throw new Error("denied"); return { code_reviews: { saved: JSON.parse(opts.body).settings } }; });
  t.api.openSettings(); // no document yet: defaults
  let dlg = $("dialog.rv-settings");
  assert.equal(dlg.open, true);
  assert.match(dlg.textContent, /gpt-6.1-sol/);
  assert.equal(dlg.querySelectorAll(".switch").length, 0);
  dom.fire(dlg.querySelector("form"), "submit", { submitter: btn(dlg, "취소") });
  assert.equal(t.calls.length, 0);
  dlg.close();
  assert.equal($("dialog.rv-settings"), null);

  t.state.data.reviews = docWith({ settings: { provider: "codex", on_blocking: "notify" } });
  t.api.openSettings();
  dlg = $("dialog.rv-settings");
  assert.match(dlg.textContent, /gpt-x · 지금 사용 중/);
  assert.match(dlg.textContent, /PR 3개 · 마지막 검사 방금/);
  assert.match(dlg.textContent, /검사 기록 없음/);
  const cards = dlg.querySelectorAll(".start-card");
  cards[1].click(); // Claude
  assert.deepEqual(cards.map((c) => c.classList.contains("on")), [false, true, true, false]);
  cards[3].click(); // block
  const sw = dlg.querySelectorAll(".switch");
  sw[0].click(); sw[1].click(); sw[1].click();
  assert.equal(sw[0].getAttribute("aria-checked"), "false");
  const ok = btn(dlg, "저장");
  dom.fire(dlg.querySelector("form"), "submit", { submitter: ok }); await dom.flush();
  assert.deepEqual(t.calls.at(-1).body.settings, { provider: "claude", on_blocking: "block", repos_off: ["o/arkism"] });
  assert.equal($("dialog.rv-settings"), null);
  assert.match(t.notices.at(-1), /저장했어요/);
  assert.equal(t.renders(), 1);

  t.state.data.reviews = docWith({ items: [{ ...items()[1], model: "claude-x" }] });
  t.api.openSettings();
  dlg = $("dialog.rv-settings");
  assert.match(dlg.textContent, /gpt-6.1-sol/);
  fail = true;
  dom.fire(dlg.querySelector("form"), "submit", { submitter: btn(dlg, "저장") }); await dom.flush();
  assert.equal(t.notices.at(-1), "저장하지 못했어요 (denied).");
  assert.ok($("dialog.rv-settings"));
  dlg.close();
  // a codex model with no " / " part
  t.state.data.reviews = docWith({ items: [{ ...items()[1], model: "codex" }] });
  t.api.openSettings();
  assert.match($("dialog.rv-settings").textContent, / · 지금 사용 중/);
  $("dialog.rv-settings").close();
});
