const test = require("node:test");
const assert = require("node:assert/strict");
const { boot, dom } = require("./testdata/crew-harness.cjs");
const delivery = () => ({ id: "delivery-1", revision: 17, delivery: { status: "unknown", part: 1, message_ids: ["123456789012345678"] } });
async function ready(post, status = "unknown") {
  let current = delivery(); current.delivery.status = status;
  const h = await boot({ view: "crew", routes: [["GET", /\/runs\/run1\/result\?/, () => ({ run_id: "run1", revision: 8, status: "succeeded", result: "완료한 결과", delivery: current })], ["POST", /\/deliveries\/delivery-1\/recover$/, (_m, _url, body) => post(body, value => { current = value; })]] });
  h.render(); await h.settle(); h.btn(document.querySelector("#chips"), "결과함").click(); await h.settle();
  document.querySelector(".flow-history-row").click(); await h.settle();
  return h;
}
function choose(action) {
  const select = document.querySelector('[aria-label="전달 복구 방법"]'); select.value = action; dom.fire(select, "change");
}
function input(name, value) { const field = document.querySelector(`[name="${name}"]`); field.value = value; dom.fire(field, "input"); }
function confirm() { document.querySelector('[aria-label="복구 내용 확인"]').click(); }
const send = h => h.btn(document.querySelector("#table"), "확인한 내용으로 복구 요청");

test("delivery retry requires explicit confirmation and the displayed delivery revision", async () => {
  const h = await ready((body, save) => {
    assert.deepEqual(body, { scope: h.state.scope, expected_revision: 17, action: "retry", confirmed: true });
    save({ ...delivery(), revision: 18, delivery: { status: "pending" } }); return {};
  });
  assert.equal(send(h).disabled, true); assert.equal(h.calls.filter(c => c.method === "POST").length, 0);
  choose("retry"); assert.equal(send(h).disabled, true);
  confirm(); assert.equal(send(h).disabled, false);
  send(h).click(); await h.settle();
  assert.equal(h.calls.filter(c => c.method === "POST").length, 1);
  assert.match(document.querySelector("#table").textContent, /전달 대기/);
  assert.match(h.notices.join(" "), /실제 전달 상태/);
  h.closeAll();
});

test("receipt and destination recovery require explicit values with renewed confirmation", async () => {
  const bodies = [];
  const h = await ready(body => { bodies.push(body); return {}; });
  choose("ack_part"); confirm(); assert.equal(send(h).disabled, true);
  input("message_id", "123456789012345679"); send(h).click(); await h.settle();
  assert.equal(bodies[0].message_id, "123456789012345679"); assert.equal(bodies[0].action, "ack_part");
  choose("redirect");
  for (const name of ["bot_id", "guild_id", "channel_id", "thread_id"]) assert.equal(document.querySelector(`[name="${name}"]`).value, "");
  confirm(); input("bot_id", "confirmed-bot"); input("guild_id", "123456789012345680"); input("channel_id", "123456789012345681");
  assert.equal(send(h).disabled, true);
  input("thread_id", "123456789012345682"); assert.equal(send(h).disabled, false);
  choose("retry"); assert.equal(send(h).disabled, true, "changing the action revokes the previous confirmation");
  choose("redirect");
  input("bot_id", "confirmed-bot"); input("guild_id", "123456789012345680"); input("channel_id", "123456789012345681"); input("thread_id", "123456789012345682"); confirm();
  send(h).click(); await h.settle();
  assert.deepEqual(bodies[1].destination, { bot_id: "confirmed-bot", guild_id: "123456789012345680", channel_id: "123456789012345681", thread_id: "123456789012345682" });
  assert.equal(bodies[1].expected_revision, 17); assert.equal(bodies[1].confirmed, true);
  h.closeAll();
});

test("a stale recovery reply never reopens the previous connection's result", async () => {
  let resolve;
  const response = new Promise(done => { resolve = done; });
  const h = await ready(() => response);
  choose("retry"); confirm(); send(h).click();
  assert.equal(send(h).disabled, true);
  h.state.token = "different-connection";
  h.btn(document.querySelector("#chips"), "작업").click(); await h.settle();
  resolve({}); await h.settle();
  assert.ok(document.querySelector(".wf-grid"));
  assert.equal(document.querySelector(".flow-history-detail"), null);
  assert.equal(h.notices.length, 0);
  h.closeAll();
});

test("failed recovery does not automatically retry or report delivery success", async () => {
  const h = await ready(() => new Error("revision conflict"), "failed");
  assert.equal([...document.querySelector('[aria-label="전달 복구 방법"]').children].some(option => option.value === "ack_part"), false);
  choose("retry"); confirm(); send(h).click(); await h.settle();
  assert.equal(h.calls.filter(c => c.method === "POST").length, 1);
  assert.match(h.notices.join(" "), /결과를 확인하지 못했어요/);
  assert.doesNotMatch(h.notices.join(" "), /저장했어요/);
  assert.match(document.querySelector("#table").textContent, /전달 실패/);
  h.closeAll();
});
