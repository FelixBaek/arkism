// Switching the screen language (i18n.js in a page): Korean text, titles and placeholders turn English from the
// dictionary (i18n-en.js), user data stays as written, new text is translated as it appears, and Korean comes back.
// Run: node --test desktop/ui/*.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const dom = require("./testdata/dom.js");

const { window, document } = dom.install({ html: `
  <nav><button title="설정" aria-label="닫기">저장</button><input placeholder="검색"></nav>
  <article data-no-i18n><p>저장</p><input title="저장" placeholder="검색" value="설정"></article>
  <code>저장</code><textarea placeholder="검색">저장</textarea><p>그냥 English</p>` });
document.title = "설정";
let want = "ko";
window.ArkismLook = { get: () => ({ lang: want }) };
const confirmed = [];
window.confirm = (m) => { confirmed.push(m); return true; };
require("./i18n.js");
const I = window.ArkismI18n;
const { $, flush } = dom;

test("starts in Korean and leaves the page as written", async () => {
  assert.equal(await I.ready(), true);
  assert.equal(I.lang(), "ko");
  assert.equal(I.locale(), "ko-KR");
  assert.equal(I.translate("저장"), "저장");
  assert.equal($("nav button").textContent, "저장");
  I.registerMessages({ "shell.save": "저장" });
  I.registerMessages({ "shell.save": "저장" }); // the same text again is fine
  assert.throws(() => I.registerMessages({ "shell.save": "닫기" }), /Duplicate message ID: shell.save/);
  assert.throws(() => I.bindMessage(document.createElement("b"), "nope"), /Unknown message ID: nope/);
  const b = I.bindMessage(document.createElement("b"), "shell.save");
  document.body.append(b);
  assert.equal(b.textContent, "저장");
  window.confirm("설정");
  assert.equal(confirmed.pop(), "설정");
});

test("English translates text, attributes, the title and messages, but not user data or code", async () => {
  // a dictionary that fails to load keeps Korean and is tried again next time
  let fail = true, dict;
  Object.defineProperty(window, "ArkismI18nEN", { configurable: true, get() { if (fail && !new Error().stack.includes("i18n-en.js")) { fail = false; throw new Error("broken"); } return dict; }, set(v) { dict = v; } });
  const warn = console.warn; console.warn = () => {};
  try { assert.equal(await I.setLang("en"), false); } finally { console.warn = warn; }
  assert.equal(I.lang(), "ko");
  assert.equal(document.documentElement.lang, "ko");
  want = "en";
  window.dispatchEvent(new dom.Event("arkism-look"));
  assert.notEqual(await I.setLang("en"), false);
  assert.equal(I.lang(), "en");
  assert.equal(I.locale(), "en-US");
  assert.equal(document.documentElement.lang, "en");
  assert.equal(window.ArkismI18nEN.strings["저장"], "Save");
  const btn = $("nav button");
  assert.equal(btn.textContent, "Save");
  assert.equal(btn.getAttribute("title"), "Settings");
  assert.equal(btn.getAttribute("aria-label"), "Close");
  assert.equal($("nav input").getAttribute("placeholder"), "Search");
  assert.equal(document.title, "Settings");
  assert.equal($("[data-i18n-id]").textContent, "Save");
  assert.equal(I.translate("저장"), "Save");
  // user data (data-no-i18n) is left as written
  assert.equal($("article p").textContent, "저장");
  assert.equal($("article input").getAttribute("title"), "저장");
  assert.equal($("article input").getAttribute("placeholder"), "검색");
  assert.equal($("article input").value, "설정");
  // a field drawn later inside user data still gets its labels translated; other user data does not
  const field = document.createElement("input");
  field.setAttribute("placeholder", "검색");
  const note = document.createElement("span");
  note.setAttribute("title", "저장");
  $("article").append(field, note);
  await flush();
  assert.equal(field.getAttribute("placeholder"), "Search");
  assert.equal(note.getAttribute("title"), "저장");
  assert.equal($("code").textContent, "저장");
  assert.equal($("textarea").textContent, "저장");
  assert.equal($("textarea").getAttribute("placeholder"), "Search");
  window.confirm("설정");
  window.confirm(42);
  assert.deepEqual(confirmed.splice(0), ["Settings", 42]);
  // new and changed text is translated as it appears; translated text is not translated twice
  const p = document.createElement("p");
  const span = document.createElement("span");
  span.setAttribute("title", "검색");
  span.textContent = "닫기";
  p.append(span);
  document.body.append(p, document.createTextNode("설정"));
  await flush();
  assert.equal(p.textContent, "Close");
  assert.equal(p.firstChild.getAttribute("title"), "Search");
  assert.equal(document.body.lastChild.data, "Settings");
  p.firstChild.firstChild.textContent = "저장";
  btn.setAttribute("title", "닫기");
  await flush();
  btn.setAttribute("title", "Close"); // already translated
  await flush();
  assert.equal(p.textContent, "Save");
  assert.equal(btn.getAttribute("title"), "Close");
  assert.equal(await I.setLang("en"), true); // already English
});

test("back to Korean restores what was written, keeping text the screen changed since", async () => {
  const kept = document.body.lastChild;
  kept.data = "Changed by the screen";
  want = "ko";
  window.dispatchEvent(new dom.Event("arkism-look"));
  await I.setLang("ko");
  assert.equal(document.documentElement.lang, "ko");
  assert.equal($("nav button").textContent, "저장");
  assert.equal($("nav button").getAttribute("title"), "닫기");
  assert.equal($("nav input").getAttribute("placeholder"), "검색");
  assert.equal(document.title, "설정");
  assert.equal($("[data-i18n-id]").textContent, "저장");
  assert.equal(kept.data, "Changed by the screen");
  const late = document.createElement("p");
  late.textContent = "설정";
  document.body.append(late);
  await flush();
  assert.equal(late.textContent, "설정"); // no longer observed
  // a switch that a newer one overtakes gives up
  const first = I.setLang("en");
  const second = I.setLang("ko");
  assert.equal(await first, false);
  assert.equal(await second, true);
  assert.equal(I.lang(), "ko");
});

test("the extractor prints the Korean strings of a folder as JSON when run directly", () => {
  const fs = require("fs"), os = require("os"), path = require("path");
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "i18n-extract-"));
  try {
    fs.writeFileSync(path.join(dir, "a.js"), 'say("저장했어요"); say(`${n}개 지웠어요`);\n');
    const out = require("child_process").execFileSync(process.execPath, ["--max-old-space-size=256", path.join(__dirname, "i18n-extract.js"), dir], { encoding: "utf8" });
    assert.deepEqual(JSON.parse(out), { statics: ["저장했어요"], templates: ["{0}개 지웠어요"] });
  } finally { fs.rmSync(dir, { recursive: true, force: true }); }
});
