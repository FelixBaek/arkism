// 일정을 평소 말로: whenToCron reads the common Korean shapes; anything else is left to the AI (null).
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");

const win = { ArkismCrewPopup: () => ({}) };
vm.runInNewContext(fs.readFileSync(require("node:path").join(__dirname, "crew-automation.js"), "utf8"), { window: win, structuredClone, JSON, Date, Math, String, Array, Object, Set, Map, Number, Promise, document: {}, crypto: {} });
const { whenToCron } = win.ArkismAutomation({ kit: {} });

test("whenToCron reads plain Korean times", () => {
  assert.equal(whenToCron("평일 아침 9시"), "0 9 * * 1-5");
  assert.equal(whenToCron("매주 월·목 오후 3시 반"), "30 15 * * 1,4");
  assert.equal(whenToCron("매일 18:30"), "30 18 * * *");
  assert.equal(whenToCron("주말 저녁"), "0 18 * * 0,6");
  assert.equal(whenToCron("오전 12시"), "0 0 * * *");
  assert.equal(whenToCron("밤 12시"), "0 0 * * *");
  assert.equal(whenToCron("밤 2시"), "0 2 * * *");
  assert.equal(whenToCron("밤 9시"), "0 21 * * *");
  assert.equal(whenToCron("매월 1일 오전 9시"), null);
  assert.equal(whenToCron("월요일 9시"), "0 9 * * 1");
  assert.equal(whenToCron("PR이 열리면"), null);
});
