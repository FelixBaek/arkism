// 판 이력 (Figma 998:60693): diffLines names each changed value, not just the field.
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");

const win = { ArkismCrewPopup: () => ({}) };
vm.runInNewContext(fs.readFileSync(require("node:path").join(__dirname, "crew-automation.js"), "utf8"), { window: win, structuredClone, JSON, Date, Math, String, Array, Object, Set, Map, Number, Promise, document: {}, crypto: {} });
const noop = () => {};
const auto = win.ArkismAutomation({ state: {}, el: noop, icon: noop, button: noop, notice: noop, request: noop, query: noop, byId: noop, kit: {}, cronText: (c) => c, nodeTitle: (n) => n.title || n.id, isBack: (e) => e.back });
const wf = (workers, model, minutes, backMax) => ({ name: "리서치", limits: { max_minutes: minutes }, graph: { nodes: [{ id: "r", title: "조사", config: { max_workers: workers } }, { id: "w", title: "정리", config: { model } }], edges: backMax ? [{ from: "w", to: "r", back: true, max: backMax }] : [] } });

test("diffLines gives the changed values", () => {
  assert.deepEqual([...auto.diffLines(wf(3, "sonnet-5.5", 20), wf(5, "opus-5.5", 30, 2))], ["멈추는 조건 20분 → 30분", "조사 워커 3 → 5", "정리 모델 sonnet-5.5 → opus-5.5", "되돌아가는 선 정리 → 조사 최대 2번"]);
  assert.equal(auto.diffLines(wf(3, "a", 20), wf(3, "a", 20)).length, 0);
});
