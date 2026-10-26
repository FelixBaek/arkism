// Settings (pages.js) driven through the booted app: every tab's buttons, the desktop shell calls they make and what they say.
// Run: node --test desktop/ui/*.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("fs");
const path = require("path");
const { boot } = require("./testdata/app-harness.js");

const fixture = (name) => JSON.parse(fs.readFileSync(path.join(__dirname, "testdata/preview/v1", name), "utf8"));
let app, answers = [], nextFile = null, blobs = [], postFails = false, lastAssist = "";

// The server's agent list with an agent found but off, one more off, a pending request and a second shared folder.
function agentTools() {
  const d = fixture("agent_tools.json"), t = d.agent_tools;
  t.agents.push({ id: "opencode", name: "OpenCode", enabled: true, found: true }, { id: "cursor", name: "Cursor", enabled: true, found: true }, { id: "aider", name: "Aider", enabled: false, found: true, skills_dirs: ["~/.aider/skills"] }, { id: "cline", name: "Cline", enabled: false });
  t.shared_dirs = ["~/.agents/skills", "~/team/skills"];
  t.requests.push({ id: "r2", kind: "agent", agent: "orbit", enabled: true, status: "pending" });
  return d;
}
async function api(a) {
  const p = a.path.split("?")[0];
  if (a.method === "GET" && p === "/v1/agent_tools") return { status: 200, body: JSON.stringify(agentTools()) };
  // the capabilities echo the asked scope, so the account checks of the transfer buttons pass
  if (p === "/v1/capabilities") return { status: 200, body: JSON.stringify({ ...fixture("capabilities.json"), scope: Object.fromEntries(new URLSearchParams(a.path.split("?")[1])) }) };
  // the category AI puts every unsorted skill in the first category, plus one name it never asked about
  if (a.method === "POST" && p === "/v1/crew/assists" && /Categories:/.test(a.body)) { lastAssist = JSON.parse(a.body).assist.prompt; return { status: 200, body: '{"id":"cat"}' }; }
  if (p === "/v1/crew/assists/cat") {
    const cat = /Categories: ([^,\n]+)/.exec(lastAssist)[1], skills = lastAssist.split("(name: description):\n")[1].split("\n\n")[0].split("\n").map((l) => l.split(":")[0]);
    const map = Object.fromEntries([...skills.map((n) => [n, cat]), ["no-such-skill", cat], [skills[0] + "x", "no-such-category"]]);
    return { status: 200, body: JSON.stringify({ crew_assist: { status: "done", output: "답: " + JSON.stringify(map) } }) };
  }
  if (postFails && a.method === "POST" && p === "/v1/agent_tools/requests") return { status: 500, body: JSON.stringify({ code: "boom" }) };
  return null;
}

test.before(async () => {
  app = await boot({
    stored: { "arkism.prefs": "{broken", "arkism.theme": "light" },
    invoke: { app_defaults: { local: true, updates: true }, ssh_host_key_check: { state: "trusted" } },
    beforeEntry: (env) => {
      env.window.apiOverride = api;
      env.window.__TAURI__.app = { getVersion: async () => "0.1.1" };
      globalThis.ask = async () => (answers.length ? answers.shift() : true);
      env.document.styleSheets = [
        { get cssRules() { throw new Error("cross-origin"); } },
        { cssRules: [{ selectorText: "body", style: [] }, { selectorText: ":root", style: Object.assign(["--bg-window", "--neutral-100", "--amber-500", "--type-heading", "--type-body", "--type-heading-tracking", "--space-4", "--space-0", "--radius-8", "--radius-full", "--shadow-sm", "plain"], { getPropertyValue: (n) => ({ "--space-4": "16px", "--space-0": "0", "--radius-8": "8px", "--radius-full": "999px", "--type-heading": "600 16px var(--font-family-sans)" })[n] || "#000" }) }] }
      ];
    }
  });
  const click = app.window.Element.prototype.click;
  app.window.Element.prototype.click = function () {
    if (this.localName === "input" && this.type === "file") { this.files = nextFile ? [nextFile] : []; this.dispatchEvent(new app.window.Event("change")); return; }
    return click.call(this);
  };
  URL.createObjectURL = (blob) => { blobs.push(blob); return "blob:test"; };
  URL.revokeObjectURL = () => {};
});
test.after(() => app?.stop());

const text = (n) => app.dom.text(n);
const notice = () => text(app.$$(".dlg-notice").at(-1) || app.$("#notice"));
const find = (label, root = app.document) => root.querySelectorAll("button").find((b) => text(b) === label || b.getAttribute("aria-label") === label);
async function press(label, root, ms) { const b = find(label, root); assert.ok(b, `button ${label}`); b.click(); await app.settle(ms); return b; }
// Waits on fake timers until `re` shows in the notice (a save that restarts the built-in Arkism takes a few rounds).
async function until(re) { for (let i = 0; i < 40 && !re.test(notice()); i++) await app.settle(100); assert.match(notice(), re); }
async function mode(title) { app.$$(".mode-card").find((c) => text(c).startsWith(title)).click(); await app.settle(); }
async function settings(tab) { app.window.dispatchEvent(new app.window.CustomEvent("arkism:open-settings", { detail: { tab } })); await app.settle(); }
const file = (content, size = 10) => ({ size, text: async () => content });
const cmd = (name) => app.calls.filter(([c]) => c === name);

test("an update found at start is announced and installs from 정보", async () => {
  await settings("info");
  assert.ok(cmd("update_check").length);
  assert.match(text(app.$("#table")), /새 버전 0\.1\.2이 있어요/);
  for (const [error, said] of [["update_manual", /GitHub Releases/], ["bad hash", /맞지 않아/], ["other", /설치하지 못했어요/]]) {
    app.fake.update_install = new Error(error);
    await press("지금 업데이트");
    assert.match(text(app.$("#table")), said);
  }
  app.fake.update_check = new Error("unreachable");
  await press("다시 확인");
  assert.match(text(app.$("#table")), /업데이트 서버에 연결하지 못했어요/);
  app.fake.update_check = new Error("x");
  await press("다시 확인");
  assert.match(text(app.$("#table")), /업데이트를 확인하지 못했어요/);
  app.fake.update_check = { current: "0.1.2", latest: "0.1.2", newer: false };
  await press("다시 확인");
  assert.match(text(app.$("#table")), /최신 버전이에요/);
  app.fake.update_install = null;
});

test("일반: start at login, capture switch, excluded folders and the memo", async () => {
  await settings("general");
  app.fake.autostart_set = null;
  await press("로그인할 때 Arkism 시작");
  assert.match(notice(), /시작하지 않아요/);
  delete app.fake.autostart_set;
  await press("로그인할 때 Arkism 시작");
  assert.match(notice(), /시작 설정을 바꾸지 못했어요/);
  await press("답에 안 드러난 기억 모으기");
  app.fake.set_capture_settings = null;
  await press("수집");
  assert.match(notice(), /수집을 멈췄어요/);
  await press("수집");
  const input = app.$('[aria-label="뺄 폴더 경로"]');
  await press("폴더 빼기"); // empty: nothing happens
  input.value = "~/private";
  await press("폴더 빼기");
  assert.match(notice(), /이미 뺀 폴더/);
  input.value = "C:\\work";
  app.dom.key(input, "Enter"); await app.settle();
  assert.match(notice(), /수집하지 않아요/);
  await press("빼기 취소");
  assert.match(notice(), /다시 수집해요/);
  app.dom.type(app.$('[aria-label="기억할 것에 대한 메모"]'), "고객 이름은 빼"); await app.settle();
  assert.match(notice(), /메모를 저장했어요/);
  delete app.fake.set_capture_settings;
  await press("수집");
  assert.match(notice(), /수집 설정을 저장하지 못했어요/);
  app.fake.autostart_get = new Error("x"); app.fake.capture_settings = new Error("x");
  await settings("info"); await settings("general");
  assert.match(text(app.$("#table")), /확인 못 함/);
  assert.match(text(app.$("#table")), /수집 설정을 읽지 못했어요/);
  app.fake.autostart_get = true; app.fake.capture_settings = { paused: true, exclude: [] };
});

test("에이전트: switches, removal, shared folders and the add dialog", async () => {
  await settings("agents");
  assert.match(text(app.$("#table")), /적용 대기/);
  // one 에이전트 추가, the last row of the agent list (Figma 1300:5254); 역할별 모델 is a card on this page, not a tab
  const adds = app.$$("button").filter((b) => text(b) === "에이전트 추가");
  assert.equal(adds.length, 1);
  assert.equal(adds[0].className, "ag-add-agent");
  assert.ok(!app.$$("#area-tabs button").some((b) => text(b) === "역할별 모델"));
  assert.match(text(app.$(".ag-roles")), /라우팅 판정.*기억 추출.*반성·검토.*임베딩/);
  // the first row is chosen: its detail (계정, Arkism 연결) is in the right column
  assert.ok(app.$(".ag-row.on"));
  assert.match(text(app.$(".ag-link")), /Arkism 연결/);
  for (const name of ["Codex", "Gemini CLI", "ZCode", "Grok", "Cursor", "OpenCode"]) { const b = find(`${name} 켜기`); if (b) { b.click(); await app.settle(); } }
  await press("Claude Code 켜기");
  assert.match(notice(), /하나는 켜져 있어야/);
  await press("Codex 켜기");
  await press("Orbit 켜기");
  assert.match(notice(), /Orbit를 껐어요/);
  app.$$(".ag-row-main").find((b) => text(b).startsWith("Orbit")).click(); await app.settle();
  answers.push(false); await press("에이전트 제거");
  await press("에이전트 제거");
  assert.match(notice(), /Orbit를 뺐어요/);
  await press("~/team/skills 빼기");
  assert.match(notice(), /공용 폴더를 뺐어요/);
  const dir = app.$('input[placeholder="~/team/skills"]');
  dir.value = "../x"; await press("폴더 추가");
  assert.match(notice(), /~\/ 나 \/ 로 시작/);
  dir.value = "~/more"; await press("폴더 추가");
  assert.match(notice(), /공용 폴더를 넣었어요/);
  postFails = true;
  await press("~/team/skills 빼기");
  assert.match(notice(), /보내지 못했어요/);
  postFails = false;

  await app.click(".found-bar");
  let dlg = app.$("dialog.add-agent");
  assert.ok(dlg.open);
  assert.equal(text(find("Aider 추가", dlg)), "Aider 추가");
  app.dom.type(dlg.querySelector('input[placeholder="에이전트 이름으로 찾기"]'), "cl");
  await press("Cline~/.aider/skills".replace("~/.aider/skills", ""), dlg).catch(() => {});
  dlg.querySelector(".agent-choice").click(); await app.settle();
  await press("Cline 추가", dlg);
  assert.match(notice(), /Cline를 추가했어요/);

  await press("에이전트 추가");
  dlg = app.$("dialog.add-agent");
  const name = dlg.querySelector('input[placeholder="예: Aider"]');
  app.dom.type(name, "My Tool!");
  await press("My Tool! 추가", dlg);
  assert.match(notice(), /영문 이름과/);
  assert.ok(dlg.open);
  dlg.querySelector('input[placeholder="~/.aider/skills"]').value = "~/.mytool/skills";
  dlg.querySelector('input[placeholder="~/.aider/history"]').value = "~/.mytool/chats";
  await press("My Tool! 추가", dlg);
  assert.match(notice(), /My Tool!를 추가했어요/);
  const posted = app.apiCalls.filter(([m, p]) => m === "POST" && p === "/v1/agent_tools/requests").map(([, , b]) => JSON.parse(b));
  assert.ok(posted.some((b) => b.agent === "my-tool" && b.chats_dir === "~/.mytool/chats"));
  app.dom.type(name, ""); // name cleared: back to the choice, or nothing
  await press("에이전트 추가");
  dlg = app.$$("dialog.add-agent").at(-1);
  app.dom.type(dlg.querySelector('input[placeholder="예: Aider"]'), "x");
  app.dom.type(dlg.querySelector('input[placeholder="예: Aider"]'), "");
  await press("취소", dlg);
  assert.equal(app.$("dialog.add-agent"), null);
});

test("에이전트 › 판정 모델과 폴더 위치는 빠진 값을 알려 주고 저장한다", async () => {
  await settings("agents");
  app.fake.set_model_roles = null;
  const kit = () => app.$(".v4-kit-body");
  const roles = () => app.$(".v4-roles-body");
  const save = () => press("저장", kit());
  const choose = async (label, value) => { const sel = roles().querySelector(`[aria-label="${label}"]`); sel.value = value; app.dom.fire(sel, "change"); await app.settle(); };
  await choose("라우팅 판정 모델", "clef"); await save();
  assert.match(notice(), /Cloudflare 계정 ID와 토큰/);
  await choose("라우팅 판정 모델", "jev"); await save();
  assert.match(notice(), /API 키가 필요/);
  await choose("라우팅 판정 모델", "chat"); await save();
  assert.match(notice(), /주소와 모델 이름/);
  roles().querySelector('[aria-label="판별 모델 주소"]').value = "http://example.com";
  roles().querySelector('[aria-label="판별 모델"]').value = "qwen3:4b";
  await save();
  assert.match(notice(), /https이거나/);
  roles().querySelector('[aria-label="판별 모델 주소"]').value = "http://127.0.0.1:11434/v1";
  await choose("기억 추출 모델", "opus"); await choose("임베딩", "none");
  kit().querySelector('[aria-label="스킬 폴더"]').value = "relative/path";
  await save();
  assert.match(notice(), /전체 경로여야/);
  kit().querySelector('[aria-label="스킬 폴더"]').value = "C:\\skills";
  await press("저장", kit());
  await until(/^저장했어요/);
  assert.equal(cmd("set_model_roles").at(-1)[1].roles.skills_root, "C:\\skills");
  assert.equal(cmd("set_model_roles").at(-1)[1].roles.extract_model, "opus");
  assert.equal(cmd("set_model_roles").at(-1)[1].roles.embedder, "none");
  app.fake.set_model_roles = new Error("invalid model");
  await save();
  assert.match(notice(), /모델 이름이나 주소/);
  app.fake.set_model_roles = new Error("x");
  await save();
  assert.match(notice(), /저장하지 못했어요/);
  app.fake.set_model_roles = null;
  const saved = app.fake.model_roles;
  app.fake.model_roles = new Error("x");
  await settings("info"); await settings("agents");
  assert.match(text(app.$(".ag-roles")), /모델 설정을 읽지 못했어요/);
  app.fake.model_roles = saved;
});

test("카테고리 › AI로 전부 다시 분류 proposes and applies before 저장", async () => {
  await settings("categories");
  await press("AI로 전부 다시 분류");
  const chip = app.$$(".ex-ai-chip").find((c) => text(c) === "전부 분류해 줘");
  chip.click();
  for (let i = 0; i < 30 && !find("모두 적용"); i++) await app.settle(500);
  await press("모두 적용");
  assert.match(text(app.$("#table")), /저장하지 않은 변경/);
  await press("되돌리기");
});

test("카테고리: draft edits, the save bar, order and delete", async () => {
  await settings("categories");
  await press("새 카테고리");
  const side = () => app.$("#detail");
  const name = side().querySelector('[aria-label="카테고리 이름"]');
  name.value = "릴리스"; app.dom.fire(name, "input"); app.dom.fire(name, "change"); await app.settle();
  side().querySelectorAll(".icon-pick button")[1].click(); await app.settle();
  const kind = side().querySelector('[aria-label="규칙 종류"]');
  kind.value = "path"; app.dom.fire(kind, "change"); await app.settle();
  const val = side().querySelector('[aria-label="규칙 값"]');
  val.value = "release"; app.dom.fire(val, "change"); await app.settle();
  await press("조건 추가");
  await press("조건 지우기");
  await press("위로");
  assert.match(text(app.$("#table")), /저장하지 않은 변경/);
  await press("되돌리기");
  assert.doesNotMatch(text(app.$("#table")), /저장하지 않은 변경/);
  app.$("#table").querySelectorAll(".cat-table .v4-tr:not(.th)")[1].click(); await app.settle();
  await press("카테고리 지우기");
  await press("저장");
  assert.match(notice(), /카테고리를 저장했어요/);
  for (let i = 0; i < 6; i++) { const b = find("카테고리 지우기"); if (!b) break; b.click(); await app.settle(); }
  assert.match(text(app.$("#table")), /카테고리가 없어요/);
  await press("저장");
});


test("고급: ComfyUI address, settings transfer, doctor, backups and erase", async () => {
  Object.assign(app.fake, {
    doctor: ({ fix }) => ({ checks: [{ status: "ok", message: "연결" }, { status: "warn", message: "수집 늦음", fix: "다시 시도" }, { status: "fail", message: "디스크 부족" }], summary: { ok: 1, warn: 1, fail: 1 }, fixes: fix ? [{ message: "수집을 다시 시도했어요." }] : [] }),
    local_backups: { backups: Array.from({ length: 12 }, (_, i) => ({ name: `arkism-202610${String(i + 10)}-010203`, created: `202610${String(i + 10)}-010203`, bytes: i ? 4096 : 3 * 1048576, auto: i % 2 === 0, app_version: i ? "0.1.1" : "" })) },
    local_restore: { safety_copy: "arkism-20261023-090000" }, local_backup: { bytes: 2048 }, local_erase: null, search_mode: "keyword",
    open_backups: new Error("x")
  });
  await settings("advanced");
  const table = () => text(app.$("#table")), side = () => app.$("#detail");
  assert.match(table(), /키워드 검색/);
  assert.match(table(), /오래된 백업 2개/);
  const comfy = side().querySelector('input[type="url"]');
  comfy.value = "http://127.0.0.1:8188//"; app.dom.fire(comfy, "change"); await app.settle();
  assert.match(notice(), /ComfyUI 주소를 저장/);

  await press("점검", side());
  assert.match(text(side()), /정상 1 · 확인 필요 1 · 문제 1/);
  assert.match(text(side()), /디스크 부족/);
  await press("고치기", side());
  assert.match(text(side()), /수집을 다시 시도했어요/);
  app.fake.doctor = new Error("x");
  await press("점검", side());
  assert.match(text(side()), /점검하지 못했어요/);

  blobs = [];
  await press("PC 설정 내보내기", side());
  assert.match(notice(), /설정 파일을 저장/);
  nextFile = file(await blobs[0].text());
  const reloads = app.window._reloaded || 0;
  await press("PC 설정 가져오기", side()); await app.settle(700);
  assert.ok(app.window._reloaded > reloads);
  nextFile = file("{}");
  await press("PC 설정 가져오기", side());
  assert.match(notice(), /가져오지 못했어요/);
  nextFile = file("{}", 2 * 1048576); // too big: nothing happens
  await press("PC 설정 가져오기", side());
  for (const [out, inn, said] of [["기억 정리 설정 내보내기", "기억 정리 설정 가져오기", /기억 정리 설정을 가져왔어요/], ["스킬 제안 내보내기", "스킬 제안 가져오기", /스킬 제안 초안 \d+개를 가져왔어요/]]) {
    blobs = [];
    await press(out, side());
    assert.ok(blobs.length, out);
    nextFile = file(await blobs[0].text());
    await press(inn, side()); await app.settle(700);
    assert.match(notice(), said);
    nextFile = file("not json");
    await press(inn, side());
    assert.match(notice(), /가져오지 못했어요/);
  }
  nextFile = null;

  answers.push(false); await press("이 백업으로 되돌리기");
  answers.push(true); await press("이 백업으로 되돌리기");
  await until(/바꾸기 전 데이터는/);
  app.fake.local_restore = { safety_copy: "" };
  answers.push(true); await press("이 백업으로 되돌리기");
  await until(/^되돌렸어요\.$/);
  app.fake.local_restore = new Error("local_restore_failed: 20261023-090000");
  await settings("advanced");
  answers.push(true); await press("이 백업으로 되돌리기");
  await until(/되돌리지 못했어요/);
  await settings("advanced");
  await press("지금 백업");
  await until(/백업했어요 · 2KB/);
  app.fake.local_backup = new Error("local_backup_nothing");
  await press("지금 백업");
  assert.match(notice(), /백업할 데이터가 없어요/);
  app.fake.local_backup = new Error("x");
  await press("지금 백업");
  assert.match(notice(), /작업하지 못했어요/);
  await press("백업 폴더 열기");
  assert.match(notice(), /폴더를 열지 못했어요/);
  answers.push(false); await press("모든 데이터 지우기");
  answers.push(true, false); await press("모든 데이터 지우기");
  assert.equal(cmd("local_erase").length, 0);
  answers.push(true, true); await press("모든 데이터 지우기");
  await until(/모두 지웠어요/);
  app.fake.local_erase = new Error("local_busy");
  await settings("advanced");
  answers.push(true, true); await press("모든 데이터 지우기");
  await until(/다른 작업이 끝난 뒤/);
  app.fake.local_backups = new Error("x"); app.fake.search_mode = "ollama:qwen3";
  await settings("advanced");
  assert.match(table(), /백업 목록을 읽지 못했어요/);
  assert.match(table(), /의미 검색/);
  app.fake.local_backups = { backups: [] };
  await settings("advanced");
  assert.match(table(), /아직 백업이 없어요/);
  const clear = find("기록 지우기");
  if (clear) { clear.click(); await app.settle(); assert.match(notice(), /속도 기록을 지웠어요/); }
  answers.push(false); await press("가져오기");
});

test("모양: theme, font size and zoom apply at once", async () => {
  await settings("appearance");
  await press("어둡게"); await press("크게");
  const zoom = () => app.$(".zoom-control");
  zoom().querySelectorAll("button")[1].click(); await app.settle();
  assert.equal(text(zoom().querySelector("output")), "110%");
  zoom().querySelectorAll("button")[0].click(); await app.settle();
  await press("기본값");
  assert.equal(text(zoom().querySelector("output")), "100%");
  assert.equal(app.document.documentElement.dataset.theme, "dark");
});

test("Arkism 토큰: the design system page reads the live tokens", async () => {
  await app.click('.nav-item[data-menu="gallery"]');
  const tab = app.$$("#area-tabs .area-tab").find((t) => /디자인/.test(text(t)));
  if (tab) await app.click(tab);
  await press("Arkism 토큰");
  assert.match(text(app.$("#table")), /bg-window/);
  for (const t of ["글자", "간격·모서리", "부품", "색"]) await press(t);
  await press("스위치 견본"); await press("스위치 견본");
  await press("라이트로 보기");
  assert.equal(app.document.documentElement.dataset.theme, "light");
  await press("다크로 보기");
  await press("라이브러리로");
});

test("연결: SSH test, connect, failure, then the server side", async () => {
  await settings("connect");
  await mode("SSH로 연결");
  await press("SSH 테스트", app.$("#table"));
  assert.match(text(app.$(".mode-out")), /호스트를 넣어 주세요/);
  await mode("이 PC");
  await press("저장하고 다시 연결");
  assert.match(text(app.$(".mode-out")), /이미 이 PC의 Arkism/);
  await mode("SSH로 연결");
  app.$(".mode-body input").value = "me@server";
  await press("SSH 테스트", app.$("#table"));
  assert.match(text(app.$(".mode-out")), /확인했어요/);
  app.fake.ssh_connect = new Error("ssh_unreachable");
  await press("저장하고 다시 연결");
  assert.match(text(app.$(".mode-out")), /서버에 닿지 못했어요/);
  app.fake.ssh_connect = { server: app.origin, token: "preview", workspace_id: "default", project_id: "default", ssh_host: "me@server" };
  await press("저장하고 다시 연결");
  await app.settle(200);
  assert.equal(app.storage.getItem("arkism.mode"), "remote");
  // connected to the server: 설정 › 연결 opens on the SSH card, with 연결 끊기 beside 저장하고 다시 연결
  await settings("connect");
  assert.match(text(app.$(".mode-card.active")), /^SSH로 연결/);
  assert.ok(find("연결 끊기", app.$("#table")));
  await press("연결 끊기");
  await settings("connect");
  await settings("agents");
  assert.match(text(app.$(".ag-roles")), /서버가 정함/);
  await settings("advanced");
  assert.doesNotMatch(text(app.$("#table")), /지금 백업/);
  await settings("connect");
  // last step: the reload does not restart the app in the DOM stand-in, so nothing runs after it
  await mode("이 PC");
  const reloads = app.window._reloaded || 0;
  await press("저장하고 다시 연결"); // back to the built-in Arkism: remembers this PC as the choice and reloads
  assert.ok(app.window._reloaded > reloads);
  assert.equal(app.storage.getItem("arkism.mode"), "local");
});
