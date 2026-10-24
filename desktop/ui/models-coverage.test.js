// 설정 › 모델 설정 (models.js): providers, accounts, keys, models and the key dialog against a stand-in server.
const test = require("node:test");
const assert = require("node:assert/strict");
const { install, byText, text, flush } = require("./testdata/router-dom.js");

const dom = install();
// The runner is polled every 1.5 s: a fake clock lets a poll that never settles reach its time limit at once.
let clock = 0;
const immediate = setImmediate;
globalThis.setTimeout = (fn, ms = 0) => { clock += ms; immediate(fn); return 0; };
Date.now = () => clock;
globalThis.requestAnimationFrame = (fn) => fn();
require("./models.js");

const at = "2026-10-02T10:00:00Z";
function serverDoc() {
  return {
    collected_at: at,
    providers: [
      { id: "openai", name: "OpenAI", kind: "subscription", models: ["gpt-5", "antigravity-x"],
        accounts: [
          { id: "acc1", cli: "codex", label: "Codex", status: "connected", quotas: [{ window: "5h", left_pct: 40, resets_at: at }, { window: "week-fable", left_pct: null }], cli_info: { version: "1.0", via: "npm", update: true, latest: "1.1" } },
          { id: "acc2", cli: "gemini", status: "expired", quota: { window: "day-gemini-2.5", left_pct: 90 } },
          { id: "acc3", cli: "zz", label: "Z", status: "missing" },
          { id: "acc4", cli: "claude", status: "connected", cli_info: { version: "2", update: true, via: "odd" } },
          { id: "acc5", status: "weird" },
        ],
        keys: [{ id: "k1", name: "업무", last4: "abcd", monthly_budget_usd: 30, base_url: "https://x/v1", created_at: at }, { id: "k0", name: "옛", last4: "0000" }] },
      { id: "anthropic", name: "Anthropic", kind: "subscription", accounts: [{ id: "a1", cli: "claude", status: "connected", cli_info: { version: "3", via: "self", update: true } }], keys: [], models: ["opus"] },
      { id: "xai", name: "xAI", accounts: [], models: ["grok"] },
      { id: "custom-abc", name: "Custom", custom: true, accounts: [], keys: [{ id: "k2", name: "c", last4: "1111" }], models: ["m1", "m2"] },
    ],
    requests: [
      { id: "r1", kind: "model_test", provider: "openai", model: "gpt-5", status: "done", note: "1.2초" },
      { id: "r2", kind: "model_test", provider: "openai", model: "antigravity-x", status: "pending" },
      { id: "r3", kind: "model_test", provider: "anthropic", model: "opus", status: "done" },
      { id: "r4", kind: "model_test", provider: "xai", model: "grok", status: "failed" },
      { id: "r5", kind: "cli_update", cli: "claude", status: "failed", error: "e" },
      { id: "r6", kind: "cli_update", cli: "claude", status: "failed" },
      { kind: "key_add", provider: "openai", status: "pending", name: "n", last4: "9999" },
      { kind: "key_add", provider: "openai", status: "failed", name: "n", last4: "8888" },
      { kind: "key_add", provider: "openai", status: "expired", name: "n", last4: "7777", error: "x" },
    ],
    disabled: ["openai/antigravity-x"], extra: ["openai/hand", "custom-abc/m1"], labels: { "model:openai/gpt-5": "GPT 5", "account:openai/acc1": "내 계정" },
    assist_model: "gpt-5", assist_provider: "openai",
  };
}

function mk({ mode = "server", fail = {}, outcome = "done" } = {}) {
  const server = serverDoc();
  const state = { data: {}, scope: { project_id: "p" }, mode };
  const nodes = { main: dom.el("div"), side: dom.el("div") };
  const notes = [], calls = [], went = [];
  let n = 0;
  const v = { state, nodes, notes, calls, went, server, outcome, fail };
  const ctx = {
    state, el: dom.el, icon: dom.icon, button: dom.button, agentLogo: dom.agentLogo, ago: () => "방금", go: (x) => went.push(x), notice: (m) => notes.push(m),
    query: () => "",
    request: async (path, opt) => {
      const body = opt && JSON.parse(opt.body);
      calls.push([path, body]);
      const f = v.fail[path];
      if (f) throw new Error(f);
      if (path.startsWith("/v1/providers?")) {
        for (const q of server.requests) if (q.id?.startsWith("q") && q.status === "pending" && v.outcome !== "stay") { q.status = v.outcome; q.models = ["m-a", "m-b"]; q.error = v.outcome === "failed" ? "" : undefined; }
        return { providers: server };
      }
      server.requests.push({ id: `q${++n}`, kind: body.cli ? "cli_update" : "x", cli: body.cli, provider: body.provider, model: body.model, status: "pending", key_id: "key_ABCDEF1234567890" });
      if (path === "/v1/providers/cli_update" && v.noProviders) return {};
      return { providers: server };
    },
    render: () => draw(),
  };
  const kit = { head() {}, side: (...x) => nodes.side.replaceChildren(...x), sideSec: (t, ...kids) => { const s = dom.el("section"); s.append(dom.el("h4", "", t), ...kids); return s; } };
  const draw = () => nodes.main.replaceChildren(...v.m.tab(kit));
  v.m = window.ArkismModels(ctx);
  v.draw = draw; v.ctx = ctx;
  return v;
}
const settle = async (k = 40) => { for (let i = 0; i < k; i++) await flush(); };
const click = (root, t) => { const all = byText(root, t), n = all.find((x) => x.tagName === "BUTTON") || all[0]; assert.ok(n, `no node with text ${t}`); n.click(); return n; };
const label = (root, l) => root.all((x) => x.getAttribute("aria-label") === l)[0];
const dialog = () => document.body.children.at(-1);

test("the tab lists providers, accounts, quotas, CLI versions, keys and models", async () => {
  globalThis.ask = async () => true;
  const v = mk();
  await v.m.load();
  v.draw();
  const t = text(v.nodes.main);
  assert.match(t, /5시간 한도/); assert.match(t, /주간 한도 · fable/); assert.match(t, /일일 한도 · gemini-2\.5/);
  assert.match(t, /Codex 1\.0 · npm/); assert.match(t, /새 버전 1\.1/); assert.match(t, /Claude Code 2 · odd/); assert.match(t, /지난 업데이트 실패: 알 수 없음/);
  assert.match(t, /Gemini CLI를 한 번 실행하면/); assert.match(t, /zz 로그인을 다시/); assert.match(t, /CLI 로그인을 다시/);
  assert.match(t, /GPT 5/); assert.match(t, /직접 추가/); assert.match(t, /시험 중/); assert.match(t, /1\.2초/); assert.match(t, /3개 중 2개 켜짐/);
  const side = text(v.nodes.side);
  assert.match(side, /••••abcd  ·  \$30\/월/); assert.match(side, /x\/v1 · 방금/); assert.match(side, /서버에 저장하는 중/); assert.match(side, /저장 실패: 알 수 없음/); assert.match(side, /다시 넣어 주세요/);
  // AI 도구 기본 모델 moved from this title row to the top of 설정 › 에이전트 (pages.js, 29ace25); the tab only hands out the picker.
  assert.doesNotMatch(t, /AI 도구 기본 모델/);
  const pick = v.m.assistPicker();
  assert.equal(pick.getAttribute("aria-label"), "AI 도구 기본 모델");
  assert.deepEqual([...pick.options].map((o) => o.value), ["", "anthropic|opus", "openai|gpt-5", "openai|hand", "xai|grok", "custom-abc|m1"]);
  assert.equal(pick.value, "openai|gpt-5");
  assert.deepEqual(v.m.enabledModels(), ["opus", "gpt-5", "hand", "grok", "m1"]);
  assert.deepEqual(v.m.enabledByProvider().map((p) => p.id), ["anthropic", "openai", "xai", "custom-abc"]);
  // each provider heading carries its 모델 설정 chip logo: the brand tile, or the stack fallback for xAI and custom ones
  const heads = pick.children.filter((c) => c.tagName === "OPTGROUP");
  assert.deepEqual(heads.map((g) => g.logo.className), ["agent-tile", "agent-tile", "agent-tile mono-logo", "agent-tile mono-logo"]);
  // every chip draws its provider
  for (const name of ["Anthropic", "Google", "Z.ai", "xAI", "Custom"]) { click(v.nodes.main, name); assert.match(text(v.nodes.main), /계정/); }
  assert.match(text(v.nodes.main), /직접 추가한 제공자는 키로만 연결해요/);
  click(v.nodes.main, "xAI");
  assert.match(text(v.nodes.side), /저장된 키가 없어요\./);
  click(v.nodes.main, "Anthropic");
  assert.match(text(v.nodes.side), /구독 로그인만 쓰면/);
  assert.match(text(v.nodes.main), /Claude Code 3 · 자체 업데이트/);
});

test("an old server says the tab is unsupported; a missing report says the runner has not reported", async () => {
  const v = mk({ fail: { "/v1/providers?": "x" } });
  v.fail["/v1/providers?"] = "x";
  v.ctx.request = async () => { throw new Error("HTTP 404"); };
  assert.equal(await v.m.load(), null);
  v.draw();
  assert.match(text(v.nodes.main), /아직 모델 설정을 지원하지 않아요/);
  const w = mk();
  w.state.data.providers = { providers: [{ id: "zz", name: "ZZ" }] };
  w.draw();
  assert.match(text(w.nodes.main), /아직 보고하지 않았어요/);
  assert.match(text(w.nodes.main), /서버 실행기가 알려 준 모델이 없어요/);
  const fresh = mk();
  fresh.state.data.providers = undefined;
  assert.deepEqual(fresh.m.enabledModels(), []);
});

test("models: test a connection, switch, add, remove, rename and filter", async () => {
  const v = mk();
  await v.m.load(); v.draw();
  const main = () => v.nodes.main;
  // connection test: done, failed, no answer, error
  label(main(), "gpt-5 연결 시험").click(); await settle();
  assert.match(v.notes.at(-1), /gpt-5 연결 정상 · 응답 받음/);
  v.outcome = "failed"; label(main(), "gpt-5 연결 시험").click(); await settle();
  assert.match(v.notes.at(-1), /gpt-5 시험 실패: 알 수 없음/);
  v.outcome = "stay"; label(main(), "gpt-5 연결 시험").click(); await settle(1000);
  assert.match(v.notes.at(-1), /아직 답하지 않았어요/);
  v.outcome = "done";
  v.fail["/v1/providers/test"] = "access_denied"; label(main(), "gpt-5 연결 시험").click(); await settle();
  assert.equal(v.notes.at(-1), "이 계정은 모델 설정을 바꿀 수 없어요.");
  delete v.fail["/v1/providers/test"];
  click(main(), "연결 시험"); await settle(); // the account's own test button
  // switches
  label(main(), "gpt-5 켜기").click(); await settle();
  assert.equal(v.calls.at(-1)[1].enabled, false);
  v.fail["/v1/providers/models"] = "odd"; label(main(), "gpt-5 켜기").click(); await settle();
  assert.equal(v.notes.at(-1), "처리하지 못했어요 (odd).");
  delete v.fail["/v1/providers/models"];
  // add a model: bad name, Enter while composing, then a good one
  click(main(), "모델 추가");
  const mi = main().all((x) => x.placeholder === "모델 이름 (예: glm-5.3)")[0];
  mi.value = "  "; click(main(), "넣기"); await settle();
  assert.match(v.notes.at(-1), /글자·숫자/);
  mi.value = "glm-5.3"; mi.fire("keydown", { key: "Enter", isComposing: true }); mi.fire("keydown", { key: "a" });
  mi.fire("keydown", { key: "Enter" }); await settle();
  assert.equal(v.notes.at(-1), "glm-5.3를 넣었어요. 워크플로우 모델 목록에 보여요.");
  // remove a hand-added model, asked first
  globalThis.ask = async () => false; label(main(), "hand 빼기").click(); await settle();
  globalThis.ask = async () => true; label(main(), "hand 빼기").click(); await settle();
  assert.deepEqual(v.calls.at(-1)[1].remove, true);
  // rename a model: cancel, save, fail
  globalThis.askText = async () => null; label(main(), "gpt-5 이름 고치기").click(); await settle();
  globalThis.askText = async () => " 새 이름 "; label(main(), "gpt-5 이름 고치기").click(); await settle();
  assert.equal(v.calls.at(-1)[1].label, "새 이름");
  v.fail["/v1/providers/labels"] = "secret_detected"; label(main(), "gpt-5 이름 고치기").click(); await settle();
  assert.equal(v.notes.at(-1), "이름이나 주소에 키처럼 보이는 값이 있어요.");
  // account name: save, fail
  const nameIn = label(main(), "계정 이름"); nameIn.value = "회사"; click(main(), "이름 저장"); await settle();
  assert.equal(v.notes.at(-1), "secret_detected" === v.fail["/v1/providers/labels"] ? "이름이나 주소에 키처럼 보이는 값이 있어요." : "이름을 저장했어요.");
  delete v.fail["/v1/providers/labels"];
  click(main(), "이름 저장"); await settle();
  assert.equal(v.notes.at(-1), "이름을 저장했어요.");
  // the filter hides rows, keeps its text over a redraw and refocuses
  const f = label(main(), "모델 찾기");
  f.fire("focus"); f.value = "GPT"; f.fire("input");
  assert.equal(main().all((x) => x.classList.contains("mp-model-row")).filter((r) => !r.hidden).length, 1);
  f.fire("blur", { relatedTarget: null });
  v.draw();
  assert.equal(label(main(), "모델 찾기").focused, true);
  document.activeElement = dom.el("input"); v.draw(); document.activeElement = null;
  label(main(), "모델 찾기").fire("blur", { relatedTarget: {} });
  label(main(), "모델 찾기").value = ""; label(main(), "모델 찾기").fire("input");
});

test("직접 추가 providers list the turned-on models first and toggle by adding or removing", async () => {
  const v = mk();
  await v.m.load(); v.draw();
  click(v.nodes.main, "Custom");
  const rows = v.nodes.main.all((x) => x.classList.contains("mp-model-row")).map((r) => r.dataset.model);
  assert.deepEqual(rows, ["m1", "m2"]);
  label(v.nodes.main, "m1 켜기").click(); await settle();
  assert.equal(v.calls.at(-1)[1].remove, true);
  label(v.nodes.main, "m2 켜기").click(); await settle();
  assert.equal(v.calls.at(-1)[1].add, true);
  const many = mk();
  many.state.data.providers = { ...serverDoc(), providers: [{ id: "custom-zz", name: "Many", models: Array.from({ length: 25 }, (_, i) => `x${i}`) }] };
  many.draw(); click(many.nodes.main, "Many");
  assert.equal(label(many.nodes.main, "모델 찾기").placeholder, "모델 25개에서 찾기");
});

test("accounts: refresh, CLI update, account help and key removal", async () => {
  const v = mk({ mode: "local" });
  await v.m.load(); v.draw();
  const main = () => v.nodes.main;
  assert.match(main().all((x) => x.textContent === "새로고침")[0].title, /이 PC에서 방금 확인/);
  window.__TAURI__ = { core: { invoke: async () => { throw new Error("x"); } } };
  click(main(), "새로고침"); await settle();
  assert.match(v.notes.at(-1), /한도를 새로 확인하지 못했어요/);
  window.__TAURI__ = { core: { invoke: async () => ({}) } };
  click(main(), "다시 불러오기"); await settle();
  delete window.__TAURI__;
  click(main(), "새로고침"); await settle();
  // CLI update: done, failed, conflict, other error, a reply without providers
  click(main(), "CLI 업데이트"); await settle();
  assert.equal(v.notes.at(-1), "Codex를 업데이트했어요.");
  v.outcome = "failed"; click(main(), "CLI 업데이트"); await settle();
  assert.equal(v.notes.at(-1), "업데이트하지 못했어요: 알 수 없음");
  v.outcome = "done";
  for (const [err, re] of [["revision_conflict", /이미 업데이트 중/], ["boom", /처리하지 못했어요/]]) {
    v.fail["/v1/providers/cli_update"] = err; click(main(), "CLI 업데이트"); await settle();
    assert.match(v.notes.at(-1), re);
  }
  delete v.fail["/v1/providers/cli_update"];
  v.noProviders = true; click(main(), "CLI 업데이트"); await settle();
  assert.match(v.notes.at(-1), /이 PC에서 설치하면/);
  v.noProviders = false;
  v.draw();
  assert.match(text(main()), /업데이트 중…/); // the queued request stays pending in this fixture
  // account help: with CLIs, then 키 넣기 opens the key dialog
  click(main(), "계정 추가");
  assert.match(text(dialog()), /codex CLI/); assert.match(text(dialog()), /zz login/);
  click(dialog(), "키 넣기");
  assert.match(text(dialog()), /OpenAI API 키 추가/);
  dialog().close();
  click(main(), "xAI"); click(main(), "계정 추가");
  assert.match(text(dialog()), /xAI는 API 키로 연결해요/);
  dialog().close();
  // key removal: asked, done, failed, error
  click(main(), "OpenAI");
  globalThis.ask = async () => false; label(v.nodes.side, "업무 키 지우기").click(); await settle();
  globalThis.ask = async () => true;
  label(v.nodes.side, "업무 키 지우기").click(); await settle();
  v.outcome = "failed"; label(v.nodes.side, "업무 키 지우기").click(); await settle();
  assert.equal(v.notes.at(-1), "지우지 못했어요: 알 수 없음");
  v.fail["/v1/providers/keys/remove"] = "invalid_request"; label(v.nodes.side, "업무 키 지우기").click(); await settle();
  assert.match(v.notes.at(-1), /칸 형식을 확인해/);
  const plain = mk(); plain.state.data.providers = { ...serverDoc(), collected_at: "" }; plain.draw();
  assert.equal(plain.nodes.main.all((x) => x.textContent === "새로고침")[0].title, "지금 확인");
  const srv = mk(); await srv.m.load(); srv.draw();
  assert.match(srv.nodes.main.all((x) => x.textContent === "새로고침")[0].title, /서버에서 방금 확인/);
});

test("chips and AI 도구 headings show the LobeHub mark by id, name or key base URL, else the generic logo", async () => {
  window.ArkismBrand = { kindLabel: () => "", mark: (n) => (/^(anthropic|xai|deepseek)/.test(String(n).toLowerCase()) ? dom.el("i", `mark-${n}`) : null) };
  try {
    const v = mk();
    v.server.providers.find((p) => p.id === "custom-abc").keys[0].base_url = "https://api.deepseek.com/v1";
    await v.m.load(); v.draw();
    const marks = (root) => root.all((x) => /^mark-/.test(x.className)).map((x) => x.className);
    assert.deepEqual(marks(v.nodes.main), ["mark-anthropic", "mark-xai", "mark-deepseek.com"]); // OpenAI and Google: no mark here, their KNOWN logo
    const heads = v.m.assistPicker().children.filter((c) => c.tagName === "OPTGROUP");
    assert.deepEqual(heads.map((g) => g.logo.children[0]?.className || g.logo.className), ["mark-anthropic", "agent-tile", "mark-xai", "mark-deepseek.com"]);
  } finally { delete window.ArkismBrand; }
});

test("AI 도구 기본 모델 saves the provider and model, falls back to the server default, and keeps an old pick", async () => {
  const v = mk();
  await v.m.load(); v.draw();
  // 설정 › 에이전트 mounts the picker (pages.js); a fresh one stands in for each redraw there.
  const host = dom.el("div"), mount = () => host.replaceChildren(v.m.assistPicker());
  const sel = () => label(host, "AI 도구 기본 모델");
  for (const [value, re] of [["anthropic|opus", /Anthropic의 opus로/], ["", /서버 기본 모델로/], ["nobody|zz", /이제 zz로/]]) {
    mount(); sel().value = value; sel().fire("change"); await settle();
    assert.match(v.notes.at(-1), re);
  }
  mount(); v.fail["/v1/providers/assist_model"] = "odd"; sel().value = ""; sel().fire("change"); await settle();
  assert.equal(sel().value, "openai|gpt-5");
  v.server.assist_model = "gone"; v.server.assist_provider = ""; mount();
  assert.ok(sel().options.some((o) => o.value === "|gone"));
});

test("key dialog: peek, test, pick found models and save", async () => {
  const v = mk();
  await v.m.load(); v.draw();
  click(v.nodes.side, "키 추가");
  let d = dialog();
  const key = d.all((x) => x.placeholder === "키를 붙여 넣어요")[0], base = d.all((x) => x.placeholder === "https://…/v1")[0], budget = d.all((x) => x.placeholder === "$30")[0];
  const err = () => d.all((x) => x.classList.contains("form-error"))[0].textContent;
  click(d, "보기"); assert.equal(key.type, "text"); click(d, "숨기기"); assert.equal(key.type, "password");
  click(d, "연결 테스트"); assert.equal(err(), "키를 먼저 넣어 주세요.");
  key.value = "sk-1"; budget.value = "10";
  click(d, "연결 테스트"); await settle();
  assert.match(text(d), /모델 2개를 찾았어요/);
  click(d, "m-a"); click(d, "m-a"); click(d, "m-b");
  v.outcome = "failed"; click(d, "연결 테스트"); await settle();
  assert.match(text(d), /연결하지 못했어요/);
  v.outcome = "stay"; click(d, "연결 테스트"); await settle(1000);
  assert.match(text(d), /답이 없어요/);
  v.outcome = "done";
  v.fail["/v1/providers/keys"] = "boom"; click(d, "연결 테스트"); await settle();
  assert.match(err(), /boom/);
  // submit: other buttons, empty key, a server error, then a save that turns off the unpicked model
  const form = d.all((x) => x.tagName === "FORM")[0];
  form.fire("submit", { submitter: { value: "cancel" } });
  key.value = ""; form.fire("submit", { submitter: { value: "save" } }); assert.equal(err(), "키를 넣어 주세요.");
  key.value = "sk-1"; form.fire("submit", { submitter: { value: "save" } }); await settle();
  assert.match(err(), /boom/);
  delete v.fail["/v1/providers/keys"];
  form.fire("submit", { submitter: { value: "save" } }); await settle();
  assert.equal(v.notes.at(-1), "키를 저장했어요.");
  assert.ok(v.calls.some(([p, b]) => p === "/v1/providers/models" && b.model === "m-b" && b.enabled === false));
  // saves that fail or never settle
  for (const [outcome, re] of [["failed", /저장하지 못했어요: 알 수 없음/], ["stay", /아직 저장되지 않았어요/]]) {
    click(v.nodes.side, "키 추가"); d = dialog();
    v.outcome = outcome;
    d.all((x) => x.placeholder === "키를 붙여 넣어요")[0].value = "sk";
    d.all((x) => x.tagName === "FORM")[0].fire("submit", { submitter: { value: "save" } }); await settle(1000);
    assert.match(v.notes.at(-1), re);
  }
  v.outcome = "done";
});

test("key dialog for Z.ai checks the https address; a new provider needs an address and starts with the picked models", async () => {
  const v = mk();
  await v.m.load(); v.draw();
  click(v.nodes.main, "Z.ai"); click(v.nodes.side, "키 추가");
  let d = dialog();
  const field = (dd, p) => dd.all((x) => x.placeholder === p)[0];
  const err = (dd) => dd.all((x) => x.classList.contains("form-error"))[0].textContent;
  field(d, "키를 붙여 넣어요").value = "k"; field(d, "https://…/v1").value = "http://z.ai";
  click(d, "연결 테스트"); assert.match(err(d), /https:\/\/로 시작/);
  d.all((x) => x.tagName === "FORM")[0].fire("submit", { submitter: { value: "save" } }); assert.match(err(d), /https:\/\/로 시작/);
  d.close();
  // 공급자 추가 → a custom provider
  v.m.openKeyDialog({ id: "custom", name: "직접 추가한 제공자", custom: true });
  d = dialog();
  assert.equal(field(d, "https://…/v1").focused, true);
  assert.equal(d.all((x) => x.tagName === "DETAILS")[0].open, true);
  field(d, "키를 붙여 넣어요").value = "k";
  const form = d.all((x) => x.tagName === "FORM")[0];
  form.fire("submit", { submitter: { value: "save" } }); assert.equal(err(d), "직접 추가한 제공자는 주소가 필요해요.");
  field(d, "https://…/v1").value = "https://my/v1";
  click(d, "연결 테스트"); await settle();
  assert.match(text(d), /쓸 모델을 눌러 켜요/);
  click(d, "m-a"); // turn one on
  form.fire("submit", { submitter: { value: "save" } }); await settle();
  assert.ok(v.calls.some(([p, b]) => p === "/v1/providers/models" && b.provider === "custom-abcdef123456" && b.model === "m-a" && b.add));
  assert.ok(!v.calls.some(([, b]) => b?.model === "m-b" && b?.provider === "custom-abcdef123456"));
  // a custom key reply without a key id still names a provider
  v.server.requests.push({ id: "q-last", status: "done" });
  v.m.openKeyDialog({ id: "custom", name: "C", custom: true });
  d = dialog(); field(d, "키를 붙여 넣어요").value = "k"; field(d, "https://…/v1").value = "https://a";
  v.ctx.request = async (path) => (path.startsWith("/v1/providers?") ? { providers: v.server } : { providers: { ...v.server, requests: [{ id: "q-last" }] } });
  d.all((x) => x.tagName === "FORM")[0].fire("submit", { submitter: { value: "save" } }); await settle();
  assert.equal(v.notes.at(-1), "키를 저장했어요.");
});
