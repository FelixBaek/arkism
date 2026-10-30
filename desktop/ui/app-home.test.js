// Home, palette search, shortcuts, start steps and the MEMORY › 개요 windows, driven through the booted app (testdata/app-harness.js).
// One boot per file: entry.mjs is cached per process. API answers the preview fixtures lack come from `routes` below.
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const { boot } = require("./testdata/app-harness.js");

// [method, path prefix, answer(args)] checked before the preview server; an answer returns { status, body }
const routes = [];
const ok = (body) => ({ status: 200, body: JSON.stringify(body) });
const fail = (status, code) => ({ status, body: JSON.stringify({ code }) });
const route = (method, prefix, answer) => { const r = [method, prefix, answer]; routes.unshift(r); return () => routes.splice(routes.indexOf(r), 1); };
const NOW = Date.now(), ago = (h) => new Date(NOW - h * 3600000).toISOString();

let app;
test.before(async () => {
  app = await boot({
    beforeEntry(env) {
      env.window.apiOverride = (a) => { for (const [m, p, fn] of routes) if (a.method === m && a.path.startsWith(p)) return fn(a); return null; };
    }
  });
});
test.after(() => app?.stop());

// memory use: m1 used lately, m2 used a week ago (the 불러오기 / 사용량 lists need a last use)
route("GET", "/v1/memory_usage", () => ok({ items: [{ id: "m1", d7: 4, d30: 9, last: ago(2) }, { id: "m2", d7: 0, d30: 2, last: ago(200) }, { id: "m3", d7: 1, d30: 1, last: ago(30) }] }));

const text = (n) => app.dom.text(n);
const win = () => app.$("dialog.ov-win[open]");
const btn = (label, root = app.document) => [...root.querySelectorAll("button")].find((b) => text(b) === label || b.getAttribute("aria-label") === label);
const press = async (label, root, ms = 0) => { const b = btn(label, root); assert.ok(b, `button ${label}`); b.click(); await app.settle(ms); return b; };
const openWindow = async (detail) => { app.window.dispatchEvent(new app.window.CustomEvent("arkism:open-overview-window", { detail })); await app.settle(50); return win(); };
const closeAll = async () => { for (const d of app.$$("dialog[open]")) d.close(); await app.settle(); };
// opens 승인 대기 and picks the candidate whose text starts so (a focus id given before the page loads falls to the first item)
async function reviewOf(start) {
  await openWindow({ wide: "review" }); await app.settle(50);
  // the window shows one kind at a time: look through each kind's chip
  const find = () => [...win().querySelectorAll(".ov-wide-row")].find((r) => text(r.querySelector(".ov-win-item-title")).startsWith(start));
  for (const chip of [...win().querySelectorAll(".ov-wide-filter > .ov-wide-chips > .ov-wide-chip")]) { if (find()) break; chip.click(); await app.settle(50); }
  const row = find();
  assert.ok(row, `row ${start}`); row.click(); await app.settle();
  return win();
}

test("overview: stage windows step through every stage and their lists", async () => {
  await openWindow({ key: "collect" });
  await app.settle(100);
  // 대화 저장 opens wide: the saved sessions with what each produced and whether its transcript is kept
  await openWindow({ key: "collect" }); await app.settle(50); let d = win();
  assert.ok(d.classList.contains("ov-wide-win"));
  assert.match(text(d.querySelector(".ov-win-title")), /대화 저장/);
  assert.ok(d.querySelectorAll(".ov-wide-row").length > 1, "the session list fills once /v1/usage/sessions answered");
  assert.match(text(d), /원문 보관/);
  assert.equal(btn("‹ 작게 보기", d), undefined, "no compact step first");
  // no ‹ n / N 단계 › walker: the overview cards already open each step
  d = await openWindow({ key: "dream" }); assert.doesNotMatch(text(d), /\d \/ \d 단계/);
  assert.equal(btn("다음 단계", d), undefined);
  // 승인 is its own group after 정리, in the top bar and the 기억 처리 grid
  assert.deepEqual([...app.document.querySelectorAll(".ov-group-head span")].map(text), ["저장", "사용", "정리", "승인"]);
  assert.deepEqual([...app.document.querySelectorAll(".ov-grid-sec")].map(text), ["저장", "사용", "정리", "승인"]);
  d = await openWindow({ key: "review" });
  assert.equal(d.querySelector(".ov-win-next"), null);
  // every stage opens wide with its own list on the right; 분석 links to the session list
  for (const key of ["extract", "classify", "judge", "dream"]) {
    d = await openWindow({ key });
    assert.ok(d.classList.contains("ov-wide-win"), `${key} opens wide`);
    assert.ok(d.querySelector(".ov-stage-list .ov-win-list"), key);
    assert.equal(!!btn("세션 기록 보기 ›", d), key === "extract", `${key} session link`);
  }
  // 사용 기록 opens wide, with no compact step first
  d = await openWindow({ key: "trace" });
  assert.ok(d.classList.contains("ov-wide-win"), "trace opens wide");
  assert.ok(d.querySelector(".ov-wide-row"), "a row per memory");
  assert.ok(d.querySelector("select.ov-wide-sort"), "sortable");
  assert.equal(btn("‹ 작게 보기", d), undefined);
  // 대화 저장 reads a session's dialogue in place (there is no separate 세션 screen)
  await openWindow({ key: "collect" }); await app.settle(50);
  const talks = app.apiCalls.filter(([, p]) => p === "/v1/usage/sessions/transcript").length;
  win().querySelector(".ov-wide-row").click(); await app.settle();
  await press("대화 보기", win(), 80);
  assert.ok(app.apiCalls.filter(([, p]) => p === "/v1/usage/sessions/transcript").length > talks, "it reads the dialogue");
  assert.ok(win().querySelector(".ss-msg"), "the dialogue shows in the window");
  await press("닫기", await openWindow({ key: "trace" }));
  assert.equal(win(), null);
});

test("overview: 자세히 on a 분류 card opens that candidate's own detail, even off the loaded page", async () => {
  const d = await openWindow({ key: "classify" });
  const items = [...d.querySelectorAll(".ov-win-list .ov-win-item")];
  assert.ok(items.length > 1);
  // the newest card, with a first page that does not carry it (the 승인 대기 list loads oldest first)
  const card = items.at(-1), title = text(card.querySelector(".ov-win-item-title"));
  const off = route("POST", "/v1/memory_candidates/query", () => ok({ items: [], total: 0, facets: {} }));
  await press("자세히", card, 80);
  off();
  const w = win(), head = title.replace(/…$/, "").slice(0, 20);
  assert.ok(text(w.querySelector(".ov-wide-row.on")).includes(head), "the clicked candidate is the selected row");
  assert.ok(text(w.querySelector(".ov-wide-detail")).includes(head), "and its detail is open");
  await closeAll();
});

test("overview: 분류 groups its waiting candidates by kind inside the window", async () => {
  const d = await openWindow({ key: "classify" });
  const list = d.querySelector(".ov-win-list"), heads = [...list.querySelectorAll(".ov-win-group")];
  assert.ok(heads.length > 1, "more than one kind header");
  for (const h of heads) { // each header counts the rows up to the next header
    let n = 0; for (let x = h.nextSibling; x && !x.classList.contains("ov-win-group"); x = x.nextSibling) n++;
    assert.equal(text(h.querySelector("small")), `${n}건`, text(h));
  }
  assert.equal(new Set(heads.map((h) => text(h.querySelector(".ov-win-tag")))).size, heads.length, "one header per kind");
  assert.equal(btn("승인 대기에서 보기", d), undefined, "no jump to the approval window per row");
  assert.ok(![...d.querySelectorAll("button")].some((b) => /모두 보기|승인 대기 열기/.test(text(b))), "every candidate shows, nothing to jump to");
  // each candidate is approved or discarded right here, with the same review call as 승인 대기
  const item = list.querySelector(".ov-win-item");
  await press("승인", item, 50);
  assert.ok(app.apiCalls.some(([m, p, b]) => m === "POST" && /\/v1\/memory_candidates\/.+\/review$/.test(p) && /"approve"/.test(String(b))));
  const next = win().querySelector(".ov-win-list .ov-win-item");
  await press("버리기", next);
  assert.ok(next.querySelector(".del-reason"), "버리기 still asks for a reason, in place");
  await press("취소", next.querySelector(".del-reason"));
  await closeAll();
});

test("overview: 정리하는 중 decides each proposal in its own row, with the 승인 대기 choices", async () => {
  let d = await openWindow({ key: "dream" });
  assert.equal(d.querySelector(".ov-win-foot button"), null, "no jump to 승인 대기");
  const wrap = d.querySelector(".ov-win-proposal"); assert.ok(wrap, "a proposal row");
  const panel = wrap.querySelector(".ov-win-proposal-panel"); assert.ok(panel.hidden);
  await press("결정하기 ⌄", wrap); assert.ok(!panel.hidden);
  assert.ok(btn("반대로 남기기", panel) || btn("괜찮아요", panel), "the same choices as 승인 대기");
  await press("접기 ⌃", wrap); assert.ok(panel.hidden);
  await press("결정하기 ⌄", wrap);
  const pick = btn("둘 다 두기", panel) ? "둘 다 두기" : "괜찮아요";
  await press(pick, panel, 30);
  if (pick === "둘 다 두기") await press("이대로 적용", panel, 80); // a pair choice previews first
  assert.ok(app.apiCalls.some(([m, p, b]) => m === "POST" && /\/v1\/maintenance_proposals\/.+\/decide$/.test(p) && /"dismiss"/.test(String(b))));
  assert.match(text(win()), /정리하는 중/, "it stays in the 정리하는 중 window");
  await closeAll();
});

test("overview: a stage window lists every row on the right and loads more on scroll", async () => {
  const d = await openWindow({ key: "extract" });
  assert.equal([...d.querySelectorAll("button")].find((b) => /모두 보기|작게 보기/.test(text(b))), undefined, "no compact or expanded mode");
  const list = d.querySelector(".ov-stage-list");
  const before = list.querySelectorAll(".ov-win-item").length;
  assert.ok(before > 1);
  list._rect = { top: 0, left: 0, width: 300, height: 100, right: 300, bottom: 100 }; list.scrollHeight = 120; list.scrollTop = 0;
  app.dom.fire(list, "scroll"); // fewer rows than one page: nothing more to load
  assert.equal(list.querySelectorAll(".ov-win-item").length, before);
  await closeAll();
});

test("overview: review rows approve and reject in place, with the reason asked first", async () => {
  let d = await openWindow({ key: "classify" });
  const before = d.querySelectorAll(".ov-win-item").length;
  const row = d.querySelector(".ov-win-item");
  await press("승인", row, 50);
  assert.ok(!row.isConnected, "an approved row leaves the list");
  assert.ok(app.apiCalls.some(([m, p]) => m === "POST" && /\/review$/.test(p)));
  d = win() || await openWindow({ key: "classify" });
  // 버리기 opens the reason picker in place; no reason is refused, 취소 keeps the row
  const row2 = d.querySelector(".ov-win-item");
  await press("버리기", row2);
  let ask = row2.querySelector(".del-reason"); assert.ok(ask);
  assert.deepEqual([...ask.querySelectorAll(".del-reason-chip")].map(text), ["오래된 기억", "이미 바뀜", "사용하지 않음", "사실과 다름", "중복"]);
  await press("버리기", ask);
  assert.match(text(ask), /이유를 하나 골라 주세요/);
  await press("취소", ask); assert.ok(!ask.isConnected && row2.isConnected);
  await press("버리기", row2); ask = row2.querySelector(".del-reason");
  await press("중복", ask);
  ask.querySelector("textarea").value = "겹치는 내용";
  await press("버리기", ask, 50);
  assert.ok(!row2.isConnected);
  const sent = JSON.parse(app.apiCalls.filter(([m, p]) => m === "POST" && /\/review$/.test(p)).at(-1)[2]);
  assert.deepEqual([sent.action, sent.reason_code, sent.reason], ["reject", "duplicate", "겹치는 내용"]);
  // a server error keeps the row and says why
  const off = route("POST", "/v1/memory_candidates/", () => fail(409, "revision_conflict"));
  d = win() || await openWindow({ key: "classify" });
  const row3 = d.querySelector(".ov-win-item");
  if (row3) {
    await press("승인", row3, 50);
    assert.match(text(row3), /이미 처리됐거나/);
    await press("버리기", row3); ask = row3.querySelector(".del-reason"); await press("오래된 기억", ask); await press("버리기", ask, 50);
    assert.match(text(ask), /이미 처리됐거나/, "the picker stays open with the reason it failed");
  }
  off();
  assert.ok(before > 0);
  await closeAll();
});

test("overview: 불러오기 shows sessions by provider, an empty history and a failed read", async () => {
  await openWindow({ key: "recall" });
  await app.settle(50); let d = win();
  assert.ok(d.classList.contains("ov-wide-win"), "불러오기 opens wide");
  assert.match(text(d), /세션 \d+개 · \d+번 불러옴, 한 번에 평균/);
  assert.deepEqual([...d.querySelectorAll(".ov-wide-chip")].map((c) => text(c).replace(/\s*\d+$/, "")), ["전체", "Codex", "Claude Code", "Gemini CLI", "이전 기록"], "chips split by provider");
  assert.deepEqual([...d.querySelectorAll(".ov-wide-row .ov-win-item-title")].map(text), ["Codex · 세션 019a2f6c", "Claude Code · 세션 7d3e9b12", "Gemini CLI · 세션 g-202610", "이전 기록 · 세션 정보 없음"], "one row per session; older receipts fold into one");
  assert.match(text(d.querySelector(".ov-wide-row .ov-wide-row-r")), /^2번 · 기억 \d+개/, "a session's recalls fold together");
  assert.match(text(d), /이 세션에 2번, 기억 \d+개를 넣어 줬어요/, "the first session shows on the right");
  d.querySelectorAll(".ov-wide-row")[3].click(); await app.settle(); d = win();
  assert.ok(d.querySelectorAll(".ov-wide-row")[3].classList.contains("on"), "the picked session shows on the right");
  assert.match(text(d.querySelector(".ov-wide-detail .ov-detail-head")), /세션 정보 없음/);
  [...d.querySelectorAll(".ov-wide-chip")].find((c) => text(c).startsWith("Codex")).click(); await app.settle(); d = win();
  assert.equal(d.querySelectorAll(".ov-wide-row").length, 1, "a provider chip keeps its sessions only");
  assert.ok(d.querySelector(".ov-recall-item") && !/^m\d/.test(text(d.querySelector(".ov-recall-item p"))), "a memory shows as its text, not its id");
  assert.ok(d.querySelector("details.ov-recall-more"), "scores sit behind a toggle");
  let off = route("GET", "/v1/recalls", () => ok({ items: [] }));
  await openWindow({ key: "recall" }); await app.settle(50); d = win();
  assert.match(text(d), /아직 불러온 기록이 없어요/);
  // a folder with no remote and no project read as words, not keys
  off(); off = route("GET", "/v1/recalls", () => ok({ items: [["path:274fec32160f60b2", "2026-10-28T10:00:00Z"], [null, "2026-10-28T09:00:00Z"]].map(([project_key, created_at], i) => ({ id: `r${i}`, client: "zcode", session_id: "s1", project_key, created_at, items: [{ memory: { id: "m1" }, rank: 1, score: 0.5, lanes: ["vector"] }] })) }));
  await openWindow({ key: "recall" }); await app.settle(50); d = win();
  assert.deepEqual([...d.querySelectorAll(".ov-wide-row .ov-win-item-title")].map(text), ["ZCode · 세션 s1"]);
  assert.match(text(d.querySelector(".ov-wide-row small")), /^저장소 없는 폴더, 프로젝트 밖$/);
  off(); off = route("GET", "/v1/recalls", () => fail(500, "boom"));
  await openWindow({ key: "recall" }); await app.settle(50); d = win();
  assert.match(text(d), /불러오기 기록을 읽지 못했어요/);
  off();
  assert.equal(d.querySelector(".ov-win-foot button"), null, "no jump to 사용량 from here");
  await closeAll();
});

test("overview: 승인 대기 window searches, sorts, filters, batch-approves and decides", async () => {
  const merge = { id: "mpx", revision: 3, merge: { proposal: { action: "merge", candidate: { id: "c3" }, targets: [{ id: "m1" }] } } };
  const offMerge = route("GET", "/v1/merge_proposals", () => ok({ items: [merge, { id: "skip", merge: { proposal: { action: "keep" } } }] }));
  await openWindow({ wide: "review" });
  await app.settle(50);
  let d = win();
  assert.match(text(d), /확인해 주세요/);
  // sort and search go to the server query
  const sorter = d.querySelector("select.ov-wide-sort");
  for (const v of ["new", "risk", "old"]) { sorter.value = v; app.dom.fire(sorter, "change"); await app.settle(20); }
  const search = d.querySelector(".ov-wide-search");
  app.dom.type(search, "Orbit"); await app.settle(30);
  assert.ok(app.apiCalls.some(([, p, b]) => p === "/v1/memory_candidates/query" && /Orbit/.test(String(b))));
  app.dom.type(search, ""); await app.settle(30);
  // one kind at a time (no mixed 전체 list): 새 기억 확인, 서로 다른 기억, 정리 제안; a state with nothing in it has no chip
  assert.ok(![...d.querySelectorAll(".ov-wide-chip")].some((c) => text(c).startsWith("전체")), "no 전체 chip");
  for (const label of ["서로 다른 기억", "정리 제안", "새 기억 확인"]) {
    const chip = [...d.querySelectorAll(".ov-wide-chip")].find((c) => text(c).startsWith(label)); assert.ok(chip, label);
    chip.click(); await app.settle(30);
  }
  assert.ok([...d.querySelectorAll(".ov-wide-chip")].every((c) => !/^\D+ 0$/.test(text(c))), "no chip counts 0");
  // the kind lives in the 필터 bubble beside the sort order, not a second chip row
  assert.ok(![...d.querySelectorAll(".ov-wide-chip")].some((c) => text(c).startsWith("모든 종류")));
  const filt = btn("필터 ⌄", d), pop = d.querySelector(".ov-filter-pop");
  assert.ok(filt && pop.hidden && d.querySelector(".ov-filter").previousSibling === sorter, "the 필터 button sits next to the sort, closed");
  filt.click(); assert.ok(!pop.hidden); assert.match(text(pop), /종류/);
  d.click(); assert.ok(pop.hidden, "a click outside closes it");
  filt.click(); app.dom.fire(d, "keydown", { key: "Escape" }); assert.ok(pop.hidden && d.open, "Esc closes the bubble, not the window");
  filt.click();
  const kindChip = pop.querySelector(".ov-wide-chip"); assert.ok(kindChip, "a kind to pick");
  assert.ok([...pop.querySelectorAll(".ov-wide-chip")].every((c) => !/ 0$/.test(text(c))), "no kind counts 0");
  const kindName = text(kindChip).replace(/ [\d,]+$/, "");
  kindChip.click(); await app.settle(30);
  assert.ok(app.apiCalls.some(([, p, b]) => p === "/v1/memory_candidates/query" && /"kind":"[a-z_]+"/.test(String(b))));
  assert.ok(btn("필터 1 ⌄", win()), "the button counts the active filter");
  assert.ok(!win().querySelector(".ov-filter-pop").hidden, "the bubble stays open while picking");
  [...win().querySelectorAll(".ov-filter-pop .ov-wide-chip")].find((c) => text(c).startsWith(kindName)).click(); await app.settle(30);
  assert.ok(btn("필터 ⌄", win()), "picking it again clears it");
  // rows pick the detail; a checkbox counts toward the batch button
  const rows = [...d.querySelectorAll(".ov-wide-row:not(.ov-wide-row-dream)")];
  assert.ok(rows.length > 2);
  rows[1].click(); await app.settle();
  const cb = win().querySelectorAll(".ov-wide-row input[type=checkbox]");
  cb[0].click(); cb[0].checked = true; app.dom.fire(cb[0], "change");
  cb[1].checked = true; app.dom.fire(cb[1], "change");
  cb[1].checked = false; app.dom.fire(cb[1], "change");
  const batch = btn("선택 1개 승인", win()); assert.ok(batch && !batch.disabled);
  batch.click(); await app.settle(80);
  assert.match(text(win()), /확인해 주세요/);
  // a batch where the server refuses says how many failed
  d = win();
  const cb2 = d.querySelector(".ov-wide-row input[type=checkbox]");
  cb2.checked = true; app.dom.fire(cb2, "change");
  const offRev = route("POST", "/v1/memory_candidates/", (a) => a.path.endsWith("/review") ? fail(500, "boom") : null);
  const calls = app.apiCalls.length;
  btn("선택 1개 승인", d).click(); await app.settle(80);
  // the failure count is written to the footer, which the page load that follows redraws
  assert.ok(app.apiCalls.slice(calls).some(([m, p]) => m === "POST" && p.endsWith("/review")));
  assert.match(text(win()), /확인해 주세요/);
  offRev();
  offMerge();
  await closeAll();
});

test("overview: 승인 대기 keeps the edited text across redraws, as the next AI base and in the heading", async () => {
  const kit = app.window.ArkismAIChat.kit, asked = [];
  let answer = "코드를 수정한 뒤에는 꼭 관련 테스트를 실행한다";
  app.window.ArkismAIChat.kit = { assist: async (_k, prompt) => { asked.push(prompt); return { status: "done", output: JSON.stringify({ value: answer }) }; }, assistError: () => "x", parseJSON: JSON.parse };
  try {
    let d = await reviewOf("코드 수정 후");
    const heading = () => text(win().querySelector(".ov-wide-detail h3")), box = () => win().querySelector(".ov-detail-edit");
    assert.equal(heading(), "코드 수정 후 관련 테스트를 실행한다.");
    // the AI proposal is a preview: the box keeps the original and the heading does not move
    const original = box().value;
    d.querySelector(".ex-ai-spark").click(); await app.settle(30);
    assert.equal(box().value, original);
    assert.equal(heading(), "코드 수정 후 관련 테스트를 실행한다.");
    assert.ok(win().querySelector(".ex-ai-diff ins"), "the proposal shows as a diff beside the box");
    // 취소 drops only the preview
    await press("취소", win().querySelector(".ex-ai-note"));
    assert.equal(box().value, original);
    assert.equal(win().querySelector(".ex-ai-diff").hidden, true);
    // 저장 puts it in the box, and the heading now shows what 승인 would save
    win().querySelector(".ex-ai-spark").click(); await app.settle(30);
    await press("저장", win().querySelector(".ex-ai-note"));
    assert.equal(box().value, answer);
    assert.equal(heading(), answer);
    // a redraw (another row, then back) keeps the saved text in the box and the heading
    const rows = () => [...win().querySelectorAll(".ov-wide-row:not(.ov-wide-row-dream)")];
    rows().find((r) => !text(r).includes("코드 수정 후")).click(); await app.settle();
    rows().find((r) => text(r).includes("코드 수정 후")).click(); await app.settle();
    assert.equal(box().value, answer);
    assert.equal(heading(), answer);
    // the next AI run starts from that saved text, so its diff is against it
    const first = answer; answer = "코드를 수정한 뒤에는 반드시 관련 테스트를 실행한다";
    win().querySelector(".ex-ai-spark").click(); await app.settle(30);
    assert.match(asked.at(-1), new RegExp(`Current text of the field:\\n${first}\\n`));
    assert.deepEqual([...win().querySelectorAll(".ex-ai-diff del")].map(text), ["꼭"]);
    await press("저장", win().querySelector(".ex-ai-note"));
    assert.equal(heading(), answer);
    // 취소 drops the draft: the heading is the candidate's own text again
    await press("취소", win().querySelector(".ov-detail-actions"));
    assert.equal(heading(), "코드 수정 후 관련 테스트를 실행한다.");
  } finally { app.window.ArkismAIChat.kit = kit; await closeAll(); }
});

test("overview: 승인 대기 detail shows the AI judgement, merge, edit, reject and conflict paths", async () => {
  const merge = { id: "mpx", revision: 3, merge: { proposal: { action: "merge", candidate: { id: "c3" }, targets: [{ id: "m1" }] } } };
  const offMerge = route("GET", "/v1/merge_proposals", () => ok({ items: [merge] }));
  // c5: keep verdict with semantic checks; reclassify answers a waiting judgement
  let d = await reviewOf("Alex는 주로");
  assert.match(text(d), /그대로 저장 추천/);
  const again = d.querySelector(".ov-verdict-again");
  let offRe = route("POST", "/v1/memory_candidates/c5/reclassify", () => fail(500, "boom"));
  again.click(); await app.settle(30);
  assert.match(again.title, /다시 보게 하지 못했어요/);
  offRe(); offRe = route("POST", "/v1/memory_candidates/c5/reclassify", () => ok({ classification: { verdict: { action: "keep" }, by: "ai", at: ago(1) } }));
  again.click(); await app.settle(30);
  assert.match(text(win()), /다시 보는 중이에요/);
  offRe();
  // 세션 보기 opens the source session in the 세션 기록 window
  await reviewOf("Alex는 주로");
  await press("세션 보기 ›", win(), 50);
  assert.match(text(win().querySelector(".ov-win-title")), /세션 기록/);
  assert.ok(win().querySelector(".ov-wide-search").value, "searched for that session");
  // c6: fix verdict opens the editor; 취소 restores, 고친 내용으로 승인 saves
  d = await reviewOf("코드 수정 후");
  assert.match(text(d), /고쳐서 저장 추천/);
  await press("취소", d);
  await press("고쳐서 승인", d);
  d.querySelector(".ov-detail-edit").value = "코드를 고친 뒤 테스트를 돌린다";
  await press("고친 내용으로 승인", d, 60);
  assert.ok(app.apiCalls.some(([, p, b]) => /c6\/review$/.test(p) && /edit_approve/.test(String(b))));
  // c3 has a merge proposal: a stale proposal says so, then the accept goes through
  d = await reviewOf("외부 API");
  assert.match(text(d), /비교/);
  let offDec = route("POST", "/v1/merge_proposals/", () => fail(409, "revision_conflict"));
  await press("기존 기억에 합치기", d, 30);
  assert.match(text(d), /이 제안은 낡았어요/);
  offDec(); offDec = route("POST", "/v1/merge_proposals/", () => fail(500, "boom"));
  await press("기존 기억에 합치기", d, 30);
  assert.match(text(d), /합치지 못했어요/);
  offDec();
  await press("기존 기억에 합치기", d, 60);
  assert.ok(app.apiCalls.some(([, p]) => p === "/v1/merge_proposals/mpx/decide"));
  offMerge();
  // reject from the detail opens the reason picker; an error from the server stays on the picker
  d = await reviewOf("Orbit 버그 재현");
  const offRej = route("POST", "/v1/memory_candidates/c4/review", () => fail(500, "boom"));
  await press("버리기", d.querySelector(".ov-detail-actions"));
  let pick = win().querySelector(".del-reason"); assert.ok(pick, "reason picker opens in place");
  await press("버리기", pick, 30);
  assert.match(text(pick), /이유를 하나 골라 주세요/, "a reason is required");
  await press("중복", pick); await press("버리기", pick, 50);
  assert.match(text(win()), /처리하지 못했어요/);
  assert.ok(app.apiCalls.some(([, p, b]) => p === "/v1/memory_candidates/c4/review" && /"reason_code":"duplicate"/.test(String(b))), "the reason code goes with the reject");
  offRej();
  await press("그대로 승인", win(), 60);
  // a conflicted candidate is resolved here: the existing memory beside it, where each came from, and the 기억 screen's choices
  await reviewOf("출시일은"); await app.settle(50); d = win();
  assert.match(text(d), /충돌하는 두 기억/);
  assert.match(text(d), /출시일은 2026년 10월 15일이다/, "the existing memory in full");
  assert.equal(btn("전체 화면으로", d), undefined, "no jump to the full screen");
  assert.ok(d.querySelector(".ov-context .ss-msg"), "the source conversation lines");
  // AI 제안 lights one choice and says why; nothing runs until the person clicks
  const kit = app.window.ArkismAIChat.kit, reviews = () => app.apiCalls.filter(([, p]) => p === "/v1/memory_candidates/k1/review").length, before = reviews();
  app.window.ArkismAIChat.kit = { assist: async () => ({ status: "done", output: '{"pick":"keep_old","reason":"기존 날짜가 공지와 같아요."}' }), assistError: () => "x", parseJSON: JSON.parse };
  await press("AI 제안", d.querySelector(".ov-detail-actions"), 30);
  assert.ok(btn("기존 것 유지", d).classList.contains("primary") && !btn("새 내용으로 바꾸기", d).classList.contains("primary"));
  assert.match(text(d), /기존 날짜가 공지와 같아요/);
  assert.equal(reviews(), before, "advice only");
  app.window.ArkismAIChat.kit = { assist: async () => ({ status: "done", output: '{"pick":"maybe"}' }), assistError: () => "x", parseJSON: JSON.parse };
  await press("AI 제안", d.querySelector(".ov-detail-actions"), 30);
  assert.match(text(d), /AI 제안을 받지 못했어요/);
  app.window.ArkismAIChat.kit = kit;
  // a failed choice keeps the panel and its buttons usable
  const offConflict = route("POST", "/v1/memory_candidates/k1/review", () => fail(500, "boom"));
  await press("기존 것 유지", d.querySelector(".ov-detail-actions"), 80);
  offConflict(); d = win();
  assert.ok(btn("기존 것 유지", d) && !btn("기존 것 유지", d).disabled && !btn("새 내용으로 바꾸기", d).disabled, "the choices are usable again");
  await press("기존 것 유지", d.querySelector(".ov-detail-actions"), 80);
  assert.ok(app.apiCalls.some(([m, p, b]) => m === "POST" && p === "/v1/memory_candidates/k1/review" && /keep_old/.test(String(b))), "the conflict goes through resolveConflict");
  await closeAll();
});

test("overview: a candidate's 원문 근거 shows the lines around the quote, and 세션 보기 opens that spot", async () => {
  await reviewOf("Alex는 주로 오전에"); await app.settle(50); let d = win();
  const box = d.querySelector(".ov-context"); assert.ok(box, "context block");
  assert.match(text(box), /나는 보통 오전에 코드 리뷰해/);
  assert.equal(text(box.querySelector(".ss-msg mark.ss-hit")), "나는 보통 오전에 코드 리뷰해", "the quoted words are underlined in the conversation");
  assert.ok(box.querySelectorAll(".ss-msg").length <= 5, "only the lines around it");
  await press("세션 보기 ›", box, 80);
  assert.match(text(win()), /세션 기록/);
  assert.equal(win().querySelector(".ov-wide-search").value, "session_01JX8K2M1QWERTY");
  await closeAll();
});

test("overview: 정리 제안 decide accept, swap, dismiss and a stale proposal", async () => {
  await openWindow({ wide: "review", args: [null, "dream"] }); await app.settle(50);
  let d = win();
  assert.match(text(d), /비슷한 기억 2개/);
  assert.ok(d.querySelectorAll(".ov-wide-row-dream").length >= 2);
  let off = route("POST", "/v1/maintenance_proposals/", () => fail(409, "revision_conflict"));
  await press("제안대로 합치기", d); await press("이대로 적용", d, 30);
  assert.match(text(d), /이 제안은 낡았어요/);
  off(); off = route("POST", "/v1/maintenance_proposals/", () => fail(500, "boom"));
  await press("취소", d); await press("반대로 남기기", d); await press("이대로 적용", d, 30);
  assert.match(text(d), /처리하지 못했어요/);
  off();
  await press("취소", d); await press("둘 다 두기", d); await press("이대로 적용", d, 60);
  assert.ok(app.apiCalls.some(([, p]) => /\/v1\/maintenance_proposals\/.+\/decide/.test(p)));
  // the signal proposal (rule often broken) has its own two choices
  win() || await openWindow({ wide: "review", args: [null, "dream"] }); await app.settle(30); d = win();
  const sig = [...d.querySelectorAll(".ov-wide-row-dream")].find((r) => /지켜짐/.test(text(r)));
  assert.ok(sig, "signal proposal row");
  sig.click(); await app.settle();
  assert.match(text(win()), /자주 어겨요/);
  await press("괜찮아요", win(), 60);
  // the search box filters proposals too
  win() || await openWindow({ wide: "review", args: [null, "dream"] }); await app.settle(30); d = win();
  app.dom.type(d.querySelector(".ov-wide-search"), "없는말"); await app.settle(30);
  assert.match(text(win()), /지금 보여 줄 후보가 없어요/);
  assert.match(text(win()), /확인해 주세요/);
  await closeAll();
});

test("overview: a dreaming proposal's 지우기 picks which memory and why; 취소 sends nothing", async () => {
  await openWindow({ wide: "review", args: [null, "dream"] }); await app.settle(50);
  const d = win();
  assert.equal(btn("둘 다 지우기", d), undefined, "one 지우기 with a target instead");
  const rows = [...d.querySelectorAll(".ov-wide-row-dream")];
  for (const r of rows) { r.click(); await app.settle(); if (/서로 다른 말을 하는 기억/.test(text(win()))) break; }
  assert.match(text(win()), /서로 다른 말을 하는 기억/);
  assert.match(text(win()), /넘기거나 지운 기억은 기억 이력에 남아요/);
  const sent = () => app.apiCalls.filter(([m, p, b]) => m === "POST" && /\/v1\/maintenance_proposals\/.+\/decide$/.test(p) && /"forget_both"/.test(String(b)));
  const before = sent().length;
  await press("지우기", win());
  let pick = win().querySelector(".del-reason"); assert.match(text(pick), /넘길 기억남길 기억둘 다/);
  await press("취소", pick, 30);
  assert.equal(win().querySelector(".del-reason"), null, "cancel closes the picker");
  assert.equal(sent().length, before, "cancel sends nothing");
  await press("지우기", win());
  pick = win().querySelector(".del-reason");
  await press("둘 다", pick); await press("사실과 다름", pick);
  pick.querySelector("textarea").value = "둘 다 예전 일정";
  await press("지우기", pick, 60);
  assert.equal(sent().length, before + 1);
  assert.match(String(sent().at(-1)[2]), /"reason_code":"wrong".*"reason":"둘 다 예전 일정"/);
  await closeAll();
});

test("overview: each pair choice previews its result and sends nothing before 이대로 적용", async () => {
  await openWindow({ wide: "review", args: [null, "dream"] }); await app.settle(50);
  for (const r of [...win().querySelectorAll(".ov-wide-row-dream")]) { r.click(); await app.settle(); if (/서로 다른 말을 하는 기억/.test(text(win()))) break; }
  const sent = () => app.apiCalls.filter(([m, p]) => m === "POST" && /\/v1\/maintenance_proposals\/.+\/decide$/.test(p)).length;
  const side = (cls) => [...win().querySelectorAll(`.ov-cmp.${cls} span`)].map(text);
  const [keep, retire] = [side("diff-add")[0], side("diff-del")[0]];
  const before = sent();
  for (const [label, add, del] of [["제안대로 바꾸기", [keep], [retire]], ["반대로 남기기", [retire], [keep]], ["둘 다 두기", [keep, retire], []]]) {
    await press(label, win(), 30);
    assert.deepEqual([side("diff-add"), side("diff-del")], [add, del], label);
    assert.ok(btn("이대로 적용", win()), label);
    await press("취소", win(), 30);
    assert.deepEqual([side("diff-add"), side("diff-del")], [[keep], [retire]], `${label} 취소 puts the proposal back`);
  }
  assert.equal(sent(), before, "nothing is sent before 이대로 적용");
  await press("반대로 남기기", win()); await press("이대로 적용", win(), 60);
  const last = app.apiCalls.filter(([m, p]) => m === "POST" && /\/decide$/.test(p)).pop();
  assert.equal(JSON.parse(last[2]).decision, "swap");
  await closeAll();
});

test("overview: an older server's invalid_request on 둘 다 지우기 reads as plain Korean", async () => {
  const off = route("POST", "/v1/maintenance_proposals/", () => fail(400, "invalid_request"));
  await openWindow({ wide: "review", args: [null, "dream"] }); await app.settle(50);
  for (const r of [...win().querySelectorAll(".ov-wide-row-dream")]) { r.click(); await app.settle(); if (/서로 다른 말을 하는 기억/.test(text(win()))) break; }
  await press("지우기", win());
  const pick = win().querySelector(".del-reason");
  await press("둘 다", pick); await press("사실과 다름", pick);
  await press("지우기", pick, 60);
  off();
  assert.match(text(win()), /서버를 최신 판으로 올린 뒤/);
  assert.doesNotMatch(text(win()), /invalid_request/);
  await closeAll();
});

test("overview: 쓰는 중인 기억 window filters, picks, edits and deletes", async () => {
  await openWindow({ wide: "active" }); await app.settle(50); let d = win();
  assert.match(text(d), /쓰는 중인 기억/);
  const sorter = d.querySelector("select.ov-wide-sort");
  for (const v of ["new", "old", "used"]) { sorter.value = v; app.dom.fire(sorter, "change"); await app.settle(20); }
  app.dom.type(d.querySelector(".ov-wide-search"), "Orbit"); await app.settle(30);
  app.dom.type(d.querySelector(".ov-wide-search"), ""); await app.settle(30);
  for (const label of ["결정", "안 쓰임", "전체"]) { [...d.querySelectorAll(".ov-wide-chip")].find((c) => text(c).startsWith(label)).click(); await app.settle(30); }
  const rows = d.querySelectorAll(".ov-wide-row");
  rows[2].click(); await app.settle();
  assert.ok(rows[2].classList.contains("on") || win().querySelector(".ov-wide-row.on"));
  // 지우기 asks why first; 취소 keeps it, a failure says why, then it is rejected with the reason (kept in history)
  const removed = () => app.apiCalls.filter(([m, p, b]) => m === "POST" && /^\/v1\/memories\/[^/]+\/transition$/.test(p) && /"status":"rejected"/.test(String(b)));
  const was = removed().length;
  await press("지우기", win().querySelector(".ov-detail-actions"));
  let pick = win().querySelector(".del-reason"); await press("취소", pick);
  assert.equal(removed().length, was, "cancel sends nothing");
  const off = route("POST", "/v1/memories/", () => fail(500, "boom"));
  await press("지우기", win().querySelector(".ov-detail-actions"));
  pick = win().querySelector(".del-reason"); await press("오래된 기억", pick); await press("지우기", pick, 50);
  assert.match(text(win()), /지우지 못했어요/);
  off();
  await press("지우기", pick, 80);
  assert.ok(removed().length > was && /"reason_code":"stale"/.test(String(removed().at(-1)[2])));
  assert.ok(!app.apiCalls.some(([m, p]) => m === "DELETE" && p.startsWith("/v1/memories/")), "no erasure");
  // 세션 보기 on a memory with a source, 고치기 opens it in the memory list
  win() || await openWindow({ wide: "active" }); await app.settle(30); d = win();
  const withSource = [...d.querySelectorAll(".ov-wide-row")].find((r) => { r.click(); return btn("세션 보기 ›", win()); });
  if (withSource) await press("세션 보기 ›", win());
  await openWindow({ wide: "active" }); await app.settle(50);
  const editor = () => app.$$("dialog[open]").find((x) => /기억 고치기/.test(text(x)));
  await press("고치기", win().querySelector(".ov-detail-actions"), 50);
  assert.ok(win() && editor(), "고치기 opens the edit popup over the window");
  editor().close(); await app.settle();
  // 쓰임 확인 keeps, fixes or forgets an unused memory in its own row (the same confirm and DELETE as here)
  d = await openWindow({ key: "judge" });
  assert.equal(btn("정리하러 가기", d), undefined);
  assert.match(text(d), /답에 안 쓰인 기억/);
  const forgets = removed().length;
  await press("잊기", d.querySelector(".ov-win-list .ov-win-item"));
  await press("잊기", win().querySelector(".del-reason"), 80);
  assert.ok(removed().length > forgets && /"reason_code":"unused"/.test(String(removed().at(-1)[2])), "잊기 asks why (사용하지 않음 chosen), then removes");
  d = await openWindow({ key: "judge" });
  await press("고치기", d.querySelector(".ov-win-list .ov-win-item"), 50);
  assert.ok(win() && editor(), "고치기 opens the edit popup over 쓰임 확인");
  editor().close(); await app.settle();
  const before = win().querySelectorAll(".ov-win-list .ov-win-item").length;
  await press("맞아요, 유지", win().querySelector(".ov-win-list .ov-win-item"));
  assert.equal(win().querySelectorAll(".ov-win-list .ov-win-item").length, before - 1, "유지 takes the row off the list");
  d = await openWindow({ key: "judge" });
  assert.equal(d.querySelectorAll(".ov-win-list .ov-win-item").length, before - 1, "a kept memory is not asked about again");
  await closeAll();
});

test("overview: 세션 기록 window pages, filters, deletes and links candidates", async () => {
  const fresh = new Date(NOW - 60000).toISOString();
  const rows = [
    { id: "w1", agent: "codex", session_id: "session_01JX8K2M1QWERTY", title: "후보 있는 세션", project: "arkism", started_at: ago(3), ended_at: ago(2), messages: 10, active_seconds: 600, tokens: 1200, requests: 4, device: "my-pc" },
    { id: "w2", agent: "claude_code", session_id: "fresh-1", project: "home", started_at: fresh, ended_at: fresh, messages: 3 },
    { id: "w3", agent: "claude_code", session_id: "old-1", title: "", started_at: ago(50), messages: 1 }
  ];
  const page = (items, next = "") => ok({ authority: "data_only_untrusted", items, next_cursor: next, total: 4 });
  const off = route("GET", "/v1/usage/sessions", (a) => /cursor=p2/.test(a.path) ? page([{ id: "w4", agent: "codex", session_id: "s4", started_at: ago(5), messages: 2 }]) : page(rows, /sort=recent/.test(a.path) ? "p2" : ""));
  await press("새로고침", app.document, 50).catch(() => {});
  await openWindow({ wide: "sessions" }); await app.settle(50); let d = win();
  assert.match(text(d), /후보 있는 세션/);
  assert.match(text(d), /분석 대기/);
  assert.match(text(d), /후보 없음/);
  await press("더 보기", d, 50);
  assert.equal(d.querySelectorAll(".ov-wide-row").length, 4);
  for (const label of ["Codex", "전체"]) { [...d.querySelectorAll(".ov-wide-chip")].find((c) => text(c).startsWith(label)).click(); await app.settle(30); }
  app.dom.type(d.querySelector(".ov-wide-search"), "후보"); await app.settle(30);
  app.dom.type(d.querySelector(".ov-wide-search"), ""); await app.settle(30);
  d.querySelectorAll(".ov-wide-row")[1].click(); await app.settle();
  d.querySelectorAll(".ov-wide-row")[0].click(); await app.settle();
  assert.match(text(d), /이 세션의 흐름/);
  // 내보내기: one session as Markdown or JSON, a failure says so and the button works again
  const made = [], real = [URL.createObjectURL, URL.revokeObjectURL];
  URL.createObjectURL = (b) => { made.push(b); return "blob:t"; }; URL.revokeObjectURL = () => {};
  let offExp = route("POST", "/v1/usage/sessions/export", (a) => ok({ content: "# s", filename: JSON.parse(a.body).format === "json" ? "s.json" : "s.md" }));
  await press("Markdown 내보내기", d, 50);
  const exp = app.apiCalls.filter(([, p]) => p === "/v1/usage/sessions/export").pop();
  assert.equal(JSON.parse(exp[2]).ids.length, 1); assert.equal(JSON.parse(exp[2]).format, "markdown");
  assert.equal(made.length, 1); assert.match(text(d), /세션 다운로드를 시작했어요/);
  offExp(); offExp = route("POST", "/v1/usage/sessions/export", () => fail(500, "boom"));
  await press("JSON", d, 50);
  assert.match(text(d), /내보내지 못했어요/); assert.ok(!btn("JSON", d).disabled);
  // a repaint while the export runs (대화 보기) keeps it busy, and its failure still shows
  offExp(); let answer; offExp = route("POST", "/v1/usage/sessions/export", () => new Promise((r) => { answer = r; }));
  btn("Markdown 내보내기", d).click(); await app.settle(0, 20);
  btn("대화 보기", d).click(); await app.settle(0, 20);
  assert.ok(btn("JSON", d).disabled && btn("Markdown 내보내기", d).disabled); assert.match(text(d), /내보내는 중/);
  answer(fail(500, "late")); await app.settle(30);
  assert.match(text(d), /내보내지 못했어요/); assert.ok(!btn("JSON", d).disabled);
  offExp(); [URL.createObjectURL, URL.revokeObjectURL] = real;
  // 세션 지우기: denied, failed, then done
  let offDel = route("POST", "/v1/usage/sessions/delete", () => fail(403, "access_denied"));
  await press("세션 지우기", d); await press("지우기", app.$("dialog.ov-ask[open]"), 50);
  assert.match(text(win()), /지울 권한이 없어요/);
  offDel(); offDel = route("POST", "/v1/usage/sessions/delete", () => fail(500, "boom"));
  await press("세션 지우기", win()); await press("지우기", app.$("dialog.ov-ask[open]"), 50);
  assert.match(text(win()), /지우지 못했어요/);
  offDel();
  await press("세션 지우기", win()); await press("지우기", app.$("dialog.ov-ask[open]"), 80);
  assert.match(text(win()), /세션 기록/);
  // a waiting candidate links to 승인 대기; 새로고침 reads the list again
  const cand = win().querySelector(".ov-detail-cand.link");
  if (cand) { cand.click(); await app.settle(50); assert.match(text(win()), /확인해 주세요/); }
  await openWindow({ wide: "sessions" }); await app.settle(50);
  await press("새로고침", win(), 50);
  // 대화 보기 turns the window into the dialogue: the person on the right, the agent by name, each message's own time; ‹ 세션 정보 goes back
  win().querySelector(".ov-wide-row").click(); await app.settle();
  await press("대화 보기", win(), 50);
  assert.match(text(win()), /리뷰 일정 좀 같이 잡아 줘/);
  assert.ok(win().querySelector(".ss-body.ss-talk:not(.ss-solo)"), "the dialogue takes the window");
  const msgs = [...win().querySelectorAll(".ss-msg")], label = msgs.find((m) => m.classList.contains("assistant"))?.querySelector("b");
  assert.ok(msgs[0].classList.contains("user") && label && !/에이전트/.test(text(label)), "roles told apart, the agent by its name");
  const times = msgs.map((m) => text(m.querySelector("time")));
  assert.match(times[0], /월 .*\d\d:\d\d$/, "the first message carries its day");
  assert.match(times[1], /^\d\d:\d\d$/, "the same day again shows only the time");
  assert.ok(times.some((t, i) => i > 1 && /월/.test(t)), "a new day shows the day again");
  assert.ok(times.includes(""), "a message without a recorded time shows none");
  await press("‹ 세션 정보", win());
  assert.equal(win().querySelector(".ss-msg"), null);
  assert.ok(win().querySelector(".ss-body.ss-solo .ss-detail"), "back on the session's details");
  // a failed page offers 다시 시도
  off();
  const offBad = route("GET", "/v1/usage/sessions", () => fail(500, "boom"));
  await openWindow({ wide: "sessions" }); await app.settle(50);
  assert.match(text(win()), /불러오지 못했어요/);
  offBad();
  await press("다시 시도", win(), 50);
  await closeAll();
});

test("overview: 세션 기록 opened on one session underlines the quote, pages the dialogue and says when it is missing", async () => {
  const rows = [{ id: "w1", agent: "codex", session_id: "sid-a", title: "다른 세션", started_at: ago(3), messages: 4 }, { id: "w2", agent: "codex", session_id: "sid-b", title: "찾는 세션", started_at: ago(30), messages: 4 }];
  const off = route("GET", "/v1/usage/sessions", (a) => ok({ authority: "data_only_untrusted", items: /q=sid-b/.test(a.path) && /range=all/.test(a.path) ? rows : [], next_cursor: "", total: 2 }));
  const page = (items, next) => ok({ authority: "data_only_untrusted", state: "active", items, next_cursor: next, archive_id: "a1" });
  let flaky = true; // the third page fails once: its cursor stays, so 메시지 더 보기 can try again
  const offTalk = route("POST", "/v1/usage/sessions/transcript", (a) => /"after":"c2"/.test(a.body) ? (flaky ? (flaky = false, fail(500, "boom")) : page([{ role: "user", text: "세 번째 쪽" }], ""))
    : /"after":"c1"/.test(a.body) ? page([{ role: "assistant", text: "그 말에 맞춰 둘게요." }], "c2") : page([{ role: "user", text: "앞으로 커밋은 영어로 써 줘." }], "c1"));
  // the quote sits on the second page: the window reads on until it finds it, underlined
  await openWindow({ wide: "sessions", args: ["sid-b", "그 말에 맞춰"] }); await app.settle(80); let d = win();
  assert.match(text(d.querySelector(".ov-wide-row.on")), /찾는 세션/, "the asked-for session is picked, not the first row");
  assert.equal(text(d.querySelector("mark.ss-hit")), "그 말에 맞춰");
  assert.equal(d.querySelectorAll(".ss-msg").length, 2);
  await press("메시지 더 보기", d, 50);
  assert.ok(app.apiCalls.some(([, p, b]) => p === "/v1/usage/sessions/transcript" && /"after":"c2"/.test(String(b))));
  assert.match(text(win()), /대화를 불러오지 못했어요/);
  await press("메시지 더 보기", win(), 50);
  assert.match(text(win()), /그 말에 맞춰 둘게요[\s\S]*세 번째 쪽/);
  // a dialogue that cannot be read says so
  offTalk(); const offBad = route("POST", "/v1/usage/sessions/transcript", () => fail(500, "boom"));
  d.querySelectorAll(".ov-wide-row")[0].click(); await app.settle();
  await press("대화 보기", win(), 50);
  assert.match(text(win()), /대화를 불러오지 못했어요/);
  offBad();
  // a session the search does not find is named missing, and no other row is picked for it
  await openWindow({ wide: "sessions", args: ["sid-gone"] }); await app.settle(80); d = win();
  assert.match(text(d), /이 세션을 찾지 못했어요/);
  assert.equal(d.querySelector(".ov-wide-detail").children.length, 0);
  off(); await closeAll();
});

test("overview: 세션 기록 copies resume and continue-in commands, counts agents and finds words in the dialogue", async () => {
  const rows = [
    { id: "h1", agent: "codex", session_id: "cx-1", title: "배포 고치기", project: "arkism", folder: "/home/me/arkism", file: "/home/me/.codex/r.jsonl", started_at: ago(2), ended_at: ago(1), messages: 6, tokens: 3000, input: 1000, output: 500, cache_read: 1500, priced: true, cost_usd: 0.42, models: [{ name: "gpt-x", tokens: 3000, priced: true, cost_usd: 0.42 }] },
    { id: "h2", agent: "claude_code", session_id: "cc-1", title: "", started_at: ago(3), messages: 2 }
  ];
  const offList = route("GET", "/v1/usage/sessions", () => ok({ authority: "data_only_untrusted", items: rows, next_cursor: "", total: 7, facets: { agents: { codex: 5, claude_code: 2 }, projects: { arkism: 5 } } }));
  const offHand = route("POST", "/v1/usage/sessions/handoff", () => ok({ authority: "data_only_untrusted", id: "h1", agent: "codex", session_id: "cx-1", folder: "/home/me/arkism", work: { id: "wk-1", title: "배포 고치기" }, archive_id: "arc-1" }));
  const offFind = route("POST", "/v1/usage/sessions/search", () => ok({ authority: "data_only_untrusted", items: [{ session: rows[0], hits: [{ role: "assistant", snippet: "…스크립트를 고쳤어요", match: 3 }] }], hidden: 1, secret_records: 0 }));
  const offTalk = route("POST", "/v1/usage/sessions/transcript", () => ok({ authority: "data_only_untrusted", state: "active", items: [{ role: "assistant", text: "배포 스크립트를 고쳤어요" }], next_cursor: "" }));
  await openWindow({ wide: "sessions" }); await app.settle(50); let d = win();
  // one list, newest first; the chips carry the server's counts and the project is only a filter
  assert.deepEqual([...d.querySelectorAll(".ov-wide-chip")].map(text), ["전체 7", "Codex 5", "Claude Code 2"]);
  assert.equal(d.querySelectorAll(".ss-row").length, 2);
  assert.match(text(d), /제목 없음[\s\S]*토큰 기록 없음/);
  // 재개 copies the session's own command; nothing runs
  await press("이어서 하기", d.querySelector(".ss-row"), 20);
  assert.equal(app.clipboard.text, "cd /home/me/arkism; codex resume cx-1");
  // handoff ▾ lists the other agents seen here, reads the handoff and copies that agent's start command
  await press("넘기기", d.querySelector(".ss-row"));
  assert.deepEqual([...d.querySelectorAll(".ss-menu .cat-menu-item")].map(text), ["Claude Code(으)로 넘기기"]);
  await press("Claude Code(으)로 넘기기", d, 30);
  assert.match(app.clipboard.text, /^cd \/home\/me\/arkism; claude 'Arkism resume_work 도구로 작업 wk-1를/);
  assert.match(text(win()), /Claude Code 시작 명령을 복사했어요/);
  assert.ok(!app.calls.some((c) => /spawn|execute|run_command/.test(String(c?.[0] ?? c))), "no command is run");
  // the row opens its facts in place
  win().querySelector(".ov-wide-row").click(); await app.settle(); d = win();
  assert.match(text(d.querySelector(".ss-facts")), /폴더\/home\/me\/arkism[\s\S]*세션 IDcx-1[\s\S]*이어서 하기 명령cd \/home\/me\/arkism; codex resume cx-1[\s\S]*gpt-x[\s\S]*파일\/home\/me\/.codex\/r.jsonl/);
  // a session without folder or tokens says 기록 없음 instead of guessing
  d.querySelectorAll(".ov-wide-row")[1].click(); await app.settle(); d = win();
  assert.match(text(d.querySelector(".ss-facts")), /폴더기록 없음[\s\S]*토큰기록 없음/);
  // 대화에서 찾기: hits grouped under their session, a hit opens the dialogue at the words
  app.dom.type(d.querySelector(".ov-wide-search"), "스크립트"); await app.settle();
  await press("대화에서 찾기", d, 50); d = win();
  const call = app.apiCalls.filter(([, p]) => p === "/v1/usage/sessions/search").pop();
  assert.equal(JSON.parse(call[2]).q, "스크립트");
  assert.equal(text(d.querySelector(".ss-hit-line mark.ss-hit")), "스크립트");
  assert.match(text(d), /비공개 부분에만 있어 뺀 메시지 1개/);
  d.querySelector(".ss-hit-line").click(); await app.settle(50); d = win();
  assert.equal(text(d.querySelector(".ov-wide-right mark.ss-hit")), "스크립트");
  await press("목록으로", d);
  assert.equal(win().querySelectorAll(".ss-hit-line").length, 0);
  offList(); offHand(); offFind(); offTalk(); await closeAll();
});

test("overview: 사용량 window draws the counts and its two buttons", async () => {
  await openWindow({ wide: "usage" }); await app.settle(30); let d = win();
  assert.match(text(d), /최근 사용 기록/);
  assert.match(text(d), /많이 쓰인 기억/);
  await press("잘 쓰이나 확인 보기", d);
  assert.match(text(win()), /잘 쓰이나 확인/);
  d = await openWindow({ wide: "usage" });
  await press("사용량 화면으로", d, 50);
  assert.equal(win(), null);
});

// A2: /v1/memory_usage timing out (503) left every recall count at 0 beside a 7-day preview of 2,090 injections.
test("overview: 사용량 window shows — and says why when the use counts did not load", async () => {
  let asked = 0;
  // reloads every list and waits until the use counts were asked for (they load after the rest)
  const reload = async () => { const n = asked; app.dom.fire(app.window, "storage", { key: "arkism.popout-saved" }); for (let i = 0; i < 100 && asked === n; i++) await app.settle(100); await app.settle(100); };
  let off = route("GET", "/v1/memory_usage", () => { asked++; return fail(503, "unavailable"); });
  await reload();
  await openWindow({ wide: "usage" }); await app.settle(30); let d = win();
  const tiles = [...d.querySelectorAll(".ov-win-tile")].map(text);
  assert.ok(tiles.some((x) => /^—\s*7일 불러온 횟수/.test(x)), tiles.join(" | "));
  assert.ok(tiles.some((x) => /^—\s*불러온 기억/.test(x)), tiles.join(" | "));
  assert.doesNotMatch(text(d), /0회/);
  assert.match(text(d), /서버가 불러오기 집계를 주지 못했어요/);
  off(); off = route("GET", "/v1/memory_usage", () => { asked++; return ok({ items: [{ id: "m1", d7: 4, d30: 9, last: ago(2) }] }); });
  await reload();
  await openWindow({ wide: "usage" }); await app.settle(30); d = win();
  assert.match(text(d), /4회\s*7일 불러온 횟수/);
  assert.doesNotMatch(text(d), /집계를 주지 못했어요/);
  off();
  // opened while the counts are still on the way: it says so, then shows the failure without reopening
  let release; off = route("GET", "/v1/memory_usage", () => { asked++; return new Promise((r) => { release = r; }); });
  await closeAll(); await reload();
  d = await openWindow({ wide: "usage" });
  assert.match(text(d), /불러오는 중…/);
  assert.doesNotMatch(text(d), /지금 보여 줄 항목이 없어요/);
  release(fail(503, "unavailable")); await app.settle(50);
  assert.match(text(win()), /서버가 불러오기 집계를 주지 못했어요/);
  off();
  await closeAll();
});

test("overview: 기억 지도 picks a memory, follows its links and filters", async () => {
  const graph = {
    nodes_truncated: true, edges_truncated: true,
    nodes: [
      { id: "m1", label: "Orbit 백엔드", kind: "decision", reach: "project", uses: 3, status: "active" },
      { id: "m2", kind: "procedural", reach: "general", status: "superseded" },
      { id: "m3", label: "세 번째", kind: "lesson", reach: "personal" },
      { id: "m4", label: "네 번째", kind: "lesson" },
      { id: "m5", label: "다섯", kind: "user", reach: "personal" },
      { id: "gone", label: "없는 기억", kind: "fact", reach: "general" }
    ],
    edges: [
      { from: "m1", to: "m2", type: "supports" }, { from: "m1", to: "m3", type: "human_link" }, { from: "m4", to: "m1", type: "conflicts" },
      { from: "m1", to: "m5", type: "supersedes" }, { from: "m1", to: "gone", type: "similar" }, { from: "gone", to: "m3", type: "same_session" }
    ]
  };
  const offG = route("GET", "/v1/memory-graph", () => ok(graph));
  const offT = route("GET", "/v1/digests", () => ok({ items: [{ id: "t1", title: "배포 주제", members: [{ id: "m1" }, { id: "m3" }] }, { id: "t2", members: [{ id: "m4" }] }] }));
  const offM = route("GET", "/v1/memories/gone", () => fail(404, "not_found"));
  let d = await openWindow({ wide: "map" }); await app.settle(50); d = win();
  assert.match(text(d), /지도 요약/);
  assert.match(text(d), /기억이 많아 6개까지만 그렸어요 · 선이 많아 일부 선은 빠졌어요/);
  // keyboard on the canvas picks the first dot, Enter opens it
  const canvas = d.querySelector("canvas");
  canvas.focus?.();
  app.dom.key(canvas, "ArrowRight"); app.dom.key(canvas, "Enter"); await app.settle(30);
  assert.match(text(d), /기억 상세/);
  // walk to m1 through the related list until its four links show, then 나머지 펼치기
  const toM1 = () => [...d.querySelectorAll(".ov-map-rel")].find((r) => /Orbit 백엔드/.test(text(r)));
  for (let i = 0; i < 4 && !/나머지/.test(text(d)); i++) { const r = toM1() || d.querySelector(".ov-map-rel"); if (!r) break; r.click(); await app.settle(30); }
  assert.match(text(d), /나머지 1개 펼치기/);
  await press("나머지 1개 펼치기", d); await press("접기", d);
  assert.match(text(d), /AI 관계 제안/);
  // 선택한 연결 / 전체 관계, the zoom buttons, the tabs
  await press("선택한 연결", d); await press("전체 관계", d);
  for (const l of ["확대", "축소", "화면에 맞추기"]) await press(l, d);
  await press("전체 지도", d); await press("선택한 기억", d);
  // the unreadable memory says so; ✕ clears the pick
  const gone = [...d.querySelectorAll(".ov-map-rel")].find((r) => /없는 기억/.test(text(r)));
  if (gone) { gone.click(); await app.settle(30); }
  await press("닫기", d.querySelector(".ov-map-sidehead"));
  assert.match(text(d), /지도 요약/);
  // topics filter by click and by keyboard; reach chips toggle; a search with no hit offers 조건 지우기
  const topic = [...d.querySelectorAll(".ov-win-click")].find((r) => /배포 주제/.test(text(r)));
  topic.click(); await app.settle();
  app.dom.key([...d.querySelectorAll(".ov-win-click")].find((r) => /배포 주제/.test(text(r))), "a");
  app.dom.key([...d.querySelectorAll(".ov-win-click")].find((r) => /배포 주제/.test(text(r))), "Enter"); await app.settle();
  app.dom.key([...d.querySelectorAll(".ov-win-click")].find((r) => /배포 주제/.test(text(r))), " "); await app.settle();
  for (const c of [...d.querySelectorAll(".ov-map-chip")].slice(0, 3)) { c.click(); await app.settle(); }
  const search = d.querySelector(".ov-wide-search");
  app.dom.type(search, "배포"); await app.settle(200);
  app.dom.type(search, "zzz 없음"); await app.settle(200);
  assert.match(text(d), /조건에 맞는 기억이 없어요/);
  await press("조건 지우기", d);
  // the map's memory-edit button opens the edit popup
  app.dom.key(canvas, "ArrowDown"); app.dom.key(canvas, "Enter"); await app.settle(30);
  app.dom.key(canvas, "Escape"); await app.settle();
  app.dom.key(canvas, "ArrowLeft"); app.dom.key(canvas, "Enter"); await app.settle(30);
  const src = btn("원문 대화 보기 ›", d); if (src) { src.click(); await app.settle(); assert.match(text(win().querySelector(".ov-win-title")), /세션 기록/); }
  d = await openWindow({ wide: "map" }); await app.settle(50); d = win();
  app.dom.key(d.querySelector("canvas"), "ArrowRight"); app.dom.key(d.querySelector("canvas"), "Enter"); await app.settle(30);
  await press("기억 수정", d, 50);
  const editor = app.$$("dialog[open]").find((x) => /기억 고치기/.test(text(x)));
  assert.ok(win() && editor, "기억 수정 opens the edit popup over the map");
  editor.close(); await closeAll();
  offT(); offM();
  // an empty map, a denied map, a failed map with 다시 시도, an older server's truncated flag
  const offE = route("GET", "/v1/memory-graph", () => ok({ nodes: [], edges: [] }));
  d = await openWindow({ wide: "map" }); await app.settle(50);
  assert.match(text(win()), /지도에 그릴 기억이 아직 없어요/);
  offE();
  let offF = route("GET", "/v1/memory-graph", () => fail(403, "access_denied"));
  d = await openWindow({ wide: "map" }); await app.settle(50);
  assert.match(text(win()), /지도를 볼 권한이 없어요/);
  offF(); offF = route("GET", "/v1/memory-graph", () => fail(500, "boom"));
  d = await openWindow({ wide: "map" }); await app.settle(50);
  assert.match(text(win()), /지도를 불러오지 못했어요/);
  offF(); offG();
  const offOld = route("GET", "/v1/memory-graph", () => ok({ truncated: true, nodes: graph.nodes.slice(0, 2), edges: [] }));
  await press("다시 시도", win(), 80);
  assert.match(text(win()), /기억이 많아 일부만 그렸어요/);
  offOld();
  // closing before the map answers draws nothing
  let release; const offSlow = route("GET", "/v1/memory-graph", () => new Promise((r) => { release = () => r(ok(graph)); }));
  app.window.dispatchEvent(new app.window.CustomEvent("arkism:open-overview-window", { detail: { wide: "map" } })); await app.dom.flush(20);
  win()?.close(); release?.(); await app.settle(30);
  offSlow();
  await press("‹ 개요", await openWindow({ wide: "map" }), 50);
  await closeAll();
});

test("overview: the screen's chips, steps and cards open windows; motion follows the settings", async () => {
  await openWindow({ key: "collect" }); await closeAll();
  const table = app.$("#table");
  for (const c of table.querySelectorAll(".ov-chip")) { c.click(); await app.settle(30); assert.ok(win(), text(c)); await closeAll(); }
  table.querySelector(".ov-step").click(); await app.settle(); assert.ok(win()); await closeAll();
  table.querySelector(".ov-card").click(); await app.settle(); assert.ok(win()); await closeAll();
  // the nerve firing runs on its interval while shown, stops when hidden or on reduced motion
  await app.settle(1100);
  app.document.hidden = true; app.document.dispatchEvent(new app.window.Event("visibilitychange")); await app.settle(600);
  const firing = () => table.querySelectorAll(".ov-n-fire.on").length;
  assert.equal(firing(), 0, "hidden: no path fires");
  app.document.hidden = false; app.document.dispatchEvent(new app.window.Event("visibilitychange")); await app.settle(600);
  assert.ok(firing() > 0, "shown again: the paths fire");
  app.media.change("(prefers-reduced-motion: reduce)", true); await app.settle(600);
  assert.equal(firing(), 0, "reduced motion: the firing stops and stays off");
  app.media.change("(prefers-reduced-motion: reduce)", false); await app.settle(600);
  assert.ok(firing() > 0, "motion back: the firing resumes");
  // the head's 새로고침 reads the sessions again
  const refresh = btn("새로고침"); if (refresh) { refresh.click(); await app.settle(50); }
  assert.match(text(table), /기억 처리/);
});

// ------------------------------------------------------------ home
const home = async () => { await closeAll(); app.$('.nav-item[data-menu="home"]').click(); await app.settle(50); };
const search = () => app.$("#search");
const gs = () => app.$("#gsearch");
const pal = () => app.$("dialog.palette");
const palType = async (v) => { app.dom.type(pal().querySelector(".pal-input"), v); await app.settle(300); };
const palKey = async (k, init) => { app.dom.key(pal().querySelector(".pal-input"), k, init); await app.settle(); };
const palRows = () => [...pal().querySelectorAll(".pal-row")];

test("home search: the popover finds memories, skills, crew, tools and settings and opens them", async () => {
  await home();
  const s = search();
  s.focus(); app.dom.type(s, "orbit"); await app.settle(300);
  assert.ok(!gs().hidden);
  assert.match(text(gs()), /전체 검색/);
  assert.match(text(gs()), /MEMORY › 기억/);
  // ↑↓ move, mouseenter marks, Esc hides
  app.dom.key(s, "ArrowDown"); app.dom.key(s, "ArrowUp"); app.dom.key(s, "ArrowUp");
  app.dom.fire(gs().querySelectorAll(".gs-row")[1], "mouseenter");
  assert.ok(gs().querySelectorAll(".gs-row")[1].classList.contains("active"));
  app.dom.key(s, "Escape");
  assert.ok(gs().hidden);
  app.dom.key(s, "ArrowDown"); // hidden: ignored
  // focus repaints; Enter opens the active memory
  app.dom.fire(s, "focus"); assert.ok(!gs().hidden);
  app.dom.key(s, "Enter", { isComposing: true }); assert.ok(!gs().hidden, "composing Enter is ignored");
  app.dom.key(s, "Enter"); await app.settle(50);
  assert.equal(s.value, "");
  // nothing found: the noResults panel clears the box
  // (the preview server answers every search the same, so this one comes back empty)
  const offEmpty = route("POST", "/v1/search", () => ok({ items: [] }));
  await home();
  app.dom.type(s, "z"); await app.settle(); // one letter drops the last server answer
  app.dom.type(s, "zzqqxx"); await app.settle();
  assert.match(text(gs()), /서버에서도 찾는 중이에요/);
  await app.settle(300);
  assert.match(text(gs()), /검색 결과 없음/);
  app.dom.key(s, "ArrowDown");
  await press("검색 지우기", gs());
  assert.equal(s.value, "");
  offEmpty();
  // one letter skips the server; "설정" finds the settings tabs
  app.dom.type(s, "설정"); await app.settle(300);
  const setting = [...gs().querySelectorAll(".gs-row")].find((r) => /설정/.test(text(r.querySelector("small"))));
  setting.click(); await app.settle(50);
  // the first settings tab (일반) opens: 설정 and 일반 are the picked menu and tab, and its 수집 group is on screen
  const picked = [...app.$$("[aria-selected=true], [aria-current]")].map(text);
  assert.ok(picked.includes("설정") && picked.includes("일반"), picked.join(" | "));
  assert.match(text(app.$("#table")), /수집 켜기/);
  // 기억 목록에서 걸러 보기 › moves the query to the memory list
  await home();
  app.dom.type(s, "orbit"); await app.settle(300);
  await press("기억 목록에서 걸러 보기 ›", gs(), 50);
  assert.equal(s.value, "orbit");
  // off the home the box filters the list instead
  app.dom.type(s, "orbit"); await app.settle();
  assert.ok(gs().hidden);
  // blur hides after a moment
  await home(); app.dom.type(s, "orbit"); await app.settle(300);
  app.dom.fire(s, "blur"); await app.settle(150);
  assert.ok(gs().hidden);
  app.dom.type(s, ""); await app.settle();
});

test("home search: the server half falls back to lexical modes, and a stale answer is dropped", async () => {
  await home();
  let calls = 0;
  const off = route("POST", "/v1/search", (a) => { calls++; return /vector/.test(String(a.body)) ? fail(503, "embedder_down") : ok({ items: [{ id: "x9", collection: "memories", text: "서버에서만 찾은 기억 orbit" }] }); });
  app.dom.type(search(), "orbit"); await app.settle(300);
  assert.equal(calls, 2, "vector failed, exact+fts asked again");
  assert.match(text(gs()), /서버에서만 찾은 기억/);
  // the server memory is not loaded: it is read, and a read failure says so
  const offRead = route("GET", "/v1/memories/x9", () => fail(404, "not_found"));
  [...gs().querySelectorAll(".gs-row")].find((r) => /서버에서만/.test(text(r))).click(); await app.settle(50);
  assert.match(text(app.document.body), /이 기억을 열지 못했어요/);
  offRead();
  // both modes failing leaves only the loaded matches
  off(); const off2 = route("POST", "/v1/search", () => fail(500, "boom"));
  await home(); app.dom.type(search(), "orbit"); await app.settle(300);
  assert.ok(!/서버에서만/.test(text(gs())));
  off2();
  // a newer query wins over a slower older one
  let release; const off3 = route("POST", "/v1/search", () => new Promise((r) => { release = r; }));
  app.dom.type(search(), "orb"); await app.timers.tick(260); await app.dom.flush(10);
  off3();
  app.dom.type(search(), "orbi"); await app.settle(300);
  release?.(ok({ items: [{ id: "late", text: "늦게 온 결과 orb" }] })); await app.settle();
  assert.ok(!/늦게 온 결과/.test(text(gs())));
  app.dom.type(search(), ""); await app.settle();
});

test("home palette: Ctrl K, tabs, keys, results by kind and the empty actions", async () => {
  await home();
  app.dom.key(app.document.body, "k", { ctrlKey: true }); await app.settle();
  assert.ok(pal()?.open);
  assert.match(text(pal()), /최근 기억/);
  assert.match(text(pal()), /새 기억 확인/);
  // the tabs by click and by keys (←→ only while empty, Tab / Shift Tab always)
  pal().querySelector('.pal-tab[data-tab="memory"]').click(); await app.settle();
  assert.ok(!/새 기억 확인/.test(text(pal())));
  await palKey("ArrowRight"); await palKey("ArrowLeft"); await palKey("ArrowLeft");
  await palKey("Tab"); await palKey("Tab", { shiftKey: true });
  await palKey("ArrowDown"); await palKey("ArrowUp"); await palKey("ArrowDown", { isComposing: true });
  pal().querySelector('.pal-tab[data-tab="all"]').click(); await app.settle();
  // every kind turns up for its own word
  await palType("리");
  assert.match(text(pal()), /FLOW/);
  for (const [q, group] of [["github", "MCP"], ["orbit mail", "플러그인"], ["review", "스킬"], ["설정", "설정"]]) { await palType(q); assert.match(text(pal()), new RegExp(group), q); }
  pal().querySelector('.pal-tab[data-tab="skill"]').click(); await app.settle();
  await palType("review");
  assert.ok(palRows().length >= 1);
  pal().querySelector('.pal-tab[data-tab="all"]').click(); await app.settle();
  const offEmpty = route("POST", "/v1/search", () => ok({ items: [] }));
  await palType("z");
  await palType("zzqqxx");
  assert.match(text(pal()), /찾는 결과가 없어요/);
  await palKey("ArrowDown"); await palKey("Enter");
  app.dom.type(pal().querySelector(".pal-input"), "zzy"); await app.settle();
  assert.match(text(pal()), /서버에서도 찾는 중이에요/);
  await app.settle(300);
  offEmpty();
  // Enter opens the active row (an MCP server opens its tool detail)
  await palType("github");
  app.dom.fire(palRows()[0], "mouseenter");
  await palKey("Enter"); await app.settle(50);
  assert.ok(!pal().open);
  // the plugin, a skill, an expert, a team and a workflow
  const openBy = async (q, pick) => { app.$("#search-toggle").click(); await app.settle(); await palType(q); const r = palRows().find((x) => pick.test(text(x))); assert.ok(r, q); r.click(); await app.settle(50); };
  await openBy("orbit mail", /Orbit Mail/);
  await openBy("review", /code-review/);
  let ev = null; const seen = (e) => { ev = e.type; };
  app.window.addEventListener("arkism:open-expert", seen); app.window.addEventListener("arkism:open-team", seen);
  await openBy("디자이너", /^.*디자이너/); assert.equal(ev, "arkism:open-expert");
  await openBy("디자인 리뷰", /디자인 리뷰 팀/); assert.equal(ev, "arkism:open-team");
  app.window.removeEventListener("arkism:open-expert", seen); app.window.removeEventListener("arkism:open-team", seen);
  await openBy("릴리스", /\$릴리스점검/);
  // the empty actions: 기억 후보 검토, 연결 상태 보기, 설정 열기; a backdrop click and ✕ close it
  for (const label of ["새 기억 확인", "연결 상태 보기", "설정 열기"]) {
    app.$("#search-toggle").click(); await app.settle();
    palRows().find((r) => text(r) === label).click(); await app.settle(50);
    assert.ok(!pal().open, label);
  }
  app.$("#search-toggle").click(); await app.settle();
  app.dom.fire(pal().querySelector(".pal-body"), "click"); assert.ok(pal().open);
  app.dom.fire(pal(), "click"); assert.ok(!pal().open);
  app.$("#search-toggle").click(); await app.settle();
  app.$("#search-toggle").click(); await app.settle(); // already open: just focuses
  await press("닫기", pal());
  // / opens it too when the screen has no search of its own
  await home();
  app.document.activeElement = app.document.body;
  app.dom.key(app.document.body, "/"); await app.settle();
  assert.ok(pal().open);
  pal().close();
});

test("home: ? opens the shortcuts once", async () => {
  await home();
  app.document.activeElement = app.document.body;
  app.dom.key(app.document.body, "?"); await app.settle();
  const d = app.$("#keys-dialog");
  assert.ok(d?.open);
  assert.match(text(d), /단축키/);
  assert.match(text(d), /한글을 조합하는 동안/);
  d.close();
  app.dom.key(app.document.body, "?"); await app.settle();
  assert.ok(app.$("#keys-dialog").open);
  assert.equal(app.$$("#keys-dialog").length, 1);
  app.$("#keys-dialog").close();
});

test("home: menu rows count crew runs, routing and knowledge; a stalled pipeline warns", async () => {
  const offs = [
    route("GET", "/v1/crew/runs", () => ok({ items: [{ crew_run: { status: "waiting", waiting: { kind: "approval" } }, created_at: ago(1) }, { crew_run: { status: "failed" }, created_at: ago(5) }, { crew_run: { status: "failed" }, created_at: ago(500) }] })),
    route("GET", "/v1/routing/overview", () => ok({ kpi: { cost_usd: 12, budget_usd: 40 }, models: [{ name: "Sol", status: "slow" }, { name: "Astra", status: "watch" }, { name: "Jev", status: "ok" }] })),
    route("GET", "/v1/wiki/pages", () => ok({ items: [{ review_state: "draft" }, { review_state: "approved", stale_sources: ["a"] }, { review_state: "deleted" }] })),
    route("GET", "/v1/knowledge/sources", () => ok({ items: [{ lifecycle_status: "draft" }, { lifecycle_status: "ready", trust_level: "reviewed" }, { lifecycle_status: "failed" }] })),
    route("GET", "/v1/usage/sessions", () => ok({ authority: "data_only_untrusted", items: [{ id: "u1", ended_at: ago(70) }, { id: "u2", ended_at: ago(1) }], next_cursor: "" }))
  ];
  await home();
  app.dom.fire(app.window, "storage", { key: "arkism.popout-saved" }); await app.settle(100); // a new load reads every row again
  await home(); await app.settle(100);
  const body = text(app.$("#table"));
  assert.match(body, /확인 대기 1/);
  assert.match(body, /실패 1/);
  assert.match(body, /Sol 느림/);
  assert.match(body, /Astra 지연 모니터링/);
  assert.match(body, /이번 달 예산 30%/);
  assert.match(body, /위키 초안 1/);
  assert.match(body, /출처 바뀜 1/);
  assert.match(body, /검토 대기 자료 1/);
  assert.match(body, /처리 흐름 보기/);
  assert.match(body, /기억 추출이 \d+일째 멈췄어요/);
  // the minute timer reads the sessions again while the home is shown
  const before = app.apiCalls.filter(([, p]) => p.startsWith("/v1/usage/sessions")).length;
  await app.settle(61000);
  assert.ok(app.apiCalls.filter(([, p]) => p.startsWith("/v1/usage/sessions")).length > before);
  offs.forEach((off) => off());
  // a routing overview without kpi and failing reads hide those rows
  const offs2 = [route("GET", "/v1/routing/overview", () => ok({})), route("GET", "/v1/crew/runs", () => fail(500, "boom")), route("GET", "/v1/wiki/pages", () => fail(500, "boom"))];
  app.dom.fire(app.window, "storage", { key: "arkism.popout-saved" }); await app.settle(100);
  await home(); await app.settle(100);
  assert.ok(!/이번 달 예산/.test(text(app.$("#table"))));
  offs2.forEach((off) => off());
});

test("home: a finished analysis run with no candidate is not a stall, and the 분석 window says why", async () => {
  const run = (at, extra) => route("GET", "/v1/agent_tools", () => ok({ agent_tools: { servers: [], plugins: [], requests: [], analysis_run: { at, jobs: 2, done: 1, candidates: 0, waiting: 1, dropped: 0, parked: 0, failed: 0, reason: "session_batch_wait", error: "", last_done_at: at, last_done_candidates: 0, ...extra } } }));
  const offs = [route("GET", "/v1/usage/sessions", () => ok({ authority: "data_only_untrusted", items: [{ id: "u1", ended_at: ago(70) }, { id: "u2", ended_at: ago(1) }], next_cursor: "" })), run(ago(0.5))];
  app.dom.fire(app.window, "storage", { key: "arkism.popout-saved" }); await app.settle(100);
  await home(); await app.settle(100);
  assert.ok(!/기억 추출이 .*멈췄어요/.test(text(app.$("#table"))), "the run finished a job after the sessions ended");
  let d = await openWindow({ key: "extract" });
  assert.match(text(d), /마지막 분석/);
  assert.match(text(d), /대화가 끝나고 5분 쉬면 분석해요/);
  assert.ok(!d.querySelector(".ov-win-note-r"), "no stall note");
  await closeAll(); offs.pop()();
  // no run for 80 hours: a real stall, and the window names the server command
  offs.push(run(ago(80), { last_done_at: ago(80) }));
  app.dom.fire(app.window, "storage", { key: "arkism.popout-saved" }); await app.settle(100);
  await home(); await app.settle(100);
  d = await openWindow({ key: "extract" });
  assert.match(text(d.querySelector(".ov-win-note-r")), /분석 타이머를 켜 주세요/);
  assert.match(text(d), /sudo systemctl start arkism-reflect.service/);
  await closeAll(); offs.pop()();
  // the server refused the run: the action is the agent sign-in
  offs.push(run(ago(0.2), { done: 0, failed: 1, error: "401", last_done_at: ago(80) }));
  app.dom.fire(app.window, "storage", { key: "arkism.popout-saved" }); await app.settle(100);
  await home(); await app.settle(100);
  d = await openWindow({ key: "extract" });
  assert.match(text(d), /로그인 만료/);
  await press("에이전트 설정 열기", d, 50);
  assert.ok(!app.$("dialog.ov-win-extract[open]"), "the window closes"); assert.match(text(app.$("#table")), /Gemini CLI/, "agent settings are shown");
  await closeAll();
  offs.forEach((off) => off());
});

// A fresh install with only Claude Code: the other agents are grey and out of 주의, and a memory extractor that cannot
// run says so in plain words with the way to fix it, on 홈 and in the status card.
test("home: agents that are not installed stay grey, and a broken memory extractor gets one plain card", async () => {
  const ids = ["claude_code", "codex", "zcode", "gemini", "grok", "opencode", "cursor"];
  const tools = (extractor, run) => route("GET", "/v1/agent_tools", () => ok({ agent_tools: { collected_at: new Date().toISOString(), servers: [], plugins: [], requests: [],
    agents: ids.map((id, i) => ({ id, name: id, found: i === 0, enabled: true })), extractor, analysis_run: run || null } }));
  let off = tools({ model: "haiku", cli: "claude", state: "login_needed", clis: { claude: "login_needed", codex: "missing", gemini: "missing" } });
  app.dom.fire(app.window, "storage", { key: "arkism.popout-saved" }); await app.settle(150);
  await home(); await app.settle(100);
  assert.match(text(app.$("#table")), /기억을 뽑으려면 로그인이 필요해요/);
  assert.match(text(app.$("#table")), /로그인하기/);
  for (const id of ids.slice(1, 5)) assert.equal(app.$(`#status-agents [data-agent="${id}"]`).hidden, true, id); // the tiles in index.html
  assert.equal(app.$('#status-agents [data-agent="claude_code"]').hidden, false);
  assert.match(text(app.$("#status-list")), /기억 뽑을 모델로그인 필요/);
  assert.equal(text(app.$("#status-badge")), "주의 1", "only the extractor, not the six missing agents");
  // the host picked a model but its last run failed: the code turns into words, never shown as the main text
  off(); off = tools({ model: "codex", cli: "codex", state: "ready" }, { at: ago(0.1), jobs: 1, done: 0, failed: 1, reason: "extractor_no_json", error: "" });
  app.dom.fire(app.window, "storage", { key: "arkism.popout-saved" }); await app.settle(150);
  await home(); await app.settle(100);
  assert.match(text(app.$("#table")), /기억을 뽑는 모델의 답을 읽지 못했어요/);
  assert.doesNotMatch(text(app.$("#table")), /extractor_no_json/);
  // the 분석 window: its status is the failed run (not 정상), and it names the extractor that runs, Codex, as 홈 does
  let d = await openWindow({ key: "extract" });
  const badge = d.querySelector(".ov-win-badge");
  // a 6-hour stall (the preview's sessions, depending on the clock) outranks it and is red
  if (badge.classList.contains("r")) assert.match(text(badge), /멈춤/);
  else { assert.equal(text(badge), "답을 못 읽음"); assert.ok(badge.classList.contains("a"), "a warning tone"); }
  assert.match(text(d.querySelector(".ov-stage-what")), /AI \(Codex\)/); assert.doesNotMatch(text(d), /서버 Claude/);
  await closeAll();
  // ready and nothing failed: no card, no status row
  off(); off = tools({ model: "codex", cli: "codex", state: "ready" });
  app.dom.fire(app.window, "storage", { key: "arkism.popout-saved" }); await app.settle(150);
  await home(); await app.settle(100);
  assert.doesNotMatch(text(app.$("#table")), /기억을 뽑/);
  assert.doesNotMatch(text(app.$("#status-list")), /기억 뽑을 모델/);
  d = await openWindow({ key: "extract" });
  assert.ok(!d.querySelector(".ov-win-badge").classList.contains("a"), "no failure once the run is fine");
  await closeAll();
  off();
  app.dom.fire(app.window, "storage", { key: "arkism.popout-saved" }); await app.settle(150);
});

test("home: a prompt turns up in search once the gallery menu shows", async () => {
  const off = route("GET", "/v1/prompts", () => ok({ items: [{ id: "pp1", title: "썸네일 슬라이드", prompt: "표지", kind: "slide", image_prompt: { kind: "slide", title: "썸네일 슬라이드", prompt: "표지 {제목}", engine: "gpt" } }] }));
  app.dom.fire(app.window, "storage", { key: "arkism.popout-saved" }); await app.settle(1500);
  const gallery = app.$('.nav-item[data-menu="gallery"]');
  gallery.hidden = false;
  await home();
  const s = search();
  s.focus(); app.dom.type(s, "썸네일"); await app.settle(300);
  const row = [...gs().querySelectorAll(".gs-row")].find((r) => /썸네일 슬라이드/.test(text(r)));
  gallery.hidden = true; off();
  assert.ok(row, "prompt row");
  assert.match(text(row), /GALLERY › 슬라이드/);
  row.click(); await app.settle(50);
  assert.ok(gs().hidden);
});

test("home: a manual connection walks the start steps; SSH errors explain themselves", async () => {
  await home();
  const form = app.$("#unlock-form");
  // a connect that works also loads everything, so wait for 연결하기 to come back
  const connect = async () => { form.requestSubmit(app.$("#ssh-button")); await app.settle(100); for (let i = 0; i < 30 && app.$("#ssh-button").disabled; i++) await app.settle(100); };
  // no host yet: the form's own check fails and the side pane says so
  form.requestSubmit(app.$("#ssh-button")); await app.settle(50);
  let pane = app.$("#ssh-error-side");
  assert.ok(pane && !pane.hidden);
  assert.match(text(pane), /연결하지 못했어요/);
  assert.match(text(app.$("#unlock-error")), /서버 주소를 넣어 주세요/);
  const [host, user, port, key] = app.$("#ssh-form").querySelectorAll("input");
  host.value = "my-server"; user.value = "me"; port.value = "2222"; key.value = "~/.ssh/id_ed25519";
  // a refused login names the kind and shows the ssh command to try
  app.fake.ssh_host_key_check = { state: "trusted" };
  app.fake.ssh_connect = new Error("ssh_auth_failed");
  await press("다시 시도", pane, 50);
  pane = app.$("#ssh-error-side");
  assert.match(text(pane), /SSH 로그인이 거부됐어요/);
  assert.match(text(pane), /ssh -p 2222 -i ~\/\.ssh\/id_ed25519 me@my-server/);
  assert.ok([...pane.querySelectorAll(".ssh-err-row")].some((r) => r.classList.contains("on")));
  await press("SSH 테스트", pane, 50);
  app.fake.ssh_connect = new Error("ssh_missing");
  form.requestSubmit(app.$("#ssh-button")); await app.settle(50);
  assert.ok(!/ssh -p/.test(text(app.$("#ssh-error-side"))), "no command when ssh itself is missing");
  app.fake.ssh_connect = { server: app.origin, token: "preview", workspace_id: "default", project_id: "default", ssh_host: "my-server" };
  // connected: 시작 2단계 lists the agents the server found
  await connect();
  assert.ok(app.$("#ssh-error-side").hidden, "a new try puts the usual hint back");
  let t = app.$("#table");
  assert.match(text(t), /이 PC에 표시/);
  assert.match(text(t), /서버 my-server가 찾은 에이전트/);
  // every switch but one off, then the last one refuses
  const flip = async () => { const on = [...app.$$("#table .switch")].filter((s) => s.getAttribute("aria-checked") === "true"); on[0].click(); await app.settle(); return on.length; };
  for (let i = 0; i < 20 && await flip() > 1; i++) { /* turn them off one by one; bounded so a stuck switch fails below */ }
  assert.match(text(app.document.body), /에이전트가 하나는 켜져 있어야 해요/);
  for (const s of app.$$("#table .switch")) if (s.getAttribute("aria-checked") === "false") { s.click(); await app.settle(); }
  // 다음 → 3단계; 이전 goes back; the three buttons there
  await press("다음 · 연결 확인", app.$("#table"));
  t = app.$("#table");
  assert.match(text(t), /SSH로 연결됐어요/);
  assert.match(text(app.document.body), /연결 정보/);
  await press("연결 상태 보기", t);
  await press("이전", app.$("#table"));
  assert.match(text(app.$("#table")), /이 PC에 표시/);
  await press("다음 · 연결 확인", app.$("#table"));
  await press("모델 연결 시험", app.$("#table"), 50);
  await connect();
  await press("에이전트 관리 · 직접 추가 ›", app.$("#table"), 50);
  await connect();
  await press("다음 · 연결 확인", app.$("#table"));
  await press("홈으로 시작하기", app.$("#table"), 50);
  assert.ok(!/SSH로 연결됐어요/.test(text(app.$("#table"))));
  // 2단계's 이전 locks the app again
  await connect();
  await press("이전", app.$("#table"), 50);
  await connect();
  await press("홈으로 시작하기", (await press("다음 · 연결 확인", app.$("#table")), app.$("#table")), 50);
});

test("home: a lost connection shows why, how to retry and the way back to this PC", async () => {
  await home();
  // the server answered with an error: its own words
  let off = route("GET", "/v1/memories?", () => fail(500, "boom"));
  app.dom.fire(app.window, "storage", { key: "arkism.popout-saved" }); await app.settle(100);
  await home();
  let lost = app.$(".home-lost");
  assert.ok(lost);
  assert.ok(text(lost).length > 0, text(lost));
  assert.ok(btn("이 PC에서 쓰기", lost), "remote with a local Arkism offers it");
  await press("지금 다시 연결", lost, 100);
  await press("연결 설정", app.$(".home-lost") || lost, 50);
  // the server was not reached: where it tried, and the usual reasons beside it
  off(); off = route("GET", "/v1/memories?", () => { throw new Error("offline"); });
  app.dom.fire(app.window, "storage", { key: "arkism.popout-saved" }); await app.settle(100);
  await home();
  lost = app.$(".home-lost");
  assert.match(text(lost), /me@my-server:2222에 SSH로 연결하지 못했어요/);
  const side = app.$(".home-reasons");
  assert.ok(side);
  assert.match(text(side), /PC가 잠자기에서 깨어났어요/);
  await press("연결 방법 다시 보기 ›", side, 50);
  await home();
  const reloads = app.window._reloaded || 0;
  await press("이 PC에서 쓰기", app.$(".home-lost"), 50);
  assert.equal(app.window._reloaded, reloads + 1);
  off();
  app.dom.fire(app.window, "storage", { key: "arkism.popout-saved" }); await app.settle(100);
  await home();
  assert.equal(app.$(".home-lost"), null);
});

// 분류 대기·승인 대기 and the skill count showed the first page (50) while 100+ waited on the server: they now show the server totals.
test("overview and skills: counts are the server totals while more pages wait", async () => {
  let asked = 0;
  const reload = async () => { const n = asked; app.dom.fire(app.window, "storage", { key: "arkism.popout-saved" }); for (let i = 0; i < 100 && asked === n; i++) await app.settle(100); await app.settle(100); };
  const cand = { id: "cx1", text: "first page", status: "candidate", kind: "user", created_at: ago(1) };
  const page = (total, facets = {}) => ok({ items: [], total, next_cursor: "", authority: "data_only", facets: { kinds: {}, repositories: {}, categories: {}, states: {}, ...facets } });
  const offs = [
    route("GET", "/v1/memory_usage", () => { asked++; return ok({ items: [] }); }),
    route("GET", "/v1/memory_candidates", (a) => ok({ items: /conflicted/.test(a.path) ? [] : [cand], next_cursor: /conflicted/.test(a.path) ? "" : "more", authority: "data_only_untrusted" })),
    route("POST", "/v1/memory_candidates/query", (a) => JSON.parse(a.body).state === "all" ? page(123, { unclassified: 77 }) : page(120)),
    route("GET", "/v1/skills?", () => ok({ items: [{ id: "s1", name: "one", status: "approved" }], next_cursor: "more", authority: "data_only_untrusted" })),
    route("POST", "/v1/skills/query", () => page(137))
  ];
  await reload();
  await openWindow({ key: "collect" }); await closeAll(); await app.settle(50);
  const steps = text(app.$(".ov-steps"));
  assert.match(steps, /대기 77/, steps);
  assert.match(steps, /123건/, steps);
  assert.equal(text(app.$("#count-skills")), "137");
  offs.forEach((off) => off());
  await reload();
});
