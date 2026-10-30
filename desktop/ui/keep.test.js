// The shared guards (keep.js): latest() drops a reply that a newer request (or a cancel) made stale, view() keeps
// what is being typed across a redraw. Then the past regressions they now cover, each replayed against its screen.
const test = require("node:test");
const assert = require("node:assert/strict");
globalThis.window ??= globalThis;
const keep = require("./keep.js");
const { createDocument, house } = require("./testdata/views-dom.js");

const deferred = () => { let resolve, reject; const p = new Promise((a, b) => { resolve = a; reject = b; }); return { p, resolve, reject }; };

test("latest: a newer begin makes the older turn stale and aborts its signal", () => {
  const loads = keep.latest();
  const a = loads.begin("x");
  assert.equal(a.live(), true);
  const b = loads.begin("x");
  assert.equal(a.live(), false);
  assert.equal(a.signal.aborted, true);
  assert.equal(b.live(), true);
  assert.equal(b.signal.aborted, false);
});

test("latest: keys are independent; cancel(key) drops one, cancel() drops all", () => {
  const loads = keep.latest();
  const a = loads.begin("a"), b = loads.begin("b"), d = loads.begin();
  loads.cancel("a");
  assert.deepEqual([a.live(), b.live(), d.live()], [false, true, true]);
  assert.equal(a.signal.aborted, true);
  loads.cancel();
  assert.deepEqual([b.live(), d.live()], [false, false]);
  loads.cancel("missing"); // nothing out: no throw
});

function field(doc) {
  const root = doc.createElement("div");
  const box = doc.createElement("textarea"); box.setAttribute("aria-label", "검토 메모");
  const det = doc.createElement("details"); const sum = doc.createElement("summary"); sum.textContent = "자세히"; det.append(sum); det.open = true;
  root.append(box, det);
  return { root, box, det };
}

test("view: a rebuild keeps the typed text, caret, focus and open sections (#395)", () => {
  const doc = createDocument(); house(doc);
  const host = doc.createElement("div"); doc.body.append(host);
  let { root, box } = field(doc); host.append(root);
  box.focus(); box.value = "쓰는 중"; box.setSelectionRange(2, 3);
  host.scrollTop = 120;
  const restore = keep.view(host, host);
  // the reply redraws the box from server data: the field comes back empty and closed, the box at the top
  root.remove(); doc.activeElement = doc.body; host.scrollTop = 0;
  ({ root } = field(doc)); root.querySelector("details").open = false; host.append(root);
  restore();
  const next = host.querySelector("textarea");
  assert.equal(doc.activeElement, next);
  assert.equal(next.value, "쓰는 중");
  assert.deepEqual([next.selectionStart, next.selectionEnd], [2, 3]);
  assert.equal(host.querySelector("details").open, true);
  assert.equal(host.scrollTop, 120);
});

test("view: focus that moved to a live control stays there; a pressed button is not put back", () => {
  const doc = createDocument(); house(doc);
  const { root, box } = field(doc); doc.body.append(root);
  const other = doc.createElement("input"); other.setAttribute("name", "else"); doc.body.append(other);
  box.focus();
  const restore = keep.view(root);
  other.focus(); // the person moved on while the redraw ran
  restore();
  assert.equal(doc.activeElement, other);
  const btn = doc.createElement("button"); root.append(btn); btn.focus();
  const again = keep.view(root);
  btn.remove(); doc.activeElement = doc.body;
  again();
  assert.equal(doc.activeElement, doc.body);
});

test("view: a select gets focus back without its value being forced", () => {
  const doc = createDocument(); house(doc);
  const root = doc.createElement("div"); doc.body.append(root);
  const make = (v) => { const s = doc.createElement("select"); s.setAttribute("name", "role"); s.value = v; root.replaceChildren(s); return s; };
  make("a").focus();
  const restore = keep.view(root);
  doc.activeElement = doc.body;
  const fresh = make("b"); // the server's newer pick
  restore();
  assert.equal(doc.activeElement, fresh);
  assert.equal(fresh.value, "b");
});

// ---- past regressions

function models() {
  const state = { scope: {}, data: {} };
  const out = [];
  const request = (url, opt) => { const d = deferred(); out.push({ url, opt, d }); return d.p; };
  require("./models.js");
  const m = window.ArkismModels({ state, request, query: () => "", el: () => ({}), icon: () => ({}), button: () => ({}), render() {}, notice() {}, agentLogo() {}, ago() {} });
  return { m, state, out };
}

test("models refresh: an older providers read landing after a newer one does not undo it (05ccd9cb, d674bb65, ce606af5, 16f7c60b, 078df4fd)", async () => {
  const { m, state, out } = models();
  const first = m.load(), second = m.load();
  assert.equal(out[0].opt.signal.aborted, true, "the older read is aborted where the transport can");
  out[1].d.resolve({ providers: { providers: [{ id: "new" }] } }); await second;
  out[0].d.resolve({ providers: { providers: [{ id: "old" }] } }); await first;
  assert.equal(state.data.providers.providers[0].id, "new");
});

test("role/model pick: a providers read that started before the pick cannot snap it back (6e7bb042)", async () => {
  const { m, state, out } = models();
  const read = m.load(); // opening the tab
  m.publish({ providers: [{ id: "picked" }] }); // the POST's answer
  out[0].d.resolve({ providers: { providers: [{ id: "before" }] } }); await read;
  assert.equal(state.data.providers.providers[0].id, "picked");
});

async function editor() {
  globalThis.ask = async () => true;
  const { createCrewEditor } = await import("./modules/crewEditor.mjs");
  const saves = [], reloads = [], notices = [];
  const deps = {
    stopPolling() {}, defaultGraph: () => ({ nodes: [], edges: [] }), request: async () => ({ items: [] }), query: () => "",
    render() {}, pollRun() {}, closeGraph() {}, ownerOf: () => null, getAuto: () => ({ open() {} }), byId: () => null,
    parseCron: () => true, notice: (m) => notices.push(m), autoTitle() {}, errorText: String, graphBody: () => null,
    save: () => { const d = deferred(); saves.push(d); return d.p; },
    reload: () => { const d = deferred(); reloads.push(d); return d.p; }
  };
  const ed = createCrewEditor(deps);
  const rec = { id: "w1", crew: { graph: { nodes: [], edges: [] } } };
  return { ed, rec, saves, reloads, notices };
}
const tick = () => new Promise((r) => setImmediate(r));

test("crew editor: a save that returns after the editor was left reports false (fd7f5e45, adfea930)", async () => {
  const { ed, rec, saves, reloads } = await editor();
  ed.openGraph(rec);
  const saving = ed.saveGraph();
  await ed.leaveGraph();
  saves[0].resolve({ id: "w1" }); await tick();
  reloads[0].resolve();
  assert.equal(await saving, false);
});

test("crew editor: reopening while the post-save reload is out drops the old save (5c9ddc56)", async () => {
  const { ed, rec, saves, reloads, notices } = await editor();
  ed.openGraph(rec);
  const saving = ed.saveGraph();
  saves[0].resolve({ id: "w1" }); await tick();
  ed.openGraph(rec); // reopened (another workflow, or the same one again) before the list came back
  reloads[0].resolve();
  assert.equal(await saving, false);
  assert.deepEqual(notices, []);
});
