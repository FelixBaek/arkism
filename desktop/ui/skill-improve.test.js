// 스킬 › 개선하기 in one click (skill-improve.js): 시작 → make → compare → result card → 적용 starts the watch.
const test = require("node:test");
const assert = require("node:assert/strict");
const { createHash } = require("node:crypto");
require("./keep.js");
const { createDocument, house, improveSide, flush } = require("./testdata/views-dom.js");

Object.defineProperty(globalThis, "crypto", { configurable: true, value: { subtle: { digest: async (_, data) => { const h = createHash("sha256").update(Buffer.from(data)).digest(); return h.buffer.slice(h.byteOffset, h.byteOffset + h.byteLength); } } } });
globalThis.window = globalThis;
globalThis.document = createDocument();
globalThis.localStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };
require("./skill-generate.js");
require("./skill-improve.js");

const scope = { workspace_id: "w" }, hex = "a".repeat(64), job = "b".repeat(64);
const sk = { id: "sk1", source_path: "skills/a/SKILL.md", status: "active" };
const btn = (root, label) => root.querySelectorAll("button").find((b) => b.textContent.includes(label));
const idle = () => flush(24);
const refs = (n) => Array.from({ length: n }, (_, i) => ({ collection: "memory_candidates", id: `m${i}`, revision: 1 }));
const profile = { id: "fixed", name: "고정 과제", cases: 4, dataset_sha256: hex, environment_sha256: hex, default_plan: { min_score_gain: 0.05, max_cost: 1e6, max_duration_ms: 240000 } };
const report = (extra = {}) => ({ baseline_score: 0.8, candidate_score: 0.9, baseline_cost: 0.24, candidate_cost: 0.3, baseline_duration_ms: 32000, candidate_duration_ms: 41000, regression_passed: true, safety_passed: true, ...extra });

function setup({ history = ["v1"], versions = {}, jobs = [], lessons = refs(4), statusJob = { status: "published", passed: true, report: report(), evaluation: { id: "ev1" } }, watch = null, role = "human" } = {}) {
  const doc = createDocument(); globalThis.document = doc;
  const h = house(doc), state = { server: "s", token: "t", scope: { ...scope }, view: "skills", caps: { role }, defaults: { ssh_host: "box" } };
  const calls = [], invokes = [];
  let reloads = 0, skillRev = 2, active = "v1";
  const all = { v1: { id: "v1", scope, revision: 1, skill_id: "sk1", status: "active", text: "# A\nOld line" }, ...versions };
  const request = async (path, opt) => {
    const body = opt?.body ? JSON.parse(opt.body) : null;
    calls.push([path, body]);
    if (path.startsWith("/v1/reflections")) return { authority: "data_only_untrusted", items: lessons ? [{ scope, subject: "skill:sk1", interpretation: "배포 전 묻기", status: "candidate", candidate_refs: lessons }] : [] };
    if (path.startsWith("/v1/skills/sk1?")) return { id: "sk1", scope, revision: skillRev, active_version: active, version_history: history };
    if (path.startsWith("/v1/skill_versions/") && !body) return all[decodeURIComponent(path.split("/")[3].split("?")[0])];
    if (path === "/v1/skills/generate") { const v = { id: "v2", scope, revision: 1, skill_id: "sk1", status: "candidate", base_version: "v1", comparison_plan: body.comparison_plan, text: "# A\nNew line" }; all.v2 = v; history.push("v2"); return { record: v }; }
    if (path.endsWith("/transition")) { const id = path.split("/")[3]; all[id] = { ...all[id], status: body.status, revision: all[id].revision + 1 }; return {}; }
    if (path.endsWith("/apply")) { active = body.version_id; skillRev++; return {}; }
    if (path.endsWith("/watch")) return { state: "watching" };
    if (path.endsWith("/watch/check")) { if (!watch) throw new Error("not_found"); return watch; }
    if (path.endsWith("/rollback")) return {};
    if (path === "/v1/evaluations/summary") return { evaluation_refs: [] };
    throw new Error(`unexpected ${path}`);
  };
  const tauri = { invoke: async (cmd, arg) => {
    const { action } = arg.request; invokes.push(arg.request);
    const base = { authority: "data_only_untrusted" };
    if (action === "profiles") return { ...base, items: [profile] };
    if (action === "history") return { ...base, items: jobs };
    if (action === "start") return { ...base, job_id: job, status: "running" };
    return { ...base, job_id: job, ...statusJob };
  } };
  const generate = window.ArkismSkillGenerate({ state, el: h.el, button: h.button, request, query: (q) => (q ? `subject=${encodeURIComponent(q.subject)}` : "q=1"), reload: async () => {}, tauri });
  const improve = window.ArkismSkillImprove({ state, el: h.el, button: h.button, request, query: (q) => (q ? `subject=${encodeURIComponent(q.subject)}` : "q=1"), tauri, reload: async () => { reloads++; }, generate });
  const steps = [];
  const side = improveSide(h);
  const page = improve.page(sk, side, (i) => steps.push(i));
  doc.body.append(page, side.box);
  return { doc, page, side, calls, invokes, improve, steps, all, reloads: () => reloads, posts: (end) => calls.filter(([p, b]) => b && p.endsWith(end)) };
}

test("one 시작 makes the version, compares it and shows a better result; 적용 approves, applies and starts the watch", async () => {
  const ctx = setup();
  await idle();
  const card = ctx.page.querySelector(".v4s-better");
  assert.match(card.textContent, /이 스킬을 더 좋게 만들어 볼까요\?/);
  assert.match(card.textContent, /모아 둔 교훈 4개로 새 버전을 만들어요/);
  assert.match(card.textContent, /같은 문제 4개로/);
  assert.match(card.textContent, /길어야 약 9분/); // (2 × 4분 + 1분)
  assert.equal(ctx.posts("/generate").length, 0, "nothing paid before 시작");
  assert.ok(!ctx.invokes.some((r) => r.action === "start"));
  btn(card, "시작").click();
  await idle();
  const [[, gen]] = ctx.posts("/generate");
  assert.match(gen.event_id, /^imp-[a-f0-9]{48}$/);
  assert.equal(gen.lessons.length, 4);
  assert.deepEqual(gen.comparison_plan, { baseline_version: "v1", dataset_sha256: hex, environment_sha256: hex, ...profile.default_plan });
  const start = ctx.invokes.find((r) => r.action === "start");
  assert.deepEqual(start.candidate, { id: "v2", revision: 1 });
  assert.match(start.event_id, /^imp-[a-f0-9]{48}-0$/);
  assert.equal(start.profile_id, "fixed");
  assert.match(card.textContent, /새 버전이 더 나아요/);
  assert.match(card.textContent, /점수 · 80점 · 90점/);
  assert.match(card.textContent, /걸린 시간 · 32초 · 41초/);
  assert.match(card.textContent, /비용 · \$0\.24 · \$0\.30/);
  assert.match(card.textContent, /Old line · New line/);
  assert.match(ctx.page.querySelector(".v4s-better-more").textContent, /고정 과제 · 4개/);
  btn(ctx.page.querySelector(".v4s-better-more"), "비교 평가").click();
  assert.deepEqual(ctx.steps, [1]);
  btn(card, "적용").click();
  await idle();
  const order = ctx.calls.filter(([, b]) => b && !b.target && !b.event_id).map(([p]) => p.split("/").at(-1));
  assert.deepEqual(order, ["transition", "apply", "watch"]);
  assert.equal(ctx.posts("/transition")[0][1].validation_id, "ev1");
  assert.equal(ctx.posts("/watch")[0][1].version_id, "v2");
  assert.match(card.textContent, /새 버전을 적용했어요/);
  assert.equal(ctx.reloads(), 1);
});

test("the same 시작 twice sends the same event ids, so a repeat is a replay", async () => {
  const a = setup(); await idle(); btn(a.page, "시작").click(); await idle();
  const b = setup(); await idle(); btn(b.page, "시작").click(); await idle();
  assert.equal(a.posts("/generate")[0][1].event_id, b.posts("/generate")[0][1].event_id);
  assert.equal(a.invokes.find((r) => r.action === "start").event_id, b.invokes.find((r) => r.action === "start").event_id);
});

test("a worse saved comparison resumes on its card, offers no 적용, and 버리기 rejects the candidate", async () => {
  const v2 = { id: "v2", scope, revision: 3, skill_id: "sk1", status: "candidate", base_version: "v1", comparison_plan: { dataset_sha256: hex, environment_sha256: hex }, text: "# A\nNew line" };
  const ctx = setup({ history: ["v1", "v2"], versions: { v2 }, jobs: [{ job_id: job, status: "published", passed: false, report: report({ candidate_score: 0.7 }) }] });
  await idle();
  const card = ctx.page.querySelector(".v4s-better");
  assert.match(card.textContent, /새 버전이 더 나빠요/);
  assert.equal(ctx.posts("/generate").length, 0);
  assert.equal(btn(card, "적용"), undefined);
  btn(card, "버리기").click();
  await idle();
  assert.deepEqual(ctx.posts("/transition")[0][1], { scope, expected_revision: 3, status: "rejected" });
  assert.match(card.textContent, /새 버전을 버렸어요/);
});

test("a similar result, an interrupted run asks before paying again, and missing lessons block 시작", async () => {
  const v2 = { id: "v2", scope, revision: 1, skill_id: "sk1", status: "candidate", base_version: "v1", comparison_plan: { dataset_sha256: hex, environment_sha256: hex }, text: "x" };
  const same = setup({ history: ["v1", "v2"], versions: { v2 }, jobs: [{ job_id: job, status: "published", passed: false, report: report({ candidate_score: 0.81 }) }] });
  await idle();
  assert.match(same.page.textContent, /두 버전이 비슷해요/);
  const stopped = setup({ history: ["v1", "v2"], versions: { v2: { ...v2 } }, jobs: [{ job_id: job, status: "interrupted_or_failed" }] });
  await idle();
  assert.match(stopped.page.textContent, /새 버전을 만들어 뒀어요/); // the intro, comparison only
  btn(stopped.page, "시작").click();
  await idle();
  assert.match(stopped.invokes.find((r) => r.action === "start").event_id, /-1$/);
  const none = setup({ lessons: refs(2) });
  await idle();
  assert.match(none.page.textContent, /아직 시작할 수 없어요/);
  assert.equal(btn(none.page, "시작"), undefined);
  const bot = setup({ role: "service" });
  await idle();
  assert.match(bot.page.textContent, /관리 계정의 데스크톱 앱/);
});

test("status: the watch line with 되돌리기, a finished rollback, and the lesson hint", async () => {
  const until = "2026-11-06T00:00:00Z";
  const w = setup({ watch: { authority: "data_only_untrusted", state: "watching", until, previous_version: "v1", can_rollback: true } });
  const box = w.improve.status(sk, () => {});
  w.doc.body.append(box);
  await idle();
  assert.match(box.textContent, /새 버전을 지켜보는 중이에요 · /);
  btn(box, "되돌리기").click();
  await idle();
  assert.deepEqual(w.posts("/rollback")[0][1], { scope, expected_revision: 2, version_id: "v1" });
  const blocked = setup({ watch: { authority: "data_only_untrusted", state: "watching", until, previous_version: "v1", can_rollback: false } });
  const b = blocked.improve.status(sk, () => {}); await idle();
  assert.equal(btn(b, "되돌리기"), undefined);
  assert.match(b.textContent, /새 버전을 지켜보는 중이에요/);
  const stuck = setup({ watch: { authority: "data_only_untrusted", state: "rollback_blocked" } });
  const k = stuck.improve.status(sk, () => {}); await idle();
  assert.match(k.textContent, /저절로 되돌리지 못했어요/);
  const back = setup({ watch: { state: "rolled_back" } });
  const r = back.improve.status(sk, () => {}); await idle();
  assert.match(r.textContent, /예전 버전으로 되돌렸어요/);
  let opened = 0;
  const hint = setup().improve.status(sk, () => opened++); await idle();
  assert.equal(hint.hidden, false);
  btn(hint, "더 좋게 만들 수 있어요").click();
  assert.equal(opened, 1);
  const quiet = setup({ lessons: null }).improve.status(sk, () => {}); await idle();
  assert.equal(quiet.hidden, true);
});
