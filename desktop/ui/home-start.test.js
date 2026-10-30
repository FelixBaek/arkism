// 홈 › 시작하기 and the memory extractor card (home-start.js): the words for each extractor state and the install record.
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const { extractorIssue, setupSummary, EXTRACTOR_CODES } = require("./home-start.js");

test("each extractor state and failure code turns into a plain sentence with the right fix", () => {
  assert.equal(extractorIssue(null), null, "nothing reported yet");
  assert.equal(extractorIssue({ extractor: { cli: "claude", state: "ready" } }), null);
  const cases = [
    [{ extractor: { cli: "claude", state: "login_needed" } }, /로그인이 필요해요/, "login", /Claude Code에 로그인/],
    [{ extractor: { cli: "codex", state: "missing" } }, /프로그램이 없어요/, "login", /Codex를 찾지 못해/],
    [{ extractor: { cli: "codex", state: "ready" }, analysis_run: { reason: "extractor_exit_1" } }, /답하지 않았어요/, "login", /Codex 로그인이 끝났거나/],
    [{ extractor: { cli: "codex", state: "ready" }, analysis_run: { reason: "extractor_missing" } }, /프로그램이 없어요/, "login", /Codex/],
    [{ analysis_run: { error: "extractor_no_json" } }, /답을 읽지 못했어요/, "model", /다른 모델/],
    [{ analysis_run: { reason: "TimeoutExpired" } }, /너무 오래 걸렸어요/, "retry", /더 빠른 모델/]
  ];
  for (const [tools, title, action, text] of cases) {
    const r = extractorIssue(tools);
    assert.match(r.title, title); assert.equal(r.action, action); assert.match(r.text, text);
    assert.doesNotMatch(r.title + r.text, /extractor_|TimeoutExpired/, "no raw code in the words");
  }
  assert.equal(extractorIssue({ analysis_run: { reason: "session_batch_wait" } }), null, "an ordinary wait is not an extractor problem");
});

test("every extractor failure code has a short sentence and exactly one of the three fixes", () => {
  assert.equal(EXTRACTOR_CODES.length, 5);
  for (const code of EXTRACTOR_CODES) {
    const tools = code === "login_needed" ? { extractor: { cli: "gemini", state: code } } : { extractor: { cli: "gemini", state: "ready" }, analysis_run: { error: code.endsWith("_") ? code + "137" : code } };
    const r = extractorIssue(tools);
    assert.ok(r, code);
    assert.ok(r.title && r.text && r.short, code);
    assert.ok(["login", "model", "retry"].includes(r.action), code);
    assert.doesNotMatch(r.title + r.text + r.short, /extractor_|TimeoutExpired|login_needed/, code);
  }
});

test("the install record names the agents whose files changed and keeps the backup path", () => {
  assert.equal(setupSummary(null), null);
  const ok = setupSummary({ ok: true, at: "t", summary: { installed: true, backup: "C:\\\\Users\\\\a\\\\.arkism\\\\backup\\\\1", files: [
    "C:\\\\Users\\\\a\\\\.claude\\\\settings.json", "C:\\\\Users\\\\a\\\\.codex\\\\config.toml", "/home/a/.gemini/settings.json", "/home/a/.arkism/bin/arkism"] } });
  assert.deepEqual(ok.agents, ["Claude Code", "Codex", "Gemini CLI"]);
  assert.match(ok.backup, /backup/);
  assert.equal(ok.files, 4);
  assert.deepEqual(setupSummary({ ok: false, error: "boom" }), { ok: false, agents: [], backup: "", error: "boom" });
});
