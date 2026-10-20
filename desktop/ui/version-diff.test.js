// 버전 기록 › 바뀐 점 보기: only changed fields, long text diffed by line.
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const win = {};
vm.runInNewContext(fs.readFileSync(require("node:path").join(__dirname, "version-diff.js"), "utf8"), { window: win, JSON, Array, Object, Set, Math });
const { blocks } = win.ArkismVersionDiff;

test("one block per changed field, - now and + that version", () => {
  const now = { name: "디자이너", model: "deepseek", instructions: "a\nb\nc", revision: 7 };
  const old = { name: "디자이너", model: "sonnet", instructions: "a\nB\nc", revision: 6 };
  assert.deepEqual(JSON.parse(JSON.stringify(blocks(now, old))), [
    { label: "기본 모델", rows: [["-", "deepseek"], ["+", "sonnet"]] },
    { label: "지시", rows: [["-", "b"], ["+", "B"]] },
  ]);
  assert.equal(blocks(now, { ...now, revision: 1 }).length, 0);
});
