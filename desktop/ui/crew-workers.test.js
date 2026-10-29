// Behavior tests for the experts screen and expert window (crewWorkers, crewPersonas).
const test = require("node:test");
const assert = require("node:assert/strict");
require("./keep.js"); // the shared stale-reply and redraw guards the screens use
const { boot, dom } = require("./testdata/crew-harness.cjs");

const skipClose = (n) => n.matches(".crew-popup-close") || /^(취소|닫기)$/.test(n.textContent.trim());
const sweep = (h, root, rounds = 2) => dom.crawl(root, { settle: () => h.settle(), rounds, skip: skipClose });
const clean = () => dom.errors.splice(0).map((x) => String(x.stack || x).split("\n").slice(0, 2).join(" | "));

test("every expert card opens a window whose tabs all work", async () => {
  const h = await boot({ view: "workers" });
  h.render(); await h.settle();
  const cards = document.querySelectorAll(".ex-card");
  assert.ok(cards.length >= 2);
  for (const card of cards) {
    h.closeAll(); card.click(); await h.settle();
    const d = h.top();
    assert.ok(d, "expert window opens");
    // a saved expert can open as its own window (popup-drag.js); a new one has no id to reopen there yet
    if (card.matches(".ex-card-new")) assert.equal(d.dataset.popout, undefined);
    else { assert.equal(JSON.parse(d.dataset.popout).event, "arkism:open-expert"); assert.ok(d.dataset.popoutTitle); }
    for (const tab of [...d.querySelectorAll(".crew-popup-tab")]) { tab.click(); await h.settle(); await sweep(h, d); }
  }
  assert.deepEqual(clean(), []);
});

test("the workers page sweep and the open-expert event", async () => {
  const h = await boot({ view: "workers" });
  h.render(); await h.settle();
  await sweep(h, document.querySelector("#app"), 3);
  for (const tab of ["edit", "test", "usage", "history"]) {
    h.closeAll();
    h.state.view = "crew"; h.render();
    window.dispatchEvent(new CustomEvent("arkism:open-expert", { detail: { id: "ag1", tab } }));
    await h.settle();
    assert.equal(h.state.view, "crew");
  }
  window.dispatchEvent(new CustomEvent("arkism:open-expert", { detail: {} }));
  assert.deepEqual(clean(), []);
});

test("the open-expert event with over opens the window on the current screen", async () => {
  for (const tab of ["edit", "test", "usage", "history"]) {
    const h = await boot();
    h.render(); await h.settle();
    window.dispatchEvent(new CustomEvent("arkism:open-expert", { detail: { id: "ag2", tab, over: true } }));
    await h.settle();
    assert.equal(h.state.view, "crew");
    assert.ok(h.top(), "window opens over the current screen");
  }
  assert.deepEqual(clean(), []);
});

const dt = () => ({ setData() {}, getData() { return ""; }, effectAllowed: "", dropEffect: "" });
async function openExpert(h, name) {
  h.render(); await h.settle();
  [...document.querySelectorAll(".ex-card")].find((c) => c.textContent.includes(name)).click(); await h.settle();
  return h.top();
}

test("use chips open their detail, reorder by drag and come off the expert", async () => {
  const h = await boot({ view: "workers" });
  h.state.data.skills = [{ id: "s1", name: "git-review", description: "리뷰", category: "코드", version_history: ["a", "b", "c"], active_version: "c", updated_at: new Date().toISOString() }, { id: "s2", name: "write-doc", description: "문서" }];
  h.state.data.memories = [{ id: "m1", text: "기억 하나" }];
  const d = await openExpert(h, "리뷰어");
  const chips = () => [...d.querySelectorAll(".ex-uchip")];
  assert.ok(chips().length >= 3);
  for (let i = 0; i < chips().length; i++) {
    chips()[i].click(); await h.settle();
    const dlg = h.top();
    assert.match(dlg.className, /ex-skill-dlg/);
    await sweep(h, dlg);
    if (dlg.open) h.btn(dlg, "완료").click();
    await h.settle();
  }
  // pin a skill, pick an older version, then take the newer one
  const sk = chips().find((c) => c.textContent.includes("git-review"));
  sk.click(); await h.settle();
  let dlg = h.top();
  dlg.querySelector(".ex-pin-toggle input, .ex-pin-toggle button")?.click(); await h.settle();
  const ver = [...dlg.querySelectorAll("select")].at(-1);
  if (ver) { ver.value = "1"; dom.fire(ver, "change"); await h.settle(); }
  for (const b of [...dlg.querySelectorAll(".ex-newver button")]) { b.click(); await h.settle(); }
  h.btn(dlg, "이 전문가에서 빼기").click(); await h.settle();
  // drag the first pinned chip onto the last
  const pinned = () => [...d.querySelectorAll(".ex-uchip.pinned")];
  if (pinned().length >= 2) {
    const [a, b] = [pinned()[0], pinned().at(-1)];
    dom.fire(a, "dragstart", { dataTransfer: dt() }); dom.fire(b, "dragover", { dataTransfer: dt() }); dom.fire(b, "dragleave");
    dom.fire(b, "drop", { dataTransfer: dt() }); dom.fire(a, "dragend");
    dom.fire(a, "drop", { dataTransfer: dt() });
  }
  // add from the pick lists
  for (const add of [...d.querySelectorAll("input.wk-add")]) {
    const opt = document.getElementById(add.getAttribute("list"))?.querySelector("option");
    if (opt) { add.value = opt.value; dom.fire(add, "change"); await h.settle(); }
  }
  assert.deepEqual(clean(), []);
});

test("discord channels link to the expert from its window", async () => {
  const bots = [
    { id: "b1", revision: 1, discord_bot: { name: "봇", state: "connected", channels: [{ channel_id: "c1", name: "general", action: { kind: "agent", id: "ag1" }, auto_start: false }, { channel_id: "c9", action: { kind: "agent", id: "ag1" }, auto_start: true }] } },
    { id: "b2", revision: 1, discord_bot: { name: "둘", state: "needs_permission", channels: [{ channel_id: "c5", name: "five", action: { kind: "agent", id: "ag1" } }] } }
  ];
  const h = await boot({ view: "workers", discord: { bots, settings: {} } });
  h.routes.unshift(["GET", /^\/v1\/discord\/bots\/[^/]+\/channels/, () => ({ items: [{ channel_id: "c1", name: "general", parent: "글" }, { channel_id: "c2", name: "release" }] })]);
  const d = await openExpert(h, "리뷰어");
  await h.settle();
  const card = () => d.querySelector(".ex-ch");
  assert.ok(card(), "channel card");
  assert.match(card().textContent, /general/);
  // toggle the reply mode, then a failed save
  h.btn(card(), /부를 때만/).click(); await h.settle();
  h.routes.unshift(["POST", /^\/v1\/discord\/bots\/[^/]+\/update/, () => new Error("저장 실패")]);
  h.btn(card(), /답해요|부를 때만/).click(); await h.settle();
  h.routes.shift();
  h.btn(card(), "빼기").click(); await h.settle();
  // add a channel: none picked, one taken already, then a new one
  h.btn(card(), "+ 채널 연결").click(); await h.settle(); await h.settle();
  const [botSel, chSel] = card().querySelectorAll(".ex-ch-add select");
  const link = () => [...card().querySelectorAll("button")].find((b) => b.textContent.trim() === "연결");
  link().click(); await h.settle();
  assert.ok(h.needs.includes("채널을 골라 주세요."));
  chSel.value = "c1"; dom.fire(chSel, "change");
  h.answer = false; link().click(); await h.settle();
  h.answer = true;
  const [, ch2] = card().querySelectorAll(".ex-ch-add select");
  ch2.value = "c2"; dom.fire(ch2, "change");
  link().click(); await h.settle();
  assert.ok(h.notices.includes("채널을 연결했어요."));
  h.btn(card(), "+ 채널 연결").click(); await h.settle();
  const b2 = card().querySelector(".ex-ch-add select");
  b2.value = "b2"; dom.fire(b2, "change"); await h.settle();
  h.btn(card(), "+ 채널 연결").click(); await h.settle();
  assert.ok(botSel);
  assert.deepEqual(clean(), []);
});

test("a channel card with no bot asks for one first", async () => {
  const h = await boot({ view: "workers" });
  h.routes.unshift(["GET", /^\/v1\/discord(\?|$)/, () => new Error("없음")]);
  const d = await openExpert(h, "리뷰어");
  await h.settle();
  h.btn(d.querySelector(".ex-ch"), "+ 채널 연결").click(); await h.settle();
  assert.match(d.querySelector(".ex-ch").textContent, /먼저 디스코드 봇을 추가/);
  assert.deepEqual(clean(), []);
});

test("removing an expert: in a team, refused, failed and done", async () => {
  const h = await boot({ view: "workers" });
  h.data.crew.push({ id: "tm9", revision: 1, crew: { type: "team", name: "임시 팀", handle: "tmp", members: [{ agent_id: "ag1", role: "worker" }] } });
  let d = await openExpert(h, "리뷰어");
  h.btn(d, "전문가 삭제").click(); await h.settle();
  assert.ok(h.notices.some((n) => /팀에서 먼저 빼/.test(n)));
  h.closeAll();
  h.data.crew.pop();
  d = await openExpert(h, "리뷰어");
  h.typed = false; h.btn(d, "전문가 삭제").click(); await h.settle();
  h.typed = true;
  h.routes.unshift(["DELETE", /^\/v1\/crew\//, () => new Error("삭제 실패")]);
  h.btn(d, "전문가 삭제").click(); await h.settle();
  h.routes.shift();
  h.btn(d, "전문가 삭제").click(); await h.settle();
  assert.ok(h.notices.includes("지웠어요."));
  assert.deepEqual(clean(), []);
});

test("AI proposals change a saved expert", async () => {
  const h = await boot({ view: "workers" });
  await openExpert(h, "리뷰어");
  const chat = h.chats.filter((c) => c.label === "AI 제안").at(-1);
  h.assistReply = () => ({ status: "done", output: JSON.stringify({ role: "새 소개", instructions: "새 지시", effort: "low", skills: ["write-doc", "없는"], tools: ["github", "slack"] }) });
  const r = await chat.ask("바꿔 줘");
  assert.ok(r.props.length >= 3);
  for (const p of r.props) { p.apply(); p.undo(); p.apply(); }
  await h.settle();
  h.assistReply = () => ({ status: "done", output: "{}" });
  assert.match((await chat.ask("그대로")).say, /찾지 못했어요/);
  h.assistReply = () => ({ status: "failed" });
  await assert.rejects(chat.ask("실패"));
  assert.deepEqual(clean(), []);
});

test("a new expert from an AI draft joins old and new teams", async () => {
  const h = await boot({ view: "workers" });
  h.render(); await h.settle();
  h.btn(document, "+ 새 전문가").click(); await h.settle();
  const d = h.top();
  const chat = h.chats.filter((c) => c.label === "AI로 초안").at(-1);
  h.assistReply = () => ({ status: "done", output: JSON.stringify({ name: "테스트 도우미", role: "빠진 테스트를 찾아요", instructions: "## 역할\n테스트", file_name: "test-helper", skills: ["git-review", "x"], tools: ["github"] }) });
  const r = await chat.ask("테스트 찾는 동료");
  assert.ok(r.props.length >= 3);
  for (const p of r.props) p.apply();
  await h.settle();
  h.assistReply = () => ({ status: "done", output: "{\"x\":1}" });
  await assert.rejects(chat.ask("다시"), /읽지 못했어요/);
  h.assistReply = () => ({ status: "failed" });
  await assert.rejects(chat.ask("실패"));
  // join the existing team and two new ones made in the small popup
  const teamCard = () => [...d.querySelectorAll(".wk-chip")];
  teamCard().find((b) => b.textContent === "릴리스 팀").click(); await h.settle();
  for (const [name, handle] of [["", ""], ["새 팀", "bad handle"], ["새 팀", "reviewer"], ["새 팀", ""], ["둘째 팀", "second"]]) {
    teamCard().find((b) => b.textContent === "+ 새 팀").click(); await h.settle();
    const pop = h.top();
    const [n, hd] = pop.querySelectorAll("input");
    dom.type(n, name);
    if (handle) dom.type(hd, handle);
    h.btn(pop, "추가").click(); await h.settle();
    if (pop.open) h.btn(pop, "취소").click();
    await h.settle();
  }
  const nt = teamCard().find((b) => /새 팀 빼기/.test(b.textContent) && /둘째/.test(b.textContent));
  assert.ok(teamCard().some((b) => /새 팀 빼기/.test(b.textContent)));
  if (nt) { nt.click(); await h.settle(); }
  teamCard().find((b) => b.textContent === "+ 새 팀").click(); await h.settle();
  const pop = h.top();
  dom.type(pop.querySelector("input"), "셋째 팀"); h.btn(pop, "추가").click(); await h.settle();
  // the model must be set before saving; one team save fails
  h.routes.unshift(["POST", /^\/v1\/crew\/tm1$/, () => new Error("팀 저장 실패")]);
  const mk = () => [...d.querySelectorAll("button")].find((b) => b.textContent.trim() === "만들기");
  mk().click(); await h.settle();
  assert.ok(h.needs.some((n) => /기본 모델/.test(n)), JSON.stringify([h.needs, h.notices]));
  dom.type(d.querySelector('[aria-label="기본 모델"]'), "sonnet"); await h.settle();
  mk().click(); await h.settle(); await h.settle();
  assert.ok(h.notices.some((n) => /만들었어요/.test(n)), h.notices.join(" / "));
  assert.deepEqual(clean(), []);
});

test("the expert window: its workflow chart, memory kinds, face, close guard, test talk and version restore", async () => {
  const h = await boot({ view: "workers" });
  delete h.data.crew.find((r) => r.id === "wf1").crew.trigger;
  h.data.crew.find((r) => r.id === "ag1").crew.workflow = "$release-check";
  const d = await openExpert(h, "리뷰어");
  assert.ok(d.querySelector(".wk-flow-limit"), "limits under the chart");
  assert.ok(d.querySelector(".ex-flow-back-t"), "back edge label");
  for (const label of ["결정", "교훈"]) { [...d.querySelectorAll("button.wk-chip")].find((b) => b.textContent.trim() === label).click(); await h.settle(); }
  // the face picker: cancelled, then a pick
  const face = d.querySelector('[aria-label="아바타 고르기"]');
  face.click(); await h.settle();
  h.top().close(); await h.settle();
  face.click(); await h.settle();
  const fd = h.top();
  const radio = fd.querySelectorAll("input[type=radio]");
  radio[radio.length - 1].checked = true; dom.fire(radio[radio.length - 1], "change");
  fd.querySelector("form") && dom.fire(fd.querySelector("form"), "submit");
  h.btn(fd, /고르기|완료|확인/)?.click(); await h.settle();
  if (fd.open) fd.close();
  await h.settle();
  // closing with unsaved changes asks first
  h.answer = false;
  dom.fire(d, "cancel"); await h.settle();
  assert.ok(d.open);
  h.answer = true;
  dom.fire(d, "cancel"); await h.settle();
  assert.equal(d.open, false);
  // the test tab
  window.dispatchEvent(new CustomEvent("arkism:open-expert", { detail: { id: "ag1", tab: "test", over: true } })); await h.settle();
  const t = h.top();
  const q = t.querySelector('[aria-label="테스트 질문"]');
  const run = () => [...t.querySelectorAll("button")].find((b) => b.textContent.trim() === "실행");
  run().click(); await h.settle();
  for (const reply of [{ status: "done", output: "답이에요" }, { status: "done", output: "" }, { status: "failed" }, new Error("끊김")]) {
    h.assistReply = () => reply;
    q.value = "물어볼게"; dom.key(q, "Enter"); await h.settle();
  }
  assert.match(t.textContent, /답이에요/);
  assert.match(t.textContent, /끊김/);
  t.close(); await h.settle();
  // the history tab: restore an older version, refused then done
  window.dispatchEvent(new CustomEvent("arkism:open-expert", { detail: { id: "ag1", tab: "history", over: true } })); await h.settle();
  const hd = h.top();
  const back = () => [...hd.querySelectorAll("button")].find((b) => b.textContent.trim() === "이 버전으로 변경");
  h.answer = false; back().click(); await h.settle();
  h.answer = true; back().click(); await h.settle();
  assert.ok(h.notices.includes("저장했어요."), h.notices.join(" / "));
  assert.deepEqual(clean(), []);
});

test("persona settings tab", async () => {
  const h = await boot({ view: "settings" });
  h.render(); await h.settle();
  assert.ok(document.querySelector("#table").children.length);
  // Figma 198:6134: the persona list sits alone, with no 페르소나 title or hint above it
  assert.ok(document.querySelector("#table .cat-head"));
  assert.equal(document.querySelector("#table .pg-group h2"), null);
  await sweep(h, document.querySelector("#table"), 3);
  for (const d of h.dialogs()) await sweep(h, d);
  assert.deepEqual(clean(), []);
});

test("a new persona finished by AI and saved", async () => {
  const h = await boot({ view: "settings" });
  h.render(); await h.settle();
  h.btn(document.querySelector("#table"), "새 페르소나").click(); await h.settle();
  const chat = () => h.chats.filter((c) => c.label === "AI로 마무리").at(-1);
  await assert.rejects(chat().ask("마무리"), /먼저 써 주세요/);
  const body = document.querySelector("#table .cat-body");
  const [name] = body.querySelectorAll("input");
  dom.type(name, "검토자");
  h.assistReply = () => ({ status: "done", output: JSON.stringify({ name: "꼼꼼한 검토자", role: "보안 위주", instructions: "1. 너는 검토자다." }) });
  const r = await chat().ask("그대로 마무리해 줘");
  assert.equal(r.props.length, 3);
  for (const p of r.props) { p.apply(); p.undo(); p.apply(); }
  h.assistReply = () => ({ status: "done", output: "{}" });
  await assert.rejects(chat().ask("다시"), /읽지 못했어요/);
  h.assistReply = () => ({ status: "failed" });
  await assert.rejects(chat().ask("실패"));
  h.btn(body, "저장").click(); await h.settle();
  assert.ok(h.notices.includes("페르소나를 저장했어요."), h.notices.join(" / "));
  assert.deepEqual(clean(), []);
});

test("실행하는 곳: this PC's runner connections, their state, and only the name saved", async () => {
  window.__TAURI__ = { core: { invoke: async (cmd) => (cmd === "agent_backends" ? [
    { name: "myrunner", status: "ok", checked_at: "2026-10-26T01:42:00Z", capabilities: { cancel: true, tool_restriction: true }, own_model: true },
    { name: "down", status: "failed: timeout", capabilities: {} }] : null) } };
  try {
    const h = await boot({ view: "workers" });
    const d = await openExpert(h, "리뷰어");
    const row = (k) => [...d.querySelectorAll(".ex-run-row")].find((r) => r.querySelector(".k")?.textContent === k);
    assert.ok(row("실행하는 곳"));
    assert.equal(row("연결 상태").hidden, true, "Arkism's own path needs no connection state");
    const pick = (label) => [...row("실행하는 곳").querySelectorAll("button")].find((b) => b.textContent === label);
    assert.ok(pick("Arkism") && pick("myrunner") && pick("down"));
    pick("myrunner").click(); await h.settle();
    const badges = () => [...row("연결 상태").querySelectorAll(".wf-badge")].map((b) => b.textContent);
    assert.deepEqual(badges(), ["실행 가능", "모델 선택 안 됨", "사용량 확인 안 됨"]);
    pick("down").click(); await h.settle();
    assert.deepEqual(badges(), ["연결 실패"]);
    pick("myrunner").click(); await h.settle();
    h.btn(d, "저장").click(); await h.settle();
    const saved = h.calls.filter((c) => c.method === "POST" && /\/v1\/crew\/ag1$/.test(c.url)).at(-1);
    assert.equal(saved.body.crew.backend, "myrunner");
    assert.ok(!JSON.stringify(saved.body).includes("checked_at"), "only the connection name goes to the server");
    pick("Arkism").click(); await h.settle();
    h.btn(d, "저장").click(); await h.settle();
    assert.equal(h.calls.filter((c) => c.method === "POST" && /\/v1\/crew\/ag1$/.test(c.url)).at(-1).body.crew.backend, undefined);
  } finally { delete window.__TAURI__; }
  assert.deepEqual(clean(), []);
});
