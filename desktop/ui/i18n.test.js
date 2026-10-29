// English UI coverage: re-extracts every Korean string in desktop/ui and checks the dictionary in i18n-en.js.
// Run: node --test desktop/ui/*.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("fs");
const path = require("path");
const vm = require("vm");
const { extract, stale } = require("./i18n-extract.js");
const { makeTranslator, HANGUL } = require("./i18n.js");

function loadDict() {
  const sandbox = { window: {} };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, "i18n-en.js"), "utf8"), sandbox);
  return sandbox.window.ArkismI18nEN;
}
const dict = loadDict();
const { translate } = makeTranslator(dict);
const { statics, templates } = extract(__dirname);
const placeholders = (s) => [...s.matchAll(/\{(\d+)\}/g)].map((m) => m[1]).sort().join(",");

test("every static string has its own English entry", () => {
  // an exact entry, not a lucky pattern match: "{0}건" would otherwise turn "조건" into "조 req"
  const missing = [...statics.keys()].filter((s) => !Object.hasOwn(dict.strings, s));
  const pct = (100 * (statics.size - missing.length)) / statics.size;
  console.log(`i18n: ${statics.size} static strings, ${missing.length} untranslated (${pct.toFixed(1)}% covered)`);
  if (missing.length) console.log("  untranslated:\n" + missing.slice(0, 40).map((s) => `   - ${s} (${statics.get(s)[0]})`).join("\n"));
  assert.equal(missing.length, 0, `${missing.length} of ${statics.size} static strings have no English`);
});

test("every template string has a pattern", () => {
  const missing = [...templates.keys()].filter((k) => !Object.hasOwn(dict.patterns, k));
  console.log(`i18n: ${templates.size} template strings, ${missing.length} without a pattern`);
  if (missing.length) console.log("  no pattern:\n" + missing.slice(0, 40).map((s) => `   - ${s} (${templates.get(s)[0]})`).join("\n"));
  assert.equal(missing.length, 0, `${missing.length} of ${templates.size} templates have no pattern`);
});

test("no English entry outlives its Korean", () => {
  // Keys are the Korean sentences themselves: rewording one in the code leaves its old entry behind and the new text
  // untranslated. The test above catches the new text; this one catches the old entry, so the two move together.
  const old = stale(dict, __dirname);
  console.log(`i18n: ${Object.keys(dict.strings).length} strings, ${old.strings.length} stale; ${Object.keys(dict.patterns).length} patterns, ${old.patterns.length} stale`);
  for (const [kind, keys] of Object.entries(old)) if (keys.length) console.log(`  stale ${kind}:\n` + keys.slice(0, 40).map((k) => `   - ${k}`).join("\n"));
  assert.deepEqual(old, { strings: [], patterns: [] }, "remove these from i18n-en.js, or put the English on the reworded Korean");
});

test("each pattern keeps its placeholders and translates a sample", () => {
  const bad = [];
  for (const [k, en] of Object.entries(dict.patterns)) {
    if (placeholders(k) !== placeholders(en)) { bad.push(`placeholders differ: ${k} → ${en}`); continue; }
    const sample = k.replace(/\{(\d+)\}/g, (_, n) => `Q${n}x`);
    const out = translate(sample);
    if (out === sample || (HANGUL.test(out) && !HANGUL.test(en))) bad.push(`no match: ${sample} → ${out}`);
  }
  assert.deepEqual(bad, []);
});

test("translator details", () => {
  // exact, surrounding whitespace kept, collapsed whitespace, captures translated recursively, unknown text untouched
  const t = makeTranslator({ strings: { "저장": "Save", "기억": "Memory" }, patterns: { "{0}개 지웠어요": "Deleted {0}", "{0} 화면": "{0} screen" } }).translate;
  assert.equal(t("저장"), "Save");
  assert.equal(t("  저장\n"), "  Save\n");
  assert.equal(t("3개 지웠어요"), "Deleted 3");
  assert.equal(t("기억 화면"), "Memory screen");
  assert.equal(t("저장 · 기억"), "Save · Memory");
  assert.equal(t("모르는 문장"), "모르는 문장");
  assert.equal(t("plain English"), "plain English");
});

test("translator keeps words whole and leaves half-translated data alone", () => {
  const t = makeTranslator({
    strings: { "조건": "Condition", "지원 안 함": "Not supported", "· 이번 달 한도": "· this month's limit", "빠진 곳 있음": "Missing", "실패": "Failed" },
    patterns: { "{0}건": "{0} req", "{0}개{1}": "{0}{1}", "{0} 빠짐{1}": "{0} missing{1}", "{0} 실행": "Run {0}", "{0} · {1}명": "{0} · {1} people", "협업 워커 {0}명": "{0} workers", "{0}단계": "{0} steps", "{0}요일": "{0}day" }
  }).translate;
  assert.equal(t("3건"), "3 req");
  assert.equal(t("조건"), "Condition"); // exact wins; "조" is not a number
  assert.equal(t("개선"), "개선"); // "{0}개{1}" must not cut a word
  assert.equal(t("code-review 빠짐"), "code-review missing"); // empty capture
  assert.equal(t("git diff · 테스트 실행"), "git diff · 테스트 실행");
  assert.equal(t("$리서치 · 6단계 · 협업 워커 5명"), "$리서치 · 6 steps · 5 workers");
  assert.equal(t("Codex · 지원 안 함"), "Codex · Not supported");
  assert.equal(t("빠진 곳 있음 10"), "Missing 10");
  assert.equal(t("금요일"), "금day"); // a particle-like unit after a one-letter word is allowed
});

test("a one-letter unit stays with the last piece", () => {
  const t = makeTranslator({ strings: { "개인": "Personal", "교훈": "Lesson", "쓰임": "Usage" }, patterns: { "{0}회": "{0} times", "쓰임 {0}회": "Used {0} times", "교훈 · {0}": "Lesson · {0}" } }).translate;
  assert.equal(t("교훈 · 쓰임 3회"), "Lesson · Used 3 times");
  assert.equal(t("개인 · 교훈 · 쓰임 3회"), "Personal · Lesson · Used 3 times"); // not "{0}회" over the whole line
});

test("one API key is singular", () => {
  assert.equal(translate("API 키"), "API key"); // #192: the key dialog labels a single key
});

test("each runner setting that was not applied is translated", () => {
  assert.equal(translate("외부 실행기 r · 실패 · 못 쓴 것: 고른 모델 / 도구 제한 · 비용 모름"), "Agent runner r · Failed · Not applied: chosen model / tool restriction · cost unknown");
});

test("a reworded Korean sentence leaves its old entry stale (8c845bab, e6adecdf)", () => {
  const os = require("os");
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "i18n-stale-"));
  try {
    fs.writeFileSync(path.join(dir, "a.js"), 'say("쓰임 확인 보기"); say("첫 줄\\n둘째 줄"); say(`${n}개를 지웠어요`);\n');
    const old = stale({
      strings: { "쓰임 확인 보기": "View use check", "검증 보기": "View verification", "첫 줄\n둘째 줄": "Line\nline" },
      patterns: { "{0}개를 지웠어요": "Deleted {0}", "{0}개 지웠어요": "Deleted {0}" }
    }, dir);
    assert.deepEqual(old, { strings: ["검증 보기"], patterns: ["{0}개 지웠어요"] });
  } finally { fs.rmSync(dir, { recursive: true, force: true }); }
});
