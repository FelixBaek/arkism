// ArkismAIChat: the sparkle pill with its bubble and purple proposal list, and the one-field sparkle.
const test = require("node:test");
const assert = require("node:assert/strict");
const { createDocument, flush, Element } = require("./testdata/views-dom.js");

const doc = createDocument();
globalThis.window = globalThis;
globalThis.document = doc;
globalThis.requestAnimationFrame = (fn) => setImmediate(fn);
globalThis.innerHeight = 800;
Element.prototype.select = function () {};
require("./text-diff.js");
require("./ai-chat.js");
const AI = window.ArkismAIChat;

const q = (root, sel) => root.querySelectorAll(sel);
const byText = (root, text, sel = "button") => q(root, sel).find((b) => b.textContent.trim() === text);
const mountIn = (node) => { const host = doc.createElement("div"); doc.body.append(host); host.append(node); return host; };

test("bubble: open, quick ask, proposals of every shape, apply, undo, drop, apply all", async () => {
  window.ArkismI18n = { translate: (s) => s };
  const undone = [], dropped = [];
  let applyResult;
  const props = () => [
    null,
    { label: "스킬", from: ["a", "b"], to: ["b", "c"], apply: () => true, undo: () => { undone.push("list"); } },
    { label: "순서", from: ["a", "b"], to: ["b", "a"], apply: () => applyResult },
    { label: "이름", from: "", to: "새 이름", apply: () => true },
    { label: "지시", from: "짧은 글이지만 스물네 자를 넘어가는 긴 지시문이에요", to: "긴 지시문이에요\n둘째 줄", apply: () => true },
    { label: "제안", from: null, to: undefined, apply: () => true },
  ];
  const st = {};
  const root = AI.button({ label: "AI", sub: "도움", greeting: "안녕", placeholder: "물어보세요", quick: ["다듬어 줘"], state: st, onDrop: (p) => dropped.push(p.label), ask: async () => ({ props: props() }) });
  mountIn(root);
  await flush();
  const anchor = root.querySelector(".ex-ai-btn");
  anchor.click();
  assert.equal(anchor.getAttribute("aria-expanded"), "true");
  await flush();
  byText(root, "다듬어 줘").click(); await flush();
  assert.equal(st.props.length, 5);
  assert.equal(st.open, false);
  const list = root.querySelector(".ex-props");
  assert.ok(list.textContent.includes("AI 제안 5개") && list.textContent.includes("순서만 바뀌어요") && list.textContent.includes("없음"));
  assert.deepEqual(q(list, "del").map((d) => d.textContent).slice(0, 2), ["a", "없음"]);
  // apply that refuses stays unapplied; a list proposal applies and undoes
  applyResult = false;
  byText(q(root, ".ex-prop")[1], "적용").click(); await flush();
  assert.equal(st.props[1].applied, false);
  byText(q(root, ".ex-prop")[0], "적용").click(); await flush();
  assert.ok(root.querySelector(".ex-prop.applied").textContent.includes("적용함"));
  byText(root.querySelector(".ex-prop.applied"), "되돌리기").click(); await flush();
  assert.deepEqual(undone, ["list"]);
  byText(q(root, ".ex-prop")[1], "빼기").click(); await flush();
  assert.deepEqual(dropped, ["순서"]);
  byText(root, "모두 적용").click(); await flush();
  assert.equal(st.props.every((p) => p.applied), true);
  assert.equal(byText(root, "모두 적용"), undefined);
  // an applied proposal without undo shows no 되돌리기
  assert.equal(q(root, ".ex-prop.applied").length, 4);
  delete window.ArkismI18n;
});

test("bubble: typing, Enter, IME, Escape, close, busy guard, errors and empty answers", async () => {
  let release, mode = "wait";
  const st = {};
  const ask = (text) => {
    if (mode === "wait") return new Promise((r) => { release = r; });
    if (mode === "throw") throw new Error("모델 오류");
    if (mode === "bare") throw {};
    if (mode === "null") return null;
    return { say: `답: ${text}` };
  };
  const root = AI.button({ state: st, ask: (t) => ask(t) });
  mountIn(root);
  const anchor = root.querySelector(".ex-ai-btn");
  anchor.click(); await flush();
  let input = root.querySelector("input");
  const oldGo = byText(root, "보내기");
  input.value = "  안녕  "; await input.fire("input");
  assert.equal(st.ask, "  안녕  ");
  await input.fire("keydown", { key: "Enter", isComposing: true });
  assert.equal(st.log.length, 0);
  await input.fire("keydown", { key: "Enter" });
  assert.ok(root.querySelector(".ex-chat-ai.busy"));
  oldGo.click(); // a second send while the first is thinking is ignored
  input = root.querySelector("input");
  await input.fire("keydown", { key: "Enter" }); // go is disabled while busy
  release(undefined); await flush();
  assert.ok(root.textContent.includes("바꿀 곳을 찾지 못했어요"));
  for (const m of ["throw", "bare", "null", "say", "say", "say", "say", "say"]) { mode = m; input = root.querySelector("input"); input.value = `${m}!`; byText(root, "보내기").click(); await flush(); }
  assert.equal(st.log.length, 12);
  assert.ok(root.textContent.includes("답: say!"));
  byText(root, "보내기").click(); // empty text sends nothing
  // Escape inside the bubble closes it; an already handled Escape does not
  const pop = root.querySelector(".ex-ai-pop");
  await pop.fire("keydown", { key: "Escape", defaultPrevented: true });
  assert.equal(st.open, true);
  await pop.fire("keydown", { key: "Escape" });
  assert.equal(st.open, false);
  assert.equal(doc.activeElement, anchor);
  anchor.click(); await flush();
  root.querySelector(".ex-ai-pop .icon-btn").click();
  assert.equal(st.open, false);
  // the zone keeps clicks unless they land on a field
  const zone = root.querySelector(".ex-ai-zone");
  const inField = await zone.fire("click", { target: doc.createElement("input") });
  const elsewhere = await zone.fire("click", { target: doc.createElement("span") });
  assert.equal(inField.defaultPrevented, false);
  assert.equal(elsewhere.defaultPrevented, true);
});

test("docked button, a newer copy of the box, scroll to the list", async () => {
  const st = {};
  let scrolled = 0;
  const opts = { state: st, ask: async () => ({ say: "봐요", props: [{ label: "k", from: "a", to: "b", apply: () => true }] }) };
  const first = AI.button(opts);
  mountIn(first);
  const second = AI.button(opts); // the screen redrew: this copy now owns the state
  const bar = doc.createElement("div");
  mountIn(second);
  doc.body.append(bar); bar.append(second.dock);
  second.scrollIntoView = () => { scrolled++; };
  await flush();
  assert.equal(second.hidden, true);
  first.remove();
  first.querySelector(".ex-ai-btn").click(); // the old copy hands the paint to the new one
  assert.ok(second.dock.querySelector(".ex-ai-pop"));
  byText(second.dock, "보내기").click();
  second.dock.querySelector("input").value = "go";
  byText(second.dock, "보내기").click(); await flush();
  assert.equal(scrolled, 1);
  assert.equal(second.hidden, false);
  assert.equal(st.props[0].applied, false);
});

test("bubble: capped to the room above when docked, below otherwise, so the first message never leaves the screen", async () => {
  const rect = Element.prototype.getBoundingClientRect, h = globalThis.innerHeight;
  Element.prototype.getBoundingClientRect = () => ({ top: 100, bottom: 300, left: 0, right: 100, width: 100, height: 200 });
  globalThis.innerHeight = 400;
  try {
    const below = AI.button({ state: {}, ask: async () => ({}) });
    mountIn(below);
    await flush();
    below.querySelector(".ex-ai-btn").click(); await flush();
    assert.equal(below.querySelector(".ex-ai-pop").style.maxHeight, "292px"); // 400 − top 100 − 8
    const up = AI.button({ state: {}, ask: async () => ({}) });
    mountIn(up);
    const bar = doc.createElement("div"); doc.body.append(bar); bar.append(up.dock);
    await flush();
    up.dock.querySelector(".ex-ai-btn").click(); await flush();
    assert.equal(up.dock.querySelector(".ex-ai-pop").style.maxHeight, "292px"); // bottom 300 − 8
    globalThis.innerHeight = 50; // no room: a floor keeps the input usable
    below.querySelector(".ex-ai-btn").click(); below.querySelector(".ex-ai-btn").click(); await flush();
    assert.equal(below.querySelector(".ex-ai-pop").style.maxHeight, "160px");
  } finally { Element.prototype.getBoundingClientRect = rect; globalThis.innerHeight = h; }
});

test("field: fills, polishes, trims one-line answers, undoes and reports failures", async () => {
  const replies = [];
  const kit = {
    assist: async (kind, prompt) => { assert.equal(kind, "json"); kit.last = prompt; return replies.shift(); },
    assistError: (a) => `오류: ${a.error}`,
    parseJSON: (t) => { try { return JSON.parse(t); } catch { return null; } },
  };
  // no kit anywhere
  const area = doc.createElement("textarea");
  const holder = doc.createElement("label"); holder.append(area); mountIn(holder);
  const sta = {};
  const fa = AI.field(area, { state: sta, title: "소개" });
  assert.ok(fa.classList.contains("is-area"));
  const star = fa.querySelector(".ex-ai-spark");
  await star.click(); await flush();
  assert.ok(fa.querySelector(".ex-ai-note.err").textContent.includes("AI 도움을 쓸 수 없어요."));
  // api.kit is the fallback; empty field writes from context; execCommand types the text
  AI.kit = kit;
  doc.execCommand = (cmd, _ui, v) => { doc.activeElement.value = v; return true; };
  replies.push({ status: "done", output: JSON.stringify({ value: "  새 소개  " }) });
  await star.click(); await flush();
  assert.equal(area.value, "새 소개");
  assert.ok(kit.last.includes("(empty)") && kit.last.includes("Help write this field."));
  // the same text back: nothing to change
  replies.push({ status: "done", output: JSON.stringify({ value: "새 소개" }) });
  await star.click(); await flush();
  assert.ok(fa.textContent.includes("더 고칠 곳이 없어요."));
  // failed run, unreadable answer, thrown error without message
  replies.push({ status: "failed", error: "x" });
  await star.click(); await flush();
  assert.ok(fa.textContent.includes("오류: x"));
  replies.push({ status: "done", output: "not json" });
  await star.click(); await flush();
  assert.ok(fa.textContent.includes("AI의 답을 읽지 못했어요"));
  replies.push({ status: "done" });
  await star.click(); await flush();
  const old = kit.assist; kit.assist = async () => { throw {}; };
  await star.click(); await flush();
  assert.ok(fa.textContent.includes("AI가 답하지 못했어요."));
  kit.assist = old;

  // one-line INPUT: bullet stripped, max length, execCommand missing → plain assignment + input event
  delete doc.execCommand;
  const inp = doc.createElement("input"); inp.maxLength = 5; inp.value = "옛 글";
  let inputs = 0; inp.addEventListener("input", () => { inputs++; });
  const sti = {};
  const fi = AI.field(inp, { state: sti, kit, preset: "이름", context: () => "맥락" });
  mountIn(fi);
  let release;
  replies.push(new Promise((r) => { release = r; }));
  const s2 = fi.querySelector(".ex-ai-spark");
  s2.click(); s2.listeners.click[0](); // busy: the second run does nothing
  assert.equal(s2.disabled, true);
  release({ status: "done", output: JSON.stringify({ value: "\n- 첫 줄입니다요\n둘째" }) }); await flush();
  assert.equal(inp.value, "첫 줄입니");
  assert.ok(kit.last.includes("Context:\n맥락") && kit.last.includes("Improve the current text"));
  assert.ok(inputs >= 1);
  // user typing clears the note; a synthetic input does not
  await inp.fire("input", { isTrusted: false });
  assert.equal(fi.querySelector(".ex-ai-note").hidden, false);
  await inp.fire("input", { isTrusted: true });
  assert.equal(fi.querySelector(".ex-ai-note").hidden, true);
  await inp.fire("input", { isTrusted: true }); // nothing left to clear
  // 취소 puts the old text back
  replies.push({ status: "done", output: JSON.stringify({ value: "다른 것" }) });
  await s2.click(); await flush();
  assert.equal(fi.querySelector(".ex-ai-diff del").textContent, "첫 줄입니");
  assert.equal(fi.querySelector(".ex-ai-diff ins").textContent, "다른 것");
  byText(fi, "취소").click();
  assert.equal(inp.value, "첫 줄입니");
  // undo with no field on screen does nothing; a newer copy takes over show()
  replies.push({ status: "done", output: JSON.stringify({ value: "셋째" }) });
  await s2.click(); await flush();
  const undo = byText(fi, "취소");
  const keep = sti.input; sti.input = null; undo.click(); sti.input = keep;
  assert.equal(inp.value, "셋째");
  const inp2 = doc.createElement("input");
  fi.remove();
  const fi2 = AI.field(inp2, { state: sti, kit });
  mountIn(fi2);
  replies.push({ status: "done", output: JSON.stringify({ value: "넷" }) });
  s2.click(); await flush();
  assert.equal(inp2.value, "넷");
  assert.equal(doc.activeElement, byText(fi2, "저장"), "focus lands on 저장 of the copy on screen, not the replaced one");
  assert.ok(byText(fi2, "저장") && !fi2.textContent.includes("AI가 고쳤어요"), "only 저장 / 취소, no note text");
  // the box shows what changed in place (added / removed); a click on it goes back to the field
  const box = fi2.querySelector(".ex-ai-diff");
  assert.equal(box.hidden, false);
  assert.equal(inp2.style.display, "none");
  assert.equal(box.querySelector("del"), null, "the new copy started empty: all of it is added");
  assert.equal(box.querySelector("ins").textContent, "넷");
  box.listeners.click[0]();
  assert.equal(box.hidden, true);
  assert.equal(inp2.style.display, "");
  assert.ok(fi2.textContent.includes("취소"), "취소 stays after going back to edit");
  // 저장 keeps the AI text and closes the note
  byText(fi2, "저장").click();
  assert.equal(inp2.value, "넷");
  assert.equal(fi2.querySelector(".ex-ai-note").hidden, true);
  // field() with default opts and an input outside the page
  const loose = AI.field(doc.createElement("input"));
  assert.ok(loose.classList.contains("ex-ai-field"));
});
