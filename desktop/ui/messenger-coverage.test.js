// 설정 › 메신저: discord, telegram and slack cards driven through their buttons against a fake API.
const test = require("node:test");
const assert = require("node:assert/strict");
const { createDocument, house, flush } = require("./testdata/views-dom.js");

const doc = createDocument();
const make = doc.createElement;
doc.createElement = (t) => { const n = make(t); n.style.setProperty = (k, v) => { n.style[k] = v; }; return n; };
globalThis.window = globalThis;
globalThis.document = doc;
require("./brand.js");
require("./messenger.js");

const all = (root, sel = "button") => [root, ...root.querySelectorAll("*")].filter((n) => n.matches?.(sel));
const btn = (root, text) => all(root).find((b) => b.textContent.trim() === text);
const has = (root, text) => root.textContent.includes(text);

// routes: path prefix → value, or a function (path, opts) returning a value or throwing.
function setup(routes) {
  const calls = [], notices = [];
  const ctx = {
    state: { scope: { workspace_id: "w" }, view: "settings", settingsTab: "messenger" },
    ...house(doc),
    notice: (t) => notices.push(t),
    render: () => {},
    request: async (path, opts) => {
      calls.push([path, opts && JSON.parse(opts.body)]);
      const key = Object.keys(routes).filter((k) => path.startsWith(k)).sort((a, b) => b.length - a.length)[0];
      const r = routes[key];
      return typeof r === "function" ? r(path, opts) : r;
    },
  };
  const m = window.ArkismMessenger(ctx);
  const side = [];
  const view = () => { side.length = 0; const [wrap] = m.tab({ side: (n) => side.push(n) }); const box = doc.createElement("div"); box.append(wrap, ...side); return box; };
  return { calls, notices, view, ctx };
}
const fail = (msg) => () => { throw new Error(msg); };

test("empty server: loading, then new discord, telegram and slack forms save", async () => {
  const s = setup({ "/v1/discord": { bots: [] }, "/v1/messengers": { destinations: [] } });
  assert.ok(has(s.view(), "불러오는 중"));
  await flush();
  let v = s.view();
  assert.ok(has(v, "연결된 채널이 없어요."));
  const inputs = all(v, "input");
  // discord: too short, then a real token
  inputs[0].value = "short"; btn(v, "저장하고 연결").click();
  assert.equal(s.notices.pop(), "값을 끝까지 붙여 넣어 주세요.");
  inputs[0].value = "x".repeat(30); btn(v, "저장하고 연결").click(); await flush();
  assert.deepEqual(s.calls.find(([p]) => p === "/v1/discord/bots")[1].bot, { name: "Arkism", token: "x".repeat(30) });
  // telegram: token without a chat id, then with one typed in
  v = s.view();
  const [, tgToken, chat, slack] = all(v, "input");
  tgToken.value = "1".repeat(25);
  btn(tgToken.closest(".mg-token"), "저장하고 연결").click();
  assert.equal(s.notices.pop(), "채팅 ID를 먼저 넣어 주세요.");
  chat.value = "@room"; await chat.fire("input");
  btn(tgToken.closest(".mg-token"), "저장하고 연결").click(); await flush();
  assert.deepEqual(s.calls.at(-4)[1], { scope: { workspace_id: "w" }, channel: "telegram", bot_token: "1".repeat(25), chat_id: "@room" });
  slack.value = "https://hooks.slack.com/services/abc";
  btn(slack.closest(".mg-token"), "저장하고 연결").click(); await flush();
  assert.equal(s.calls.at(-4)[1].webhook_url, "https://hooks.slack.com/services/abc");
});

test("connected bot and destinations: change, check, invite, test, disconnect", async () => {
  const now = Date.now();
  let answer = true;
  globalThis.ask = async () => answer;
  window.ArkismDiscordInvite = (id) => `https://discord.com/invite?id=${id}`;
  const bot = { id: "b1", revision: 3, discord_bot: { name: "Ark", state: "needs_permission", guild_name: "G", guild_id: "g1", target: "t", token_last4: "abcd", application_id: "123456789",
    channels: [{ channel_id: "c1", name: "general", label: "알림" }, { channel_id: "c2" }], missing: [{ channel_id: "c1" }], last_seen_at: new Date(now - 30 * 60000).toISOString() } };
  let testReply = { ok: true };
  const s = setup({
    "/v1/discord": { bots: [{ id: "old", archived: true }, bot] },
    "/v1/messengers": { destinations: [{ channel: "telegram", name: "default", chat_id: "-100", last4: "9999" }, { channel: "slack", name: "default", last4: "wxyz" }] },
    "/v1/messengers/test": () => { if (!testReply) throw new Error("boom"); return testReply; },
    "/v1/discord/bots/": {},
  });
  s.view(); await flush();
  let v = s.view();
  assert.ok(has(v, "권한 필요") && has(v, "#general 채널에 필요한 권한이 빠졌어요") && has(v, "30분 전 확인") && has(v, "채널 2개"));
  assert.ok(has(v, "웹훅 ····wxyz") && has(v, "-100") && has(v, "#c2"));
  assert.equal(all(v, "a")[0].href, "https://discord.com/invite?id=123456789");
  // 바꾸기 → editing with a cancel button
  btn(v, "바꾸기").click();
  v = s.view();
  assert.ok(btn(v, "취소"));
  const tok = all(v, "input")[0]; tok.value = "y".repeat(24);
  btn(tok.closest(".mg-token"), "저장").click(); await flush();
  const upd = s.calls.find(([p]) => p === "/v1/discord/bots/b1/update")[1];
  assert.deepEqual(upd.bot, { name: "Ark", channels: bot.discord_bot.channels, target: "t", allowed: { user_ids: [], role_ids: [] }, guild_id: "g1", token: "y".repeat(24) });
  assert.equal(upd.expected_revision, 3);
  v = s.view();
  btn(v, "바꾸기").click(); btn(s.view(), "취소").click();
  v = s.view();
  btn(v, "권한 다시 확인").click(); await flush();
  assert.ok(s.calls.some(([p]) => p === "/v1/discord/bots/b1/check"));
  // telegram chat change, test messages, disconnects
  v = s.view();
  const chat = all(v, "input").find((i) => i.getAttribute("aria-label") === "채팅 ID");
  chat.value = " -200 "; btn(chat.closest(".mg-token"), "저장").click(); await flush();
  assert.equal(s.calls.filter(([p]) => p === "/v1/messengers").at(-1)[1].chat_id, "-200");
  v = s.view();
  btn(v, "시험 보내기").click(); await flush();
  assert.equal(s.notices.pop(), "시험 메시지를 보냈어요.");
  testReply = { ok: false, error: "403" };
  btn(v, "시험 보내기").click(); await flush();
  assert.equal(s.notices.pop(), "보내지 못했어요 (403).");
  testReply = null;
  const tb = btn(v, "시험 보내기"); tb.click(); tb.click(); await flush(); // the second click lands while the first is busy
  assert.equal(s.notices.pop(), "처리하지 못했어요 (boom).");
  const offs = all(v).filter((b) => b.textContent === "연결 끊기");
  answer = false; offs[0].click(); await flush();
  assert.ok(!s.calls.some(([, b]) => b?.archive));
  answer = true;
  for (const b of offs) { b.click(); await flush(); }
  assert.ok(s.calls.some(([, b]) => b?.archive));
  assert.equal(s.calls.filter(([p]) => p === "/v1/messengers/remove").length, 2);
});

test("ages, unknown states, permission without channel names and busy guard", async () => {
  const now = Date.now();
  const variants = [[now - 10000, "방금 확인"], [now - 5 * 3600000, "5시간 전 확인"], [now - 5 * 86400000, "5일 전 확인"]];
  for (const [t, want] of variants) {
    const s = setup({ "/v1/discord": { bots: [{ id: "b", name: "B", state: "odd", last_seen_at: new Date(t).toISOString(), missing: [{ channel_id: "zz" }] }] }, "/v1/messengers": {} });
    s.view(); await flush();
    const v = s.view();
    assert.ok(has(v, want) && has(v, "odd") && has(v, "필요한 권한이 빠졌어요. 서버") && has(v, "····"));
  }
  // a bot with no last_seen_at, no channels, an invalid application id; act() refuses while busy
  let release;
  const s = setup({ "/v1/discord": { bots: [{ id: "b", name: "B", state: "connected", application_id: "x" }] }, "/v1/messengers": { destinations: [] }, "/v1/discord/bots/": () => new Promise((r) => { release = r; }) });
  window.ArkismDiscordInvite = () => "url";
  s.view(); s.view(); await flush(); // the second tab() while loading returns early from load()
  const v = s.view();
  assert.equal(all(v, "a").length, 0);
  btn(v, "바꾸기").click();
  const v2 = s.view(), tok = all(v2, "input")[0];
  tok.value = "z".repeat(22); btn(tok.closest(".mg-token"), "저장").click();
  tok.value = "z".repeat(22); btn(tok.closest(".mg-token"), "저장").click();
  release(); await flush();
  assert.equal(s.calls.filter(([p]) => p.endsWith("/update")).length, 1);
});

test("unsupported servers, failed reads, retry and error texts", async () => {
  let dErr = "HTTP 404", mErr = "HTTP 501";
  const s = setup({ "/v1/discord": () => { throw new Error(dErr); }, "/v1/messengers": () => { throw new Error(mErr); } });
  s.view(); await flush();
  let v = s.view();
  assert.ok(has(v, "디스코드 연결을 지원하지 않아요") && has(v, "이 연결을 지원하지 않아요"));
  dErr = "down"; mErr = "offline";
  const s2 = setup({ "/v1/discord": () => { throw new Error(dErr); }, "/v1/messengers": () => { throw new Error(mErr); } });
  s2.view(); await flush();
  v = s2.view();
  assert.ok(has(v, "디스코드 상태를 불러오지 못했어요 (down)") && has(v, "연결 상태를 불러오지 못했어요 (offline)"));
  btn(v, "다시 불러오기").click(); await flush();
  assert.equal(s2.calls.length, 6); // two loads of discord, messengers and webhooks
  // errText: known codes, and a save failure reloads quietly
  for (const [code, text] of [["secret_detected", "키나 토큰처럼"], ["invalid_request", "토큰 형식을"], ["revision_conflict", "다른 곳에서 먼저"]]) {
    const s3 = setup({ "/v1/discord": { bots: [] }, "/v1/messengers": { destinations: [] }, "/v1/discord/bots": fail(code) });
    s3.view(); await flush();
    const w = s3.view(), inp = all(w, "input")[0];
    inp.value = "q".repeat(20); btn(w, "저장하고 연결").click(); await flush();
    assert.ok(s3.notices.pop().startsWith(text));
  }
  // no render outside the messenger tab
  const s4 = setup({ "/v1/discord": {}, "/v1/messengers": {} });
  s4.ctx.state.view = "home";
  s4.view(); await flush();
  assert.ok(has(s4.view(), "연결 안 됨"));
});

test("webhook shape: lines become headers and query, the wait is bounded, a patch sends only what changed", () => {
  const M = window.ArkismMessenger;
  const ok = M.hookShape({ method: "PUT", timeout: "10", headers: "Authorization: Bearer abc\n\nX-Team: core", query: "team=memory" });
  assert.deepEqual(ok.create, { method: "PUT", timeout_seconds: 10, headers: { Authorization: "Bearer abc", "X-Team": "core" }, query_params: { team: "memory" } });
  assert.deepEqual(M.hookShape({ method: "POST", timeout: "", headers: "", query: "" }).create, {}, "defaults send nothing");
  assert.match(M.hookShape({ headers: "no colon here" }).error, /이름: 값/);
  assert.match(M.hookShape({ query: "=v" }).error, /이름=값/);
  assert.match(M.hookShape({ timeout: "31" }).error, /1~30초/);
  const h = { url: "https://a.example/x", events: ["memory.approved", "candidate.created"], include_content: false, method: "", timeout_seconds: 0, header_names: ["Authorization"] };
  const same = { url: h.url, events: ["candidate.created", "memory.approved"], content: false, method: "POST", timeout: "", headers: "", query: "", clear: false, secret: "" };
  assert.deepEqual(M.hookPatch(h, same, M.hookShape(same)), {}, "nothing changed, nothing sent (event order does not count)");
  const changed = { ...same, url: "https://b.example/y", events: ["memory.deleted"], method: "PATCH", timeout: "5", query: "k=v", clear: true, secret: "s" };
  assert.deepEqual(M.hookPatch(h, changed, M.hookShape(changed)), { url: "https://b.example/y", events: ["memory.deleted"], method: "PATCH", timeout_seconds: 5, secret: "s", headers: {}, query_params: { k: "v" } });
});

test("a webhook is changed in place: 고치기 opens its form, 저장 PATCHes only the changed fields, header names show but values never", async () => {
  const hook = { id: "wh1", revision: 3, url: "https://hooks.example.com/a", events: ["candidate.created"], include_content: false, active: true, method: "PUT", timeout_seconds: 8, header_names: ["Authorization"], query_param_names: ["team"] };
  const s = setup({ "/v1/discord": { bots: [] }, "/v1/messengers": { destinations: [] }, "/v1/webhooks": { items: [hook] } });
  s.view(); await flush();
  let v = s.view();
  assert.ok(has(v, "PUT · 8초까지 기다림 · 헤더 Authorization · 쿼리 team"));
  btn(v, "고치기").click(); v = s.view();
  const box = v.querySelector(".mg-hook-form");
  assert.equal(all(box, "input").find((n) => n.getAttribute("aria-label") === "받을 주소").value, hook.url);
  assert.equal(all(box, "select")[0].value, "PUT");
  const area = all(box, "textarea").find((n) => n.getAttribute("aria-label")?.startsWith("더 보낼 헤더"));
  assert.equal(area.value, "", "a sealed header value never comes back");
  area.value = "Authorization: Bearer new"; await area.fire("input");
  const wait = all(box, "input").find((n) => n.getAttribute("aria-label")?.startsWith("답을 기다릴 시간")); wait.value = "12"; await wait.fire("input");
  btn(box, "저장").click(); await flush();
  const [path, sent] = s.calls.find(([p, b]) => p === "/v1/webhooks/wh1" && b?.expected_revision === 3);
  assert.equal(path, "/v1/webhooks/wh1");
  assert.deepEqual(sent, { scope: { workspace_id: "w" }, expected_revision: 3, timeout_seconds: 12, headers: { Authorization: "Bearer new" } });
  assert.equal(s.notices.at(-1), "웹훅을 고쳤어요.");
  assert.equal(s.view().querySelector(".mg-hook-form .mg-actions")?.textContent.includes("취소") || false, false, "the form closed after saving");
});
