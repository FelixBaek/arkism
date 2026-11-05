// 운영 화면: 파일 → 기억 후보 상태, 웹훅 카드, 태그 이름 바꾸기·합치기와 기억 숫자 계산.
const test = require("node:test");
const assert = require("node:assert/strict");
const { createDocument, house, flush } = require("./testdata/views-dom.js");
const management = require("./memory-management.js");

const doc = createDocument();
const make = doc.createElement;
doc.createElement = (t) => { const n = make(t); n.style.setProperty = (k, v) => { n.style[k] = v; }; return n; };
globalThis.window = globalThis;
globalThis.document = doc;
require("./brand.js");
require("./messenger.js");
require("./knowledge.js");

const all = (root, sel = "button") => [root, ...root.querySelectorAll("*")].filter((n) => n.matches?.(sel));
const btn = (root, text) => all(root).find((b) => b.textContent.trim() === text);
const has = (root, text) => root.textContent.includes(text);

test("extract state maps job status to a phase and reads delta counts wherever they sit", () => {
  const s = window.ArkismKnowledge.extractState;
  assert.deepEqual(s({ id: "j", status: "running", revision: 2, job: { total: 10, next: 4, candidates_created: 3, skipped: [{}, {}] } }),
    { id: "j", revision: 2, phase: "running", active: true, total: 10, next: 4, created: 3, skipped: 2, jobError: "", delta: null });
  assert.equal(s({ id: "j", status: "retry" }).phase, "queued");
  assert.equal(s({ id: "j", status: "cancelled" }).active, false);
  assert.equal(s({ id: "j", status: "failed", job: { error: "no_text" } }).jobError, "no_text");
  assert.equal(s({ status: "running" }).active, false, "no id: nothing to poll");
  assert.deepEqual(s({ id: "j", status: "done", job: { delta: { chunks_total: 5, chunks_changed: 2 } } }).delta, { total: 5, unchanged: 0, changed: 2, removed: 0 });
  assert.deepEqual(s({ id: "j", status: "done", chunks_total: 3, chunks_unchanged: 3 }).delta, { total: 3, unchanged: 3, changed: 0, removed: 0 });
});

test("delivery state names done, retry, failed and waiting deliveries", () => {
  const d = window.ArkismMessenger.deliveryState;
  assert.deepEqual(d({ status: "done", delivery: { http_status: 200, latency_ms: 40 } }), { label: "보냄", detail: "HTTP 200 · 40ms", tone: "" });
  const retry = d({ status: "retry", attempts: 2, next_attempt_at: "2026-11-05T10:00:00Z", delivery: { error: "timeout" } });
  assert.match(retry.label, /2번 실패/); assert.equal(retry.tone, "warn");
  assert.equal(d({ status: "failed", attempts: 5 }).label, "보내지 못했어요");
  assert.equal(d({ status: "pending" }).label, "보낼 차례 기다림");
  assert.ok(!("webhook.test" in window.ArkismMessenger.WEBHOOK_EVENTS) || window.ArkismMessenger.WEBHOOK_EVENTS["webhook.test"] === "시험 알림");
});

test("tag rows mark session tags; tag change refuses bad names and spots merges", () => {
  const rows = management.tagRows({ items: [{ tag: "work", count: 3 }, { tag: "home", count: 1 }, { tag: "session:abc", count: 9 }, { tag: "" }] });
  assert.equal(rows.find((r) => r.tag === "session:abc").system, true);
  for (const to of ["", "work", "session:x", "a b", "a,b", "x".repeat(65)]) assert.equal(management.tagChange(rows, "work", to), null, to);
  assert.equal(management.tagChange(rows, "session:abc", "x"), null);
  assert.deepEqual(management.tagChange(rows, "work", "home"), { from: "work", to: "home", merge: true, moving: 3, into: 1 });
  assert.equal(management.tagChange(rows, "work", "job").merge, false);
});

test("stat tiles read totals and leave unknown values empty", () => {
  const t = management.statTiles({ memories: { total: 12, by_status: { active: 9 }, by_kind: { user: 2, project: 7 } }, pending_candidates: 4, sources: { total: 3 } });
  assert.equal(t.active, 9); assert.equal(t.pending, 4); assert.equal(t.sources, 3);
  assert.deepEqual(t.kinds[0], ["project", 7]);
  assert.equal(management.statTiles({}).active, null);
});

function setup(routes) {
  const calls = [], notices = [];
  const ctx = {
    state: { scope: { workspace_id: "w" }, view: "settings", settingsTab: "messenger" }, ...house(doc),
    notice: (t) => notices.push(t), render: () => {},
    request: async (path, opts) => {
      calls.push([path, opts?.method || "GET", opts?.body && JSON.parse(opts.body)]);
      const key = Object.keys(routes).filter((k) => path.startsWith(k)).sort((a, b) => b.length - a.length)[0];
      const r = routes[key];
      return typeof r === "function" ? r(path, opts) : r;
    }
  };
  const m = window.ArkismMessenger(ctx);
  const side = [];
  const view = () => { side.length = 0; const [wrap] = m.tab({ side: (n) => side.push(n) }); const box = doc.createElement("div"); box.append(wrap, ...side); return box; };
  return { calls, notices, view };
}
const base = { "/v1/discord": { bots: [] }, "/v1/messengers": { destinations: [] } };
const hook = { id: "h1", url: "https://hooks.test/a", events: ["memory.approved"], active: true, revision: 2 };

test("webhook card: add, test, log with retry, turn off, delete only after confirm", async () => {
  let answer = false;
  globalThis.ask = async () => answer;
  const s = setup({ ...base, "/v1/webhooks": { items: [hook] }, "/v1/webhooks/h1/deliveries": { items: [{ id: "d1", status: "retry", attempts: 1, delivery: { event: "memory.approved", error: "timeout" } }] } });
  s.view(); await flush();
  let v = s.view();
  assert.ok(has(v, "https://hooks.test/a") && has(v, "1개 켜짐"));
  // add: bad url first, then a good one
  const url = all(v, "input").find((i) => i.type === "url");
  url.value = "http://plain"; await url.fire("input"); btn(v, "웹훅 추가").click(); await flush();
  assert.equal(s.notices.pop(), "https://로 시작하는 주소를 넣어 주세요.");
  url.value = "https://hooks.test/b"; await url.fire("input"); btn(v, "웹훅 추가").click(); await flush();
  const add = s.calls.find(([p, m]) => p === "/v1/webhooks" && m === "POST")[2];
  assert.equal(add.url, "https://hooks.test/b");
  assert.deepEqual(add.events, ["candidate.created", "memory.approved"]);
  assert.ok(add.secret.length >= 16);
  v = s.view();
  btn(v, "시험 보내기").click(); await flush();
  assert.ok(s.calls.some(([p, m]) => p === "/v1/webhooks/h1/test" && m === "POST"));
  v = s.view(); btn(v, "보낸 기록").click(); await flush();
  v = s.view(); assert.ok(has(v, "1번 실패"));
  btn(v, "끄기").click(); await flush();
  assert.deepEqual(s.calls.find(([, m]) => m === "PATCH")[2].active, false);
  v = s.view(); btn(v, "지우기").click(); await flush();
  assert.ok(!s.calls.some(([, m]) => m === "DELETE"), "declined confirm deletes nothing");
  answer = true; v = s.view(); btn(v, "지우기").click(); await flush();
  assert.ok(s.calls.some(([p, m]) => p.startsWith("/v1/webhooks/h1?") && m === "DELETE"));
});

test("webhook card: unsupported and denied servers explain themselves", async () => {
  const gone = new Error("not_found");
  let s = setup({ ...base, "/v1/webhooks": () => { throw gone; } });
  s.view(); await flush();
  assert.ok(has(s.view(), "아직 웹훅을 지원하지 않아요"));
  s = setup({ ...base, "/v1/webhooks": () => { throw new Error("access_denied"); } });
  s.view(); await flush();
  assert.ok(has(s.view(), "사람 계정으로 로그인했을 때만"));
});
