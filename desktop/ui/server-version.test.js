// 서버 버전 맞추기 (modules/serverVersion.mjs): reading /healthz, the banner, and the server update button.
const test = require("node:test");
const assert = require("node:assert/strict");
const { createDocument, house, flush } = require("./testdata/views-dom.js");

const mod = import("./modules/serverVersion.mjs");

test("a server from before the check is level 0 and lacks every gated feature", async () => {
  const { readServerInfo, missingFeatures, serverOlder, bannerText } = await mod;
  const old = readServerInfo({ status: "ok" });
  assert.deepEqual(old, { version: "", commit: "", built: "", apiLevel: 0, features: [] });
  assert.deepEqual([...missingFeatures(old)], ["role_model_picks", "forget_both"]);
  assert.equal(serverOlder(old, "0.1.7"), true);
  assert.equal(bannerText(old, "0.1.7"), "서버가 앱보다 오래됐어요 · 서버 (이전 버전) / 앱 v0.1.7");
  // health not read (network): nothing is disabled and no banner claims an old server
  assert.equal(missingFeatures(readServerInfo(null)).size, 0);
  assert.equal(serverOlder(null, "0.1.7"), false);
});

test("versions compare by number; dev builds by level only", async () => {
  const { readServerInfo, serverOlder, missingFeatures, bannerText } = await mod;
  const at = (version) => readServerInfo({ status: "ok", version, api_level: 1, features: ["role_model_picks", "forget_both"] });
  assert.equal(serverOlder(at("0.1.6"), "0.1.7"), true);
  assert.equal(serverOlder(at("0.1.10"), "0.1.9"), false);
  assert.equal(serverOlder(at("0.1.7"), "0.1.7"), false);
  assert.equal(serverOlder(at("0.2.0"), "0.1.7"), false);
  assert.equal(serverOlder(at("dev"), "0.1.7"), false);
  assert.equal(missingFeatures(at("0.1.7")).size, 0);
  assert.equal(bannerText(at("0.1.5"), "0.1.6"), "서버가 앱보다 오래됐어요 · 서버 v0.1.5 / 앱 v0.1.6");
});

test("status words and the build line", async () => {
  const { readServerInfo, serverStatus, buildText, STATUS_TEXT } = await mod;
  const info = readServerInfo({ status: "ok", version: "0.1.5", commit: "ec057c47f757aa", built: "2026-10-26T09:12:00Z", api_level: 1, features: [] });
  assert.equal(buildText(info), "10월 26일 판 · ec057c4");
  assert.equal(buildText(readServerInfo({ status: "ok" })), "");
  assert.equal(STATUS_TEXT[serverStatus(info, "0.1.6")], "업데이트 있음");
  assert.equal(STATUS_TEXT[serverStatus(readServerInfo({ status: "ok" }), "0.1.6")], "앱보다 오래됨");
  assert.equal(STATUS_TEXT[serverStatus(info, "0.1.5")], "최신");
  assert.equal(STATUS_TEXT[serverStatus(null, "0.1.5")], "확인 못 함");
});

test("update errors read as plain words, unknown codes keep the code", async () => {
  const { updateErrorText } = await mod;
  assert.match(updateErrorText("needs_write /srv/arkism/bin"), /쓸 권한이 없어요/);
  assert.match(updateErrorText("needs_sudo_restart: memoryd.service"), /sudo가 필요해요/);
  assert.match(updateErrorText("health_failed_rolled_back"), /되돌렸어요/);
  assert.equal(updateErrorText("weird"), "서버를 업데이트하지 못했어요 (weird).");
});

function setup({ health, mode = "remote", ssh = mode === "remote" ? { host: "me@srv" } : null, invoke }) {
  const doc = createDocument();
  globalThis.document = doc;
  const h = house(doc);
  doc.body.append(Object.assign(h.el("div"), { id: "server-banner" }));
  const notices = [], calls = [], steps = [];
  let listener = null;
  const state = { mode, ssh };
  return import("./modules/serverVersion.mjs").then(({ createServerBanner }) => {
    const banner = createServerBanner({
      ...h, state,
      request: async (path) => { calls.push(path); if (health instanceof Error) throw health; return health; },
      tauri: { invoke: async (cmd, args) => { calls.push([cmd, args]); if (listener) { listener({ payload: "verify" }); steps.push(doc.querySelector("#server-banner").textContent); } return invoke(cmd, args); } },
      listen: async (name, fn) => { listener = fn; return () => { listener = null; }; },
      notice: (t) => notices.push(t),
      ask: async () => true,
      reconnect: async () => calls.push("reconnect"),
      sshForm: () => ({ ...state.ssh, password: null }),
      appVersion: () => "0.1.7",
      openInfo: () => calls.push("openInfo")
    });
    // 설정 › 정보 draws the server line again on every change; tests read the latest one.
    const line = () => { const r = banner.row(); doc.body.append(r); return r; };
    return { doc, banner, state, notices, calls, steps, line, box: () => doc.querySelector("#server-banner") };
  });
}

test("an older SSH server: a short banner points to 설정 › 정보, whose server line updates and reconnects", async () => {
  const t = await setup({ health: { status: "ok" }, invoke: async () => ({ version: "0.1.7" }) });
  await t.banner.check();
  assert.equal(t.box().hidden, false);
  assert.match(t.box().textContent, /서버가 앱보다 오래됐어요 · 서버 \(이전 버전\) \/ 앱 v0.1.7/);
  assert.equal(t.banner.lacks("forget_both"), true);
  t.box().querySelector("button").click();
  assert.ok(t.calls.includes("openInfo"));
  const line = t.line();
  assert.match(line.textContent, /서버 · me@srv/);
  assert.match(line.textContent, /이전 버전/);
  assert.match(line.textContent, /앱보다 오래됨/);
  const button = line.querySelector("button");
  assert.match(button.textContent, /서버 업데이트/);
  button.click();
  await flush(); await flush();
  assert.deepEqual(t.calls.find((c) => c[0] === "server_update"), ["server_update", { form: { host: "me@srv", password: null }, version: "0.1.7" }]);
  assert.equal(t.steps.length, 1);
  assert.match(t.steps[0], /서명 확인 중/);
  assert.ok(t.calls.includes("reconnect"));
  assert.deepEqual(t.notices, ["서버를 v0.1.7로 올렸어요."]);
});

test("a failed update explains the cause and keeps the banner", async () => {
  const t = await setup({ health: { status: "ok" }, invoke: async () => { throw new Error("needs_sudo_restart: memoryd.service"); } });
  await t.banner.check();
  t.line().querySelector("button").click();
  await flush(); await flush();
  assert.match(t.notices[0], /sudo가 필요해요/);
  assert.equal(t.box().hidden, false);
  // The reason stays on the server line, not only in a passing notice.
  assert.match(t.line().textContent, /sudo가 필요해요/);
  assert.ok(!t.calls.includes("reconnect"));
});

test("a current server shows nothing; a built-in one offers reconnect, not a download", async () => {
  const current = await setup({ health: { status: "ok", version: "0.1.7", commit: "abcdef1234", built: "2026-10-29T01:00:00Z", api_level: 1, features: ["role_model_picks", "forget_both"] } });
  await current.banner.check();
  assert.equal(current.box().hidden, true);
  assert.equal(current.banner.lacks("forget_both"), false);
  const ok = current.line();
  assert.match(ok.textContent, /v0\.1\.7.*10월 29일 판 · abcdef1/);
  assert.match(ok.textContent, /최신/);
  assert.equal(ok.querySelector("button"), null);
  const local = await setup({ health: { status: "ok", version: "0.1.6", api_level: 1, features: [] }, mode: "local" });
  await local.banner.check();
  const line = local.line();
  assert.match(line.textContent, /서버 · 이 PC/);
  assert.match(line.textContent, /앱과 함께 업데이트돼요/);
  assert.match(line.querySelector("button").textContent, /다시 연결/);
  line.querySelector("button").click();
  await flush();
  assert.ok(local.calls.includes("reconnect"));
});

test("a server reached by address shows why it cannot be updated from the app", async () => {
  const t = await setup({ health: { status: "ok", version: "0.1.6", api_level: 1, features: [] }, mode: "remote", ssh: null });
  await t.banner.check();
  const line = t.line();
  assert.match(line.textContent, /서버 · 서버 주소/);
  assert.match(line.textContent, /서버에서 직접 업데이트해 주세요/);
  assert.equal(line.querySelector("button").disabled, true);
});

test("serverNeeded keeps the control visible but off, with the reason", async () => {
  const { serverNeeded } = await mod;
  const h = house(createDocument());
  const b = h.button("둘 다 지우기");
  serverNeeded(b);
  assert.equal(b.disabled, true);
  assert.match(b.title, /서버 업데이트가 필요해요/);
});
