// Shared widgets without the app: in-app ask boxes, drop-down lists, right-click menu, field hints, popup drag and
// tear-off, window buttons and zoom keys. Run: node --test desktop/ui/*.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const dom = require("./testdata/dom.js");

const winCalls = [];
let maximized = false, resized = null;
const env = dom.install({ html: `
  <div id="window-bar" hidden><button data-win="min"></button><button data-win="max"></button><button data-win="close"></button></div>
  <main id="page"></main>`, stored: { "arkism.look": JSON.stringify({ theme: "light", font: "lg", zoom: 1 }) } });
const { window, document, timers, clipboard, selection, media } = env;
const invokes = [];
window.__TAURI__ = {
  core: { invoke: async (cmd, a) => { invokes.push([cmd, a]); if (window.failInvoke) throw new Error("denied"); return "popout-1"; } },
  window: { getCurrentWindow: () => ({
    isMaximized: async () => maximized,
    onResized: async (fn) => { resized = fn; },
    minimize: () => winCalls.push("min"),
    toggleMaximize: async () => { maximized = !maximized; winCalls.push("max"); },
    close: () => winCalls.push("close")
  }) }
};
const notices = [];
window.ArkismNotice = (t) => notices.push(t);
for (const f of ["look.js", "ask.js", "combo.js", "context-menu.js", "field-need.js", "popup-drag.js", "window-controls.js"]) require("./" + f);
const { $, fire, key, flush } = dom;
const page = $("#page");
const add = (html) => { page.replaceChildren(); dom.parseHTML(document, html, page); return page; };

test("ask resolves true on 확인 and false on 취소, and Enter in the field submits as 확인", async () => {
  let p = window.ask("지울까요?\n되돌릴 수 없어요.", "지우기");
  let d = $("dialog.cn-confirm");
  assert.equal(d.querySelector("h2").textContent, "지울까요?");
  assert.equal(d.querySelector("p").textContent, "되돌릴 수 없어요.");
  assert.equal(document.activeElement.textContent, "취소");
  d.querySelector("button[value=ok]").click();
  assert.equal(await p, true);
  assert.equal($("dialog.cn-confirm"), null);

  p = window.ask("그냥 제목");
  d = $("dialog.cn-confirm");
  assert.equal(d.querySelector("p"), null);
  assert.equal(d.querySelector("button[value=ok]").textContent, "확인");
  d.querySelector("button[value=cancel]").click();
  assert.equal(await p, false);

  p = window.askText("이름을 바꿀까요?", "옛 이름");
  d = $("dialog.cn-confirm");
  const input = d.querySelector("input");
  assert.equal(input.value, "옛 이름");
  input.value = "새 이름";
  d.querySelector("form").requestSubmit(); // Enter: no submitter
  assert.equal(await p, "새 이름");
  p = window.askText("비워 둘까요?", null);
  assert.equal($("dialog.cn-confirm input").value, "");
  $("dialog.cn-confirm button[value=cancel]").click();
  assert.equal(await p, null);
});

test("askTyped opens 확인 only when the exact name is typed", async () => {
  const p = window.askTyped("팀을 지울까요?", "my-team", "지우기");
  const d = $("dialog.cn-confirm"), input = d.querySelector("input"), ok = d.querySelector("button[value=ok]");
  assert.equal(input.placeholder, "my-team");
  assert.equal(ok.disabled, true);
  dom.type(input, "my-tea");
  assert.equal(ok.disabled, true);
  d.querySelector("form").requestSubmit(); // Enter with the wrong text is stopped
  assert.equal(d.open, true);
  dom.type(input, " my-team ");
  assert.equal(ok.disabled, false);
  ok.click();
  assert.equal(await p, true);
  const q = window.askTyped("또?", "x");
  $("dialog.cn-confirm button[value=cancel]").click(); // 취소 always closes
  assert.equal(await q, false);
});

test("a select opens the app's list, picks with the pointer and the keyboard, and Escape closes it", async () => {
  add(`<select id="s"><option value="a">에이</option><option value="b" selected>비</option><option value="h" hidden>숨김</option><option value="c" disabled>씨</option><option value="d">디</option></select>`);
  const s = $("#s");
  const changes = [];
  s.addEventListener("change", () => changes.push(s.value));
  fire(s, "pointerdown", { button: 0 });
  const pop = $(".combo-pop");
  assert.ok(pop._popover);
  assert.deepEqual([...pop.querySelectorAll(".combo-opt")].map((n) => n.textContent), ["에이", "비", "씨", "디"]);
  assert.ok(pop.querySelector(".combo-opt.on.at"));
  assert.equal(s.getAttribute("aria-expanded"), "true");
  fire(s, "pointerdown", { button: 0 }); // second press closes
  assert.equal(pop._popover, false);
  key(s, " ");
  assert.equal(pop._popover, true);
  key(s, "ArrowDown"); // skips the disabled 씨
  assert.equal(pop.querySelector(".at").textContent, "디");
  key(s, "ArrowUp");
  assert.equal(pop.querySelector(".at").textContent, "비");
  key(s, "ArrowUp");
  key(s, "Enter");
  assert.deepEqual(changes, ["a"]);
  key(s, "F4");
  fire(pop.querySelector(".combo-opt.off"), "pointerdown"); // a disabled option does nothing
  assert.equal(pop._popover, true);
  fire(pop.querySelector(".combo-opt.on"), "pointerdown"); // the same value: closes, no change
  assert.deepEqual(changes, ["a"]);
  key(s, "ArrowDown", { altKey: true });
  const esc = key(s, "Escape");
  assert.equal(esc.defaultPrevented, true);
  assert.equal(pop._popover, false);
  key(s, "Enter");
  key(s, "Tab");
  assert.equal(pop._popover, false);
  key(s, "x"); // other keys on a closed select do nothing
  fire(s, "pointerdown", { button: 2 });
  assert.equal(pop._popover, false);
});

test("a list input filters as typed, shows tabs, groups, tags, keys and an empty note", async () => {
  window.ArkismBrand = { mark: (name) => { const m = document.createElement("i"); m.className = "mark-" + name; return m; } };
  add(`<input id="m" list="models" data-combo-empty="없음"><datalist id="models" data-tabs="전체">
    <option value="haiku" label="빠름" data-group="Claude" data-tab="cloud" data-tag="추천" data-key="k1"></option>
    <option value="sonnet" data-group="Claude" data-tab="cloud" data-icon="claude"></option>
    <option value="llama" data-group="Local" data-group-icon="ollama" data-tab="local"></option></datalist>`);
  const m = $("#m"), pop = $(".combo-pop");
  window.innerHeight = 100; // little room below: opens upward
  m._rect = { left: 10, top: 60, width: 200, height: 20 };
  fire(m, "focusin");
  assert.equal(m.dataset.comboList, "models");
  assert.equal(m.hasAttribute("list"), false);
  fire(m, "pointerdown", { button: 0 });
  assert.deepEqual([...pop.querySelectorAll(".combo-tab")].map((b) => b.textContent), ["전체", "cloud", "local"]);
  assert.deepEqual([...pop.querySelectorAll(".combo-head")].map((h) => h.textContent), ["Claude", "Local"]);
  assert.ok(pop.querySelector(".combo-head .mark-ollama") && pop.querySelector(".combo-opt .mark-claude"));
  assert.equal(pop.querySelector(".combo-tag").textContent, "추천");
  assert.equal(pop.querySelector("small").textContent, "빠름");
  fire(pop.querySelectorAll(".combo-tab")[2], "pointerdown");
  assert.deepEqual([...pop.querySelectorAll(".combo-opt")].map((n) => n.textContent), ["llama"]);
  window.innerHeight = 800;
  m.value = "zzz";
  fire(m, "input", { isTrusted: true });
  assert.equal(pop.querySelector(".combo-none").textContent, "없음");
  m.value = "haik";
  fire(pop.querySelectorAll(".combo-tab")[0], "pointerdown");
  fire(m, "input", { isTrusted: true });
  fire(pop.querySelector(".combo-opt"), "pointerdown");
  assert.equal(m.value, "haiku");
  assert.equal(m.dataset.comboKey, "k1");
  assert.equal(m.dataset.comboTyped, "0");
  key(m, "ArrowDown"); // ↓ opens a list input
  assert.equal(pop._popover, true);
  key(m, "ArrowDown"); key(m, "Enter");
  fire(document.body, "pointerdown", { button: 0 }); // a press elsewhere closes it
  window.ArkismCombo.open(m);
  assert.equal(pop._popover, true);
  m.focus(); document.body.focus();
  fire(m, "focusout");
  await timers.tick(1);
  assert.equal(pop._popover, false);
  window.ArkismCombo.open(m);
  fire(page, "scroll");
  assert.equal(pop._popover, false);
  window.ArkismCombo.open(m);
  fire(window, "resize");
  assert.equal(pop._popover, false);
  window.ArkismCombo.open(m);
  fire(page, "close");
  assert.equal(pop._popover, false);
  add(`<input id="n" list="empty"><datalist id="empty"></datalist><input id="plain">`);
  window.ArkismCombo.open($("#n")); // no options and no empty note: stays shut
  window.ArkismCombo.open($("#plain"));
  assert.equal(pop._popover, false);
  // inside an open window the list goes into it
  add(`<dialog id="w"><select id="ws"><option>하나</option></select></dialog>`);
  $("#w").showModal();
  fire($("#ws"), "pointerdown", { button: 0 });
  assert.equal(pop.parentNode, $("#w"));
  fire($("#ws"), "pointerdown", { button: 0 });
  $("#w").close();
  delete window.ArkismBrand;
});

test("the right-click menu offers field, selection, link and own items, and closes on Esc, pointer, blur and scroll", async () => {
  add(`<input id="f" value="hello"><input id="ro" readonly value="x"><div id="ce" contenteditable></div>
    <a id="l" href="https://example.com/x">링크</a><a id="m" href="mailto:a@b">메일</a><p id="t">글</p><div id="own"><span id="in">안</span></div>`);
  const menu = () => $(".ctx-menu");
  const items = () => [...menu().querySelectorAll("button")].map((b) => `${b.textContent}${b.disabled ? "×" : ""}`);
  const f = $("#f");
  f.setSelectionRange(1, 3);
  fire(f, "contextmenu", { clientX: 5, clientY: 5 });
  assert.deepEqual(items(), ["잘라내기", "복사", "붙여넣기", "모두 선택"]);
  menu().querySelectorAll("button")[0].click();
  assert.deepEqual(document.execCommands.at(-1), ["cut"]);
  assert.equal(menu(), null);
  fire(f, "contextmenu", { clientX: 5, clientY: 5 });
  menu().querySelectorAll("button")[1].click();
  fire(f, "contextmenu", { clientX: 5, clientY: 5 });
  menu().querySelectorAll("button")[3].click();
  assert.equal(f.selectionEnd, 5);
  clipboard.text = "붙일 글";
  fire(f, "contextmenu", { clientX: 5, clientY: 5 });
  menu().querySelectorAll("button")[2].click();
  await flush();
  assert.deepEqual(document.execCommands.at(-1), ["insertText", false, "붙일 글"]);
  clipboard.fail = true;
  fire(f, "contextmenu", { clientX: 5, clientY: 5 });
  menu().querySelectorAll("button")[2].click();
  await flush();
  assert.equal(notices.at(-1), "Ctrl+V로 붙여넣어 주세요.");
  clipboard.fail = false;
  fire($("#ro"), "contextmenu", { clientX: 5, clientY: 5 });
  assert.deepEqual(items(), ["잘라내기×", "복사×", "붙여넣기×", "모두 선택"]);
  // contenteditable: the selection is a range
  selection.rangeCount = 1;
  fire($("#ce"), "contextmenu", { clientX: 0, clientY: 0 }); // the menu key: placed by the element
  assert.deepEqual(items(), ["잘라내기×", "복사×", "붙여넣기", "모두 선택"]);
  menu().querySelectorAll("button")[3].click();
  assert.deepEqual(document.execCommands.at(-1), ["selectAll"]);
  fire($("#ce"), "contextmenu", { clientX: 1, clientY: 1 });
  clipboard.text = "x";
  menu().querySelectorAll("button")[2].click();
  await flush();
  selection.rangeCount = 0;
  // selected text
  selection.text = "고른 글";
  fire($("#t"), "contextmenu", { clientX: 1, clientY: 1 });
  assert.deepEqual(items(), ["복사"]);
  menu().querySelector("button").click();
  await flush();
  assert.equal(clipboard.text, "고른 글");
  assert.equal(notices.at(-1), "복사했어요.");
  selection.text = "";
  // links and own items
  fire($("#l"), "contextmenu", { clientX: 1, clientY: 1 });
  assert.deepEqual(items(), ["링크 복사"]);
  clipboard.fail = true;
  menu().querySelector("button").click();
  await flush();
  assert.equal(notices.at(-1), "복사하지 못했어요.");
  clipboard.fail = false;
  fire($("#m"), "contextmenu", { clientX: 1, clientY: 1 });
  assert.equal(menu(), null);
  let ran = 0;
  $("#own").ctxItems = () => [{ label: "하나", run: () => ran++ }, { label: "꺼짐", off: true, run() {} }, { label: "둘", run() {} }];
  fire($("#in").firstChild, "contextmenu", { clientX: 1, clientY: 1 }); // a text node: its parent
  assert.deepEqual(items(), ["하나", "꺼짐×", "둘"]);
  key(document.activeElement, "ArrowDown");
  assert.equal(document.activeElement.textContent, "둘");
  key(document.activeElement, "ArrowUp");
  assert.equal(document.activeElement.textContent, "하나");
  key(document.activeElement, "x");
  document.activeElement.click();
  assert.equal(ran, 1);
  const lone = document.createTextNode("x");
  fire(lone, "contextmenu", {}); // no parent: nothing
  fire($("#t"), "contextmenu", { clientX: 1, clientY: 1 });
  assert.equal(menu(), null);
  for (const close of [
    () => key(document.body, "Escape"),
    () => fire(document.body, "pointerdown"),
    () => fire(window, "blur"),
    () => fire(window, "resize"),
    () => fire(page, "scroll")
  ]) {
    fire($("#in"), "contextmenu", { clientX: 1, clientY: 1 });
    fire(menu().firstChild, "pointerdown"); // inside: stays
    assert.ok(menu());
    close();
    assert.equal(menu(), null);
  }
  key(document.body, "Escape"); // no menu: ignored
  // inside an open dialog the menu joins it
  add(`<dialog id="dd"><input id="di"></dialog>`);
  $("#dd").showModal();
  fire($("#di"), "contextmenu", { clientX: 1, clientY: 1 });
  assert.equal(menu().parentNode, $("#dd"));
  key(document.body, "Escape");
  $("#dd").close();
});

test("ArkismNeed marks a field by element or label, says why, and clears on the next edit", async () => {
  add(`<dialog id="nd"><label class="field"><span>이름</span><input id="nm"></label><div class="field"><b>설명</b><textarea id="ds"></textarea></div>
    <small>없는칸</small><button aria-label="모델" id="mb"></button></dialog><input id="lone">`);
  $("#nd").showModal();
  const nm = $("#nm");
  assert.equal(window.ArkismNeed("이름", "이름을 넣어 주세요."), undefined);
  assert.ok(nm.classList.contains("field-need") && nm.classList.contains("field-shake"));
  assert.equal(nm.nextElementSibling.textContent, "이름을 넣어 주세요.");
  assert.equal(notices.at(-1), "이름을 넣어 주세요.");
  assert.equal(document.activeElement, nm);
  assert.equal(nm._scrolled.behavior, "smooth");
  await timers.tick(400);
  assert.equal(nm.classList.contains("field-shake"), false);
  window.ArkismNeed(nm, "다시"); // again: the old message is replaced
  assert.equal($$count(".field-need-msg"), 1);
  fire(nm, "input");
  assert.equal(nm.classList.contains("field-need"), false);
  assert.equal($$count(".field-need-msg"), 0);
  media["(prefers-reduced-motion: reduce)"] = true;
  window.ArkismNeed("설명");
  assert.equal($("#ds")._scrolled.behavior, "auto");
  assert.ok($("#ds").classList.contains("field-need"));
  window.ArkismNeed("모델");
  assert.ok($("#mb").classList.contains("field-need"));
  fire($("#mb"), "click");
  assert.equal($("#mb").classList.contains("field-need"), false);
  window.ArkismNeed("없는칸");
  window.ArkismNeed("모르는 칸", "안내만");
  assert.equal(notices.at(-1), "안내만");
  media["(prefers-reduced-motion: reduce)"] = false;
  $("#nd").close();
  window.ArkismNeed("이름"); // no open dialog: looks in the page
  window.ArkismNeed(document.createElement("input"), ""); // not on the page: nothing
});
const $$count = (s) => document.querySelectorAll(s).length;

test("a popup drags by its head, stays on screen, tears off outside the app and closes on an outside click", async () => {
  add(`<dialog id="pd" data-popout='{"event":"arkism:open-x"}' data-popout-title="창"><div class="dialog-head"><span id="hd">제목</span><button id="hb">x</button></div></dialog>
       <dialog id="np"><div class="crew-popup-head" id="ch">새 전문가</div></dialog><dialog id="plain"><div class="dialog-head" id="ph">p</div></dialog>`);
  const pd = $("#pd"), hd = $("#hd");
  window.innerWidth = 1280; window.innerHeight = 800;
  pd._rect = { left: 100, top: 100, width: 400, height: 300 };
  pd.showModal();
  fire(hd, "pointerdown", { button: 2 });
  fire($("#hb"), "pointerdown", { button: 0 }); // a button in the head is not a grip
  assert.equal(pd.classList.contains("dragging"), false);
  pd.style.translate = "-80px";
  fire(hd, "pointerdown", { button: 0, clientX: 150, clientY: 110, pointerId: 1 });
  assert.ok(pd.classList.contains("dragging"));
  const head = pd.querySelector(".dialog-head");
  fire(head, "pointermove", { clientX: 170, clientY: 140 });
  assert.equal(pd.style.translate, "-60px 30px");
  $("#window-bar").hidden = false;
  fire(head, "pointermove", { clientX: 150, clientY: -500 }); // never above the window strip
  assert.equal(pd.style.translate, "-80px -68px");
  $("#window-bar").hidden = true;
  fire(head, "pointermove", { clientX: -5, clientY: 110 });
  $("#window-bar").hidden = false;
  assert.ok(pd.classList.contains("tear-off"));
  window.ArkismPopoutSession = () => ({ token: "t" });
  fire(head, "pointerup", { clientX: -5, clientY: 110, screenX: 20, screenY: 30 });
  await flush();
  assert.equal(invokes.at(-1)[0], "popout_open");
  assert.deepEqual(JSON.parse(invokes.at(-1)[1].payload), { recipe: { event: "arkism:open-x" }, session: { token: "t" } });
  assert.equal(invokes.at(-1)[1].title, "창");
  assert.equal(pd.open, false);
  assert.equal(pd.style.translate, ""); // closing re-centers
  // a failing popout_open says so and puts the popup back
  pd.showModal();
  window.failInvoke = true;
  fire(hd, "pointerdown", { button: 0, clientX: 150, clientY: 110 });
  fire(head, "pointerup", { clientX: 2000, clientY: 110 });
  await flush();
  assert.match(notices.at(-1), /새 창으로 열지 못했어요/);
  assert.equal(pd.open, true);
  window.failInvoke = false;
  // a guard that stops cancel keeps it
  const stop = (e) => e.preventDefault();
  pd.addEventListener("cancel", stop);
  fire(hd, "pointerdown", { button: 0, clientX: 150, clientY: 110 });
  fire(head, "pointercancel", { clientX: 2000, clientY: 110 });
  fire(hd, "pointerdown", { button: 0, clientX: 150, clientY: 110 });
  fire(head, "pointerup", { clientX: 2000, clientY: 110 });
  await flush();
  assert.equal(pd.open, true);
  key(document.body, "o", { code: "KeyO", altKey: true, shiftKey: true });
  await flush();
  assert.equal(pd.open, true);
  pd.removeEventListener("cancel", stop);
  // Alt+Shift+O opens the top popup where it sits
  key(document.body, "O", { code: "KeyO", altKey: true, shiftKey: true });
  await flush();
  assert.equal(pd.open, false);
  key(document.body, "o", { code: "KeyO", altKey: true, shiftKey: true }); // nothing open
  key(document.body, "o", { code: "KeyO", altKey: true, shiftKey: true, repeat: true });
  key(document.body, "p", { code: "KeyP", altKey: true, shiftKey: true });
  // an unsaved crew popup and a popup that cannot go
  $("#np").showModal();
  key(document.body, "o", { code: "KeyO", altKey: true, shiftKey: true });
  assert.equal(notices.at(-1), "저장한 뒤에 앱 밖 새 창으로 옮길 수 있어요.");
  fire($("#ch"), "pointerdown", { button: 0, clientX: 10, clientY: 10 });
  fire($("#ch"), "pointerup", { clientX: -1, clientY: 10 });
  assert.equal(notices.at(-1), "저장한 뒤에 앱 밖 새 창으로 옮길 수 있어요.");
  $("#np").close();
  $("#plain").showModal();
  key(document.body, "o", { code: "KeyO", altKey: true, shiftKey: true });
  assert.equal(notices.at(-1), "이 창은 새 창으로 열 수 없어요.");
  $("#plain").classList.add("popout-root");
  key(document.body, "o", { code: "KeyO", altKey: true, shiftKey: true });
  fire($("#ph"), "pointerdown", { button: 0 });
  $("#plain").classList.remove("popout-root");
  $("#plain").close();
  pd.showModal();
  const core = window.__TAURI__.core;
  delete window.__TAURI__.core;
  key(document.body, "o", { code: "KeyO", altKey: true, shiftKey: true });
  assert.equal(notices.at(-1), "새 창은 데스크톱 앱에서 열 수 있어요.");
  window.__TAURI__.core = core;
  // outside click: press and release both on the backdrop
  pd._rect = { left: 100, top: 100, width: 400, height: 300 };
  fire(pd, "pointerdown", { clientX: 50, clientY: 50 });
  fire(pd, "click", { clientX: 50, clientY: 50 });
  assert.equal(pd.open, false);
  pd.showModal();
  fire(pd, "pointerdown", { clientX: 150, clientY: 150 }); // inside
  fire(pd, "click", { clientX: 150, clientY: 150 });
  assert.equal(pd.open, true);
  pd.close();
});

test("window buttons minimize, maximize, close (asking a popout popup first) and move into a top modal", async () => {
  const bar = $("#window-bar");
  assert.equal(bar.hidden, false);
  await flush();
  const max = bar.querySelector('[data-win="max"]');
  assert.equal(max.getAttribute("aria-label"), "최대화");
  bar.querySelector('[data-win="min"]').click();
  max.click();
  await flush();
  assert.equal(max.getAttribute("aria-label"), "이전 크기로");
  assert.ok(max.classList.contains("on"));
  resized();
  bar.querySelector('[data-win="close"]').click();
  assert.deepEqual(winCalls, ["min", "max", "close"]);
  add(`<dialog id="root" class="popout-root"></dialog><dialog id="cover"></dialog><dialog id="small"></dialog>`);
  const root = $("#root");
  root.showModal();
  let stopped = true;
  root.addEventListener("cancel", (e) => { if (stopped) e.preventDefault(); });
  bar.querySelector('[data-win="close"]').click();
  assert.equal(root.open, true);
  stopped = false;
  bar.querySelector('[data-win="close"]').click();
  assert.equal(root.open, false);
  assert.equal(winCalls.length, 3);
  const cover = $("#cover");
  cover._rect = { left: 0, top: 0, width: 1280, height: 800 };
  cover.showModal();
  await flush();
  assert.equal(bar.parentNode, cover);
  cover.close();
  await flush();
  assert.equal(bar.parentNode, document.body);
  $("#small")._rect = { left: 100, top: 100, width: 10, height: 10 };
  $("#small").showModal();
  await flush();
  assert.ok(bar.classList.contains("under-modal"));
  $("#small").close();
  fire(window, "resize");
  assert.equal(bar.classList.contains("under-modal"), false);
});

test("Ctrl +, Ctrl − and Ctrl 0 zoom any screen; other keys pass", () => {
  const look = window.ArkismLook;
  assert.equal(document.documentElement.getAttribute("data-theme"), "light"); // the saved look applies at start
  assert.equal(look.get().font, "lg");
  key(document.body, "=", { ctrlKey: true });
  assert.equal(look.get().zoom, 1.1);
  key(document.body, "+", { metaKey: true });
  key(document.body, "-", { ctrlKey: true });
  assert.equal(look.get().zoom, 1.1);
  const e = key(document.body, "0", { ctrlKey: true });
  assert.equal(e.defaultPrevented, true);
  assert.equal(document.documentElement.style.zoom, "1");
  assert.equal(key(document.body, "x", { ctrlKey: true }).defaultPrevented, false);
  assert.equal(key(document.body, "=", { ctrlKey: true, altKey: true }).defaultPrevented, false);
});

test("the type-only contracts module loads with no runtime exports", async () => {
  assert.deepEqual(Object.keys(await import("./modules/contracts.mjs")), []);
});
