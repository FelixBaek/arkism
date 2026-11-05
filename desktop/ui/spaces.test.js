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

function setup({ role = "human", revision = 2, aliases = ["front", "web"] } = {}) {
  const state = { view: "settings", settingsTab: "space", server: "s", scope: { workspace_id: "w", project_id: "p" }, caps: { actions: role === "human" ? {} : { review_decide: false } } };
  const sent = [], asks = [];
  globalThis.ask = async (m) => { asks.push(m); return true; };
  globalThis.askText = async () => "새 이름";
  const request = async (path, o = {}) => {
    if (o.method) { sent.push([o.method, path, o.body ? JSON.parse(o.body) : null]); return path.endsWith("/clone") || path.endsWith("/import") ? { id: "j1", state: "queued" } : {}; }
    if (path === "/v1/spaces/w/p/config") return { revision, config: { display_name: "기본", aliases } };
    if (path.startsWith("/v1/spaces/template-schema")) return { "x-format": "arkism.space", "x-format-version": 1 };
    if (path.startsWith("/v1/spaces?")) return { items: [{ project_id: "p", config: { display_name: "기본", aliases }, counts: { memories: 12 } }, { project_id: "orbit", config: { display_name: "", aliases: [] }, counts: { memories: 3 } }], next: "" };
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
  for (const label of ["기억 가져오기", "템플릿 가져오기", "대표로", "빼기"]) assert.equal(dom.buttons(t.box, label).length, 0, label);
  assert.ok(dom.buttons(t.box, "이름 바꾸기")[0].disabled); assert.ok(dom.buttons(t.box, "복제하기")[0].disabled);
  assert.match(t.box.textContent, /orbit/);
});
