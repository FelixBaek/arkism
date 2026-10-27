const test = require("node:test");
const assert = require("node:assert/strict");
const { boot } = require("./testdata/crew-harness.cjs");
const result = (extra = {}) => ({ run_id: "run1", revision: 8, status: "succeeded", source_type: "agent", invocation_mode: "flow", result: "최종 결과", children: {}, ...extra });
async function tab(h, label) { h.btn(document.querySelector("#chips"), label).click(); await h.settle(); }
async function firstResult(h) { await tab(h, "결과함"); document.querySelector(".flow-history-row").click(); await h.settle(); }

test("FLOW results retain the full report, child refs and uncertain delivery without resending", async () => {
  const full = "긴 근거와 출처\n".repeat(900);
  const h = await boot({ view: "crew", routes: [["GET", /\/runs\/run1\/result\?/, () => result({ result: full, children: { "work:1": { id: "child-1", kind: "workflow" } }, delivery: { delivery: { status: "unknown", message_ids: ["receipt-1"] } } })], ["GET", /\/runs\/child-1\/result\?/, () => result({ run_id: "child-1", result: "자식의 최종 결과" })]] });
  h.render(); await h.settle(); await firstResult(h);
  assert.equal(document.querySelector(".flow-result-text").textContent, full);
  assert.match(document.querySelector("#table").textContent, /전달 여부 확인 필요/);
  assert.match(document.querySelector("#table").textContent, /receipt-1/);
  h.btn(document.querySelector("#table"), /^하위 실행/).click(); await h.settle();
  assert.equal(document.querySelector(".flow-result-text").textContent, "자식의 최종 결과");
  assert.equal(h.calls.filter(x => x.method === "POST").length, 0);
  h.closeAll();
});

test("FLOW child approval binds the displayed parent revision and stop remains a request", async () => {
  let current = result({ status: "waiting", waiting: { kind: "child", node_id: "delegate", decision_kind: "approval", child_run_id: "child-1", question: "조사할까요?" } });
  const h = await boot({ view: "crew", routes: [["GET", /\/runs\/run1\/result\?/, () => current], ["POST", /\/runs\/run1\/decision$/, (_m, _url, body) => { assert.equal(body.expected_revision, 8); assert.equal(body.node_id, "delegate"); current = result({ status: "running", revision: 9 }); return {}; }], ["POST", /\/runs\/run1\/stop$/, (_m, _url, body) => { assert.equal(body.expected_revision, 9); current = result({ status: "running", revision: 10, cancel_requested: true }); return {}; }]] });
  h.render(); await h.settle(); await firstResult(h);
  h.btn(document.querySelector("#table"), "승인").click(); await h.settle();
  assert.equal(h.calls.filter(x => x.url.endsWith("/decision")).length, 1);
  h.btn(document.querySelector("#table"), "중단 요청").click(); await h.settle();
  assert.equal(h.btn(document.querySelector("#table"), "중단 요청됨").disabled, true);
  assert.match(document.querySelector("#table").textContent, /실행 중/);
  assert.match(h.notices.join(" "), /실제 종료 상태를 확인/);
  h.closeAll();
});

test("FLOW rejects a stale private result after switching connections", async () => {
  let resolve;
  const deferred = new Promise(r => { resolve = r; });
  const h = await boot({ view: "crew", routes: [["GET", /\/runs\/run1\/result\?/, () => deferred]] });
  h.render(); await h.settle(); await tab(h, "결과함");
  document.querySelector(".flow-history-row").click();
  h.state.token = "different-fixture-connection";
  await tab(h, "작업");
  resolve(result({ result: "이전 연결의 비공개 결과" })); await h.settle();
  assert.doesNotMatch(document.querySelector("#table").textContent, /이전 연결의 비공개 결과/);
  assert.ok(document.querySelector(".wf-grid"));
  h.closeAll();
});

test("FLOW improvements continue an empty authorized page and never auto-advance", async () => {
  const h = await boot({ view: "crew", routes: [["GET", /^\/v1\/flow\/improvements\?/, (_m, url) => new URLSearchParams(url.split("?")[1]).has("after") ? { items: [{ id: "improve-1", skill_id: "review-skill", flow_improvement: { stage: "approval_required" } }] } : { items: [], next_cursor: "next-1" }], ["GET", /\/improvements\/improve-1\?/, () => ({ id: "improve-1", revision: 4, improvement: { stage: "approval_required", source_run: { id: "run1", revision: 3 }, pending: { action: "compare", state: "unknown" }, candidate: { id: "candidate-1", revision: 2 } } })]] });
  h.render(); await h.settle(); await tab(h, "개선 흐름");
  h.btn(document.querySelector("#table"), "더 보기").click(); await h.settle();
  h.btn(document.querySelector("#table"), /^review-skill/).click(); await h.settle();
  assert.match(document.querySelector("#table").textContent, /사람 승인 필요/);
  assert.match(document.querySelector("#table").textContent, /확인 없이 같은 작업을 다시 시작하지 마세요/);
  assert.match(document.querySelector("#table").textContent, /candidate-1/);
  assert.equal(h.calls.filter(x => x.method === "POST").length, 0);
  assert.equal(h.calls.some(x => new URLSearchParams(x.url.split("?")[1]).get("limit") === "50"), true);
  h.closeAll();
});

test("FLOW read errors are not displayed as an empty successful history", async () => {
  const h = await boot({ view: "crew", routes: [["GET", /^\/v1\/flow\/improvements\?/, () => new Error("denied")]] });
  h.render(); await h.settle(); await tab(h, "개선 흐름");
  assert.match(document.querySelector("#table").textContent, /접근 권한/);
  assert.doesNotMatch(document.querySelector("#table").textContent, /아직 연결된 개선 흐름이 없어요/);
  h.closeAll();
});

test("FLOW decision replies do not reopen a result after leaving the page", async () => {
  let resolve;
  const deferred = new Promise(r => { resolve = r; });
  const h = await boot({ view: "crew", routes: [["GET", /\/runs\/run1\/result\?/, () => result({ status: "waiting", waiting: { kind: "approval", node_id: "approve", question: "진행할까요?" } })], ["POST", /\/runs\/run1\/decision$/, () => deferred]] });
  h.render(); await h.settle(); await firstResult(h);
  h.btn(document.querySelector("#table"), "승인").click();
  await tab(h, "작업");
  resolve({}); await h.settle();
  assert.ok(document.querySelector(".wf-grid"));
  assert.equal(document.querySelector(".flow-history-detail"), null);
  assert.equal(h.calls.filter(x => x.method === "GET" && /\/result\?/.test(x.url)).length, 1);
  h.closeAll();
});
