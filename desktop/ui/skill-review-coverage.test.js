// 스킬 › 개선안과 버전 검토 (skill-review.js) and 고정 과제로 비교 실행 (skill-execution.js).
const test = require("node:test");
const assert = require("node:assert/strict");
const { createDocument, house, flush } = require("./testdata/views-dom.js");

globalThis.window = globalThis;
globalThis.document = createDocument();
require("./skill-review.js");
require("./skill-execution.js");

const scope = { workspace_id: "w" };
const baseState = () => ({ server: "s/", token: "t", scope: { ...scope }, view: "skills", selected: "skill:sk1", caps: { role: "human" }, defaults: { ssh_host: "eval" } });
const btn = (root, label) => root.querySelectorAll("button").find(b => b.textContent.includes(label));
const text = (n) => n.textContent;

function records() {
  const ev = (id, target, extra = {}) => ({ id, revision: 1, scope, target: { collection: "skill_versions", id: target }, status: "active", text: "평가", ...extra });
  return {
    "skills/sk1": { id: "sk1", revision: 2, scope, version_history: ["v1", "v2", "v3", "v4", "v5", "bad", "nob"], active_version: "v1" },
    "skill_versions/v1": { id: "v1", revision: 1, scope, skill_id: "sk1", text: "옛", status: "superseded" },
    "skill_versions/v2": { id: "v2", revision: 3, scope, skill_id: "sk1", text: "새", status: "candidate", base_version: "v1", validation_id: "e2", created_by: "alice",
      comparison_plan: { min_score_gain: 0.1, max_cost: 1, max_duration_ms: 9 } },
    "skill_versions/v3": { id: "v3", revision: 1, scope, skill_id: "sk1", text: "", status: "superseded", validation_id: "e4" },
    "skill_versions/v4": { id: "v4", revision: 1, scope, skill_id: "sk1", text: "x", status: "approved" },
    "skill_versions/v5": { id: "v5", revision: 1, scope, skill_id: "sk1", text: "x", status: "candidate", base_version: "bad" },
    "skill_versions/bad": { id: "bad", revision: 1, scope, skill_id: "other", text: "x", status: "candidate" },
    "skill_versions/nob": { id: "nob", revision: 1, scope, skill_id: "sk1", text: "x", status: "candidate", base_version: "gone" },
    "evaluations/e1": ev("e1", "v2", { created_by: "bob", evaluation: { passed: true, comparison_report: { baseline_score: 1, candidate_score: 2, candidate_cost: 0.5, candidate_duration_ms: 3, regression_passed: true, safety_passed: false } } }),
    "evaluations/e2": ev("e2", "v2", { evaluation: { eval_type: "model_self", passed: false, comparison_report: { regression_passed: false, safety_passed: true } } }),
    "evaluations/e3": ev("e3", "v1", { evaluation: { passed: true } }),
    "evaluations/e4": ev("e4", "v3", { evaluation: {} }),
  };
}

function setup({ mode = "", sk = { id: "sk1" }, fail = new Set(), summary } = {}) {
  const doc = createDocument(); globalThis.document = doc;
  const h = house(doc), state = baseState(), db = records(), calls = [];
  let reloads = 0;
  const request = async (path, opt) => {
    calls.push(path);
    if (fail.has(path.split("?")[0])) throw new Error("boom");
    if (path === "/v1/evaluations/summary") {
      const target = JSON.parse(opt.body).target;
      if (summary) return summary(target);
      const refs = { v2: [{ collection: "evaluations", id: "e1", revision: 1 }], v1: [{ collection: "evaluations", id: "e3", revision: 1 }], v3: [{ collection: "evaluations", id: "e4", revision: 1 }] }[target.id] || [];
      return { authority: "data_only_untrusted", target, evaluation_refs: refs };
    }
    if (opt?.method === "POST") return {};
    const key = path.slice(4).split("?")[0].split("/").map(decodeURIComponent).join("/");
    const r = db[key]; if (!r) throw new Error("not_found"); return r;
  };
  const execution = (v) => h.el("div", "exec", `exec:${v.id}`), trial = (s, v) => h.el("details", "trial", `trial:${v}`);
  const panel = window.ArkismSkillReview({ state, el: h.el, button: h.button, request, query: () => "q=1", reload: async () => { reloads++; }, execution, trial })(sk, mode);
  doc.body.append(panel);
  return { doc, panel, state, calls, db, reloads: () => reloads };
}
const open = async (panel) => { panel.open = true; await panel.fire("toggle"); await flush(); };
const pick = async (panel, id) => { const s = panel.querySelector("select"); s.value = id; await s.fire("change"); await flush(); };

test("review shows evaluations, approves, rejects and rolls back", async () => {
  const { panel, calls, reloads } = setup();
  await open(panel);
  assert.match(text(panel), /버전 7 · 기존|버전 1 · 사용 중/);
  await pick(panel, "v2");
  assert.match(text(panel), /검토 대기/);
  assert.match(text(panel), /기존 점수 1 → 후보 점수 2/);
  assert.match(text(panel), /모델 자기평가/);
  assert.match(text(panel), /exec:v2/);
  await btn(panel, "이 평가로 후보 승인").click(); await flush();
  assert.ok(calls.includes("/v1/skill_versions/v2/transition"));
  // a failed transition offers a fresh read
  const ctx = setup({ fail: new Set(["/v1/skill_versions/v2/transition"]) });
  await open(ctx.panel); await pick(ctx.panel, "v2");
  await btn(ctx.panel, "후보 제외").click(); await flush();
  assert.match(text(ctx.panel), /반영 결과를 확인하지 못했어요/);
  await btn(ctx.panel, "다시 조회").click(); await flush();

  await pick(panel, "v1");
  await btn(panel, "이 평가로 과거판 복귀").click(); await flush();
  assert.equal(reloads(), 1);
  await pick(panel, "v3");
  assert.ok(btn(panel, "이 버전으로 복귀"));
  assert.match(text(panel), /판정 없음/);
  await pick(panel, "v4");
  await btn(panel, "이 버전 적용").click(); await flush();
  assert.equal(reloads(), 2);
  // act after the version view went stale does nothing
  const apply = btn(panel, "이 버전 적용");
  await pick(panel, "v1"); await apply.click();
  assert.equal(reloads(), 2);
});

test("broken versions, baselines, summaries and evaluations show a retry", async () => {
  const { panel } = setup();
  await open(panel);
  for (const id of ["bad", "v5", "nob"]) {
    await pick(panel, id);
    assert.match(text(panel), /버전이나 평가를 읽지 못했어요/, id);
  }
  await btn(panel, "다시 조회").click(); await flush();
  const s = setup({ summary: (target) => ({ authority: "x", target }) });
  await open(s.panel); assert.match(text(s.panel), /버전이나 평가를 읽지 못했어요/);
  const e = setup({ summary: (target) => ({ authority: "data_only_untrusted", target, evaluation_refs: [{ collection: "evaluations", id: "e1", revision: 1 }] }) });
  e.db["skills/sk1"].version_history = ["v1"];
  await open(e.panel); assert.match(text(e.panel), /버전이나 평가를 읽지 못했어요/);
  const ex = setup({ summary: (target) => ({ authority: "data_only_untrusted", target, evaluation_refs: [{ collection: "evaluations", id: "e1", revision: 1 }] }) });
  ex.db["skills/sk1"].version_history = ["v2"];
  ex.db["evaluations/e1"].expires_at = "2000-01-01T00:00:00Z";
  await open(ex.panel); assert.match(text(ex.panel), /버전이나 평가를 읽지 못했어요/);
  const none = setup({ summary: (target) => ({ authority: "data_only_untrusted", target, evaluation_refs: [] }) });
  none.db["skills/sk1"].version_history = ["v4"];
  none.state.caps = null;
  await open(none.panel); assert.match(text(none.panel), /연결된 평가가 없어요/);
});

test("bad skill references, histories and collapsing", async () => {
  const bad = setup({ sk: { id: ".." } });
  await open(bad.panel); assert.match(text(bad.panel), /버전 목록을 읽지 못했어요/);
  const dup = setup(); dup.db["skills/sk1"].version_history = ["v1", "v1"];
  await open(dup.panel); assert.match(text(dup.panel), /버전 목록을 읽지 못했어요/);
  const changed = setup(); changed.db["skills/sk1"].revision = 0;
  await open(changed.panel); assert.match(text(changed.panel), /버전 목록을 읽지 못했어요/);
  const quiet = setup(); quiet.panel.open = true; const p = quiet.panel.fire("toggle");
  quiet.panel.open = false; await quiet.panel.fire("toggle"); await p; await flush();
  assert.equal(quiet.panel.querySelector("p"), null);
});

test("compare and trial modes open one part", async () => {
  const c = setup({ mode: "compare" }); assert.equal(c.panel.open, true);
  c.db["skills/sk1"].version_history = ["v2"];
  await c.panel.fire("toggle"); await flush();
  assert.match(text(c.panel), /exec:v2/);
  assert.equal(c.panel.querySelector(".exec").open, true);
  const t = setup({ mode: "trial" }); await t.panel.fire("toggle"); await flush();
  assert.match(text(t.panel), /trial:nob/);
  const r = setup({ mode: "review" }); r.db["skills/sk1"].version_history = ["v2"]; await r.panel.fire("toggle"); await flush();
  assert.doesNotMatch(text(r.panel), /exec:/);
});

// ---- skill-execution.js
const JOB = "a".repeat(64);
function exec({ version, tauri, caps = { role: "human" }, reply } = {}) {
  const doc = createDocument(); globalThis.document = doc;
  const h = house(doc), state = baseState(); state.caps = caps;
  const invokes = [];
  tauri = tauri === undefined ? { invoke: async (cmd, arg) => { invokes.push(arg); return reply(arg.request, arg.host); } } : tauri;
  const make = window.ArkismSkillExecution({ state, el: h.el, button: h.button, tauri });
  let refreshed = 0;
  version ||= { id: "v2", revision: 3, status: "candidate", comparison_plan: { dataset_sha256: "d", environment_sha256: "e" } };
  const box = make(version, () => { refreshed++; });
  doc.body.append(box);
  return { box, state, invokes, make, doc, h, refreshed: () => refreshed };
}
const openBox = async (box) => { box.open = true; await box.fire("toggle"); await flush(); };

test("execution explains when it cannot run", async () => {
  const a = exec({ tauri: null }); await openBox(a.box); assert.match(text(a.box), /데스크톱 앱에서/);
  const b = exec({ version: { id: "v", revision: 1, status: "approved" }, reply: () => ({}) }); await openBox(b.box); assert.match(text(b.box), /검토 대기 버전에서/);
  b.box.open = false; await b.box.fire("toggle"); assert.equal(b.box.querySelector("p"), null);
});

test("execution lists past runs and checks their state", async () => {
  const items = [
    { status: "published", job_id: JOB, created_at: "c1", profile_id: "p1", report: { baseline_score: 1, candidate_score: 2 }, passed: true },
    { status: "awaiting_publication", job_id: JOB, created_at: "c2", profile_id: "p1" },
  ];
  let next = { status: "running", job_id: JOB };
  const r = exec({ reply: (req) => req.action === "history" ? { authority: "data_only_untrusted", items } : req.action === "fail" ? null : { authority: "data_only_untrusted", ...next } });
  await openBox(r.box);
  const input = r.box.querySelector("input");
  input.value = " bad host! "; await input.fire("input");
  await btn(r.box, "평가 설정과").click(); await flush();
  assert.match(text(r.box), /SSH 서버 이름을 확인/);
  input.value = "eval"; await input.fire("input");
  await btn(r.box, "평가 설정과").click(); await flush();
  assert.match(text(r.box), /비교 기준 통과 · 기존 점수 1 → 후보 점수 2/);
  await btn(r.box, "평가 다시 읽기").click(); assert.equal(r.refreshed(), 1);
  const select = r.box.querySelector("select"); select.value = "1"; await select.fire("change");
  await btn(r.box, "저장 재시도").click(); await flush();
  assert.match(text(r.box), /비교 실행 중이에요/);
  next = { status: "weird" };
  await btn(r.box, "상태 확인").click(); await flush();
  assert.match(text(r.box), /결과를 확인하지 못했어요/);
});

test("execution starts a matched profile once and remembers the request", async () => {
  let profiles = [{ id: "p1", name: "기본", cases: 3, dataset_sha256: "d", environment_sha256: "e" }, { id: "bad id", name: "x" }];
  const reply = (req) => {
    if (req.action === "history") return { authority: "data_only_untrusted", items: [] };
    if (req.action === "profiles") return { authority: "data_only_untrusted", items: profiles };
    return { authority: "data_only_untrusted", status: "interrupted_or_failed", job_id: JOB };
  };
  const r = exec({ reply });
  await openBox(r.box);
  await btn(r.box, "평가 설정과").click(); await flush();
  await btn(r.box, "비교 실행").click(); await flush();
  assert.match(text(r.box), /저장된 결과 없이/);
  const start = r.invokes.find(i => i.request.action === "start");
  assert.equal(start.request.server, "s");
  // a second box for the same version reuses the pending event id
  const again = r.make({ id: "v2", revision: 3, status: "candidate", comparison_plan: { dataset_sha256: "d", environment_sha256: "e" } }, () => {});
  r.doc.body.append(again); await openBox(again);
  await btn(again, "평가 설정과").click(); await flush();
  assert.equal(again.querySelector("select").disabled, true);
  profiles = [];
  const none = exec({ reply }); await openBox(none.box);
  await btn(none.box, "평가 설정과").click(); await flush();
  assert.match(text(none.box), /일치하는 평가 설정이 없어요/);
  profiles = "nope";
  await btn(none.box, "평가 설정과").click(); await flush();
  assert.match(text(none.box), /불러오지 못했어요/);
  const broken = exec({ reply: () => ({ authority: "data_only_untrusted", items: [{ status: "running", job_id: "short" }] }) });
  await openBox(broken.box); await btn(broken.box, "평가 설정과").click(); await flush();
  assert.match(text(broken.box), /불러오지 못했어요/);
});
