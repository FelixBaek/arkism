// tools/i18n_sync.mjs: merging translations, the English fallback and the file format it writes.
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("fs");
const path = require("path");
const tool = import(path.join(__dirname, "..", "..", "tools", "i18n_sync.mjs"));

const fake = () => ({
  en: { strings: { "저장": "Save", "새 문구": "New text" }, patterns: { "{0}개": "{0} items" } },
  ja: { strings: { "저장": "保存" }, patterns: {}, fallback: [] }
});

test("a usable translation keeps its placeholders and has no Korean", async () => {
  const { problem } = await tool;
  assert.equal(problem("{0}개", "{0}件"), "");
  assert.match(problem("{0}개", "件"), /placeholders/);
  assert.equal(problem("저장", "저장"), "still has Korean");
  assert.equal(problem("저장", " "), "empty");
});

test("without --fallback a missing key stays missing; with it the English is filled in and listed", async () => {
  const { merge } = await tool;
  let { next } = merge(fake());
  assert.deepEqual(next.ja, { strings: { "저장": "保存" }, patterns: {}, fallback: [] });
  ({ next } = merge(fake(), {}, { fallback: true }));
  assert.deepEqual(next.ja.strings, { "저장": "保存", "새 문구": "New text" });
  assert.deepEqual(next.ja.patterns, { "{0}개": "{0} items" });
  assert.deepEqual(next.ja.fallback.sort(), ["{0}개", "새 문구"].sort());
});

test("a real translation replaces the fallback; bad ones are rejected; keys English dropped go too", async () => {
  const { merge } = await tool;
  const d = fake();
  d.ja.strings["새 문구"] = "New text"; d.ja.fallback = ["새 문구"]; d.ja.strings["옛 문구"] = "古い";
  d.en.strings["새 문구"] = "Fresh text"; // English reworded: the fallback follows it
  const { next, rejected } = merge(d, { ja: { patterns: { "{0}개": "件" } } });
  assert.equal(next.ja.strings["새 문구"], "Fresh text");
  assert.ok(!Object.hasOwn(next.ja.strings, "옛 문구"));
  assert.deepEqual(rejected, ["ja {0}개: placeholders 0 expected"]);
  const done = merge(d, { ja: { strings: { "새 문구": "新しい文" } } }).next.ja;
  assert.equal(done.strings["새 문구"], "新しい文");
  assert.deepEqual(done.fallback, []);
});

test("English is never filled from a fallback, only from a translation file", async () => {
  const { merge } = await tool;
  const d = fake(); delete d.en.strings["새 문구"];
  const { addEN, rejected } = merge(d, { en: { strings: { "새 문구": "New text", "다른": "" } } }, { fallback: true });
  assert.deepEqual(addEN.strings, { "새 문구": "New text" });
  assert.deepEqual(rejected, ["en 다른: empty"]);
});

test("writing the current dictionaries changes nothing, and the result loads like the browser loads it", async () => {
  const { load, merge, render } = await tool;
  const dicts = load(["en", "ja"]);
  const { next } = merge(dicts);
  const ja = fs.readFileSync(path.join(__dirname, "i18n-ja.js"), "utf8");
  assert.equal(render("ja", next.ja, "Japanese"), ja);
  const withFallback = render("ja", { ...next.ja, fallback: ["저장"] }, "Japanese");
  const sandbox = { window: {} };
  require("vm").runInNewContext(withFallback, sandbox);
  assert.deepEqual([...sandbox.window.ArkismI18nDicts.ja.fallback], ["저장"]);
});
