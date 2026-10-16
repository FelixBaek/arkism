// The injection preview must build the same text as internal/host/capture/capture.go.
// Run: node --test desktop/ui/*.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const core = require("./inject.js");

test("indexLine matches the hook's pointer line", () => {
  assert.equal(core.indexLine({ id: "a", kind: "user", text: "  보고는\n짧게   하기 " }), "- [user] 보고는 짧게 하기");
  const long = "가".repeat(119) + " 나다";
  assert.equal(core.indexLine({ id: "b", kind: "procedural", text: long }), `- [procedural] ${"가".repeat(119)}… (id: b)`);
  assert.equal(core.indexLine({ id: "c", kind: "user", text: "x", conflict_pending: true, supersedes: [{ id: "z" }] }), "- [user, 충돌 검토 중, 이전 기억 대체] x (id: c)");
  assert.equal(core.indexLine({ id: "d", kind: "user", text: "", summary: "요약", superseded_by: {}, supersedes: [] }), "- [user] 요약");
});

test("forRepo keeps shared memories and the chosen repository's", () => {
  const items = [{ id: "s", tags: ["x"] }, { id: "a", tags: ["repo:o/a"] }, { id: "b", tags: ["repo:o/b"] }];
  assert.deepEqual(core.forRepo(items, "o/a").map((i) => i.id), ["s", "a"]);
  assert.deepEqual(core.forRepo(items, "").map((i) => i.id), ["s"]);
});

test("sessionPreview fills the 4000-char budget in standing order and reports what is cut", () => {
  const items = Array.from({ length: 60 }, (_, i) => ({ id: `m${i}`, kind: i % 2 ? "procedural" : "user", text: `${i} ${"규칙".repeat(40)}`, tags: [] }));
  items.push({ id: "empty", kind: "user", text: "", tags: [] }, { id: "other", kind: "user", text: "다른 저장소", tags: ["repo:o/b"] });
  const p = core.sessionPreview(items, "o/a");
  assert.ok(p.text.startsWith(core.INDEX_HEADER + "\n- [user] 0 "));
  assert.ok([...p.text].length <= core.SESSION_MAX_CHARS);
  assert.equal(p.included.length + p.cut.length, 60);
  assert.ok(p.cut.length > 0 && p.included.length > 0);
  // No created_at here: user before procedural, then id.
  const order = items.slice(0, 60).sort((a, b) => (a.kind === "user" ? 0 : 1) - (b.kind === "user" ? 0 : 1) || (a.id < b.id ? -1 : 1));
  assert.deepEqual([...p.included, ...p.cut].map((i) => i.id), order.map((i) => i.id));
  assert.equal(p.outOfRepo, 1);
  assert.equal(p.chars, [...p.text].length);
  // Exactly what within() admits: each line plus a newline, the header and its newline inside the limit.
  const lines = p.text.split("\n").slice(1);
  const used = lines.reduce((a, l) => a + [...l].length + 1, 0);
  const next = core.indexLine(p.cut[0]);
  assert.ok(used + [...next].length + 1 > core.SESSION_MAX_CHARS - [...core.INDEX_HEADER].length - 1);
});

test("contract-related memories go after the index and take their share of the budget", () => {
  const items = [{ id: "a", kind: "user", text: "선호" }, { id: "r", kind: "procedural", text: "계약 규칙" }];
  const p = core.sessionPreview(items, "", [{ id: "r", kind: "procedural", text: "계약 규칙" }]);
  assert.equal(p.text, `${core.INDEX_HEADER}\n- [user] 선호\n\n${core.CONTRACT_HEADER}\n- [procedural] 계약 규칙`);
  assert.deepEqual(p.included.map((i) => i.id), ["a"]);
  assert.equal(core.sessionPreview([], "").text, "");
});

test("promptPreview is the prompt header plus recalled lines for this repository", () => {
  const hits = [{ id: "a", kind: "decision", text: "배포 전 테스트", tags: [] }, { id: "b", kind: "lesson", text: "x", tags: ["repo:o/b"] }, { id: "c", kind: "lesson", text: "" }];
  assert.equal(core.promptPreview(hits, "o/a").text, `${core.PROMPT_HEADER}\n- [decision] 배포 전 테스트`);
  assert.equal(core.promptPreview([], "").text, "");
});

test("effect hides what cannot be computed", () => {
  assert.equal(core.effect(null), null);
  const e = core.effect({ days: 7, sessions: 2, injections: 0, used: 0, items: [] });
  assert.equal(e.rate, null);
  assert.equal(e.avgChars, null);
  const f = core.effect({ days: 30, sessions: 3, injections: 10, used: 3, inject_sessions: 4, inject_chars: 1001, items: [
    { id: "a", injected: 5, used: 2 }, { id: "b", injected: 2, used: 0 }, { id: "c", injected: 3, used: 1 }, { id: "d", injected: 4, used: 0 }] });
  assert.equal(f.rate, 30);
  assert.equal(f.avgChars, 250);
  assert.deepEqual(f.topUsed.map((x) => x.id), ["a", "c"]);
  assert.deepEqual(f.topUnused.map((x) => x.id), ["d", "b"]);
});

test("golden: the same SessionStart text the Go hook builds (internal/host/capture inject_stats_test.go)", () => {
  const g = JSON.parse(require("fs").readFileSync(require("path").join(__dirname, "testdata", "inject-session.json"), "utf8"));
  const p = core.sessionPreview([...g.user, ...g.procedural], g.repo);
  assert.equal(p.text, g.want);
  assert.deepEqual(p.included.map((i) => i.id), g.included);
  assert.ok(p.cut.length > 0, "the fixture must exercise the budget");
});
