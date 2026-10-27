// Run with: node --test desktop/ui/*.test.js
const test = require("node:test");
const assert = require("node:assert/strict");
global.window = global.window || {};
require("./session-list.js");
const S = window.ArkismSessionList;

test("resume command quotes the folder for the shell the folder belongs to", () => {
  assert.equal(S.resumeCommand({ agent: "claude_code", session_id: "abc-1", folder: "/home/me/it's here" }), "cd '/home/me/it'\\''s here'; claude --resume abc-1");
  assert.equal(S.resumeCommand({ agent: "codex", session_id: "x1", folder: "C:\\Users\\me\\Kim's" }), "cd 'C:\\Users\\me\\Kim''s'; codex resume x1");
  assert.equal(S.resumeCommand({ agent: "gemini", session_id: "g1" }), "gemini --resume g1");
  assert.equal(S.resumeCommand({ agent: "grok", session_id: "z" }), "", "no known command");
  assert.equal(S.resumeCommand({ agent: "codex" }), "");
});

test("continue-in prefers the saved work state, then the archive, else nothing", () => {
  const base = { agent: "codex", session_id: "s1", folder: "/w" };
  const work = S.continueCommand("claude_code", { ...base, work: { id: "w-9" }, archive_id: "a1" });
  assert.match(work, /^cd \/w; claude 'Arkism resume_work 도구로 작업 w-9를/);
  assert.ok(work.includes("Codex 세션 s1"));
  assert.match(S.continueCommand("gemini", { ...base, archive_id: "a1" }), /^cd \/w; gemini -i '.*archive_id a1/);
  assert.equal(S.continueCommand("claude_code", base), "");
  assert.equal(S.continueCommand("zcode", { ...base, archive_id: "a1" }), "");
  assert.deepEqual(S.continueTargets("codex", ["codex", "claude_code", "zcode", "claude_code", "opencode"]), ["claude_code", "opencode"]);
});

test("numbers never invent a value", () => {
  assert.equal(S.tokens({ tokens: 0 }), "기록 없음");
  assert.equal(S.cost({ tokens: 0 }), "기록 없음");
  assert.equal(S.cost({ tokens: 10, priced: false }), "가격 모름");
  assert.equal(S.cost({ tokens: 10, priced: true, cost_usd: 1.234 }), "약 $1.23");
  assert.equal(S.split({ tokens: 0 }), "");
  assert.match(S.split({ tokens: 30, input: 10, output: 5, cache_read: 10, cache_write: 5 }), /^입력 10 · 출력 5 · 캐시 15$/);
  assert.equal(S.mainModel({ models: [{ name: "big" }, { name: "small" }] }), "big +1");
  assert.equal(S.mainModel({ models: null }), "");
  assert.equal(S.modelLine({ name: "m", tokens: 0 }), "m · 기록 없음 · 기록 없음");
});

test("a hit is cut at the server's byte offset, falling back to the first match", () => {
  const snip = "…앞말 배포 스크립트 뒷말";
  assert.deepEqual(S.hitParts(snip, Buffer.byteLength("…앞말 "), "배포 스크립트"), ["…앞말 ", "배포 스크립트", " 뒷말"]);
  assert.deepEqual(S.hitParts(snip, 0, "뒷말"), ["…앞말 배포 스크립트 ", "뒷말", ""]);
  assert.deepEqual(S.hitParts("없음", 0, "x"), ["없음", "", ""]);
});

test("a message shows its own time, the day only when it changes, nothing when unrecorded", () => {
  const a = new Date(2026, 9, 13, 9, 5).toISOString(), b = new Date(2026, 9, 13, 23, 59).toISOString(), c = new Date(2026, 9, 14, 0, 1).toISOString();
  assert.equal(S.msgTime(a, undefined), "10월 13일 09:05");
  assert.equal(S.msgTime(b, a), "23:59");
  assert.equal(S.msgTime(c, b), "10월 14일 00:01");
  assert.equal(S.msgTime(c, "bad"), "10월 14일 00:01");
  assert.equal(S.msgTime(undefined, a), "");
  assert.equal(S.msgTime("nope", a), "");
});
