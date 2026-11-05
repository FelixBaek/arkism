// Run with: node --test desktop/ui/*.test.js
// 설정 › 기억 공간 (spaces.js): the bundle preview, 대표 별칭 order, rename, clone and template bodies, the space list.
const test = require("node:test");
const assert = require("node:assert/strict");
const dom = require("./testdata/fake-dom.cjs");
const { document } = dom.install();
const h = dom.helpers(document);
require("./spaces.js");
const S = window.ArkismSpaces;

const manifest = (o) => JSON.stringify({ manifest: { format: "arkism.space", format_version: 1, template: false, sections: [], ...o } });

test("bundlePreview reads the manifest, the settings keys and the names of rules and outlines; a foreign or newer file is refused", () => {
  const schema = { "x-format": "arkism.space", "x-format-version": 1 };
  const text = [manifest({ template: true, sections: [{ name: "config", count: 1 }, { name: "directives", count: 2 }] }),
    JSON.stringify({ section: "config", config: { extraction_mode: "verbose", aliases: [], notes: "" } }),
    JSON.stringify({ section: "directives", record: { directive: { title: "리뷰는 파일별로" } } }),
    JSON.stringify({ section: "mental_models", record: { mental_model: { name: "주간 정리" } } }), "not json"].join("\n");
  assert.deepEqual(S.bundlePreview(text, schema), { template: true, sections: [{ name: "config", count: 1 }, { name: "directives", count: 2 }], settings: ["extraction_mode"], names: ["리뷰는 파일별로", "주간 정리"] });
  assert.equal(S.bundlePreview("{}", schema).error, "not_bundle");
  assert.equal(S.bundlePreview(manifest({ format: "other" }), schema).error, "not_bundle");
  assert.equal(S.bundlePreview(manifest({ format_version: 2 }), schema).error, "newer");
  assert.equal(S.bundlePreview(manifest({}), null).template, false, "no schema (an older server): read unchecked");
});

test("primaryMoves takes out and re-adds only the aliases in front of the pick", () => {
  assert.deepEqual(S.primaryMoves(["a", "b", "c"], "c"), ["a", "b"]);
  assert.deepEqual(S.primaryMoves(["a", "b"], "a"), []);
  assert.deepEqual(S.primaryMoves(["a"], "zz"), []);
  assert.ok(S.ALIAS.test("front-1.x") && !S.ALIAS.test("Front") && !S.ALIAS.test("-a"));
});

let typed; // what the person types into a typed confirm: undefined = the exact match, null = 취소
function setup({ role = "human", revision = 2, aliases = ["front", "web"] } = {}) {
  const state = { view: "settings", settingsTab: "space", server: "s", scope: { workspace_id: "w", project_id: "p" }, caps: { actions: role === "human" ? {} : { review_decide: false } } };
  const sent = [], asks = [];
  globalThis.ask = async (m) => { asks.push(m); return true; };
  globalThis.askText = async () => "새 이름";
  globalThis.askTyped = async (m, match) => { asks.push(m); return typed === null ? false : typed ?? match; };
  const request = async (path, o = {}) => {
    if (o.method) {
      const body = o.body ? JSON.parse(o.body) : null;
      sent.push([o.method, path, body]);
      if (path.startsWith("/v1/spaces/delete?")) return { results: body.spaces.map((x) => x.project_id === "busy" ? { project_id: x.project_id, error: "revision_conflict" } : { project_id: x.project_id, report: { deleted: { memories: 3 } } }) };
      if (path.endsWith("/rename")) return { records: { memories: 12, wiki_pages: 2 }, skipped: body.execute ? ["project_memberships"] : [], executed: body.execute };
      return path.endsWith("/clone") || path.endsWith("/import") ? { id: "j1", state: "queued" } : {};
    }
    if (path === "/v1/spaces/w/p/config") return { revision, config: { display_name: "기본", aliases } };
    if (path.startsWith("/v1/spaces/template-schema")) return { "x-format": "arkism.space", "x-format-version": 1 };
    if (path.startsWith("/v1/spaces?")) return { items: [{ project_id: "p", config: { display_name: "기본", aliases }, counts: { memories: 12 } }, { project_id: "orbit", config: { display_name: "", aliases: [] }, counts: { memories: 3 }, created: "2026-03-02T09:00:00Z", last_activity: "2026-11-01T10:00:00Z" }, { project_id: "busy", config: { display_name: "작업 중", aliases: [] }, counts: { memories: 1 } }], next: "" };
    throw new Error("not_found");
  };
  const kit = { group: (t, hint) => { const g = h.el("section", "", t); if (hint) g.append(h.el("p", "", hint)); return g; },
    line: (t, hint, ctl) => { const r = h.el("div", "", t); r.append(h.el("small", "", hint), ctl); return r; },
    more: (s, ...n) => { const d = h.el("details", "", s); d.append(...n); return d; } };
  let box = h.el("div");
  const draw = () => { box = h.el("div"); box.append(...ui.groups(kit)); };
  const files = [];
  const ui = S({ state, ...h, request, query: (o = {}) => new URLSearchParams({ workspace_id: "w", project_id: "p", ...o }).toString(), notice() {}, render: draw, go() {},
    pickFile: async () => files.shift() || null, saveFile() {}, spaceJob: async (kind, job) => ({ ...job, state: "done", report: { imported: 2 } }) });
  draw();
  return { state, sent, asks, files, get box() { return box; }, press: async (label) => { const b = dom.buttons(box, label)[0]; assert.ok(b, label); assert.ok(!b.disabled, `${label} enabled`); b.click(); await dom.flush(); await dom.flush(); await dom.flush(); } };
}

test("the list shows every space with its count, the current one as accent text; the name and 대표 별칭 change with a person's writes", async () => {
  const t = setup(); await dom.flush(); await dom.flush();
  const text = t.box.textContent;
  assert.match(text, /orbit · 기억 3개/); assert.match(text, /지금 공간/);
  assert.ok(t.box.querySelectorAll(".sp-here").some((n) => n.textContent === "지금 공간"), "current space marked with accent text, not a bar");
  assert.match(text, /@front대표/, "the first alias is 대표");
  await t.press("이름 바꾸기");
  assert.deepEqual(t.sent[0], ["PATCH", "/v1/spaces/w/p/config", { expected_revision: 2, display_name: "새 이름" }]);
  t.sent.length = 0;
  await t.press("대표로");
  assert.deepEqual(t.sent.map(([m, p, b]) => [m, p, b]), [["DELETE", "/v1/spaces/w/p/aliases/front", null], ["POST", "/v1/spaces/w/p/aliases", { alias: "front" }]], "web becomes first");
});

test("clone sends the target and the picked parts after asking; a template import shows the file first and goes in as_template", async () => {
  const t = setup({ revision: 0 }); await dom.flush(); await dom.flush();
  const input = t.box.querySelectorAll("input").find((n) => n.getAttribute("aria-label") === "새 프로젝트 이름");
  input.value = "orbit-copy"; input.dispatchEvent(new dom.Event("input"));
  await t.press("복제하기");
  assert.match(t.asks[0], /orbit-copy/);
  assert.deepEqual(t.sent.at(-1), ["POST", "/v1/spaces/w/p/clone", { target: { workspace_id: "w", project_id: "orbit-copy" }, parts: ["config", "data"] }]);
  t.files.push([manifest({ template: true, sections: [{ name: "directives", count: 1 }] }), JSON.stringify({ record: { directive: { title: "리뷰는 파일별로" } } })].join("\n"));
  await t.press("템플릿 가져오기");
  assert.match(t.box.textContent, /템플릿 파일이에요/); assert.match(t.box.textContent, /규칙 1/); assert.match(t.box.textContent, /리뷰는 파일별로/);
  await t.press("이 공간에 템플릿으로 넣기");
  assert.deepEqual(t.sent.at(-1).slice(0, 2), ["POST", "/v1/spaces/w/p/import"]);
  assert.equal(t.sent.at(-1)[2].as_template, true); assert.equal(t.sent.at(-1)[2].import_as, "candidate");
});

test("an agent token sees the list and names but no write buttons", async () => {
  const t = setup({ role: "agent" }); await dom.flush(); await dom.flush();
  for (const label of ["기억 가져오기", "템플릿 가져오기", "대표로", "빼기", "지우기", "미리 보기"]) assert.equal(dom.buttons(t.box, label).length, 0, label);
  assert.ok(dom.buttons(t.box, "이름 바꾸기")[0].disabled); assert.ok(dom.buttons(t.box, "복제하기")[0].disabled);
  assert.match(t.box.textContent, /orbit/);
});

test("matchSpace finds by project id, shown name or alias; deleteOutcome splits what went from what did not", () => {
  const s = { project_id: "orbit", config: { display_name: "Orbit 노트", aliases: ["front"] } };
  assert.ok(S.matchSpace(s, "orb") && S.matchSpace(s, "노트") && S.matchSpace(s, "@front") && S.matchSpace(s, "") && S.matchSpace(s, "ORBIT front"));
  assert.ok(!S.matchSpace(s, "web"));
  assert.deepEqual(S.deleteOutcome({ results: [{ project_id: "a", report: {} }, { project_id: "b", error: "revision_conflict" }] }), { done: ["a"], failed: [["b", "revision_conflict"]] });
});

test("the list shows dates and a search box; one space is deleted after typing its id, the space in use never", async () => {
  typed = undefined;
  const t = setup(); await dom.flush(); await dom.flush();
  assert.match(t.box.textContent, /만든 날/); assert.match(t.box.textContent, /마지막 활동/);
  assert.equal(dom.buttons(t.box, "지우기").length, 2, "every space but the one in use");
  const find = t.box.querySelectorAll("input").find((n) => n.getAttribute("aria-label") === "공간 찾기");
  find.value = "orb"; find.dispatchEvent(new dom.Event("input"));
  const visible = t.box.querySelectorAll(".sp-row").filter((r) => !r.hidden).map((r) => r.textContent).join(" ");
  assert.match(visible, /orbit/); assert.doesNotMatch(visible, /작업 중/);
  typed = null;
  await t.press("지우기");
  assert.equal(t.sent.length, 0, "취소 sends nothing");
  typed = undefined;
  await t.press("지우기");
  assert.match(t.asks.at(-1), /orbit/);
  assert.deepEqual(t.sent.at(-1), ["POST", "/v1/spaces/delete?workspace_id=w&project_id=p", { spaces: [{ project_id: "orbit", confirm: "orbit" }] }]);
  assert.match(t.box.textContent, /공간 1개를 지웠어요/);
  assert.doesNotMatch(t.box.textContent, /orbit ·/, "the deleted space leaves the list");
});

test("several picked spaces go in one call after typing how many; a refused one stays with its reason", async () => {
  typed = undefined;
  const t = setup(); await dom.flush(); await dom.flush();
  for (const id of ["orbit", "busy"]) { const c = t.box.querySelectorAll("input").find((n) => n.getAttribute("aria-label") === `${id} 고르기`); c.checked = true; c.dispatchEvent(new dom.Event("change")); }
  await t.press("고른 공간 2개 지우기");
  assert.match(t.asks.at(-1), /공간 수\(2\)/);
  assert.deepEqual(t.sent.at(-1)[2], { spaces: [{ project_id: "orbit", confirm: "orbit" }, { project_id: "busy", confirm: "busy" }] });
  assert.match(t.box.textContent, /공간 1개를 지웠어요/); assert.match(t.box.textContent, /‘busy’: 작업이 돌고 있거나/);
  assert.match(t.box.textContent, /고른 공간 1개 지우기/, "the refused space stays picked");
});

test("the project id changes after a preview and a confirm, through the rename route", async () => {
  typed = undefined;
  const t = setup(); await dom.flush(); await dom.flush();
  const input = t.box.querySelectorAll("input").find((n) => n.getAttribute("aria-label") === "새 프로젝트 ID");
  input.value = "p2"; input.dispatchEvent(new dom.Event("input"));
  await t.press("미리 보기");
  assert.deepEqual(t.sent.at(-1), ["POST", "/v1/spaces/w/p/rename", { project_id: "p2", execute: false }]);
  assert.match(t.box.textContent, /기록 14개가 ‘p2’로 옮겨져요/);
  await t.press("ID 바꾸기");
  assert.match(t.asks.at(-1), /‘p’에서 ‘p2’로/);
  assert.deepEqual(t.sent.at(-1), ["POST", "/v1/spaces/w/p/rename", { project_id: "p2", execute: true }]);
  assert.match(t.box.textContent, /프로젝트 ID를 ‘p2’로 바꿨어요/); assert.match(t.box.textContent, /서버 관리자가 옮겨야/);
});
