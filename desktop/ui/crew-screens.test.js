// Behavior tests for the workflow list, detail and graph editor (crew.js, crewEditor, crewNodes, crewReview, crewRunView).
const test = require("node:test");
const assert = require("node:assert/strict");
const { boot, dom, simpleGraph } = require("./testdata/crew-harness.cjs");

const skipClose = (n) => n.matches(".crew-popup-close") || /^(취소|닫기)$/.test(n.textContent.trim());
const sweep = (h, root, rounds = 3) => dom.crawl(root, { settle: () => h.settle(), rounds, skip: skipClose });
const clean = () => { const e = dom.errors.splice(0); return e.map((x) => String(x.stack || x).split("\n").slice(0, 2).join(" | ")); };
const at = (n, type, init = {}) => dom.fire(n, type, { clientX: 30, clientY: 30, pointerId: 1, button: 0, ...init });

async function openGraph(id = "wf1", opts) {
  const h = await boot(opts);
  h.state.view = "crew"; h.state.selected = `crew:${id}`; h.render(); await h.settle();
  await h.click(document.querySelector("#detail"), "편집");
  assert.ok(graphOpen());
  return h;
}
// the editor opens in its own window: a dialog popup-drag.js moves out (it stays here without Tauri, as in the preview)
const graphOpen = () => !!document.querySelector("dialog.graph-win[open]");
const nodeWin = (h) => { const d = h.top(); return d?.classList.contains("graph-win") ? null : d; }; // a window over the editor
const node = (id) => document.querySelector(`.g-node[data-id="${id}"]`);

test("list and both workflow details run every control without errors", async () => {
  const h = await boot();
  h.render(); await h.settle();
  assert.ok(document.querySelectorAll(".wf-card, .crew-card, [data-id]").length > 0);
  await sweep(h, document.querySelector("#app"));
  for (const id of ["wf1", "wf2"]) {
    h.closeAll(); h.state.view = "crew"; h.state.selected = `crew:${id}`; h.render(); await h.settle();
    await sweep(h, document.querySelector("#detail"));
    for (const d of h.dialogs()) await sweep(h, d);
  }
  assert.deepEqual(clean(), []);
  assert.ok(h.calls.some((c) => c.method === "POST"));
});

test("graph editor sweep covers every panel without errors", async () => {
  for (const id of ["wf1", "wf2"]) {
    const h = await openGraph(id);
    await sweep(h, document.body, 3);
    assert.deepEqual(clean(), []);
  }
});

test("a port click links two nodes and Escape cancels a pending link", async () => {
  const h = await openGraph();
  const out = node("start").querySelector(".g-port.out");
  at(out, "pointerdown"); at(window, "pointerup"); await h.settle();
  assert.ok(document.querySelector(".g-link-hint"), "link hint shows");
  dom.key(document, "Escape"); await h.settle();
  assert.equal(document.querySelector(".g-link-hint"), null);
  at(node("start").querySelector(".g-port.out"), "pointerdown"); at(window, "pointerup"); await h.settle();
  node("start").click(); // same node: ignored
  const before = document.querySelectorAll(".g-edge-hit").length;
  const target = node("once"); // start links only to ai1, so this edge is new
  target.click(); await h.settle();
  assert.equal(document.querySelectorAll(".g-edge-hit").length, before + 1);
  // drag a port onto another node: the drop target comes from elementFromPoint
  const src = node("ai1").querySelector(".g-port.out");
  at(src, "pointerdown");
  at(window, "pointermove", { clientX: 200, clientY: 200 });
  document._pointTarget = node("end") || target;
  at(window, "pointerup", { clientX: 200, clientY: 200 }); await h.settle();
  // drag onto a start node is refused
  at(node("ai1").querySelector(".g-port.out"), "pointerdown");
  document._pointTarget = node("start");
  at(window, "pointerup", { clientX: 220, clientY: 220 }); await h.settle();
  assert.ok(h.notices.some((n) => /시작 노드/.test(n)));
  // back edge makes a loop notice
  at(node("end")?.querySelector(".g-port.out") || node("code").querySelector(".g-port.out"), "pointerdown");
  document._pointTarget = node("ai1");
  at(window, "pointerup", { clientX: 240, clientY: 240 }); await h.settle();
  document._pointTarget = null;
  assert.deepEqual(clean(), []);
});

test("dragging nodes, panning and wheel zoom move the canvas", async () => {
  const h = await openGraph();
  const box = node("ai1");
  const x0 = box.style.left;
  at(box.querySelector(".g-node-head"), "pointerdown");
  at(window, "pointermove", { clientX: 90, clientY: 80 });
  at(window, "pointermove", { clientX: 120, clientY: 90 });
  await h.settle();
  at(window, "pointerup"); await h.settle();
  assert.notEqual(node("ai1").style.left, x0);
  const stage = document.querySelector(".g-stage");
  const canvas = stage.querySelector(".g-canvas");
  const pan = () => canvas.style.transform.match(/translate\((-?[\d.]+)px, (-?[\d.]+)px\)/).slice(1).map(Number);
  const [px, py] = pan();
  at(stage, "pointerdown", { button: 1 });
  at(window, "pointermove", { clientX: 80, clientY: 70 });
  at(window, "pointerup", { clientX: 90, clientY: 75 });
  assert.deepEqual(pan(), [px + 60, py + 45]);
  // space held turns a left drag into a pan, and the click after it is swallowed
  dom.key(document, " ", { code: "Space" });
  at(stage, "pointerdown", { button: 0 });
  at(stage, "mousedown", { button: 0 });
  at(window, "pointermove", { clientX: 90, clientY: 90 });
  at(window, "pointercancel");
  dom.fire(document, "keyup", { code: "Space" });
  stage.click();
  at(stage, "auxclick", { button: 1 });
  at(stage, "mousedown", { button: 1 });
  dom.fire(stage, "wheel", { deltaY: -100, deltaX: 0, clientX: 50, clientY: 50 });
  dom.fire(stage, "wheel", { deltaY: 30, deltaX: 0, shiftKey: true });
  dom.fire(stage, "wheel", { deltaY: 0, deltaX: 40 });
  assert.match(stage.querySelector(".g-tools output").textContent, /%$/);
  dom.fire(window, "blur");
  assert.deepEqual(clean(), []);
});

test("node picker rows and canvas cards lead with a kind icon or the persona's face", async () => {
  const h = await openGraph();
  for (const b of document.querySelectorAll(".g-node")) assert.ok(b.querySelector(".g-node-head > .g-icon > *"), `${b.dataset.id} has a mark`);
  assert.equal(node("start").querySelector(".g-icon svg").dataset.icon, "play");
  await h.click(document.querySelector(".g-tools"), "노드 추가");
  const rows = [...document.querySelectorAll(".g-menu .g-preset")];
  assert.ok(rows.length > 3);
  for (const r of rows) assert.ok(r.querySelector(".g-icon > *"), `${r.textContent} has a mark`);
  const persona = rows.find((r) => r.textContent.includes("꼼꼼이"));
  assert.ok(persona.querySelector(".g-icon .wf-mark"), "a saved persona shows its face, not a generic icon");
  const before = new Set([...document.querySelectorAll(".g-node")].map((b) => b.dataset.id));
  persona.click(); await h.settle();
  document.querySelector("dialog.node-dialog")?.close(); await h.settle();
  const added = [...document.querySelectorAll(".g-node")].find((b) => !before.has(b.dataset.id));
  assert.ok(added.querySelector(".g-icon .wf-mark"), "the persona's node card shows its face too");
  assert.deepEqual(clean(), []);
});

test("the wheel scrolls the node picker and zooms only on the canvas", async () => {
  const h = await openGraph();
  await h.click(document.querySelector(".g-tools"), "노드 추가");
  const stage = document.querySelector(".g-stage");
  const zoom = () => stage.querySelector(".g-tools output").textContent;
  const z0 = zoom();
  const row = document.querySelector(".g-menu .g-preset");
  const ev = dom.fire(row, "wheel", { deltaY: -200, deltaX: 0, clientX: 50, clientY: 50 });
  assert.equal(zoom(), z0, "a wheel over the picker leaves the zoom alone");
  if (ev && "defaultPrevented" in ev) assert.equal(ev.defaultPrevented, false, "the picker list keeps its native scroll");
  // a middle-drag inside the picker does not pan the canvas either
  const canvas = stage.querySelector(".g-canvas");
  const t0 = canvas.style.transform;
  at(row, "pointerdown", { button: 1 }); at(window, "pointermove", { clientX: 120, clientY: 120 }); at(window, "pointerup", { clientX: 120, clientY: 120 });
  assert.equal(canvas.style.transform, t0);
  dom.fire(stage, "wheel", { deltaY: -200, deltaX: 0, clientX: 50, clientY: 50 });
  assert.notEqual(zoom(), z0, "a wheel on the canvas still zooms");
  assert.deepEqual(clean(), []);
});

test("undo and redo step through canvas edits; Ctrl+Z inside a field stays with the field", async () => {
  const h = await openGraph();
  const tools = () => document.querySelector(".g-tools");
  const undoB = () => tools().querySelector(".g-undo"), redoB = () => tools().querySelector(".g-redo");
  assert.equal(undoB().getAttribute("aria-label"), "되돌리기 (Ctrl+Z)");
  assert.ok(undoB().disabled && redoB().disabled, "nothing to undo on open");
  const n0 = document.querySelectorAll(".g-node").length;
  const saveB = () => document.querySelector(".page-head .g-save");
  // a node window cancelled on a clean workflow leaves it clean
  node("once").click(); await h.settle();
  await h.click(document.querySelector(".g-right"), "자세히");
  await h.click(document.querySelector("dialog.node-dialog"), "취소");
  assert.ok(saveB().disabled, "cancelling marks nothing unsaved");
  node("ai1").click(); await h.settle();
  dom.key(document, "Delete"); await h.settle();
  assert.equal(document.querySelectorAll(".g-node").length, n0 - 1);
  assert.ok(!undoB().disabled && redoB().disabled);
  undoB().click(); await h.settle();
  assert.ok(node("ai1"), "undo brings the node back");
  assert.ok(undoB().disabled && !redoB().disabled);
  assert.ok(saveB().disabled, "back at the saved state there is nothing to save");
  dom.key(document, "Z", { ctrlKey: true, shiftKey: true }); await h.settle();
  assert.equal(node("ai1"), null, "redo removes it again");
  dom.key(document, "z", { metaKey: true }); await h.settle();
  assert.ok(node("ai1"));
  dom.key(document, "y", { ctrlKey: true }); await h.settle();
  assert.equal(node("ai1"), null);
  assert.ok(!saveB().disabled);
  // 두벌식: the Z key types ㅋ, so the physical key decides
  dom.key(document, "ㅋ", { ctrlKey: true, code: "KeyZ" }); await h.settle();
  assert.ok(node("ai1"));
  dom.key(document, "ㅛ", { ctrlKey: true, code: "KeyY" }); await h.settle();
  assert.equal(node("ai1"), null);
  // focus inside a text field: Ctrl+Z is the field's own undo, the graph stays
  const field = document.createElement("input");
  document.body.append(field); field.focus();
  dom.key(field, "z", { ctrlKey: true }); await h.settle();
  assert.equal(node("ai1"), null);
  field.remove();
  // edits in a node window that is cancelled leave no undo step behind
  node("once").click(); await h.settle();
  await h.click(document.querySelector(".g-right"), "자세히");
  const dlg = document.querySelector("dialog.node-dialog");
  const name = dlg.querySelector("input");
  name.value = "취소될 이름"; dom.fire(name, "input"); await h.settle();
  await h.click(dlg, "취소");
  assert.equal(document.querySelector("dialog.node-dialog"), null);
  undoB().click(); await h.settle();
  assert.ok(node("ai1"), "the step before the window is the next undo");
  assert.ok(undoB().disabled);
  assert.doesNotMatch(document.querySelector(".g-canvas").textContent, /취소될 이름/);
  // a save starts the history over: older steps cannot reach back past it
  dom.key(document, "y", { ctrlKey: true }); await h.settle();
  saveB().click(); await h.settle();
  assert.ok(saveB().disabled && undoB().disabled && redoB().disabled);
  assert.deepEqual(clean(), []);
});

test("an edit made while a save is pending stays unsaved", async () => {
  let release;
  const gate = new Promise((r) => { release = r; });
  const h = await openGraph("wf1", { routes: [["POST", /^\/v1\/crew\/wf1$/, async (m, url, body) => { await gate; return { id: "wf1", revision: 9, crew: body.crew }; }]] });
  const saveB = () => document.querySelector(".page-head .g-save");
  node("ai1").click(); await h.settle();
  dom.key(document, "Delete"); await h.settle();
  saveB().click(); await h.settle();
  node("once").click(); await h.settle();
  dom.key(document, "Delete"); await h.settle();
  release(); await h.settle();
  assert.equal(node("once"), null);
  assert.ok(!saveB().disabled, "the edit after the request went out still needs saving");
  dom.key(document, "z", { ctrlKey: true }); await h.settle();
  assert.ok(node("once") && !node("ai1"), "undo steps back to what was saved");
  assert.ok(saveB().disabled, "back at the saved state there is nothing to save");
  assert.deepEqual(clean(), []);
});

test("a save that returns after leaving the editor does not reopen it", async () => {
  let release;
  const gate = new Promise((r) => { release = r; });
  const h = await openGraph("wf1", { routes: [["POST", /^\/v1\/crew\/wf1$/, async (m, url, body) => { await gate; return { id: "wf1", revision: 9, crew: body.crew }; }]] });
  node("ai1").click(); await h.settle();
  dom.key(document, "Delete"); await h.settle();
  document.querySelector(".page-head .g-save").click(); await h.settle();
  h.answer = true;
  await h.click(document.querySelector(".graph-win-head"), /돌아가기/);
  assert.ok(!graphOpen());
  release(); await h.settle();
  assert.ok(!graphOpen(), "the late save leaves the editor closed");
  assert.ok(!h.notices.some((n) => /저장했어요/.test(n)));
  assert.deepEqual(clean(), []);
});

test("inline card edits change cron, model and text values", async () => {
  const h = await boot();
  h.data.crew[0].crew.graph.nodes.push({ id: "sch", type: "schedule", title: "", x: 0, y: 300, config: { cron: "0 9 * * 1-5" } });
  h.state.view = "crew"; h.state.selected = "crew:wf1"; h.render(); await h.settle();
  await h.click(document.querySelector("#detail"), "편집");
  const rowOf = (id, label) => [...node(id).querySelectorAll(".g-node-field.editable")].find((r) => r.querySelector("small").textContent === label);
  // model: a select that saves on change
  rowOf("ai1", "모델").click(); await h.settle();
  const sel = node("ai1").querySelector("select.g-inline");
  // the 설정 › 역할별 모델 picker: 기본·자동 first, then one group per way a provider is connected, saving the plain model name
  assert.deepEqual([...sel.querySelectorAll("optgroup")].map((g) => `${g.label}:${g.dataset.badges}`), ["Anthropic:MAX", "Anthropic:API", "OpenAI:"]);
  assert.deepEqual([...sel.querySelectorAll("option")].slice(1).map((o) => o.value), ["auto", "sonnet", "haiku", "sonnet", "haiku", "gpt-5"]);
  sel.value = sel.options[sel.options.length - 1].value; dom.fire(sel, "change"); await h.settle();
  rowOf("ai1", "모델").click(); await h.settle();
  const sel2 = node("ai1").querySelector("select.g-inline");
  sel2.value = ""; dom.fire(sel2, "change"); await h.settle();
  // goal textarea: Ctrl+Enter saves, Escape cancels, blur saves
  rowOf("ai1", "목표").click(); await h.settle();
  let ta = node("ai1").querySelector("textarea.g-inline");
  ta.value = "새 목표"; dom.key(ta, "Enter", { ctrlKey: true }); await h.settle();
  assert.match(node("ai1").textContent, /새 목표/);
  rowOf("ai1", "목표").click(); await h.settle();
  ta = node("ai1").querySelector("textarea.g-inline"); dom.key(ta, "Escape"); await h.settle();
  rowOf("ai1", "목표").click(); await h.settle();
  ta = node("ai1").querySelector("textarea.g-inline"); ta.value = "흐림 저장"; ta.blur(); await h.settle();
  // cron editor on the schedule node
  const sch = [...document.querySelectorAll(".g-node")].find((b) => rowOf(b.dataset.id, "언제"));
  if (sch) {
    rowOf(sch.dataset.id, "언제").click(); await h.settle();
    const box = sch.isConnected ? sch.querySelector(".g-inline-cron") : node(sch.dataset.id).querySelector(".g-inline-cron");
    const [days] = box.querySelectorAll("select"); const time = box.querySelector("input[type=time]"); const raw = box.querySelector("input.mono");
    days.value = "1-5"; dom.fire(days, "change"); time.value = "07:30"; dom.fire(time, "change");
    assert.equal(raw.value, "30 7 * * 1-5");
    raw.value = "bad"; dom.fire(raw, "input");
    dom.key(box, "Enter");
    raw.value = "0 8 * * *"; dom.fire(raw, "input"); dom.key(box, "Enter"); await h.settle();
    rowOf(sch.dataset.id, "언제").click(); await h.settle();
    const box2 = node(sch.dataset.id).querySelector(".g-inline-cron");
    dom.key(box2, "Escape"); await h.settle();
    rowOf(sch.dataset.id, "언제").click(); await h.settle();
    const box3 = node(sch.dataset.id).querySelector(".g-inline-cron");
    dom.fire(box3, "focusout", { relatedTarget: null }); await h.settle();
  }
  assert.deepEqual(clean(), []);
});

test("add, remove, delete keys, leave and save in the editor", async () => {
  const h = await openGraph();
  await h.click(document.querySelector(".g-tools"), "노드 추가");
  const menu = document.querySelector(".g-menu");
  assert.ok(menu);
  const q = menu.querySelector("input[type=search]");
  dom.type(q, "zzzz-없음"); assert.match(document.querySelector(".g-menu").textContent, /맞는 노드가 없어요/);
  dom.type(document.querySelector(".g-menu input[type=search]"), "");
  for (const t of document.querySelectorAll(".g-menu .tab")) t.click();
  const n0 = document.querySelectorAll(".g-node").length;
  document.querySelector(".g-menu .g-preset").click(); await h.settle();
  assert.equal(document.querySelectorAll(".g-node").length, n0 + 1);
  // the newly added node is selected; Delete removes it
  const sel = document.querySelector(".g-node.selected");
  if (sel) { dom.key(document, "Delete"); await h.settle(); assert.equal(document.querySelectorAll(".g-node").length, n0); }
  // select an edge and Backspace it
  const hit = document.querySelector(".g-edge-hit");
  hit.click(); await h.settle();
  const edges = document.querySelectorAll(".g-edge-hit").length;
  dom.key(document, "Backspace"); await h.settle();
  assert.equal(document.querySelectorAll(".g-edge-hit").length, edges - 1);
  // leave with unsaved changes: refused, then accepted
  h.answer = false;
  await h.click(document.querySelector(".graph-win-head"), /돌아가기/);
  assert.ok(graphOpen());
  // save failure then success
  h.routes.unshift(["POST", /^\/v1\/crew\/wf1$/, () => new Error("저장 실패")], ["POST", /\/v1\/crew$/, () => new Error("저장 실패")]);
  const saveBtn = document.querySelector(".page-head .g-save") || h.btn(document.querySelector(".graph-win-head"), "저장");
  saveBtn.click(); await h.settle();
  h.routes.splice(0, 2);
  (document.querySelector(".page-head .g-save") || h.btn(document.querySelector(".graph-win-head"), "저장")).click(); await h.settle();
  assert.ok(h.notices.some((n) => /저장했어요/.test(n)));
  h.answer = true;
  await h.click(document.querySelector(".graph-win-head"), /돌아가기/);
  assert.ok(!graphOpen());
  assert.deepEqual(clean(), []);
});

test("settings dialog saves, validates and reports a failed save", async () => {
  const h = await boot();
  h.state.selected = "crew:wf1"; h.render(); await h.settle();
  await h.click(document.querySelector("#detail"), "설정");
  let d = h.top();
  assert.ok(d);
  const cron = [...d.querySelectorAll("input")].find((i) => /\*/.test(i.value));
  if (cron) { dom.type(cron, "never"); }
  const ok = h.btn(d, "저장");
  ok.click(); await h.settle();
  if (cron) { assert.match(d.textContent, /형식을 확인/); dom.type(cron, "0 9 * * *"); }
  for (const i of d.querySelectorAll("input[type=number]")) dom.type(i, "9999");
  h.routes.unshift(["PUT", /\/v1\/crew\//, () => new Error("서버 오류")], ["POST", /\/v1\/crew/, () => new Error("서버 오류")]);
  ok.click(); await h.settle();
  assert.ok(d.open);
  h.routes.splice(0, 2);
  ok.click(); await h.settle();
  assert.ok(h.notices.includes("저장했어요."));
  // 복제 opens the same window for a new record that starts as a copy
  h.closeAll(); h.state.view = "crew"; h.state.selected = "crew:wf1"; h.render(); await h.settle();
  await h.click(document.querySelector("#detail"), "복제");
  d = h.top();
  assert.ok(h.btn(d, "만들기"));
  h.btn(d, "만들기").click(); await h.settle();
  assert.ok(h.notices.some((n) => /만들었어요/.test(n)));
  assert.deepEqual(clean(), []);
});

test("an untitled workflow gets its name from the AI after saving", async () => {
  const graph = { nodes: [{ id: "start", type: "start", x: 0, y: 0 }, { id: "a", type: "ai", x: 200, y: 0, config: { goal: "x" } }, { id: "b", type: "http", x: 400, y: 0, config: { url: "https://x" } }, { id: "end", type: "end", x: 600, y: 0 }], edges: [{ from: "start", to: "a" }, { from: "a", to: "b" }, { from: "b", to: "end" }] };
  const h = await boot({ data: {} });
  h.data.crew.push({ id: "wfu", crew: { type: "workflow", name: "제목 없음", handle: "제목없음-1", graph } });
  h.assistReply = (kind) => (kind === "title" ? { status: "done", output: JSON.stringify({ name: "자동 이름", role: "하는 일" }) } : { status: "done", output: "{}" });
  h.state.selected = "crew:wfu"; h.render(); await h.settle();
  await h.click(document.querySelector("#detail"), "편집");
  const head = node("a").querySelector(".g-node-head");
  at(head, "pointerdown"); at(window, "pointermove", { clientX: 60, clientY: 60 }); at(window, "pointerup"); await h.settle();
  document.querySelector(".page-head .g-save").click();
  await h.settle(); await h.settle();
  assert.ok(h.assists.some((a) => a.kind === "title"));
  assert.ok(h.notices.some((n) => /자동 이름/.test(n)));
  assert.deepEqual(clean(), []);
});

test("the run box starts a run and opens it in the editor", async () => {
  const h = await boot();
  h.state.selected = "crew:wf1"; h.render(); await h.settle();
  await h.click(document.querySelector("#detail"), "실행");
  const q = document.querySelector(".try-box input");
  assert.ok(q);
  dom.type(q, "보고서 써 줘");
  dom.key(q, "Enter"); await h.settle();
  assert.ok(h.calls.some((c) => c.method === "POST" && /runs/.test(c.url)));
  assert.ok(graphOpen());
  await sweep(h, document.body, 2);
  assert.deepEqual(clean(), []);
});

test("the simple editor shows a one-AI workflow", async () => {
  const h = await boot();
  h.data.crew.push({ id: "wfs", crew: { type: "workflow", name: "한 단계", handle: "one", graph: simpleGraph() } });
  h.state.selected = "crew:wfs"; h.render(); await h.settle();
  await h.click(document.querySelector("#detail"), "편집");
  await sweep(h, document.body, 3);
  assert.deepEqual(clean(), []);
});

test("every kind of node can be added and edited in its node window", async () => {
  const h = await openGraph();
  await h.click(document.querySelector(".g-tools"), "노드 추가");
  const count = document.querySelectorAll(".g-menu .g-preset").length;
  assert.ok(count > 10);
  for (let i = 0; i < count; i++) {
    if (!document.querySelector(".g-menu")) await h.click(document.querySelector(".g-tools"), "노드 추가");
    const presets = document.querySelectorAll(".g-menu .g-preset");
    presets[i].click(); await h.settle();
    const more = h.btn(document.querySelector(".g-right"), "자세히");
    assert.ok(more, `panel for preset ${i}`);
    more.click(); await h.settle();
    const d = nodeWin(h);
    assert.ok(d, "node window opens");
    await sweep(h, d, 2);
    const keep = nodeWin(h);
    if (keep) { const b = h.btn(keep, i % 2 ? "취소" : "적용"); (b || keep).click?.(); if (keep.open) keep.close(); await h.settle(); }
  }
  // every node of the saved graph too, and the inspector panel for each
  for (const box of [...document.querySelectorAll(".g-node")].map((b) => b.dataset.id)) {
    node(box)?.click(); await h.settle();
    await sweep(h, document.querySelector(".g-right"), 1);
    const more = h.btn(document.querySelector(".g-right"), "자세히");
    if (more) { more.click(); await h.settle(); const d = nodeWin(h); if (d) { await sweep(h, d, 1); if (d.open) d.close(); await h.settle(); } }
  }
  assert.deepEqual(clean(), []);
});

const nodeGraph = () => ({
  nodes: [{ id: "s", type: "start", x: 0, y: 0, config: {} }, { id: "ex", type: "expert", x: 200, y: 0, config: { expert: "ag1", goal: "보기" } },
    { id: "tm", type: "team", x: 400, y: 0, config: { team: "tm2", topic: "주제" } }, { id: "tc", type: "team", x: 400, y: 200, config: { team: "tm1" } },
    { id: "ai", type: "ai", x: 600, y: 0, config: { persona: "ps1", skills: "git-review,gone", collab: "on", workers: JSON.stringify([{ expert: "ag1" }, { name: "조사원", model: "haiku" }]) } },
    { id: "once", type: "ai_once", x: 800, y: 0, config: { prompt: "물어" } }, { id: "end", type: "end", x: 1000, y: 0, config: {} }],
  edges: [{ from: "s", to: "ex" }, { from: "ex", to: "tm" }, { from: "ex", to: "tc" }, { from: "tm", to: "ai" }, { from: "tc", to: "ai" }, { from: "ai", to: "once", port: "more" }, { from: "once", to: "end" }]
});

async function openNodes() {
  const h = await boot();
  h.data.crew.push({ id: "tm2", revision: 1, crew: { type: "team", name: "디자인 팀", handle: "design", mode: "moderator", ending: { max_rounds: 3 }, members: [{ agent_id: "ag1" }, { agent_id: "ag2" }] } },
    { id: "wfn", revision: 1, crew: { type: "workflow", name: "노드 모음", handle: "nodes", model: "sonnet", graph: nodeGraph() } });
  h.state.selected = "crew:wfn"; h.render(); await h.settle();
  await h.click(document.querySelector("#detail"), "편집");
  return h;
}
const nodeWindow = async (h, id) => {
  node(id).click(); await h.settle();
  await h.click(document.querySelector(".g-right"), "자세히");
  return h.top();
};

test("node window: kinds, picks, new experts and teams, workers and skills", async () => {
  const h = await openNodes();
  // expert node: pick by chip, by select, then make a new expert (invalid, taken, ok)
  let d = await nodeWindow(h, "ex");
  dom.byText(d, "button.node-chip", "기획자").click(); await h.settle();
  const change = d.querySelector(".node-pick-change"); change.value = "ag1"; dom.fire(change, "change"); await h.settle();
  dom.byText(d, "button", "+ 새 전문가").click(); await h.settle();
  let m = h.top();
  h.btn(m, "만들기").click(); await h.settle();
  assert.ok(h.notices.some((n) => /채워 주세요/.test(n)));
  const [nm, hd] = m.querySelectorAll("input");
  dom.type(nm, "리뷰어 둘"); dom.type(hd, "reviewer"); dom.type(m.querySelector("textarea"), "너는 리뷰어다.");
  h.btn(m, "만들기").click(); await h.settle();
  assert.ok(h.notices.some((n) => /이미 있어요/.test(n)));
  dom.type(hd, "reviewer2");
  h.routes.unshift(["POST", /^\/v1\/crew$/, () => new Error("save_failed")]);
  h.btn(m, "만들기").click(); await h.settle();
  h.routes.shift();
  h.btn(m, "만들기").click(); await h.settle();
  assert.equal(m.open, false);
  // switch the kind through every option, then apply
  for (const k of ["팀", "AI 에이전트", "AI에게 한 번 묻기", "전문가"]) { dom.byText(h.top(), "button", k).click(); await h.settle(); }
  h.btn(h.top(), "적용").click(); await h.settle();
  // team node: new team
  d = await nodeWindow(h, "tm");
  dom.byText(d, "button", "+ 새 팀").click(); await h.settle();
  m = h.top();
  const [tn] = m.querySelectorAll("input"); dom.type(tn, "새 팀 하나");
  h.btn(m, "만들기").click(); await h.settle();
  assert.equal(m.open, false);
  h.btn(h.top(), "취소").click(); await h.settle();
  // AI node: collaboration workers, persona, skills
  d = await nodeWindow(h, "ai");
  const sel = d.querySelector("select[aria-label='워커 전문가']");
  sel.value = ""; dom.fire(sel, "change"); await h.settle();
  d = h.top();
  for (const i of d.querySelectorAll(".g-worker input")) dom.type(i, "바꿈");
  h.btn(d, "워커 더하기").click(); await h.settle();
  d = h.top(); d.querySelector(".g-worker .icon-btn").click(); await h.settle();
  d = h.top(); const chipX = d.querySelector(".chip .icon-btn"); if (chipX) { chipX.click(); await h.settle(); }
  d = h.top(); const add = [...d.querySelectorAll("input")].find((i) => i.getAttribute("list") === "crew-skills");
  if (add) { add.value = "write-doc"; dom.fire(add, "change"); await h.settle(); }
  d = h.top(); const sw = d.querySelector(".g-collab-head [role=switch], .g-collab-head input[type=checkbox], .g-collab-head button");
  if (sw) { sw.click(); await h.settle(); d = h.top(); const sw2 = d.querySelector(".g-collab-head [role=switch], .g-collab-head input[type=checkbox], .g-collab-head button"); sw2?.click(); await h.settle(); }
  await sweep(h, h.top(), 1);
  if (h.top()) { h.btn(h.top(), "적용")?.click(); await h.settle(); }
  // start node: schedule and webhook triggers
  d = await nodeWindow(h, "s");
  dom.byText(d, "button", "일정").click(); await h.settle();
  d = h.top(); dom.type(d.querySelector("input.mono"), "0 7 * * *");
  dom.byText(d, "button", "웹훅").click(); await h.settle();
  d = h.top(); dom.type([...d.querySelectorAll("input")].find((i) => i.placeholder === "GitHub PR 열림"), "PR 열림");
  h.btn(d, "적용").click(); await h.settle();
  assert.deepEqual(clean(), []);
});

test("node and edge removal ask first; one-node run needs a saved graph", async () => {
  const h = await openNodes();
  // run this node only (saved, not dirty)
  node("once").click(); await h.settle();
  await h.click(document.querySelector(".g-right"), "자세히");
  h.btn(h.top(), "이 노드만 실행").click(); await h.settle();
  assert.ok(h.calls.some((c) => c.method === "POST" && /\/v1\/crew\/runs$/.test(c.url)));
  // node foot run button
  const go = node("once").querySelector(".g-node-run");
  if (go) { dom.fire(go, "pointerdown"); go.click(); await h.settle(); }
  // remove a node: refuse, then accept
  node("once").click(); await h.settle();
  h.btn(document.querySelector(".g-right"), "노드 지우기").click(); await h.settle();
  h.btn(h.top(), "취소").click(); await h.settle();
  assert.ok(node("once"));
  h.btn(document.querySelector(".g-right"), "노드 지우기").click(); await h.settle();
  h.btn(h.top(), "노드 지우기").click(); await h.settle();
  assert.equal(node("once"), null);
  // dirty now: a node window's run asks to save first
  node("ai").click(); await h.settle();
  await h.click(document.querySelector(".g-right"), "자세히");
  h.btn(h.top(), "이 노드만 실행").click(); await h.settle();
  assert.ok(h.notices.some((n) => /저장한 뒤에/.test(n)));
  h.btn(h.top(), "노드 지우기").click(); await h.settle();
  h.btn(h.top(), "노드 지우기").click(); await h.settle();
  // an edge: pick it through its label, change its kind and limit, then delete it
  const chip = document.querySelector(".g-edge-label");
  chip.click(); await h.settle();
  const side = () => document.querySelector(".g-right");
  for (const b of side().querySelectorAll(".g-kind:not([disabled])")) { b.click(); await h.settle(); }
  const fan = [...side().querySelectorAll("select")].find((s) => s.options[0]?.textContent.includes("함께 보낼"));
  if (fan && fan.options[1]) { fan.value = fan.options[1].value; dom.fire(fan, "change"); await h.settle(); }
  document.querySelector(".g-edge-hit").click(); await h.settle();
  h.btn(side(), "선 지우기").click(); await h.settle();
  h.btn(h.top(), "취소").click(); await h.settle();
  h.btn(side(), "선 지우기").click(); await h.settle();
  h.btn(h.top(), "선 지우기").click(); await h.settle();
  // fit the canvas and zoom buttons
  for (const label of ["맞추기", "+", "−"]) h.btn(document.querySelector(".g-tools"), label).click();
  await h.settle();
  assert.deepEqual(clean(), []);
});

test("structure review: AI suggestions apply, go stale and fail cleanly", async () => {
  const h = await openGraph();
  await h.click(document, /^구조 검토$/);
  assert.ok(document.querySelector(".g-right.review"));
  const chat = () => h.chats.filter((c) => c.label === "AI 제안").at(-1);
  const suggestions = [
    { title: "모델 낮추기", body: "싸게", tag: "tokens", action: { type: "set_model", node: "ai1", model: "haiku" } },
    { title: "쓰기로", body: "차례로", tag: "safety", action: { type: "set_access", node: "ai1", access: "write" } },
    { title: "되돌림 줄이기", body: "한 번", tag: "stop", action: { type: "set_edge_max", from: "code", to: "ai1", max: 1 } },
    { title: "확인 넣기", body: "앞에", tag: "order", action: { type: "insert_before", node: "code", new: { type: "code", title: "", config: { lang: "" } } } },
    { title: "넘침", body: "다섯째", tag: "x" }
  ];
  h.assistReply = (kind) => (kind === "review" ? { status: "done", output: JSON.stringify({ suggestions: [...suggestions, { body: "제목 없음" }] }) } : { status: "done", output: "{}" });
  let r = await chat().ask("전체를 검토해 줘");
  assert.equal(r.props.length, 4);
  assert.match(r.say, /제안 4개/);
  await h.settle();
  // the edge limit leaves ai1 alone; the model change on ai1 then makes the access suggestion stale
  assert.notEqual(r.props[2].apply(), false);
  assert.notEqual(r.props[0].apply(), false);
  assert.equal(r.props[1].apply(), false);
  assert.notEqual(r.props[3].apply(), false);
  await h.settle();
  // suggestions without an action, unknown actions and inserts of the wrong kind
  h.assistReply = () => ({ status: "done", output: JSON.stringify([{ title: "직접", body: "" }, { title: "모름", action: { type: "nope" } }, { title: "끼움", action: { type: "insert_before", node: "end", new: { type: "http" } } }, { title: "허용 밖", action: { type: "set_edge_max", from: "code", to: "ai1", max: 30 } }]) });
  r = await chat().ask("다시");
  for (const p of r.props) assert.equal(p.apply(), false);
  chat().onDrop(r.props[0]);
  // nothing found, a failed run and an unreadable answer
  h.assistReply = () => ({ status: "done", output: "{\"suggestions\":[]}" });
  assert.match((await chat().ask("또")).say, /찾지 못했어요/);
  h.assistReply = () => ({ status: "failed" });
  await assert.rejects(chat().ask("실패"));
  h.assistReply = () => ({ status: "done", output: "\"text\"" });
  await assert.rejects(chat().ask("글"), /읽지 못했어요/);
  // an answer that arrives after the server changed is dropped
  let release;
  h.assistReply = () => new Promise((res) => { release = () => res({ status: "done", output: JSON.stringify({ suggestions }) }); });
  const late = chat().ask("늦게");
  await h.settle();
  const server = h.state.server;
  h.state.server = "other";
  release();
  assert.match((await late).say, /찾지 못했어요/);
  h.state.server = server;
  // closing the panel
  document.querySelector('.g-right.review [aria-label="구조 검토 닫기"]').click(); await h.settle();
  assert.equal(document.querySelector(".g-right.review"), null);
  assert.deepEqual(clean(), []);
});

async function openRun(wf, i) {
  const h = await boot();
  h.render(); await h.settle();
  window.dispatchEvent(new CustomEvent("arkism:open-automation", { detail: { id: wf, tab: "history" } }));
  await h.settle();
  const rows = h.top().querySelectorAll(".auto-runline");
  if (!rows[i]) return null;
  rows[i].click(); await h.settle();
  h.btn(h.top(), "단계 보기").click(); await h.settle();
  assert.ok(graphOpen());
  return h;
}

test("each kind of run opens in the run view with its list and side panel", async () => {
  for (const [wf, n] of [["wf1", 2], ["wf2", 2]]) {
    for (let i = 0; i < n; i++) {
      const h = await openRun(wf, i);
      assert.ok(h, `${wf} run ${i}`);
      await sweep(h, document.body, 2);
      const list = h.btn(document.querySelector(".graph-win-head"), "목록");
      if (list && list.isConnected) { list.click(); await h.settle(); await sweep(h, document.querySelector(".graph-win-body"), 2); }
      assert.deepEqual(clean(), []);
    }
  }
});

test("waiting run: approve and reject from the run view", async () => {
  for (const label of ["승인", "거절"]) {
    const h = await openRun("wf1", 0);
    const b = h.btn(document.querySelector(".graph-win-body"), label);
    assert.ok(b, label);
    b.click(); await h.settle();
    assert.ok(h.calls.some((c) => /decision/.test(c.url)));
  }
  assert.deepEqual(clean(), []);
});

test("failed run: retry from the failure and from a done step", async () => {
  const h = await boot();
  h.data.runs.unshift({ id: "runf", revision: 1, created_at: new Date().toISOString(), crew_run: { crew_id: "wf1", status: "failed", input: JSON.stringify({ request: "다시" }), summary: "",
    loops: { "code>ai1:fail": 2 }, nodes: { start: { status: "done" }, ai1: { status: "done", output: "x", runs: 2 }, once: { status: "done" }, cond: { status: "done", port: "no" }, code: { status: "failed", error: "exit 2" }, ok: { status: "skipped" } } } });
  h.render(); await h.settle();
  window.dispatchEvent(new CustomEvent("arkism:open-automation", { detail: { id: "wf1", tab: "history" } }));
  await h.settle();
  h.top().querySelector(".auto-runline").click(); await h.settle();
  h.btn(h.top(), "단계 보기").click(); await h.settle();
  h.btn(document.querySelector(".graph-win-head"), "목록").click(); await h.settle();
  await h.click(document.querySelector(".graph-win-body"), "실패한 곳부터 다시");
  assert.ok(h.calls.some((c) => c.method === "POST" && /\/v1\/crew\/runs$/.test(c.url)));
  await sweep(h, document.body, 2);
  assert.deepEqual(clean(), []);
});

test("a trial run from the editor asks for the task and shows the result badge", async () => {
  const h = await openGraph();
  document.querySelector(".page-head .g-trial").click(); await h.settle();
  let d = h.top();
  h.btn(d, "취소").click(); await h.settle();
  document.querySelector(".page-head .g-trial").click(); await h.settle();
  d = h.top();
  dom.type(d.querySelector("input"), "한 번 돌려 봐");
  h.btn(d, "실행").click(); await h.settle();
  assert.ok(h.calls.some((c) => c.method === "POST" && /\/v1\/crew\/runs$/.test(c.url)));
  h.data.runs.unshift({ id: "run-new", revision: 2, created_at: new Date(Date.now() - 4000).toISOString(), crew_run: { crew_id: "wf1", status: "succeeded",
    nodes: { ai1: { status: "done", output: "{\"output\":\"결과 첫 줄\\n둘\"}", started_at: new Date(Date.now() - 4000).toISOString(), finished_at: new Date().toISOString() }, code: { status: "failed" } } } });
  await h.settle(2000); await h.settle(2000);
  const badge = document.querySelector(".g-test-badge");
  if (badge) { badge.click(); await h.settle(); }
  await sweep(h, document.body, 1);
  assert.deepEqual(clean(), []);
});

test("a step on an agent runner shows the runner, its state and what it could not apply", async () => {
  const h = await boot();
  h.data.runs.unshift({ id: "runb", revision: 1, created_at: new Date().toISOString(), crew_run: { crew_id: "wf1", status: "failed", input: "{}", summary: "",
    nodes: { start: { status: "done" }, ai1: { status: "failed", error: "backend_rejected", backend: { backend: "myrunner", task_id: "t-9", state: "failed", not_applied: ["model"], context: ["instructions", "memory:m1"], usage: "unknown", fallback: false } } } } });
  h.render(); await h.settle();
  window.dispatchEvent(new CustomEvent("arkism:open-automation", { detail: { id: "wf1", tab: "history" } }));
  await h.settle();
  h.top().querySelector(".auto-runline").click(); await h.settle();
  h.btn(h.top(), "단계 보기").click(); await h.settle();
  assert.match(document.querySelector(".wf-run-step.st-failed").title, /외부 실행기 myrunner · 실패/);
  h.btn(document.querySelector(".graph-win-head"), "목록").click(); await h.settle();
  assert.match(document.querySelector(".graph-win-body").textContent, /외부 실행기 myrunner · 실패 · 작업 t-9 · 못 쓴 것: 고른 모델 · 기억 1개 보냄 · 비용 모름/);
  assert.deepEqual(clean(), []);
});

test("graph editing always opens in its own window, from the detail, the workflow window and its history", async () => {
  const h = await openGraph();
  const win = document.querySelector("dialog.graph-win");
  assert.ok(win.open && win.parentNode === document.body, "a body dialog popup-drag.js can move into a new window");
  assert.deepEqual(JSON.parse(win.dataset.popout), { detail: { id: "wf1" } });
  assert.equal(win.dataset.popoutTitle, h.data.crew.find((r) => r.id === "wf1").crew.name);
  assert.equal(h.state.view, "crew", "this window keeps its own screen");
  assert.match(win.querySelector(".graph-win-head").textContent, /워크플로우 편집/);
  assert.ok(win.querySelector(".graph-win-body .graph-page, .graph-win-body .wf-simple"));
  await h.click(win.querySelector(".graph-win-head"), /돌아가기/);
  assert.ok(!graphOpen());

  // the workflow window's 그래프로 편집: the window gives way to the editor's, and 창으로 돌아가기 brings it back
  window.dispatchEvent(new CustomEvent("arkism:open-automation", { detail: { id: "wf1" } })); await h.settle();
  await h.click(h.top(), /그래프로 편집/);
  assert.ok(graphOpen());
  assert.equal(h.dialogs().length, 1, "only the editor's window is open");
  await h.click(document.querySelector(".graph-win-head"), "창으로 돌아가기");
  assert.ok(!graphOpen());
  assert.ok(h.top()?.classList.contains("v4-auto"), "back in the workflow window");
  h.closeAll();

  // a run in its history opens in the same kind of window
  window.dispatchEvent(new CustomEvent("arkism:open-automation", { detail: { id: "wf1", tab: "history" } })); await h.settle();
  h.top().querySelector(".auto-runline").click(); await h.settle();
  h.btn(h.top(), "단계 보기").click(); await h.settle();
  assert.ok(graphOpen());
  assert.match(document.querySelector(".graph-win-head").textContent, /실행/);
  assert.deepEqual(clean(), []);
});

test("where no window can open (the browser preview) the editor covers this one and Esc leaves it like 돌아가기", async () => {
  const h = await openGraph();
  assert.equal(window.__TAURI__, undefined);
  const win = document.querySelector("dialog.graph-win");
  assert.ok(win.open && win.ownerDocument === document, "it stays here, open over the page");
  node("ai1").click(); await h.settle();
  dom.key(document, "Delete"); await h.settle();
  h.answer = false;
  assert.equal(win.dispatchEvent(new Event("cancel", { cancelable: true })), false, "Esc never closes it straight away");
  await h.settle();
  assert.ok(graphOpen(), "unsaved edits: refused, so it stays");
  h.answer = true;
  win.dispatchEvent(new Event("cancel", { cancelable: true })); await h.settle();
  assert.ok(!graphOpen());
  assert.equal(document.querySelector(".graph-win"), null, "nothing is left behind");
  assert.deepEqual(clean(), []);
});

test("a save in the editor's window refreshes the list and cards in this one", async () => {
  const h = await openGraph("wf1", { routes: [["POST", /^\/v1\/crew\/wf1$/, (m, url, body) => {
    const r = h.data.crew.find((x) => x.id === "wf1");
    Object.assign(r, { revision: r.revision + 1, crew: { ...body.crew, name: "고친 워크플로우" } });
    return structuredClone(r);
  }]] });
  assert.doesNotMatch(document.querySelector("#table").textContent, /고친 워크플로우/);
  node("ai1").click(); await h.settle();
  dom.key(document, "Delete"); await h.settle();
  document.querySelector(".graph-win .g-save").click(); await h.settle();
  assert.match(document.querySelector("#table").textContent, /고친 워크플로우/, "the card here shows the saved workflow");
  assert.ok(graphOpen(), "the editor stays open");
  assert.match(document.querySelector(".graph-win-title").textContent, /고친 워크플로우/);
  assert.deepEqual(clean(), []);
});
