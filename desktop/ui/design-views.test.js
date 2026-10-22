// 디자인 시스템 라이브러리 (modules/designViews.mjs): grouping, cards, detail, the editor dialog and its saves.
const test = require("node:test");
const assert = require("node:assert/strict");
const { createDocument, house, flush } = require("./testdata/views-dom.js");

function setup({ systems = [], requests = {}, picking = false, pick = false, galleryOpen = false } = {}) {
  const doc = createDocument();
  globalThis.document = doc;
  const opened = [];
  globalThis.window = { CSS: { supports: (_, v) => /^#|^rgb|^red$/.test(v) }, open: (u) => opened.push(u) };
  let answer = true;
  globalThis.ask = async () => answer;
  const h = house(doc);
  for (const id of ["controls", "chips", "stats", "table", "ds-products", "ds-purposes", "ds-error", "ds-color-chips", "ds-dialog-title", "ds-meta"]) doc.body.append(Object.assign(h.el("div"), { id }));
  const dialog = Object.assign(h.el("dialog"), { id: "ds-dialog" });
  const form = Object.assign(h.el("form"), { id: "ds-form" });
  for (const id of ["ds-title", "ds-product", "ds-purpose", "ds-description", "ds-source", "ds-fonts"]) form.append(Object.assign(h.el("input"), { id }));
  for (const id of ["ds-colors", "ds-guide"]) form.append(Object.assign(h.el("textarea"), { id }));
  dialog.append(form);
  doc.body.append(dialog);
  const calls = [], notices = [], log = [];
  const state = { designGroup: "product", selected: null, scope: { project_id: "p" }, data: { designSystems: systems } };
  const deps = {
    ...h, state,
    segment: (opts, cur, on) => { const s = h.el("div", "segment"); s.pick = on; s.opts = opts; return s; },
    store: (k, v) => log.push(["store", k, v]),
    render: (x) => log.push(["render", x]),
    gallery: {
      isOpen: () => galleryOpen, libraryButtons: () => [h.el("button", "lib")], renderArchive: (k) => log.push(["archive", k]),
      pick: () => pick, picking: () => picking, markCard: (r, card) => { card.marked = true; return card; },
      cardShortcut: (k, r, card) => card, deleteButton: () => h.el("button", "del"),
    },
    matches: (t) => !state.query || t.toLowerCase().includes(state.query),
    paneSearch: (ph, redraw) => { const s = h.el("label", "pane-search"); s.redraw = redraw; return s; },
    go: (v) => log.push(["go", v]), markSelected: () => log.push(["mark"]), renderDetail: () => log.push(["detail"]), openDetail: () => log.push(["open"]),
    detailShell: (t) => { const b = h.el("div", "detail"); b.title = t; doc.body.append(b); return b; },
    callout: (t, x) => h.el("div", "callout", `${t} ${x}`),
    copyButton: (t) => h.el("button", "copy", t),
    shortDate: (d) => `D(${d})`, notice: (t) => notices.push(t), query: () => "q=1", copyTitle: (t) => `${t} 사본`,
    request: async (path, opt) => {
      calls.push([path, opt && JSON.parse(opt.body)]);
      const r = requests[path];
      if (r instanceof Error) throw r;
      return typeof r === "function" ? r(opt) : r ?? {};
    },
  };
  return { doc, h, deps, state, calls, notices, log, opened, dialog, form, setAnswer: (v) => { answer = v; } };
}

const mod = import("./modules/designViews.mjs");
async function views(o) { const s = setup(o); const { createDesignViews } = await mod; s.v = createDesignViews(s.deps); return s; }

const SYS = (id, ds, extra = {}) => ({ id, revision: 3, updated_at: "2026-10-01", created_by: "felix", design_system: { tags: [], ...ds }, ...extra });

test("empty library offers the first system and a filtered library says nothing matched", async () => {
  const s = await views({ systems: [] });
  s.v.renderLibrary();
  assert.match(s.doc.querySelector("#table").textContent, /아직 디자인 시스템이 없어요/);
  s.doc.querySelector("#table .btn").click();
  assert.equal(s.dialog.open, true);
  s.state.data.designSystems = [SYS("a", { title: "Orbit" })];
  s.state.query = "zzz";
  s.v.renderLibrary();
  assert.match(s.doc.querySelector("#table").textContent, /조건에 맞는 디자인 시스템이 없어요/);
  const seg = s.doc.querySelector(".segment");
  seg.pick("purpose");
  assert.equal(s.state.designGroup, "purpose");
  assert.deepEqual(s.log.find((x) => x[0] === "store"), ["store", "arkism.designGroup", "purpose"]);
});

test("cards group Arkism first, then by name, unnamed last; card clicks open detail or tokens", async () => {
  const s = await views({ systems: [
    SYS("z", { title: "Zeta", product: "Zed", colors: [{ name: "ink", value: "#111" }, { name: "bad", value: "nope" }] }),
    SYS("n", { title: "None!" }),
    SYS("b", { title: "Arkism", product: "Arkism", colors: [{ name: "a", value: "#0a0a0a" }, { name: "b", value: "#ffffff" }, { name: "c", value: "red" }] }, { builtin: true }),
    SYS("a", { title: "Alpha", product: "Alp", purpose: "웹사이트", colors: [{ name: "x", value: "#123456" }] }),
  ] });
  s.state.selected = "ds:a";
  s.v.renderLibrary();
  const heads = s.doc.querySelectorAll(".ds-group h2").map((x) => x.childNodes[0].textContent);
  assert.deepEqual(heads, ["Arkism", "Alp", "Zed", "제품 정하지 않음"]);
  const cards = s.doc.querySelectorAll(".ds-card");
  assert.equal(cards[0].querySelector(".ds-name").textContent, "arkism.");
  assert.equal(cards[0].querySelector(".ds-art").style.background, "#0a0a0a");
  assert.equal(cards[3].querySelector(".ds-name").textContent, "None!");
  assert.ok(cards[1].classList.contains("selected"));
  cards[0].click();
  assert.deepEqual(s.log.at(-1), ["go", "tokens"]);
  cards[1].click();
  assert.equal(s.state.selected, "ds:a");
  assert.deepEqual(s.log.slice(-3), [["mark"], ["detail"], ["open"]]);
  s.state.designGroup = "purpose";
  s.state.data.designSystems = null;
  s.v.renderLibrary();
  assert.match(s.doc.querySelector("#table").textContent, /아직 디자인 시스템이 없어요/);
});

test("server without design systems shows the hint; gallery pick and archive modes take over", async () => {
  const s = await views({ systems: [SYS("a", { title: "A" })], picking: true, pick: true });
  s.state.designGroup = "purpose";
  s.v.renderLibrary();
  const card = s.doc.querySelector(".ds-card");
  assert.equal(card.marked, true);
  assert.equal(s.doc.querySelector(".ds-group h2").childNodes[0].textContent, "목적 정하지 않음");
  const before = s.log.length;
  card.click();
  assert.equal(s.log.length, before);
  const g = await views({ galleryOpen: true });
  g.v.renderLibrary();
  assert.deepEqual(g.log.at(-1), ["archive", "design_systems"]);
  const n = await views({ systems: [SYS("a", { title: "A" })] });
  n.v.librarySummary();
  assert.ok(!n.doc.body.textContent.includes("서버 업데이트가 필요해요"));
  n.state.data.designSystems = null;
  n.v.librarySummary();
  assert.match(n.doc.body.textContent, /서버 업데이트가 필요해요/);
});

test("system detail lists palette, fonts, rules, values and source, with its actions", async () => {
  const sys = SYS("a", { title: "Orbit", product: "Mail", purpose: "앱 화면", description: "설명", guide: "첫 규칙\n\n둘째 규칙", source: "https://x.test/ds",
    fonts: ["Plex \"Sans\"", "Pretendard", "Mono", "Extra"], colors: [{ name: "ink", value: "#000" }, { name: "paper", value: "#fff" }] });
  const s = await views({ systems: [sys], requests: { "/v1/design_systems": { id: "copy1" }, "/v1/design_systems?q=1": { items: [sys] }, "/v1/design_systems/a": {} } });
  s.v.systemDetail(sys);
  const body = s.doc.querySelector(".detail");
  assert.match(body.textContent, /Orbit\./);
  assert.match(body.textContent, /Mail \/ 앱 화면/);
  assert.equal(body.querySelectorAll(".ds-rules li").length, 2);
  assert.equal(body.querySelector(".ds-font-a").style.fontFamily, '"Plex Sans", serif');
  assert.equal(body.querySelectorAll(".ds-font-c").length, 2);
  assert.match(body.querySelector(".ds-names").textContent, /INK · PAPER/);
  const btn = (label) => body.querySelectorAll(".btn").find((b) => b.textContent.includes(label));
  btn("원본에서 열기").click();
  assert.deepEqual(s.opened, ["https://x.test/ds"]);
  btn("디자인 시스템 고치기").click();
  assert.equal(s.dialog.open, true);
  assert.equal(s.doc.querySelector("#ds-title").value, "Orbit");
  assert.equal(s.doc.querySelector("#ds-colors").value, "ink #000\npaper #fff");
  assert.equal(s.doc.querySelector("#ds-meta").hidden, false);
  await btn("복제").click();
  await flush();
  assert.deepEqual(s.calls[0], ["/v1/design_systems", { scope: { project_id: "p" }, system: { ...sys.design_system, title: "Orbit 사본" } }]);
  assert.equal(s.state.selected, "ds:copy1");
  assert.match(s.notices.at(-1), /복제했어요/);
  s.setAnswer(false);
  await btn("보관함에서 빼기").click();
  await flush();
  assert.equal(s.calls.length, 2);
  s.setAnswer(true);
  await btn("보관함에서 빼기").click();
  await flush();
  assert.deepEqual(s.calls[2], ["/v1/design_systems/a", { scope: { project_id: "p" }, expected_revision: 3, archive: true }]);
  assert.equal(s.state.selected, null);
  assert.match(s.notices.at(-1), /보관함에서 뺐어요/);
});

test("a bare system detail and a non-link source show only what exists", async () => {
  const s = await views({ systems: [] });
  s.v.systemDetail(SYS("b", { title: "Bare.", source: "notes" }, { created_by: undefined }));
  const body = s.doc.querySelector(".detail");
  assert.equal(body.querySelector(".ds-sub"), null);
  assert.match(body.textContent, /출처 · notes/);
  assert.ok(!body.textContent.includes("원본에서 열기"));
});

test("duplicate and archive failures are explained", async () => {
  const fail = (m) => Object.assign(new Error(m));
  const sys = SYS("a", { title: "Orbit" });
  for (const [err, re] of [["secret_detected", /비밀값|키나 토큰/], ["invalid_request", /이름과 색/], ["boom", /\(boom\)/]]) {
    const s = await views({ systems: [sys], requests: { "/v1/design_systems": fail(err), "/v1/design_systems/a": fail(err) } });
    s.v.systemDetail(sys);
    const body = s.doc.querySelector(".detail");
    await body.querySelectorAll(".btn").find((b) => b.textContent === "복제").click();
    await flush();
    assert.match(s.notices.at(-1), re);
    await body.querySelectorAll(".btn").find((b) => b.textContent === "보관함에서 빼기").click();
    await flush();
    assert.match(s.notices.at(-1), new RegExp(`처리하지 못했어요: ${err}`));
  }
  // An older server: no list to copy into, nothing is sent.
  const s = await views({ systems: [sys] });
  s.v.systemDetail(sys);
  s.state.data.designSystems = null;
  await s.doc.querySelectorAll(".detail .btn").find((b) => b.textContent === "복제").click();
  assert.equal(s.calls.length, 0);
  const r = await views({ systems: [sys], requests: { "/v1/design_systems": {}, "/v1/design_systems?q=1": {} } });
  r.v.systemDetail(sys);
  await r.doc.querySelectorAll(".detail .btn").find((b) => b.textContent === "복제").click();
  await flush();
  assert.equal(r.state.selected, null);
  assert.deepEqual(r.state.data.designSystems, []);
});

test("editor: new system, color chips, saves and the save errors", async () => {
  const saved = SYS("new1", { title: "New" });
  const s = await views({ systems: [SYS("a", { title: "A", product: "Mail", purpose: "브랜드" })], requests: { "/v1/design_systems": { id: "new1" }, "/v1/design_systems?q=1": { items: [saved] } } });
  s.v.openSystemEditor(null);
  assert.equal(s.doc.querySelector("#ds-dialog-title").textContent, "디자인 시스템 추가");
  assert.equal(s.doc.querySelector("#ds-meta").hidden, true);
  assert.deepEqual(s.doc.querySelector("#ds-products").children.map((o) => o.value), ["Arkism", "Mail"]);
  assert.ok(s.doc.querySelector("#ds-purposes").children.map((o) => o.value).includes("브랜드"));
  const colors = s.doc.querySelector("#ds-colors");
  colors.value = "ink #000\nbrand: #f60\n#abc\nwrong zzz";
  await colors.fire("input");
  const chips = s.doc.querySelectorAll("#ds-color-chips .color-chip");
  assert.deepEqual(chips.map((c) => c.textContent), ["ink", "brand", "color-3", "wrong"]);
  assert.ok(chips[3].classList.contains("bad"));
  s.doc.querySelector("#ds-title").value = " New ";
  s.doc.querySelector("#ds-fonts").value = "A, , B";
  await s.form.fire("submit", { submitter: { value: "cancel" } });
  assert.equal(s.calls.length, 0);
  const e = await s.form.fire("submit", { submitter: { value: "save" }, preventDefault() { this.defaultPrevented = true; } });
  assert.equal(e.defaultPrevented, true);
  assert.equal(s.calls[0][0], "/v1/design_systems");
  assert.deepEqual(s.calls[0][1].system.fonts, ["A", "B"]);
  assert.deepEqual(s.calls[0][1].system.colors[1], { name: "brand", value: "#f60" });
  assert.equal(s.state.selected, "ds:new1");
  assert.equal(s.dialog.open, false);
  assert.equal(s.notices.at(-1), "저장했어요.");

  const fail = (m) => new Error(m);
  for (const [m, re] of [["secret_detected", /키나 토큰/], ["invalid_request", /64개/], ["revision_conflict", /먼저 고쳤어요/], ["x", /\(x\)/]]) {
    const t = await views({ systems: [saved], requests: { "/v1/design_systems/new1": fail(m) } });
    t.v.openSystemEditor(saved);
    assert.equal(t.doc.querySelector("#ds-dialog-title").textContent, "디자인 시스템 고치기");
    await t.form.fire("submit", { submitter: { value: "save" }, preventDefault() {} });
    assert.equal(t.calls[0][1].expected_revision, 3);
    assert.match(t.doc.querySelector("#ds-error").textContent, re);
  }
  const old = await views({ systems: null });
  old.v.openSystemEditor(null);
  assert.match(old.notices[0], /서버를 새 버전으로/);
  const empty = await views({ systems: [saved], requests: { "/v1/design_systems/new1": { id: "new1" }, "/v1/design_systems?q=1": {} } });
  empty.v.openSystemEditor(saved);
  await empty.form.fire("submit", { submitter: { value: "save" }, preventDefault() {} });
  assert.deepEqual(empty.state.data.designSystems, []);
  empty.v.fillList("#ds-products", ["x", "x", "", "y"]);
  assert.equal(empty.doc.querySelector("#ds-products").children.length, 2);
});
