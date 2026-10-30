// 스킬 › 개선안과 버전 검토 (skill-review.js) and 고정 과제로 비교 실행 (skill-execution.js).
const test = require("node:test");
const assert = require("node:assert/strict");
require("./keep.js"); // the shared stale-reply and redraw guards the screens use
const { createDocument, house, improveSide, flush } = require("./testdata/views-dom.js");

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
  // The step card, the 평가 결과 card and the page's right panel sit under one root so a lookup sees all three.
  const side = improveSide(h), panel = window.ArkismSkillReview({ state, el: h.el, button: h.button, request, query: () => "q=1", reload: async () => { reloads++; }, execution, trial })(sk, mode, side), root = h.el("div");
  root.append(panel, side.extra, side.box); doc.body.append(root);
  return { doc, panel, root, state, calls, db, reloads: () => reloads };
}
const open = async (panel) => { panel.open = true; await panel.fire("toggle"); await flush(); };
const pick = async (panel, id) => { const s = panel.querySelector("select"); s.value = id; await s.fire("change"); await flush(); };

test("review shows evaluations, approves, rejects and rolls back", async () => {
  const { panel, root, calls, reloads } = setup();
  await open(panel);
  assert.match(text(root), /버전 7 · 기존|버전 1 · 사용 중/);
  await pick(panel, "v2");
  assert.match(text(root), /검토 대기/);
  assert.match(text(root), /버전 2 후보를 승인할까요\?/);
  assert.match(text(root), /옛 · 새/); // the changed-lines table
  assert.match(text(root), /점수 · 1 · 2/);
  assert.match(text(root), /비용 \/ 시간 · — · \$0\.50 \/ 0초/); // a report without baseline totals
  assert.match(root.querySelector(".extra .up")?.textContent || "", /^점수/); // the candidate beat the baseline
  assert.match(text(root), /모델 자기평가 · 실패/);
  assert.match(text(root), /exec:v2/);
  // another evaluation can be picked; the self-evaluation cannot approve
  const choose = root.querySelector(".extra select"); choose.value = "1"; await choose.fire("change");
  assert.equal(btn(root, "이 평가로 승인").disabled, true);
  choose.value = "0"; await choose.fire("change");
  await btn(root, "이 평가로 승인").click(); await flush();
  assert.ok(calls.includes("/v1/skill_versions/v2/transition"));
  // a failed transition offers a fresh read
  const ctx = setup({ fail: new Set(["/v1/skill_versions/v2/transition"]) });
  Object.assign(ctx.db["evaluations/e1"].evaluation.comparison_report, { baseline_cost: 0.24, baseline_duration_ms: 32000 });
  await open(ctx.panel); await pick(ctx.panel, "v2");
  assert.match(text(ctx.root), /비용 \/ 시간 · \$0\.24 \/ 32초 · \$0\.50 \/ 0초/);
  await btn(ctx.root, "후보 제외").click(); await flush();
  assert.match(text(ctx.root), /반영 결과를 확인하지 못했어요/);
  await btn(ctx.root, "다시 조회").click(); await flush();

  await pick(panel, "v1");
  await btn(root, "과거 판으로 복귀").click(); await flush();
  assert.equal(reloads(), 1);
  await pick(panel, "v3");
  assert.equal(btn(root, "이 버전으로 복귀").disabled, false);
  assert.match(text(root), /판정 없음/);
  await pick(panel, "v4");
  assert.match(text(root), /승인된 버전 4, 지금 적용할까요\?/);
  await btn(root, "이 버전 적용").click(); await flush();
  assert.equal(reloads(), 2);
  // act after the version view went stale does nothing
  const apply = btn(root, "이 버전 적용");
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
  await open(none.panel); assert.match(text(none.root), /연결된 평가가 없어요/);
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
  let went = -1;
  const side = improveSide(h, (i) => { went = i; }), target = h.el("label", "field", "비교 대상"), root = h.el("div");
  const box = make(version, () => { refreshed++; }, side, target);
  root.append(box, side.extra, side.box); doc.body.append(root);
  return { box, root, side, state, invokes, make, doc, h, refreshed: () => refreshed, went: () => went };
}
const openBox = async (box) => { box.open = true; await box.fire("toggle"); await flush(); };

test("execution explains when it cannot run", async () => {
  const a = exec({ tauri: null }); await openBox(a.box); assert.match(text(a.box), /데스크톱 앱에서/);
  const b = exec({ version: { id: "v", revision: 1, status: "approved" }, reply: () => ({}) }); await openBox(b.box); assert.match(text(b.box), /검토 대기 버전에서/);
  b.box.open = false; await b.box.fire("toggle"); assert.equal(b.box.querySelector("p"), null);
});

const PROFILE = { id: "p1", name: "기본", cases: 3, dataset_sha256: "d", environment_sha256: "e" };
test("execution lists past runs and checks their state", async () => {
  const items = [
    { status: "published", job_id: JOB, created_at: "c1", profile_id: "p1", report: { baseline_score: 1, candidate_score: 2 }, passed: true },
    { status: "awaiting_publication", job_id: "b".repeat(64), created_at: "c2", profile_id: "p1" },
  ];
  let next = { status: "running", job_id: "b".repeat(64) };
  const r = exec({ reply: (req) => req.action === "history" ? { authority: "data_only_untrusted", items } : req.action === "profiles" ? { authority: "data_only_untrusted", items: [PROFILE] } : { authority: "data_only_untrusted", ...next } });
  await openBox(r.box);
  // opening reads the history on its own and shows the newest run
  assert.match(text(r.root), /비교 대상/);
  assert.match(text(r.root), /c1 · 저장됨 · 통과 · 2/);
  assert.match(text(r.root), /c1 결과 저장됨/);
  assert.match(text(r.root), /저장된 평가\(c1 · 2\)로 후보를 검토할 수 있어요/);
  await btn(r.root, "저장된 평가로 검토").click(); assert.equal(r.went(), 2);
  await btn(r.root, "평가 다시 읽기").click(); assert.equal(r.refreshed(), 1);
  const input = r.box.querySelector("input");
  input.value = " bad host! "; await input.fire("input");
  assert.match(text(r.root), /서버를 다시 조회해 주세요/);
  await btn(r.root, "설정·기록 보기").click(); await flush();
  assert.match(text(r.root), /SSH 서버 이름을 확인/);
  input.value = "eval"; await input.fire("input");
  await btn(r.root, "설정·기록 보기").click(); await flush();
  await btn(r.root, "c2 · 저장 대기").click();
  assert.match(text(r.root), /서버에 저장하는 것만 남았어요/);
  await btn(r.root, "다시 저장 시도").click(); await flush();
  assert.match(text(r.root), /비교 실행 중이에요/);
  assert.match(text(r.root), /c2 · 실행 중/);
  next = { status: "weird" };
  await btn(r.root, "상태 확인").click(); await flush();
  assert.match(text(r.root), /결과를 확인하지 못했어요/);
});

test("execution starts a matched profile once and remembers the request", async () => {
  let profiles = [PROFILE, { id: "bad id", name: "x" }];
  const reply = (req) => {
    if (req.action === "history") return { authority: "data_only_untrusted", items: [] };
    if (req.action === "profiles") return { authority: "data_only_untrusted", items: profiles };
    return { authority: "data_only_untrusted", status: "interrupted_or_failed", job_id: JOB };
  };
  const r = exec({ reply });
  await openBox(r.box);
  assert.match(text(r.root), /아직 비교 기록이 없어요/);
  await btn(r.root, "비교 실행").click(); await flush();
  assert.match(text(r.root), /저장된 결과 없이/);
  const start = r.invokes.find(i => i.request.action === "start");
  assert.equal(start.request.server, "s");
  // a second box for the same version reuses the pending event id
  const again = r.make({ id: "v2", revision: 3, status: "candidate", comparison_plan: { dataset_sha256: "d", environment_sha256: "e" } }, () => {}, improveSide(r.h));
  r.doc.body.append(again); await openBox(again);
  assert.equal(again.querySelector("select").disabled, true);
  profiles = [];
  const none = exec({ reply }); await openBox(none.box);
  assert.match(text(none.root), /일치하는 평가 설정이 없어요/);
  assert.equal(btn(none.root, "비교 실행").disabled, true);
  profiles = "nope";
  await btn(none.root, "설정·기록 보기").click(); await flush();
  assert.match(text(none.root), /불러오지 못했어요/);
  const broken = exec({ reply: () => ({ authority: "data_only_untrusted", items: [{ status: "running", job_id: "short" }] }) });
  await openBox(broken.box);
  assert.match(text(broken.root), /불러오지 못했어요/);
});
