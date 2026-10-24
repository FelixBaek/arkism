// AI로 고치기 제안 (Figma 868:61909 전→후): the diff marks exactly what goes and what comes.
const test = require("node:test");
const assert = require("node:assert/strict");
global.window = {};
require("./text-diff.js");

test("marks removed and added words, keeps the rest", () => {
  assert.deepEqual(window.ArkismTextDiff("막히면 멈추고 알려.", "막히면 멈추고 알려. 배포 직전에는 꼭 물어봐."), [["=", "막히면 멈추고 알려."], ["+", " 배포 직전에는 꼭 물어봐."]]);
  assert.deepEqual(window.ArkismTextDiff("보통으로 해", "깊게 해"), [["-", "보통으로"], ["+", "깊게"], ["=", " 해"]]);
  assert.deepEqual(window.ArkismTextDiff("", "새 소개"), [["+", "새 소개"]]);
  const back = (d, op) => d.filter(([o]) => o !== op).map(([, t]) => t).join("");
  const a = "줄 하나\n둘째 줄을 고쳐요\n셋", b = "줄 하나\n둘째 줄은 그대로\n셋\n넷";
  const d = window.ArkismTextDiff(a, b);
  assert.equal(back(d, "+"), a);
  assert.equal(back(d, "-"), b);
});

test("a line rewritten from scratch shows as old line then new line", () => {
  const d = window.ArkismTextDiff("앞 줄\n선택 상태를 장식으로만 구분하지 않아요.", "앞 줄\n배포 직전에는 꼭 나한테 물어봐.");
  assert.deepEqual(d, [["=", "앞 줄\n"], ["-", "선택 상태를 장식으로만 구분하지 않아요."], ["+", "배포 직전에는 꼭 나한테 물어봐."]]);
});

test("a change too big to compare word by word shows the whole old text then the whole new text", () => {
  const a = "가 ".repeat(3000), b = "나 ".repeat(3000);
  assert.deepEqual(window.ArkismTextDiff(a, b), [["-", a], ["+", b]]);
});
