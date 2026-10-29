const test = require("node:test");
const assert = require("node:assert/strict");
require("./keep.js"); // the shared stale-reply and redraw guards the screens use
const { boot } = require("./testdata/crew-harness.cjs");
const result = (extra = {}) => ({ run_id: "run1", revision: 8, status: "succeeded", source_type: "agent", invocation_mode: "flow", result: "최종 결과", children: {}, ...extra });
// A FLOW run's result lives in its workflow's 기록: open wf1's 기록 and pick its first run (run1).
async function history(h) { window.dispatchEvent(new CustomEvent("arkism:open-automation", { detail: { id: "wf1", tab: "history" } })); await h.settle(); }
async function firstResult(h) { await history(h); h.top().querySelector(".auto-runline").click(); await h.settle(); }
const box = (h) => h.top();

test("the workflow screen has no 작업·결과함·개선 흐름 tabs", async () => {
  const h = await boot({ view: "crew" });
  h.render(); await h.settle();
  assert.equal(document.querySelector("#chips [role=tab]"), null);
  assert.ok(document.querySelector(".wf-grid"));
  assert.doesNotMatch(document.body.textContent, /결과함|개선 흐름/);
  assert.equal(h.calls.some(x => /\/v1\/flow\/improvements/.test(x.url)), false);
  h.closeAll();
});

test("a run detail in 기록 shows the final result text and delivery status", async () => {
  const h = await boot({ view: "crew", routes: [["GET", /\/runs\/run1\/result\?/, () => result({ result: "배포 점검 보고서", delivery: { delivery: { status: "delivered", message_ids: ["receipt-9"] } } })]] });
  h.render(); await h.settle(); await firstResult(h);
  assert.equal(box(h).querySelector(".flow-result-text").textContent, "배포 점검 보고서");
  assert.match(box(h).textContent, /대화로 전달/);
  assert.match(box(h).textContent, /전달됨/);
  assert.match(box(h).textContent, /receipt-9/);
  h.btn(box(h), "목록으로").click(); await h.settle();
  assert.ok(box(h).querySelector(".auto-runline"));
  box(h).querySelector(".auto-runline").click(); await h.settle();
  h.btn(box(h), "단계 보기").click(); await h.settle();
  assert.ok(document.querySelector("dialog.graph-win[open]"), "단계 보기 opens the run in the graph window");
  h.closeAll();
});

test("실행 보기 on a waiting FLOW run opens that run in its workflow's 기록", async () => {
  const h = await boot({ view: "crew", routes: [["GET", /\/runs\/run1\/result\?/, () => result({ status: "waiting", waiting: { kind: "approval", node_id: "ok", question: "보낼까요?" } })]] });
  h.data.runs[0].crew_run.invocation_mode = "flow";
  h.render(); await h.settle();
  h.btn(document.querySelector("#stats"), "실행 보기").click(); await h.settle();
  assert.equal(h.top().querySelector(".crew-popup-tab.on").textContent, "기록");
  assert.ok(h.top().querySelector(".flow-history-detail"));
  assert.match(h.top().textContent, /사람의 결정이 필요해요/);
  h.closeAll();
});

test("FLOW results retain the full report, child refs and uncertain delivery without resending", async () => {
  const full = "긴 근거와 출처\n".repeat(900);
  const h = await boot({ view: "crew", routes: [["GET", /\/runs\/run1\/result\?/, () => result({ result: full, children: { "work:1": { id: "child-1", kind: "workflow" } }, delivery: { delivery: { status: "unknown", message_ids: ["receipt-1"] } } })], ["GET", /\/runs\/child-1\/result\?/, () => result({ run_id: "child-1", result: "자식의 최종 결과" })]] });
  h.render(); await h.settle(); await firstResult(h);
  assert.equal(document.querySelector(".flow-result-text").textContent, full);
  assert.match(box(h).textContent, /전달 여부 확인 필요/);
  assert.match(box(h).textContent, /receipt-1/);
  h.btn(box(h), /^하위 실행/).click(); await h.settle();
  assert.equal(document.querySelector(".flow-result-text").textContent, "자식의 최종 결과");
  assert.equal(h.calls.filter(x => x.method === "POST").length, 0);
  h.closeAll();
});

test("FLOW child approval binds the displayed parent revision and stop remains a request", async () => {
  let current = result({ status: "waiting", waiting: { kind: "child", node_id: "delegate", decision_kind: "approval", child_run_id: "child-1", question: "조사할까요?" } });
  const h = await boot({ view: "crew", routes: [["GET", /\/runs\/run1\/result\?/, () => current], ["POST", /\/runs\/run1\/decision$/, (_m, _url, body) => { assert.equal(body.expected_revision, 8); assert.equal(body.node_id, "delegate"); current = result({ status: "running", revision: 9 }); return {}; }], ["POST", /\/runs\/run1\/stop$/, (_m, _url, body) => { assert.equal(body.expected_revision, 9); current = result({ status: "running", revision: 10, cancel_requested: true }); return {}; }]] });
  h.render(); await h.settle(); await firstResult(h);
  h.btn(box(h), "승인").click(); await h.settle();
  assert.equal(h.calls.filter(x => x.url.endsWith("/decision")).length, 1);
  h.btn(box(h), "중단 요청").click(); await h.settle();
  assert.equal(h.btn(box(h), "중단 요청됨").disabled, true);
  assert.match(box(h).textContent, /실행 중/);
  assert.match(h.notices.join(" "), /실제 종료 상태를 확인/);
  h.closeAll();
});

test("FLOW rejects a stale private result after switching connections", async () => {
  let resolve;
  const deferred = new Promise(r => { resolve = r; });
  const h = await boot({ view: "crew", routes: [["GET", /\/runs\/run1\/result\?/, () => deferred]] });
  h.render(); await h.settle(); await history(h);
  box(h).querySelector(".auto-runline").click();
  h.state.token = "different-fixture-connection";
  resolve(result({ result: "이전 연결의 비공개 결과" })); await h.settle();
  assert.doesNotMatch(box(h).textContent, /이전 연결의 비공개 결과/);
  h.closeAll();
});

test("FLOW read errors are not displayed as an empty successful history", async () => {
  const h = await boot({ view: "crew", routes: [["GET", /\/runs\/run1\/result\?/, () => new Error("denied")]] });
  h.render(); await h.settle(); await firstResult(h);
  assert.match(box(h).textContent, /접근 권한/);
  assert.doesNotMatch(box(h).textContent, /아직 최종 결과가 없어요/);
  h.closeAll();
});

test("FLOW decision replies do not reopen a result after leaving the 기록 tab", async () => {
  let resolve;
  const deferred = new Promise(r => { resolve = r; });
  const h = await boot({ view: "crew", routes: [["GET", /\/runs\/run1\/result\?/, () => result({ status: "waiting", waiting: { kind: "approval", node_id: "approve", question: "진행할까요?" } })], ["POST", /\/runs\/run1\/decision$/, () => deferred]] });
  h.render(); await h.settle(); await firstResult(h);
  h.btn(box(h), "승인").click();
  h.btn(box(h), "개요").click(); await h.settle();
  resolve({}); await h.settle();
  assert.equal(box(h).querySelector(".flow-history-detail"), null);
  assert.equal(h.calls.filter(x => x.method === "GET" && /\/result\?/.test(x.url)).length, 1);
  h.closeAll();
});

test("an agent's and a team's 기록 list their @이름 FLOW runs and open the result in place", async () => {
  const h = await boot({ view: "crew", routes: [["GET", /\/runs\/flow-a\/result\?/, () => result({ run_id: "flow-a", result: "전문가 보고서", delivery: { delivery: { status: "failed" } } })], ["GET", /\/runs\/flow-t\/result\?/, () => result({ run_id: "flow-t", result: "팀 결론" })]] });
  h.data.runs.unshift({ id: "flow-a", revision: 1, created_at: new Date().toISOString(), crew_run: { crew_id: "ag1", status: "succeeded", invocation_mode: "flow" } },
    { id: "flow-t", revision: 1, created_at: new Date().toISOString(), crew_run: { crew_id: "tm1", status: "succeeded", invocation_mode: "flow" } });
  h.render(); await h.settle();
  for (const [card, run, text] of [["ag1", "flow-a", "전문가 보고서"], ["tm1", "flow-t", "팀 결론"]]) {
    document.querySelector(`[data-id="crew:${card}"]`).click(); await h.settle();
    [...h.top().querySelectorAll("[role=tab]")].find((t) => t.textContent.trim() === "기록").click(); await h.settle();
    assert.match(h.top().textContent, /부른 실행 결과/);
    h.btn(h.top(), new RegExp(run)).click(); await h.settle();
    assert.equal(h.top().querySelector(".flow-result-text").textContent, text);
    if (card === "ag1") assert.match(h.top().textContent, /전달 실패/);
    h.closeAll();
  }
});
