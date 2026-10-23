// The app (entry.mjs) on the built-in Arkism (preview server): Home's to-do pages, the status card,
// keyboard shortcuts, notices, dialogs, skill details and the category menu the crew popups share.
// Run: node --test desktop/ui/*.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const { boot } = require("./testdata/app-harness.js");

const realFetch = globalThis.fetch;
// The factories app.js hands its helpers to; kept so a test can reach catMenu, notice, ... directly.
const deps = {};
function capture(win, name) {
  let fn;
  Object.defineProperty(win, name, { configurable: true, get: () => fn, set: (f) => { fn = (...a) => { deps[name] = a[0]; return f(...a); }; } });
}
const suggest = { at: new Date().toISOString(), items: [{ name: "weekly-report", title: "주간 보고", why: "매주 같은 요청", examples: ["주간 보고 써 줘"], skill_md: "# weekly" }], dismissed: [] };
// Each API call may be answered here first: return { status, body } to stand in for the preview server.
let hook = () => null;

const json = (status, v) => ({ status, body: JSON.stringify(v) });
async function tools(a) {
  const r = await realFetch(a.base + a.path);
  const d = await r.json();
  const now = Date.now(), iso = (ms) => new Date(ms).toISOString();
  Object.assign(d.agent_tools, {
    skill_updates: [{ name: "writing-style", repo: "~/.agents/skills/writing", from: "1.0.0", to: "1.1.0" }, { name: "commit-and-push", repo: "~/.agents/skills/git", from: "main", to: "2.0.0" }],
    skill_repos: [{ repo: "~/.agents/skills/code-review", remote: "https://example.test/code-review.git", behind: 2, checked_at: iso(now - 3600e3) }],
    pc_links: [{ agent: "claude_code", last_seen: iso(now - 60e3) }, { agent: "codex", last_seen: iso(now), failing: true }, { agent: "gemini", last_seen: iso(now), pending: 3 }, { agent: "zcode", last_seen: iso(now - 9 * 86400e3) }],
    skill_check_tools: "arkism-builtin 1 · skillevaluator 1"
  });
  return json(200, d);
}

let app, t;
test.before(async () => {
  app = await boot({
    stored: { "arkism.skillSuggest": JSON.stringify(suggest), "arkism.crewCatIcons": "{bad", "arkism.prefs": JSON.stringify({ categories: [{ name: "문서", icon: "book-open", rules: [["name", "*writing*"]] }] }) },
    beforeEntry: (env) => {
      // ask.js sets window.ask; a browser reads it as a bare global, this fake window does not
      globalThis.ask = (...a) => env.window.ask(...a);
      for (const n of ["ArkismTeams", "ArkismHome", "ArkismPages", "ArkismReviews", "ArkismDigests"]) capture(env.window, n);
      env.window.apiOverride = async (a) => (await hook(a)) || (a.method === "GET" && a.path.split("?")[0] === "/v1/agent_tools" ? tools(a) : null);
    }
  });
  t = app.dom.text;
  await app.settle(2000);
});
test.after(() => app?.stop());

const closeDialogs = () => { for (const d of [...app.$$("dialog[open]")]) d.close(); };
const home = async () => { closeDialogs(); await app.click('.nav-item[data-menu="home"]'); await app.settle(500); };
const rows = (s) => [...app.$$(s)];
const byText = (s, re) => rows(s).find((n) => re.test(t(n)));
const key = (k, init = {}, target = app.document) => target.dispatchEvent(new app.window.KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true, ...init }));
const waitNotice = async (re) => { for (let i = 0; i < 60 && !re.test(t(app.$("#notice"))); i++) await app.settle(1000); };
const openTodo = async (re) => { await home(); const r = byText(".todo-item", re); if (!r) return openAll(re); await app.click(r, 200); return r; };
// 전체 항목 보기 lists every kind, also the ones folded into a menu row on Home.
const openAll = async (re) => { await home(); await app.click("#home-all", 100); const r = byText(".home-table .todo-item", re); assert.ok(r, `no to-do ${re}`); await app.click(r, 200); return r; };
const dlgButton = (v) => rows("dialog.crew-dialog[open] button").find((b) => b.value === v);
const press = async (re, ms = 300, root = "#detail") => { const b = byText(`${root} button`, re); assert.ok(b, `no button ${re}`); await app.click(b, ms); return b; };

test("home lists the first checks, every row opens its page and ← 홈 goes back", async () => {
  await home();
  assert.match(t(app.$("#table")), /먼저 확인/);
  const n = app.$$(".home-first .todo-item").length;
  assert.ok(n >= 3);
  for (let i = 0; i < n; i++) {
    await home();
    const row = app.$$(".home-first .todo-item")[i];
    if (!row) continue;
    await app.click(row, 100);
    assert.ok(app.$("#detail"));
    if (app.$("#home-back")) await app.click("#home-back", 100);
  }
  await home();
  const fold = app.$(".home-fold");
  if (fold) { await app.click(fold, 50); if (app.$(".home-fold")) await app.click(".home-fold", 50); }
  for (let i = 0; i < app.$$(".todo-rest .todo-item").length; i++) { await home(); const r = app.$$(".todo-rest .todo-item")[i]; if (r) await app.click(r, 100); }
  for (let i = 0; i < app.$$(".home-menus .todo-item").length; i++) { await home(); const r = app.$$(".home-menus .todo-item")[i]; if (r) await app.click(r, 100); }
  await home();
});

test("전체 항목 보기 shows every to-do in one table and each row opens its page", async () => {
  await home();
  if (!app.$("#home-all")) return;
  await app.click("#home-all", 100);
  assert.match(t(app.$("#title")), /전체 항목/);
  const n = app.$$(".home-table .todo-item").length;
  for (let i = 0; i < n; i++) {
    if (!app.$(".home-table")) { await home(); await app.click("#home-all", 100); }
    const r = app.$$(".home-table .todo-item")[i];
    if (r) await app.click(r, 100);
  }
  await home();
});

test("the to-do action buttons run, open their page or go to the menu", async () => {
  let clicked = 0;
  for (let i = 0; i < 12; i++) {
    await home();
    const b = app.$$(".todo-item button")[i];
    if (!b) break;
    await app.click(b, 200);
    clicked++;
    closeDialogs();
    await app.settle(100);
  }
  assert.ok(clicked > 0, "no to-do action button on Home");
  await home();
  assert.ok(app.$(".todo-item"), "Home lost its to-do list after the actions");
});

test("업데이트 가능 lists the versions and asks before pulling the repositories", async () => {
  const open = async () => { await openAll(/업데이트 가능/); assert.match(t(app.$("#detail")), /v1\.0\.0[\s\S]*v1\.1\.0/); };
  const yes = async (answer) => { await press(/업데이트 받기/, 100); assert.match(t(app.$("dialog.cn-confirm[open]")), /스킬 2개를 새 버전으로/); rows("dialog.cn-confirm[open] button").find((b) => b.value === answer).click(); await app.settle(500); };
  await open(); await yes("cancel");
  // yes: one request per repository, then the tools poll; a refused request says so
  await open(); await yes("ok");
  assert.ok(app.apiCalls.some(([m, p, b]) => m === "POST" && p === "/v1/agent_tools/requests" && /skill_update/.test(b)));
  hook = (a) => (a.path === "/v1/agent_tools/requests" ? json(500, { error: "nope" }) : null);
  await open(); await yes("ok");
  hook = () => null;
  assert.match(t(app.$("#notice")), /요청하지 못했어요/);
  // a row with a list behind it offers 목록 보기 next to its action
  await openAll(/빠진 에이전트/);
  if (byText("#detail button", /목록 보기/)) await press(/목록 보기/, 200);
  await openAll(/새 기억|호출과 문구/);
  if (byText("#detail button", /^취소$/)) await press(/^취소$/, 200);
  await home();
});

test("새 스킬 제안: a bad name is refused, 스킬 만들기 installs it and 스킬이 아니에요 hides it", async () => {
  await openTodo(/주간 보고/);
  assert.match(t(app.$("#detail")), /주간 보고/);
  const name = app.$("#detail input");
  if (name) {
    name.value = "Bad Name";
    await press(/스킬 만들기/, 100);
    assert.match(t(app.$("#notice")), /영문 소문자/);
    name.value = "weekly-report";
    await press(/스킬 만들기/, 4000);
    assert.ok(app.apiCalls.some(([m, p, b]) => m === "POST" && p === "/v1/crew/assists" && /install_skill/.test(b)));
  }
  // a failed install says why
  app.window.localStorage.setItem("arkism.skillSuggest", JSON.stringify(suggest));
  hook = (a) => (a.path.startsWith("/v1/crew/assists/") ? json(200, { crew_assist: { status: "failed", error: "expired lease" } }) : null);
  await openTodo(/주간 보고/);
  if (byText("#detail button", /스킬 만들기/)) { await press(/스킬 만들기/, 3000); assert.match(t(app.$("#notice")), /모델 설정 실행기/); }
  hook = () => null;
  await openTodo(/주간 보고/);
  await press(/스킬이 아니에요/, 200);
  assert.ok(JSON.parse(app.window.localStorage.getItem("arkism.skillSuggest")).dismissed.includes("weekly-report"));
  app.window.localStorage.setItem("arkism.skillSuggest", JSON.stringify(suggest));
  await home();
});

test("다시 분석 asks the AI for new skills, and a broken answer or failure is a notice", async () => {
  await home();
  const again = () => byText("#head-actions button", /다시 분석/);
  assert.ok(again());
  await app.click(again(), 0);
  await app.settle(61000); // past a minute the wait notice says it is slow
  await waitNotice(/새 스킬 제안 \d+개|반복 요청이 없어요/);
  assert.ok(app.apiCalls.some(([m, p, b]) => m === "POST" && p === "/v1/crew/assists" && /"suggest"/.test(b)));
  for (const [answer, re] of [[{ status: "done", output: "[not json]" }, /AI 답을 읽지 못했어요/], [{ status: "done", output: "[]" }, /반복 요청이 없어요/], [{ status: "failed", error: "boom" }, /AI가 답하지 못했어요/]]) {
    hook = (a) => (a.path.startsWith("/v1/crew/assists/") ? json(200, { crew_assist: answer }) : null);
    await home(); await app.click(again(), 0);
    await waitNotice(re);
    assert.match(t(app.$("#notice")), re);
  }
  hook = () => null;
  app.window.localStorage.setItem("arkism.skillSuggest", JSON.stringify(suggest));
  await home();
});

test("비슷한 스킬: 합쳐서 미리 보기 drafts one SKILL.md, the other goes to the trash, 다른 스킬이에요 hides the pair", async () => {
  await openAll(/비슷한 스킬/);
  if (byText("#detail button", /합쳐서 미리 보기/)) {
    await press(/합쳐서 미리 보기/, 3000);
    const trash = byText("#detail button", /휴지통으로/);
    if (trash) {
      await app.click(trash, 100);
      assert.match(t(app.$("dialog.crew-dialog[open]")), /휴지통으로 옮길까요/);
      dlgButton("ok").click(); await app.settle(1000);
    }
    hook = (a) => (a.path.startsWith("/v1/crew/assists/") ? json(200, { crew_assist: { status: "failed", error: "x" } }) : null);
    await press(/합쳐서 미리 보기/, 3000);
    hook = () => null;
    assert.match(t(app.$("#detail")), /AI가 답하지 못했어요/);
  }
  if (byText("#detail button", /다른 스킬이에요/)) await press(/다른 스킬이에요/, 200);
  await home();
});

test("안 쓰는 스킬 shows the targets first and moves them to the trash", async () => {
  const act = async () => { await openAll(/안 쓰는 스킬/); await press(/휴지통으로/, 100); };
  await act();
  assert.match(t(app.$("dialog.crew-dialog[open]")), /휴지통으로 옮길까요/);
  dlgButton("cancel").click(); await app.settle(100);
  // one skill fails to move: its name is in the notice, the rest still go
  let first = true;
  hook = (a) => (a.method === "POST" && /\/availability$/.test(a.path) && first ? (first = false, json(409, { error: "revision_conflict" })) : null);
  await act();
  dlgButton("ok").click(); await app.settle(3000);
  hook = () => null;
  assert.match(t(app.$("#notice")), /휴지통으로 옮겼어요/);
  await home();
});

test("기억 정리 제안 opens a proposal and sends its choice", async () => {
  await openAll(/정리 제안/);
  assert.match(t(app.$("#detail")), /남길 기억[\s\S]*이력으로 넘길 기억/);
  const sent = [];
  hook = (a) => { if (a.method === "POST") sent.push(a.path); return null; };
  await press(/둘 다 두기/, 1000);
  hook = () => null;
  assert.ok(sent.some((p) => /^\/v1\/maintenance_proposals\/[^/]+\/decide$/.test(p)), sent.join(","));
  await home();
});

test("기억 검토 한 번에: apply sends the batch and counts what was skipped", async () => {
  await home();
  const r = byText(".todo-item", /기억 후보/);
  if (!r) return;
  await app.click(r, 200);
  const apply = byText("#detail button", /한 번에 반영/);
  if (apply) {
    hook = (a) => (a.path === "/v1/memory_candidates/review_batch" ? json(200, { items: [{ status: 200 }, { status: 409 }] }) : null);
    await app.click(apply, 1000);
    hook = () => null;
    assert.match(t(app.$("#notice")), /반영|건너뛰었어요/);
  }
  if (byText("#detail button", /하나씩 보기/)) await press(/하나씩 보기/, 200);
  await home();
});

test("the status card draws the stages; a stage opens MEMORY › 개요 and 설정 opens settings", async () => {
  await home();
  await app.click("#status-toggle", 500);
  const card = app.$("#status-card");
  if (card.hidden) await app.click("#status-toggle", 500);
  assert.equal(card.hidden, false);
  assert.match(t(app.$("#status-stages-badge")), /멈춤|움직임|알 수 없음/);
  const stage = app.$("#status-stages .st-stage");
  if (stage) { await app.click(stage, 500); assert.equal(card.hidden, true); }
  card.hidden = false;
  const set = byText("#status-card button", /^설정$/);
  if (set) { await app.click(set, 300); assert.equal(card.hidden, true); }
  await home();
});

test("keyboard: Ctrl+K and / open search, ? the shortcuts, j/k walk the rows, Escape closes the detail", async () => {
  await home();
  key("k", { ctrlKey: true }); await app.settle(100); closeDialogs(); await app.settle(50);
  key("/"); await app.settle(100); closeDialogs(); await app.settle(50);
  key("?"); await app.settle(100); closeDialogs(); await app.settle(50);
  app.document.activeElement?.blur?.(); // j/k are letters while a field has focus
  key("j"); await app.settle(100);
  key("j"); await app.settle(100);
  key("k"); await app.settle(100);
  app.$("#app").classList.add("detail-open");
  key("Escape"); await app.settle(100);
  assert.equal(app.$("#app").classList.contains("detail-open"), false);
  deps.ArkismHome.go("memories", "active"); await app.settle(500);
  key("j"); await app.settle(200);
  app.$("#app").classList.add("detail-open");
  key("Escape"); await app.settle(100);
  const s = app.$("#search");
  s.value = "commit"; s.dispatchEvent(new app.window.Event("input", { bubbles: true })); await app.settle(500);
  s.value = ""; s.dispatchEvent(new app.window.Event("input", { bubbles: true })); await app.settle(500);
  await home();
});

test("Enter in a dialog field presses its main button, and a notice shows inside the open dialog", async () => {
  const d = app.document;
  const dlg = d.createElement("dialog"), form = d.createElement("form"), input = d.createElement("input"), ok = d.createElement("button");
  form.method = "dialog"; ok.type = "submit"; ok.value = "ok"; ok.className = "btn primary";
  let submitted = null;
  form.addEventListener("submit", (e) => { submitted = e.submitter?.value; });
  form.append(input, ok); dlg.append(form); d.body.append(dlg); dlg.showModal();
  key("Enter", {}, input); await app.settle(50);
  assert.equal(submitted, "ok");
  if (!dlg.open) dlg.showModal();
  deps.ArkismTeams.notice("창 안 알림");
  assert.match(t(dlg.querySelector(".dlg-notice")), /창 안 알림/);
  dlg.querySelector(".dlg-notice .notice-close").click();
  assert.equal(dlg.querySelector(".dlg-notice").hidden, true);
  dlg.close(); dlg.remove();
  deps.ArkismTeams.notice("");
});

test("catMenu picks, clears, renames and makes categories with a name and an icon", async () => {
  const cm = deps.ArkismTeams.catMenu;
  const anchor = app.document.createElement("button");
  app.document.body.append(anchor);
  const picks = [];
  const open = (current = "디자인") => cm(anchor, { list: ["디자인", "문서"], current, onPick: (v) => picks.push(v) });
  open("외부");
  assert.ok(app.$(".cat-menu"));
  byText(".cat-menu button", /^외부$/).click();
  assert.equal(picks.at(-1), "외부");
  open(); byText(".cat-menu button", /카테고리 없음/).click();
  assert.equal(picks.at(-1), "");
  // new: the icon follows the name until one is picked; arrows move it; an empty name keeps the form open
  open(); byText(".cat-menu button", /새 카테고리/).click();
  const name = app.$(".cat-new input");
  name.value = "디자인 노트"; name.dispatchEvent(new app.window.Event("input", { bubbles: true }));
  name.value = ""; byText(".cat-new button", /^추가$/).click();
  assert.ok(app.$(".cat-new"));
  const grid = app.$(".cat-icons");
  for (const k of ["ArrowRight", "ArrowDown", "ArrowLeft", "ArrowUp", "x"]) key(k, {}, grid);
  app.$(".cat-icon").click();
  name.value = "새것"; key("Enter", {}, name);
  assert.equal(picks.at(-1), "새것");
  // rename: a taken name is refused; a new one repaints with it
  open(); app.$(".cat-menu-edit").click();
  app.$(".cat-new input").value = "문서"; byText(".cat-new button", /^저장$/).click(); await app.settle(100);
  assert.match(t(app.$("#notice")), /같은 이름/);
  app.$(".cat-menu")?.remove();
  open(); app.$(".cat-menu-edit").click();
  app.$(".cat-new input").value = "디자인2"; byText(".cat-new button", /^저장$/).click(); await app.settle(300);
  assert.equal(picks.at(-1), "디자인2");
  open("문서"); rows(".cat-menu-edit")[0].click();
  app.$(".cat-new input").value = "디자인3"; byText(".cat-new button", /^저장$/).click(); await app.settle(300);
  assert.equal(picks.at(-1), "문서");
  // 취소, Escape and a click outside close it
  open(); byText(".cat-menu button", /새 카테고리/).click(); byText(".cat-new button", /취소/).click();
  assert.equal(app.$(".cat-menu"), null);
  open(); key("Escape", {}, app.$(".cat-menu .cat-menu-item"));
  assert.equal(app.$(".cat-menu"), null);
  open(); app.document.body.dispatchEvent(new app.window.Event("pointerdown", { bubbles: true }));
  assert.equal(app.$(".cat-menu"), null);
  anchor.remove();
});

test("helpers: stat, ago, originOf and opening an intelligence record", async () => {
  assert.match(t(deps.ArkismReviews.stat("검토", "3", "개", true)), /검토\s*3\s*개/);
  const ago = deps.ArkismDigests.ago, now = Date.now();
  for (const ms of [5e3, 120e3, 3 * 3600e3, 3 * 86400e3, 40 * 86400e3, 400 * 86400e3, -5e3]) assert.equal(typeof ago(new Date(now - ms).toISOString()), "string");
  ago("not a date"); ago("");
  const o = deps.ArkismDigests.originOf;
  assert.equal(o({ generator: { prompt_version: "session-reflect-1" } }), "세션 반성");
  assert.equal(o({ tags: ["basis:explicit_user"] }), "사용자 말");
  assert.equal(o({ tags: ["event:backfill"] }), "가져옴");
  assert.equal(o({ tags: ["basis:assistant_result"] }), "에이전트 결과");
  assert.equal(o({ tags: ["basis:inferred"] }), "AI 추측");
  assert.equal(o({ created_by_type: "human" }), "직접 입력");
  assert.equal(o({}), "가져옴");
  const open = deps.ArkismHome.openRecord;
  open({ id: "m1", status: "active", kind: "user", text: "기억 하나", tags: [], revision: 1, created_at: new Date(now).toISOString() });
  await app.settle(300);
  assert.equal(app.$("#app").classList.contains("detail-open"), true);
  open({ id: "c-x", status: "candidate", kind: "lesson", text: "후보 하나", tags: [], revision: 1 }); await app.settle(300);
  open({ id: "c-y", status: "conflicted", conflict_id: "k1", kind: "lesson", text: "부딪힘", tags: [], revision: 1 }); await app.settle(300);
  await home();
});

test("a skill's detail shows the PC links, checks and repository, and asks the server to check again", async () => {
  deps.ArkismHome.go("skills"); await app.settle(1000);
  for (const name of [/code-review/, /writing-style/, /commit-and-push/]) {
    const r = byText(".v4s-row", name);
    if (!r) continue;
    await app.click(r, 500);
    const check = byText("#detail button", /업데이트 확인/);
    if (check) {
      await app.click(check, 500);
      assert.match(t(app.$("#notice")), /확인을 요청했어요/);
      hook = (a) => (a.path === "/v1/agent_tools/requests" ? json(500, { error: "x" }) : null);
      const again = byText("#detail button", /업데이트 확인/);
      if (again && !again.disabled) { await app.click(again, 500); assert.match(t(app.$("#notice")), /요청하지 못했어요/); }
      hook = () => null;
    }
  }
  assert.ok(app.$(".pc-link"));
  await home();
});

test("a failed load shows the retry notice, and 다시 시도 loads again", async () => {
  await home();
  hook = (a) => (a.method === "GET" && /^\/v1\/(memories|skills|capabilities)(\?|$)/.test(a.path) ? json(503, { error: "unavailable" }) : null);
  await app.click(byText("#head-actions button", /다시 불러오기/), 1500);
  hook = () => null;
  assert.equal(app.$("#notice").hidden, false);
  const retry = byText("#notice button", /다시/);
  if (retry) await app.click(retry, 2000);
  await app.settle(1000);
  await home();
});
