// Popout windows (popup-drag.js): a popup dragged out of the app moves into its own OS window, a separate document.
// Every capability it needs there (styles, icons, look, keys, lists, menus, notices, translation, clipboard, the
// head as title bar) used to be attached script by script, and one was always missed. All of it now goes through
// popup-drag.js's dress() and window.ArkismPopoutSetup; these tests hold every popout kind and every script to that.
// Run: node --test desktop/ui/*.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("fs");
const path = require("path");
const { boot } = require("./testdata/app-harness.js");

const UI = __dirname;
const sources = [
  ...fs.readdirSync(UI).filter((f) => /\.(js|mjs)$/.test(f) && !f.endsWith(".test.js")),
  ...fs.readdirSync(path.join(UI, "modules")).filter((f) => f.endsWith(".mjs")).map((f) => `modules/${f}`)
].map((f) => ({ f, src: fs.readFileSync(path.join(UI, f), "utf8") }));

// Every kind of popup that can move into its own window, the script that marks it (data-popout), and its head.
const KINDS = [
  { kind: "expert", file: "modules/crewWorkers.mjs", cls: "crew-dialog crew-popup ex-pop", head: "crew-popup-head", via: "crew-popup.js" },
  { kind: "team", file: "team.js", cls: "crew-dialog crew-popup tm-pop", head: "crew-popup-head", via: "crew-popup.js" },
  { kind: "automation", file: "crew-automation.js", cls: "crew-dialog crew-popup v4-auto", head: "crew-popup-head", via: "crew-popup.js" },
  { kind: "memory stage and wide windows", file: "overview-windows.js", cls: "crew-dialog ov-win", head: "ov-win-head" },
  { kind: "workflow graph", file: "crew.js", cls: "graph-win", head: "graph-win-head" }
];
// The per-document wirings a popout window must get (popup-drag.js runs each on it). Dropping one loses a capability.
const SETUPS = ["combo", "contextMenu", "dialogKeys", "graphKeys", "translate", "zoomKeys"];
// Scripts that listen on the main document for input but only for the main window, and why.
const MAIN_ONLY = {
  "pages.js": "the shortcut field in 설정 › 일반 records the next key press on the settings page",
  "shell.js": "the connection card next to the logo lives in the main window",
  "popup-drag.js": "drags and tears off popups inside the main window; dress() sets up the popout side",
  "modules/skillViews.mjs": "the filter menu on the 스킬 page, which never moves into a popout window"
};
// Screens that never open in a popout window may copy with the main window's clipboard.
const MAIN_CLIPBOARD = ["modules/imageViews.mjs", "skill-trial.js"];

test("popout wiring goes only through ArkismPopoutSetup", () => {
  for (const { f, src } of sources) assert.doesNotMatch(src, /["']arkism-popout["']/, `${f}: listen through window.ArkismPopoutSetup, not an event`);
  // a script that listens on `document` for keys, clicks or menus has to wire a popout document the same way
  const listens = /\bdocument\.addEventListener\(\s*["'](keydown|keyup|contextmenu|pointerdown|mousedown|click|input|focusin|paste|copy)["']/;
  const missing = sources.filter(({ f, src }) => listens.test(src) && !/ArkismPopoutSetup\s*\|\|=/.test(src) && !MAIN_ONLY[f]).map(({ f }) => f);
  assert.deepEqual(missing, [], "add a window.ArkismPopoutSetup entry (or a MAIN_ONLY reason here)");
  const registered = sources.flatMap(({ src }) => [...src.matchAll(/ArkismPopoutSetup\s*\|\|=\s*\{\}\)\.(\w+)\s*=/g)].map((m) => m[1]));
  assert.deepEqual(registered.sort(), SETUPS);
});

test("every popout kind is known here, and its head moves the window", () => {
  const marks = sources.filter(({ src }) => /\bpopout:\s|dataset\.popout\s*=[^=]/.test(src)).map(({ f }) => f).sort();
  const known = [...new Set(KINDS.flatMap((k) => [k.file, k.via].filter(Boolean)))].sort();
  assert.deepEqual(marks, known, "a new popout kind: add it to KINDS so the setup tests below cover it");
  const HEAD = /const HEAD = "([^"]+)"/.exec(sources.find((s) => s.f === "popup-drag.js").src)[1].split(",").map((s) => s.trim());
  for (const k of KINDS) {
    assert.ok(HEAD.includes(`.${k.head}`), `${k.kind}: .${k.head} is not a drag head in popup-drag.js`);
    assert.ok(sources.find((s) => s.f === (k.via || k.file)).src.includes(k.head), `${k.kind}: ${k.via || k.file} no longer draws .${k.head}`);
  }
  // only popup-drag.js names the Alt+Shift+O way out, so no kind can forget it (the workflow graph window did, 75811c86)
  const keyshortcut = sources.filter(({ src }) => /aria-keyshortcuts/.test(src) && /setAttribute\(\s*"aria-keyshortcuts"/.test(src)).map(({ f }) => f);
  assert.deepEqual(keyshortcut, ["popup-drag.js"]);
});

test("a popup copies with its own window's clipboard (9461bf34)", () => {
  // the main window has no focus while a popout window is in use, so its clipboard refuses the write
  const bare = sources.filter(({ f, src }) => /(?<![.\w])navigator\.clipboard/.test(src) && !MAIN_CLIPBOARD.includes(f)).map(({ f }) => f);
  assert.deepEqual(bare, [], "copy with el.ownerDocument.defaultView.navigator.clipboard");
});

// The whole app, so every script has added its wiring, with window.open giving a child document like WebView2 does.
let app, opened;
test.before(async () => {
  app = await boot();
  opened = [];
  app.window.open = (url, name) => {
    const d = new app.dom.Document(); d._focus = false;
    d.adoptedStyleSheets = [];
    const w = d.defaultView = { document: d, closed: false, innerWidth: 900, innerHeight: 700, on: {}, addEventListener(t, f) { (this.on[t] ||= []).push(f); }, close() { this.closed = true; },
      CSSStyleSheet: class { constructor(o) { this.media = o?.media; } replaceSync(css) { this.css = css; } } };
    opened.push({ url, name, w });
    return w;
  };
});
test.after(() => app?.stop());

const flush = async () => { await app.dom.flush(); await app.settle(60); };
async function popOut(k, inner = "") {
  const dlg = app.document.createElement("dialog");
  dlg.className = k.cls;
  app.dom.parseHTML(app.document, `<div class="${k.head}"><span>${k.kind}</span><button class="x">닫기</button></div>${inner}`, dlg);
  Object.assign(dlg.dataset, { popout: JSON.stringify({ detail: { id: k.kind } }), popoutTitle: "기억 처리" });
  app.document.body.append(dlg);
  dlg.showModal();
  await flush();
  return { dlg, kd: dlg.ownerDocument, label: opened.at(-1).name };
}

test("every popout kind gets the whole setup in its own window", async () => {
  assert.deepEqual(Object.keys(app.window.ArkismPopoutSetup).sort(), SETUPS, "the booted app registers every wiring");
  for (const k of KINDS) {
    const { dlg, kd, label } = await popOut(k);
    assert.notEqual(kd, app.document, `${k.kind}: did not move into its own window`);
    assert.ok(dlg.classList.contains("popout-root") && dlg.open, k.kind);
    assert.deepEqual(app.window.ArkismPopout.setups(kd).sort(), SETUPS, `${k.kind}: wirings run on its document`);
    assert.equal(dlg.getAttribute("aria-keyshortcuts"), "Alt+Shift+O", `${k.kind}: names the Alt+Shift+O way out`);
    assert.ok(kd.documentElement.classList.contains("popout"));
    assert.equal(kd.documentElement.getAttribute("data-theme"), app.document.documentElement.getAttribute("data-theme"), `${k.kind}: look`);
    assert.ok(kd.querySelector("svg symbol[id^=i-]"), `${k.kind}: icon sprite (c79f4be9)`);
    assert.equal(kd.title, "기억 처리 · Arkism");
    // no OS title bar: the head moves the window (f8c7a1f4)
    app.dom.fire(dlg.querySelector(`.${k.head} span`), "mousedown", { button: 0, detail: 1 });
    assert.deepEqual(app.calls.at(-1), ["plugin:window|start_dragging", { label }], `${k.kind}: head drags its window`);
    // Esc closes the popup there, and its window goes with it
    app.dom.key(dlg.querySelector("button.x"), "Escape");
    await flush();
    assert.equal(dlg.open, false, `${k.kind}: Esc`);
    assert.ok(app.calls.some(([c, a]) => c === "popout_destroy" && a.label === label), `${k.kind}: window closed`);
    dlg.remove();
  }
});

// One check per kind of past popout regression, each inside a real popout document of the booted app.
test("styles: sheets go over as constructed sheets under the CSP, font urls pinned (70bbe70f, 6dd12792, e79826cf)", async () => {
  const added = ["tokens.css", "app.css", "crew.css"].map((href) => { const l = app.document.createElement("link"); l.setAttribute("rel", "stylesheet"); l.href = `http://tauri.localhost/${href}`; app.document.head.append(l); return l; });
  const links = [...app.document.querySelectorAll('link[rel="stylesheet"], style')];
  links.forEach((l, i) => { l.sheet = { cssRules: [{ cssText: i ? `.s${i} { color: red; }` : '@font-face { src: url("fonts/a.woff2"); }' }] }; });
  try {
    const { dlg, kd } = await popOut(KINDS[0]);
    assert.equal(kd.head.querySelector("style, link"), null, "no <style> (blocked by style-src 'self') and no refetched <link>");
    assert.equal(kd.adoptedStyleSheets.length, links.length, "every sheet, in the app's order");
    assert.equal(kd.adoptedStyleSheets[0].css, `@font-face { src: url("${new URL("fonts/a.woff2", links[0].href).href}"); }`, "url() pinned to the sheet's address");
    assert.equal(kd.adoptedStyleSheets[2].css, ".s2 { color: red; }");
    dlg.close(); await flush(); dlg.remove();
  } finally { for (const l of links) delete l.sheet; for (const l of added) l.remove(); }
});

test("keys: Enter saves, zoom keys and look reach the popout (71cacc9c)", async () => {
  const { dlg, kd } = await popOut(KINDS[3], `<form method="dialog"><input id="nm"><button value="cancel">취소</button><button class="primary" value="ok">저장</button></form>`);
  let by = null;
  dlg.querySelector("form").addEventListener("submit", (e) => { by = e.submitter; e.preventDefault(); });
  app.dom.key(kd.querySelector("#nm"), "Enter");
  assert.equal(by?.value, "ok", "Enter means the main button, not ✕ or 취소");
  const zoom = app.window.ArkismLook.get().zoom;
  app.dom.key(kd.querySelector("#nm"), "=", { ctrlKey: true });
  assert.equal(kd.documentElement.style.zoom, String(Math.round((zoom + 0.1) * 100) / 100));
  app.window.ArkismLook.set({ zoom });
  dlg.close(); await flush(); dlg.remove();
});

test("lists, menus and notices open in the popout, not the app (71cacc9c, 438b6ab0)", async () => {
  const { dlg, kd } = await popOut(KINDS[1], `<input id="f"><select id="s"><option>가</option><option>나</option></select>`);
  app.dom.fire(kd.querySelector("#s"), "pointerdown", { button: 0 });
  assert.ok(kd.querySelector(".combo-pop"), "the app's list for a select");
  app.dom.key(kd.querySelector("#s"), "Escape");
  app.dom.fire(kd.querySelector("#f"), "contextmenu", { clientX: 10, clientY: 10 });
  assert.ok(kd.querySelector(".ctx-menu"), "the right-click menu");
  app.dom.key(kd.querySelector(".ctx-menu"), "Escape");
  kd._focus = true;
  app.window.ArkismNotice("알림이에요.");
  assert.ok(dlg.querySelector(".dlg-notice"), "a notice shows in the popup the user is in");
  kd._focus = false;
  dlg.close(); await flush(); dlg.remove();
});

test("translation: a popout opened in English is translated, and turns back (71cacc9c, 1d6cd33e)", async () => {
  await app.window.ArkismI18n.setLang("en");
  try {
    const { dlg, kd } = await popOut(KINDS[2], `<p id="t">저장</p>`);
    assert.equal(kd.querySelector("#t").textContent, "Save");
    assert.equal(kd.documentElement.lang, "en");
    await app.window.ArkismI18n.setLang("ko");
    assert.equal(kd.querySelector("#t").textContent, "저장");
    dlg.close(); await flush(); dlg.remove();
  } finally { await app.window.ArkismI18n.setLang("ko"); }
});
