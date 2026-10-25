// Behavior tests for the automation window (crew-automation.js), the shared popup (crew-popup.js) and the cover picker (team-cover.js).
const test = require("node:test");
const assert = require("node:assert/strict");
const { boot, dom } = require("./testdata/crew-harness.cjs");

const skipClose = (n) => n.matches(".crew-popup-close") || /^(취소|닫기)$/.test(n.textContent.trim());
const sweep = (h, root, rounds = 3) => dom.crawl(root, { settle: () => h.settle(), rounds, skip: skipClose });
const clean = () => dom.errors.splice(0).map((x) => String(x.stack || x).split("\n").slice(0, 2).join(" | "));

for (const tab of ["overview", "test", "history"]) {
  for (const id of ["wf1", "wf2"]) {
    test(`automation window ${id} · ${tab}`, async () => {
      const h = await boot();
      h.render(); await h.settle();
      window.dispatchEvent(new CustomEvent("arkism:open-automation", { detail: { id, tab } }));
      await h.settle();
      const d = h.top();
      assert.ok(d, "window opens");
      await sweep(h, d);
      assert.deepEqual(clean(), []);
    });
  }
}

test("new automation window", async () => {
  const h = await boot();
  h.render(); await h.settle();
  h.btn(document, /새 워크플로우/).click(); await h.settle();
  const d = h.top();
  assert.match(d.className, /v4-auto-new/);
  await sweep(h, d);
  assert.deepEqual(clean(), []);
});

test("cover picker: search, more, pick, clear, upload and errors", async () => {
  const h = await boot();
  const calls = [];
  let reply = (url) => (/page=2/.test(url) ? { items: [{ id: "p4", url: "u4", thumb: "t4", author: "라" }, { id: "p5", url: "u5", thumb: "t5", author: "마" }], total_pages: 3 }
    : { items: [{ id: "p1", url: "u1", thumb: "t1", author: "가", alt: "우주" }, { id: "p2", url: "u2", thumb: "t2", author: "나" }], total_pages: 3 });
  const request = async (url, opts) => { calls.push(url); const r = reply(url, opts); if (r instanceof Error) throw r; return r; };
  const notices = [];
  const picker = window.ArkismCoverPicker({ el: (t, c, x) => { const n = document.createElement(t); if (c) n.className = c; if (x) n.textContent = x; return n; }, icon: () => document.createElement("i"),
    request, query: (o) => new URLSearchParams(o).toString(), state: { scope: { project_id: "p1" } }, notice: (m) => notices.push(m) });
  assert.equal(picker.src({ thumb: "t" }), "t");
  assert.equal(picker.src(null), "");
  const picks = [];
  const anchor = document.createElement("div");
  anchor.getBoundingClientRect = () => ({ left: 10, bottom: 20 });
  picker.open({ current: { id: "p1" }, onPick: (c) => picks.push(c), anchor }); await h.settle();
  let d = document.querySelector("dialog.tp-cover");
  assert.equal(d.querySelectorAll(".tp-cover-ph").length, 2);
  assert.ok(d.querySelector(".tp-cover-ph.on"));
  const more = h.btn(d, "더 보기");
  assert.equal(more.hidden, false);
  more.click(); await h.settle();
  assert.deepEqual([...d.querySelectorAll(".tp-cover-ph img")].map((i) => i.src), ["t1", "t2", "t4", "t5"]);
  // typed search with no hits, an empty one, then a stale answer
  const q = d.querySelector("input[type=search]");
  reply = () => ({ items: [] });
  q.value = "zz"; dom.key(q, "Enter"); await h.settle();
  assert.match(d.textContent, /맞는 사진이 없어요/);
  q.value = "  "; dom.key(q, "Enter"); await h.settle();
  // errors: no key on the server, then any other
  reply = () => new Error("dependency_unavailable");
  q.value = "space"; dom.key(q, "Enter"); await h.settle();
  assert.match(d.textContent, /ARKISM_UNSPLASH_KEY/);
  reply = () => new Error("boom");
  h.btn(d.querySelector(".tp-cover-chips"), "숲").click(); await h.settle();
  assert.match(d.textContent, /찾지 못했어요 \(boom\)/);
  // a pick closes the window and tells Unsplash
  reply = () => ({ items: [{ id: "p3", url: "u3", thumb: "t3", author: "다" }], total_pages: 1 });
  h.btn(d.querySelector(".tp-cover-chips"), "바다").click(); await h.settle();
  d.querySelector(".tp-cover-ph").click(); await h.settle();
  assert.equal(picks.at(-1).id, "p3");
  assert.ok(calls.some((u) => /photos\/p3\/use/.test(u)));
  // clear, and a click on the backdrop
  picker.open({ current: null, onPick: (c) => picks.push(c) }); await h.settle();
  d = document.querySelector("dialog.tp-cover");
  h.btn(d, "배경 없애기").click(); await h.settle();
  assert.equal(picks.at(-1), null);
  picker.open({ current: null, onPick: (c) => picks.push(c) }); await h.settle();
  d = document.querySelector("dialog.tp-cover");
  dom.fire(d, "click"); await h.settle();
  assert.equal(d.open, false);
  // an own photo: shrunk until it fits, one that never fits, one that is not an image, and no file
  const real = { Image: globalThis.Image, create: URL.createObjectURL, revoke: URL.revokeObjectURL, make: document.createElement };
  let size = [200000, 100], bad = false;
  globalThis.Image = class { constructor() { this.width = 2000; this.height = 1000; } set src(v) { queueMicrotask(() => (bad ? this.onerror() : this.onload())); } };
  URL.createObjectURL = () => "blob:x"; URL.revokeObjectURL = () => {};
  document.createElement = function (t) { if (t !== "canvas") return real.make.call(this, t); return { getContext: () => ({ drawImage() {} }), toDataURL: () => "d".repeat(size.length > 1 ? size.shift() : size[0]) }; };
  try {
    for (const [s, b] of [[[200000, 100], false], [[200000], false], [[1], true]]) {
      size = s; bad = b;
      picker.open({ current: null, onPick: (c) => picks.push(c) }); await h.settle();
      d = document.querySelector("dialog.tp-cover");
      const file = d.querySelector("input[type=file]");
      h.btn(d, "↑ 내 사진 올리기").click();
      Object.defineProperty(file, "files", { value: [{ name: "a.png" }], configurable: true });
      dom.fire(file, "change"); await h.settle(); await h.settle();
      if (d.open) d.close();
    }
    picker.open({ current: null, onPick: (c) => picks.push(c) }); await h.settle();
    d = document.querySelector("dialog.tp-cover");
    dom.fire(d.querySelector("input[type=file]"), "change"); await h.settle();
    d.close();
  } finally {
    globalThis.Image = real.Image; URL.createObjectURL = real.create; URL.revokeObjectURL = real.revoke; document.createElement = real.make;
  }
  assert.ok(picks.some((p) => p?.id === "mine"));
  assert.equal(notices.length, 2);
  assert.deepEqual(clean(), []);
});

const exact = (root, text) => [...root.querySelectorAll("button")].find((b) => b.textContent.trim() === text);
async function openAuto(h, id, tab = "overview") {
  h.render(); await h.settle();
  window.dispatchEvent(new CustomEvent("arkism:open-automation", { detail: { id, tab } }));
  await h.settle();
  return h.top();
}
const buildReply = (extra = {}) => ({ status: "done", output: JSON.stringify({ graph: { nodes: [{ id: "start", type: "start" }, { id: "s1", type: "ai_once", title: "모으기", config: { prompt: "{{input}}" } }, { id: "end", type: "end" }], edges: [{ from: "start", to: "s1" }, { from: "s1", to: "end" }] }, ...extra }) });

test("automation AI proposals: steps, goal and trigger, and the errors", async () => {
  const h = await boot();
  await openAuto(h, "wf1");
  const chat = () => h.chats.filter((c) => c.label === "AI 제안").at(-1);
  h.assistReply = () => buildReply({ goal: { task: "새 일", done: "점검", output: "  " }, trigger: { kind: "webhook", label: "PR 열림" } });
  let r = await chat().ask("웹훅으로 바꿔 줘");
  assert.ok(r.props.length >= 3);
  for (const p of r.props.filter(Boolean)) { p.apply(); await h.settle(); p.undo(); await h.settle(); p.apply(); await h.settle(); }
  h.assistReply = () => buildReply({ goal: "글", trigger: { kind: "schedule", cron: "0 7 * * *" } });
  r = await chat().ask("일정");
  for (const p of r.props.filter(Boolean)) { p.apply(); await h.settle(); }
  h.assistReply = () => buildReply({ trigger: { kind: "schedule", cron: "0 7 * * *" } });
  await chat().ask("같은 일정");
  h.assistReply = () => buildReply({ trigger: { kind: "odd" } });
  await chat().ask("이상한 일정");
  h.assistReply = () => ({ status: "failed" });
  await assert.rejects(chat().ask("실패"));
  h.assistReply = () => ({ status: "done", output: JSON.stringify({ ask: "무엇을 할까요?" }) });
  await assert.rejects(chat().ask("모름"), /무엇을 할까요/);
  h.assistReply = () => ({ status: "done", output: "{}" });
  await assert.rejects(chat().ask("빈 답"), /읽지 못했어요/);
  assert.deepEqual(clean(), []);
});

test("a new automation drafted by AI, given steps and created", async () => {
  const h = await boot();
  h.render(); await h.settle();
  h.btn(document, /새 워크플로우/).click(); await h.settle();
  const d = h.top();
  const chat = h.chats.filter((c) => c.label === "AI로 초안").at(-1);
  // saving with no name asks for one
  const make = () => exact(d, "자동화 만들기");
  if (make()) { make().click(); await h.settle(); }
  h.assistReply = () => buildReply({ name: "PR 요약", role: "아침마다 PR을 모아요", goal: { task: "PR 요약", done: "보냄", output: "글" } });
  const r = await chat.ask("매일 아침 PR 요약");
  for (const p of r.props.filter(Boolean)) { p.apply(); await h.settle(); p.undo(); await h.settle(); p.apply(); await h.settle(); }
  exact(d, "+ 단계 추가").click(); await h.settle();
  exact(d, "+ 단계 추가").click(); await h.settle();
  for (const f of h.fields) if (/textarea/i.test(f.input.tagName)) dom.type(f.input, "할 일");
  // a goal line stays one line even when pasted text carries line breaks
  const goal = h.fields.map((f) => f.input).find((i) => /auto-goal-input/.test(i.className || ""));
  dom.type(goal, "첫 줄\n  둘째 줄");
  assert.equal(goal.value, "첫 줄 둘째 줄");
  h.routes.unshift(["POST", /^\/v1\/crew$/, () => new Error("만들기 실패")]);
  make().click(); await h.settle();
  h.routes.shift();
  make().click(); await h.settle();
  assert.ok(h.notices.some((n) => /만들었어요/.test(n)), h.notices.join(" / "));
  assert.deepEqual(clean(), []);
});

test("when it runs: the start menu, plain words and the raw cron", async () => {
  const h = await boot();
  const d = await openAuto(h, "wf1");
  const menu = async () => { exact(d, "+ 시작 조건 추가").click(); await h.settle(); return d.querySelector(".auto-menu"); };
  const item = async (label) => { const m = await menu(); [...m.querySelectorAll("button")].find((b) => b.firstChild.textContent === label).click(); await h.settle(); };
  await item("일정");
  assert.ok(h.notices.some((n) => /이미 켜져/.test(n)));
  await item("이벤트");
  h.answer = false; await item("웹훅");
  h.answer = true; await item("웹훅");
  await item("웹훅");
  await item("일정");
  await menu(); await menu();
  // the day, half-day and hour pickers
  for (const label of ["토", "오후", "오전"]) { exact(d.querySelector(".auto-sched-pick"), label).click(); await h.settle(); }
  const hour = d.querySelector('.auto-sched-pick [aria-label="시"]');
  dom.type(hour, "0"); await h.settle();
  dom.type(d.querySelector('.auto-sched-pick [aria-label="시"]'), "7"); await h.settle();
  dom.type(d.querySelector('.auto-sched-pick [aria-label="분"]'), "99"); await h.settle();
  dom.type(d.querySelector('.auto-sched-pick [aria-label="분"]'), "15"); await h.settle();
  // plain words, read here or asked of the AI
  const say = () => d.querySelector(".auto-sched-say");
  for (const text of ["평일 아침 9시", "주말 오후 3시 반", "월·수 18:30", "매일 밤 2시", "매일 밤 9시", "화요일 점심", "목 저녁", "금 밤", "오전 12시", "매일 25시", "아무 말"]) {
    h.assistReply = () => ({ status: "done", output: "{\"cron\":null}" });
    say().value = text; dom.key(say(), "Enter"); await h.settle();
  }
  h.assistReply = () => new Error("끊김");
  say().value = "매월 1일 9시"; dom.key(say(), "Enter"); await h.settle();
  h.assistReply = () => ({ status: "done", output: "{\"cron\":\"0 9 1 * *\"}" });
  say().value = "매월 1일 9시"; dom.key(say(), "Enter"); await h.settle();
  // a cron the picker cannot show stays a raw line
  assert.equal(d.querySelector(".auto-days"), null);
  assert.ok(h.notices.some((n) => /알아듣지 못했어요/.test(n)));
  assert.ok(h.notices.some((n) => /끊김/.test(n)));
  assert.deepEqual(clean(), []);
});

test("automation settings, step goals, channels, the test run and version restore", async () => {
  const bots = [{ id: "b1", revision: 1, discord_bot: { name: "봇", state: "token_invalid", channels: [{ channel_id: "c1", name: "general", action: { kind: "workflow", id: "wf1" }, auto_start: false }, { channel_id: "c4", action: { kind: "workflow", id: "wf1" }, auto_start: true }] } }];
  const h = await boot({ discord: { bots, settings: {} } });
  h.routes.unshift(["GET", /^\/v1\/discord\/bots\/[^/]+\/channels/, () => ({ items: [{ channel_id: "c1", name: "general", parent: "글" }, { channel_id: "c2", name: "release" }] })]);
  const d = await openAuto(h, "wf1");
  await h.settle();
  // 멈추는 조건
  const stopRow = [...d.querySelectorAll(".auto-row, .auto-value, div")].find((r) => r.querySelector(".auto-key")?.textContent === "멈추는 조건" && exact(r, "바꾸기"));
  exact(stopRow, "바꾸기").click(); await h.settle();
  const [mins, steps] = stopRow.querySelectorAll("input");
  dom.type(mins, "45"); dom.type(steps, "0");
  exact(stopRow, "확인").click(); await h.settle();
  const stopRow2 = [...d.querySelectorAll("div")].find((r) => r.querySelector(".auto-key")?.textContent === "멈추는 조건" && exact(r, "바꾸기"));
  exact(stopRow2, "바꾸기").click(); await h.settle();
  for (const i of stopRow2.querySelectorAll("input")) dom.type(i, "");
  exact(stopRow2, "확인").click(); await h.settle();
  // a step goal edited in place: Escape, then a click outside keeps it
  let g = d.querySelector(".auto-goal-text");
  if (g) {
    g.click(); await h.settle();
    dom.key(h.fields.at(-1).input, "Escape"); await h.settle();
    g = d.querySelector(".auto-goal-text");
    dom.key(g, "Enter"); await h.settle();
    h.fields.at(-1).input.value = "새 목표";
    dom.fire(document.body, "pointerdown"); await h.settle();
    g = d.querySelector(".auto-goal-text");
    g.click(); await h.settle();
    dom.fire(d.querySelector(".auto-goal-edit"), "focusout", { relatedTarget: document.body }); await h.settle();
  }
  // discord channels that call the workflow
  exact(d, "$release-check로 부를 때만")?.click(); await h.settle();
  h.routes.unshift(["POST", /^\/v1\/discord\/bots\/[^/]+\/update/, () => new Error("저장 실패")]);
  exact(d, "글이 올라오면 시작")?.click(); await h.settle();
  h.routes.shift();
  h.answer = false; exact(d, "빼기").click(); await h.settle();
  h.answer = true; exact(d, "빼기").click(); await h.settle();
  exact(d, "채널 추가").click(); await h.settle(); await h.settle();
  const link = () => exact(d, "연결");
  link().click(); await h.settle();
  let ch = d.querySelector('select[aria-label="채널"]');
  ch.value = "c1";
  h.answer = false; link().click(); await h.settle();
  h.answer = true;
  ch = d.querySelector('select[aria-label="채널"]'); ch.value = "c2";
  link().click(); await h.settle();
  exact(d, "채널 추가").click(); await h.settle();
  const bs = d.querySelector('select[aria-label="봇"]'); dom.fire(bs, "change"); await h.settle();
  exact(d, "닫기")?.click(); await h.settle();
  assert.ok(h.notices.includes("채널을 연결했어요."), h.notices.join(" / "));
  assert.deepEqual(clean(), []);
});

test("automation test tab runs and polls, history restores a version", async () => {
  const h = await boot();
  let d = await openAuto(h, "wf2", "test");
  const q = d.querySelector(".auto-ask input");
  q.value = "주제"; dom.key(q, "Enter"); await h.settle();
  await h.settle(2600); await h.settle(2600);
  assert.ok(d.querySelector(".auto-run"));
  // one step on its own and up to a step: cancelled, then run
  d.close(); await h.settle();
  d = await openAuto(h, "wf1");
  for (const label of ["이 단계만", "여기까지"]) {
    exact(d, label).click(); await h.settle();
    h.btn(h.top(), "취소").click(); await h.settle();
  }
  exact(d, "여기까지").click(); await h.settle();
  const ask = h.top();
  dom.type(ask.querySelector("input"), "시험"); h.btn(ask, "실행").click(); await h.settle();
  const stop = exact(d, "멈추기");
  if (stop) { stop.click(); await h.settle(); }
  d.close(); await h.settle(2600);
  d = await openAuto(h, "wf1", "history");
  await h.settle();
  const back = () => exact(d, "이 버전으로 변경");
  h.answer = false; back().click(); await h.settle();
  h.answer = true;
  h.routes.unshift(["POST", /^\/v1\/crew\/wf1$/, () => new Error("저장 실패")]);
  back().click(); await h.settle();
  h.routes.shift();
  back().click(); await h.settle();
  assert.ok(h.notices.some((n) => /되돌렸어요/.test(n)), h.notices.join(" / "));
  assert.deepEqual(clean(), []);
});
