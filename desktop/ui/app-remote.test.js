// The app (entry.mjs) connected to a server over SSH: the reconnect at start, the connect form, SSH 테스트,
// the error each failure shows, and switching between the server and the built-in Arkism.
// Run: node --test desktop/ui/*.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const { boot } = require("./testdata/app-harness.js");

// ArkismPages and ArkismHome get the connection's functions and state; kept so a test can call them as 설정 › 연결 and 홈 do.
let pages, home;
let app, t;
test.before(async () => {
  app = await boot({
    remote: true,
    invoke: { ssh_host_key_check: { state: "trusted" }, ssh_host_key_approve: null, ssh_host_key_cancel: null },
    beforeEntry: (env) => {
      const keep = (name, put) => { let fn; Object.defineProperty(env.window, name, { configurable: true, get: () => fn, set: (f) => { fn = (d) => { put(d); return f(d); }; } }); };
      keep("ArkismPages", (d) => { pages = d; });
      keep("ArkismHome", (d) => { home = d; });
    }
  });
  t = app.dom.text;
  await app.settle(2000);
});
test.after(() => app?.stop());

const ran = (cmd) => app.calls.filter(([c]) => c === cmd).length;
const fields = () => [...app.$$("#ssh-form input")];
const fill = (host, port = "") => { const [h, , p] = fields(); h.value = host; p.value = port; };
const submit = async (ms = 1500) => { app.$("#unlock-form").dispatchEvent(new app.window.Event("submit", { bubbles: true, cancelable: true })); await app.settle(ms); };
const locked = () => app.$("#unlock").hidden === false;

test("the SSH form shows the server address first; the rest waits under 자세히", () => {
  const box = pages.sshFields(null).node;
  const more = box.querySelector("details.ssh-more");
  assert.ok(more, "자세히");
  assert.equal(more.open, false, "a new form keeps the rest folded");
  assert.equal(box.querySelector(":scope > label.ssh-row strong").textContent, "서버 주소");
  assert.equal(more.querySelectorAll("input").length, 5, "user, port, key, password and path sit inside");
  assert.equal(pages.sshFields({ host: "h", user: "me" }).node.querySelector("details.ssh-more").open, true, "a saved user opens it");
});

test("a saved server connection reconnects at start", () => {
  assert.ok(ran("ssh_connect") >= 1);
  assert.equal(locked(), false);
  assert.equal(pages.state.mode, "remote");
});

test("연결 끊기 goes back to the address form, filled with the saved server", async () => {
  pages.disconnect();
  await app.settle(200);
  assert.ok(locked());
  assert.ok(ran("ssh_disconnect") >= 1);
  assert.equal(app.window.localStorage.getItem("arkism.token"), null);
  assert.equal(fields()[0].value, "my-server");
  assert.equal(fields()[1].value, "me");
  assert.equal(app.$("#local-link").hidden, true); // no built-in Arkism on this PC
});

test("SSH 테스트 checks the fields first, then asks the server", async () => {
  const test1 = async () => { await app.click("#ssh-test", 300); return t(app.$("#ssh-test-result")); };
  fill("");
  assert.match(await test1(), /서버 주소를 넣어 주세요/);
  assert.ok(app.$("#ssh-test-result").classList.contains("warn"));
  fill("my-server", "99999");
  assert.match(await test1(), /포트는 1~65535/);
  fill("my-server", "abc");
  assert.match(await test1(), /포트는/);
  fill("my-server", "2222");
  assert.match(await test1(), /확인했어요 \(my-server\)/);
  assert.equal(app.$("#ssh-test-result").classList.contains("warn"), false);
  app.fake.ssh_test = new Error("ssh_unreachable");
  assert.match(await test1(), /서버에 닿지 못했어요/);
  app.fake.ssh_test = { host: "my-server" };
});

test("연결하기 shows why a connection failed and keeps the form", async () => {
  const cases = [
    ["ssh_auth_failed", /SSH 로그인이 거부됐어요/],
    ["arkism_connect_failed: no token", /접속 정보를 주지 못했어요 \(no token\)/],
    ["arkism_connect_failed", /접속 정보를 주지 못했어요 \(알 수 없음\)/],
    ["ssh_mystery", /연결하지 못했어요 \(ssh_mystery\)/],
    ["socket closed", /연결하지 못했어요 \(socket closed\)/]
  ];
  fill("my-server");
  for (const [code, re] of cases) {
    app.fake.ssh_connect = new Error(code);
    await submit();
    assert.match(t(app.$("#unlock-error")), re);
    assert.ok(locked());
    assert.equal(t(app.$("#ssh-button")), "연결하기");
  }
  fill("");
  await submit(200);
  assert.match(t(app.$("#unlock-error")), /서버 주소를 넣어 주세요/);
});

test("a server that logs in but does not answer keeps the old connection", async () => {
  fill("my-server");
  app.fake.ssh_connect = { server: "http://127.0.0.1:9", token: "x", workspace_id: "w", project_id: "p", ssh_host: "my-server" };
  await submit(3000);
  assert.match(t(app.$("#unlock-error")), /Arkism을 열지 못했어요/);
  assert.equal(pages.state.token, "");
  // the app brings the previous tunnel back instead of keeping the new one
  assert.deepEqual(app.calls.filter(([c]) => c === "ssh_connect_done").at(-1)[1], { ok: false });
});

test("연결하기 connects, saves the server and starts the first-run steps", async () => {
  app.fake.ssh_connect = { server: app.origin, token: "preview", workspace_id: "default", project_id: "default", ssh_host: "my-server" };
  fill("me@my-server", "22");
  await submit(2000);
  assert.equal(locked(), false);
  for (let i = 0; i < 30 && JSON.parse(app.window.localStorage.getItem("arkism.ssh")).port !== 22; i++) await app.settle(500); // open() waits for every list
  assert.equal(JSON.parse(app.window.localStorage.getItem("arkism.ssh")).port, 22);
  assert.equal(app.window.localStorage.getItem("arkism.mode"), "remote");
  assert.deepEqual(app.calls.filter(([c]) => c === "ssh_connect_done").at(-1)[1], { ok: true });
  assert.equal(pages.state.start, 2);
  pages.state.start = 0;
});

test("지금 다시 연결 uses the same connection, and a failure is a notice", async () => {
  const n = ran("ssh_connect");
  const b = app.document.createElement("button");
  app.document.body.append(b);
  await home.reconnect(b);
  assert.equal(ran("ssh_connect"), n + 1);
  assert.equal(b.disabled, false);
  app.fake.ssh_connect = new Error("ssh_host_key_changed");
  await home.reconnect();
  assert.match(t(app.$("#notice")), /SSH 키가 예전과 달라/);
  app.fake.ssh_connect = { server: app.origin, token: "preview", workspace_id: "default", project_id: "default", ssh_host: "my-server" };
  // without a saved address it only reloads the lists
  const ssh = pages.state.ssh;
  pages.state.ssh = null;
  await home.reconnect(b);
  pages.state.ssh = ssh;
  b.remove();
});

test("a confirm that fails keeps the opened server, and connects do not overlap", async () => {
  const n = app.calls.length;
  app.fake.ssh_connect_done = ({ ok }) => { if (ok) throw "agents_write_failed"; return null; };
  await home.reconnect();
  assert.deepEqual(app.calls.slice(n).filter(([c]) => c === "ssh_connect_done").map(([, a]) => a), [{ ok: true }]);
  assert.match(t(app.$("#notice")), /마무리하지 못했어요 \(agents_write_failed\)/);
  assert.equal(pages.state.token, "preview");
  app.fake.ssh_connect_done = null;
  const first = pages.sshConnect({ ...pages.state.ssh, password: null });
  await assert.rejects(pages.sshConnect({ ...pages.state.ssh, password: null }), /이미 연결하는 중/);
  await first;
  await pages.sshConnect({ ...pages.state.ssh, password: null });
});

test("sshFields and sshLabel describe a saved connection", () => {
  assert.equal(pages.sshLabel({ host: "srv", user: "me", port: 2200 }), "me@srv:2200");
  assert.equal(pages.sshLabel({ host: "me@srv" }), "me@srv");
  assert.equal(pages.sshLabel(null), "");
  const f = pages.sshFields({ host: "srv", port: 2200, key: "~/.ssh/k", bin: "/opt/arkism" });
  assert.deepEqual(f.read(), { host: "srv", user: null, port: 2200, key: "~/.ssh/k", bin: "/opt/arkism", auth: "key", password: null });
  f.focus();
  // a connection saved with password auth reads back the typed password and starts on that field
  const p = pages.sshFields({ host: "srv", auth: "password" });
  app.document.body.append(p.node);
  const pass = p.node.querySelector('input[type="password"]');
  pass.value = "pw";
  assert.deepEqual(p.read(), { host: "srv", user: null, port: null, key: null, bin: null, auth: "password", password: "pw" });
  p.focus();
  assert.equal(app.document.activeElement, pass);
  p.node.remove();
});

test("서버에 연결하기 and 이 PC에서 쓰기 switch between the server and the built-in Arkism", async () => {
  // the built-in Arkism fails to start: the form gives way to 다시 시도 and 서버에 연결하기
  // a context change under an in-flight memory read stores null for an empty proposal ref (memoryPaging hydrateMaintenance); let it land
  await app.settle(3000);
  pages.state.defaults.local = true;
  app.fake.local_start = new Error("port in use");
  await pages.startLocal();
  await app.settle(200);
  assert.ok(locked());
  assert.equal(app.$("#local-retry").hidden, false);
  // a plain sentence up front, the raw code only under 자세히, and the check and log buttons beside 다시 시도
  assert.match(t(app.$("#unlock-title")), /이 PC의 Arkism을 켜지 못했어요/);
  assert.match(t(app.$("#unlock-intro")), /켜는 중에 문제가 생겼어요/);
  assert.doesNotMatch(t(app.$("#unlock-intro")), /port in use/);
  assert.equal(t(app.$("#local-code")), "port in use");
  assert.equal(app.$("#local-detail").hidden, false);
  assert.equal(app.$("#local-doctor").hidden, false);
  assert.equal(app.$("#local-logs").hidden, false);
  // 점검하고 고치기 runs doctor --fix and says what it did in one line
  app.fake.doctor = { summary: { ok: 4, warn: 0, fail: 0 }, checks: [], fixes: [{ message: "다시 연결" }] };
  await app.click("#local-doctor", 200);
  assert.ok(app.calls.some(([c, a]) => c === "doctor" && a.fix === true));
  assert.match(t(app.$("#local-doctor-result")), /1가지를 고쳤어요/);
  app.fake.doctor = { summary: { fail: 1 }, checks: [{ status: "fail", message: "기억 저장소가 꺼져 있어요" }], fixes: [] };
  await app.click("#local-doctor", 200);
  assert.match(t(app.$("#local-doctor-result")), /남은 문제 1개: 기억 저장소가 꺼져 있어요/);
  app.fake.open_logs = null;
  await app.click("#local-logs", 50);
  assert.ok(app.calls.some(([c]) => c === "open_logs"));
  app.fake.local_start = { server: app.origin, token: "preview" };
  await app.click("#local-retry", 2000);
  await app.settle(3000); // let the maintenance records land before the next failed start empties the token
  assert.equal(locked(), false);
  assert.equal(pages.state.mode, "local");
  // 서버에 연결하기 from the failed screen locks to the address form
  app.fake.local_start = new Error("again");
  await app.settle(3000);
  await pages.startLocal(); await app.settle(200);
  await app.click("#remote-link", 200);
  assert.equal(pages.state.mode, "remote");
  assert.equal(app.$("#local-link").hidden, false);
  // 이 PC에서 쓰기 reloads into the built-in Arkism; so does 연결 끊기 when this PC has one
  const r = app.window._reloaded || 0;
  await app.click("#local-link", 100);
  assert.equal(app.window._reloaded, r + 1);
  pages.useLocal();
  app.$("#unlock").hidden = true; // hide the address form so a lock() from 연결 끊기 would show
  pages.disconnect();
  assert.equal(app.window._reloaded, r + 3);
  assert.equal(locked(), false); // 연결 끊기 with a built-in Arkism reloads instead of locking
  // in local mode 다시 연결 starts the built-in Arkism again
  app.fake.local_start = { server: app.origin, token: "preview" };
  pages.state.mode = "local";
  await home.reconnect();
  await app.settle(1000);
  assert.equal(locked(), false);
  pages.state.defaults.local = false;
});
